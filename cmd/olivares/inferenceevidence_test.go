// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// inferenceevidence_test.go is the REGRESSION half of the governed Chat path's two extractions. Its job is
// not to prove the new Chat path works — that is modelsguardedtext_test.go — but to prove
// that pulling the anchoring mechanism and the inspection invocation out from under the
// Messages proxy changed NOTHING about what Messages writes and publishes.
//
// The assertions are therefore deliberately literal: the exact action strings, the exact
// target kind, the exact metadata keys and values, the payload hash recomputed from the
// same inputs, and the same drop accounting. A test that only asserted "an event was
// appended" would pass on a rename.

// evidenceLedgerRecord reads through the CanonicalWalker capability. Walk deliberately
// returns a nil Meta ("the canonical string is authoritative"), so an assertion over ev.Meta
// would be an assertion over an always-empty map — it would pass whatever was recorded.
type evidenceLedgerRecord struct {
	event model.AuditEvent
	meta  map[string]any
	raw   string
}

func evidenceRecords(t *testing.T, st store.Store, tenant model.TenantID) []evidenceLedgerRecord {
	t.Helper()
	var records []evidenceLedgerRecord
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("this store exposes no canonical audit walk; the metadata assertions would be vacuous")
		}
		return walker.WalkCanonical(context.Background(), 0, func(ev model.AuditEvent, metaCanonical string, _ []byte) error {
			record := evidenceLedgerRecord{event: ev, raw: metaCanonical}
			if metaCanonical != "" {
				if err := json.Unmarshal([]byte(metaCanonical), &record.meta); err != nil {
					return err
				}
			}
			records = append(records, record)
			return nil
		})
	}); err != nil {
		t.Fatalf("walk ledger: %v", err)
	}
	return records
}

