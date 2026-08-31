// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// connectorSubject is the self-audit subject for the connector's own posture findings (a
// degraded poll), so the ledger/SIEM carries proof of a coverage gap rather than silence.
const connectorSubject = "connector.claude_managed_agents"

// pollLoop runs an immediate first refresh, then re-runs every refresh interval until ctx is
// canceled. It is the GET-poller half of the streaming Gather.
func (s *Source) pollLoop(ctx context.Context, sink sdk.Sink) {
	s.refreshOnce(ctx, sink)
	t := time.NewTicker(s.cfg.refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refreshOnce(ctx, sink)
		}
	}
}

// refreshOnce runs every enabled inventory/governance surface once, best-effort: a fetch
// failure on one surface degrades to a self-audit finding and does not abort the others.
func (s *Source) refreshOnce(ctx context.Context, sink sdk.Sink) {
	at := s.clock()
	if s.cfg.observeVaults {
		s.refreshVaults(ctx, sink, at)
	}
	if s.cfg.observeMemory {
		s.refreshMemory(ctx, sink, at)
	}
	if s.cfg.observeWorkQueue {
		s.refreshWorkQueue(ctx, sink, at)
	}
	if s.cfg.observeSkills {
		s.refreshAgents(ctx, sink, at)
	}
	if s.cfg.observeSessions {
		s.refreshSessions(ctx, sink, at)
	}
	if s.cfg.observeDreams {
		s.refreshDreams(ctx, sink, at)
	}
}

// emit sends one observation, returning false when the sink reports an error (typically a
// canceled context), so the caller stops the current surface promptly.
func (s *Source) emit(ctx context.Context, sink sdk.Sink, obs model.Observation) bool {
	return sink.Emit(ctx, obs) == nil
}

// degrade emits the honest self-audit posture finding for a failed surface fetch. The error
// is scrubbed (httpx never puts the credential in an error, but a body excerpt is cleaned
// defensively) and only its hash is retained.
func (s *Source) degrade(ctx context.Context, sink sdk.Sink, surface string, err error, at time.Time) {
	_ = sink.Emit(ctx, model.FindingReport{
		Kind:        findingSelfAudit,
		Severity:    model.SeverityLow,
		SubjectKind: connectorSubject,
		SubjectRef:  surface,
		Title:       "CMA observation degraded: " + surface,
		DetailHash:  redact.Hash("cma poll degraded surface=" + surface + " err=" + redact.Clean(err.Error())),
		OccurredAt:  at,
	})
}

func (s *Source) refreshVaults(ctx context.Context, sink sdk.Sink, at time.Time) {
	vaults, err := s.cl.fetchVaults(ctx)
	if err != nil {
		s.degrade(ctx, sink, "vaults", err, at)
		return
	}
	for _, v := range vaults {
		if v.archived() {
			continue
		}
		if !s.emit(ctx, sink, vaultEdge(v, s.cfg.workspaceID, at)) {
			return
		}
		if !s.emit(ctx, sink, vaultLateralFinding(v, at)) {
			return
		}
		creds, err := s.cl.fetchCredentials(ctx, v.ID)
		if err != nil {
			s.degrade(ctx, sink, "vault_credentials", err, at)
			continue
		}
		for _, cred := range creds {
			if cred.ArchivedAt != "" {
				continue
			}
			if e, ok := credentialInventoryEdge(v.ID, cred, at); ok {
				if !s.emit(ctx, sink, e) {
					return
				}
			}
			if e, ok := credentialEdge(v.ID, cred, at); ok {
				if !s.emit(ctx, sink, e) {
					return
				}
			}
			for _, f := range credentialFindings(v.ID, cred, at, at) {
				if !s.emit(ctx, sink, f) {
					return
				}
			}
		}
	}
}

func (s *Source) refreshMemory(ctx context.Context, sink sdk.Sink, at time.Time) {
	stores, err := s.cl.fetchMemoryStores(ctx)
	if err != nil {
		s.degrade(ctx, sink, "memory_stores", err, at)
		return
	}
	for _, st := range stores {
		if st.ArchivedAt != "" {
			continue
		}
		if !s.emit(ctx, sink, memoryStoreEdge(st, s.cfg.workspaceID, at)) {
			return
		}
		versions, err := s.cl.fetchMemoryVersions(ctx, st.ID)
		if err != nil {
			s.degrade(ctx, sink, "memory_versions", err, at)
			continue
		}
		for _, v := range versions {
			if f, ok := memoryVersionFinding(st.ID, v, at); ok {
				if !s.emit(ctx, sink, f) {
					return
				}
			}
		}
	}
}

