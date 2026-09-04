package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// API tokens. DFC ships with a password-unlock gate only — the master password
// lives in memory, and every /api/* call trusts whoever can reach the port. The
// app is meant to sit on 127.0.0.1 behind a Cloudflare tunnel here, so the
// backup scripts and the Dockyard panel need something that is NOT the master
// password to authenticate with. Two token kinds:
//
//   - read  : GET on read-only endpoints (status, files listing, download, share)
//   - write : everything read can do plus upload / delete / mkdir / catalog sync
//
// Tokens are stored in the settings table as sha256 hashes, never plaintext.
// The first token is bootstrapped from the environment so a headless install
// (systemd EnvironmentFile=/root/dfcloud.env) is scriptable from the start:
//
//	DFC_API_TOKEN_WRITE   – seeded once, then it lives in the DB
//	DFC_API_TOKEN_READ    – same, read-scoped
//
// Requests authenticate with  Authorization: Bearer <token>  or
//  X-API-Token: <token>  (the header exists because an <img>/<video> tag cannot
// set Authorization; share URLs use a separate signed-token path instead).
// ---------------------------------------------------------------------------

const (
	settingsKeyWriteToken = "api_token_write_hash"
	settingsKeyReadToken  = "api_token_read_hash"
)

type tokenScope int

const (
	scopeNone tokenScope = iota
	scopeRead
	scopeWrite
)

type ctxKey int

const principalKey ctxKey = 1

// authEnabled is false when no tokens have been seeded, which keeps the local
// dashboard working unchanged on a fresh install until you run create-token.
func (s *Server) authEnabled() bool {
	w, _ := s.db.GetSetting(settingsKeyWriteToken)
	r, _ := s.db.GetSetting(settingsKeyReadToken)
	return w != "" || r != ""
}

// Public access modes, set by PUBLIC_ACCESS. This is how the drive is published
// without a password prompt; it is a deliberate, reversible choice:
//
//	off  (default) every API call needs a token or an unlocked browser session
//	read anonymous callers get the read scope: browse, preview, download
//	full anonymous callers get the write scope: uploads, renames and DELETES too
//
// "full" means anyone who finds the URL can erase the drive, backups included.
const (
	publicOff  = "off"
	publicRead = "read"
	publicFull = "full"
)

func publicMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PUBLIC_ACCESS"))) {
	case "read", "public", "1", "true", "yes", "on":
		return publicRead
	case "full", "write", "all":
		return publicFull
	default:
		return publicOff
	}
}

// anonymousScope is the scope handed to a caller with no credential at all.
func anonymousScope() tokenScope {
	switch publicMode() {
	case publicRead:
		return scopeRead
	case publicFull:
		return scopeWrite
	default:
		return scopeNone
	}
}

// signinPasswordMatches reports whether the supplied password is the sign-in
// password from SIGNIN_PASSWORD.
//
// This is an access credential, NOT key material. The encryption key is derived
// from MASTER_PASSWORD and loaded headlessly at boot; a sign-in password only
// decides who may drive an already-unlocked service. Keeping the two separate is
// what makes "change the login password" safe: re-deriving the master key would
// orphan every chunk already in Discord, backups included.
func signinPasswordMatches(supplied string) bool {
	want := strings.TrimSpace(os.Getenv("SIGNIN_PASSWORD"))
	if want == "" || supplied == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(want)) == 1
}

// hashToken is the storage form; tokens are random hex so no KDF is needed.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte("dfc-api-token:" + token))
	return hex.EncodeToString(sum[:])
}

// tokenFromRequest pulls the credential from either accepted header.
func tokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if h := r.Header.Get("X-API-Token"); h != "" {
		return strings.TrimSpace(h)
	}
	return ""
}

// classify returns the scope a token holds.
func (s *Server) classify(token string) tokenScope {
	if token == "" {
		return scopeNone
	}
	want := hashToken(token)
	if w, _ := s.db.GetSetting(settingsKeyWriteToken); w != "" &&
		subtle.ConstantTimeCompare([]byte(want), []byte(w)) == 1 {
		return scopeWrite
	}
	if r, _ := s.db.GetSetting(settingsKeyReadToken); r != "" &&
		subtle.ConstantTimeCompare([]byte(r), []byte(want)) == 1 {
		return scopeRead
	}
	return scopeNone
}

// readOnlyRoute lists endpoints a read token may call. Anything not listed and
// not in publicRoutes requires a write token. Shares are capabilities that
// carry their own token in the path, so /api/share/<token> never appears here.
var readOnlyRoutes = map[string]bool{
	"/api/status":          true,
	"/api/stats":           true,
	"/api/files":           true,
	"/api/download":        true,
	"/api/download/file":   true,
	"/api/download/check":  true,
	"/api/jobs":            true,
	"/api/channels":        true,
	"/api/servers":         true,
	"/api/verify":          true,
	"/api/files/view":      true,
	"/api/files/details":   true,
	"/api/files/raw_chunk": true,
	"/api/shares/list":     true,
	"/api/auth/status":     true,
}

