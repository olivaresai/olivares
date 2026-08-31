// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/updatecheck"
)

// handleSetupStatus returns the onboarding state of the deployment — which
// first-time setup steps have been completed. The console wizard uses this to
// detect first-use and show the appropriate onboarding step. Superadmin-gated
// (no AAL3: it returns no secret).
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	ctx := r.Context()
	steps := []setupStepDTO{
		{ID: "database", Completed: s.checkDBReady(ctx)},
		{ID: "connectors", Completed: s.checkHasConfiguredConnector(ctx)},
		s.identityStep(ctx),
		{ID: "users", Completed: s.checkHasUsers(ctx)},
	}
	// `completed` is computed over the APPLICABLE steps. A step this build cannot
	// complete used to hold it at false permanently, which is how a correct install
	// came to be reported as an unfinished one.
	allDone := true
	for _, step := range steps {
		if step.Applicable != nil && !*step.Applicable {
			continue
		}
		if !step.Completed {
			allDone = false
			break
		}
	}
	writeJSON(w, http.StatusOK, setupStatusDTO{Completed: allDone, Steps: steps})
}

// handleKeyCustody returns the non-secret boot-time key/sealer inventory.
// Superadmin-gated but intentionally not AAL3-gated: this is a secretless
// operational read, like handleHealthSummary.
func (s *Server) handleKeyCustody(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	keys := append([]KeyInfo{}, s.keyCustody.Keys...)
	writeJSON(w, http.StatusOK, KeyCustodyInfo{Keys: keys})
}

// bridgeStatsProvider is deliberately a capability interface rather than a
// concrete *natsbus.Bus assertion. Enterprise bus wrappers that embed the NATS
// bridge promote Bridge() and remain visible through the same seam.
type bridgeStatsProvider interface {
	Bridge() natsbus.BridgeStats
}

