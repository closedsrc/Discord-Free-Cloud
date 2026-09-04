package server

// qol.go adds the drive-style endpoints the redesigned dashboard uses:
// storage-health inspection, rename/move, favorites, trash and global search.
// Everything here stays inside the existing auth wrapper; no handler ever
// returns a bot token, webhook URL or the master key itself.

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"discord-free-cloud/internal/db"
)

// FileView is what the API exposes about a file: the catalog fields plus
// storage health. A file is split into parts (chunk indexes) and every part may
// be stored more than once, on more than one server — so parts, attachments and
// servers are three different numbers and are reported separately.
type FileView struct {
	db.FileRecord
	AttachmentCount int         `json:"attachment_count"` // completed Discord attachments, replicas included
	ChunkCount      int         `json:"chunk_count"`      // distinct parts the file is split into
	ReplicaServers  int         `json:"replica_servers"`  // servers holding at least one part
	Health          string      `json:"health"`           // ok | partial | empty
	Parts           []ChunkPart `json:"parts,omitempty"`  // per-part breakdown (details only)
}

// ChunkPart is one part of a file, collapsed across its replicas.
type ChunkPart struct {
	Index   int    `json:"index"`
	Size    int    `json:"size"`
	Copies  int    `json:"copies"`
	Servers int    `json:"servers"`
	Status  string `json:"status"` // COMPLETED once at least one copy is stored
}

func (s *Server) fileHealth(fileID string) (int, int, int, string) {
	chunks, err := s.db.GetAllChunksForFile(fileID)
	if err != nil || len(chunks) == 0 {
		return 0, 0, 0, "empty"
	}
	attachments := 0
	parts := map[int]bool{}
	storedParts := map[int]bool{}
	guilds := map[string]bool{}
	for _, c := range chunks {
		parts[c.ChunkIndex] = true
		if c.Status == db.StatusCompleted {
			attachments++
			storedParts[c.ChunkIndex] = true
			if c.GuildID != "" {
				guilds[c.GuildID] = true
			}
		}
	}
	health := "ok"
	if len(storedParts) < len(parts) {
		health = "partial"
	}
	if len(storedParts) == 0 {
		health = "empty"
	}
	return attachments, len(parts), len(guilds), health
}

// filePartsBreakdown collapses the chunk rows of a file into one entry per part,
// so the dashboard can show "part 3 — 2 copies on 2 servers" instead of listing
// the same part once per replica.
func (s *Server) filePartsBreakdown(fileID string) []ChunkPart {
	chunks, err := s.db.GetAllChunksForFile(fileID)
	if err != nil || len(chunks) == 0 {
		return nil
	}
	order := []int{}
	byIndex := map[int]*ChunkPart{}
	guilds := map[int]map[string]bool{}
	for _, c := range chunks {
		p := byIndex[c.ChunkIndex]
		if p == nil {
			p = &ChunkPart{Index: c.ChunkIndex, Size: c.ChunkSize, Status: "PENDING"}
			byIndex[c.ChunkIndex] = p
			guilds[c.ChunkIndex] = map[string]bool{}
			order = append(order, c.ChunkIndex)
		}
		if c.Status == db.StatusCompleted {
			p.Copies++
			p.Status = db.StatusCompleted
			if c.ChunkSize > 0 {
				p.Size = c.ChunkSize
			}
			if c.GuildID != "" {
				guilds[c.ChunkIndex][c.GuildID] = true
			}
		}
	}
	sort.Ints(order)
	out := make([]ChunkPart, 0, len(order))
	for _, i := range order {
		p := byIndex[i]
		p.Servers = len(guilds[i])
		out = append(out, *p)
	}
	return out
}

func (s *Server) decorate(files []db.FileRecord) []FileView {
	out := make([]FileView, 0, len(files))
	for _, f := range files {
		v := FileView{FileRecord: f}
		if !f.IsDir {
			v.AttachmentCount, v.ChunkCount, v.ReplicaServers, v.Health = s.fileHealth(f.ID)
		}
		out = append(out, v)
	}
	return out
}

