package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"discord-free-cloud/internal/catalog"
	"discord-free-cloud/internal/crypto"
	"discord-free-cloud/internal/db"
	"discord-free-cloud/internal/discord"
	"discord-free-cloud/internal/downloader"
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
	catalog    *catalog.Manager
	frontendFS fs.FS

	wsMu      sync.Mutex
	wsClients map[*websocket.Conn]bool

	syncMu sync.Mutex // serializes server/node syncing + provisioning

	sessMu   sync.Mutex
	sessions map[string]time.Time // session token -> expiry
}

func NewServer(database *db.Database, discordClient *discord.Client, upEngine *uploader.Engine, downEngine *downloader.Engine, catManager *catalog.Manager, frontend fs.FS) *Server {
	s := &Server{
		db:         database,
		discord:    discordClient,
		uploader:   upEngine,
		downloader: downEngine,
		catalog:    catManager,
		frontendFS: frontend,
		wsClients:  make(map[*websocket.Conn]bool),
		sessions:   make(map[string]time.Time),
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
			mem := syswin.GetMemStats()
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

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			_, _ = s.syncAllBotGuilds(context.Background())
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

// RegisterRoutes wires every endpoint onto mux and returns the auth-wrapped
// handler the caller should serve — see the note at the end of this function.
func (s *Server) RegisterRoutes(mux *http.ServeMux) http.Handler {
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
	mux.HandleFunc("/api/servers/sync", s.handleServersSync)
	mux.HandleFunc("/api/servers/channels", s.handleServerChannels)
	mux.HandleFunc("/api/servers/health", s.handleServerHealth)
	mux.HandleFunc("/api/servers/setup_channels", s.handleServerSetupChannels)
	mux.HandleFunc("/api/stats", s.handleStatus)
	mux.HandleFunc("/api/upload", s.handleUploadFile)
	mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/api/auth/unlock", s.handleAuthUnlock)
	mux.HandleFunc("/api/auth/set_password", s.handleAuthSetPassword)
	mux.HandleFunc("/api/auth/lock", s.handleAuthLock)
	mux.HandleFunc("/api/create-token", s.handleCreateToken)
	mux.HandleFunc("/api/shares/create", s.handleShareCreate)
	mux.HandleFunc("/api/shares/list", s.handleShareList)
	mux.HandleFunc("/api/shares/revoke", s.handleShareRevoke)
	mux.HandleFunc("/api/share/", s.handleSharePublic)
	mux.HandleFunc("/api/verify", s.handleVerifyFile)

	var fileHandler http.Handler
	if _, err := os.Stat(filepath.Join("frontend", "index.html")); err == nil {
		fileHandler = http.FileServer(http.Dir("frontend"))
	} else {
		fileHandler = http.FileServer(http.FS(s.frontendFS))
	}
	mux.Handle("/", fileHandler)

	// The auth middleware is applied here rather than in main so every caller
	// of RegisterRoutes gets a guarded handler: no API tokens seeded means it
	// is a transparent pass-through (first-run dashboard unchanged).
	return s.requireAuth(mux)
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

func (s *Server) isDriveUnlocked() bool {
	return s.uploader.HasMasterKey()
}

func (s *Server) isStorageReady() (bool, string) {
	if !s.isDriveUnlocked() {
		return false, "Drive is locked. Enter password to unlock."
	}
	botNodes, _ := s.db.GetAllBotNodes()
	activeGuildCount := 0
	for _, n := range botNodes {
		if n.GuildID != "" {
			activeGuildCount++
		}
	}
	channels, _ := s.db.GetStorageChannels()
	if activeGuildCount == 0 && len(channels) == 0 {
		return false, "No Discord servers connected"
	}
	return true, ""
}

func (s *Server) ensureMasterKeyUnlocked() bool {
	return s.isDriveUnlocked()
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	channels, _ := s.db.GetStorageChannels()
	fileCount := s.db.GetFileCount()
	totalStorageBytes := s.db.GetTotalStorageBytes()
	activeJobs, _ := s.db.GetActiveJobs()
	diskSpace, _ := syswin.GetDiskFreeSpace(".")
	mem := syswin.GetMemStats()

	botToken, _ := s.db.GetSetting("bot_token")
	guildID, _ := s.db.GetSetting("guild_id")
	lastSync, _ := s.db.GetSetting("last_catalog_sync")
	passHash, _ := s.db.GetSetting("master_pass_hash")
	botNodes, _ := s.db.GetAllBotNodes()

	var diskFree int64 = 0
	if diskSpace != nil {
		diskFree = int64(diskSpace.FreeBytes)
	}

	isUnlocked := s.isDriveUnlocked()
	isConfigured := (botToken != "" && guildID != "") || len(botNodes) > 0 || len(channels) > 0
	cloudReady := isUnlocked && isConfigured

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"is_configured":       isConfigured,
		"cloud_ready":         cloudReady,
		"bot_nodes_count":     len(botNodes),
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
		"is_unlocked":         isUnlocked,
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		settings, err := s.db.GetAllSettings()
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		// The dashboard only ever needs to know *whether* a secret is set —
		// never the secret. bot_token used to be returned verbatim here, and
		// password/token hashes are equally sensitive.
		redactedSettings := map[string]string{
			"master_pass_hash":     "master_password_set",
			"api_token_write_hash": "api_token_write_set",
			"api_token_read_hash":  "api_token_read_set",
		}
		for key, flag := range redactedSettings {
			if v, ok := settings[key]; ok {
				delete(settings, key)
				settings[flag] = boolString(v != "")
			}
		}
		if salt, ok := settings["master_salt_hex"]; ok {
			delete(settings, "master_salt_hex")
			settings["master_salt_set"] = boolString(salt != "")
		}
		if token, ok := settings["bot_token"]; ok {
			settings["bot_token_masked"] = ""
			if len(token) > 10 {
				settings["bot_token_masked"] = token[:4] + "..." + token[len(token)-4:]
			}
			settings["bot_token"] = ""
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
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid request body"})
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
				node, err := s.discord.VerifyNode(context.Background(), t, g)
				if err == nil && node != nil {
					_ = s.db.UpsertBotNode(node)
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

		jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "settings updated"})
		return
	}

	jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
}

func (s *Server) handleAutoSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
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
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Bot Token and at least one Server ID are required"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := s.discord.SetupGuilds(ctx, guildList)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": fmt.Sprintf("Setup stopped: %v", err)})
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
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
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
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "No Discord servers found to clean"})
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
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	metaChannel, _ := s.db.GetMetadataChannel()

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":               true,
		"storage_channels": channels,
		"metadata_channel": metaChannel,
	})
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	parentID := r.URL.Query().Get("parent_id")
	files, err := s.db.ListFiles(parentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if files == nil {
		files = []db.FileRecord{}
	}
	jsonResponse(w, http.StatusOK, files)
}

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	if ready, reason := s.isStorageReady(); !ready {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": reason})
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

	cleanName := strings.TrimSpace(req.Name)
	cleanName = strings.ReplaceAll(cleanName, "/", "")
	cleanName = strings.ReplaceAll(cleanName, "\\", "")
	if cleanName == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid folder name"})
		return
	}

	parentPath := ""
	if req.ParentID != "" {
		parentRec, err := s.db.GetFile(req.ParentID)
		if err == nil && parentRec != nil {
			parentPath = parentRec.Path
		}
	}

	folderPath := filepath.ToSlash(filepath.Join(parentPath, cleanName))
	existing, _ := s.db.GetFileByPath(folderPath)
	if existing != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "A folder or file with this name already exists"})
		return
	}

	dirRec := &db.FileRecord{
		ID:        uuid.New().String(),
		ParentID:  req.ParentID,
		Name:      cleanName,
		Path:      folderPath,
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
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	if ready, reason := s.isStorageReady(); !ready {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": reason})
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType != "" && len(contentType) >= 19 && strings.HasPrefix(contentType, "multipart/form-data") {
		err := r.ParseMultipartForm(100 * 1024 * 1024)
		if err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Could not read upload form: " + err.Error()})
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "File part is missing: " + err.Error()})
			return
		}
		defer file.Close()

		tempDir := filepath.Join(os.TempDir(), "discord-free-cloud")
		_ = os.MkdirAll(tempDir, 0755)
		tempPath := filepath.Join(tempDir, header.Filename)

		dst, err := os.Create(tempPath)
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Could not create temporary file: " + err.Error()})
			return
		}
		if _, err := io.Copy(dst, file); err != nil {
			dst.Close()
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Copy error: " + err.Error()})
			return
		}
		dst.Close()

		parentID := r.FormValue("parent_id")
		targetGuildID := r.FormValue("target_guild_id")

		parentPath := ""
		if parentID != "" {
			parentRec, err := s.db.GetFile(parentID)
			if err == nil && parentRec != nil {
				parentPath = parentRec.Path
			}
		}
		virtualPath := filepath.ToSlash(filepath.Join(parentPath, header.Filename))

		jobID, err := s.uploader.UploadFile(context.Background(), tempPath, virtualPath, parentID, targetGuildID)
		if err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}

		jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "job_id": jobID, "status": "started"})
		return
	}

	var req struct {
		LocalPath     string `json:"local_path"`
		VirtualPath   string `json:"virtual_path"`
		ParentID      string `json:"parent_id"`
		TargetGuildID string `json:"target_guild_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid request body"})
		return
	}

	jobID, err := s.uploader.UploadFile(context.Background(), req.LocalPath, req.VirtualPath, req.ParentID, req.TargetGuildID)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "job_id": jobID, "status": "started"})
}

func (s *Server) handleUploadDir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	if ready, reason := s.isStorageReady(); !ready {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": reason})
		return
	}

	var req struct {
		LocalPath         string `json:"local_path"`
		VirtualParentPath string `json:"virtual_parent_path"`
		TargetGuildID     string `json:"target_guild_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid request body"})
		return
	}

	jobIDs, err := s.uploader.UploadDir(context.Background(), req.LocalPath, req.VirtualParentPath, req.TargetGuildID)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "job_ids": jobIDs, "count": len(jobIDs), "status": "started"})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleStreamDownload(w, r)
		return
	}

	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	if ready, reason := s.isStorageReady(); !ready {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": reason})
		return
	}

	var req struct {
		FileID    string `json:"file_id"`
		LocalDest string `json:"local_dest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid request body"})
		return
	}

	jobID, err := s.downloader.DownloadFile(context.Background(), req.FileID, req.LocalDest)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "job_id": jobID, "status": "started"})
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
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": "Drive is locked. Enter password to unlock."})
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
		http.Error(w, "Drive is locked. Enter password to unlock.", http.StatusUnauthorized)
		return
	}

	if fileRec.IsDir {
		zipName := fmt.Sprintf("%s.zip", fileRec.Name)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, zipName))
		w.Header().Set("Content-Type", "application/zip")
		_ = s.downloader.StreamZip(r.Context(), fileID, w)
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

	if ready, reason := s.isStorageReady(); !ready {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": reason})
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

	tempDir := filepath.Join(os.TempDir(), "discord-free-cloud")
	_ = os.MkdirAll(tempDir, 0755)
	tempPath := filepath.Join(tempDir, targetName)

	if err := os.WriteFile(tempPath, []byte(req.Content), 0644); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	parentPath := ""
	if req.ParentID != "" {
		parentRec, err := s.db.GetFile(req.ParentID)
		if err == nil && parentRec != nil {
			parentPath = parentRec.Path
		}
	}
	virtualPath := filepath.ToSlash(filepath.Join(parentPath, targetName))

	jobID, err := s.uploader.UploadFile(context.Background(), tempPath, virtualPath, req.ParentID)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "job_id": jobID, "name": targetName, "status": "started"})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
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

	type deleteTarget struct {
		fileRec *db.FileRecord
		chunks  []db.ChunkRecord
	}
	var deleteTargets []deleteTarget

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
						sfCopy := sf
						chunks, _ := s.db.GetAllChunksForFile(sfCopy.ID)
						deleteTargets = append(deleteTargets, deleteTarget{fileRec: &sfCopy, chunks: chunks})
					}
				}
			}
			chunks, _ := s.db.GetAllChunksForFile(fileRec.ID)
			deleteTargets = append(deleteTargets, deleteTarget{fileRec: fileRec, chunks: chunks})
		} else {
			chunks, _ := s.db.GetAllChunksForFile(fID)
			deleteTargets = append(deleteTargets, deleteTarget{
				fileRec: &db.FileRecord{ID: fID},
				chunks:  chunks,
			})
		}
	}

	deletedCount := 0
	for _, dt := range deleteTargets {
		_ = s.db.DeleteFile(dt.fileRec.ID)
		deletedCount++
	}

	s.broadcastJSON(map[string]any{
		"type": "files_changed",
	})

	type discordDeleteItem struct {
		channelID string
		messageID string
		guildID   string
	}
	var discordDeletes []discordDeleteItem

	for _, dt := range deleteTargets {
		for _, chunk := range dt.chunks {
			if chunk.ChannelID != "" && chunk.MessageID != "" {
				discordDeletes = append(discordDeletes, discordDeleteItem{
					channelID: chunk.ChannelID,
					messageID: chunk.MessageID,
					guildID:   chunk.GuildID,
				})
			}
		}
	}

	deleteJobID := "del-" + uuid.New().String()
	displayName := "Selected files"
	if len(deleteTargets) == 1 && deleteTargets[0].fileRec != nil && deleteTargets[0].fileRec.Name != "" {
		displayName = deleteTargets[0].fileRec.Name
	} else if len(deleteTargets) > 1 {
		displayName = fmt.Sprintf("%d items (%d parts)", deletedCount, len(discordDeletes))
	}

	if len(discordDeletes) > 0 {
		totalCount := len(discordDeletes)

		s.broadcastJSON(map[string]any{
			"type": "telemetry",
			"data": map[string]any{
				"job_id":           deleteJobID,
				"file_name":        displayName,
				"type":             "DELETE",
				"total_bytes":      int64(totalCount),
				"processed_bytes":  0,
				"total_chunks":     totalCount,
				"completed_chunks": 0,
				"progress_percent": 0.0,
				"status":           "ACTIVE",
				"log_message":      fmt.Sprintf("Cleaning up %d parts from Discord...", totalCount),
			},
		})

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			clientCache := make(map[string]*discord.Client)
			nodes, _ := s.db.GetAllBotNodes()
			for _, n := range nodes {
				if n.GuildID != "" && n.BotToken != "" {
					clientCache[n.GuildID] = discord.NewClient(n.BotToken)
				}
			}

			itemChan := make(chan discordDeleteItem, totalCount)
			for _, it := range discordDeletes {
				itemChan <- it
			}
			close(itemChan)

			var completedCount int64
			var failedCount int64
			var wg sync.WaitGroup

			workers := 4
			if workers > totalCount {
				workers = totalCount
			}

			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for item := range itemChan {
						select {
						case <-ctx.Done():
							return
						default:
						}

						client := s.discord
						if item.guildID != "" {
							if gc, ok := clientCache[item.guildID]; ok {
								client = gc
							}
						}

						err := client.DeleteMessage(ctx, item.channelID, item.messageID)
						if err != nil {
							atomic.AddInt64(&failedCount, 1)
							log.Printf("could not delete message %s in channel %s: %v", item.messageID, item.channelID, err)
						} else {
							done := atomic.AddInt64(&completedCount, 1)
							pct := (float64(done) / float64(totalCount)) * 100.0

							s.broadcastJSON(map[string]any{
								"type": "telemetry",
								"data": map[string]any{
									"job_id":           deleteJobID,
									"file_name":        displayName,
									"type":             "DELETE",
									"total_bytes":      int64(totalCount),
									"processed_bytes":  done,
									"total_chunks":     totalCount,
									"completed_chunks": int(done),
									"progress_percent": pct,
									"status":           "ACTIVE",
									"log_message":      fmt.Sprintf("Deleted part %d of %d from Discord", done, totalCount),
								},
							})
						}
					}
				}()
			}

			wg.Wait()

			finalDone := atomic.LoadInt64(&completedCount)
			finalFailed := atomic.LoadInt64(&failedCount)

			log.Printf("cleanup finished: %d deleted, %d failed out of %d total messages", finalDone, finalFailed, totalCount)

			s.broadcastJSON(map[string]any{
				"type": "telemetry",
				"data": map[string]any{
					"job_id":           deleteJobID,
					"file_name":        displayName,
					"type":             "DELETE",
					"total_bytes":      int64(totalCount),
					"processed_bytes":  int64(totalCount),
					"total_chunks":     totalCount,
					"completed_chunks": totalCount,
					"progress_percent": 100.0,
					"status":           "COMPLETED",
					"log_message":      fmt.Sprintf("Deleted %d parts from Discord", finalDone),
				},
			})
		}()
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":               true,
		"status":           "deleted",
		"job_id":           deleteJobID,
		"deleted_count":    deletedCount,
		"discord_messages": len(discordDeletes),
	})
}

func (s *Server) handleCatalogSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	if !s.ensureMasterKeyUnlocked() {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": "Drive is locked. Enter password to unlock."})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	msgID, err := s.catalog.Sync(ctx)
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
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": "Drive is locked. Enter password to unlock."})
		return
	}

	var req struct {
		MetadataChannelID string `json:"metadata_channel_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	manifest, err := s.catalog.Restore(ctx, req.MetadataChannelID)
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
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if jobs == nil {
		jobs = []db.JobRecord{}
	}
	jsonResponse(w, http.StatusOK, jobs)
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid request body"})
		return
	}

	if req.JobID == "all" || req.JobID == "" {
		s.uploader.CancelAllJobs()
		s.downloader.CancelAllJobs()
		jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "all paused"})
		return
	}

	s.uploader.CancelJob(req.JobID)
	s.downloader.CancelJob(req.JobID)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "paused"})
}