// publicRoutes stay unauthenticated: the unlock dance, health probes and share
// links (which carry their own signature).
var publicRoutes = map[string]bool{
	"/api/auth/status":       true,
	"/api/auth/unlock":       true,
	"/api/auth/set_password": true,
	"/api/auth/lock":         true, // clears only the caller's own cookie
	"/api/create-token":      true, // localhost bootstrap, enforced in the handler
}

// isPublicPath covers the exact-match routes plus the share download prefix.
func isPublicPath(path string) bool {
	if publicRoutes[path] {
		return true
	}
	return strings.HasPrefix(path, "/api/share/")
}

func routeRequiresRead(path string) bool {
	if readOnlyRoutes[path] {
		return true
	}
	// /ws is the dashboard progress feed — read scope is enough.
	return path == "/ws"
}

// requireAuth wraps the mux. When no tokens exist yet it lets everything
// through (first-run dashboard). When tokens exist, the browser session cookie
// also works so the embedded dashboard stays usable after unlock.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if !strings.HasPrefix(path, "/api/") && path != "/ws" {
			next.ServeHTTP(w, r) // static frontend
			return
		}
		if isPublicPath(path) {
			next.ServeHTTP(w, r)
			return
		}

		// Public mode is applied BEFORE the token-seeded gate. Otherwise
		// PUBLIC_ACCESS=read would be a no-op on a fresh install that has no API
		// tokens yet, and the first-run dashboard would silently grant everyone
		// full write. A configured anonymous scope is authoritative here.
		anon := anonymousScope()
		if !s.authEnabled() && anon == scopeNone {
			next.ServeHTTP(w, r) // first-run dashboard, lock still off
			return
		}

		scope := s.classify(tokenFromRequest(r))
		if scope == scopeNone && s.sessionValid(r) {
			scope = scopeWrite // unlocked browser session keeps working
		}
		if scope == scopeNone {
			scope = anon // PUBLIC_ACCESS=read|full
		}
		if scope == scopeNone {
			jsonError(w, http.StatusUnauthorized, "missing or invalid API token")
			return
		}
		if !routeRequiresRead(path) && scope != scopeWrite {
			jsonError(w, http.StatusForbidden, "read-only access")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, scope)))
	})
}

// principalScope reports whether requireAuth stamped a concrete scope on the
// request. ok is false for a first-run request, where authEnabled was false and
// requireAuth passed the caller through without classifying them — that caller
// has full access, so per-handler write gates must not reject them for lacking a
// scope header. Only an explicitly scoped-down caller (a read token, or the read
// public mode) yields ok=true with a non-write scope.
func principalScope(r *http.Request) (tokenScope, bool) {
	v := r.Context().Value(principalKey)
	sc, ok := v.(tokenScope)
	return sc, ok
}

// scopeAllowsWrite reports whether the caller behind r may perform a write. It is
// needed for routes that requireAuth treats as "read" (so a read token or the
// anonymous read public mode can reach them) but whose POST form mutates the
// server — e.g. /api/download writing a file to local_dest. A first-run request
// (no stamped scope) is permitted; an authenticated read scope is denied.
func scopeAllowsWrite(r *http.Request) bool {
	sc, stamped := principalScope(r)
	if !stamped {
		return true // first-run dashboard, no scope enforced
	}
	return sc == scopeWrite
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

// sessionValid reuses the existing unlock gate: the dashboard holds the master
// key in memory after unlock and the frontend has no cookie yet, so we treat a
// same-origin request that arrives with a valid session id as trusted.
// (Kept deliberately narrow: only the dashboard's own /ws and same-origin
// XHR carry it.)
func (s *Server) sessionValid(r *http.Request) bool {
	token := r.Header.Get("X-DFC-Session")
	if token == "" {
		token = r.URL.Query().Get("session")
	}
	if token == "" {
		if c, err := r.Cookie(sessionCookieName); err == nil {
			token = c.Value
		}
	}
	if token == "" {
		return false
	}
	return s.checkSession(token)
}

// ---------------------------------------------------------------------------
// Browser sessions. A successful unlock hands the browser a random session id
// that is stored server-side in memory with an expiry. The id travels back as
// an HttpOnly cookie (so plain <img>/<video>/<a> navigations are authenticated
// without leaking the value to JS), and — because native media elements cannot
// set a custom header but *can* be given a signed query — the frontend also
// mirrors it into X-DFC-Session / ?session= for same-origin fetch calls that
// the cookie already covers. The session grants write scope: whoever holds the
// master password can already do everything the dashboard can.
// ---------------------------------------------------------------------------

const (
	sessionCookieName = "dfc_session"
	sessionLifetime   = 12 * time.Hour
)

// hashSessionID is the storage form of a session token; the plaintext never
// leaves the browser, so a DB/log leak cannot be replayed.
func hashSessionID(token string) string {
	sum := sha256.Sum256([]byte("dfc-session:" + token))
	return hex.EncodeToString(sum[:])
}

// checkSession reports whether a session id is known and unexpired, refreshing
// its expiry on use so an actively-browsed dashboard is not cut off mid-task.
func (s *Server) checkSession(token string) bool {
	if token == "" {
		return false
	}
	id := hashSessionID(token)

	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	exp, ok := s.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, id)
		return false
	}
	s.sessions[id] = time.Now().Add(sessionLifetime)
	return true
}