func evidenceRecordFor(t *testing.T, st store.Store, tenant model.TenantID, action string) evidenceLedgerRecord {
	t.Helper()
	var found []evidenceLedgerRecord
	for _, record := range evidenceRecords(t, st, tenant) {
		if record.event.Action == action {
			found = append(found, record)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s appears %d times, want exactly 1", action, len(found))
	}
	return found[0]
}

// --- the extracted mechanism itself ---------------------------------------------------------

// TestInferenceEvidenceWriterHonoursTheAnchoringDiscipline pins the four outcomes the shared
// mechanism must produce, against a REAL store and a REAL spool budget. The degrade case is
// the one that matters: the loss accounting must be COMMITTED, not rolled back, and the
// receipt must still refuse.
func TestInferenceEvidenceWriterHonoursTheAnchoringDiscipline(t *testing.T) {
	digest := sha256.Sum256([]byte("an effect"))
	binding := sdk.EvidenceBinding{
		OperationID:  sdk.OperationID("op-1"),
		EffectDigest: sdk.EffectDigest(hex.EncodeToString(digest[:])),
	}
	draft := func() model.AuditDraft {
		return model.AuditDraft{
			Actor: "user:u1", ActorKind: "user", Action: "test.evidence.written",
			TargetKind: model.Kind("test.subject"), TargetID: model.ID("op-1"),
			PayloadHash: digest[:], Meta: map[string]any{"k": "v"},
		}
	}

	t.Run("a_healthy_append_anchors_for_its_own_binding", func(t *testing.T) {
		ipx := inferenceproxy.New()
		st, tenant := provisionTenant(t, ipx, "")
		receipt, err := inferenceEvidenceWriter{store: st}.Append(context.Background(), tenant, binding, draft())
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if !receipt.AnchoredFor(binding) {
			t.Fatalf("receipt = %+v, want anchored", receipt)
		}
		// The ref came from THIS append: it is the committed event's own chain hash.
		record := evidenceRecordFor(t, st, tenant, "test.evidence.written")
		if receipt.EvidenceRef != hex.EncodeToString(record.event.Hash) {
			t.Fatalf("receipt ref %q is not the committed event's hash %x", receipt.EvidenceRef, record.event.Hash)
		}
		// And it authorizes NOTHING else.
		other := sdk.EvidenceBinding{OperationID: "op-2", EffectDigest: binding.EffectDigest}
		if receipt.AnchoredFor(other) {
			t.Fatal("the receipt authorized a different operation")
		}
	})

	t.Run("a_degrade_drop_commits_the_loss_accounting_and_refuses", func(t *testing.T) {
		ipx := inferenceproxy.New()
		st, tenant := provisionTenantThenReopenDegraded(t, ipx, "")
		before := evidencePendingDrops(t, st)
		receipt, err := inferenceEvidenceWriter{store: st}.Append(context.Background(), tenant, binding, draft())
		if err != nil {
			t.Fatalf("a degrade drop returned an error: %v", err)
		}
		if receipt.Fault != sdk.EvidenceFaultSpoolDegraded || receipt.AnchoredFor(binding) {
			t.Fatalf("receipt = %+v, want a refused spool_degraded receipt", receipt)
		}
		if after := evidencePendingDrops(t, st); after <= before {
			t.Fatalf("the drop was rolled back: PendingDrops before=%d after=%d", before, after)
		}
		if !evidenceAppendDropped(receipt, nil) {
			t.Fatal("evidenceAppendDropped did not see a committed drop")
		}
	})

	t.Run("a_block_mode_full_rolls_back_and_returns_an_error", func(t *testing.T) {
		ipx := inferenceproxy.New()
		st, tenant := provisionTenantThenReopenBlocked(t, ipx)
		before := len(evidenceRecords(t, st, tenant))
		receipt, err := inferenceEvidenceWriter{store: st}.Append(context.Background(), tenant, binding, draft())
		if err == nil {
			t.Fatal("a block-mode spool full returned no error")
		}
		if receipt.Fault != sdk.EvidenceFaultWriteError || receipt.AnchoredFor(binding) {
			t.Fatalf("receipt = %+v, want a classified non-anchored receipt", receipt)
		}
		if after := len(evidenceRecords(t, st, tenant)); after != before {
			t.Fatalf("a rolled-back append grew the chain from %d to %d", before, after)
		}
		if evidenceAppendDropped(receipt, err) {
			t.Fatal("a block-mode refusal was reported as a degrade drop")
		}
	})

	t.Run("no_ledger_is_deny_closed", func(t *testing.T) {
		receipt, err := inferenceEvidenceWriter{store: nil}.Append(context.Background(), "t", binding, draft())
		if !errors.Is(err, ErrNoLedger) {
			t.Fatalf("err = %v, want ErrNoLedger", err)
		}
		if receipt.Fault != sdk.EvidenceFaultLedgerUnwired || receipt.AnchoredFor(binding) {
			t.Fatalf("receipt = %+v", receipt)
		}
	})

	t.Run("an_incomplete_binding_can_never_anchor", func(t *testing.T) {
		ipx := inferenceproxy.New()
		st, tenant := provisionTenant(t, ipx, "")
		empty := sdk.EvidenceBinding{}
		receipt, err := inferenceEvidenceWriter{store: st}.Append(context.Background(), tenant, empty, draft())
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if receipt.AnchoredFor(empty) || receipt.Fault != sdk.EvidenceFaultWriteError {
			t.Fatalf("an incomplete binding produced %+v", receipt)
		}
	})
}

// provisionTenantThenReopenBlocked mirrors provisionTenantThenReopenDegraded for the BLOCK
// policy: the append is refused outright and nothing durable is written.
func provisionTenantThenReopenBlocked(t *testing.T, ipx *inferenceproxy.Module) (store.Store, model.TenantID) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "inferenceproxy-anchor-block.db")
	seed, tenant := provisionTenantWithConfig(t, ipx, "", store.Config{Engine: store.EngineSQLite, DSN: dsn})
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	st, err := coreengine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolBlock,
	}, ipx.RegisterSchema)
	if err != nil {
		t.Fatalf("reopen blocked store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ipx.UseData(api.NewModuleData(st))
	return st, tenant
}

func evidencePendingDrops(t *testing.T, st store.Store) int64 {
	t.Helper()
	//nolint:misspell // agent noun of store.AuditSpoolStatuser, not "stature".
	statuser, ok := st.(store.AuditSpoolStatuser)
	if !ok {
		t.Fatal("store does not expose AuditSpoolStatuser")
	}
	//nolint:misspell // same local as above.
	status, _, err := statuser.AuditSpoolStatus(context.Background())
	if err != nil {
		t.Fatalf("spool status: %v", err)
	}
	return status.PendingDrops
}

// --- the Messages legs, unchanged ---------------------------------------------------------------

// messagesAnchorSession is the exact session shape the Messages legs anchor from.
func messagesAnchorSession(tenant model.TenantID) *proxySession {
	input := sha256.Sum256([]byte("inbound-bytes"))
	effective := sha256.Sum256([]byte("frozen-forward-bytes"))
	return &proxySession{
		tenant: tenant, actor: "user:u1", actorKind: "user", modelRef: "claude-opus-4-8",
		requestRef: "req-fixed-ref", inputDigest: input[:], effectiveDigest: effective[:],
	}
}

// TestMessagesIntentAnchorIsUnchangedByTheExtraction states the intent leg's committed row
// literally: the action, the target kind and id, the payload hash recomputed from the SAME
// inputs by the SAME helper, and every metadata key with its value.
func TestMessagesIntentAnchorIsUnchangedByTheExtraction(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "")
	d := &inferenceProxyDecider{surface: "direct", store: st}
	sess := messagesAnchorSession(tenant)
	if err := d.anchorIntent(context.Background(), sess); err != nil {
		t.Fatalf("anchorIntent: %v", err)
	}
	record := evidenceRecordFor(t, st, tenant, "inference.proxy.authorized")
	if record.event.TargetKind != proxyCallKind || record.event.TargetID != model.ID(sess.requestRef) {
		t.Fatalf("target = %s/%s", record.event.TargetKind, record.event.TargetID)
	}
	if record.event.Actor != "user:u1" || record.event.ActorKind != "user" {
		t.Fatalf("actor = %s/%s", record.event.Actor, record.event.ActorKind)
	}
	wantHash := proxyIntentHash(sess.requestRef, "direct", tenant.String(), sess.modelRef, sess.actor, sess.inputDigest, sess.effectiveDigest)
	if hex.EncodeToString(record.event.PayloadHash) != hex.EncodeToString(wantHash) {
		t.Fatalf("payload hash = %x, want %x", record.event.PayloadHash, wantHash)
	}
	want := map[string]any{
		"request_ref": sess.requestRef, "surface": "direct", "model": sess.modelRef,
		"decision":         "allow",
		"input_digest":     hex.EncodeToString(sess.inputDigest),
		"effective_digest": hex.EncodeToString(sess.effectiveDigest),
	}
	assertMetaExactly(t, "inference.proxy.authorized", record, want)
}