func (s *Source) refreshWorkQueue(ctx context.Context, sink sdk.Sink, at time.Time) {
	envIDs := s.cfg.environmentIDs
	if len(envIDs) == 0 {
		envs, err := s.cl.fetchEnvironments(ctx)
		if err != nil {
			s.degrade(ctx, sink, "environments", err, at)
			return
		}
		for _, e := range envs {
			if e.ArchivedAt != "" {
				continue
			}
			if !s.emit(ctx, sink, environmentEdge(e, s.cfg.workspaceID, at)) {
				return
			}
			if e.selfHosted() {
				envIDs = append(envIDs, e.ID)
			}
		}
	} else {
		// Operator-pinned environments skip discovery but must still be inventoried
		// (fix: pinned mode previously produced stats/work edges for environments
		// the inventory had never seen).
		for _, envID := range envIDs {
			if !s.emit(ctx, sink, environmentEdgeByID(envID, s.cfg.workspaceID, at)) {
				return
			}
		}
	}
	for _, envID := range envIDs {
		stats, err := s.cl.fetchWorkStats(ctx, envID)
		if err != nil {
			s.degrade(ctx, sink, "work_stats", err, at)
			continue
		}
		if f, ok := workQueueFinding(envID, stats, s.cfg.backlog, at); ok {
			if !s.emit(ctx, sink, f) {
				return
			}
		}
		items, err := s.cl.fetchWorkItems(ctx, envID)
		if err != nil {
			s.degrade(ctx, sink, "work_items", err, at)
			continue
		}
		for _, item := range items {
			if e, ok := workItemEdge(item, at); ok {
				if !s.emit(ctx, sink, e) {
					return
				}
			}
		}
	}
}

// refreshAgents observes the agent-DEFINITION governance surface: attached skills
// (supply-chain pin signals), the declared tools[] expanded as PERMITTED edges
// (agentToolEdges) and the multi-agent roster grants (rosterEdges).
func (s *Source) refreshAgents(ctx context.Context, sink sdk.Sink, at time.Time) {
	agents, err := s.cl.fetchAgents(ctx)
	if err != nil {
		s.degrade(ctx, sink, "agent_skills", err, at)
		return
	}
	for _, a := range agents {
		for _, sk := range a.Skills {
			if e, ok := skillEdge(a.ID, sk, at); ok {
				if !s.emit(ctx, sink, e) {
					return
				}
			}
			if f, ok := skillFinding(a.ID, sk, at); ok {
				if !s.emit(ctx, sink, f) {
					return
				}
			}
		}
		for _, e := range agentToolEdges(a, at) {
			if !s.emit(ctx, sink, e) {
				return
			}
		}
		for _, e := range rosterEdges(a, at) {
			if !s.emit(ctx, sink, e) {
				return
			}
		}
	}
}

// refreshSessions observes the ACTIVE session surface (idle/running): memory-store
// mounts with their access mode (+ the read_write poisoning posture), vault use,
// terminal outcome verdicts, and — for multi-agent coordinators — the thread topology.
// Terminated sessions are enriched event-driven (webhook GET-back), not re-listed.
func (s *Source) refreshSessions(ctx context.Context, sink sdk.Sink, at time.Time) {
	sessions, err := s.cl.fetchActiveSessions(ctx)
	if err != nil {
		s.degrade(ctx, sink, "sessions", err, at)
		return
	}
	for _, sess := range sessions {
		if sess.ArchivedAt != "" {
			continue
		}
		for _, obs := range sessionObservations(sess, at) {
			if !s.emit(ctx, sink, obs) {
				return
			}
		}
		if sess.Agent.Multiagent == nil {
			continue
		}
		threads, err := s.cl.fetchThreads(ctx, sess.ID)
		if err != nil {
			s.degrade(ctx, sink, "session_threads", err, at)
			continue
		}
		for _, t := range threads {
			if e, ok := threadEdge(t, at); ok {
				if !s.emit(ctx, sink, e) {
					return
				}
			}
		}
	}
}

// refreshDreams inventories the Dreams research preview and runs the output-store
// admission checks (see dreams.go). The preview is GATED: a 403/404 marks the surface
// honestly absent ONCE (a posture finding) and stops polling it until restart, so a
// no-access org gets a declared gap instead of a finding every refresh interval.
func (s *Source) refreshDreams(ctx context.Context, sink sdk.Sink, at time.Time) {
	if s.dreamsGated {
		return
	}
	dreams, err := s.cl.fetchDreams(ctx)
	if err != nil {
		if dreamsGated(err) {
			var se *httpx.StatusError
			_ = errors.As(err, &se)
			// Latch only once the declaration is DELIVERED: a failed Emit (canceled
			// ctx, sink teardown) must not permanently swallow the one coverage-gap
			// record the latch exists to guarantee.
			if sink.Emit(ctx, dreamsGatedFinding(se.Status, at)) == nil {
				s.dreamsGated = true
			}
			return
		}
		s.degrade(ctx, sink, "dreams", err, at)
		return
	}
	for _, d := range dreams {
		if d.ArchivedAt != "" {
			continue
		}
		for _, obs := range dreamObservations(d, s.cfg.workspaceID, s.cfg.admittedOutputs, at) {
			if !s.emit(ctx, sink, obs) {
				return
			}
		}
		// Admission drift probe: any session observed mounting an UNADMITTED output
		// store (other than the dream's own pipeline session, which produced it) is
		// the gate failing — the HIGH finding the security view persists.
		for _, storeID := range d.outputStoreIDs() {
			if s.cfg.admittedOutputs[storeID] {
				continue
			}
			attached, err := s.cl.fetchSessionsByMemoryStore(ctx, storeID)
			if err != nil {
				s.degrade(ctx, sink, "dream_attach_probe", err, at)
				continue
			}
			for _, sess := range attached {
				if sess.ID == d.SessionID {
					continue
				}
				if !s.emit(ctx, sink, unadmittedAttachFinding(d.ID, storeID, sess, at)) {
					return
				}
			}
		}
	}
}
