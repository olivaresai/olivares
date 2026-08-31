// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// tenantConfig is the resolved per-tenant recording policy.
type tenantConfig struct {
	namespaces    map[string]bool
	consent       string
	idleSeconds   int64
	retentionDays int64
	aiSummaries   bool
}

// cachedConfig is one cfgCache entry; expires by clock so a config change on
// another node converges within the TTL.
type cachedConfig struct {
	cfg     tenantConfig
	fetched time.Time
}

// cfgTTL bounds how stale the Gate hot path's view of the tenant config can be.
const cfgTTL = 10 * time.Second

// defaultConfig is the policy when a tenant has no config row.
func defaultConfig() tenantConfig {
	ns := make(map[string]bool, len(defaultNamespaces))
	for _, n := range defaultNamespaces {
		ns[n] = true
	}
	return tenantConfig{
		namespaces:    ns,
		consent:       consentNotice,
		idleSeconds:   defaultIdleSeconds,
		retentionDays: defaultRetentionDays,
	}
}

// configOf resolves the tenant's recording policy through the TTL cache. The
// epoch captured before the DB read makes the store conditional: a concurrent
// invalidateConfig (a local PUT) bumps the epoch, so an in-flight stale read
// can never re-cache the pre-update policy over the invalidation.
func (m *Module) configOf(ctx context.Context, tenant model.TenantID) (tenantConfig, error) {
	now := m.clock.Now().Time()
	m.cfgMu.Lock()
	if c, ok := m.cfgCache[tenant]; ok && now.Sub(c.fetched) < cfgTTL {
		m.cfgMu.Unlock()
		return c.cfg, nil
	}
	epoch := m.cfgEpoch[tenant]
	m.cfgMu.Unlock()
	cfg := defaultConfig()
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(configKind)
		if err != nil {
			return err
		}
		rec, found, err := findOne(ctx, repo, eq(colCfgKey, cfgKey))
		if err != nil || !found {
			return err
		}
		cfg = configFromRecord(rec)
		return nil
	})
	if err != nil {
		return tenantConfig{}, err
	}
	m.cfgMu.Lock()
	if m.cfgEpoch[tenant] == epoch {
		m.cfgCache[tenant] = cachedConfig{cfg: cfg, fetched: now}
	}
	m.cfgMu.Unlock()
	return cfg, nil
}

// invalidateConfig drops the cached policy after a config write (local node;
// other nodes converge within cfgTTL) and bumps the epoch so racing reads
// cannot restore the stale entry.
func (m *Module) invalidateConfig(tenant model.TenantID) {
	m.cfgMu.Lock()
	delete(m.cfgCache, tenant)
	m.cfgEpoch[tenant]++
	m.cfgMu.Unlock()
}

// configFromRecord decodes a config row into the resolved policy, falling back
// to defaults field-by-field so a partial row never widens or zeroes the policy.
func configFromRecord(rec model.Record) tenantConfig {
	cfg := defaultConfig()
	var names []string
	if raw := rec.String(colCfgNS); raw != "" {
		if err := json.Unmarshal([]byte(raw), &names); err == nil {
			ns := make(map[string]bool, len(names))
			for _, n := range names {
				if n = strings.TrimSpace(n); n != "" {
					ns[n] = true
				}
			}
			cfg.namespaces = ns
		}
	}
	if c := rec.String(colCfgConsent); c == consentRequired || c == consentNotice {
		cfg.consent = c
	}
	if v := rec.Int(colCfgIdleSecs); v > 0 {
		cfg.idleSeconds = v
	}
	if v := rec.Int(colCfgRetention); v > 0 {
		cfg.retentionDays = v
	}
	cfg.aiSummaries = rec.Bool(colCfgAI)
	return cfg
}

// isBreakGlassCall reports whether the call sits on the MANDATORY recording
// floor (any break-glass route, any principal kind, regardless of config).
func isBreakGlassCall(call api.RecordedCall) bool {
	return strings.HasPrefix(string(call.Permission), breakGlassPermPrefix)
}

// consentExempt reports whether the call is one of the two recording-module
// routes that must stay reachable BEFORE a session exists: the notice (how a
// console learns the policy) and the ack (the consent action itself — it
// opens and acknowledges the session in its own handler).
func consentExempt(call api.RecordedCall) bool {
	return call.Namespace == Namespace && (call.Pattern == "/notice" || call.Pattern == "/ack")
}