// TestMessagesBatchIntentAnchorIsUnchangedByTheExtraction is the same statement for the
// batch submission leg, including its deliberately nil input digest.
func TestMessagesBatchIntentAnchorIsUnchangedByTheExtraction(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "")
	d := &inferenceProxyDecider{surface: "direct", store: st}
	sess := messagesAnchorSession(tenant)
	if err := d.anchorBatchIntent(context.Background(), sess, 3); err != nil {
		t.Fatalf("anchorBatchIntent: %v", err)
	}
	record := evidenceRecordFor(t, st, tenant, "inference.proxy.batch.authorized")
	wantHash := proxyIntentHash(sess.requestRef, "direct", tenant.String(), "batch", sess.actor, nil, sess.effectiveDigest)
	if hex.EncodeToString(record.event.PayloadHash) != hex.EncodeToString(wantHash) {
		t.Fatalf("payload hash = %x, want %x", record.event.PayloadHash, wantHash)
	}
	assertMetaExactly(t, "inference.proxy.batch.authorized", record, map[string]any{
		"request_ref": sess.requestRef, "surface": "direct", "kind": "batch",
		"entries": float64(3), "decision": "allow",
		"effective_digest": hex.EncodeToString(sess.effectiveDigest),
	})
}

// TestMessagesOutcomeAnchorsAreUnchangedByTheExtraction covers both outcome legs, which now
// route through the shared writer while keeping their best-effort posture: they return
// nothing, they log a gap, and they must still commit exactly the row they used to.
func TestMessagesOutcomeAnchorsAreUnchangedByTheExtraction(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "")
	d := &inferenceProxyDecider{surface: "direct", store: st, log: discardLog()}
	sess := messagesAnchorSession(tenant)

	reqSHA := sha256.Sum256([]byte("req"))
	respSHA := sha256.Sum256([]byte("resp"))
	out := claudeapi.ProxyForwardResult{
		ReqBytes: 120, RespBytes: 340, ReqSHA: reqSHA[:], RespSHA: respSHA[:],
		EffectiveSHA: sess.effectiveDigest, Streamed: false, UpstreamStatus: 200,
	}
	d.anchorOutcome(context.Background(), sess, out, "allow")
	record := evidenceRecordFor(t, st, tenant, "inference.proxy.recorded")
	wantHash := proxyOutcomeHash(sess.requestRef, "direct", tenant.String(), sess.modelRef, "allow",
		out.ReqBytes, out.RespBytes, out.ReqSHA, out.RespSHA, sess.inputDigest, sess.effectiveDigest)
	if hex.EncodeToString(record.event.PayloadHash) != hex.EncodeToString(wantHash) {
		t.Fatalf("payload hash = %x, want %x", record.event.PayloadHash, wantHash)
	}
	assertMetaExactly(t, "inference.proxy.recorded", record, map[string]any{
		"request_ref": sess.requestRef, "surface": "direct", "model": sess.modelRef,
		"decision": "allow", "req_bytes": float64(120), "resp_bytes": float64(340),
		"streamed": false, "upstream_status": float64(200),
		"input_digest":     hex.EncodeToString(sess.inputDigest),
		"effective_digest": hex.EncodeToString(sess.effectiveDigest),
	})

	batchOut := claudeapi.ProxyBatchForwardResult{
		ReqBytes: 500, Entries: 3, EffectiveSHA: sess.effectiveDigest, UpstreamStatus: 200,
		Batch: claudeapi.Batch{ID: "batch_123"},
	}
	d.anchorBatchOutcome(context.Background(), sess, batchOut, "allow")
	batchRecord := evidenceRecordFor(t, st, tenant, "inference.proxy.batch.recorded")
	wantBatchHash := proxyOutcomeHash(sess.requestRef, "direct", tenant.String(), "batch", "allow",
		batchOut.ReqBytes, 0, batchOut.ReqSHA, nil, nil, sess.effectiveDigest)
	if hex.EncodeToString(batchRecord.event.PayloadHash) != hex.EncodeToString(wantBatchHash) {
		t.Fatalf("batch payload hash = %x, want %x", batchRecord.event.PayloadHash, wantBatchHash)
	}
	assertMetaExactly(t, "inference.proxy.batch.recorded", batchRecord, map[string]any{
		"request_ref": sess.requestRef, "surface": "direct", "kind": "batch",
		"batch_id": "batch_123", "entries": float64(3), "decision": "allow",
		"req_bytes": float64(500), "upstream_status": float64(200),
		"effective_digest": hex.EncodeToString(sess.effectiveDigest),
	})
}

