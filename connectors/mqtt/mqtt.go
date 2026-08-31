// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/sdk"
)

// SourceName is the globally unique connector identifier. This connector kind
// ("mqtt") observes the OT/IoT event surface — MQTT 5.0 traffic and Sparkplug B
// topology — over a single hand-rolled, stdlib-only wire client (doc.go).
const SourceName = "olivares.mqtt"

const sourceVersion = "0.1.0"

// Publish is the minimal-data view of one observed MQTT PUBLISH the transport seam
// hands to the observer. Topic and the MQTT 5 User Properties carry the topology and
// CloudEvents context; ContentType is the PUBLISH Content Type property. Payload is
// the raw application body — it is carried only so a real client need not special-
// case it, and it is NEVER read for content or emitted (docs/SECURITY-HARDENING.md).
type Publish struct {
	Topic       string
	UserProps   map[string]string
	ContentType string
	Payload     []byte
}

// mqttClient is the narrow TRANSPORT SEAM for the wire client. The real
// implementation (wire.go) dials net.Conn/tls.Conn, performs the MQTT 5 CONNECT/
// SUBSCRIBE handshake, and reads PUBLISH packets; a fake injected in tests yields
// canned Publishes, so the OBSERVATION path runs offline in CI with no broker and
// no network. Read blocks until the next PUBLISH arrives or ctx is canceled.
type mqttClient interface {
	// Read returns the next observed PUBLISH, blocking until one arrives or ctx is
	// done. It returns ctx.Err() (or a transport error) when no more will come.
	Read(ctx context.Context) (Publish, error)
	// Close releases the connection. Safe to call once after Read returns.
	Close() error
}

// Source is the MQTT 5.0 / Sparkplug B observer. It is a STREAMING source: Gather
// dials the broker, SUBSCRIBEs to the configured observation topic filters, then
// blocks reading PUBLISH packets and emitting minimal-data edges until ctx is
// canceled (the engine owns scheduling — the connector holds no ticker, S02 §5). It
// never emits a message payload (docs/SECURITY-HARDENING.md).
type Source struct {
	cfg config
	obs *observer
	otl brokerobs.Instrumentation
	// newClient builds the wire client; defaults to the real hand-rolled MQTT 5
	// client and is overridden in tests with a fake that yields canned Publishes.
	newClient func(config) (mqttClient, error)
}

var _ sdk.SourceConnector = (*Source)(nil)

// defaultClientFactory dials the real hand-rolled MQTT 5 client (wire.go).
func defaultClientFactory(c config) (mqttClient, error) { return dialClient(c) }

// New returns an MQTT source connector; configuration is supplied in Open.
func New() *Source { return &Source{newClient: defaultClientFactory} }

// Descriptor returns the source's stable self-description and declared config.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:         SourceName,
		Version:      sourceVersion,
		APIVersion:   sdk.APIVersion,
		Type:         sdk.TypeSource,
		Title:        "MQTT 5.0 / Sparkplug B (OT/IoT)",
		Description:  "Observes MQTT 5.0 and Sparkplug B event flows; minimal-data topology edges from the topic namespace, never message payloads.",
		ConfigFields: descriptorFields(),
	}
}

// Open resolves configuration and builds the observer. A configuration error
// (missing broker, bad URL, bad TLS material) surfaces here, not in Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	s.obs = &observer{brokerRef: c.brokerRef}
	s.otl = brokerobs.InstrumentationFromConfig(cfg, "mqtt")
	if s.newClient == nil {
		s.newClient = defaultClientFactory
	}
	return nil
}

// Gather dials the broker (CONNECT + SUBSCRIBE happen inside the client), then
// blocks reading PUBLISH packets and emitting edges until ctx is canceled. A read
// error is returned so the engine can restart the source with backoff; a
// clean ctx cancellation returns ctx.Err().
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	nc := s.newClient
	if nc == nil {
		nc = defaultClientFactory
	}
	cl, err := nc(s.cfg)
	if err != nil {
		return err
	}
	defer cl.Close()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pub, err := cl.Read(ctx)
		if err != nil {
			return err
		}
		for _, e := range s.obs.observePublish(pub, time.Now().UTC()) {
			if emitErr := sink.Emit(ctx, e); emitErr != nil {
				return emitErr
			}
		}
	}
}

// Close releases the connector's resources; the client is owned by Gather and
// closed there, so this is a safe no-op even if Open failed.
func (s *Source) Close(context.Context) error { return nil }
