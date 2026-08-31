// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	voiceconn "github.com/olivaresai/olivares/connectors/voice"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

const (
	callTransportSIP        = "sip"
	callProviderOpenAI      = "openai"
	callDefaultModel        = "gpt-realtime-2"
	callRejectDecline       = 603
	defaultMaxCallObservers = 32
	defaultCallSweep        = 15 * time.Second

	costTypeRealtimeAudio = "openai_realtime_audio"
	costTypeRealtimeText  = "openai_realtime_text"

	recordingReasonNoSADControls = "recording_without_sad_controls"
	recordingReasonFinancialSAD  = "financial_data_observed_while_recording"
)

type callPolicyMatch struct {
	policyRef    string
	agentRef     string
	model        string
	instructions string
	recording    callRecordingPolicyDTO
}

type acceptedCall struct {
	tenant       model.TenantID
	callID       string
	eventID      string
	fromRedacted string
	toRedacted   string
	policy       callPolicyMatch
}

type liveCall struct {
	acceptedCall
	modelRef string
	conn     CallSideband
	cancel   context.CancelFunc
}

// RealtimeWebhookHandler returns the fail-closed OpenAI Realtime SIP webhook
// receiver. The composition root mounts it only when a secret and tenant are
// operator-configured; an empty secret still verifies as unauthorized.
func (m *Module) RealtimeWebhookHandler(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, errorBody("method not allowed"))
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxReqBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("cannot read body"))
			return
		}
		if err := voiceconn.VerifyCallWebhook(r.Header, body, secret, m.clock.Now().Time()); err != nil {
			m.debugf("voice: realtime call webhook rejected")
			writeJSON(w, http.StatusUnauthorized, errorBody("signature verification failed"))
			return
		}
		ev, err := voiceconn.ParseCallWebhook(body)
		if err != nil || strings.TrimSpace(ev.EventID) == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("malformed call webhook"))
			return
		}
		if m.markReplaySeen(ev.EventID) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}

		tenant := m.callConfig.Tenant
		if tenant.IsZero() || tenant.IsSystem() || m.data == nil {
			_ = m.callController.Reject(r.Context(), ev.CallID, callRejectDecline)
			m.debugf("voice: realtime call rejected; call plane missing tenant/data wiring")
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}

		fromRedacted := voiceconn.RedactSIPAddress(ev.From())
		toRedacted := voiceconn.RedactSIPAddress(ev.To())
		if m.callStopBlocks(r.Context(), tenant, ev.CallID, ev.EventID, fromRedacted, toRedacted) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}

		match, ok, err := m.matchCallPolicy(r.Context(), tenant, ev.From(), ev.To())
		if err != nil {
			_ = m.callController.Reject(r.Context(), ev.CallID, callRejectDecline)
			m.debugf("voice: realtime call policy lookup failed", "err", err)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		call := acceptedCall{
			tenant:       tenant,
			callID:       ev.CallID,
			eventID:      ev.EventID,
			fromRedacted: fromRedacted,
			toRedacted:   toRedacted,
			policy:       match,
		}
		if !ok {
			m.rejectCallForPolicy(r.Context(), call)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}

		if err := m.callController.Accept(r.Context(), ev.CallID, CallAccept{
			Model:        match.model,
			Instructions: match.instructions,
		}); err != nil {
			m.recordCallOpenFailed(r.Context(), call, "accept failed")
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if err := m.recordCallAccepted(r.Context(), call); err != nil {
			// Deny-closed: a live call without governed-open evidence is worse
			// than a dropped call — hang up best-effort and skip the observer.
			m.errorf("voice: realtime call accepted but evidence write failed; hanging up fail-closed", "call_ref", ev.CallID, "err", err)
			if herr := m.callController.Hangup(r.Context(), ev.CallID); herr != nil {
				m.debugf("voice: fail-closed hangup after evidence failure failed", "err", herr)
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		m.recordingPostureFinding(r.Context(), call)
		m.startSidebandObserver(r.Context(), call)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
}

func (m *Module) markReplaySeen(eventID string) bool {
	now := m.clock.Now().Time()
	expireBefore := now
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, exp := range m.replay {
		if exp.Before(expireBefore) {
			delete(m.replay, id)
		}
	}
	if exp, ok := m.replay[eventID]; ok && exp.After(now) {
		return true
	}
	m.replay[eventID] = now.Add(voiceconn.CallWebhookReplayWindow)
	return false
}

func (m *Module) callStopBlocks(ctx context.Context, tenant model.TenantID, callID, eventID, fromRedacted, toRedacted string) bool {
	verdict, err := m.stopGate.Check(ctx, tenant, StopDims{})
	if err == nil && !verdict.Stopped {
		return false
	}
	stopRef, stopScope := verdict.StopRef, verdict.Scope
	detail := "emergency stop active"
	if err != nil {
		stopRef, stopScope, detail = "state-unreadable", "estate", "kill-switch state unreadable"
	}
	if rerr := m.callController.Reject(ctx, callID, callRejectDecline); rerr != nil {
		m.debugf("voice: realtime call reject under stop failed", "err", rerr)
	}
	call := acceptedCall{
		tenant:       tenant,
		callID:       callID,
		eventID:      eventID,
		fromRedacted: fromRedacted,
		toRedacted:   toRedacted,
	}
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := m.recordDecision(ctx, sc, decisionRow{
			sessionRef: callID, op: opOpenRequest, opStatus: opStatusBlocked, policyVerdict: verdictDenied,
			actor: model.ActorSystem, actorKind: model.ActorSystem, result: detail,
		}); err != nil {
			return err
		}
		return m.auditCallEvent(ctx, sc, "voice.call.reject", call, map[string]any{
			"reason": stopRef, "stop_ref": stopRef, "stop_scope": stopScope, "op_status": opStatusBlocked,
		})
	}); err != nil {
		m.errorf("voice: failed to record stopped call rejection", "call_ref", callID, "err", err)
	}
	return true
}

func (m *Module) matchCallPolicy(ctx context.Context, tenant model.TenantID, rawFrom, rawTo string) (callPolicyMatch, bool, error) {
	var matches []callPolicyMatch
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(policyKind)
		if err != nil {
			return err
		}
		recs, err := listAll(ctx, repo)
		if err != nil {
			return err
		}
		sort.Slice(recs, func(i, j int) bool {
			return recs[i].String(model.ColID) < recs[j].String(model.ColID)
		})
		for _, rec := range recs {
			calls := parseCallPolicyDTO(rec.String(colCallsJSON))
			if calls == nil || !calls.Enabled || len(calls.ToPatterns) == 0 {
				continue
			}
			if !callPatternsMatch(rawTo, calls.ToPatterns, false) {
				continue
			}
			if !callPatternsMatch(rawFrom, calls.FromPatterns, true) {
				continue
			}
			matches = append(matches, callPolicyMatch{
				policyRef:    rec.String(model.ColID),
				agentRef:     rec.String(colPolAgentRef),
				model:        strings.TrimSpace(calls.Model),
				instructions: calls.GuardrailInstructions,
				recording:    calls.Recording,
			})
		}
		return nil
	})
	if err != nil || len(matches) == 0 {
		return callPolicyMatch{}, false, err
	}
	return matches[0], true, nil
}