// handleFilesView serves GET /api/files/view?parent_id=&search=&view=&sort=&dir=
// view=all|recents|favorites|trash; sort=name|size|date; dir=asc|desc.
func (s *Server) handleFilesView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}
	parentID := r.URL.Query().Get("parent_id")
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	view := r.URL.Query().Get("view")
	sortBy := r.URL.Query().Get("sort")
	desc := r.URL.Query().Get("dir") == "desc"

	var files []db.FileRecord
	var err error

	switch {
	case search != "":
		files, err = s.db.SearchFiles(search)
	case view == "favorites":
		files, err = s.db.ListFavorites()
	case view == "trash":
		files, err = s.db.ListTrash()
	case view == "recents":
		files, err = s.db.ListRecent(50)
	default:
		files, err = s.db.ListFiles(parentID)
	}
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// Simple insertion sort — catalogs are small and this keeps ordering stable.
	less := func(i, j int) bool { return false }
	switch sortBy {
	case "size":
		less = func(i, j int) bool { return files[i].Size < files[j].Size }
	case "date":
		less = func(i, j int) bool { return files[i].ModTime < files[j].ModTime }
	default:
		less = func(i, j int) bool { return files[i].Name < files[j].Name }
	}
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && less(j, j-1); j-- {
			files[j], files[j-1] = files[j-1], files[j]
		}
	}
	if desc {
		for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
			files[i], files[j] = files[j], files[i]
		}
	}

	if files == nil {
		files = []db.FileRecord{}
	}
	jsonResponse(w, http.StatusOK, s.decorate(files))
}

// handleFilesBatch handles POST /api/files/batch {ids:[...], action:"favorite"|"unfavorite"|"trash"|"restore"}
func (s *Server) handleFilesBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}
	var req struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ids and action required"})
		return
	}
	switch req.Action {
	case "favorite":
		for _, id := range req.IDs {
			_ = s.db.SetFavorite(id, true)
		}
	case "unfavorite":
		for _, id := range req.IDs {
			_ = s.db.SetFavorite(id, false)
		}
	case "trash":
		for _, id := range req.IDs {
			_ = s.db.SetTrashed(id, true)
		}
	case "restore":
		for _, id := range req.IDs {
			_ = s.db.SetTrashed(id, false)
		}
	default:
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown action"})
		return
	}
	s.broadcastJSON(map[string]any{"type": "files_changed"})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// handleFileUpdate handles POST /api/files/update {id, action, value}
