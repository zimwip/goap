package platform

import (
	"net/http"
	"time"
)

// H2CClient returns an HTTP client speaking HTTP/2 cleartext (prior knowledge), for
// service-to-service Connect calls inside the cluster.
func H2CClient() *http.Client {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{
		Timeout:   5 * time.Minute,
		Transport: &http.Transport{Protocols: &p},
	}
}