func callPatternsMatch(raw string, patterns []string, emptyAny bool) bool {
	if len(patterns) == 0 {
		return emptyAny
	}
	got := sipUserDigits(raw)
	if got == "" {
		return false
	}
	for _, p := range patterns {
		trimmed := strings.TrimSpace(p)
		want := digitsOnly(trimmed)
		if want == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "*") {
			if strings.HasSuffix(got, want) {
				return true
			}
			continue
		}
		// A plain pattern is an EXACT digit match: this is an allowlist, and a
		// suffix match here would silently admit look-alike numbers.
		if got == want {
			return true
		}
	}
	return false
}

func sipUserDigits(raw string) string {
	s := strings.TrimSpace(raw)
	lower := strings.ToLower(s)
	start := 0
	if i := strings.Index(lower, "sips:"); i >= 0 {
		start = i + len("sips:")
	} else if i := strings.Index(lower, "sip:"); i >= 0 {
		start = i + len("sip:")
	} else if i := strings.IndexByte(s, ':'); i >= 0 {
		start = i + 1
	}
	user := s[start:]
	if i := strings.IndexByte(user, '@'); i >= 0 {
		user = user[:i]
	}
	user = strings.Trim(user, "<>\"' ")
	return digitsOnly(user)
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (m *Module) rejectCallForPolicy(ctx context.Context, call acceptedCall) {
	if err := m.callController.Reject(ctx, call.callID, callRejectDecline); err != nil {
		m.debugf("voice: realtime call reject failed", "err", err)
	}
	if err := m.data.Mutate(ctx, call.tenant, func(sc store.Scope) error {
		if err := m.recordDecision(ctx, sc, decisionRow{
			sessionRef: call.callID, op: opOpenRequest, opStatus: opStatusBlocked, policyVerdict: verdictDenied,
			actor: model.ActorSystem, actorKind: model.ActorSystem, result: "no matching call policy",
		}); err != nil {
			return err
		}
		if err := m.persistFinding(ctx, sc, finding{
			kind: busPolicyViolation, severity: sdkmodel.SeverityMedium, subjectKind: "call", subjectRef: call.callID,
			title: "realtime SIP call rejected by voice policy", detail: "no enabled call policy matched the redacted destination",
			meta: map[string]any{"call_ref": clamp(call.callID, maxRefLen), "from_redacted": call.fromRedacted, "to_redacted": call.toRedacted},
		}); err != nil {
			return err
		}
		return m.auditCallEvent(ctx, sc, "voice.call.reject", call, map[string]any{
			"reason": "no_matching_call_policy", "op_status": opStatusBlocked,
		})
	}); err != nil {
		m.errorf("voice: failed to record policy-rejected call", "call_ref", call.callID, "err", err)
	}
	m.emitFinding(ctx, call.tenant, busPolicyViolation, sdkmodel.SeverityMedium, "call", call.callID,
		"realtime SIP call rejected by voice policy", "no enabled call policy matched")
}

func (m *Module) recordCallOpenFailed(ctx context.Context, call acceptedCall, result string) {
	if err := m.data.Mutate(ctx, call.tenant, func(sc store.Scope) error {
		if err := m.recordDecision(ctx, sc, decisionRow{
			sessionRef: call.callID, agentRef: call.policy.agentRef, reqModel: acceptedModelRef(call.policy.model),
			reqProvider: callProviderOpenAI, policyRef: call.policy.policyRef, op: opOpen, opStatus: opStatusFailed,
			policyVerdict: verdictAllowed, actor: model.ActorSystem, actorKind: model.ActorSystem, result: result,
		}); err != nil {
			return err
		}
		return m.auditCallEvent(ctx, sc, "voice.call.accept", call, map[string]any{"op_status": opStatusFailed})
	}); err != nil {
		m.errorf("voice: failed to record call accept failure", "call_ref", call.callID, "err", err)
	}
}

func (m *Module) recordCallAccepted(ctx context.Context, call acceptedCall) error {
	return m.data.Mutate(ctx, call.tenant, func(sc store.Scope) error {
		if err := m.markCallGovernedOpen(ctx, sc, call); err != nil {
			return err
		}
		if err := m.recordDecision(ctx, sc, decisionRow{
			sessionRef: call.callID, agentRef: call.policy.agentRef, reqModel: acceptedModelRef(call.policy.model),
			reqProvider: callProviderOpenAI, policyRef: call.policy.policyRef, op: opOpen, policyVerdict: verdictAllowed,
			opStatus: opStatusDispatched, dispatchRef: call.callID, actor: model.ActorSystem, actorKind: model.ActorSystem,
			result: "accepted",
		}); err != nil {
			return err
		}
		return m.auditCallEvent(ctx, sc, "voice.call.accept", call, map[string]any{"op_status": opStatusDispatched})
	})
}

func (m *Module) markCallGovernedOpen(ctx context.Context, sc store.Scope, call acceptedCall) error {
	repo, err := sc.Ext(sessionKind)
	if err != nil {
		return err
	}
	rec, found, err := findOne(ctx, repo, eq(colSessionRef, clamp(call.callID, maxRefLen)))
	if err != nil {
		return err
	}
	if !found {
		atTS := m.clock.Now().String()
		rec = model.Record{
			colSessionRef: clamp(call.callID, maxRefLen),
			colUserTurns:  int64(0), colAgentTurns: int64(0), colDurationMS: int64(0),
			colLatencyCount: int64(0), colLatencySumMS: int64(0), colLatencyMaxMS: int64(0),
			colFirstEventAt: atTS, colLastEventAt: atTS,
		}
	}
	rec[colAgentRef] = clamp(call.policy.agentRef, maxRefLen)
	rec[colModelRef] = acceptedModelRef(call.policy.model)
	rec[colProviderRef] = callProviderOpenAI
	rec[colPrincipalRef] = model.ActorSystem
	rec[colPolicyRef] = call.policy.policyRef
	rec[colGoverned] = true
	rec[colTransport] = callTransportSIP
	rec[colCallRef] = clamp(call.callID, maxRefLen)
	rec[colFromRedacted] = clamp(call.fromRedacted, maxRefLen)
	rec[colToRedacted] = clamp(call.toRedacted, maxRefLen)
	if found {
		_, err = repo.Update(ctx, rec)
	} else {
		_, err = repo.Create(ctx, rec)
	}
	return err
}

func (m *Module) auditCallEvent(ctx context.Context, sc store.Scope, action string, call acceptedCall, meta map[string]any) error {
	clean := map[string]any{
		"call_ref":      clamp(call.callID, maxRefLen),
		"event_id":      clamp(call.eventID, maxRefLen),
		"from_redacted": clamp(call.fromRedacted, maxRefLen),
		"to_redacted":   clamp(call.toRedacted, maxRefLen),
	}
	for k, v := range meta {
		clean[k] = v
	}
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: action,
		TargetKind: sessionKind, TargetID: parseIDOrZero(call.callID), Meta: clean,
	})
	return err
}

