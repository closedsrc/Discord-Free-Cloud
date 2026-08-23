package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"discord-free-cloud/internal/catalog"
	"discord-free-cloud/internal/crypto"
	"discord-free-cloud/internal/db"
	"discord-free-cloud/internal/discord"
	"discord-free-cloud/internal/downloader"
	"discord-free-cloud/internal/storage"
	"discord-free-cloud/internal/syswin"
	"discord-free-cloud/internal/uploader"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Server struct {
	db         *db.Database
	discord    *discord.Client
	uploader   *uploader.Engine
	downloader *downloader.Engine
	catalog    *catalog.SyncManager
	frontendFS fs.FS

	wsMu      sync.Mutex
	wsClients map[*websocket.Conn]bool
}

func NewServer(database *db.Database, discordClient *discord.Client, upEngine *uploader.Engine, downEngine *downloader.Engine, catManager *catalog.SyncManager, frontend fs.FS) *Server {
	s := &Server{
		db:         database,
		discord:    discordClient,
		uploader:   upEngine,
		downloader: downEngine,
		catalog:    catManager,
		frontendFS: frontend,
		wsClients:  make(map[*websocket.Conn]bool),
	}

	telemetryHandler := func(event uploader.TelemetryEvent) {
		s.broadcastJSON(map[string]any{
			"type": "telemetry",
			"data": event,
		})
	}

	upEngine.AddTelemetryListener(telemetryHandler)
	downEngine.AddTelemetryListener(telemetryHandler)

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			mem := syswin.GetMemoryTelemetry()
			diskSpace, _ := syswin.GetDiskFreeSpace(".")
			s.broadcastJSON(map[string]any{
				"type": "system_stats",
				"data": map[string]any{
					"memory":              mem,
					"disk_space":          diskSpace,
					"total_storage_bytes": s.db.GetTotalStorageBytes(),
					"total_files":         s.db.GetFileCount(),
				},
			})
		}
	}()

	return s
}

