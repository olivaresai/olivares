// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package debezium

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/sdk/model"
)

// Debezium change-event operation codes (the envelope's "op" field). They classify
// the row change without revealing it.
const (
	opCreate   = "c" // INSERT
	opUpdate   = "u" // UPDATE
	opDelete   = "d" // DELETE
	opRead     = "r" // snapshot read (initial load)
	opTruncate = "t" // TRUNCATE
	opMessage  = "m" // logical decoding message (no table)
)

// Resource/origin kinds this connector materializes. The resource kind is
// deliberately "cdc.table" — DISTINCT from "postgres.table"/"mysql.table" —
// so a CDC streaming edge never collides with a static-audit edge in the graph
// (the frontier; see doc.go).
const (
	kindCDCSource = "cdc.source"
	kindCDCTable  = "cdc.table"
)

// change is the minimal-data projection of a Debezium change event: the operation
// and the SOURCE coordinates (which connector/server/database/schema/table the
// change touched). The before/after row images are deliberately NOT parsed — they
// are the data, which a minimal-data observer never reads (docs/SECURITY-HARDENING.md).
type change struct {
	Op        string
	Connector string // "postgresql", "mysql", "sqlserver", "mongodb", ...
	Server    string // logical server name (source.name)
	DB        string
	Schema    string
	Table     string
	TSMs      int64
}

// envelope is the Debezium CDC envelope. Debezium may wrap the event under a
// "payload" member (schema+payload form, the default for Kafka Connect JSON) or emit
// the payload fields at the top level (when value.converter.schemas.enable=false).
// We accept BOTH by trying "payload" first and falling back to the top level. Only
// op and source are decoded; before/after are intentionally ignored (json leaves
// unlisted members unparsed — they are never materialized).
type envelope struct {
	Op     string `json:"op"`
	Source struct {
		Connector string `json:"connector"`
		Name      string `json:"name"`
		DB        string `json:"db"`
		Schema    string `json:"schema"`
		Table     string `json:"table"`
		// MySQL uses "table" + "db" (no schema); Postgres uses "schema" + "table" +
		// "db". MongoDB uses "collection". We surface whichever is present.
		Collection string `json:"collection"`
		TSMs       int64  `json:"ts_ms"`
	} `json:"source"`
	TSMs int64 `json:"ts_ms"`
}

// parseChange extracts the change from a Debezium record value. It returns ok=false
// for a tombstone (null value, the Kafka-compaction delete marker) or any value that
// is not a Debezium envelope, so the caller skips it cleanly. It never decodes the
// before/after row images.
func parseChange(value []byte) (change, bool) {
	if len(value) == 0 {
		return change{}, false // tombstone / empty
	}
	// schema+payload form: the envelope lives under "payload".
	var wrapper struct {
		Payload json.RawMessage `json:"payload"`
	}
	body := value
	if json.Unmarshal(value, &wrapper) == nil && len(wrapper.Payload) > 0 {
		body = wrapper.Payload
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return change{}, false
	}
	if env.Op == "" {
		return change{}, false // not a change event
	}
	table := env.Source.Table
	if table == "" {
		table = env.Source.Collection // MongoDB
	}
	ts := env.TSMs
	if ts == 0 {
		ts = env.Source.TSMs
	}
	return change{
		Op:        env.Op,
		Connector: env.Source.Connector,
		Server:    env.Source.Name,
		DB:        env.Source.DB,
		Schema:    env.Source.Schema,
		Table:     table,
		TSMs:      ts,
	}, true
}

// modeForOp maps a Debezium op to the R/RW classification. A snapshot read is a
// read; insert/update/delete/truncate are writes. An unknown op is unknown — never
// guessed (ARCHITECTURE.md).
func modeForOp(op string) model.AccessMode {
	switch op {
	case opRead:
		return model.ModeRead
	case opCreate, opUpdate, opDelete, opTruncate:
		return model.ModeWrite
	default:
		return model.ModeUnknown
	}
}

// tableRef builds the fully-qualified table reference (db.schema.table, omitting an
// empty segment) — the natural key a consumer materializes the CDC entity from.
func (c change) tableRef() string {
	parts := make([]string, 0, 3)
	for _, p := range []string{c.DB, c.Schema, c.Table} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return c.Table
	}
	return strings.Join(parts, ".")
}

// sourceRef labels the CDC pipeline origin (connector:logical-server) — the "who" of
// a change-data edge. Debezium does not carry the SQL principal in the default
// envelope, so the origin is the capture pipeline, attributed approximately.
func (c change) sourceRef() string {
	switch {
	case c.Connector != "" && c.Server != "":
		return c.Connector + ":" + c.Server
	case c.Server != "":
		return c.Server
	case c.Connector != "":
		return c.Connector
	default:
		return "debezium"
	}
}

// edges turns a change into minimal-data CDC edges: the capture source touched the
// table (source -> cdc.table, Mode from op), and the table belongs to its logical
// server (server -> cdc.table topology). It NEVER includes a column value. A
// message-type event (op "m", no table) yields no edge.
func (c change) edges(at time.Time) []model.EdgeObservation {
	if c.Table == "" || c.Op == opMessage {
		return nil
	}
	table := c.tableRef()
	return []model.EdgeObservation{
		brokerobs.Observation{
			OriginKind:   kindCDCSource,
			OriginRef:    c.sourceRef(),
			ResourceKind: kindCDCTable,
			ResourceRef:  table,
			Mode:         modeForOp(c.Op),
			// The change is attributed to the capture pipeline, not a verified SQL
			// principal, so the attribution is approximate.
			Confidence: model.ConfidenceApproximate,
			ToolRef:    c.Op,
			ObservedAt: at,
		}.Edge(brokerobs.SignalDebezium),
	}
}