func acceptedModelRef(modelRef string) string {
	if strings.TrimSpace(modelRef) == "" {
		return callDefaultModel
	}
	return strings.TrimSpace(modelRef)
}

func (m *Module) recordingPostureFinding(ctx context.Context, call acceptedCall) {
	rec := call.policy.recording
	if !rec.Active || rec.DTMFMasking || rec.PauseResume {
		return
	}
	m.persistAndEmitCallFinding(ctx, call, busRecordingSADRisk, sdkmodel.SeverityHigh,
		"call", "voice recording active without SAD controls",
		"recording active without dtmf masking or pause/resume", map[string]any{
			"reason": recordingReasonNoSADControls,
		})
}

func (m *Module) startSidebandObserver(ctx context.Context, call acceptedCall) {
	if m.sidebandAttacher == nil {
		return
	}
	lc := &liveCall{acceptedCall: call, modelRef: acceptedModelRef(call.policy.model)}
	if !m.reserveLiveCall(lc) {
		m.persistAndEmitCallFinding(ctx, call, busTranscriptUnclassified, sdkmodel.SeverityMedium,
			"call", "realtime SIP call observer not started",
			"sideband observer capacity exceeded", map[string]any{"reason": "observer_capacity_exceeded"})
		return
	}
	conn, err := m.sidebandAttacher(ctx, call.callID)
	if err != nil {
		m.releaseLiveCall(call.callID)
		m.debugf("voice: sideband attach failed", "err", err)
		return
	}
	obsCtx, cancel := context.WithCancel(context.Background())
	lc.conn = conn
	lc.cancel = cancel
	m.mu.Lock()
	if cur, ok := m.liveCalls[call.callID]; ok {
		cur.conn = conn
		cur.cancel = cancel
	}
	m.mu.Unlock()
	m.callWG.Add(1)
	go func() {
		defer m.callWG.Done()
		defer m.releaseLiveCall(call.callID)
		defer conn.Close()
		m.observeSideband(obsCtx, lc)
	}()
}