// Gate implements api.SessionRecorder. It classifies the call and, on a
// recorded surface, ensures an appendable, consent-satisfied session exists and
// reserves a frame slot — DENY-CLOSED: any failure here keeps the privileged
// handler from running. See doc.go for the scope rules.
func (m *Module) Gate(ctx context.Context, call api.RecordedCall) (api.RecordingDecision, error) {
	if consentExempt(call) {
		return api.RecordingDecision{}, nil
	}
	mandatory := isBreakGlassCall(call)
	if m.data == nil {
		// Wired as the engine's recorder but without a data handle: every candidate
		// call (humans, and anyone on the break-glass floor) is deny-closed.
		return api.RecordingDecision{}, errors.New("recording: no data handle (module not wired)")
	}
	if !mandatory {
		cfg, err := m.configOf(ctx, call.Tenant)
		if err != nil {
			return api.RecordingDecision{}, fmt.Errorf("recording: load config: %w", err)
		}
		if !cfg.namespaces[call.Namespace] {
			return api.RecordingDecision{}, nil
		}
	}
	id, err := m.ensureSession(ctx, call, mandatory)
	if err != nil {
		// A canceled request died with its caller — nothing was denied that anyone
		// is waiting for; emitting the CRITICAL outage finding for it would let any
		// flaky client page the on-call at will.
		if !errors.Is(err, api.ErrRecordingConsentRequired) &&
			!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			m.emitFinding(ctx, call.Tenant, findingRecordingUnavailable, "", sdkmodel.SeverityCritical,
				"Privileged surface DENY-CLOSED: session recording could not gate a request")
		}
		return api.RecordingDecision{}, err
	}
	return api.RecordingDecision{Record: true, Session: id}, nil
}

// retrySleep backs off briefly before the next optimistic retry (capped
// exponential; deterministic). Real time, not the module clock: the contention
// being awaited is real-world transaction interleaving.
func retrySleep(attempt int) {
	d := time.Duration(1<<uint(attempt)) * time.Millisecond
	if d > 50*time.Millisecond {
		d = 50 * time.Millisecond
	}
	time.Sleep(d)
}

// ensureSession finds (or opens) the caller's active recording session,
// enforces consent, seals an idle predecessor, and reserves one frame slot.
func (m *Module) ensureSession(ctx context.Context, call api.RecordedCall, mandatory bool) (model.ID, error) {
	cfg, err := m.configOf(ctx, call.Tenant)
	if err != nil {
		return "", fmt.Errorf("recording: load config: %w", err)
	}
	p := call.Principal
	cred := credOf(p)
	for attempt := 0; attempt < maxAppendRetries; attempt++ {
		now := m.clock.Now()
		var (
			id         model.ID
			consentErr bool
		)
		err := m.data.Mutate(ctx, call.Tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(sessionKind)
			if err != nil {
				return err
			}
			rec, found, err := findOne(ctx, repo, eq(colCred, cred), eq(colOpenGuard, openGuard))
			if err != nil {
				return err
			}
			if found {
				// Idle window passed → seal the stale session and open a fresh one
				// (lazy seal: no background loop needed).
				if last, ok := tsValue(rec, colLastAt); ok &&
					!now.Time().Before(last.Time().Add(time.Duration(cfg.idleSeconds)*time.Second)) {
					if err := m.sealLocked(ctx, sc, rec, now, sealReasonIdle, p.Actor(), p.ActorKind()); err != nil {
						return err
					}
					found = false
				}
				// Consent flipped to "required" after this session auto-opened under
				// "notice": the high-assurance dial must bite ACTIVE operators too, not
				// only those who happen to idle out — seal and force the explicit ack.
				if found && p.Kind == auth.KindUser && cfg.consent == consentRequired &&
					rec.String(colConsentMode) != consentRequired {
					if err := m.sealLocked(ctx, sc, rec, now, sealReasonConsent, p.Actor(), p.ActorKind()); err != nil {
						return err
					}
					found = false
				}
			}
			if !found {
				// Consent (AC-8): a human on "required" must have acked explicitly —
				// the ack handler is the only path that opens their session.
				if p.Kind == auth.KindUser && cfg.consent == consentRequired {
					consentErr = true
					return nil
				}
				rec, err = m.openSession(ctx, sc, call.Tenant, p, now, consentModeFor(p, cfg))
				if err != nil {
					return err
				}
			}
			rec[colReserved] = rec.Int(colReserved) + 1
			rec[colLastAt] = now.String()
			if _, err := repo.Update(ctx, rec); err != nil {
				return err // ErrConflict → retry
			}
			id = model.ID(rec.String(model.ColID))
			return nil
		})
		if err != nil {
			if isConflict(err) {
				retrySleep(attempt)
				continue // concurrent open/reserve on the same credential: re-evaluate
			}
			return "", err
		}
		if consentErr {
			return "", fmt.Errorf("this privileged surface is recorded; acknowledge the recording notice first (POST /v1/m/recording/ack): %w", api.ErrRecordingConsentRequired)
		}
		return id, nil
	}
	return "", errors.New("recording: session reserve conflicted repeatedly")
}