// TestMessagesOutcomeDropStillCommitsItsLossAccounting: the outcome leg is best-effort, so
// a degrade drop must not become invisible. The counter is the only place that drop is
// recorded, and the extraction must not have moved the commit.
func TestMessagesOutcomeDropStillCommitsItsLossAccounting(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenantThenReopenDegraded(t, ipx, "")
	d := &inferenceProxyDecider{surface: "direct", store: st, log: discardLog()}
	sess := messagesAnchorSession(tenant)
	before := evidencePendingDrops(t, st)
	d.anchorOutcome(context.Background(), sess, claudeapi.ProxyForwardResult{UpstreamStatus: 200}, "allow")
	if after := evidencePendingDrops(t, st); after <= before {
		t.Fatalf("the outcome drop was rolled back: PendingDrops before=%d after=%d", before, after)
	}
}

// assertMetaExactly compares the committed canonical metadata to a COMPLETE expectation: a
// key that appeared, disappeared or changed value fails. "Contains" would let a leak in.
func assertMetaExactly(t *testing.T, action string, record evidenceLedgerRecord, want map[string]any) {
	t.Helper()
	if len(record.meta) != len(want) {
		t.Fatalf("%s metadata has %d keys, want %d: %s", action, len(record.meta), len(want), record.raw)
	}
	for key, value := range want {
		got, present := record.meta[key]
		if !present {
			t.Fatalf("%s metadata lost %q: %s", action, key, record.raw)
		}
		if got != value {
			t.Fatalf("%s metadata[%q] = %v (%T), want %v (%T)", action, key, got, got, value, value)
		}
	}
}

// --- the shared inspection service ------------------------------------------------------------------

// recordingSink captures published observations so the metering and finding assertions are
// about what actually reached the bus.
type recordingSink struct {
	findings []sdkmodel.FindingReport
	costs    []sdkmodel.CostSample
}