func (m *Module) reserveLiveCall(lc *liveCall) bool {
	limit := m.callConfig.MaxObservers
	if limit <= 0 {
		limit = defaultMaxCallObservers
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.liveCalls[lc.callID]; exists {
		return false
	}
	if len(m.liveCalls) >= limit {
		return false
	}
	m.liveCalls[lc.callID] = lc
	return true
}

func (m *Module) releaseLiveCall(callID string) {
	m.mu.Lock()
	delete(m.liveCalls, callID)
	m.mu.Unlock()
}

func (m *Module) observeSideband(ctx context.Context, lc *liveCall) {
	if strings.TrimSpace(lc.policy.instructions) != "" {
		if update, err := voiceconn.GuardrailSessionUpdate(lc.policy.instructions); err == nil {
			if err := lc.conn.WriteText(ctx, update); err != nil {
				m.debugf("voice: guardrail sideband update failed", "err", err)
			}
		}
	}
	seenResponses := map[string]struct{}{}
	for {
		raw, err := lc.conn.ReadMessage(ctx)
		if err != nil {
			return
		}
		ev, err := voiceconn.ParseSidebandEvent(raw)
		if err != nil {
			m.debugf("voice: sideband event dropped", "err", err)
			continue
		}
		if ev.Usage != nil {
			m.observeUsage(ctx, lc, *ev.Usage, seenResponses)
		}
		if ev.Transcript != nil {
			m.observeTranscript(ctx, lc, *ev.Transcript)
		}
	}
}

func (m *Module) observeUsage(ctx context.Context, lc *liveCall, usage voiceconn.ResponseUsage, seen map[string]struct{}) {
	m.maybeReportUngovernedCall(ctx, lc.tenant, lc.callID, lc.policy.agentRef)
	m.publishCallTelemetry(ctx, lc, usage)
	if usage.ResponseID != "" {
		if _, ok := seen[usage.ResponseID]; ok {
			return
		}
		seen[usage.ResponseID] = struct{}{}
	}
	m.publishRealtimeCosts(ctx, lc, usage)
}

func (m *Module) publishCallTelemetry(ctx context.Context, lc *liveCall, usage voiceconn.ResponseUsage) {
	if m.host == nil {
		return
	}
	now := m.clock.Now()
	modelRef := usage.Model
	if strings.TrimSpace(modelRef) == "" {
		modelRef = lc.modelRef
	}
	vt := Telemetry{
		SessionRef: lc.callID, AgentRef: lc.policy.agentRef, ModelRef: modelRef, ProviderRef: callProviderOpenAI,
		Role: "agent", TurnDelta: 1, OccurredAt: now.String(),
	}
	if err := m.host.Publish(ctx, event.Event{
		Type: TypeVoiceTelemetry, Tenant: lc.tenant.String(), Source: "module:" + Namespace, Time: now.Time(), Payload: vt,
	}); err != nil {
		m.debugf("voice: publish call telemetry failed", "err", err)
	}
}

func (m *Module) publishRealtimeCosts(ctx context.Context, lc *liveCall, usage voiceconn.ResponseUsage) {
	if m.host == nil {
		return
	}
	modelRef := usage.Model
	if strings.TrimSpace(modelRef) == "" {
		modelRef = lc.modelRef
	}
	pricing, priced := voiceconn.RealtimePricing(modelRef)
	occurred := m.clock.Now().Time()
	samples := []sdkmodel.CostSample{
		m.realtimeCostSample(modelRef, lc.callID, m.callConfig.WorkspaceRef, costTypeRealtimeAudio,
			usage.InputAudioTokens, usage.CachedAudioTokens, usage.OutputAudioTokens, occurred, pricing.AudioInPerM, pricing.AudioOutPerM, pricing.CachedInPerM, priced),
		m.realtimeCostSample(modelRef, lc.callID, m.callConfig.WorkspaceRef, costTypeRealtimeText,
			usage.InputTextTokens, usage.CachedTextTokens, usage.OutputTextTokens, occurred, pricing.TextInPerM, pricing.TextOutPerM, pricing.CachedInPerM, priced),
	}
	for _, sample := range samples {
		if sample.InputTokens == 0 && sample.CacheReadTokens == 0 && sample.OutputTokens == 0 {
			continue
		}
		if err := m.host.Publish(ctx, event.FromObservation(lc.tenant.String(), "module:"+Namespace, sample)); err != nil {
			m.debugf("voice: publish realtime cost failed", "err", err)
		}
	}
}

func (m *Module) realtimeCostSample(modelRef, callID, workspaceRef, costType string, in, cached, out int64, occurred time.Time, inPerM, outPerM, cachedPerM int64, priced bool) sdkmodel.CostSample {
	uncached := in - cached
	if uncached < 0 {
		uncached = 0
	}
	cost := int64(0)
	if priced {
		cost = (uncached*inPerM + out*outPerM + cached*cachedPerM) / 1_000_000
	}
	return sdkmodel.CostSample{
		ProviderRef: callProviderOpenAI, ModelRef: modelRef, SessionRef: callID,
		InputTokens: in, CacheReadTokens: cached, OutputTokens: out, CostMicroUSD: cost,
		OccurredAt: occurred, WorkspaceRef: workspaceRef, Provenance: sdkmodel.ProvenanceEstimated,
		CostType: costType,
	}
}

func (m *Module) observeTranscript(ctx context.Context, lc *liveCall, tr voiceconn.TranscriptDone) {
	m.maybeReportUngovernedCall(ctx, lc.tenant, lc.callID, lc.policy.agentRef)
	if m.transcriptClassifier == nil {
		m.reportUnclassifiedTranscriptOnce(ctx, lc.acceptedCall, "classifier_unwired")
		return
	}
	hits, err := m.transcriptClassifier.Classify(tr.Transcript)
	if err != nil {
		m.reportUnclassifiedTranscriptOnce(ctx, lc.acceptedCall, "classifier_error")
		return
	}
	classes, total := sensitivityClasses(hits)
	if !containsClass(classes, "pii.financial") || !lc.policy.recording.Active {
		return
	}
	m.persistAndEmitCallFinding(ctx, lc.acceptedCall, busRecordingSADRisk, sdkmodel.SeverityHigh,
		"call", "financial data observed while call recording was active",
		"financial-data class observed while recording active", map[string]any{
			"reason": recordingReasonFinancialSAD, "classes": classes, "hit_count": total,
		})
}

func sensitivityClasses(hits []SensitivityHit) ([]string, int) {
	counts := map[string]int{}
	total := 0
	for _, h := range hits {
		if h.Class == "" {
			continue
		}
		n := h.Count
		if n <= 0 {
			n = 1
		}
		counts[h.Class] += n
		total += n
	}
	classes := make([]string, 0, len(counts))
	for class := range counts {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	return classes, total
}

func containsClass(classes []string, want string) bool {
	i := sort.SearchStrings(classes, want)
	return i < len(classes) && classes[i] == want
}

func (m *Module) reportUnclassifiedTranscriptOnce(ctx context.Context, call acceptedCall, reason string) {
	m.mu.Lock()
	if _, ok := m.unclassified[call.callID]; ok {
		m.mu.Unlock()
		return
	}
	m.unclassified[call.callID] = struct{}{}
	m.mu.Unlock()
	m.persistAndEmitCallFinding(ctx, call, busTranscriptUnclassified, sdkmodel.SeverityMedium,
		"call", "realtime transcript was not classified",
		"transcript event could not be classified", map[string]any{"reason": reason})
}

func (m *Module) maybeReportUngovernedCall(ctx context.Context, tenant model.TenantID, callID, agentRef string) {
	if m.hasOpenDispatched(ctx, tenant, callID) {
		return
	}
	m.mu.Lock()
	if _, ok := m.ungoverned[callID]; ok {
		m.mu.Unlock()
		return
	}
	m.ungoverned[callID] = struct{}{}
	m.mu.Unlock()
	call := acceptedCall{tenant: tenant, callID: callID, policy: callPolicyMatch{agentRef: agentRef}}
	m.persistAndEmitCallFinding(ctx, call, busRealtimeUngoverned, sdkmodel.SeverityHigh,
		"call", "realtime call observed without governed open evidence",
		"call sideband or telemetry referenced a call without an open/dispatched decision", nil)
}

func (m *Module) hasOpenDispatched(ctx context.Context, tenant model.TenantID, callID string) bool {
	if m.data == nil || tenant.IsZero() {
		return false
	}
	found := false
	_ = m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(decisionKind)
		if err != nil {
			return err
		}
		_, ok, err := findOne(ctx, repo, eq(colDecSessionRef, clamp(callID, maxRefLen)), eq(colOp, opOpen), eq(colOpStatus, opStatusDispatched))
		if err == nil && ok {
			found = true
		}
		return err
	})
	return found
}