// action: rename | move | favorite | unfavorite | trash | restore
func (s *Server) handleFileUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}
	var req struct {
		ID     string `json:"id"`
		Action string `json:"action"`
		Value  string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "id required"})
		return
	}

	f, err := s.db.GetFile(req.ID)
	if err != nil || f == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "file not found"})
		return
	}

	switch req.Action {
	case "rename":
		newName := filepath.Base(strings.ReplaceAll(strings.TrimSpace(req.Value), "\\", "/"))
		if newName == "" || newName == "." || newName == ".." {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid name"})
			return
		}
		parentPath := ""
		if f.ParentID != "" {
			if p, _ := s.db.GetFile(f.ParentID); p != nil {
				parentPath = p.Path
			}
		}
		newPath := filepath.ToSlash(filepath.Join(parentPath, newName))
		if existing, _ := s.db.GetFileByPath(newPath); existing != nil && existing.ID != f.ID {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "name already in use"})
			return
		}
		// For directories: rename all descendants (path prefix rewrite).
		if f.IsDir {
			descendants, _ := s.db.GetAllFiles()
			oldPrefix := f.Path + "/"
			for _, sf := range descendants {
				if strings.HasPrefix(sf.Path, oldPrefix) {
					_ = s.db.RenameFile(sf.ID, sf.Name, rebasePath(sf.Path, f.Path, newPath))
				}
			}
		}
		if err := s.db.RenameFile(f.ID, newName, newPath); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	case "move":
		// value = destination folder ID ("", or a directory id)
		var destPath string
		if req.Value != "" {
			dest, err := s.db.GetFile(req.Value)
			if err != nil || dest == nil || !dest.IsDir {
				jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "destination folder not found"})
				return
			}
			destPath = dest.Path
		}
		newPath := filepath.ToSlash(filepath.Join(destPath, f.Name))
		if existing, _ := s.db.GetFileByPath(newPath); existing != nil && existing.ID != f.ID {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "name already in use in destination"})
			return
		}
		if f.IsDir {
			// refuse to move a folder into itself or its own subtree
			if req.Value == f.ID || strings.HasPrefix(newPath, f.Path+"/") {
				jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "cannot move a folder into itself"})
				return
			}
			descendants, _ := s.db.GetAllFiles()
			oldPrefix := f.Path + "/"
			for _, sf := range descendants {
				if strings.HasPrefix(sf.Path, oldPrefix) {
					newChild := filepath.ToSlash(filepath.Join(newPath, strings.TrimPrefix(sf.Path, oldPrefix)))
					_ = s.db.RenameFile(sf.ID, sf.Name, newChild)
				}
			}
		}
		if err := s.db.RenameFile(f.ID, f.Name, newPath); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	case "favorite":
		if err := s.db.SetFavorite(f.ID, true); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	case "unfavorite":
		if err := s.db.SetFavorite(f.ID, false); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	case "trash":
		if err := s.db.SetTrashed(f.ID, true); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	case "restore":
		if err := s.db.SetTrashed(f.ID, false); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	default:
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown action"})
		return
	}

	s.broadcastJSON(map[string]any{"type": "files_changed", "file_id": f.ID, "parent_id": f.ParentID})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "updated"})
}

// rebasePath rewrites an old parent prefix to a new parent prefix.
func rebasePath(oldPath, oldParent, newParent string) string {
	return newParent + strings.TrimPrefix(oldPath, oldParent)
}

// handleFileDetails serves GET /api/files/details?file_id=
func (s *Server) handleFileDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}
	fileID := r.URL.Query().Get("file_id")
	if fileID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "file_id required"})
		return
	}
	f, err := s.db.GetFile(fileID)
	if err != nil || f == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "file not found"})
		return
	}
	v := s.decorate([]db.FileRecord{*f})
	if !f.IsDir {
		v[0].Parts = s.filePartsBreakdown(f.ID)
	}
	jsonResponse(w, http.StatusOK, v[0])
}

// handleFileRawChunk serves GET /api/files/raw_chunk?file_id=&chunk_index=
// Downloads the raw ENCRYPTED wrapper stored on Discord — for inspection only.
// The master key never leaves the server and the payload is never decrypted here.
func (s *Server) handleFileRawChunk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "Method not allowed"})
		return
	}
	fileID := r.URL.Query().Get("file_id")
	idxStr := r.URL.Query().Get("chunk_index")
	if fileID == "" || idxStr == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "file_id and chunk_index required"})
		return
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid chunk_index"})
		return
	}
	chunks, err := s.db.GetAllChunksForFile(fileID)
	if err != nil || len(chunks) == 0 {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no chunks for file"})
		return
	}
	for _, c := range chunks {
		if c.ChunkIndex == idx && c.Status == db.StatusCompleted && c.AttachmentURL != "" {
			http.Redirect(w, r, c.AttachmentURL, http.StatusFound)
			return
		}
	}
	jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "chunk not found"})
}

// handleFileTrash serves POST /api/files/trash {id} or {ids: [...]}
func (s *Server) handleFileTrash(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID  string   `json:"id"`
		IDs []string `json:"ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ID != "" {
		req.IDs = append(req.IDs, req.ID)
	}
	for _, id := range req.IDs {
		_ = s.db.SetTrashed(id, true)
	}
	s.broadcastJSON(map[string]any{"type": "files_changed"})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// handleFileRestore serves POST /api/files/restore {id} or {ids: [...]}
func (s *Server) handleFileRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID  string   `json:"id"`
		IDs []string `json:"ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ID != "" {
		req.IDs = append(req.IDs, req.ID)
	}
	for _, id := range req.IDs {
		_ = s.db.SetTrashed(id, false)
	}
	s.broadcastJSON(map[string]any{"type": "files_changed"})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// handleFileFavorite serves POST /api/files/favorite {id, value}