func (s *Server) broadcastJSON(payload any) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	for client := range s.wsClients {
		if err := client.WriteMessage(websocket.TextMessage, data); err != nil {
			client.Close()
			delete(s.wsClients, client)
		}
	}
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/auto-setup", s.handleAutoSetup)
	mux.HandleFunc("/api/channels", s.handleChannels)
	mux.HandleFunc("/api/channels/clean", s.handleCleanChannels)
	mux.HandleFunc("/api/files", s.handleFiles)
	mux.HandleFunc("/api/files/create_text", s.handleCreateTextFile)
	mux.HandleFunc("/api/folders/create", s.handleCreateFolder)
	mux.HandleFunc("/api/upload/file", s.handleUploadFile)
	mux.HandleFunc("/api/upload/dir", s.handleUploadDir)
	mux.HandleFunc("/api/download", s.handleDownload)
	mux.HandleFunc("/api/download/file", s.handleStreamDownload)
	mux.HandleFunc("/api/download/check", s.handleDownloadCheck)
	mux.HandleFunc("/api/delete", s.handleDelete)
	mux.HandleFunc("/api/catalog/sync", s.handleCatalogSync)
	mux.HandleFunc("/api/catalog/restore", s.handleCatalogRestore)
	mux.HandleFunc("/api/jobs", s.handleJobs)
	mux.HandleFunc("/api/jobs/cancel", s.handleJobCancel)
	mux.HandleFunc("/api/bots", s.handleBots)
	mux.HandleFunc("/api/bots/add", s.handleAddBot)
	mux.HandleFunc("/api/bots/delete", s.handleDeleteBot)
	mux.HandleFunc("/api/bots/test", s.handleTestBot)
	mux.HandleFunc("/api/servers", s.handleServers)
	mux.HandleFunc("/api/servers/channels", s.handleServerChannels)
	mux.HandleFunc("/api/servers/health", s.handleServerHealth)
	mux.HandleFunc("/api/servers/setup_channels", s.handleServerSetupChannels)
	mux.HandleFunc("/api/stats", s.handleStatus)
	mux.HandleFunc("/api/upload", s.handleUploadFile)
	mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/api/auth/unlock", s.handleAuthUnlock)
	mux.HandleFunc("/api/auth/set_password", s.handleAuthSetPassword)

	fileServer := http.FileServer(http.FS(s.frontendFS))
	mux.Handle("/", fileServer)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.wsMu.Lock()
	s.wsClients[conn] = true
	s.wsMu.Unlock()

	defer func() {
		s.wsMu.Lock()
		delete(s.wsClients, conn)
		s.wsMu.Unlock()
		conn.Close()
	}()

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	channels, _ := s.db.GetStorageChannels()
	fileCount := s.db.GetFileCount()
	totalStorageBytes := s.db.GetTotalStorageBytes()
	activeJobs, _ := s.db.GetActiveJobs()
	diskSpace, _ := syswin.GetDiskFreeSpace(".")
	mem := syswin.GetMemoryTelemetry()

	botToken, _ := s.db.GetSetting("bot_token")
	guildID, _ := s.db.GetSetting("guild_id")
	lastSync, _ := s.db.GetSetting("last_catalog_sync")
	passHash, _ := s.db.GetSetting("master_pass_hash")

	var diskFree int64 = 0
	if diskSpace != nil {
		diskFree = int64(diskSpace.FreeBytes)
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"is_configured":       botToken != "" && guildID != "",
		"channels_count":      len(channels),
		"files_count":         fileCount,
		"total_files":         fileCount,
		"total_storage_bytes": totalStorageBytes,
		"disk_free_bytes":     diskFree,
		"active_jobs_count":   len(activeJobs),
		"disk_space":          diskSpace,
		"memory":              mem,
		"last_catalog_sync":   lastSync,
		"has_password":        passHash != "",
		"is_unlocked":         s.uploader.HasMasterKey(),
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		settings, err := s.db.GetAllSettings()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if token, ok := settings["bot_token"]; ok && len(token) > 10 {
			settings["bot_token_masked"] = token[:4] + "..." + token[len(token)-4:]
		}
		jsonResponse(w, http.StatusOK, settings)
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			BotToken       string `json:"bot_token"`
			GuildID        string `json:"guild_id"`
			MasterPassword string `json:"master_password"`
			ChunkSizeBytes int    `json:"chunk_size_bytes"`
			WorkerCount    int    `json:"worker_count"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid information sent", http.StatusBadRequest)
			return
		}

		if req.BotToken != "" {
			_ = s.db.SetSetting("bot_token", req.BotToken)
			s.discord.SetBotToken(req.BotToken)
		}
		if req.GuildID != "" {
			_ = s.db.SetSetting("guild_id", req.GuildID)
		}
		currToken, _ := s.db.GetSetting("bot_token")
		currGuild, _ := s.db.GetSetting("guild_id")
		if currToken != "" && currGuild != "" {
			go func(t, g string) {
				cm := storage.NewClusterManager()
				node, err := cm.VerifyNode(context.Background(), t, g)
				if err == nil && node != nil {
					_ = s.db.UpsertBotNode(&db.BotNodeRecord{
						ID:           node.GuildID,
						BotToken:     node.BotToken,
						GuildID:      node.GuildID,
						BotName:      node.BotName,
						GuildName:    node.GuildName,
						Status:       node.Status,
						PingMs:       node.PingMs,
						ChannelCount: node.ChannelCount,
						CreatedAt:    time.Now().Unix(),
					})
				}
			}(currToken, currGuild)
		}
		if req.MasterPassword != "" {
			salt, _ := crypto.GenerateSalt()
			masterKey := crypto.DeriveKey(req.MasterPassword, salt)
			passHash := crypto.ComputeSHA256(masterKey)

			_ = s.db.SetSetting("master_salt_hex", hex.EncodeToString(salt))
			_ = s.db.SetSetting("master_pass_hash", passHash)

			s.uploader.SetMasterKey(masterKey)
			s.downloader.SetMasterKey(masterKey)
			s.catalog.SetMasterKey(masterKey)
		}
		if req.ChunkSizeBytes > 0 {
			_ = s.db.SetSetting("chunk_size_bytes", fmt.Sprintf("%d", req.ChunkSizeBytes))
		}
		if req.WorkerCount > 0 {
			_ = s.db.SetSetting("worker_count", fmt.Sprintf("%d", req.WorkerCount))
		}

		jsonResponse(w, http.StatusOK, map[string]string{"status": "settings updated"})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleAutoSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	botToken, _ := s.db.GetSetting("bot_token")
	guildID, _ := s.db.GetSetting("guild_id")

	var req struct {
		BotToken string   `json:"bot_token"`
		GuildID  string   `json:"guild_id"`
		GuildIDs []string `json:"guild_ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.BotToken != "" {
		botToken = req.BotToken
		_ = s.db.SetSetting("bot_token", botToken)
		s.discord.SetBotToken(botToken)
	}
	if req.GuildID != "" {
		guildID = req.GuildID
		_ = s.db.SetSetting("guild_id", guildID)
	}

	var guildList []string
	if len(req.GuildIDs) > 0 {
		guildList = req.GuildIDs
	} else if guildID != "" {
		parts := strings.Split(strings.ReplaceAll(guildID, "\n", ","), ",")
		for _, p := range parts {
			trimmed := strings.TrimSpace(p)
			if trimmed != "" {
				guildList = append(guildList, trimmed)
			}
		}
	}

	if botToken == "" || len(guildList) == 0 {
		http.Error(w, "Bot Token and at least one Server ID are needed", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := s.discord.AutoSetupMultiGuilds(ctx, guildList)
	if err != nil {
		http.Error(w, fmt.Sprintf("Setup stopped: %v", err), http.StatusInternalServerError)
		return
	}

	if result.MetadataChannel.ID != "" {
		_ = s.db.AddChannel(result.MetadataChannel.ID, result.MetadataChannel.Name, "", true)
	}
	for _, ch := range result.StorageChannels {
		if ch.Channel.ID != "" && ch.Webhook.URL != "" {
			_ = s.db.AddChannel(ch.Channel.ID, ch.Channel.Name, ch.Webhook.URL, false)
		}
	}

	jsonResponse(w, http.StatusOK, result)
}

func (s *Server) handleCleanChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		TargetGuildID  string   `json:"target_guild_id"`
		TargetGuildIDs []string `json:"target_guild_ids"`
		AllServers     bool     `json:"all_servers"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	var targetGuilds []string
	seen := make(map[string]bool)

	if req.AllServers || (req.TargetGuildID == "" && len(req.TargetGuildIDs) == 0) {
		nodes, _ := s.db.GetAllBotNodes()
		for _, n := range nodes {
			if n.GuildID != "" && !seen[n.GuildID] {
				seen[n.GuildID] = true
				targetGuilds = append(targetGuilds, n.GuildID)
			}
		}
		primaryGuild, _ := s.db.GetSetting("guild_id")
		if primaryGuild != "" && !seen[primaryGuild] {
			seen[primaryGuild] = true
			targetGuilds = append(targetGuilds, primaryGuild)
		}
	} else {
		for _, g := range req.TargetGuildIDs {
			g = strings.TrimSpace(g)
			if g != "" && !seen[g] {
				seen[g] = true
				targetGuilds = append(targetGuilds, g)
			}
		}
		if req.TargetGuildID != "" && req.TargetGuildID != "all" && !seen[req.TargetGuildID] {
			seen[req.TargetGuildID] = true
			targetGuilds = append(targetGuilds, req.TargetGuildID)
		}
	}

	if len(targetGuilds) == 0 {
		http.Error(w, "No Discord servers found to clean", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	totalDeleted := 0
	nodes, _ := s.db.GetAllBotNodes()

	for _, gID := range targetGuilds {
		nodeClient := s.discord
		for _, n := range nodes {
			if n.GuildID == gID && n.BotToken != "" {
				nodeClient = discord.NewClient(n.BotToken)
				break
			}
		}

		deletedCount, err := nodeClient.CleanExistingStorageChannels(ctx, gID)
		if err == nil {
			totalDeleted += deletedCount
		}
	}

	_ = s.db.ClearChannels()
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":            true,
		"status":        "cleaned",
		"deleted_count": totalDeleted,
		"servers_count": len(targetGuilds),
	})
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.db.GetStorageChannels()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	metaChannel, _ := s.db.GetMetadataChannel()

	jsonResponse(w, http.StatusOK, map[string]any{
		"storage_channels": channels,
		"metadata_channel": metaChannel,
	})
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	parentID := r.URL.Query().Get("parent_id")
	files, err := s.db.ListFiles(parentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, files)
}

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		Name     string `json:"name"`
		ParentID string `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Folder name is required"})
		return
	}

	parentPath := ""
	if req.ParentID != "" {
		parentRec, err := s.db.GetFile(req.ParentID)
		if err == nil && parentRec != nil {
			parentPath = parentRec.Path
		}
	}

	dirRec := &db.FileRecord{
		ID:        uuid.New().String(),
		ParentID:  req.ParentID,
		Name:      strings.TrimSpace(req.Name),
		Path:      filepath.ToSlash(filepath.Join(parentPath, strings.TrimSpace(req.Name))),
		Size:      0,
		IsDir:     true,
		ModTime:   time.Now().Unix(),
		SHA256:    "",
		MimeType:  "folder",
		CreatedAt: time.Now().Unix(),
	}

	if err := s.db.UpsertFile(dirRec); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	s.broadcastJSON(map[string]any{
		"type":      "files_changed",
		"file_id":   dirRec.ID,
		"parent_id": req.ParentID,
	})

	guildID, _ := s.db.GetSetting("guild_id")
	if guildID != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = s.discord.FindOrCreateCategory(ctx, guildID, dirRec.Name)
		}()
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":     true,
		"folder": dirRec,
		"id":     dirRec.ID,
		"name":   dirRec.Name,
	})
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType != "" && len(contentType) >= 19 && contentType[:19] == "multipart/form-data" {
		err := r.ParseMultipartForm(100 * 1024 * 1024)
		if err != nil {
			http.Error(w, "Could not read form "+err.Error(), http.StatusBadRequest)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "File part missing "+err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()

		tempDir := filepath.Join(os.TempDir(), "discord drive uploads")
		_ = os.MkdirAll(tempDir, 0755)
		tempPath := filepath.Join(tempDir, header.Filename)

		dst, err := os.Create(tempPath)
		if err != nil {
			http.Error(w, "Could not create temporary file "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := io.Copy(dst, file); err != nil {
			dst.Close()
			http.Error(w, "Copy error "+err.Error(), http.StatusInternalServerError)
			return
		}
		dst.Close()

		parentID := r.FormValue("parent_id")
		targetGuildID := r.FormValue("target_guild_id")
		jobID, err := s.uploader.UploadFile(context.Background(), tempPath, "/"+header.Filename, parentID, targetGuildID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		jsonResponse(w, http.StatusOK, map[string]string{"job_id": jobID, "status": "started"})
		return
	}

	var req struct {
		LocalPath     string `json:"local_path"`
		VirtualPath   string `json:"virtual_path"`
		ParentID      string `json:"parent_id"`
		TargetGuildID string `json:"target_guild_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid information sent", http.StatusBadRequest)
		return
	}

	jobID, err := s.uploader.UploadFile(context.Background(), req.LocalPath, req.VirtualPath, req.ParentID, req.TargetGuildID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"job_id": jobID, "status": "started"})
}

func (s *Server) handleUploadDir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		LocalPath         string `json:"local_path"`
		VirtualParentPath string `json:"virtual_parent_path"`
		TargetGuildID     string `json:"target_guild_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid information sent", http.StatusBadRequest)
		return
	}

	jobIDs, err := s.uploader.UploadDirectory(context.Background(), req.LocalPath, req.VirtualParentPath, req.TargetGuildID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"job_ids": jobIDs, "count": len(jobIDs), "status": "started"})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleStreamDownload(w, r)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		FileID    string `json:"file_id"`
		LocalDest string `json:"local_dest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid information sent", http.StatusBadRequest)
		return
	}

	jobID, err := s.downloader.DownloadFile(context.Background(), req.FileID, req.LocalDest)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"job_id": jobID, "status": "started"})
}

func (s *Server) ensureMasterKeyUnlocked() bool {
	return s.downloader.HasMasterKey()
}

func (s *Server) handleDownloadCheck(w http.ResponseWriter, r *http.Request) {
	fileID := r.URL.Query().Get("file_id")
	if fileID == "" {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": "File ID is required"})
		return
	}

	fileRec, err := s.db.GetFile(fileID)
	if err != nil || fileRec == nil {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": "File not found"})
		return
	}

	if !s.ensureMasterKeyUnlocked() {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": "Please enter your master password in Easy Setup first"})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "name": fileRec.Name, "size": fileRec.Size, "is_dir": fileRec.IsDir})
}

func (s *Server) handleStreamDownload(w http.ResponseWriter, r *http.Request) {
	fileID := r.URL.Query().Get("file_id")
	if fileID == "" {
		http.Error(w, "File ID is required", http.StatusBadRequest)
		return
	}

	fileRec, err := s.db.GetFile(fileID)
	if err != nil || fileRec == nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	if !s.ensureMasterKeyUnlocked() {
		http.Error(w, "Please enter your master password in Easy Setup first", http.StatusUnauthorized)
		return
	}

	if fileRec.IsDir {
		zipName := fmt.Sprintf("%s.zip", fileRec.Name)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, zipName))
		w.Header().Set("Content-Type", "application/zip")
		_ = s.downloader.StreamFolderZip(r.Context(), fileID, w)
		return
	}

	mimeType := mime.TypeByExtension(filepath.Ext(fileRec.Name))
	if mimeType == "" {
		ext := strings.ToLower(filepath.Ext(fileRec.Name))
		switch ext {
		case ".json":
			mimeType = "application/json"
		case ".txt", ".log", ".md":
			mimeType = "text/plain; charset=utf-8"
		case ".js":
			mimeType = "application/javascript"
		case ".css":
			mimeType = "text/css"
		case ".html":
			mimeType = "text/html"
		case ".py", ".go", ".rs", ".java", ".c", ".cpp", ".sh":
			mimeType = "text/plain; charset=utf-8"
		case ".pdf":
			mimeType = "application/pdf"
		case ".mp4":
			mimeType = "video/mp4"
		case ".webm":
			mimeType = "video/webm"
		case ".mp3":
			mimeType = "audio/mpeg"
		case ".wav":
			mimeType = "audio/wav"
		case ".png":
			mimeType = "image/png"
		case ".jpg", ".jpeg":
			mimeType = "image/jpeg"
		case ".gif":
			mimeType = "image/gif"
		case ".webp":
			mimeType = "image/webp"
		case ".svg":
			mimeType = "image/svg+xml"
		default:
			mimeType = "application/octet-stream"
		}
	}

	w.Header().Set("Accept-Ranges", "bytes")

	if r.URL.Query().Get("inline") == "1" || r.URL.Query().Get("preview") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileRec.Name))
	} else {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileRec.Name))
	}

	w.Header().Set("Content-Type", mimeType)

	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" && strings.HasPrefix(rangeHeader, "bytes=") {
		rangesStr := strings.TrimPrefix(rangeHeader, "bytes=")
		parts := strings.Split(rangesStr, "-")
		if len(parts) == 2 {
			var start, end int64
			var parseErr error

			if parts[0] == "" {
				suffixLen, err := strconv.ParseInt(parts[1], 10, 64)
				if err == nil {
					start = fileRec.Size - suffixLen
					end = fileRec.Size - 1
				}
			} else {
				start, parseErr = strconv.ParseInt(parts[0], 10, 64)
				if parseErr == nil {
					if parts[1] == "" {
						end = fileRec.Size - 1
					} else {
						end, parseErr = strconv.ParseInt(parts[1], 10, 64)
					}
				}
			}

			if parseErr == nil && start >= 0 && end >= start && start < fileRec.Size {
				if end >= fileRec.Size {
					end = fileRec.Size - 1
				}
				contentLength := end - start + 1
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileRec.Size))
				w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
				w.WriteHeader(http.StatusPartialContent)
				_ = s.downloader.StreamDownloadRange(r.Context(), fileID, start, end, w)
				return
			}
		}
	}

	w.Header().Set("Content-Length", strconv.FormatInt(fileRec.Size, 10))
	_ = s.downloader.StreamDownload(r.Context(), fileID, w)
}

func (s *Server) handleCreateTextFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		Name     string `json:"name"`
		Filename string `json:"filename"`
		Content  string `json:"content"`
		ParentID string `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid request"})
		return
	}

	targetName := strings.TrimSpace(req.Name)
	if targetName == "" {
		targetName = strings.TrimSpace(req.Filename)
	}
	if targetName == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "File name is required"})
		return
	}

	tempDir := filepath.Join(os.TempDir(), "discord drive uploads")
	_ = os.MkdirAll(tempDir, 0755)
	tempPath := filepath.Join(tempDir, targetName)

	if err := os.WriteFile(tempPath, []byte(req.Content), 0644); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jobID, err := s.uploader.UploadFile(context.Background(), tempPath, "/"+targetName, req.ParentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "job_id": jobID, "name": targetName, "status": "started"})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		FileID  string   `json:"file_id"`
		FileIDs []string `json:"file_ids"`
		ID      string   `json:"id"`
		IDs     []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid request body"})
		return
	}

	var targets []string
	if len(req.FileIDs) > 0 {
		targets = append(targets, req.FileIDs...)
	}
	if len(req.IDs) > 0 {
		targets = append(targets, req.IDs...)
	}
	if req.FileID != "" {
		targets = append(targets, req.FileID)
	}
	if req.ID != "" {
		targets = append(targets, req.ID)
	}

	if len(targets) == 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "No file IDs provided"})
		return
	}

	deletedCount := 0
	for _, rawID := range targets {
		fID := strings.TrimSpace(rawID)
		if fID == "" {
			continue
		}

		fileRec, _ := s.db.GetFile(fID)
		if fileRec == nil {
			fileRec, _ = s.db.GetFileByPath(fID)
		}

		if fileRec != nil {
			if fileRec.IsDir {
				subFiles, _ := s.db.GetAllFiles()
				for _, sf := range subFiles {
					if strings.HasPrefix(sf.Path, fileRec.Path+"/") {
						_ = s.db.DeleteFile(sf.ID)
					}
				}
			}
			_ = s.db.DeleteFile(fileRec.ID)
			deletedCount++
		} else {
			_ = s.db.DeleteFile(fID)
			deletedCount++
		}
	}

	s.broadcastJSON(map[string]any{
		"type": "files_changed",
	})

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":            true,
		"status":        "deleted",
		"deleted_count": deletedCount,
	})
}