// revokeAllSessions drops every browser session. It is called when the master
// password is replaced: the person who could rotate the key must not have
// everyone else's already-open write sessions survive the rotation.
func (s *Server) revokeAllSessions() {
	s.sessMu.Lock()
	s.sessions = make(map[string]time.Time)
	s.sessMu.Unlock()
}

// issueSession mints a new browser session id and returns the plaintext token
// (shown to the caller once) plus its hashed storage key.
func (s *Server) issueSession() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte("dfc-session:" + token))
	id := hex.EncodeToString(sum[:])

	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	if s.sessions == nil { // constructed without NewServer; never panic on unlock
		s.sessions = make(map[string]time.Time)
	}
	s.sessions[id] = time.Now().Add(sessionLifetime)
	s.pruneSessionsLocked()
	return token, nil
}

// pruneSessionsLocked drops expired sessions. Caller must hold sessMu.
func (s *Server) pruneSessionsLocked() {
	now := time.Now()
	for id, exp := range s.sessions {
		if now.After(exp) {
			delete(s.sessions, id)
		}
	}
}

// setSessionCookie writes the HttpOnly session cookie on a successful unlock.
// Secure is omitted deliberately: the dashboard is reached over the Cloudflare
// HTTPS tunnel in production but the local loopback origin is plain HTTP, and
// a Secure cookie would then be dropped on http://127.0.0.1.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionLifetime.Seconds()),
	})
}

// handleAuthLock drops the caller's session and clears the cookie.
func (s *Server) handleAuthLock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessMu.Lock()
		delete(s.sessions, hashSessionID(c.Value))
		s.sessMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "locked"})
}

// generateToken returns 32 bytes of hex.
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// EnsureSeededTokens reads DFC_API_TOKEN_READ / DFC_API_TOKEN_WRITE from the
// environment and stores their hashes if no token is configured yet. Returns
// true if auth is now enabled.
func (s *Server) EnsureSeededTokens() bool {
	if s.authEnabled() {
		return true
	}
	seeded := false
	if w := strings.TrimSpace(os.Getenv("DFC_API_TOKEN_WRITE")); w != "" {
		_ = s.db.SetSetting(settingsKeyWriteToken, hashToken(w))
		seeded = true
	}
	if r := strings.TrimSpace(os.Getenv("DFC_API_TOKEN_READ")); r != "" {
		_ = s.db.SetSetting(settingsKeyReadToken, hashToken(r))
		seeded = true
	}
	return seeded || s.authEnabled()
}

// handleCreateToken mints (or rotates) a token and returns the plaintext
// exactly once. Authorization model:
//
//   - An existing write token or a valid browser session may rotate either
//     scope.
//   - The loopback bootstrap (a request on 127.0.0.1 with no Cloudflare
//     header, reachable only from a root shell on the box — tunnel traffic
//     always carries Cf-Connecting-Ip) may seed a scope that is CURRENTLY
//     EMPTY, so a fresh headless install is scriptable from the start. It can
//     NOT rotate a scope that already holds a token, so an accidental local
//     curl can never silently invalidate a live token. To rotate after losing
//     the write token, clear the settings row in Postgres and re-seed.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		Scope string `json:"scope"` // "read" | "write"; default write
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope == "" {
		scope = "write"
	}
	if scope != "read" && scope != "write" {
		jsonError(w, http.StatusBadRequest, "scope must be read or write")
		return
	}

	key := settingsKeyWriteToken
	if scope == "read" {
		key = settingsKeyReadToken
	}
	existing, _ := s.db.GetSetting(key)

	host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	direct := splitErr == nil && (host == "127.0.0.1" || host == "::1") && r.Header.Get("Cf-Connecting-Ip") == ""
	tokenOrSession := s.classify(tokenFromRequest(r)) == scopeWrite || s.sessionValid(r)
	if !tokenOrSession {
		if !direct {
			jsonError(w, http.StatusUnauthorized, "write token or browser session required")
			return
		}
		if existing != "" {
			jsonError(w, http.StatusForbidden, "scope already has a token; rotation needs the write token or a session")
			return
		}
	}

	token, err := generateToken()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not generate token")
		return
	}
	if err := s.db.SetSetting(key, hashToken(token)); err != nil {
		jsonError(w, http.StatusInternalServerError, "could not store token")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "scope": scope, "token": token})
}