// consentModeFor names how consent was satisfied at auto-open: a token is
// machine automation ("auto", notice given at provisioning); a human under
// "notice" acknowledges by use (the AC-8 banner pattern).
func consentModeFor(p auth.Principal, cfg tenantConfig) string {
	if p.Kind != auth.KindUser {
		return "auto"
	}
	return cfg.consent
}

// credOf is the session anchor: the credential id (login session / token).
func credOf(p auth.Principal) string {
	return string(p.Kind) + ":" + p.CredID.String()
}

// openSession creates the session row and its ledger open anchor in the
// caller's transaction. consent_at is stamped for auto/notice modes; the
// explicit-ack path (handleAck) stamps it itself.
func (m *Module) openSession(ctx context.Context, sc store.Scope, tenant model.TenantID, p auth.Principal, now model.Timestamp, consentMode string) (model.Record, error) {
	repo, err := sc.Ext(sessionKind)
	if err != nil {
		return nil, err
	}
	rec := model.Record{
		colSubject: p.Actor(), colSubjectKind: string(p.Kind), colSubjectUser: p.UserID.String(),
		colCred: credOf(p), colStatus: statusActive,
		colOpenedAt: now.String(), colLastAt: now.String(),
		colReserved: int64(0), colWritten: int64(0), colTipHash: "",
		colOpenSeq: int64(0), colAnchorSeq: int64(0), colSealSeq: int64(0),
		colConsentMode: consentMode, colRetention: retentionClass,
		colOpenGuard: openGuard,
	}
	if consentMode == "auto" || consentMode == consentNotice {
		rec[colConsentAt] = now.String()
	}
	created, err := repo.Create(ctx, rec)
	if err != nil {
		return nil, err
	}
	id := model.ID(created.String(model.ColID))
	ev, err := appendAudit(ctx, sc, p.Actor(), p.ActorKind(), actionSessionOpen, sessionKind, id,
		map[string]any{"consent_mode": consentMode}, nil)
	if err != nil {
		return nil, err
	}
	if ev.Seq == 0 {
		// Opening is the recording-mandate gate: without persisted evidence the
		// privileged action must not proceed.
		return nil, store.ErrAuditSpoolFull
	}
	created[colOpenSeq] = ev.Seq
	return repo.Update(ctx, created)
}