func (s *Server) syncAllBotGuilds(ctx context.Context) (int, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	tokens, _ := s.db.GetUniqueBotTokens()
	mainToken, _ := s.db.GetSetting("bot_token")
	if mainToken != "" {
		hasMain := false
		for _, t := range tokens {
			if t == mainToken {
				hasMain = true
				break
			}
		}
		if !hasMain {
			tokens = append(tokens, mainToken)
		}
	}

	if len(tokens) == 0 {
		return 0, nil
	}

	existingNodes, _ := s.db.GetAllBotNodes()
	existingGuildMap := make(map[string]bool)
	for _, n := range existingNodes {
		if n.GuildID != "" {
			existingGuildMap[n.GuildID] = true
		}
	}

	newDetected := 0

	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		botDetails, err := s.discord.GetBotDetails(ctx, token)
		if err != nil {
			continue
		}

		guilds, err := s.discord.GetBotGuilds(ctx, token)
		if err != nil {
			continue
		}

		if len(guilds) == 0 {
			placeholderID := "bot_" + botDetails.ID
			_ = s.db.UpsertBotNode(&db.BotNodeRecord{
				ID:           placeholderID,
				BotToken:     token,
				BotID:        botDetails.ID,
				GuildID:      "",
				BotName:      botDetails.Username,
				GuildName:    "No Server Joined Yet",
				Status:       "Pending Server",
				PingMs:       0,
				ChannelCount: 0,
				StorageBytes: 0,
				InviteURL:    botDetails.InviteURL,
				CreatedAt:    time.Now().Unix(),
				LastSeen:     time.Now().Unix(),
			})
			continue
		}

		// Bot is in servers! Clean up placeholder
		_ = s.db.DeleteBotNode("bot_" + botDetails.ID)

		for _, g := range guilds {
			if g.ID == "" {
				continue
			}

			isNew := !existingGuildMap[g.ID]
			existingGuildMap[g.ID] = true

			channels, _ := s.db.GetChannelsByGuild(g.ID)
			if len(channels) == 0 {
				client := s.discord.CloneForToken(token)
				res, err := client.SetupServer(ctx, g.ID)
				if err == nil && res != nil {
					for _, ch := range res.StorageChannels {
						if ch.Channel.ID != "" && ch.Webhook.URL != "" {
							_ = s.db.AddChannelFull(ch.Channel.ID, ch.Channel.Name, ch.Webhook.URL, g.ID, token, ch.Channel.ParentID, false)
						}
					}
					if res.MetadataChannel.ID != "" {
						_ = s.db.AddChannelWithGuild(res.MetadataChannel.ID, res.MetadataChannel.Name, "", g.ID, token, true)
					}
					channels, _ = s.db.GetChannelsByGuild(g.ID)
				}
			}

			sBytes := s.db.GuildStorageBytes(g.ID)
			node := &db.BotNodeRecord{
				ID:           g.ID,
				BotToken:     token,
				BotID:        botDetails.ID,
				GuildID:      g.ID,
				BotName:      botDetails.Username,
				GuildName:    g.Name,
				Status:       "Active",
				PingMs:       35,
				ChannelCount: len(channels),
				StorageBytes: sBytes,
				InviteURL:    botDetails.InviteURL,
				CreatedAt:    time.Now().Unix(),
				LastSeen:     time.Now().Unix(),
			}
			_ = s.db.UpsertBotNode(node)

			if isNew {
				newDetected++
			}
		}
	}

	if newDetected > 0 {
		s.broadcastJSON(map[string]any{
			"type":      "servers_changed",
			"new_count": newDetected,
		})
		s.broadcastJSON(map[string]any{
			"type": "status_changed",
		})
	}

	return newDetected, nil
}

