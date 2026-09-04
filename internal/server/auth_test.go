package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestServerWithSessions() *Server {
	return &Server{sessions: make(map[string]time.Time)}
}

func TestGenerateTokenUniquenessAndLength(t *testing.T) {
	a, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := generateToken()
	if len(a) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(a))
	}
	if a == b {
		t.Fatal("tokens must be unique")
	}
}

func TestSessionIssueCheckRevoke(t *testing.T) {
	s := newTestServerWithSessions()

	tok, err := s.issueSession()
	if err != nil {
		t.Fatal(err)
	}
	if !s.checkSession(tok) {
		t.Fatal("freshly issued session must be valid")
	}
	if s.checkSession("deadbeef") {
		t.Fatal("unknown session must be invalid")
	}
	if s.checkSession("") {
		t.Fatal("empty session must be invalid")
	}

	// A session forced into the past is rejected and removed on next lookup.
	sum := hashSessionID(tok)
	s.sessMu.Lock()
	s.sessions[sum] = time.Now().Add(-time.Second)
	s.sessMu.Unlock()
	if s.checkSession(tok) {
		t.Fatal("expired session must be rejected")
	}
	s.sessMu.Lock()
	_, present := s.sessions[sum]
	s.sessMu.Unlock()
	if present {
		t.Fatal("expired session must be deleted on lookup")
	}
}

func TestSessionSlidingExpiry(t *testing.T) {
	s := newTestServerWithSessions()
	tok, _ := s.issueSession()

	// Age the session to 1 hour remaining, then use it: expiry must refresh.
	sum := hashSessionID(tok)
	s.sessMu.Lock()
	s.sessions[sum] = time.Now().Add(1 * time.Hour)
	s.sessMu.Unlock()

	if !s.checkSession(tok) {
		t.Fatal("session must still be valid")
	}
	s.sessMu.Lock()
	exp := s.sessions[sum]
	s.sessMu.Unlock()
	if time.Until(exp) < 6*time.Hour {
		t.Fatalf("session expiry should have been refreshed near %v, got %v", sessionLifetime, time.Until(exp))
	}
}

func TestHandleAuthLockClearsCookieSession(t *testing.T) {
	s := newTestServerWithSessions()
	tok, _ := s.issueSession()

	req := httptest.NewRequest(http.MethodPost, "/api/auth/lock", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tok})
	rec := httptest.NewRecorder()
	s.handleAuthLock(rec, req)

	if s.checkSession(tok) {
		t.Fatal("lock must revoke the caller's session")
	}
	cookies := rec.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("lock must clear the session cookie")
	}
}