func (s *Server) handleCatalogSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	if !s.ensureMasterKeyUnlocked() {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": "Please enter your master password in Easy Setup first"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	msgID, err := s.catalog.ExportAndSyncToDiscord(ctx)
	if err != nil {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": fmt.Sprintf("%v", err)})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "checkpoint uploaded", "message_id": msgID})
}

func (s *Server) handleCatalogRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	if !s.ensureMasterKeyUnlocked() {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": "Please enter your master password in Easy Setup first"})
		return
	}

	var req struct {
		MetadataChannelID string `json:"metadata_channel_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	manifest, err := s.catalog.RestoreFromDiscord(ctx, req.MetadataChannelID)
	if err != nil {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": fmt.Sprintf("%v", err)})
		return
	}

	s.broadcastJSON(map[string]any{"type": "files_changed"})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "catalog restored", "files_imported": len(manifest.Files)})
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.db.GetActiveJobs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, jobs)
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid information sent", http.StatusBadRequest)
		return
	}

	if req.JobID == "all" || req.JobID == "" {
		s.uploader.CancelAllJobs()
		s.downloader.CancelAllJobs()
		jsonResponse(w, http.StatusOK, map[string]string{"status": "all paused"})
		return
	}

	s.uploader.CancelJob(req.JobID)
	s.downloader.CancelJob(req.JobID)
	jsonResponse(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleBots(w http.ResponseWriter, r *http.Request) {
	nodes, _ := s.db.GetAllBotNodes()
	botToken, _ := s.db.GetSetting("bot_token")
	guildID, _ := s.db.GetSetting("guild_id")

	if len(nodes) == 0 && botToken != "" && guildID != "" {
		cm := storage.NewClusterManager()
		node, err := cm.VerifyNode(r.Context(), botToken, guildID)
		if err == nil && node != nil {
			botRec := &db.BotNodeRecord{
				ID:           node.GuildID,
				BotToken:     node.BotToken,
				GuildID:      node.GuildID,
				BotName:      node.BotName,
				GuildName:    node.GuildName,
				Status:       node.Status,
				PingMs:       node.PingMs,
				ChannelCount: node.ChannelCount,
				CreatedAt:    time.Now().Unix(),
			}
			_ = s.db.UpsertBotNode(botRec)
			nodes = []db.BotNodeRecord{*botRec}
		}
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":    true,
		"nodes": nodes,
		"count": len(nodes),
	})
}

func (s *Server) handleAddBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		BotToken string `json:"bot_token"`
		GuildID  string `json:"guild_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BotToken == "" || req.GuildID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Bot token and server ID are required"})
		return
	}

	cm := storage.NewClusterManager()
	node, err := cm.VerifyNode(r.Context(), req.BotToken, req.GuildID)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	botRec := &db.BotNodeRecord{
		ID:           node.GuildID,
		BotToken:     node.BotToken,
		GuildID:      node.GuildID,
		BotName:      node.BotName,
		GuildName:    node.GuildName,
		Status:       node.Status,
		PingMs:       node.PingMs,
		ChannelCount: node.ChannelCount,
		CreatedAt:    time.Now().Unix(),
	}

	if err := s.db.UpsertBotNode(botRec); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	go func() {
		client := discord.NewClient(req.BotToken)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := client.AutoSetupServer(ctx, req.GuildID)
		if err == nil && res != nil {
			for _, ch := range res.StorageChannels {
				if ch.Channel.ID != "" && ch.Webhook.URL != "" {
					_ = s.db.AddChannelWithGuild(ch.Channel.ID, ch.Channel.Name, ch.Webhook.URL, req.GuildID, req.BotToken, false)
				}
			}
			if res.MetadataChannel.ID != "" {
				_ = s.db.AddChannelWithGuild(res.MetadataChannel.ID, res.MetadataChannel.Name, "", req.GuildID, req.BotToken, true)
			}
		}
	}()

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":   true,
		"node": botRec,
	})
}

