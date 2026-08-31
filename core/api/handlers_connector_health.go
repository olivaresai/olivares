// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/store"
)

// connectorHealthDTO is the per-connector health projection returned by
// GET /v1/connectors/health. It composites the live roster status from the
// SourceRoster (running/failed/stopped) with the connector metadata from the
// ConnectorOnboarding catalog. Fields that require composition-root integration
// to populate (latency, error history, trend) are present in the contract and
// filled when the data is available — the endpoint is honest about what it knows.
type connectorHealthDTO struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Title         string `json:"title,omitempty"`
	Tenant        string `json:"tenant,omitempty"`
	Status        string `json:"status"`
	SourceMode    string `json:"source_mode"`
	Enabled       bool   `json:"enabled"`
	PollSeconds   int    `json:"poll_seconds,omitempty"`
	LastPolledAt  string `json:"last_polled_at,omitempty"`
	ErrorCount24h int    `json:"error_count_24h"`
	AvgLatencyMS  int64  `json:"avg_latency_ms"`
	Trend         string `json:"trend"`
	HealthState   string `json:"health_state"`
}

// connectorHealthResponse wraps the per-connector health list. Items is the
// non-nullable array type: a deployment with no connectors wired is the NORMAL
// first-boot posture, and it must read as [] like any other empty page.
type connectorHealthResponse struct {
	Items     JSONArray[connectorHealthDTO] `json:"items"`
	Summary   connectorSummaryDTO           `json:"summary"`
	Timestamp string                        `json:"timestamp"`
}

// connectorSummaryDTO aggregates connector fleet health.
type connectorSummaryDTO struct {
	Total    int `json:"total"`
	Running  int `json:"running"`
	Failed   int `json:"failed"`
	Stopped  int `json:"stopped"`
	Disabled int `json:"disabled"`
}

// handleConnectorHealth returns per-connector health metrics. It composites the
// live source roster status with connector catalog metadata. Gated on
// health:status:read so any admin/viewer with health permission can see it (not
// superadmin-only like the source roster CRUD).
func (s *Server) handleConnectorHealth(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authzTenant(w, r, "health:status:read"); !ok {
		return
	}

	ctx := r.Context()
	now := s.clock.Now()

	// Build the connector catalog index (kind → title) for enrichment.
	titleByKind := make(map[string]string)
	if s.connectorOnboarding != nil {
		if catalog, err := s.connectorOnboarding.ListConnectors(ctx); err == nil {
			for _, c := range catalog {
				titleByKind[c.Kind] = c.Title
			}
		}
	}

	// Read the live roster — the authoritative source of connector running state.
	var items []connectorHealthDTO
	var summary connectorSummaryDTO
	var tally rosterTally
	if s.sourceRoster != nil {
		sources, err := s.sourceRoster.ListSources(ctx)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		items = make([]connectorHealthDTO, 0, len(sources))
		for _, src := range sources {
			state := connectorStateToHealth(src.Enabled, src.Status)
			// The trend follows the shared classification, not the literal
			// "failed": a source the engine refused to wire reported "stable",
			// which is a trend line saying nothing is wrong about a row that has
			// never carried a byte.
			trend := "stable"
			if classifyRosterEntry(src.Enabled, src.Status).isRosterError() {
				trend = "down"
			}

			dto := connectorHealthDTO{
				Name:        src.Name,
				Kind:        src.Kind,
				Title:       titleByKind[src.Kind],
				Tenant:      src.Tenant,
				Status:      src.Status,
				SourceMode:  rosterEntrySourceMode(src),
				Enabled:     src.Enabled,
				PollSeconds: src.PollSeconds,
				Trend:       trend,
				HealthState: state,
			}
			items = append(items, dto)
			tally.add(src.Enabled, src.Status)
		}
		summary.Total = tally.Total
		summary.Running = tally.Running
		summary.Failed = tally.Errored
		summary.Stopped = tally.Halted
		summary.Disabled = tally.Disabled
	} else {
		items = []connectorHealthDTO{}
	}

	writeJSON(w, http.StatusOK, connectorHealthResponse{
		Items:     items,
		Summary:   summary,
		Timestamp: now.String(),
	})
}