func TestPublicPathClassification(t *testing.T) {
	cases := map[string]bool{
		"/api/auth/status":       true,
		"/api/auth/unlock":       true,
		"/api/auth/set_password": true,
		"/api/auth/lock":         true,
		"/api/share/abc123":      true,
		"/api/files":             false,
		"/api/upload/file":       false,
		"/api/shares/create":     false,
	}
	for path, want := range cases {
		if got := isPublicPath(path); got != want {
			t.Errorf("isPublicPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestReadRouteClassification(t *testing.T) {
	if !routeRequiresRead("/api/download/file") {
		t.Error("download/file must be read-allowed")
	}
	if !routeRequiresRead("/api/verify") {
		t.Error("verify must be read-allowed")
	}
	if routeRequiresRead("/api/upload/file") {
		t.Error("upload must require write")
	}
	if routeRequiresRead("/api/delete") {
		t.Error("delete must require write")
	}
}

func TestPublicModeParsing(t *testing.T) {
	cases := map[string]string{
		"":         publicOff,
		"off":      publicOff,
		"nonsense": publicOff,
		"read":     publicRead,
		"1":        publicRead,
		"true":     publicRead,
		" On ":     publicRead,
		"full":     publicFull,
		"write":    publicFull,
		"all":      publicFull,
	}
	for env, want := range cases {
		t.Setenv("PUBLIC_ACCESS", env)
		if got := publicMode(); got != want {
			t.Errorf("PUBLIC_ACCESS=%q -> %q, want %q", env, got, want)
		}
	}
}

func TestAnonymousScopePerMode(t *testing.T) {
	t.Setenv("PUBLIC_ACCESS", "off")
	if anonymousScope() != scopeNone {
		t.Error("off must grant nothing")
	}
	t.Setenv("PUBLIC_ACCESS", "read")
	if anonymousScope() != scopeRead {
		t.Error("read must grant the read scope")
	}
	t.Setenv("PUBLIC_ACCESS", "full")
	if anonymousScope() != scopeWrite {
		t.Error("full must grant the write scope")
	}
}

// A published drive must stay published for readers and closed for writers: this
// is the guard that keeps PUBLIC_ACCESS=read from handing out /api/delete.
func TestRequireAuthPublicReadBlocksWrites(t *testing.T) {
	t.Setenv("PUBLIC_ACCESS", "read")
	if anonymousScope() != scopeRead {
		t.Fatal("precondition: read mode")
	}
	if !routeRequiresRead("/api/files/view") {
		t.Error("browsing must be allowed with the read scope")
	}
	// /api/shares/list_all is the Links view's single bulk call: it carries file
	// names, which are metadata a read caller may already list — but it must not
	// leak token material, and creating shares stays write-only.
	if !routeRequiresRead("/api/shares/list_all") {
		t.Error("the bulk share list must be allowed with the read scope")
	}
	for _, p := range []string{"/api/delete", "/api/upload/file", "/api/files/rename", "/api/files/move", "/api/files/trash", "/api/shares/create", "/api/create-token", "/api/bots/add"} {
		if routeRequiresRead(p) {
			t.Errorf("%s must require the write scope", p)
		}
	}
}

// routeRequiresRead cannot distinguish the read GET form of /api/download from
// the write POST form (which writes an arbitrary local_dest). The handler closes
// that hole with scopeAllowsWrite, which trusts the scope requireAuth stamped on
// the request: a stamped read scope is denied, a write scope is allowed, and an
// unstamped (first-run) caller is allowed.
func TestScopeAllowsWriteEnforcesReadToken(t *testing.T) {
	reqWith := func(scope tokenScope, stamp bool) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/download", nil)
		if stamp {
			r = r.WithContext(context.WithValue(r.Context(), principalKey, scope))
		}
		return r
	}
	if !scopeAllowsWrite(reqWith(scopeNone, false)) {
		t.Error("unstamped (first-run) caller must be allowed")
	}
	if scopeAllowsWrite(reqWith(scopeRead, true)) {
		t.Error("read scope must not permit the local-write download")
	}
	if !scopeAllowsWrite(reqWith(scopeWrite, true)) {
		t.Error("write scope must permit the local-write download")
	}
}

// Replacing the master password must not leave prior browser sessions alive.
func TestRevokeAllSessionsDropsEveryone(t *testing.T) {
	s := newTestServerWithSessions()
	a, _ := s.issueSession()
	b, _ := s.issueSession()
	if !s.checkSession(a) || !s.checkSession(b) {
		t.Fatal("precondition: both sessions valid")
	}
	s.revokeAllSessions()
	if s.checkSession(a) || s.checkSession(b) {
		t.Fatal("password rotation must revoke all previously issued sessions")
	}
}

// be accepted when empty, and must not be comparable with a prefix.
func TestSigninPasswordMatches(t *testing.T) {
	t.Setenv("SIGNIN_PASSWORD", "")
	if signinPasswordMatches("") || signinPasswordMatches("anything") {
		t.Error("unset SIGNIN_PASSWORD must accept nothing")
	}
	t.Setenv("SIGNIN_PASSWORD", "  unit-access-password  ")
	if !signinPasswordMatches("unit-access-password") {
		t.Error("surrounding whitespace in the env value must be trimmed")
	}
	if signinPasswordMatches("") {
		t.Error("an empty attempt must never match")
	}
	if signinPasswordMatches("avix") || signinPasswordMatches("avixyy") || signinPasswordMatches("AVIXY") {
		t.Error("only the exact password may match")
	}
}