func (s *Server) handleDeleteBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ID is required"})
		return
	}

	if err := s.db.DeleteBotNode(req.ID); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "deleted"})
}

func (s *Server) handleTestBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		BotToken string `json:"bot_token"`
		GuildID  string `json:"guild_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BotToken == "" || req.GuildID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Bot token and server ID are required"})
		return
	}

	cm := storage.NewClusterManager()
	node, err := cm.VerifyNode(r.Context(), req.BotToken, req.GuildID)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":   true,
		"node": node,
	})
}

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	nodes, _ := s.db.GetAllBotNodes()
	guildID, _ := s.db.GetSetting("guild_id")
	botToken, _ := s.db.GetSetting("bot_token")

	type ServerItem struct {
		GuildID          string `json:"guild_id"`
		GuildName        string `json:"guild_name"`
		BotName          string `json:"bot_name"`
		Status           string `json:"status"`
		PingMs           int64  `json:"ping_ms"`
		ChannelCount     int    `json:"channel_count"`
		StorageBytes     int64  `json:"storage_bytes"`
		StorageFormatted string `json:"storage_formatted"`
		LastSeen         int64  `json:"last_seen"`
	}

	var servers []ServerItem
	seen := make(map[string]bool)

	for _, n := range nodes {
		seen[n.GuildID] = true
		sBytes := s.db.GetStorageBytesForGuild(n.GuildID)
		servers = append(servers, ServerItem{
			GuildID:          n.GuildID,
			GuildName:        n.GuildName,
			BotName:          n.BotName,
			Status:           n.Status,
			PingMs:           n.PingMs,
			ChannelCount:     n.ChannelCount,
			StorageBytes:     sBytes,
			StorageFormatted: formatBytes(sBytes),
			LastSeen:         n.LastSeen,
		})
	}

	if guildID != "" && !seen[guildID] {
		cm := storage.NewClusterManager()
		gName := "My Discord Server"
		chCount := 4
		if botToken != "" {
			name, count, err := cm.FetchGuildInfo(r.Context(), botToken, guildID)
			if err == nil && name != "" {
				gName = name
				chCount = count
			}
		}
		sBytes := s.db.GetStorageBytesForGuild(guildID)
		servers = append(servers, ServerItem{
			GuildID:          guildID,
			GuildName:        gName,
			BotName:          "Main Bot",
			Status:           "Active",
			PingMs:           45,
			ChannelCount:     chCount,
			StorageBytes:     sBytes,
			StorageFormatted: formatBytes(sBytes),
			LastSeen:         time.Now().Unix(),
		})
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":      true,
		"servers": servers,
		"count":   len(servers),
	})
}

func (s *Server) handleServerHealth(w http.ResponseWriter, r *http.Request) {
	nodes, _ := s.db.GetAllBotNodes()
	guildID, _ := s.db.GetSetting("guild_id")
	botToken, _ := s.db.GetSetting("bot_token")

	cm := storage.NewClusterManager()
	var botNodes []storage.BotNode
	seen := make(map[string]bool)

	for _, n := range nodes {
		seen[n.GuildID] = true
		botNodes = append(botNodes, storage.BotNode{
			ID:           n.ID,
			BotToken:     n.BotToken,
			GuildID:      n.GuildID,
			BotName:      n.BotName,
			GuildName:    n.GuildName,
			Status:       n.Status,
			PingMs:       n.PingMs,
			ChannelCount: n.ChannelCount,
			StorageBytes: s.db.GetStorageBytesForGuild(n.GuildID),
			CreatedAt:    n.CreatedAt,
			LastSeen:     n.LastSeen,
		})
	}

	if guildID != "" && !seen[guildID] && botToken != "" {
		botNodes = append(botNodes, storage.BotNode{
			ID:           guildID,
			BotToken:     botToken,
			GuildID:      guildID,
			BotName:      "Main Bot",
			GuildName:    "My Discord Server",
			Status:       "Active",
			PingMs:       45,
			ChannelCount: 4,
			StorageBytes: s.db.GetStorageBytesForGuild(guildID),
			CreatedAt:    time.Now().Unix(),
			LastSeen:     time.Now().Unix(),
		})
	}

	cm.SetNodes(botNodes)
	updatedNodes := cm.CheckAllNodesHealth(r.Context())

	for _, un := range updatedNodes {
		botRec := &db.BotNodeRecord{
			ID:           un.GuildID,
			BotToken:     un.BotToken,
			GuildID:      un.GuildID,
			BotName:      un.BotName,
			GuildName:    un.GuildName,
			Status:       un.Status,
			PingMs:       un.PingMs,
			ChannelCount: un.ChannelCount,
			StorageBytes: s.db.GetStorageBytesForGuild(un.GuildID),
			CreatedAt:    un.CreatedAt,
			LastSeen:     un.LastSeen,
		}
		_ = s.db.UpsertBotNode(botRec)
	}

	type ServerHealthItem struct {
		GuildID          string `json:"guild_id"`
		GuildName        string `json:"guild_name"`
		BotName          string `json:"bot_name"`
		Status           string `json:"status"`
		PingMs           int64  `json:"ping_ms"`
		ChannelCount     int    `json:"channel_count"`
		StorageBytes     int64  `json:"storage_bytes"`
		StorageFormatted string `json:"storage_formatted"`
		LastSeen         int64  `json:"last_seen"`
	}

	var formattedList []ServerHealthItem
	for _, un := range updatedNodes {
		sBytes := s.db.GetStorageBytesForGuild(un.GuildID)
		formattedList = append(formattedList, ServerHealthItem{
			GuildID:          un.GuildID,
			GuildName:        un.GuildName,
			BotName:          un.BotName,
			Status:           un.Status,
			PingMs:           un.PingMs,
			ChannelCount:     un.ChannelCount,
			StorageBytes:     sBytes,
			StorageFormatted: formatBytes(sBytes),
			LastSeen:         un.LastSeen,
		})
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":      true,
		"servers": formattedList,
		"count":   len(formattedList),
	})
}

func (s *Server) handleServerSetupChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		GuildID  string `json:"guild_id"`
		BotToken string `json:"bot_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GuildID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Server ID is required"})
		return
	}

	if req.BotToken == "" {
		nodes, _ := s.db.GetAllBotNodes()
		for _, n := range nodes {
			if n.GuildID == req.GuildID && n.BotToken != "" {
				req.BotToken = n.BotToken
				break
			}
		}
		if req.BotToken == "" {
			req.BotToken, _ = s.db.GetSetting("bot_token")
		}
	}

	if req.BotToken == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Bot token is missing for this server"})
		return
	}

	res, err := s.discord.AutoSetupServerWithToken(r.Context(), req.BotToken, req.GuildID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	for _, ch := range res.StorageChannels {
		_ = s.db.AddChannelWithGuild(ch.Channel.ID, ch.Channel.Name, ch.Webhook.URL, req.GuildID, req.BotToken, false)
	}
	if res.MetadataChannel.ID != "" {
		_ = s.db.AddChannelWithGuild(res.MetadataChannel.ID, res.MetadataChannel.Name, "", req.GuildID, req.BotToken, true)
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":               true,
		"storage_channels": len(res.StorageChannels),
		"metadata_channel": res.MetadataChannel.Name,
		"status":           "All storage channels configured cleanly!",
	})
}

