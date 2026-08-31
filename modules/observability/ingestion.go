// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// Observation kind labels for the per-source byKind breakdown (the bus event
// type, projected onto the sealed observation vocabulary, sdk/model/observation.go:17-23).
const (
	kindEdge    = "edge"
	kindCost    = "cost"
	kindFinding = "finding"
	kindMetric  = "metric"
)

// gen_ai bus-evidence subject kinds. The two prove DIFFERENT facts and must not
// be conflated:
//   - the posture finding (SubjectKind "genai.semconv",
//     connectors/claude/genai_mcp.go:259) is dispatched at Gather START, gated
//     only on the profile being ON (connectors/claude/claude.go:166-171) — it
//     proves the GATE is active, not that any record flowed;
//   - a dialect-drift finding (SubjectKind "genai.dialect", genai_mcp.go:242)
//     is dropped per ingested run — it proves gen_ai RECORDS actually flowed
//     at that time, so only it may feed last_seen ("most recent record").
const (
	genaiSubjectSemconv = "genai.semconv"
	genaiSubjectDialect = "genai.dialect"
)

// sourceStats is one bus source's live counters.
type sourceStats struct {
	total     int64
	firstSeen time.Time
	lastSeen  time.Time
	byKind    map[string]int64
	bySignal  map[string]int64 // edges only: EdgeObservation.Source
}

// ingestStats is the in-memory, process-global ingestion read-model, fed by
// onEvent. Mutex-guarded: the bus delivers on its own goroutines while the
// handler snapshots on request goroutines.
type ingestStats struct {
	since   time.Time
	sources map[string]*sourceStats
	// genaiGateSeen counts gate evidence (semconv posture OR dialect findings);
	// genaiRecordLast is the latest RECORD evidence (dialect findings only) —
	// see the subject-kind constants for why the two are tracked apart.
	genaiGateSeen   int64
	genaiRecordLast time.Time
}