// Record implements api.SessionRecorder: it appends the completed call's frame
// to EXACTLY the session Gate reserved on (dec.Session — loaded by id, never
// re-resolved by credential, so a session sealed mid-request yields a loud gap
// instead of a frame mis-bound into a NEWER session's chain), chains it, and
// anchors the chain into the ledger every anchorEvery frames. A failure here
// can no longer deny the action (it already ran); the reserved>written counter
// pair keeps the gap permanently evident and a HIGH finding makes it loud.
func (m *Module) Record(ctx context.Context, call api.RecordedCall, dec api.RecordingDecision, res api.RecordedResult) error {
	if m.data == nil {
		return errors.New("recording: no data handle")
	}
	if dec.Session.IsZero() {
		return errors.New("recording: Record without a reserved session")
	}
	var lastErr error
	for attempt := 0; attempt < maxAppendRetries; attempt++ {
		now := m.clock.Now()
		err := m.data.Mutate(ctx, call.Tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(sessionKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(ctx, dec.Session)
			if err != nil {
				return err
			}
			if rec.String(colStatus) != statusActive {
				// Sealed mid-request (manual seal, sweep, or the break-glass review).
				// The seal anchor already bound the chain tip — appending after it
				// would break verification; the reserved>written gap is the honest,
				// permanently visible record that an in-flight action lost its frame.
				return errors.New("recording: reserved session was sealed mid-request; frame dropped (gap evidence stands)")
			}
			return m.appendFrame(ctx, sc, repo, rec, call, res, now)
		})
		if err == nil {
			return nil
		}
		lastErr = err
		if isConflict(err) {
			retrySleep(attempt)
			continue
		}
		break
	}
	// Best-effort and loud: the gap is also permanently evident on the session
	// row (reserved > written), which verify and the console surface.
	m.emitFinding(ctx, call.Tenant, findingRecordingGap, "", sdkmodel.SeverityHigh,
		"Recording gap: a privileged action completed but its frame could not be appended (reserved>written)")
	return lastErr
}

// appendFrame writes one chained frame and updates the session row, both in the
// caller's transaction, anchoring the tip into the ledger at the cadence.
func (m *Module) appendFrame(ctx context.Context, sc store.Scope, sessions store.GenericRepo, sess model.Record, call api.RecordedCall, res api.RecordedResult, now model.Timestamp) error {
	frames, err := sc.Ext(frameKind)
	if err != nil {
		return err
	}
	sessionID := model.ID(sess.String(model.ColID))
	prev := zeroHash
	if tip, ok := decodeHexHash(sess.String(colTipHash)); ok {
		prev = tip
	}
	p := call.Principal
	actAs := ""
	if sub, ok := p.ActAs(); ok {
		actAs = sub.String()
	}
	bodySHA := ""
	if len(res.BodySHA256) > 0 {
		bodySHA = hex.EncodeToString(res.BodySHA256)
	}
	f := frameFields{
		Idx: sess.Int(colWritten) + 1, At: now.String(),
		Actor: p.Actor(), ActorKind: p.ActorKind(), ActorUser: p.UserID.String(), ActAs: actAs,
		Namespace: call.Namespace, Method: call.Method, Pattern: call.Pattern, Perm: string(call.Permission),
		Params: redactParams(call.Params), QueryKeys: boundedQueryKeys(call.QueryKeys),
		Status: int64(res.Status), Outcome: outcomeOf(res.Status),
		BodySHA: bodySHA, BodyBytes: res.BodyBytes, DurMS: res.DurationMS,
	}
	hash := frameHash(call.Tenant, sessionID, f, prev)

	frame := model.Record{
		colFrSession: sessionID.String(), colFrIdx: f.Idx, colFrAt: f.At,
		colFrActor: f.Actor, colFrActorKind: f.ActorKind, colFrActorUser: f.ActorUser, colFrActAs: f.ActAs,
		colFrNamespace: f.Namespace, colFrMethod: f.Method, colFrPattern: f.Pattern, colFrPerm: f.Perm,
		colFrParams: canonicalParams(f.Params), colFrQueryKeys: f.QueryKeys,
		colFrStatus: f.Status, colFrOutcome: f.Outcome,
		colFrBodySHA: f.BodySHA, colFrBodyBytes: f.BodyBytes, colFrDurMS: f.DurMS,
		colFrPrevHash: hex.EncodeToString(prev), colFrHash: hex.EncodeToString(hash),
		colFrAnchorSeq: int64(0),
	}

	written := f.Idx
	// Periodic ledger anchor: bind the current tip into the hash-chained,
	// signed ledger so a long-running session is tamper-evident before its seal.
	if written%anchorEvery == 0 {
		ev, err := appendAudit(ctx, sc, p.Actor(), p.ActorKind(), actionSessionAnchor, sessionKind, sessionID,
			map[string]any{"idx": written}, hash)
		if err != nil {
			return err
		}
		if ev.Seq == 0 {
			// The action already ran, so retain the frame and make the evidence gap loud.
			m.errorf("recording: periodic anchor evidence dropped by the degrade spool policy (evidence gap)",
				"session", sessionID.String(), "frame", written)
		}
		frame[colFrAnchorSeq] = ev.Seq
		sess[colAnchorSeq] = ev.Seq
	}
	if _, err := frames.Create(ctx, frame); err != nil {
		return err
	}
	sess[colWritten] = written
	sess[colTipHash] = hex.EncodeToString(hash)
	sess[colLastAt] = now.String()
	_, err = sessions.Update(ctx, sess)
	return err
}