func (s *Server) handleServerChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		GuildID string `json:"guild_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GuildID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Server ID required"})
		return
	}

	botToken, _ := s.db.GetSetting("bot_token")
	nodes, _ := s.db.GetAllBotNodes()
	for _, n := range nodes {
		if n.GuildID == req.GuildID && n.BotToken != "" {
			botToken = n.BotToken
			break
		}
	}

	if botToken == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "No bot token found for this server"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/guilds/%s/channels", discord.DiscordAPIBase, req.GuildID), nil)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	httpReq.Header.Set("Authorization", "Bot "+botToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()

	var rawChannels []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Type     int    `json:"type"`
		Topic    string `json:"topic"`
		ParentID string `json:"parent_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rawChannels)

	type ChannelView struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Type      string `json:"type"`
		Topic     string `json:"topic"`
		IsStorage bool   `json:"is_storage"`
	}

	var formatted []ChannelView
	for _, ch := range rawChannels {
		chType := "Text Channel"
		if ch.Type == 4 {
			chType = "Category"
		} else if ch.Type == 2 {
			chType = "Voice Channel"
		}

		isStorage := strings.HasPrefix(ch.Name, "storage") || strings.HasPrefix(ch.Name, "part") || strings.Contains(ch.Name, "catalog") || strings.Contains(ch.Name, "metadata")

		formatted = append(formatted, ChannelView{
			ID:        ch.ID,
			Name:      ch.Name,
			Type:      chType,
			Topic:     ch.Topic,
			IsStorage: isStorage,
		})
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":       true,
		"guild_id": req.GuildID,
		"channels": formatted,
		"count":    len(formatted),
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	passHash, _ := s.db.GetSetting("master_pass_hash")
	isUnlocked := s.uploader.HasMasterKey()

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":           true,
		"has_password": passHash != "",
		"is_unlocked":  isUnlocked,
	})
}

