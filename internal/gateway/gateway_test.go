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

	"connectrpc.com/connect"
	"github.com/labstack/echo/v5"
)

// fakeCredentials is an in-memory Credentials for local auth tests (ADR 0040).
type fakeCredentials struct{ passwords map[string]string }

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
	if err := Mount(e, Config{AuthMode: "local", JWTSecret: secret, Credentials: creds, OnSignIn: onSignIn,
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
