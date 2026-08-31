// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package debezium

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/connectors/internal/kafkawire"
	"github.com/olivaresai/olivares/connectors/internal/tlsx"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.debezium"

const version = "0.1.0"

// config is the resolved Debezium connector configuration. Debezium publishes change
// events to Kafka topics (one per captured table), so the connector consumes those
// topics through the shared kafkawire client — the same wire that backs.
type config struct {
	brokers  []string
	topics   []string // the Debezium CDC topics to observe (one per table, or a list)
	group    string
	tls      tlsConfig
	saslMech string
	saslUser string
	saslPass string
	otel     bool
	timeout  time.Duration
}

type tlsConfig struct {
	enable     bool
	caFile     string
	certFile   string
	keyFile    string
	skipVerify bool
}

// Source observes a Debezium CDC stream and emits minimal-data change-data edges. It
// is a STREAMING source: Gather blocks consuming the CDC topics until ctx is
// canceled (the engine owns scheduling, S02 §5).
type Source struct {
	cfg config
	otl brokerobs.Instrumentation
	// newConsumer builds the wire client; defaults to franz-go via kafkawire and is
	// overridden in tests with a fake yielding canned Debezium records.
	newConsumer func(config) (kafkawire.Consumer, error)
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Debezium connector; configuration is supplied in Open.
func New() *Source { return &Source{newConsumer: defaultConsumerFactory} }

func defaultConsumerFactory(c config) (kafkawire.Consumer, error) {
	t, err := tlsx.Build(tlsx.Options{
		Enable: c.tls.enable, CAFile: c.tls.caFile, CertFile: c.tls.certFile,
		KeyFile: c.tls.keyFile, InsecureSkipVerify: c.tls.skipVerify,
	})
	if err != nil {
		return nil, err
	}
	return kafkawire.NewConsumer(kafkawire.Config{
		Brokers: c.brokers, ClusterRef: clusterRef(c), Group: c.group, Topics: c.topics,
		TLS: t, SASLMech: c.saslMech, SASLUser: c.saslUser, SASLPass: c.saslPass,
	})
}

func clusterRef(c config) string {
	if len(c.brokers) > 0 {
		return c.brokers[0]
	}
	return ""
}

// Descriptor returns the connector's self-description and declared config.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Debezium CDC",
		Description: "Streams Debezium change-data events (op/source) into minimal-data CDC edges; never row data.",
		ConfigFields: []sdk.ConfigField{
			{Key: "brokers", Type: sdk.FieldString, Required: true, Description: "Comma-separated Kafka bootstrap brokers carrying the Debezium CDC topics."},
			{Key: "topics", Type: sdk.FieldString, Required: true, Description: "Comma-separated Debezium CDC topics to observe (one per captured table)."},
			{Key: "group", Type: sdk.FieldString, Default: "olivares-debezium-observer", Description: "Consumer group id."},
			{Key: "sasl_mechanism", Type: sdk.FieldString, Description: "SASL mechanism: plain, scram-sha-256 or scram-sha-512."},
			{Key: "sasl_user", Type: sdk.FieldString, Description: "SASL username."},
			{Key: "sasl_password", Type: sdk.FieldString, Secret: true, Description: "SASL password (reference)."},
			{Key: "tls", Type: sdk.FieldBool, Default: "false", Description: "Enable TLS to the brokers."},
			{Key: "tls_ca_file", Type: sdk.FieldString, Description: "PEM CA bundle."},
			{Key: "tls_cert_file", Type: sdk.FieldString, Description: "Client certificate for mTLS."},
			{Key: "tls_key_file", Type: sdk.FieldString, Description: "Client private key for mTLS."},
			{Key: "tls_insecure_skip_verify", Type: sdk.FieldBool, Default: "false", Description: "Skip broker cert verification (NOT for production)."},
			{Key: "otel_messaging", Type: sdk.FieldBool, Default: "false", Description: "Enable gated OTel messaging instrumentation (default off)."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-operation timeout."},
		},
	}
}

// Open resolves and validates configuration. brokers and topics are required.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c := config{
		brokers:  splitCSV(cfg.Get("brokers")),
		topics:   splitCSV(cfg.Get("topics")),
		group:    cfg.Get("group"),
		saslMech: strings.ToLower(strings.TrimSpace(cfg.Get("sasl_mechanism"))),
		saslUser: cfg.Get("sasl_user"),
		saslPass: cfg.Get("sasl_password"),
		otel:     cfg.GetBool("otel_messaging", false),
		timeout:  cfg.GetDuration("timeout", 30*time.Second),
		tls: tlsConfig{
			enable:     cfg.GetBool("tls", false),
			caFile:     cfg.Get("tls_ca_file"),
			certFile:   cfg.Get("tls_cert_file"),
			keyFile:    cfg.Get("tls_key_file"),
			skipVerify: cfg.GetBool("tls_insecure_skip_verify", false),
		},
	}
	if len(c.brokers) == 0 {
		return fmt.Errorf("debezium: 'brokers' is required")
	}
	if len(c.topics) == 0 {
		return fmt.Errorf("debezium: 'topics' is required (the Debezium CDC topics to observe)")
	}
	if c.group == "" {
		c.group = "olivares-debezium-observer"
	}
	switch c.saslMech {
	case "", "plain", "scram-sha-256", "scram-sha-512":
	default:
		return fmt.Errorf("debezium: unsupported sasl_mechanism %q", c.saslMech)
	}
	s.cfg = c
	s.otl = brokerobs.InstrumentationFromConfig(cfg, "kafka")
	if s.newConsumer == nil {
		s.newConsumer = defaultConsumerFactory
	}
	return nil
}

// Gather consumes the Debezium CDC topics and emits a CDC edge for each change
// event, blocking until ctx is canceled. A consume error is returned so the engine
// can restart with backoff. Tombstones and non-envelope records are skipped.
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
			ch, ok := parseChange(rec.Value)
			if !ok {
				continue
			}
			for _, e := range ch.edges(now) {
				if emitErr := sink.Emit(ctx, e); emitErr != nil {
					return emitErr
				}
			}
		}
	}
}

// Close releases the connector's resources; the consumer is owned by Gather.
func (s *Source) Close(context.Context) error { return nil }

// splitCSV splits a comma list, trimming spaces and dropping empties.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
