package platform

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
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
	msg := nats.NewMsg(subject)
	msg.Data = data
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(msg.Header))
	if st := StampOf(ctx); st.Command != "" {
		msg.Header.Set(HeaderActor, st.Actor)
		msg.Header.Set(HeaderCommand, st.Command)
	}
	_, err = e.js.PublishMsg(ctx, msg)
	return err
}

// PublishCore publishes without persistence (core NATS, at most once): for ephemeral facts such as presence,
// on subjects outside the GOAP stream (goap.>).
func (e *Events) PublishCore(subject string, v any) error {
	if e == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return e.nc.Publish(subject, data)
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

// SubscribeSubject is Subscribe for a wildcard subject: fn also gets the subject of each message and the stamp
// (who, which command) its publisher put on it.
func (e *Events) SubscribeSubject(subject string, fn func(subject string, data []byte, stamp Stamp)) error {
	if e == nil {
		return nil
	}
	_, err := e.nc.Subscribe(subject, func(m *nats.Msg) {
		fn(m.Subject, m.Data, Stamp{Actor: m.Header.Get(HeaderActor), Command: m.Header.Get(HeaderCommand)})
	})
	return err
}

// ConsumeDurable delivers, at least once and in order, every message of the GOAP stream matching the
// subjects to fn through a durable consumer named name. A message is acknowledged when fn returns nil and
// redelivered after a delay otherwise. It returns when ctx is done (or the consumer cannot be created).
func (e *Events) ConsumeDurable(ctx context.Context, log *slog.Logger, name string, subjects []string, fn func(ctx context.Context, subject string, data []byte) error) error {
	if e == nil {
		return nil
	}
	cons, err := e.js.CreateOrUpdateConsumer(ctx, "GOAP", jetstream.ConsumerConfig{
		Durable: name, FilterSubjects: subjects, AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute,
		DeliverPolicy: jetstream.DeliverAllPolicy, MaxAckPending: 1,
	})
	if err != nil {
		return err
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		mctx := otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(m.Headers()))
		if err := fn(mctx, m.Subject(), m.Data()); err != nil {
			log.Warn("event not processed, redelivered", "consumer", name, "subject", m.Subject(), "err", err)
			_ = m.NakWithDelay(5 * time.Second)
			return
		}
		_ = m.Ack()
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	cc.Stop()
	return nil
}