func (s *recordingSink) publish(_ context.Context, _ model.TenantID, obs sdkmodel.Observation) {
	switch v := obs.(type) {
	case sdkmodel.FindingReport:
		s.findings = append(s.findings, v)
	case sdkmodel.CostSample:
		s.costs = append(s.costs, v)
	}
}

type fixedInspector struct {
	decision claudeapi.ContentInspectionDecision
	seen     []claudeapi.ContentInspectionInput
}

func (f *fixedInspector) Inspect(_ context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	f.seen = append(f.seen, in)
	return f.decision
}

// TestContentInspectionServicePreservesMessagesFindingsAndMetering states the Messages
// side of the extraction literally: the subject kind, the subject ref fallback, the
// detail-hash preimage, the meter's provider and gateway, and the label values.
func TestContentInspectionServicePreservesMessagesFindingsAndMetering(t *testing.T) {
	sink := &recordingSink{}
	inspector := &fixedInspector{decision: claudeapi.ContentInspectionDecision{
		Forward: true,
		Findings: []claudeapi.ContentInspectionFinding{
			{Severity: "high", Title: "prompt injection", Detector: "prompt_injection", Channel: "tool_result", Detail: "d", OWASPLLM: []string{"LLM01:2025"}},
			{Kind: "custom_kind", Severity: "medium", Title: "exfiltration", Channel: "text", Detail: "e"},
		},
		Meter: claudeapi.ContentInspectionMeter{Inspections: 1, Channels: 2, Detectors: 4, Bytes: 99},
	}}
	clock := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	d := &inferenceProxyDecider{surface: sdkmodel.GatewayDirect, inspector: inspector, bus: nil, clock: clock, log: discardLog()}
	// Bind the recording publisher in place of the bus-backed one; every other input is
	// the decider's own.
	service := contentInspectionService{inspector: d.inspector, publish: sink.publish, approve: nil, clock: d.clock}

	content := claudeapi.CollectedContent{
		Channels: []claudeapi.ContentChannel{{Kind: "text", Role: "user", Text: "hello", Scannable: true}},
		Texts:    []string{"hello"},
	}
	subject := inspectionSubject{Kind: "anthropic.inference", ProviderRef: "anthropic", Surface: sdkmodel.GatewayDirect}
	dec := service.Inspect(context.Background(), subject, "tenant-1", "user:u1", "agent-1", false,
		claudeapi.InspectDirectionRequest, "claude-opus-4-8", content)
	if !dec.Forward {
		t.Fatal("the verdict was altered in transit")
	}

	if len(inspector.seen) != 1 {
		t.Fatalf("the inspector was called %d times", len(inspector.seen))
	}
	in := inspector.seen[0]
	if in.Tenant != "tenant-1" || in.ActorRef != "agent-1" || in.UnbindableAgent ||
		in.Direction != claudeapi.InspectDirectionRequest || in.Model != "claude-opus-4-8" ||
		len(in.Channels) != 1 || in.Unscanned {
		t.Fatalf("the inspector input changed shape: %+v", in)
	}

	if len(sink.findings) != 2 {
		t.Fatalf("published %d findings, want 2", len(sink.findings))
	}
	first := sink.findings[0]
	if first.Kind != "content_firewall" || first.Severity != sdkmodel.SeverityHigh ||
		first.SubjectKind != "anthropic.inference" || first.SubjectRef != "prompt_injection" ||
		first.Title != "prompt injection" || !first.OccurredAt.Equal(clock()) {
		t.Fatalf("finding 0 changed: %+v", first)
	}
	if first.DetailHash != hexSHA("claude-opus-4-8|request|tool_result|d") {
		t.Fatalf("the detail-hash preimage changed: %s", first.DetailHash)
	}
	second := sink.findings[1]
	if second.Kind != "custom_kind" || second.Severity != sdkmodel.SeverityMedium ||
		second.SubjectRef != "claude-opus-4-8" { // no detector ⇒ the model ref is the fallback
		t.Fatalf("finding 1 changed: %+v", second)
	}

	if len(sink.costs) != 1 {
		t.Fatalf("published %d meters, want 1", len(sink.costs))
	}
	meter := sink.costs[0]
	if meter.ProviderRef != "anthropic" || meter.ModelRef != "claude-opus-4-8" ||
		meter.CostType != "content_inspection" || meter.CostMicroUSD != 0 ||
		meter.Gateway != sdkmodel.GatewayDirect || meter.Provenance != sdkmodel.ProvenanceEstimated ||
		meter.SessionRef != "" || !meter.OccurredAt.Equal(clock()) {
		t.Fatalf("the meter changed: %+v", meter)
	}
	if meter.Labels["direction"] != "request" || meter.Labels["channels"] != "2" || meter.Labels["detectors"] != "4" {
		t.Fatalf("meter labels changed: %v", meter.Labels)
	}
}