func (m *Module) persistAndEmitCallFinding(ctx context.Context, call acceptedCall, kind string, sev sdkmodel.Severity, subjectKind, title, detail string, meta map[string]any) {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["bus_kind"] = kind
	meta["call_ref"] = clamp(call.callID, maxRefLen)
	if call.fromRedacted != "" {
		meta["from_redacted"] = clamp(call.fromRedacted, maxRefLen)
	}
	if call.toRedacted != "" {
		meta["to_redacted"] = clamp(call.toRedacted, maxRefLen)
	}
	if m.data != nil && !call.tenant.IsZero() {
		if err := m.data.Mutate(ctx, call.tenant, func(sc store.Scope) error {
			return m.persistFinding(ctx, sc, finding{
				kind: kind, severity: sev, subjectKind: subjectKind, subjectRef: call.callID,
				title: title, detail: detail, meta: meta,
			})
		}); err != nil {
			m.debugf("voice: persist call finding failed", "kind", kind, "err", err)
		}
	}
	m.emitFinding(ctx, call.tenant, kind, sev, subjectKind, call.callID, title, detail)
}

func (m *Module) startCallSweep() {
	interval := m.callConfig.StopSweepInterval
	if interval <= 0 {
		interval = defaultCallSweep
	}
	m.mu.Lock()
	if m.callCancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.callCancel = cancel
	m.mu.Unlock()
	go m.runCallSweep(ctx, interval)
	if m.log != nil {
		m.log.Info("voice: active realtime call kill-switch sweep started", "interval", interval.String())
	}
}

