package platform

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

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
	srv, _ := s.httpServer()
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

// A shutdown ends the streams (the event stream, server-sent events) instead of waiting for them until its deadline:
// stopping the stack (Ctrl-C) takes a moment, not the 15 s of the deadline.
func TestShutdownEndsStreams(t *testing.T) {
	s := NewServer(slog.New(slog.DiscardHandler), "127.0.0.1:0")
	s.Echo.GET("/stream", func(c *echo.Context) error {
		w := c.Response()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "open\n")
		http.NewResponseController(w).Flush()
		<-c.Request().Context().Done()
		return nil
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, endStreams := s.httpServer()
	go func() { _ = srv.Serve(ln) }()
	url := "http://" + ln.Addr().String() + "/stream"
	for name, client := range map[string]*http.Client{"h2c": H2CClient(), "http1": {}} {
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		defer resp.Body.Close()
		if line, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil || line != "open\n" {
			t.Fatalf("%s: %q %v", name, line, err)
		}
	}
	began := time.Now()
	if err := shutdown(context.Background(), srv, endStreams); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if took := time.Since(began); took > streamGrace+3*time.Second {
		t.Fatalf("shutdown took %s", took)
	}
}