// TestContentInspectionServiceNoOpsAndZeroMeter pins the two cases that must publish
// nothing: no inspector at all, and an inspection whose meter counted nothing.
func TestContentInspectionServiceNoOpsAndZeroMeter(t *testing.T) {
	content := claudeapi.CollectedContent{
		Channels: []claudeapi.ContentChannel{{Kind: "text", Text: "x"}}, Texts: []string{"x"},
	}
	subject := inspectionSubject{Kind: "anthropic.inference", ProviderRef: "anthropic"}

	t.Run("nil_inspector_is_a_clean_pass", func(t *testing.T) {
		sink := &recordingSink{}
		service := contentInspectionService{publish: sink.publish, clock: time.Now}
		dec := service.Inspect(context.Background(), subject, "t", "a", "r", false,
			claudeapi.InspectDirectionRequest, "m", content)
		if !dec.Forward {
			t.Fatal("a nil inspector denied the request")
		}
		if len(sink.findings) != 0 || len(sink.costs) != 0 {
			t.Fatal("a nil inspector published something")
		}
	})

	t.Run("empty_content_is_a_clean_pass", func(t *testing.T) {
		sink := &recordingSink{}
		inspector := &fixedInspector{decision: claudeapi.ContentInspectionDecision{Forward: false, Status: 403}}
		service := contentInspectionService{inspector: inspector, publish: sink.publish, clock: time.Now}
		dec := service.Inspect(context.Background(), subject, "t", "a", "r", false,
			claudeapi.InspectDirectionRequest, "m", claudeapi.CollectedContent{})
		if !dec.Forward {
			t.Fatal("empty content reached the inspector and was denied")
		}
		if len(inspector.seen) != 0 {
			t.Fatal("empty content was handed to the inspector")
		}
	})

	t.Run("a_zero_meter_publishes_no_cost_sample", func(t *testing.T) {
		sink := &recordingSink{}
		inspector := &fixedInspector{decision: claudeapi.ContentInspectionDecision{Forward: true}}
		service := contentInspectionService{inspector: inspector, publish: sink.publish, clock: time.Now}
		service.Inspect(context.Background(), subject, "t", "a", "r", false,
			claudeapi.InspectDirectionRequest, "m", content)
		if len(sink.costs) != 0 {
			t.Fatalf("a zero-Inspections meter published %d samples", len(sink.costs))
		}
	})
}

// TestContentInspectionApprovalOpensOnlyOnAHeldVerdict: the approval callback fires for a
// request deny and a response block, and for nothing else — the ordering the Messages glue
// had before the extraction.
func TestContentInspectionApprovalOpensOnlyOnAHeldVerdict(t *testing.T) {
	intent := &claudeapi.ContentInspectionApprovalIntent{Action: "inference.content.firewall", Subject: "request|prompt_injection"}
	tests := []struct {
		name      string
		direction string
		decision  claudeapi.ContentInspectionDecision
		opened    bool
	}{
		{"request_deny_opens", claudeapi.InspectDirectionRequest,
			claudeapi.ContentInspectionDecision{Forward: false, Status: 403, ApprovalIntent: intent}, true},
		{"request_allow_does_not", claudeapi.InspectDirectionRequest,
			claudeapi.ContentInspectionDecision{Forward: true, ApprovalIntent: intent}, false},
		{"response_block_opens", claudeapi.InspectDirectionResponse,
			claudeapi.ContentInspectionDecision{Forward: true, Block: true, ApprovalIntent: intent}, true},
		{"response_clean_does_not", claudeapi.InspectDirectionResponse,
			claudeapi.ContentInspectionDecision{Forward: true, ApprovalIntent: intent}, false},
	}
	content := claudeapi.CollectedContent{
		Channels: []claudeapi.ContentChannel{{Kind: "text", Text: "x"}}, Texts: []string{"x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opened := 0
			var seenKind string
			service := contentInspectionService{
				inspector: &fixedInspector{decision: tt.decision},
				publish:   func(context.Context, model.TenantID, sdkmodel.Observation) {},
				approve: func(_ context.Context, _ model.TenantID, _, _ string, got *claudeapi.ContentInspectionApprovalIntent) {
					opened++
					if got != nil {
						seenKind = got.Subject
					}
				},
				clock: time.Now,
			}
			service.Inspect(context.Background(), inspectionSubject{Kind: "anthropic.inference"},
				"t", "user:u1", "r", false, tt.direction, "m", content)
			want := 0
			if tt.opened {
				want = 1
			}
			if opened != want {
				t.Fatalf("the approval callback fired %d times, want %d", opened, want)
			}
			if tt.opened && seenKind != intent.Subject {
				t.Fatalf("the callback received a different intent: %q", seenKind)
			}
		})
	}
}

