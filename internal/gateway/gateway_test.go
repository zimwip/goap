package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestAuthAndRouting(t *testing.T) {
	var gotSubject, gotRoles string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSubject, gotRoles = r.Header.Get(HeaderSubject), r.Header.Get(HeaderRoles)
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
	resp, err := http.Post(srv.URL+"/auth/dev-token", "application/json", strings.NewReader(`{"subject":"alice","org":"acme","roles":["admin"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var tok struct{ Token string }
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if code := call(tok.Token); code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if gotSubject != "alice" || gotRoles != "admin" {
		t.Fatalf("identity not propagated: %q %q", gotSubject, gotRoles)
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