func (s *Server) handleFileFavorite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Value bool   `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "id required"})
		return
	}
	if err := s.db.SetFavorite(req.ID, req.Value); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.broadcastJSON(map[string]any{"type": "files_changed"})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// handleFileRename serves POST /api/files/rename {id, name}
func (s *Server) handleFileRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" || req.Name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "id and name required"})
		return
	}
	f, err := s.db.GetFile(req.ID)
	if err != nil || f == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "file not found"})
		return
	}
	newName := filepath.Base(strings.ReplaceAll(strings.TrimSpace(req.Name), "\\", "/"))
	if newName == "" || newName == "." || newName == ".." {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid name"})
		return
	}
	parentPath := ""
	if f.ParentID != "" {
		if p, _ := s.db.GetFile(f.ParentID); p != nil {
			parentPath = p.Path
		}
	}
	newPath := filepath.ToSlash(filepath.Join(parentPath, newName))
	if existing, _ := s.db.GetFileByPath(newPath); existing != nil && existing.ID != f.ID {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "name already in use"})
		return
	}
	if f.IsDir {
		descendants, _ := s.db.GetAllFiles()
		oldPrefix := f.Path + "/"
		for _, sf := range descendants {
			if strings.HasPrefix(sf.Path, oldPrefix) {
				_ = s.db.RenameFile(sf.ID, sf.Name, filepath.ToSlash(filepath.Join(newPath, strings.TrimPrefix(sf.Path, oldPrefix))))
			}
		}
	}
	if err := s.db.RenameFile(f.ID, newName, newPath); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.broadcastJSON(map[string]any{"type": "files_changed", "file_id": f.ID, "parent_id": f.ParentID})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "renamed"})
}

// handleFileMove serves POST /api/files/move {id, parent_id}
func (s *Server) handleFileMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string `json:"id"`
		ParentID string `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "id required"})
		return
	}
	f, err := s.db.GetFile(req.ID)
	if err != nil || f == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "file not found"})
		return
	}
	destPath := ""
	if req.ParentID != "" {
		dest, err := s.db.GetFile(req.ParentID)
		if err != nil || dest == nil || !dest.IsDir {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "destination folder not found"})
			return
		}
		destPath = dest.Path
	}
	newPath := filepath.ToSlash(filepath.Join(destPath, f.Name))
	// Prefer the truthful conflicts over a lucky lookup: a stale trashed row
	// from before the ListFiles precedence fix must not block a move, but the
	// row being moved must not block itself either.
	live, _ := s.db.GetFileByPath(newPath)
	if live != nil && live.ID != f.ID && live.TrashedAt == nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "name already in use in destination"})
		return
	}
	if newPath == f.Path && req.ParentID == f.ParentID {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "noop"})
		return
	}
	if f.IsDir {
		if req.ParentID == f.ID || strings.HasPrefix(newPath, f.Path+"/") {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "cannot move a folder into itself"})
			return
		}
		descendants, _ := s.db.GetAllFiles()
		oldPrefix := f.Path + "/"
		for _, sf := range descendants {
			if strings.HasPrefix(sf.Path, oldPrefix) {
				_ = s.db.RenameFile(sf.ID, sf.Name, filepath.ToSlash(filepath.Join(newPath, strings.TrimPrefix(sf.Path, oldPrefix))))
			}
		}
	}
	if err := s.db.RenameFile(f.ID, f.Name, newPath); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := s.db.UpdateFileParent(f.ID, req.ParentID); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.broadcastJSON(map[string]any{"type": "files_changed", "file_id": f.ID, "parent_id": req.ParentID})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "moved"})
}
