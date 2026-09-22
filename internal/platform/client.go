package platform

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/http2"
)

// H2CClient returns an HTTP client speaking HTTP/2 cleartext, for
// service-to-service Connect calls inside the cluster.
func H2CClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Minute,
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}