// Provision seeds core configuration (bot token, primary server and its base
// storage category), registers every server the bot can reach as a replication
// target, and provisions the initial storage pools. Safe to call on every
// boot; it never duplicates channels that already exist.
func (s *Server) Provision(ctx context.Context, botToken, primaryGuildID, baseCategoryID string) error {
	if botToken != "" {
		_ = s.db.SetSetting("bot_token", botToken)
		s.discord.SetBotToken(botToken)
		s.discord.SetPoolPrefix("files")
	}
	if primaryGuildID != "" {
		_ = s.db.SetSetting("guild_id", primaryGuildID)
	}
	if baseCategoryID != "" {
		_ = s.db.SetSetting("base_category_id", baseCategoryID)
		if primaryGuildID != "" {
			s.discord.SetBaseCategory(primaryGuildID, baseCategoryID)
		}
	}
	if _, err := s.syncAllBotGuilds(ctx); err != nil {
		return fmt.Errorf("server sync failed: %w", err)
	}
	return nil
}

func (s *Server) handleBots(w http.ResponseWriter, r *http.Request) {
	_, _ = s.syncAllBotGuilds(r.Context())
	nodes, _ := s.db.GetAllBotNodes()

	type BotItem struct {
		ID           string   `json:"id"`
		BotID        string   `json:"bot_id"`
		BotName      string   `json:"bot_name"`
		BotToken     string   `json:"bot_token"`
		Status       string   `json:"status"`
		PingMs       int64    `json:"ping_ms"`
		ChannelCount int      `json:"channel_count"`
		StorageBytes int64    `json:"storage_bytes"`
		InviteURL    string   `json:"invite_url"`
		GuildID      string   `json:"guild_id"`
		GuildName    string   `json:"guild_name"`
		GuildCount   int      `json:"guild_count"`
		Guilds       []string `json:"guilds"`
		CreatedAt    int64    `json:"created_at"`
		LastSeen     int64    `json:"last_seen"`
	}

	botMap := make(map[string]*BotItem)
	for _, n := range nodes {
		key := n.BotID
		if key == "" {
			key = n.BotToken
		}
		if key == "" {
			key = n.ID
		}

		item, exists := botMap[key]
		if !exists {
			item = &BotItem{
				ID:           n.ID,
				BotID:        n.BotID,
				BotName:      n.BotName,
				BotToken:     n.BotToken,
				Status:       n.Status,
				PingMs:       n.PingMs,
				ChannelCount: n.ChannelCount,
				StorageBytes: n.StorageBytes,
				InviteURL:    n.InviteURL,
				GuildID:      n.GuildID,
				GuildName:    n.GuildName,
				GuildCount:   0,
				Guilds:       make([]string, 0),
				CreatedAt:    n.CreatedAt,
				LastSeen:     n.LastSeen,
			}
			botMap[key] = item
		} else {
			item.ChannelCount += n.ChannelCount
			item.StorageBytes += n.StorageBytes
			if n.PingMs > 0 && (item.PingMs == 0 || n.PingMs < item.PingMs) {
				item.PingMs = n.PingMs
			}
			if n.Status == "Active" {
				item.Status = "Active"
			}
		}

		if n.GuildID != "" {
			item.GuildCount++
			gName := n.GuildName
			if gName == "" {
				gName = n.GuildID
			}
			item.Guilds = append(item.Guilds, gName)
		}
	}

	result := make([]*BotItem, 0, len(botMap))
	for _, item := range botMap {
		if item.GuildCount > 0 {
			item.GuildName = strings.Join(item.Guilds, ", ")
			item.Status = "Active"
		} else if item.Status == "" {
			item.Status = "Pending Server"
		}
		result = append(result, item)
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":    true,
		"nodes": result,
		"count": len(result),
	})
}