// connectorStateToHealth maps a roster row to the health state the console badge
// renders. It takes `enabled` because the row's badge cannot be read from the
// status alone: "not_wired" on a row nobody enabled is nothing to act on, while
// the same status on an ENABLED row is a source the engine refused to start. That
// second case used to render "unknown", a badge an operator reads as benign.
func connectorStateToHealth(enabled bool, status string) string {
	switch classifyRosterEntry(enabled, status) {
	case rosterRunning:
		return "healthy"
	case rosterErrored:
		return "down"
	case rosterHalted:
		return "degraded"
	case rosterDisabled:
		return "unknown"
	default:
		// A status this build does not know, or a row whose enabled flag and
		// status contradict each other. Not "unknown": nobody can act on a badge
		// that says nothing, and an unrecognized state is not evidence of health.
		return "down"
	}
}

// --- Public status page (GET /status) ----------------------------------------

// The public status vocabulary. `not_configured` is deliberately a value of its
// own and NOT a flavor of `degraded`: a fresh, correct install with no optional
// provider is incomplete, not broken, and reporting it as broken trains
// operators to ignore the one word that must stay meaningful. Ordered by
// severity in worst(); anything unrecognized outranks `operational`.
const (
	statusOperational   = "operational"
	statusNotConfigured = "not_configured"
	statusDegraded      = "degraded"
	statusOutage        = "outage"
)

// componentStatusDTO is one component in the public status page.
type componentStatusDTO struct {
	Name                string `json:"name"`
	Status              string `json:"status"`
	EmbedderKind        string `json:"embedder_kind,omitempty"`
	RetrievalSemantic   *bool  `json:"retrieval_semantic,omitempty"`
	Reason              string `json:"reason,omitempty"`
	GuardProfile        string `json:"guard_profile,omitempty"`
	GuardWarning        string `json:"guard_warning,omitempty"`
	GuardDowngradeCount int    `json:"guard_downgrade_count,omitempty"`
}

// publicStatusResponse is the unauthenticated aggregate status returned by
// GET /status — the honest external availability signal. It exposes ONLY
// component-level operational/not_configured/degraded/outage state; no tenant
// data, no connector names, no error details.
type publicStatusResponse struct {
	Status              string               `json:"status"`
	Components          []componentStatusDTO `json:"components"`
	Timestamp           string               `json:"timestamp"`
	EmbedderKind        string               `json:"embedder_kind"`
	RetrievalSemantic   bool                 `json:"retrieval_semantic"`
	KnowledgeReason     string               `json:"knowledge_status_reason,omitempty"`
	GuardProfile        string               `json:"guard_profile"`
	GuardWarning        string               `json:"guard_warning,omitempty"`
	GuardDowngradeCount int                  `json:"guard_downgrade_count,omitempty"`
}

func (s *Server) currentKnowledgeStatus(ctx context.Context) KnowledgeStatus {
	if s.knowledgeStatus == nil {
		// No provider wired at all: this is a composition-root fault, not the
		// operator declining an optional provider — hence impaired, not
		// not_configured.
		return KnowledgeStatus{
			EmbedderKind:      "local-hash",
			RetrievalSemantic: false,
			Reason:            "knowledge_status_provider_unwired",
			Posture:           PostureImpaired,
			GuardProfile:      "acl_aware",
		}
	}
	st := s.knowledgeStatus.KnowledgeStatus(ctx)
	if st.EmbedderKind == "" {
		st.EmbedderKind = "local-hash"
	}
	if st.GuardProfile == "" {
		st.GuardProfile = "acl_aware"
	}
	return st
}

// handlePublicStatus is the unauthenticated status page endpoint. It probes the
// same signals as /readyz (store ping, leader status) and enriches with the
// connector fleet aggregate when available. No sensitive data is exposed.
func (s *Server) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.publicStatusProjection(r.Context(), true))
}

