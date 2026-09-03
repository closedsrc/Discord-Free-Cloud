package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"discord-free-cloud/frontend"
	"discord-free-cloud/internal/catalog"
	"discord-free-cloud/internal/crypto"
	"discord-free-cloud/internal/db"
	"discord-free-cloud/internal/discord"
	"discord-free-cloud/internal/downloader"
	"discord-free-cloud/internal/server"
	"discord-free-cloud/internal/uploader"
)

func main() {
	portFlag := flag.Int("port", 8080, "web server port")
	dbURLFlag := flag.String("db-url", "", "PostgreSQL connection string (env DATABASE_URL is used when empty)")
	noBrowserFlag := flag.Bool("no-browser", false, "skip opening browser")
	passwordFlag := flag.String("password", "", "master password flag (env MASTER_PASSWORD is used when empty)")
	chunkSizeFlag := flag.Int("chunk-size", 8*1024*1024, "part size in bytes")
	workersFlag := flag.Int("workers", 6, "upload worker count")
	flag.Parse()

	// Retained so scripts that pass -no-browser keep working on headless hosts.
	_ = noBrowserFlag

	dbURL := *dbURLFlag
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" {
		log.Fatal("no database configured: pass -db-url or set DATABASE_URL")
	}

	fmt.Println("Discord Free Cloud by zyrexdz")
	fmt.Println("Database: PostgreSQL (configured via -db-url / DATABASE_URL)")

	database, err := db.InitDB(dbURL)
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

	masterPassword := *passwordFlag
	if masterPassword == "" {
		masterPassword = os.Getenv("MASTER_PASSWORD")
	}

	var masterKey []byte

	if masterPassword != "" {
		if len(salt) == 0 {
			salt, _ = crypto.GenerateSalt()
			_ = database.SetSetting("master_salt_hex", hex.EncodeToString(salt))
		}
		key := crypto.DeriveKey(masterPassword, salt)
		computedHash := crypto.ComputeSHA256(key)
		if passHash != "" && passHash != computedHash {
			log.Fatalf("incorrect encryption password")
		}
		if passHash == "" {
			_ = database.SetSetting("master_pass_hash", computedHash)
		}
		masterKey = key
		fmt.Println("Password verified. Drive unlocked.")
	} else {
		fmt.Println("No password flag provided. Drive starts locked; set a password in the browser.")
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
	handler := srv.RegisterRoutes(mux)

	// Seed API tokens from the (root-only) environment before the first request
	// can arrive, so headless installs are scriptable from the start.
	if srv.EnsureSeededTokens() {
		log.Printf("API token auth enabled (seeded from DFC_API_TOKEN_WRITE/DFC_API_TOKEN_READ)")
	}

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
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server stopped: %v", err)
		}
	}()

	// Provision on boot: seed config from env (falls back to stored settings),
	// register the bot's servers, and make sure storage pools exist.
	bootToken := os.Getenv("BOT_TOKEN")
	bootGuild := os.Getenv("PRIMARY_GUILD_ID")
	bootCategory := os.Getenv("BASE_CATEGORY_ID")
	if bootToken == "" {
		bootToken = settings["bot_token"]
	}
	if bootGuild == "" {
		bootGuild = settings["guild_id"]
	}
	if bootCategory == "" {
		bootCategory = settings["base_category_id"]
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := srv.Provision(ctx, bootToken, bootGuild, bootCategory); err != nil {
			log.Printf("startup provisioning failed: %v", err)
		} else {
			log.Printf("startup provisioning complete")
		}
	}()

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	<-stopChan
	fmt.Println("\nClosing app...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = httpServer.Shutdown(ctx)
	fmt.Println("Discord Free Cloud closed.")
}
