package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// startNATS starts the NATS server of the stack in this process, JetStream on (the durable consumers of the engine),
// its streams kept in <dir>/nats: the bus the compose stack runs as a container.
func startNATS(cfg config) (*server.Server, error) {
	opts := &server.Options{
		ServerName: "goap-local",
		Host:       "127.0.0.1",
		Port:       cfg.natsPort,
		JetStream:  true,
		StoreDir:   filepath.Join(cfg.dir, "nats"),
		NoSigs:     true,
	}
	if cfg.natsMonitor > 0 {
		opts.HTTPHost, opts.HTTPPort = "127.0.0.1", cfg.natsMonitor
	}
	s, err := server.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("nats: %w", err)
	}
	go s.Start()
	if !s.ReadyForConnections(15 * time.Second) {
		s.Shutdown()
		return nil, fmt.Errorf("nats: not ready on port %d", cfg.natsPort)
	}
	return s, nil
}

// natsURL is the client URL of the server of the stack.
func (c config) natsURL() string { return fmt.Sprintf("nats://127.0.0.1:%d", c.natsPort) }
