// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kafka

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/connectors/internal/kafkawire"
	"github.com/olivaresai/olivares/connectors/internal/schemaregistry"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// defaultConsumerFactory / defaultProducerFactory dial the real franz-go wire
// client through the shared kafkawire seam (overridden by fakes in tests).
func defaultConsumerFactory(c config) (kafkawire.Consumer, error) {
	return kafkawire.NewConsumer(c.wireConfig())
}

func defaultProducerFactory(c config) (kafkawire.Producer, error) {
	return kafkawire.NewProducer(c.wireConfig())
}

// SourceName / OutputName are the two globally unique connector identifiers. One
// connector kind ("kafka") observes the event surface (SourceName) and egresses
// evidence/findings as CloudEvents to a topic (OutputName). The wire is identical
// across Apache Kafka 4.0 (KRaft), Confluent, Redpanda, MSK and Azure Event Hubs'
// Kafka endpoint — one connector reaches all five (doc.go).
const (
	SourceName = "olivares.kafka"
	OutputName = "olivares.kafka-egress"
)

const sourceVersion = "0.1.0"

// Source is the Kafka consumer-group observer. It is a STREAMING source: Gather
// blocks consuming until ctx is canceled, emitting minimal-data edges for the event
// traffic it sees (the engine owns scheduling — the connector holds no ticker, S02
// §5). It never persists or emits a record's key or value content (docs/SECURITY-HARDENING.md).
type Source struct {
	cfg config
	obs *observer
	otl brokerobs.Instrumentation
	// newConsumer builds the wire client; defaults to the franz-go client and is
	// overridden in tests with a fake that yields canned records (offline).
	newConsumer func(config) (kafkawire.Consumer, error)
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Kafka source connector; configuration is supplied in Open.
func New() *Source { return &Source{newConsumer: defaultConsumerFactory} }

// Descriptor returns the source's stable self-description and declared config.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:         SourceName,
		Version:      sourceVersion,
		APIVersion:   sdk.APIVersion,
		Type:         sdk.TypeSource,
		Title:        "Apache Kafka (wire-protocol)",
		Description:  "Observes Kafka/Confluent/Redpanda/MSK/Event Hubs event flows; minimal-data edges, never message bodies.",
		ConfigFields: descriptorFields(),
	}
}

// Open resolves configuration and builds the observer (including a read-only Schema
// Registry client when one is configured). A configuration error surfaces here, not
// in Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	s.obs = &observer{
		clusterRef:     c.clusterRef,
		sr:             buildRegistryClient(c),
		srGUIDHeader:   c.registryGUIDHeader,
		resolveTimeout: c.timeout,
	}
	s.otl = brokerobs.InstrumentationFromConfig(cfg, "kafka")
	if s.newConsumer == nil {
		s.newConsumer = defaultConsumerFactory
	}
	return nil
}

// Gather dials the cluster, optionally emits a one-shot topology snapshot, then
// blocks consuming records and emitting edges until ctx is canceled. A topology
// scan that fails yields one health finding (a gap is a signal) and the
// consume loop continues. A Poll error is returned so the engine can restart the
// source with backoff.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	nc := s.newConsumer
	if nc == nil {
		nc = defaultConsumerFactory
	}
	cons, err := nc(s.cfg)
	if err != nil {
		return err
	}
	defer cons.Close()

	if s.cfg.topologyScan {
		topo, terr := cons.Topology(ctx)
		if terr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if emitErr := sink.Emit(ctx, healthFinding(s.cfg.clusterRef, "Kafka topology scan failed", terr)); emitErr != nil {
				return emitErr
			}
		} else {
			for _, e := range s.obs.observeTopology(topo, time.Now().UTC()) {
				if emitErr := sink.Emit(ctx, e); emitErr != nil {
					return emitErr
				}
			}
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		recs, err := cons.Poll(ctx)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, rec := range recs {
			for _, e := range s.obs.observeRecord(ctx, rec, now) {
				if emitErr := sink.Emit(ctx, e); emitErr != nil {
					return emitErr
				}
			}
		}
	}
}

