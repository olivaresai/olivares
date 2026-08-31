// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package brokerobs

import "github.com/olivaresai/olivares/connectors/internal/redact"

// OpenTelemetry messaging semantic-convention instrumentation, GATED.
//
// This is an OPT-IN, DEFAULT-OFF attribute carrier, and it is deliberately NOT
// pinned to a semconv schema version. As of 2026 the OpenTelemetry messaging
// semantic conventions are in DEVELOPMENT, not Stable, by OpenTelemetry's own
// status; pinning an emitted schema-url to a moving target would
// claim a stability the upstream spec does not offer. So this package:
//
//   - exposes the messaging attribute NAMES as constants (the names are the stable
//     part; the schema-url/version is the unstable part we refuse to fabricate),
//   - builds an attribute set ONLY when instrumentation is explicitly enabled, and
//   - never carries a message key, body, headers or payload — minimal-data still
//     holds for telemetry (docs/SECURITY-HARDENING.md).
//
// There is NO OTLP emitter here. Emitting OTLP traces/logs is job; this
// is the seam a connector uses to describe a broker operation for a tracer that a
// future, gated, stable wiring may attach. When the gate is off, every builder
// returns nil and the connector does no instrumentation work at all.

// Messaging semantic-convention attribute keys (un-versioned by design; see the
// package note above). These mirror the current opentelemetry.io messaging semconv
// names. Notably ABSENT: messaging.kafka.message.key and any body/header attribute
// — those would carry message content, which a minimal-data observer never emits.
const (
	AttrSystem            = "messaging.system"
	AttrDestinationName   = "messaging.destination.name"
	AttrOperationType     = "messaging.operation.type"
	AttrOperationName     = "messaging.operation.name"
	AttrConsumerGroupName = "messaging.consumer.group.name"
	AttrBatchMessageCount = "messaging.batch.message_count"
)

// Operation types from the messaging semconv (the small, stable enum of what a
// span represents). We use receive/process for an observing consumer and send for
// an egress producer.
const (
	OpReceive = "receive"
	OpProcess = "process"
	OpSend    = "send"
)

// Instrumentation is the per-connector OTel messaging gate. The zero value is
// disabled, so a connector that never reads the gate emits no telemetry.
type Instrumentation struct {
	// Enabled turns the attribute builders on. It is sourced from an explicit
	// operator opt-in (otel_messaging=true); default off.
	Enabled bool
	// System is the messaging.system value for this connector (e.g. "kafka",
	// "rabbitmq", "nats", "mqtt", "aws_sqs", "gcp_pubsub"). Empty leaves the
	// attribute out.
	System string
}

// ConfigBool is the minimal config surface this gate needs, satisfied by sdk.Config
// without importing it here (keeping this file dependency-free beyond redact). A
// connector passes cfg.GetBool.
type ConfigBool interface {
	GetBool(key string, def bool) bool
}

// gateKey is the operator opt-in setting every messaging connector honors.
const gateKey = "otel_messaging"

// InstrumentationFromConfig reads the opt-in gate (default false) for the given
// messaging system. A connector calls it once in Open and keeps the result.
func InstrumentationFromConfig(cfg ConfigBool, system string) Instrumentation {
	return Instrumentation{Enabled: cfg.GetBool(gateKey, false), System: system}
}

// Attrs builds the messaging-semconv attribute set for one broker operation, or
// nil when instrumentation is disabled (the default). destination is scrubbed
// through redact.Clean — a destination name is not normally sensitive, but a
// telemetry attribute is held to the same minimal-data bar as an edge. group and
// batch are optional (empty/zero omits them). No message content is ever included.
func (i Instrumentation) Attrs(op, destination, group string, batch int) map[string]string {
	if !i.Enabled {
		return nil
	}
	m := map[string]string{}
	if i.System != "" {
		m[AttrSystem] = i.System
	}
	if op != "" {
		m[AttrOperationType] = op
	}
	if destination != "" {
		m[AttrDestinationName] = redact.Clean(destination)
	}
	if group != "" {
		m[AttrConsumerGroupName] = redact.Clean(group)
	}
	if batch > 0 {
		m[AttrBatchMessageCount] = itoa(batch)
	}
	return m
}

// itoa is a tiny non-allocating-path integer formatter so this file needs no
// strconv import churn; batch counts are small non-negative ints.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