func (m *Module) runCallSweep(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sweepStoppedCalls(ctx)
		}
	}
}

func (m *Module) sweepStoppedCalls(ctx context.Context) {
	live := m.snapshotLiveCalls()
	for _, lc := range live {
		dec, err := m.stopGate.Check(ctx, lc.tenant, StopDims{AgentRef: lc.policy.agentRef})
		if err == nil && !dec.Stopped {
			continue
		}
		stopRef := dec.StopRef
		if err != nil {
			stopRef = "state-unreadable"
		}
		m.cutLiveCall(ctx, lc, stopRef)
	}
}

func (m *Module) snapshotLiveCalls() []*liveCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.liveCalls))
	for k := range m.liveCalls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*liveCall, 0, len(keys))
	for _, k := range keys {
		out = append(out, m.liveCalls[k])
	}
	return out
}

func (m *Module) cutLiveCall(ctx context.Context, lc *liveCall, stopRef string) {
	m.mu.Lock()
	if cur, ok := m.liveCalls[lc.callID]; !ok || cur != lc {
		m.mu.Unlock()
		return
	}
	delete(m.liveCalls, lc.callID)
	m.mu.Unlock()
	if err := m.callController.Hangup(ctx, lc.callID); err != nil {
		m.debugf("voice: realtime call hangup failed", "err", err)
	}
	_ = m.recordCallHangup(ctx, lc, stopRef)
	if lc.cancel != nil {
		lc.cancel()
	}
	if lc.conn != nil {
		_ = lc.conn.Close()
	}
}

