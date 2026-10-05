package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v5"

	"github.com/zimwip/goap/internal/credsvc"
	"github.com/zimwip/goap/pkg/authz"
)

// fakeCredentials is an in-memory Credentials for local auth tests (ADR 0040): passwords in clear, sessions kept by
// a credsvc service on a memory store (ADR 0045).
type fakeCredentials struct {
	passwords map[string]string
	sessions  *credsvc.Service
	// down makes the session checks fail, as an unreachable credentials service
	down bool
}

func (f *fakeCredentials) svc() *credsvc.Service {
	if f.sessions == nil {
		f.sessions = &credsvc.Service{Store: credsvc.NewMemoryStore()}
	}
	return f.sessions
}

func (f *fakeCredentials) StartSession(ctx context.Context, subject string, maxAge time.Duration) (string, error) {
	return f.svc().StartSession(ctx, subject, maxAge)
}

func (f *fakeCredentials) SessionActive(ctx context.Context, id, subject string) (bool, error) {
	if f.down {
		return false, errors.New("credentials unreachable")
	}
	return f.svc().SessionActive(ctx, id, subject)
}

func (f *fakeCredentials) EndSession(ctx context.Context, id string) error {
	return f.svc().EndSession(ctx, id)
}

func (f *fakeCredentials) EndSessions(ctx context.Context, subject string) error {
	return f.svc().EndSessions(ctx, subject)
}

func (f *fakeCredentials) Register(_ context.Context, subject, password string) error {
	if _, ok := f.passwords[subject]; ok {
		return connect.NewError(connect.CodeAlreadyExists, errors.New("already exists"))
	}
	f.passwords[subject] = password
	return nil
}

func (f *fakeCredentials) Verify(_ context.Context, subject, password string) (bool, error) {
	return f.passwords[subject] != "" && f.passwords[subject] == password, nil
}

// anyProject lets every caller work on every project.
func anyProject(context.Context, authz.Principal, string) (bool, error) { return true, nil }

