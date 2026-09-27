package platform

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
)

// The server answers HTTP/2 cleartext (the Connect calls between services) and HTTP/1.1 (browsers).
func TestServerSpeaksH2CAndHTTP1(t *testing.T) {
	s := NewServer(slog.New(slog.DiscardHandler), "127.0.0.1:0")
	s.Echo.GET("/proto", func(c *echo.Context) error { return c.String(http.StatusOK, c.Request().Proto) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := s.httpServer()
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	url := "http://" + ln.Addr().String()
	for name, tc := range map[string]struct {
		client *http.Client
		want   string
	}{
		"h2c":   {H2CClient(), "HTTP/2.0"},
		"http1": {&http.Client{}, "HTTP/1.1"},
	} {
		resp, err := tc.client.Get(url + "/proto")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != tc.want {
			t.Fatalf("%s: %d %q", name, resp.StatusCode, body)
		}
	}
	if resp, err := H2CClient().Get(url + "/healthz"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %v %v", resp, err)
	}
}
