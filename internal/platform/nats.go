package platform

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Events publishes JSON events on NATS JetStream.
type Events struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// ConnectEvents connects to NATS and ensures the GOAP stream (subjects goap.>).
func ConnectEvents(ctx context.Context, log *slog.Logger, url string) (*Events, error) {
	nc, err := nats.Connect(url, nats.Name("goap"), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { log.Warn("nats disconnected", "err", err) }))
	if err != nil {
		return nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "GOAP", Subjects: []string{"goap.>"}, MaxAge: 7 * 24 * time.Hour, Storage: jetstream.FileStorage,
	})
	if err != nil {
		nc.Close()
		return nil, err
	}
	return &Events{nc: nc, js: js}, nil
}

// Publish implements engine.Publisher.
func (e *Events) Publish(ctx context.Context, subject string, v any) error {
	if e == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = e.js.Publish(ctx, subject, data)
	return err
}

// Ready checks the connection.
func (e *Events) Ready(context.Context) error {
	if e == nil {
		return nil
	}
	if !e.nc.IsConnected() {
		return errors.New("nats not connected")
	}
	return nil
}

// Close drains the connection.
func (e *Events) Close() {
	if e != nil {
		_ = e.nc.Drain()
	}
}

// Subscribe calls fn for every message on subject (core NATS, at most once).
func (e *Events) Subscribe(subject string, fn func(data []byte)) error {
	if e == nil {
		return nil
	}
	_, err := e.nc.Subscribe(subject, func(m *nats.Msg) { fn(m.Data) })
	return err
}