// TestContentInspectionSubjectIsTheOnlyDifferenceBetweenCallers is the point of the
// extraction stated as a test: with the SAME inspector verdict, the Messages subject and the
// Chat subject differ in exactly the three subject fields and in nothing else.
func TestContentInspectionSubjectIsTheOnlyDifferenceBetweenCallers(t *testing.T) {
	decision := claudeapi.ContentInspectionDecision{
		Forward:  true,
		Findings: []claudeapi.ContentInspectionFinding{{Severity: "high", Title: "t", Detector: "det", Channel: "text", Detail: "d"}},
		Meter:    claudeapi.ContentInspectionMeter{Inspections: 1, Channels: 1, Detectors: 2},
	}
	content := claudeapi.CollectedContent{
		Channels: []claudeapi.ContentChannel{{Kind: "text", Text: "x"}}, Texts: []string{"x"},
	}
	run := func(subject inspectionSubject) (sdkmodel.FindingReport, sdkmodel.CostSample) {
		sink := &recordingSink{}
		service := contentInspectionService{
			inspector: &fixedInspector{decision: decision}, publish: sink.publish,
			clock: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		}
		service.Inspect(context.Background(), subject, "t", "user:u1", "r", false,
			claudeapi.InspectDirectionRequest, "m", content)
		return sink.findings[0], sink.costs[0]
	}
	messagesFinding, messagesMeter := run(inspectionSubject{Kind: "anthropic.inference", ProviderRef: "anthropic", Surface: sdkmodel.GatewayDirect})
	chatFinding, chatMeter := run(inspectionSubject{Kind: string(chatExecutionKind), ProviderRef: "fixture-gateway", Surface: sdkmodel.Gateway("direct")})

	if messagesFinding.SubjectKind == chatFinding.SubjectKind {
		t.Fatal("the two callers published the same subject kind")
	}
	if messagesMeter.ProviderRef == chatMeter.ProviderRef {
		t.Fatal("the two callers published the same provider ref")
	}
	// Everything else is byte-identical, including the detail hash and the OWASP tags. The
	// comparison goes through JSON because a FindingReport carries slices and is not
	// comparable with ==; encoding it is the exact-equality this claim needs.
	messagesFinding.SubjectKind, chatFinding.SubjectKind = "", ""
	left, err := json.Marshal(messagesFinding)
	if err != nil {
		t.Fatalf("marshal finding: %v", err)
	}
	right, err := json.Marshal(chatFinding)
	if err != nil {
		t.Fatalf("marshal finding: %v", err)
	}
	if string(left) != string(right) {
		t.Fatalf("the findings differ beyond the subject kind:\n%s\n%s", left, right)
	}
	messagesMeter.ProviderRef, chatMeter.ProviderRef = "", ""
	if messagesMeter.CostType != chatMeter.CostType || messagesMeter.CostMicroUSD != chatMeter.CostMicroUSD ||
		messagesMeter.Provenance != chatMeter.Provenance || !messagesMeter.OccurredAt.Equal(chatMeter.OccurredAt) ||
		messagesMeter.Labels["direction"] != chatMeter.Labels["direction"] ||
		messagesMeter.Labels["channels"] != chatMeter.Labels["channels"] ||
		messagesMeter.Labels["detectors"] != chatMeter.Labels["detectors"] {
		t.Fatalf("the meters differ beyond the provider ref:\n%+v\n%+v", messagesMeter, chatMeter)
	}
}