func (m *Module) recordCallHangup(ctx context.Context, lc *liveCall, stopRef string) error {
	return m.data.Mutate(ctx, lc.tenant, func(sc store.Scope) error {
		if err := m.recordDecision(ctx, sc, decisionRow{
			sessionRef: lc.callID, agentRef: lc.policy.agentRef, reqModel: lc.modelRef, reqProvider: callProviderOpenAI,
			policyRef: lc.policy.policyRef, op: opClose, opStatus: opStatusDispatched, policyVerdict: verdictAllowed,
			actor: model.ActorSystem, actorKind: model.ActorSystem, result: "kill-switch",
		}); err != nil {
			return err
		}
		return m.auditCallEvent(ctx, sc, "voice.call.hangup", lc.acceptedCall, map[string]any{
			"reason": "killswitch", "stop_ref": stopRef, "op_status": opStatusDispatched,
		})
	})
}

func (m *Module) stopCallObservers() {
	m.mu.Lock()
	cancel := m.callCancel
	m.callCancel = nil
	live := make([]*liveCall, 0, len(m.liveCalls))
	for _, lc := range m.liveCalls {
		live = append(live, lc)
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, lc := range live {
		if lc.cancel != nil {
			lc.cancel()
		}
		if lc.conn != nil {
			_ = lc.conn.Close()
		}
	}
	m.callWG.Wait()
}