func (s *Server) handleAddBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		BotToken string `json:"bot_token"`
		Token    string `json:"token"`
		GuildID  string `json:"guild_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid request"})
		return
	}

	token := strings.TrimSpace(req.BotToken)
	if token == "" {
		token = strings.TrimSpace(req.Token)
	}
	if token == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Bot token is required"})
		return
	}

	req.GuildID = strings.TrimSpace(req.GuildID)

	botDetails, err := s.discord.GetBotDetails(r.Context(), token)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	curBotToken, _ := s.db.GetSetting("bot_token")
	if curBotToken == "" {
		_ = s.db.SetSetting("bot_token", token)
	}

	if req.GuildID != "" {
		client := discord.NewClient(token)
		res, setupErr := client.SetupServer(r.Context(), req.GuildID)
		if setupErr == nil && res != nil {
			for _, ch := range res.StorageChannels {
				if ch.Channel.ID != "" && ch.Webhook.URL != "" {
					_ = s.db.AddChannelWithGuild(ch.Channel.ID, ch.Channel.Name, ch.Webhook.URL, req.GuildID, token, false)
				}
			}
			if res.MetadataChannel.ID != "" {
				_ = s.db.AddChannelWithGuild(res.MetadataChannel.ID, res.MetadataChannel.Name, "", req.GuildID, token, true)
			}
		}
	}

	_, _ = s.syncAllBotGuilds(r.Context())

	s.broadcastJSON(map[string]any{
		"type": "servers_changed",
	})
	s.broadcastJSON(map[string]any{
		"type": "status_changed",
	})

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":         true,
		"bot":        botDetails,
		"node":       map[string]any{"bot_name": botDetails.Username, "invite_url": botDetails.InviteURL, "id": botDetails.ID},
		"invite_url": botDetails.InviteURL,
		"message":    fmt.Sprintf("Bot %s connected! Use the Invite button to add it to your server.", botDetails.Username),
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

	// Delete matching bot records by ID, BotID, or GuildID
	nodes, _ := s.db.GetAllBotNodes()
	for _, n := range nodes {
		if n.ID == req.ID || n.BotID == req.ID || n.GuildID == req.ID || n.BotToken == req.ID {
			_ = s.db.DeleteBotNode(n.ID)
		}
	}

	s.broadcastJSON(map[string]any{"type": "servers_changed"})
	s.broadcastJSON(map[string]any{"type": "status_changed"})

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "deleted"})
}

func (s *Server) handleTestBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		BotToken string `json:"bot_token"`
		Token    string `json:"token"`
		GuildID  string `json:"guild_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid request"})
		return
	}

	token := strings.TrimSpace(req.BotToken)
	if token == "" {
		token = strings.TrimSpace(req.Token)
	}
	if token == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Bot token is required"})
		return
	}

	botDetails, err := s.discord.GetBotDetails(r.Context(), token)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":         true,
		"bot":        botDetails,
		"node":       map[string]any{"bot_name": botDetails.Username, "invite_url": botDetails.InviteURL, "id": botDetails.ID},
		"invite_url": botDetails.InviteURL,
	})
}