// handleBusSnapshot returns the full privileged event-bus snapshot. Subscriber
// identities are appropriate on this superadmin-only console route; the public
// /status projection below never includes them.
func (s *Server) handleBusSnapshot(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}

	var stats eventbus.Stats
	if s.busStats != nil {
		stats = s.busStats.BusStats()
	}
	out := busSnapshotDTO{
		Subscribers:      make([]busSubscriberDTO, 0, len(stats.Subscribers)),
		PublishBlocked:   stats.PublishBlocked,
		Dropped:          stats.Dropped,
		DroppedTelemetry: stats.DroppedTelemetry,
		DroppedNotify:    stats.DroppedNotify,
		HandlerErrors:    stats.HandlerErrors,
		Enqueued:         stats.Enqueued,
		Handled:          stats.Handled,
	}
	for _, subscriber := range stats.Subscribers {
		out.Subscribers = append(out.Subscribers, busSubscriberDTO{
			Name:     subscriber.Name,
			Class:    subscriber.Class.String(),
			Depth:    subscriber.Depth,
			Capacity: subscriber.Capacity,
		})
	}
	if bridge, ok := s.busStats.(bridgeStatsProvider); ok {
		stats := bridge.Bridge()
		out.Bridge = &busBridgeDTO{
			Connected:      stats.Connected,
			PendingMsgs:    stats.PendingMsgs,
			PendingBytes:   stats.PendingBytes,
			Dropped:        stats.Dropped,
			PublishErrors:  stats.PublishErrors,
			DecodeErrors:   stats.DecodeErrors,
			GateSkipped:    stats.GateSkipped,
			InvalidSubject: stats.InvalidSubject,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) checkDBReady(ctx context.Context) bool {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.st.Ping(pingCtx) == nil
}

// checkHasConfiguredConnector reports whether this deployment has a connector
// INSTANCE in the durable roster. It deliberately does NOT ask the connector
// catalog: the catalog lists the kinds this build can wire and is non-empty on
// every install, so the wizard step was green before the operator configured
// anything — a setup step that completes itself is not a setup step.
func (s *Server) checkHasConfiguredConnector(ctx context.Context) bool {
	if s.sourceRoster == nil {
		return false
	}
	sources, err := s.sourceRoster.ListSources(ctx)
	if err != nil {
		return false
	}
	return len(sources) > 0
}

// identityStep reports the setup wizard's identity step.
//
// IT USED TO COUNT USERS (2026-08-05). checkHasIdentity was checkHasUsers with the
// threshold moved from >0 to >1: the same `Users().List(Limit: 2)` in the same
// AuthView, with s.fedSvc used ONLY as a nil guard. It never asked the federation
// service whether an IdP was configured — the one question the step is named after.
//
// Two consequences, and both reached the customer. A correct single-administrator
// install reported the step incomplete, so /v1/console/setup-status answered
// `completed: false` FOREVER and the console told an operator their finished setup
// was unfinished. And when the read failed, `_ =` swallowed the error and the step
// came back as "you have no users" with HTTP 200 — a failure to look reported as a
// measurement.
//
// It now asks FederationService.Configured, and it gives THREE answers: configured,
// not configured (with what to do), or not applicable — because a build with no
// federation service wired cannot complete this step, and a step nobody can finish
// must not hold the whole wizard at false.
func (s *Server) identityStep(ctx context.Context) setupStepDTO {
	step := setupStepDTO{ID: "identity"}
	if s.fedSvc == nil {
		no := false
		step.Applicable = &no
		step.Reason = "no federation service is wired in this build; single sign-on cannot be configured here"
		return step
	}
	configured, err := s.fedSvc.Configured(ctx, model.SystemTenantID)
	if err != nil {
		// COULD NOT LOOK. Not "not configured": the operator must not be told to go
		// and do something we failed to check.
		step.Reason = "the single sign-on configuration could not be read; this is not a statement that it is missing"
		return step
	}
	step.Completed = configured
	if !configured {
		step.Reason = "no identity provider is configured; add one under Settings → Identity, or continue with local accounts"
	}
	return step
}

func (s *Server) checkHasUsers(ctx context.Context) bool {
	var count int
	_ = s.st.AuthView(ctx, func(as store.AuthScope) error {
		users, _, err := as.Users().List(ctx, model.Query{Limit: 2})
		if err != nil {
			return err
		}
		count = len(users)
		return nil
	})
	return count > 0
}

// handleHealthSummary returns an aggregated deployment health snapshot for the
// console dashboard: system readiness, the connector catalog size and the
// separate configured/running/failed connector counts, user count, SSO state.
// Superadmin-gated (no AAL3: no secret data).
func (s *Server) handleHealthSummary(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	ctx := r.Context()

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	healthy := s.st.Ping(pingCtx) == nil
	ready := healthy && s.st.Leader().Active()

	// Two different populations, counted separately and named for what they are.
	// connectors_available is the CATALOG (kinds this build can wire) — a
	// capability of the binary. The runtime numbers all come from the roster:
	// what an operator actually authored (configured), what is ingesting now
	// (running), and what is broken (error). Reporting the catalog under a
	// runtime-sounding name is what made a clean install read "101 active" with
	// zero connectors alive.
	var available, configured, running, connectorsErr int
	// connectorsMeasured distinguishes "the roster says zero" from "the roster
	// could not be read". Both used to serialize as four zeros.
	connectorsMeasured := true
	if s.connectorOnboarding != nil {
		if list, err := s.connectorOnboarding.ListConnectors(ctx); err == nil {
			available = len(list)
		}
	}
	// connectors_running uses the same criterion as the `running` field of
	// GET /v1/connectors/health, and connectors_error the same criterion the
	// public status page (handlePublicStatus) aggregates, so the dashboard tile,
	// the connector-health view and the status page never disagree (the
	// error field was declared but never computed, so it read 0 forever).
	// One classification, shared with GET /v1/connectors/health and GET /status
	// (core/api/rosterclass.go), so the three surfaces cannot disagree about the
	// same roster — which they did: a source the engine REFUSED TO WIRE matched
	// none of the literals here and was reported as zero errors on all three.
	//
	// The read failure is its own answer. `if err == nil` with no else published
	// four zeros as if they were measurements, so an unreadable roster looked
	// exactly like a deployment with nothing configured.
	if s.sourceRoster != nil {
		sources, err := s.sourceRoster.ListSources(ctx)
		if err != nil {
			connectorsMeasured = false
		} else {
			var tally rosterTally
			for _, src := range sources {
				tally.add(src.Enabled, src.Status)
			}
			configured = tally.Total
			running = tally.Running
			connectorsErr = tally.Errored
		}
	}

	// PAGE, do not take the first page and call it the total (2026-08-05). This
	// read was a single `List(Limit: 1000)` followed by `len(users)`, so any
	// deployment with more than a thousand accounts reported exactly 1000 —
	// forever, with no indication that the number was a page size rather than a
	// count. The store exposes no Count, so the honest options are to walk the
	// cursor or to say the answer is bounded; this does both, because an unbounded
	// walk on a dashboard endpoint is its own defect.
	var userCount int
	usersCapped := false
	_ = s.st.AuthView(ctx, func(as store.AuthScope) error {
		q := model.Query{Limit: userCountPageSize}
		for pages := 0; ; pages++ {
			users, page, err := as.Users().List(ctx, q)
			if err != nil {
				return err
			}
			userCount += len(users)
			if !page.HasMore || page.Cursor == "" {
				return nil
			}
			if pages+1 >= userCountMaxPages {
				// The budget stopped us. Reporting userCount as the total here
				// would be the original defect with a bigger constant.
				usersCapped = true
				return nil
			}
			q.Cursor = page.Cursor
		}
	})

	// sso_configured derives from the EFFECTIVE posture, not from the service being
	// wired: boot always constructs the federation service, so fedSvc != nil is
	// vacuously true (the tile read green on every deployment). Configured
	// includes the supported environment-backed fallback when no managed row
	// exists; a managed tombstone remains authoritative and reports false.
	ssoConfigured := false
	if s.fedSvc != nil {
		var err error
		ssoConfigured, err = s.fedSvc.Configured(ctx, auth.GlobalFederationScope)
		if err != nil {
			s.log.Warn("api: sso posture unavailable for health summary", "err", err, "request_id", requestID(ctx))
			ssoConfigured = false
		}
	}
	knowledgeStatus := s.currentKnowledgeStatus(ctx)

	// OTA update indicator: present only when update checking is configured;
	// an air-gapped deployment (nil provider) leaves it absent — silence, not error.
	var update *updatecheck.Status
	if s.updateStatus != nil {
		st := s.updateStatus()
		update = &st
	}

	// Audit spool indicator: like the OTA indicator, an undeclared budget stays
	// absent. A measurement failure is logged but never takes down the aggregate
	// health summary.
	var auditSpool *auditSpoolDTO
	if statuser, ok := s.st.(store.AuditSpoolStatuser); ok {
		status, configured, err := statuser.AuditSpoolStatus(ctx)
		if err != nil {
			s.log.Warn("api: audit spool status unavailable", "err", err, "request_id", requestID(ctx))
		} else if configured {
			auditSpool = &auditSpoolDTO{
				MaxBytes:           status.MaxBytes,
				UsedBytes:          status.UsedBytes,
				Mode:               string(status.OnFull),
				Engaged:            status.Engaged,
				PendingDropTenants: status.PendingDropTenants,
				PendingDrops:       status.PendingDrops,
			}
		}
	}

	var tlsNotAfter string
	var tlsDaysLeft *int64
	if s.tlsCertNotAfter != nil {
		if notAfter, ok := s.tlsCertNotAfter(); ok {
			notAfter = notAfter.UTC()
			tlsNotAfter = notAfter.Format(time.RFC3339)
			daysLeft := int64(notAfter.Sub(s.clock.Now().Time()) / (24 * time.Hour))
			tlsDaysLeft = &daysLeft
		}
	}

	writeJSON(w, http.StatusOK, healthSummaryDTO{
		Healthy:              healthy,
		Ready:                ready,
		StoreEngine:          string(s.st.Engine()),
		ConnectorsAvailable:  available,
		ConnectorsConfigured: configured,
		ConnectorsRunning:    running,
		ConnectorsErr:        connectorsErr,
		ConnectorsMeasured:   falseIfUnmeasured(connectorsMeasured),
		UsersCapped:          trueIfCapped(usersCapped),
		Users:                userCount,
		SSOConfigured:        ssoConfigured,
		Version:              s.version,
		EmbedderKind:         knowledgeStatus.EmbedderKind,
		RetrievalSemantic:    knowledgeStatus.RetrievalSemantic,
		KnowledgeReason:      knowledgeStatus.Reason,
		GuardProfile:         knowledgeStatus.GuardProfile,
		GuardWarning:         knowledgeStatus.GuardWarning,
		GuardDowngradeCount:  knowledgeStatus.GuardDowngradeCount,
		GuardPublicOnlyKBs:   knowledgeStatus.GuardPublicOnlyKBs,
		Update:               update,
		AuditSpool:           auditSpool,
		TLSNotAfter:          tlsNotAfter,
		TLSDaysLeft:          tlsDaysLeft,
	})
}

// falseIfUnmeasured returns a pointer to false when the roster could not be read,
// and nil otherwise. Pointer-and-omitempty rather than a plain bool: a healthy
// response must not grow a field, and `connectors_measured:false` must be an
// explicit statement rather than the zero value of something nobody set.
func falseIfUnmeasured(measured bool) *bool {
	if measured {
		return nil
	}
	no := false
	return &no
}

// The user census walks the keyset cursor rather than trusting one page, under a
// budget so a dashboard request cannot turn into an unbounded scan. 100 000 rows
// is far above any deployment this endpoint serves, and reaching it is REPORTED
// rather than rounded off.
const (
	userCountPageSize = 1000
	userCountMaxPages = 100
)

// trueIfCapped returns a pointer to true when the census hit its budget, nil
// otherwise — so `users` is a count when the field is absent and a lower bound
// when it is present, and neither has to be guessed from its value.
func trueIfCapped(capped bool) *bool {
	if !capped {
		return nil
	}
	yes := true
	return &yes
}