// outcomeOf classifies an HTTP status for the human-readable timeline.
func outcomeOf(status int) string {
	switch {
	case status < 400:
		return "allowed"
	case status == 401 || status == 403:
		return "denied"
	case status < 500:
		return "rejected"
	default:
		return "error"
	}
}

// sealLocked seals a session INSIDE the caller's transaction: terminal status,
// the seal ledger anchor binding the final tip, and the open_guard release.
func (m *Module) sealLocked(ctx context.Context, sc store.Scope, rec model.Record, now model.Timestamp, reason, actor, actorKind string) error {
	repo, err := sc.Ext(sessionKind)
	if err != nil {
		return err
	}
	id := model.ID(rec.String(model.ColID))
	var tipRaw []byte
	if tip, ok := decodeHexHash(rec.String(colTipHash)); ok {
		tipRaw = tip
	}
	ev, err := appendAudit(ctx, sc, actor, actorKind, actionSessionSeal, sessionKind, id, map[string]any{
		"reason": reason, "written": rec.Int(colWritten), "reserved": rec.Int(colReserved),
	}, tipRaw)
	if err != nil {
		return err
	}
	if ev.Seq == 0 {
		// A sealed session is presented as proof; never commit that terminal claim
		// without its persisted ledger anchor.
		return store.ErrAuditSpoolFull
	}
	rec[colStatus] = statusSealed
	rec[colSealedAt] = now.String()
	rec[colSealReason] = reason
	rec[colSealSeq] = ev.Seq
	rec[colOpenGuard] = nil
	_, err = repo.Update(ctx, rec)
	return err
}

// EnsureActive is the governance RecordingGate seam: it returns the
// caller's ACTIVE recording session. By the time a break-glass handler runs,
// the engine wrapper has already gated (and so opened) the session — a miss
// here means the recorder is not actually capturing, and break-glass must
// refuse (deny-closed).
func (m *Module) EnsureActive(ctx context.Context, tenant model.TenantID, p auth.Principal) (model.ID, error) {
	if m.data == nil {
		return "", errors.New("session recording is not wired")
	}
	var id model.ID
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, found, err := findOne(ctx, repo,
			eq(colCred, credOf(p)),
			eq(colStatus, statusActive),
			eq(colOpenGuard, openGuard),
		)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("no active recording session for this credential")
		}
		id = model.ID(rec.String(model.ColID))
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// BindGrantInScope validates and binds the exact session witness returned by
// api.SessionRecorder.Gate inside the caller's transaction. The validation is
// deliberately stronger than a credential lookup: an ID that was sealed,
// superseded or already bound cannot authorize an emergency grant.
func (m *Module) BindGrantInScope(ctx context.Context, sc store.Scope, session, grant model.ID, p auth.Principal) error {
	if session.IsZero() || grant.IsZero() {
		return fmt.Errorf("%w: exact session and grant IDs are required", api.ErrRecordingSessionPrecondition)
	}
	repo, err := sc.Ext(sessionKind)
	if err != nil {
		return err
	}
	rec, err := repo.Get(ctx, session)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: exact gated session was not found", api.ErrRecordingSessionPrecondition)
		}
		return err
	}
	switch {
	case rec.String(colCred) != credOf(p):
		return fmt.Errorf("%w: exact gated session belongs to another credential", api.ErrRecordingSessionPrecondition)
	case rec.String(colStatus) != statusActive:
		return fmt.Errorf("%w: exact gated session is not active", api.ErrRecordingSessionPrecondition)
	case rec.String(colOpenGuard) != openGuard:
		return fmt.Errorf("%w: exact gated session is not open", api.ErrRecordingSessionPrecondition)
	case strings.TrimSpace(rec.String(colBGGrant)) != "":
		return fmt.Errorf("%w: exact gated session is already bound to a break-glass grant", api.ErrRecordingSessionPrecondition)
	}

	// Keep the global tenant-audit lock order audit → session, matching frame
	// append and seal. A later CAS conflict rolls the audit append back with the
	// caller's transaction.
	if _, err := appendAudit(ctx, sc, p.Actor(), p.ActorKind(), actionGrantBind, sessionKind, session,
		map[string]any{"grant": grant.String()}, nil); err != nil {
		return err
	}
	rec[colBGGrant] = grant.String()
	_, err = repo.Update(ctx, rec)
	return err
}