func TestAuthAndRouting(t *testing.T) {
	var gotSubject, gotRoles, gotProject string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSubject, gotRoles, gotProject = r.Header.Get(HeaderSubject), r.Header.Get(HeaderRoles), r.Header.Get(HeaderProject)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	e := echo.New()
	secret := []byte(strings.Repeat("s", 32))
	if err := Mount(e, Config{AuthMode: "hs256", JWTSecret: secret, DevTokens: true,
		Routes: []Route{{Prefix: "/goap.graph.v1.GraphService/", Upstream: upstream.URL}}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e)
	defer srv.Close()

	call := func(token string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/goap.graph.v1.GraphService/ListBaselines", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(HeaderSubject, "spoofed")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := call(""); code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}
	if code := call("garbage"); code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}
	resp, err := http.Post(srv.URL+"/auth/dev-token", "application/json", strings.NewReader(`{"subject":"alice","org":"acme","project":"PROJ-X","roles":["admin"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var tok struct{ Token string }
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if code := call(tok.Token); code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if gotSubject != "alice" || gotRoles != "admin" || gotProject != "PROJ-X" {
		t.Fatalf("identity not propagated: %q %q %q", gotSubject, gotRoles, gotProject)
	}

	// switching project reissues the token with the same subject/org/roles but a new project (ADR 0039)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/dev-token/project", strings.NewReader(`{"project":"PROJ-Y"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	swResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var swTok struct{ Token string }
	_ = json.NewDecoder(swResp.Body).Decode(&swTok)
	swResp.Body.Close()
	if swResp.StatusCode != http.StatusOK || swTok.Token == "" {
		t.Fatalf("switch project: status %d, token %q", swResp.StatusCode, swTok.Token)
	}
	if code := call(swTok.Token); code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if gotSubject != "alice" || gotRoles != "admin" || gotProject != "PROJ-Y" {
		t.Fatalf("switched identity not propagated: %q %q %q", gotSubject, gotRoles, gotProject)
	}
	// a bogus/expired token cannot switch project
	badReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/dev-token/project", strings.NewReader(`{"project":"PROJ-Z"}`))
	badReq.Header.Set("Authorization", "Bearer garbage")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("switch project with a bad token: expected 401, got %d", badResp.StatusCode)
	}
	st, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	var status PlatformStatus
	_ = json.NewDecoder(st.Body).Decode(&status)
	st.Body.Close()
	// the fake upstream answers 200 on /readyz
	if status.Status != "ok" || len(status.Services) != 1 || status.Services[0].Name != "graph" {
		t.Fatalf("status %+v", status)
	}
}

// Local auth (ADR 0040): a subject registers, logs in and calls through with the token; a duplicate
// registration and a wrong password are refused; /api/auth/config tells the web which mode is active.
func TestLocalAuth(t *testing.T) {
	var gotSubject string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSubject = r.Header.Get(HeaderSubject)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	e := echo.New()
	secret := []byte(strings.Repeat("s", 32))
	creds := &fakeCredentials{passwords: map[string]string{}}
	var declared []string // subjects OnSignIn was called for (ADR 0042: the User node is created at sign-in)
	onSignIn := func(_ context.Context, subject string) error {
		if subject == "broken" {
			return errors.New("graph unavailable")
		}
		declared = append(declared, subject)
		return nil
	}
	if err := Mount(e, Config{AuthMode: "local", JWTSecret: secret, Credentials: creds, OnSignIn: onSignIn, ProjectAccess: anyProject,
		Routes: []Route{{Prefix: "/goap.graph.v1.GraphService/", Upstream: upstream.URL}}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e)
	defer srv.Close()

	cfgResp, err := http.Get(srv.URL + "/api/auth/config")
	if err != nil {
		t.Fatal(err)
	}
	var authCfg struct{ AuthMode string }
	_ = json.NewDecoder(cfgResp.Body).Decode(&authCfg)
	cfgResp.Body.Close()
	if authCfg.AuthMode != "local" {
		t.Fatalf("auth config = %+v", authCfg)
	}

	register := func(subject, password string) (int, string) {
		resp, err := http.Post(srv.URL+"/auth/register", "application/json",
			strings.NewReader(`{"subject":"`+subject+`","password":"`+password+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		var tok struct{ Token string }
		_ = json.NewDecoder(resp.Body).Decode(&tok)
		resp.Body.Close()
		return resp.StatusCode, tok.Token
	}
	login := func(subject, password string) (int, string) {
		resp, err := http.Post(srv.URL+"/auth/login", "application/json",
			strings.NewReader(`{"subject":"`+subject+`","password":"`+password+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		var tok struct{ Token string }
		_ = json.NewDecoder(resp.Body).Decode(&tok)
		resp.Body.Close()
		return resp.StatusCode, tok.Token
	}
	call := func(token string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/goap.graph.v1.GraphService/ListBaselines", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code, tok := register("alice", "correct horse"); code != http.StatusOK || tok == "" {
		t.Fatalf("register: %d %q", code, tok)
	}
	if code, _ := register("alice", "another password"); code != http.StatusConflict {
		t.Fatalf("duplicate register: %d", code)
	}
	if code, _ := login("alice", "wrong password"); code != http.StatusUnauthorized {
		t.Fatalf("login with wrong password: %d", code)
	}
	code, tok := login("alice", "correct horse")
	if code != http.StatusOK || tok == "" {
		t.Fatalf("login: %d %q", code, tok)
	}
	if code := call(tok); code != http.StatusOK {
		t.Fatalf("call with login token: %d", code)
	}
	if gotSubject != "alice" {
		t.Fatalf("subject propagated = %q", gotSubject)
	}

	if !slices.Equal(declared, []string{"alice", "alice"}) {
		t.Fatalf("OnSignIn must run on register and on login only: %v", declared)
	}
	// a user that cannot be declared in the graph is not signed in
	creds.passwords["broken"] = "some password"
	if code, tok := login("broken", "some password"); code != http.StatusServiceUnavailable || tok != "" {
		t.Fatalf("login when OnSignIn fails: %d %q", code, tok)
	}

	// switching project reissues the token in local mode too (ADR 0039)
	switchReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/dev-token/project", strings.NewReader(`{"project":"PROJ-A"}`))
	switchReq.Header.Set("Content-Type", "application/json")
	switchReq.Header.Set("Authorization", "Bearer "+tok)
	switchResp, err := http.DefaultClient.Do(switchReq)
	if err != nil {
		t.Fatal(err)
	}
	var switched struct{ Token string }
	_ = json.NewDecoder(switchResp.Body).Decode(&switched)
	switchResp.Body.Close()
	if switchResp.StatusCode != http.StatusOK || switched.Token == "" {
		t.Fatalf("switch project: %d %q", switchResp.StatusCode, switched.Token)
	}

	// logout has nothing to revoke (stateless HS256) but must answer
	logoutReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+tok)
	logoutResp, err := http.DefaultClient.Do(logoutReq)
	if err != nil {
		t.Fatal(err)
	}
	logoutResp.Body.Close()
	if logoutResp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", logoutResp.StatusCode)
	}
}

// A valid token is refreshed with a fresh expiry, keeping its identity and its sign-in time; an expired one is
// refused as such (401 "token expired"), and so is a refresh past the maximum session.
func TestRefreshToken(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	creds := &fakeCredentials{passwords: map[string]string{}}
	newServer := func(ttl, maxSession time.Duration) *httptest.Server {
		e := echo.New()
		if err := Mount(e, Config{AuthMode: "local", JWTSecret: secret, Credentials: creds, TokenTTL: ttl, MaxSession: maxSession, ProjectAccess: anyProject}); err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(e)
	}
	post := func(srv *httptest.Server, path, token, body string) (int, issued, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var raw json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		var out issued
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out, string(raw)
	}
	claimsOf := func(tok string) *Claims {
		c, err := parseToken(Config{JWTSecret: secret}, "Bearer "+tok)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	srv := newServer(time.Hour, 24*time.Hour)
	defer srv.Close()
	code, reg, _ := post(srv, "/auth/register", "", `{"subject":"alice","password":"correct horse"}`)
	if code != http.StatusOK || reg.Token == "" || time.Until(reg.ExpiresAt) < 59*time.Minute || time.Until(reg.ExpiresAt) > time.Hour {
		t.Fatalf("register: %d %+v", code, reg)
	}
	signedInAt := claimsOf(reg.Token).AuthTime.Time

	// switch project, then refresh: subject, project and sign-in time are kept
	code, sw, _ := post(srv, "/auth/dev-token/project", reg.Token, `{"project":"PROJ-A"}`)
	if code != http.StatusOK {
		t.Fatalf("switch: %d", code)
	}
	code, ref, _ := post(srv, "/auth/refresh", sw.Token, "")
	if code != http.StatusOK || ref.Token == "" {
		t.Fatalf("refresh: %d %+v", code, ref)
	}
	if c := claimsOf(ref.Token); c.Subject != "alice" || c.Project != "PROJ-A" || !c.AuthTime.Time.Equal(signedInAt) {
		t.Fatalf("refreshed claims: %+v", c)
	}
	if code, _, _ := post(srv, "/auth/refresh", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("refresh without a token: %d", code)
	}

	// an expired token is refused, and says so
	expired := newServer(time.Millisecond, 24*time.Hour)
	defer expired.Close()
	_, short, _ := post(expired, "/auth/login", "", `{"subject":"alice","password":"correct horse"}`)
	time.Sleep(1100 * time.Millisecond) // JWT times have a one-second resolution
	if code, _, body := post(expired, "/auth/refresh", short.Token, ""); code != http.StatusUnauthorized || !strings.Contains(body, "token expired") {
		t.Fatalf("refresh of an expired token: %d %s", code, body)
	}

	// past the maximum session, a valid token is not refreshed any more
	old := newServer(time.Hour, time.Nanosecond)
	defer old.Close()
	_, fresh, _ := post(old, "/auth/login", "", `{"subject":"alice","password":"correct horse"}`)
	if code, _, body := post(old, "/auth/refresh", fresh.Token, ""); code != http.StatusUnauthorized || !strings.Contains(body, "session expired") {
		t.Fatalf("refresh past the maximum session: %d %s", code, body)
	}
}

// Signing out ends the session server side (ADR 0045): its tokens, and the ones refreshed from it, are refused
// at once; signing out everywhere ends the subject's other sessions; a token from before sessions is refused; a
// session check the credentials service cannot answer is a 503, not a sign-out.
func TestLogoutRevokesTheSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer upstream.Close()
	secret := []byte(strings.Repeat("s", 32))
	creds := &fakeCredentials{passwords: map[string]string{"alice": "correct horse"}}
	cfg := Config{AuthMode: "local", JWTSecret: secret, Credentials: creds, SessionCheckTTL: time.Hour,
		Routes: []Route{{Prefix: "/goap.graph.v1.GraphService/", Upstream: upstream.URL}}}
	e := echo.New()
	if err := Mount(e, cfg); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e)
	defer srv.Close()
	post := func(path, token, body string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var raw json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		return resp.StatusCode, string(raw)
	}
	login := func() string {
		code, body := post("/auth/login", "", `{"subject":"alice","password":"correct horse"}`)
		var out issued
		_ = json.Unmarshal([]byte(body), &out)
		if code != http.StatusOK || out.Token == "" {
			t.Fatalf("login: %d %s", code, body)
		}
		return out.Token
	}
	call := func(token string) (int, string) {
		return post("/goap.graph.v1.GraphService/ListBaselines", token, "{}")
	}

	tok := login()
	if code, _ := call(tok); code != http.StatusOK {
		t.Fatalf("call with a fresh token: %d", code)
	}
	code, body := post("/auth/refresh", tok, "")
	var refreshed issued
	_ = json.Unmarshal([]byte(body), &refreshed)
	if code != http.StatusOK {
		t.Fatalf("refresh: %d %s", code, body)
	}
	// sign out with the first token: the refreshed one, of the same session, is refused at once
	if code, _ := post("/auth/logout", tok, ""); code != http.StatusNoContent {
		t.Fatalf("logout: %d", code)
	}
	if code, body := call(refreshed.Token); code != http.StatusUnauthorized || !strings.Contains(body, "session ended") {
		t.Fatalf("call after sign-out: %d %s", code, body)
	}
	if code, _ := post("/auth/refresh", refreshed.Token, ""); code != http.StatusUnauthorized {
		t.Fatalf("refresh after sign-out: %d", code)
	}

	// signing out everywhere ends the other sessions too (checked afresh by a refresh)
	a, b := login(), login()
	if code, _ := post("/auth/logout", a, `{"everywhere":true}`); code != http.StatusNoContent {
		t.Fatalf("logout everywhere: %d", code)
	}
	if code, _ := post("/auth/refresh", b, ""); code != http.StatusUnauthorized {
		t.Fatalf("another session after signing out everywhere: %d", code)
	}

	// a token from before sessions (no sid) is refused
	old, err := sign(cfg, "alice", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := call(old); code != http.StatusUnauthorized {
		t.Fatalf("token without a session: %d", code)
	}

	// the credentials service down: a session never checked is a 503, a known one keeps its last state
	known := login()
	if code, _ := call(known); code != http.StatusOK {
		t.Fatalf("call: %d", code)
	}
	unknown := login()
	creds.down = true
	if code, _ := call(unknown); code != http.StatusServiceUnavailable {
		t.Fatalf("unchecked session while credentials are down: %d", code)
	}
	if code, _ := call(known); code != http.StatusOK {
		t.Fatalf("known session while credentials are down: %d", code)
	}
}

// Switching project reissues a token, so it is checked like a request: an ended session cannot do it (ADR 0045),
// and the caller must have access to the target project (ADR 0039); with no access check wired no project is
// granted, and only clearing the project is allowed.
func TestSwitchProjectChecksSessionAndAccess(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	creds := &fakeCredentials{passwords: map[string]string{"alice": "correct horse"}}
	member := func(_ context.Context, p authz.Principal, project string) (bool, error) {
		return p.Subject == "alice" && project == "PROJ-A", nil
	}
	setup := func(access func(context.Context, authz.Principal, string) (bool, error)) *httptest.Server {
		e := echo.New()
		if err := Mount(e, Config{AuthMode: "local", JWTSecret: secret, Credentials: creds, ProjectAccess: access}); err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(e)
	}
	post := func(srv *httptest.Server, path, token, body string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var raw json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		return resp.StatusCode, string(raw)
	}
	login := func(srv *httptest.Server) string {
		code, body := post(srv, "/auth/login", "", `{"subject":"alice","password":"correct horse"}`)
		var out issued
		_ = json.Unmarshal([]byte(body), &out)
		if code != http.StatusOK || out.Token == "" {
			t.Fatalf("login: %d %s", code, body)
		}
		return out.Token
	}

	srv := setup(member)
	defer srv.Close()
	tok := login(srv)
	if code, body := post(srv, "/auth/dev-token/project", tok, `{"project":"PROJ-A"}`); code != http.StatusOK {
		t.Fatalf("a member's project: %d %s", code, body)
	}
	if code, _ := post(srv, "/auth/dev-token/project", tok, `{"project":"PROJ-B"}`); code != http.StatusForbidden {
		t.Fatalf("a project the caller has no access to: %d", code)
	}
	if code, _ := post(srv, "/auth/dev-token/project", tok, `{"project":""}`); code != http.StatusOK {
		t.Fatalf("clearing the project: %d", code)
	}
	// the session ends: the token no longer switches project
	if code, _ := post(srv, "/auth/logout", tok, ""); code != http.StatusNoContent {
		t.Fatalf("logout: %d", code)
	}
	if code, body := post(srv, "/auth/dev-token/project", tok, `{"project":"PROJ-A"}`); code != http.StatusUnauthorized || !strings.Contains(body, "session ended") {
		t.Fatalf("switch with an ended session: %d %s", code, body)
	}

	// nothing wired: no project is granted
	bare := setup(nil)
	defer bare.Close()
	if code, _ := post(bare, "/auth/dev-token/project", login(bare), `{"project":"PROJ-A"}`); code != http.StatusForbidden {
		t.Fatalf("switch with no access check: %d", code)
	}
}

// An unset auth mode is an error, not an open gateway: only an explicit "none" lets everybody in as the dev principal.
func TestEmptyAuthModeIsRefused(t *testing.T) {
	if _, err := Authenticator(Config{}); err == nil {
		t.Fatal("an empty auth mode must be refused")
	}
	if _, err := Authenticator(Config{AuthMode: "none"}); err != nil {
		t.Fatalf("explicit none: %v", err)
	}
}
