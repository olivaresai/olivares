// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package kafkawire is the shared Kafka wire-protocol client behind a narrow seam.
// Because Apache Kafka 4.0 (KRaft), Confluent, Redpanda, MSK and Azure Event Hubs'
// Kafka endpoint all speak the same protocol, ONE client reaches them all — and two
// Olivares connectors ride it: `kafka` (generic event observation) and
// `debezium` (CDC streams that Debezium publishes to Kafka topics). The
// production client is the pure-Go franz-go library (BSD-3), confined to franz.go;
// the Consumer/Producer interfaces here let every connector's observation logic run
// offline in CI with a fake that yields canned records — no franz-go network path
// and no real Kafka in CI (the wire path is integration-tested).
//
// It is minimal-data by construction: the Record it exposes carries framing and
// metadata, and although the Key/Value bytes are present so a connector can inspect
// their FRAMING (a CloudEvents header, a Schema-Registry prefix, a Debezium
// envelope), a connector emits only the identifiers it derives — never the content.
// It imports only the SDK-adjacent helpers and franz-go, never the engine.
package kafkawire

import (
	"context"
	"crypto/tls"
	"time"
)

// Record is the minimal-data view of a consumed Kafka record.
type Record struct {
	Topic     string
	Partition int32
	Offset    int64
	Timestamp time.Time
	Key       []byte
	Value     []byte
	Headers   map[string][]byte
}

// GroupInfo is a consumer group's observed topology.
type GroupInfo struct {
	Group   string
	Topics  []string
	Members int
}

// Topology is a one-shot, read-only snapshot of the cluster's event surface.
type Topology struct {
	ClusterRef string
	Topics     []string
	Groups     []GroupInfo
}

// Consumer is the consume seam: a streaming poll plus an optional topology snapshot.
type Consumer interface {
	Topology(ctx context.Context) (Topology, error)
	Poll(ctx context.Context) ([]Record, error)
	Close()
}

// Producer is the egress seam: produce one already-encoded message to a topic.
type Producer interface {
	Produce(ctx context.Context, topic string, key, value []byte, headers map[string][]byte) error
	Close()
}

// Config is the connection configuration the wire client needs, resolved by the
// owning connector from its SDK config. Secrets (SASL password) live here in memory
// only and are never logged or emitted (docs/SECURITY-HARDENING.md).
type Config struct {
	Brokers    []string
	ClusterRef string
	Group      string
	Topics     []string
	TLS        *tls.Config
	SASLMech   string // "", "plain", "scram-sha-256", "scram-sha-512"
	SASLUser   string
	SASLPass   string
}
