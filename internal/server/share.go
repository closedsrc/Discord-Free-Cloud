package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Share links. A share is a random capability stored as a SHA-256 hash with an
// expiry, pointing at one file. The plaintext token never touches the DB; the
// public download path /api/share/<token> carries it and is exempt from API
// auth (it is its own credential). Creation/revoke/list all require write
// scope. Shared downloads honour Range so a shared video still seeks.
// ---------------------------------------------------------------------------

func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !s.ensureMasterKeyUnlocked() {
		jsonError(w, http.StatusLocked, "drive is locked")
		return
	}

	var req struct {
		FileID    string `json:"file_id"`
		ExpiresIn int64  `json:"expires_in_seconds"` // 0 = default 7 days
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.FileID == "" {
		jsonError(w, http.StatusBadRequest, "file_id is required")
		return
	}
	rec, err := s.db.GetFile(req.FileID)
	if err != nil || rec == nil || rec.IsDir {
		jsonError(w, http.StatusNotFound, "file not found")
		return
	}

	ttl := req.ExpiresIn
	if ttl <= 0 {
		ttl = 7 * 24 * 3600
	}
	if ttl > 365*24*3600 {
		ttl = 365 * 24 * 3600
	}

	token, err := generateToken()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not generate share token")
		return
	}
	sum := sha256.Sum256([]byte("dfc-share:" + token))
	hash := hex.EncodeToString(sum[:])
	expires := time.Now().Unix() + ttl

	if err := s.db.CreateShare(hash, rec.ID, expires); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	base := shareBaseURL(r)
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":         true,
		"token":      token,
		"url":        base + "/share/" + token,
		"file_id":    rec.ID,
		"name":       rec.Name,
		"expires_at": expires,
	})
}

func (s *Server) handleShareList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	fileID := r.URL.Query().Get("file_id")
	if fileID == "" {
		jsonError(w, http.StatusBadRequest, "file_id is required")
		return
	}
	shares, err := s.db.ListSharesForFile(fileID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Token hashes are not secret but are useless to the caller; expose only
	// expiry/download metadata keyed by an opaque id.
	type view struct {
		ID        string `json:"id"`
		ExpiresAt int64  `json:"expires_at"`
		CreatedAt int64  `json:"created_at"`
		Downloads int64  `json:"downloads"`
		Expired   bool   `json:"expired"`
	}
	out := make([]view, 0, len(shares))
	for _, sh := range shares {
		out = append(out, view{
			ID:        sh.TokenHash,
			ExpiresAt: sh.ExpiresAt,
			CreatedAt: sh.CreatedAt,
			Downloads: sh.Downloads,
			Expired:   time.Now().Unix() > sh.ExpiresAt,
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "shares": out})
}

// handleShareRevoke takes the share's token hash (returned by list) so the
// plaintext token never has to be re-sent to revoke it.
func (s *Server) handleShareRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		jsonError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.db.DeleteShare(req.ID); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSharePublic serves GET /api/share/<token>. It resolves the token to a
// file and delegates to the same streaming path (with Range support) that the
// authenticated download uses, but it enforces the expiry itself.
func (s *Server) handleSharePublic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		jsonError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	token := trimAPIPrefix(r.URL.Path, "/api/share/")
	if token == "" {
		jsonError(w, http.StatusNotFound, "share token required")
		return
	}
	if !s.ensureMasterKeyUnlocked() {
		jsonError(w, http.StatusLocked, "this share is temporarily unavailable")
		return
	}

	sum := sha256.Sum256([]byte("dfc-share:" + token))
	hash := hex.EncodeToString(sum[:])
	share, err := s.db.GetShare(hash)
	if err != nil || share == nil {
		jsonError(w, http.StatusNotFound, "share not found")
		return
	}
	if time.Now().Unix() > share.ExpiresAt {
		_ = s.db.DeleteShare(hash)
		jsonError(w, http.StatusGone, "share has expired")
		return
	}
	rec, err := s.db.GetFile(share.FileID)
	if err != nil || rec == nil {
		jsonError(w, http.StatusNotFound, "file no longer exists")
		return
	}

	_ = s.db.TouchShare(hash)

	// Reuse the streaming logic by rewriting the request as an inline download.
	q := url.Values{}
	q.Set("file_id", share.FileID)
	if r.URL.Query().Get("inline") == "1" {
		q.Set("inline", "1")
	}
	r.URL.RawQuery = q.Encode()
	s.handleStreamDownload(w, r)
}

// shareBaseURL reconstructs the public base for building share links from the
// request, honouring the Cloudflare tunnel headers when present.
func shareBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return fmt.Sprintf("%s://%s/api", scheme, host)
}

func trimAPIPrefix(path, prefix string) string {
	if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
		return path[len(prefix):]
	}
	return ""
}

// handleVerifyFile runs a full integrity pass over one file. Read scope is
// enough; it is a heavy but strictly read-only operation.
func (s *Server) handleVerifyFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !s.ensureMasterKeyUnlocked() {
		jsonError(w, http.StatusLocked, "drive is locked")
		return
	}
	var req struct {
		FileID string `json:"file_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.FileID == "" {
		jsonError(w, http.StatusBadRequest, "file_id is required")
		return
	}
	rec, err := s.db.GetFile(req.FileID)
	if err != nil || rec == nil {
		jsonError(w, http.StatusNotFound, "file not found")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	start := uuid.New().String()
	res, verr := s.downloader.VerifyFile(ctx, req.FileID)
	if verr != nil {
		jsonResponse(w, http.StatusOK, map[string]any{
			"ok":      false,
			"run_id":  start,
			"file_id": req.FileID,
			"name":    rec.Name,
			"error":   verr.Error(),
		})
		return
	}
	res.FileID = req.FileID
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":     true,
		"run_id": start,
		"result": res,
	})
}
