// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package amqp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/sdk"
)

// SourceName / OutputName are the two globally unique connector identifiers. One
// connector kind ("amqp") observes the event surface (SourceName) and egresses
// evidence/findings as CloudEvents (OutputName). The AMQP 1.0 wire is identical
// across RabbitMQ 4.0 (native AMQP 1.0) and Azure Service Bus — one connector reaches
// both (doc.go).
const (
	SourceName = "olivares.amqp"
	OutputName = "olivares.amqp-egress"
)

const sourceVersion = "0.1.0"

// Source is the AMQP 1.0 observation receiver. It is a STREAMING source: Gather
// blocks receiving from the dedicated observation address until ctx is canceled,
// emitting minimal-data edges for the traffic it sees (the engine owns scheduling —
// the connector holds no ticker, S02 §5). It never persists or emits a message's body
// (docs/SECURITY-HARDENING.md).
type Source struct {
	cfg config
	obs *observer
	otl brokerobs.Instrumentation
	// newReceiver builds the wire client; defaults to the go-amqp receiver and is
	// overridden in tests with a fake that yields canned messages (offline).
	newReceiver func(config) (receiver, error)
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns an AMQP source connector; configuration is supplied in Open.
func New() *Source { return &Source{newReceiver: defaultReceiverFactory} }

// Descriptor returns the source's stable self-description and declared config.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:         SourceName,
		Version:      sourceVersion,
		APIVersion:   sdk.APIVersion,
		Type:         sdk.TypeSource,
		Title:        "AMQP 1.0 (RabbitMQ / Azure Service Bus)",
		Description:  "Observes AMQP 1.0 event flows from a dedicated observation queue; minimal-data edges, never message bodies.",
		ConfigFields: descriptorFields(),
	}
}

// Open resolves configuration and builds the observer. A configuration error surfaces
// here, not in Gather. The Source additionally requires observation_address.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	if c.observationAddress == "" {
		return fmt.Errorf("amqp: 'observation_address' is required for the source")
	}
	s.cfg = c
	s.obs = &observer{namespaceRef: c.namespaceRef}
	s.otl = brokerobs.InstrumentationFromConfig(cfg, "rabbitmq")
	if s.newReceiver == nil {
		s.newReceiver = defaultReceiverFactory
	}
	return nil
}

// Gather attaches to the observation address, then blocks receiving messages and
// emitting edges until ctx is canceled. A dial/attach error is returned so the engine
// can restart the source with backoff. Each received message is settled on the
// OBSERVATION queue (a dedicated tee — settling here never drains the app's queue;
// see doc.go) after its edges are emitted.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	nr := s.newReceiver
	if nr == nil {
		nr = defaultReceiverFactory
	}
	rcv, err := nr(s.cfg)
	if err != nil {
		return err
	}
	defer rcv.Close()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		msg, err := rcv.Receive(ctx)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, e := range s.obs.observeMessage(msg, s.cfg.observationAddress, now) {
			if emitErr := sink.Emit(ctx, e); emitErr != nil {
				return emitErr
			}
		}
		// Settle the message on the observation queue. A failure to settle is not
		// fatal to observation (the next Receive may still succeed), and an accept
		// error after ctx is canceled is just the shutdown path.
		if err := rcv.Accept(ctx, msg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
}

// Close releases the connector's resources; the receiver is owned by Gather and
// closed there, so this is a safe no-op even if Open failed.
func (s *Source) Close(context.Context) error { return nil }

// Output is the AMQP 1.0 egress connector: it wraps an evidence/finding Notification
// in a CloudEvents 1.0.2 envelope and sends it to a configured address, so a
// downstream consumer can route/filter/replay it without parsing an
// Olivares-proprietary shape. Minimal data: a Notification already carries only
// non-sensitive, displayable fields.
type Output struct {
	cfg config
	snd sender
	// newSender builds the wire client; defaults to go-amqp, overridden in tests.
	newSender func(config) (sender, error)
}

var _ sdk.OutputConnector = (*Output)(nil)

// NewOutput returns an AMQP egress connector; configuration is supplied in Open.
func NewOutput() *Output { return &Output{newSender: defaultSenderFactory} }

// Descriptor returns the output's stable self-description and declared config.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:         OutputName,
		Version:      sourceVersion,
		APIVersion:   sdk.APIVersion,
		Type:         sdk.TypeOutput,
		Title:        "AMQP 1.0 egress (CloudEvents)",
		Description:  "Sends evidence/findings as CloudEvents 1.0.2 to an AMQP 1.0 address (RabbitMQ / Azure Service Bus).",
		ConfigFields: descriptorFields(),
	}
}

// Open resolves configuration (addr + egress_address required) and attaches the
// sender once.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	if c.egressAddress == "" {
		return fmt.Errorf("amqp egress: 'egress_address' is required")
	}
	o.cfg = c
	if o.newSender == nil {
		o.newSender = defaultSenderFactory
	}
	snd, err := o.newSender(c)
	if err != nil {
		return err
	}
	o.snd = snd
	return nil
}

// Notify encodes n as a CloudEvent and sends it to the egress address in the
// configured content mode (structured by default; binary when binary_egress is set).
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.snd == nil {
		return fmt.Errorf("amqp egress: not open")
	}
	id, err := newEventID()
	if err != nil {
		return err
	}
	ev, err := cloudevents.FromNotification(id, o.cfg.egressSource, n)
	if err != nil {
		return err
	}
	if o.cfg.binaryEgress {
		appProps, contentType, body, err := ev.AMQPBinary()
		if err != nil {
			return err
		}
		return o.snd.Send(ctx, OutMessage{Body: body, ContentType: contentType, AppProps: appProps})
	}
	body, err := ev.StructuredBytes()
	if err != nil {
		return err
	}
	return o.snd.Send(ctx, OutMessage{Body: body, ContentType: cloudevents.ContentTypeStructured})
}

// Close releases the sender; safe to call even if Open failed.
func (o *Output) Close(context.Context) error {
	if o.snd != nil {
		o.snd.Close()
		o.snd = nil
	}
	return nil
}

// newEventID returns a unique CloudEvents id (16 random bytes, hex). A Notification
// has no id of its own and the producer owns uniqueness (cloudevents.FromNotification).
func newEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("amqp egress: event id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