// BindGrant is the governance RecordingGate seam: it stamps the
// break-glass grant onto its recording session (first-class linkage the replay
// console joins on) and appends the bind ledger event. It remains available as
// a standalone compatibility operation; the break-glass HTTP handler uses
// BindGrantInScope so grant creation and binding commit atomically.
func (m *Module) BindGrant(ctx context.Context, tenant model.TenantID, session, grant model.ID, p auth.Principal) error {
	if m.data == nil {
		return errors.New("session recording is not wired")
	}
	var lastErr error
	for attempt := 0; attempt < maxAppendRetries; attempt++ {
		err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
			return m.BindGrantInScope(ctx, sc, session, grant, p)
		})
		if err == nil {
			return nil
		}
		lastErr = err
		if !isConflict(err) {
			break
		}
		// A concurrent Gate/Record on the operator's live session row races this
		// version-guarded update routinely — losing the bind would orphan the
		// grant↔session joint AND the review-driven seal, so it retries.
		retrySleep(attempt)
	}
	return lastErr
}

// SealGrantInScope seals the one ACTIVE session bound to grant inside the
// caller's review transaction. Exactly one active/open binding is required:
// declaring a review complete without that evidence session would be fail-open.
func (m *Module) SealGrantInScope(ctx context.Context, sc store.Scope, grant model.ID, reviewer auth.Principal) error {
	if grant.IsZero() {
		return fmt.Errorf("%w: break-glass grant ID is required", api.ErrRecordingSessionPrecondition)
	}
	repo, err := sc.Ext(sessionKind)
	if err != nil {
		return err
	}
	recs, err := listAll(ctx, repo, eq(colBGGrant, grant.String()))
	if err != nil {
		return err
	}
	if len(recs) != 1 {
		return fmt.Errorf("%w: grant has %d recording-session bindings, want exactly one",
			api.ErrRecordingSessionPrecondition, len(recs))
	}
	rec := recs[0]
	if rec.String(colStatus) != statusActive || rec.String(colOpenGuard) != openGuard {
		return fmt.Errorf("%w: grant's recording session is not active and open",
			api.ErrRecordingSessionPrecondition)
	}
	return m.sealLocked(ctx, sc, rec, m.clock.Now(), sealReasonReview, reviewer.Actor(), reviewer.ActorKind())
}

// sealBoundSessions is the legacy/event-reconciliation path for reviewed
// findings. The authoritative review path has already sealed atomically before
// publishing; normal delivery therefore finds zero active rows and is a no-op.
// Keeping this idempotent path lets delayed pre-upgrade findings converge.
func (m *Module) sealBoundSessions(ctx context.Context, tenant model.TenantID, grant string) error {
	var lastErr error
	for attempt := 0; attempt < maxAppendRetries; attempt++ {
		now := m.clock.Now()
		err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(sessionKind)
			if err != nil {
				return err
			}
			recs, err := listAll(ctx, repo, eq(colBGGrant, grant), eq(colOpenGuard, openGuard))
			if err != nil {
				return err
			}
			for _, rec := range recs {
				if err := m.sealLocked(ctx, sc, rec, now, sealReasonReview, model.ActorSystem, model.ActorSystem); err != nil {
					return err
				}
			}
			return nil
		})
		if err == nil {
			return nil
		}
		lastErr = err
		if !isConflict(err) {
			break
		}
		// The reviewed finding is delivered ONCE (no bus redelivery): losing this
		// seal to a routine version race would leave the emergency recording
		// appendable after the loop supposedly closed — retry, and page on failure.
		retrySleep(attempt)
	}
	m.emitFinding(ctx, tenant, findingRecordingSealFailed, "", sdkmodel.SeverityHigh,
		"Recording session bound to a reviewed break-glass grant could NOT be sealed; seal it manually")
	return lastErr
}