// onEvent counts one delivered bus event into the per-source stats. It counts
// EVERY first-party event regardless of tenant — the read-model is
// process-global by design (engine_scope=true) — and records the gen_ai
// evidence that flips the otel_genai standard's status. It never blocks and
// never errors (a counter cannot fail).
func (m *Module) onEvent(_ context.Context, e event.Event) error {
	kind := ""
	switch e.Type {
	case event.TypeEdgeObserved:
		kind = kindEdge
	case event.TypeCostSampled:
		kind = kindCost
	case event.TypeFindingReported:
		kind = kindFinding
	case event.TypeMetricSampled:
		kind = kindMetric
	default:
		return nil // not a first-party observation event; nothing to attribute
	}
	// Event.Time is the connector's fact time; fall back to the module clock
	// only when a producer left it zero (never fabricate a zero last_seen).
	at := e.Time
	if at.IsZero() {
		at = m.now()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stats.sources[e.Source]
	if s == nil {
		s = &sourceStats{byKind: make(map[string]int64)}
		m.stats.sources[e.Source] = s
	}
	s.total++
	s.byKind[kind]++
	if s.firstSeen.IsZero() || at.Before(s.firstSeen) {
		s.firstSeen = at
	}
	if at.After(s.lastSeen) {
		s.lastSeen = at
	}
	if edge, ok := event.EdgeOf(e); ok && edge.Source != "" {
		if s.bySignal == nil {
			s.bySignal = make(map[string]int64)
		}
		s.bySignal[string(edge.Source)]++
	}
	if f, ok := event.FindingOf(e); ok {
		switch f.SubjectKind {
		case genaiSubjectSemconv:
			m.stats.genaiGateSeen++
		case genaiSubjectDialect:
			m.stats.genaiGateSeen++
			if at.After(m.stats.genaiRecordLast) {
				m.stats.genaiRecordLast = at
			}
		}
	}
	return nil
}

// snapshotIngestion copies the live stats under the lock so the handler builds
// the response race-free.
func (m *Module) snapshotIngestion() (sources []ingestionSourceDTO, since time.Time, genaiGateSeen int64, genaiRecordLast time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sources = make([]ingestionSourceDTO, 0, len(m.stats.sources))
	for name, s := range m.stats.sources {
		row := ingestionSourceDTO{
			Name:         name,
			RecordsTotal: s.total,
			FirstSeen:    rfc3339(s.firstSeen),
			LastSeen:     rfc3339(s.lastSeen),
			Kinds:        make(map[string]int64, len(s.byKind)),
		}
		for k, v := range s.byKind {
			row.Kinds[k] = v
		}
		if len(s.bySignal) > 0 {
			row.Signals = make(map[string]int64, len(s.bySignal))
			for k, v := range s.bySignal {
				row.Signals[k] = v
			}
		}
		sources = append(sources, row)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	return sources, m.stats.since, m.stats.genaiGateSeen, m.stats.genaiRecordLast
}

// handleIngestionHealth returns the per-standard table + the live per-source
// counters. Pure in-memory read: it never touches the store (the counters are
// process-global, not tenant rows).
func (m *Module) handleIngestionHealth(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	sources, since, genaiGateSeen, genaiRecordLast := m.snapshotIngestion()
	out := ingestionHealthDTO{
		Standards:   m.standardsTable(genaiGateSeen, genaiRecordLast),
		EngineScope: true, // process-global by construction (OBS-06), see dto.go
		Sources:     sources,
		Since:       rfc3339(since),
	}
	writeJSON(w, http.StatusOK, out)
}

// Version pins the standards table carries. Each is a mirror of (or a live
// import from) the authoritative constant, with the source cited so a drift is
// a one-grep fix:
//   - otelGenAIVersion mirrors genAISemconvVersion (connectors/claude/genai.go;
//     also mirrored by modules/recording semconvVersion): v1.41.1 is the last
//     VERSIONED GenAI vocabulary label. otelGenAIUpstreamRepo/Ref mirror the
//     unversioned semconv-genai authority re-verified 2026-07-05 at main@c321d7e
//     (0 releases; public README Schema URL TODO). otelGenAIGate mirrors
//     cfgSemconvOptIn "=" genAIOptInToken (connectors/claude/config.go +
//     genai.go) — all unexported there, by design.
//   - siemwire.OCSFVersion is imported live (sdk/siemwire/ocsf.go:60).
//   - asimVersion mirrors ASIMSchemaVersion (connectors/internal/siemfmt/
//     aiformats.go:226 — an internal package this module cannot import).
//   - promTextVersion mirrors the exposition version in core/metrics/metrics.go:46.
const (
	otelGenAIVersion      = "1.41.1"
	otelGenAIUpstreamRepo = "open-telemetry/semantic-conventions-genai"
	otelGenAIUpstreamRef  = "main@c321d7e, verified 2026-07-05"
	otelGenAIGate         = "semconv_opt_in=gen_ai_latest_experimental"
	asimVersion           = "0.1.0"
	promTextVersion       = "0.0.4"
	noVersion             = "—" // formats/transports with no single upstream version pin
)

// standardsTable builds the seven standards rows: static verified pins plus
// the dynamic gen_ai bus evidence. Statuses are the TRUE operational states
// (verified):
//   - otel_genai flips opt_in_off→active only on bus GATE evidence (semconv
//     posture or dialect findings — see the subject-kind constants above): the
//     gate itself is per-source connector config the engine cannot read, so
//     without evidence opt_in_active stays nil (unknowable), never
//     false-claimed. last_seen is fed by RECORD evidence only (dialect
//     findings): the posture finding fires at Gather start before any record,
//     and last_seen's contract is "most recent record on this standard".
//   - ocsf is "available", NOT "active": OCSF is emitted only when an operator
//     provisions a notify destination with format=ocsf, or pulls the on-demand
//     audit export — nothing emits by default (cmd/olivares/
//     notifydispatch.go:134-151, core/api/handlers_audit.go:137-142).
//   - ledger_push is "blocked": the Forwarder seam is declared with ZERO call
//     sites (core/audit/forward.go:27-41); the live forwarder is .
//   - prometheus_text and w3c_trace_context are "active" unconditionally: the
//     /metrics endpoint is always served (core/api/server.go:232) and the
//     ingress trace extractor always runs, stamping trace ids into the ledger
//     (core/observability/trace/middleware.go:31-48,
//     core/internal/store/sqlstore/audit.go:56-63).
func (m *Module) standardsTable(genaiGateSeen int64, genaiRecordLast time.Time) []ingestionStandardDTO {
	otelGenAI := ingestionStandardDTO{
		ID:           "otel_genai",
		Label:        "OpenTelemetry GenAI semconv",
		Direction:    "in",
		Maturity:     "development",
		Version:      otelGenAIVersion,
		UpstreamRepo: otelGenAIUpstreamRepo,
		UpstreamRef:  otelGenAIUpstreamRef,
		OptInGate:    otelGenAIGate,
		Status:       "opt_in_off",
	}
	if genaiGateSeen > 0 {
		active := true
		otelGenAI.OptInActive = &active
		otelGenAI.Status = "active"
		// Empty (omitted) until a dialect finding proves a RECORD flowed — the
		// posture finding alone proves only the gate (see onEvent).
		otelGenAI.LastSeen = rfc3339(genaiRecordLast)
		// records_total is deliberately OMITTED even when the profile is active:
		// gen_ai-derived observations are NOT distinguishable on the bus — the
		// claude_code dialect's own edges ride the same SignalOTEL
		// (connectors/claude/observations.go:85,151,162,197) as the gen_ai
		// pipeline's (genai_mcp.go:132,193,216), and Event.Source is the
		// connector instance, not the dialect. Counting SignalOTEL edges here
		// would over-attribute claude_code traffic to this standard.
	}
	return []ingestionStandardDTO{
		otelGenAI,
		{
			ID: "ocsf", Label: "OCSF (ai_operation profile)", Direction: "out",
			Maturity: "ga", Version: siemwire.OCSFVersion, Status: "available",
		},
		{
			ID: "asim_agentevent", Label: "Microsoft Sentinel ASIM AgentEvent", Direction: "out",
			Maturity: "pre_1_0", Version: asimVersion, Status: "available",
		},
		{
			ID: "siem_unified", Label: "SIEM unified (CEF / LEEF / syslog / OTLP)", Direction: "out",
			Maturity: "stable", Version: noVersion, Status: "available",
		},
		{
			ID: "ledger_push", Label: "Ledger push transport", Direction: "out",
			Maturity: "development", Version: noVersion, Status: "blocked",
		},
		{
			ID: "prometheus_text", Label: "Prometheus text exposition", Direction: "out",
			Maturity: "stable", Version: promTextVersion, Status: "active",
		},
		{
			ID: "w3c_trace_context", Label: "W3C Trace Context (ledger correlation)", Direction: "in",
			Maturity: "stable", Version: noVersion, Status: "active",
			// No records_total: counting would require a ledger scan per read —
			// the /traces view IS the windowed read over that substrate.
		},
	}
}