func (s *Server) handleServersSync(w http.ResponseWriter, r *http.Request) {
	newCount, err := s.syncAllBotGuilds(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	nodes, _ := s.db.GetAllBotNodes()
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":        true,
		"new_count": newCount,
		"total":     len(nodes),
	})
}

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	_, _ = s.syncAllBotGuilds(r.Context())
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
		InviteURL        string `json:"invite_url,omitempty"`
		LastSeen         int64  `json:"last_seen"`
	}

	servers := []ServerItem{}
	seen := make(map[string]bool)

	for _, n := range nodes {
		if n.GuildID == "" {
			continue // Skip placeholder pending bots from servers list
		}
		seen[n.GuildID] = true
		sBytes := s.db.GuildStorageBytes(n.GuildID)
		servers = append(servers, ServerItem{
			GuildID:          n.GuildID,
			GuildName:        n.GuildName,
			BotName:          n.BotName,
			Status:           n.Status,
			PingMs:           n.PingMs,
			ChannelCount:     n.ChannelCount,
			StorageBytes:     sBytes,
			StorageFormatted: formatBytes(sBytes),
			InviteURL:        n.InviteURL,
			LastSeen:         n.LastSeen,
		})
	}

	if guildID != "" && !seen[guildID] {
		gName := "My Discord Server"
		chCount := 4
		if botToken != "" {
			name, count, err := s.discord.GuildInfo(r.Context(), botToken, guildID)
			if err == nil && name != "" {
				gName = name
				chCount = count
			}
		}
		sBytes := s.db.GuildStorageBytes(guildID)
		servers = append(servers, ServerItem{
			GuildID:          guildID,
			GuildName:        gName,
			BotName:          "Main Bot",
			Status:           "Active",
			PingMs:           35,
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

	var allNodes []db.BotNodeRecord
	seen := make(map[string]bool)

	for _, n := range nodes {
		seen[n.GuildID] = true
		allNodes = append(allNodes, n)
	}

	if guildID != "" && !seen[guildID] && botToken != "" {
		allNodes = append(allNodes, db.BotNodeRecord{
			ID:           guildID,
			BotToken:     botToken,
			GuildID:      guildID,
			BotName:      "Main Bot",
			GuildName:    "My Discord Server",
			Status:       "Active",
			PingMs:       45,
			ChannelCount: 4,
			StorageBytes: s.db.GuildStorageBytes(guildID),
			CreatedAt:    time.Now().Unix(),
			LastSeen:     time.Now().Unix(),
		})
	}

	updatedNodes := s.discord.CheckNodes(r.Context(), allNodes)

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
	for i := range updatedNodes {
		updatedNodes[i].StorageBytes = s.db.GuildStorageBytes(updatedNodes[i].GuildID)
		_ = s.db.UpsertBotNode(&updatedNodes[i])

		formattedList = append(formattedList, ServerHealthItem{
			GuildID:          updatedNodes[i].GuildID,
			GuildName:        updatedNodes[i].GuildName,
			BotName:          updatedNodes[i].BotName,
			Status:           updatedNodes[i].Status,
			PingMs:           updatedNodes[i].PingMs,
			ChannelCount:     updatedNodes[i].ChannelCount,
			StorageBytes:     updatedNodes[i].StorageBytes,
			StorageFormatted: formatBytes(updatedNodes[i].StorageBytes),
			LastSeen:         updatedNodes[i].LastSeen,
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

	res, err := s.discord.SetupServerWithToken(r.Context(), req.BotToken, req.GuildID)
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
		"status":           "Storage channels configured",
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
		"ok":            true,
		"has_password":  passHash != "",
		"is_unlocked":   isUnlocked,
		"auth_required": s.authEnabled() && !s.sessionValid(r),
		"has_session":   s.sessionValid(r),
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
		var err error
		salt, err = hex.DecodeString(saltHex)
		if err != nil || len(salt) == 0 {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Corrupt salt — refusing to unlock"})
			return
		}
	} else {
		var err error
		salt, err = crypto.GenerateSalt()
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := s.db.SetSetting("master_salt_hex", hex.EncodeToString(salt)); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}

	masterKey := crypto.DeriveKey(req.Password, salt)
	computedHash := crypto.ComputeSHA256(masterKey)

	if passHash != "" && passHash != computedHash {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Incorrect password"})
		return
	}

	if passHash == "" {
		if err := s.db.SetSetting("master_pass_hash", computedHash); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}

	s.uploader.SetMasterKey(masterKey)
	s.downloader.SetMasterKey(masterKey)
	s.catalog.SetMasterKey(masterKey)

	sessionToken, err := s.issueSession()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.setSessionCookie(w, sessionToken)

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":      true,
		"status":  "unlocked",
		"session": sessionToken,
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

	salt, err := crypto.GenerateSalt()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	masterKey := crypto.DeriveKey(req.Password, salt)
	passHash := crypto.ComputeSHA256(masterKey)

	if err := s.db.SetSetting("master_salt_hex", hex.EncodeToString(salt)); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := s.db.SetSetting("master_pass_hash", passHash); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	s.uploader.SetMasterKey(masterKey)
	s.downloader.SetMasterKey(masterKey)
	s.catalog.SetMasterKey(masterKey)

	sessionToken, err := s.issueSession()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.setSessionCookie(w, sessionToken)

	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":      true,
		"status":  "password_set",
		"session": sessionToken,
	})
}

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
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