// Close releases the connector's resources; the consumer is owned by Gather and
// closed there, so this is a safe no-op even if Open failed.
func (s *Source) Close(context.Context) error { return nil }

// Output is the Kafka egress connector: it wraps an evidence/finding Notification in
// a CloudEvents 1.0.2 envelope and produces it to a topic, so a downstream consumer
// can route/filter/replay it without parsing an Olivares-proprietary shape. Minimal
// data: a Notification already carries only non-sensitive, displayable fields.
type Output struct {
	cfg  config
	prod kafkawire.Producer
	// newProducer builds the wire client; defaults to franz-go, overridden in tests.
	newProducer func(config) (kafkawire.Producer, error)
}

var _ sdk.OutputConnector = (*Output)(nil)

// NewOutput returns a Kafka egress connector; configuration is supplied in Open.
func NewOutput() *Output { return &Output{newProducer: defaultProducerFactory} }

// Descriptor returns the output's stable self-description and declared config.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:         OutputName,
		Version:      sourceVersion,
		APIVersion:   sdk.APIVersion,
		Type:         sdk.TypeOutput,
		Title:        "Apache Kafka egress (CloudEvents)",
		Description:  "Produces evidence/findings as CloudEvents 1.0.2 to a Kafka topic.",
		ConfigFields: descriptorFields(),
	}
}

// Open resolves configuration (brokers + egress_topic required) and dials the
// producer once.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	if c.egressTopic == "" {
		return fmt.Errorf("kafka egress: 'egress_topic' is required")
	}
	o.cfg = c
	if o.newProducer == nil {
		o.newProducer = defaultProducerFactory
	}
	p, err := o.newProducer(c)
	if err != nil {
		return err
	}
	o.prod = p
	return nil
}

// Notify encodes n as a CloudEvent and produces it to the egress topic in the
// configured content mode (structured by default; binary when binary_egress is set).
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.prod == nil {
		return fmt.Errorf("kafka egress: not open")
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
		headers, key, body, err := ev.KafkaBinary()
		if err != nil {
			return err
		}
		return o.prod.Produce(ctx, o.cfg.egressTopic, key, body, headers)
	}
	body, err := ev.StructuredBytes()
	if err != nil {
		return err
	}
	headers := map[string][]byte{"content-type": []byte(cloudevents.ContentTypeStructured)}
	return o.prod.Produce(ctx, o.cfg.egressTopic, nil, body, headers)
}

// Close releases the producer; safe to call even if Open failed.
func (o *Output) Close(context.Context) error {
	if o.prod != nil {
		o.prod.Close()
		o.prod = nil
	}
	return nil
}

// buildRegistryClient builds a read-only Schema Registry client from config, or nil
// when no registry URL is set (the source then emits topology without contract refs).
func buildRegistryClient(c config) *schemaregistry.Client {
	if c.registryURL == "" {
		return nil
	}
	return schemaregistry.NewClient(schemaregistry.Options{
		BaseURL: c.registryURL,
		Auth:    basicAuth(c.registryUser, c.registryPass),
	})
}

// healthFinding reports an enabled target that could not be reached (a gap is a
// signal). The error text is hashed, never emitted in the clear (docs/SECURITY-HARDENING.md).
func healthFinding(clusterRef, title string, cause error) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityMedium,
		SubjectKind: kindCluster,
		SubjectRef:  clusterRef,
		Title:       title,
		DetailHash:  hashErr(cause),
		OccurredAt:  time.Now().UTC(),
	}
}

// newEventID returns a unique CloudEvents id (16 random bytes, hex). A Notification
// has no id of its own and the producer owns uniqueness (cloudevents.FromNotification).
func newEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("kafka egress: event id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