// publicStatusProjection builds the public, non-sensitive status value without
// an HTTP loopback. The support-bundle endpoint calls this same function so its
// status section cannot drift from GET /status — but with observe=false, so a
// diagnostic capture is side-effect-free and can never consume a degraded
// ingest delta the next real /status poll should report (adversarial review m3).
func (s *Server) publicStatusProjection(ctx context.Context, observe bool) publicStatusResponse {
	now := s.clock.Now()

	components := make([]componentStatusDTO, 0, 5)
	overallWorst := statusOperational

	// API: alive by definition (we are serving this request).
	components = append(components, componentStatusDTO{Name: "api", Status: statusOperational})

	// Knowledge: the embedder posture is process-level and non-secret. A local-hash
	// fallback keeps the plane usable but retrieval is lexical, not semantic, so it
	// stays a persistent signal rather than a boot-only log line — but WHICH signal
	// depends on why. No provider at all is the product's deliberate default
	// (not_configured); a half-written provider block, a policy denial or an
	// unreadable gate is a fault (degraded). An operator-driven guard downgrade is a
	// live posture change on a configured plane and stays degraded regardless.
	knowledgeStatus := s.currentKnowledgeStatus(ctx)
	knowledgeComponentStatus := statusOperational
	switch {
	case knowledgeStatus.GuardDowngradeCount > 0:
		knowledgeComponentStatus = statusDegraded
	case knowledgeStatus.RetrievalSemantic:
		knowledgeComponentStatus = statusOperational
	case knowledgeStatus.Posture == PostureNotConfigured:
		knowledgeComponentStatus = statusNotConfigured
	default:
		knowledgeComponentStatus = statusDegraded
	}
	overallWorst = worst(overallWorst, knowledgeComponentStatus)
	semantic := knowledgeStatus.RetrievalSemantic
	components = append(components, componentStatusDTO{
		Name:                "knowledge",
		Status:              knowledgeComponentStatus,
		EmbedderKind:        knowledgeStatus.EmbedderKind,
		RetrievalSemantic:   &semantic,
		Reason:              knowledgeStatus.Reason,
		GuardProfile:        knowledgeStatus.GuardProfile,
		GuardWarning:        knowledgeStatus.GuardWarning,
		GuardDowngradeCount: knowledgeStatus.GuardDowngradeCount,
	})

	// Store: ping with a short timeout.
	storeStatus := statusOperational
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.st.Ping(pingCtx); err != nil {
		storeStatus = statusOutage
		overallWorst = worst(overallWorst, statusOutage)
	} else if !s.st.Leader().Active() {
		storeStatus = statusDegraded
		overallWorst = worst(overallWorst, statusDegraded)
	}
	components = append(components, componentStatusDTO{Name: "store", Status: storeStatus})

	// Connectors: aggregate fleet status (no names/details).
	connStatus := statusOperational
	if s.sourceRoster != nil {
		sources, err := s.sourceRoster.ListSources(ctx)
		switch {
		case err != nil:
			// COULD NOT LOOK is not clean. This branch used to be `if err == nil`
			// with no else, so a roster the engine could not read left the public
			// status page saying "connectors: operational" — the monitoring signal
			// went green precisely when nobody knew whether the sources worked.
			connStatus = statusDegraded
		default:
			var tally rosterTally
			for _, src := range sources {
				tally.add(src.Enabled, src.Status)
			}
			// The denominator is the ENABLED rows: a fleet of ten with nine
			// switched off and the tenth broken is a total outage of what the
			// operator asked to be running, and diluting it by the disabled rows
			// would report it as merely degraded.
			if enabled := tally.enabledTotal(); enabled > 0 {
				if tally.Errored == enabled {
					connStatus = statusOutage
				} else if tally.Errored > 0 || tally.Halted > 0 {
					connStatus = statusDegraded
				}
			}
		}
	}
	overallWorst = worst(overallWorst, connStatus)
	components = append(components, componentStatusDTO{Name: "connectors", Status: connStatus})

	// Ingest: when the bus exposes saturation counters, derive this component from
	// counter DELTAS and current queue pressure. The public response carries only
	// the coarse status; subscriber/module names stay on the privileged console
	// route. Embedders without the optional provider retain the historical
	// store-mirror fallback because there is no more truthful signal available.
	ingestStatus := s.currentIngestStatus(storeStatus, observe)
	overallWorst = worst(overallWorst, ingestStatus)
	components = append(components, componentStatusDTO{Name: "ingest", Status: ingestStatus})

	// Audit spool: present only when an audit-ledger budget is
	// declared — silence otherwise, like the console summary. The public page
	// carries only the COARSE posture (no byte counts, no per-tenant drop data):
	// an engaged block-mode spool refuses governed writes (outage), an engaged
	// degrade-mode spool is dropping evidence under the declared policy
	// (degraded). Both are exactly what a status page exists to say out loud.
	if statuser, ok := s.st.(store.AuditSpoolStatuser); ok {
		spool, configured, err := statuser.AuditSpoolStatus(ctx)
		if err != nil {
			s.log.Warn("api: audit spool status unavailable for public status", "err", err, "request_id", requestID(ctx))
		} else if configured {
			spoolStatus := statusOperational
			reason := ""
			if spool.Engaged {
				if spool.OnFull == store.AuditSpoolBlock {
					spoolStatus = statusOutage
					reason = "audit spool budget exhausted; governed writes are refused (mode=block)"
				} else {
					spoolStatus = statusDegraded
					reason = "audit spool budget engaged; evidence is dropping under the declared policy (mode=degrade)"
				}
			}
			overallWorst = worst(overallWorst, spoolStatus)
			components = append(components, componentStatusDTO{Name: "audit_spool", Status: spoolStatus, Reason: reason})
		}
	}

	return publicStatusResponse{
		Status:              overallWorst,
		Components:          components,
		Timestamp:           now.String(),
		EmbedderKind:        knowledgeStatus.EmbedderKind,
		RetrievalSemantic:   knowledgeStatus.RetrievalSemantic,
		KnowledgeReason:     knowledgeStatus.Reason,
		GuardProfile:        knowledgeStatus.GuardProfile,
		GuardWarning:        knowledgeStatus.GuardWarning,
		GuardDowngradeCount: knowledgeStatus.GuardDowngradeCount,
	}
}