func (s *Server) handleAuthUnlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Password is required"})
		return
	}

	passHash, _ := s.db.GetSetting("master_pass_hash")
	saltHex, _ := s.db.GetSetting("master_salt_hex")

	var salt []byte
	if saltHex != "" {
		salt, _ = hex.DecodeString(saltHex)
	}
	if len(salt) == 0 {
		salt, _ = crypto.GenerateSalt()
		_ = s.db.SetSetting("master_salt_hex", hex.EncodeToString(salt))
	}

	masterKey := crypto.DeriveKey(req.Password, salt)
	computedHash := crypto.ComputeSHA256(masterKey)

	if passHash != "" && passHash != computedHash {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Incorrect password"})
		return
	}

	if passHash == "" {
		_ = s.db.SetSetting("master_pass_hash", computedHash)
	}

	s.uploader.SetMasterKey(masterKey)
	s.downloader.SetMasterKey(masterKey)
	s.catalog.SetMasterKey(masterKey)

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": "unlocked",
	})
}

func (s *Server) handleAuthSetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Password cannot be empty"})
		return
	}

	salt, _ := crypto.GenerateSalt()
	masterKey := crypto.DeriveKey(req.Password, salt)
	passHash := crypto.ComputeSHA256(masterKey)

	_ = s.db.SetSetting("master_salt_hex", hex.EncodeToString(salt))
	_ = s.db.SetSetting("master_pass_hash", passHash)

	s.uploader.SetMasterKey(masterKey)
	s.downloader.SetMasterKey(masterKey)
	s.catalog.SetMasterKey(masterKey)

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": "password_set",
	})
}

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
