package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"discord-free-cloud/frontend"
	"discord-free-cloud/internal/catalog"
	"discord-free-cloud/internal/crypto"
	"discord-free-cloud/internal/db"
	"discord-free-cloud/internal/discord"
	"discord-free-cloud/internal/downloader"
	"discord-free-cloud/internal/server"
	"discord-free-cloud/internal/syswin"
	"discord-free-cloud/internal/uploader"

	"golang.org/x/sys/windows"
)

func isConsole(fd uintptr) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(fd), &mode) == nil
}

func readPasswordConsole(prompt string) (string, error) {
	fmt.Print(prompt)
	hStdin := windows.Handle(os.Stdin.Fd())
	var oldMode uint32
	if err := windows.GetConsoleMode(hStdin, &oldMode); err == nil {
		newMode := oldMode &^ windows.ENABLE_ECHO_INPUT
		if err := windows.SetConsoleMode(hStdin, newMode); err == nil {
			defer func() {
				_ = windows.SetConsoleMode(hStdin, oldMode)
				fmt.Println()
			}()
		}
	}

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func main() {
	portFlag := flag.Int("port", 8080, "web server port")
	dbPathFlag := flag.String("db", "", "path to storage database file")
	noBrowserFlag := flag.Bool("no-browser", false, "skip opening browser")
	noPromptFlag := flag.Bool("no-prompt", false, "skip console password prompt")
	passwordFlag := flag.String("password", "", "master password flag")
	chunkSizeFlag := flag.Int("chunk-size", 8*1024*1024, "part size in bytes")
	workersFlag := flag.Int("workers", 6, "upload worker count")
	flag.Parse()

	dbPath := *dbPathFlag
	if dbPath == "" {
		appData := os.Getenv("APPDATA")
		if appData != "" {
			dbPath = filepath.Join(appData, "DiscordFreeCloud", "drive.db")
			oldPath := filepath.Join(appData, "DiscordStorageEngine", "drive.db")
			if _, err := os.Stat(dbPath); os.IsNotExist(err) {
				if _, err := os.Stat(oldPath); err == nil {
					_ = os.MkdirAll(filepath.Dir(dbPath), 0755)
					_ = os.Rename(oldPath, dbPath)
				}
			}
		} else {
			dbPath = filepath.Join("data", "drive.db")
		}
	}

	fmt.Println("Discord Free Cloud by zyrexdz")
	fmt.Printf("Database: %s\n", dbPath)

	database, err := db.InitDB(dbPath)
	if err != nil {
		log.Fatalf("could not start database: %v", err)
	}
	defer database.Close()

	if err := database.ResetIncomplete(); err != nil {
		log.Printf("could not reset unfinished parts: %v", err)
	}

	settings, _ := database.GetAllSettings()
	botToken := settings["bot_token"]
	chunkSize := *chunkSizeFlag
	if cs, ok := settings["chunk_size_bytes"]; ok {
		if val, err := strconv.Atoi(cs); err == nil && val > 0 {
			chunkSize = val
		}
	}

	workers := *workersFlag
	if w, ok := settings["worker_count"]; ok {
		if val, err := strconv.Atoi(w); err == nil && val > 0 {
			workers = val
		}
	}

	passHash := settings["master_pass_hash"]
	saltHex := settings["master_salt_hex"]
	var salt []byte
	if saltHex != "" {
		salt, _ = hex.DecodeString(saltHex)
	}

	var masterKey []byte

	if *passwordFlag != "" {
		if len(salt) == 0 {
			salt, _ = crypto.GenerateSalt()
			_ = database.SetSetting("master_salt_hex", hex.EncodeToString(salt))
		}
		key := crypto.DeriveKey(*passwordFlag, salt)
		computedHash := crypto.ComputeSHA256(key)
		if passHash != "" && passHash != computedHash {
			log.Fatalf("incorrect encryption password")
		}
		if passHash == "" {
			_ = database.SetSetting("master_pass_hash", computedHash)
		}
		masterKey = key
		fmt.Println("Password verified. Drive unlocked.")
	} else if !*noPromptFlag && isConsole(os.Stdin.Fd()) {
		if passHash != "" {
			for attempts := 1; attempts <= 3; attempts++ {
				pass, err := readPasswordConsole(fmt.Sprintf("Enter password (attempt %d/3): ", attempts))
				if err != nil || pass == "" {
					fmt.Println("No password entered. Drive locked.")
					break
				}
				key := crypto.DeriveKey(pass, salt)
				if crypto.ComputeSHA256(key) == passHash {
					masterKey = key
					fmt.Println("Password accepted. Drive unlocked.")
					break
				}
				fmt.Println("Incorrect password, please try again.")
			}
		} else {
			fmt.Println("Set an encryption password for your storage:")
			pass1, err1 := readPasswordConsole("Password: ")
			if err1 == nil && pass1 != "" {
				pass2, err2 := readPasswordConsole("Confirm password: ")
				if err2 == nil && pass1 == pass2 {
					salt, _ = crypto.GenerateSalt()
					masterKey = crypto.DeriveKey(pass1, salt)
					_ = database.SetSetting("master_salt_hex", hex.EncodeToString(salt))
					_ = database.SetSetting("master_pass_hash", crypto.ComputeSHA256(masterKey))
					fmt.Println("Password saved. Drive unlocked.")
				} else {
					fmt.Println("Passwords did not match. Set password in browser.")
				}
			} else {
				fmt.Println("Skipped. Set password in browser.")
			}
		}
	}

	discordClient := discord.NewClient(botToken)
	uploaderEngine := uploader.NewEngine(database, discordClient, masterKey, chunkSize, workers)
	downloaderEngine := downloader.NewEngine(database, discordClient, masterKey, workers)
	catalogManager := catalog.New(database, discordClient, masterKey)

	srv := server.NewServer(
		database,
		discordClient,
		uploaderEngine,
		downloaderEngine,
		catalogManager,
		frontend.Assets,
	)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	targetPort := *portFlag
	var listener net.Listener
	for try := 0; try < 10; try++ {
		addr := fmt.Sprintf("127.0.0.1:%d", targetPort+try)
		l, err := net.Listen("tcp", addr)
		if err == nil {
			listener = l
			targetPort += try
			break
		}
	}

	if listener == nil {
		log.Fatalf("could not find an open port starting from %d", *portFlag)
	}

	serverURL := fmt.Sprintf("http://127.0.0.1:%d", targetPort)
	log.Printf("Server running at %s\n", serverURL)

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server stopped: %v", err)
		}
	}()

	if !*noBrowserFlag {
		go func() {
			time.Sleep(400 * time.Millisecond)
			_ = syswin.OpenBrowser(serverURL)
		}()
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	<-stopChan
	fmt.Println("\nClosing app...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = httpServer.Shutdown(ctx)
	fmt.Println("Discord Free Cloud closed.")
}
