package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
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
	// The row's public id is a separate random handle. Listing must hand the UI
	// an opaque id to revoke by — never the token hash, which is secret material
	// that would turn /api/shares/list into a recovery oracle for every link.
	id := uuid.New().String()
	expires := time.Now().Unix() + ttl

	if err := s.db.CreateShare(id, hash, rec.ID, expires); err != nil {
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
	// Token hashes are not usable by the caller; expose expiry/download metadata
	// keyed by the row's opaque public id. The list must never hand out token
	// material — revoke resolves the id to the row server-side.
	type view struct {
		ID        string `json:"id"`
		FileID    string `json:"file_id"`
		FileName  string `json:"file_name"`
		ExpiresAt int64  `json:"expires_at"`
		CreatedAt int64  `json:"created_at"`
		Downloads int64  `json:"downloads"`
		Expired   bool   `json:"expired"`
	}
	out := make([]view, 0, len(shares))
	for _, sh := range shares {
		name := ""
		if rec, _ := s.db.GetFile(sh.FileID); rec != nil {
			name = rec.Name
		}
		out = append(out, view{
			ID: sh.ID, FileID: sh.FileID, FileName: name,
			ExpiresAt: sh.ExpiresAt,
			CreatedAt: sh.CreatedAt,
			Downloads: sh.Downloads,
			Expired:   time.Now().Unix() > sh.ExpiresAt,
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "shares": out})
}

// handleShareListAll serves GET /api/shares/list_all: every share with its file
// name, newest first, in one call. The Links view used to walk the whole tree
// and call /api/shares/list per file (~2× files requests), which meant a 30s+
// "Loading links…" that most users read as "sharing is broken". Read scope is
// enough; nothing secret leaves the server (ids are opaque, names are the
// file's own name).
func (s *Server) handleShareListAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	all, err := s.db.ListAllShares(time.Now().Unix())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type view struct {
		ID        string `json:"id"`
		FileID    string `json:"file_id"`
		FileName  string `json:"file_name"`
		ExpiresAt int64  `json:"expires_at"`
		CreatedAt int64  `json:"created_at"`
		Downloads int64  `json:"downloads"`
		Expired   bool   `json:"expired"`
	}
	out := make([]view, 0, len(all))
	for _, sh := range all {
		out = append(out, view{
			ID: sh.ID, FileID: sh.FileID, FileName: sh.FileName,
			ExpiresAt: sh.ExpiresAt, CreatedAt: sh.CreatedAt,
			Downloads: sh.Downloads, Expired: sh.Expired,
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "shares": out})
}

// handleShareRevoke takes the share's opaque id (returned by list) and resolves
// it server-side, so the secret token material never has to be re-sent — or
// handed to the browser in the first place — to revoke a link.
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
	// Resolve through the opaque public id. The old code deleted by token hash,
	// which required list to hand those hashes to the browser — and thereby
	// turned the Links view into a download-everything oracle for anonymous
	// callers, since unauthenticated /api/share/<token> honours any token.
	rec, err := s.db.GetShareByID(req.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rec == nil {
		jsonError(w, http.StatusNotFound, "share not found")
		return
	}
	if err := s.db.DeleteShareByID(req.ID); err != nil {
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
		_ = s.db.DeleteShareByID(share.ID)
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

// DFCShareHost is the configured public host share links are built on when the
// request itself does not carry it. It lives next to the env parsing (in
// auth.go) so there is exactly one place that reads PUBLIC_* env vars.
func DFCShareHost() string {
	host := strings.TrimSpace(os.Getenv("DFC_SHARE_HOST"))
	if host != "" {
		return host
	}
	return "drive.otherworld.bond"
}

// shareBaseURL reconstructs the public base for building share links from the
// request, honouring the Cloudflare tunnel headers when present. Using r.Host
// directly would embed whatever internal host answered (127.0.0.1 when the
// backend tools upload, or no host at all) and ship dead localhost links to the
// user — exactly what happened to the links created by the backup scripts.
func shareBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	if host == "" || host != "drive.otherworld.bond" {
		if h := DFCShareHost(); h != "" {
			host = h
			scheme = "https"
		}
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