// currentIngestStatus observes one serialized bus snapshot. Cumulative loss and
// backpressure counters are degraded only when they INCREASED since the previous
// observation; a historical non-zero counter is not a permanent incident.
// Independently, a queue at or above 80% capacity is degraded immediately.
// observe=false computes the same verdict WITHOUT advancing the delta
// baseline (side-effect-free peek for diagnostic captures).
func (s *Server) currentIngestStatus(storeStatus string, observe bool) string {
	if s.busStats == nil {
		return storeStatus
	}

	s.busStatusMu.Lock()
	defer s.busStatusMu.Unlock()

	current := s.busStats.BusStats()
	degraded := false
	if s.busHasLastStats {
		degraded = busLossIncreased(s.busLastStats, current)
	}
	if !degraded {
		for _, subscriber := range current.Subscribers {
			if subscriber.Capacity > 0 &&
				float64(subscriber.Depth)/float64(subscriber.Capacity) >= 0.8 {
				degraded = true
				break
			}
		}
	}
	if observe {
		s.busLastStats = cloneBusStats(current)
		s.busLastObservedAt = s.clock.Now().Time()
		s.busHasLastStats = true
	}
	if degraded {
		return statusDegraded
	}
	return statusOperational
}

func busLossIncreased(previous, current eventbus.Stats) bool {
	return current.PublishBlocked > previous.PublishBlocked ||
		current.Dropped > previous.Dropped ||
		current.DroppedTelemetry > previous.DroppedTelemetry ||
		current.DroppedNotify > previous.DroppedNotify
}

func cloneBusStats(stats eventbus.Stats) eventbus.Stats {
	stats.Subscribers = append([]eventbus.SubscriberStats(nil), stats.Subscribers...)
	return stats
}

// worst returns the more-severe of two status strings. `not_configured` outranks
// `operational` (the plane is delivering less than the whole product) but loses to
// `degraded`: a real fault anywhere always dominates the aggregate word, so an
// unprovisioned optional capability can never mask a broken component. Anything
// unrecognized is treated as a fault, never as healthy.
func worst(a, b string) string {
	r := func(s string) int {
		switch s {
		case statusOutage:
			return 3
		case statusOperational:
			return 0
		case statusNotConfigured:
			return 1
		default: // degraded, and anything this build does not recognize
			return 2
		}
	}
	if r(b) > r(a) {
		return b
	}
	return a
}
