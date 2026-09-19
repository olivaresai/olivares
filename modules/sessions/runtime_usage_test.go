// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// resultFrame is a real-shaped Claude Code result line: the flat `usage` object is
// snake_case, the per-model `modelUsage` map is camelCase, and `total_cost_usd`
// sits beside both. The bytes are the shape measured on the golden path
// (evidence/17b-what-the-child-got.txt), not an invented envelope.
const resultFrame = `{"type":"result","subtype":"success","is_error":false,` +
	`"total_cost_usd":0.0274295,"num_turns":1,"result":"OK","session_id":"sess-1",` +
	`"usage":{"input_tokens":2,"output_tokens":4,"cache_read_input_tokens":52753,"cache_creation_input_tokens":0},` +
	`"modelUsage":{"claude-opus-5[1m]":{"inputTokens":2,"outputTokens":4,` +
	`"cacheReadInputTokens":52753,"cacheCreationInputTokens":0,"costUSD":0.0274295}}}`

// recordingCostSink captures what the module posts to the spend ledger.
type recordingCostSink struct {
	mu      sync.Mutex
	samples []SessionCostSample
	err     error
}

func (s *recordingCostSink) PublishSessionCost(_ context.Context, _ model.TenantID, sample SessionCostSample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, sample)
	return s.err
}

func (s *recordingCostSink) all() []SessionCostSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SessionCostSample(nil), s.samples...)
}

func TestResultFrameUsageReadsBothSpellingsAndTheCost(t *testing.T) {
	t.Parallel()

	r, ok := resultFrameUsage([]byte(resultFrame))
	if !ok {
		t.Fatal("a result frame with usage was read as carrying none")
	}
	if got := r.CostMicroUSD; got != 27430 {
		t.Fatalf("total_cost_usd = %d micro USD, want 27430 (0.0274295 rounded)", got)
	}
	entry, ok := r.Models["claude-opus-5[1m]"]
	if !ok {
		t.Fatalf("the per-model breakdown was lost: %+v", r.Models)
	}
	if entry.InputTokens != 2 || entry.OutputTokens != 4 || entry.CacheReadTokens != 52753 {
		t.Fatalf("camelCase model usage misread: %+v", entry)
	}
	if entry.CostMicroUSD != 27430 {
		t.Fatalf("per-model costUSD = %d", entry.CostMicroUSD)
	}
	if got := entry.totalInputTokens(); got != 52755 {
		t.Fatalf("total input volume = %d, want uncached+cache-read = 52755", got)
	}
	if r.Fallback.InputTokens != 2 || r.Fallback.CacheReadTokens != 52753 {
		t.Fatalf("snake_case flat usage misread: %+v", r.Fallback)
	}

	// A frame that is not a result, and a result with no metering at all, are both
	// "nothing to credit" — and the second is UNKNOWN, not zero.
	if _, ok := resultFrameUsage([]byte(`{"type":"assistant","message":{"content":[]}}`)); ok {
		t.Fatal("an assistant frame was read as metering")
	}
	if _, ok := resultFrameUsage([]byte(`{"type":"result","subtype":"success","result":"OK"}`)); ok {
		t.Fatal("a result frame with no usage was read as reporting zero")
	}
}

// TestCumulativeFramesAreCreditedAsDeltas is the rule the CLI's own
// documentation forces: each result frame RESTATES the session total, and a
// resume or a mid-session clear starts the count again.
func TestCumulativeFramesAreCreditedAsDeltas(t *testing.T) {
	t.Parallel()

	var acct usageAccount
	frame := func(in, out, micro int64) resultUsage {
		return resultUsage{Reported: true, Models: map[string]modelUsage{
			"m": {InputTokens: in, OutputTokens: out, CostMicroUSD: micro},
		}}
	}
	first := acct.credit(frame(100, 10, 1000))
	if len(first) != 1 || first[0].Usage.OutputTokens != 10 || first[0].Usage.CostMicroUSD != 1000 {
		t.Fatalf("first credit = %+v", first)
	}
	// A SECOND cumulative frame must credit the increase, not the total again.
	second := acct.credit(frame(250, 30, 2500))
	if len(second) != 1 || second[0].Usage.InputTokens != 150 ||
		second[0].Usage.OutputTokens != 20 || second[0].Usage.CostMicroUSD != 1500 {
		t.Fatalf("second credit = %+v, want the delta (150/20/1500)", second)
	}
	// The same frame again adds nothing: no row for zero.
	if again := acct.credit(frame(250, 30, 2500)); len(again) != 0 {
		t.Fatalf("a repeated frame produced %d rows", len(again))
	}
	// A counter that went DOWN is the reset the CLI documents: credit it whole.
	reset := acct.credit(frame(30, 3, 300))
	if len(reset) != 1 || reset[0].Usage.InputTokens != 30 || reset[0].Usage.CostMicroUSD != 300 {
		t.Fatalf("after a reset the credit = %+v, want the whole new value", reset)
	}
}

// TestFlatUsageIsCreditedWhenNoModelBreakdownIsReported keeps a CLI that reports
// only `usage` + `total_cost_usd` priced, with the model recorded as unreported
// rather than invented.
func TestFlatUsageIsCreditedWhenNoModelBreakdownIsReported(t *testing.T) {
	t.Parallel()

	var acct usageAccount
	deltas := acct.credit(resultUsage{
		Reported:     true,
		CostMicroUSD: 500,
		Fallback:     modelUsage{InputTokens: 7, OutputTokens: 2},
	})
	if len(deltas) != 1 {
		t.Fatalf("deltas = %+v", deltas)
	}
	if deltas[0].ModelRef != "" {
		t.Fatalf("a model was invented: %q", deltas[0].ModelRef)
	}
	if deltas[0].Usage.CostMicroUSD != 500 || deltas[0].Usage.InputTokens != 7 {
		t.Fatalf("flat usage lost: %+v", deltas[0].Usage)
	}
}

// TestGovernedTurnRecordsItsCostAndPostsASpendRow is defects 4 and 5 in one
// measurement, on the same path the walk took: a bridged result frame must leave
// the session's counters readable AND a row on the tenant's spend ledger.
func TestGovernedTurnRecordsItsCostAndPostsASpendRow(t *testing.T) {
	t.Parallel()

	sink := &recordingCostSink{}
	fr := &fakeRunner{initSID: "sess-cost"}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionCostSink(sink))
	ctx := context.Background()

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	// The session record starts UNKNOWN, not zero: no turn has been taken.
	if dto.CostMicroUSD != nil || dto.InputTokens != nil {
		t.Fatalf("a session with no turn reported counters: %+v", dto)
	}
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-cost"
	})

	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(resultFrame)}

	waitFor(t, "the turn's cost on the session record", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.CostMicroUSD != nil && *d.CostMicroUSD > 0
	})
	got, err := m.getRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatalf("getRun: %v", err)
	}
	if *got.CostMicroUSD != 27430 {
		t.Fatalf("cost_micro_usd = %d, want 27430", *got.CostMicroUSD)
	}
	if got.InputTokens == nil || *got.InputTokens != 52755 {
		t.Fatalf("input_tokens = %v, want the total input volume 52755", got.InputTokens)
	}
	if got.OutputTokens == nil || *got.OutputTokens != 4 {
		t.Fatalf("output_tokens = %v", got.OutputTokens)
	}
	if got.UsageModelRef != "claude-opus-5[1m]" {
		t.Fatalf("usage_model_ref = %q, want the model the provider reported", got.UsageModelRef)
	}

	waitFor(t, "a row on the tenant's spend ledger", func() bool { return len(sink.all()) > 0 })
	samples := sink.all()
	if len(samples) != 1 {
		t.Fatalf("%d spend rows for one turn: %+v", len(samples), samples)
	}
	s := samples[0]
	if s.CostMicroUSD != 27430 || s.InputTokens != 52755 || s.OutputTokens != 4 {
		t.Fatalf("spend row numbers = %+v", s)
	}
	if s.ModelRef != "claude-opus-5[1m]" || s.ProviderRef != providerDriverClaude {
		t.Fatalf("spend row attribution = %+v", s)
	}
	if s.RunRef != dto.RunRef {
		t.Fatalf("spend row names run %q, want %q", s.RunRef, dto.RunRef)
	}
	if !s.CostFromProviderClient {
		t.Fatal("the row does not record that the price came from the provider's own client")
	}
	if s.OccurredAt.IsZero() {
		t.Fatal("the spend row carries no time")
	}

	// A SECOND identical (cumulative) frame must not double the bill.
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(resultFrame)}
	time.Sleep(150 * time.Millisecond)
	if rows := sink.all(); len(rows) != 1 {
		t.Fatalf("a repeated cumulative frame posted %d rows", len(rows))
	}
	again, _ := m.getRun(ctx, tenant, dto.RunRef)
	if *again.CostMicroUSD != 27430 {
		t.Fatalf("a repeated cumulative frame doubled the cost to %d", *again.CostMicroUSD)
	}
}

// TestADriverThatReportsNoUsageRecordsUnknown is the other half of the contract:
// unknown is recorded as unknown, and no ledger row is invented for it.
func TestADriverThatReportsNoUsageRecordsUnknown(t *testing.T) {
	t.Parallel()

	sink := &recordingCostSink{}
	fr := &fakeRunner{initSID: "sess-silent"}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionCostSink(sink))
	ctx := context.Background()

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-silent"
	})
	fr.lastProc().out <- OutputFrame{Stream: streamStdout,
		Data: []byte(`{"type":"result","subtype":"success","result":"OK","session_id":"sess-silent"}`)}
	time.Sleep(200 * time.Millisecond)

	got, err := m.getRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatalf("getRun: %v", err)
	}
	if got.CostMicroUSD != nil || got.InputTokens != nil || got.OutputTokens != nil {
		t.Fatalf("a driver that reported no usage recorded numbers: %+v", got)
	}
	if rows := sink.all(); len(rows) != 0 {
		t.Fatalf("a turn with no reported usage posted %d spend rows", len(rows))
	}
}

// TestTokensWithoutMoneyLeaveTheCostUnknown closes the gap the two tests above
// leave between them: one driver reports money, the other reports nothing at
// all, and NEITHER covers the shape a real CLI produces when it meters tokens
// and is not pricing them.
//
// Measured 2026-09-18 on the frame below: the record came back
// `cost_micro_usd = 0 (PRESENT)`. Zero is a CLAIM — it says the turn was free —
// and this file's own header says the plane does not make it. The column must
// stay NULL until a frame reports money, which is exactly the distinction the
// pointer fields of the DTO exist to carry.
func TestTokensWithoutMoneyLeaveTheCostUnknown(t *testing.T) {
	t.Parallel()

	sink := &recordingCostSink{}
	fr := &fakeRunner{initSID: "sess-tokens-only"}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionCostSink(sink))
	ctx := context.Background()

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-tokens-only"
	})

	// The frame a provider sends when it reports no price: tokens, no
	// total_cost_usd, no costUSD.
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(
		`{"type":"result","subtype":"success","session_id":"sess-tokens-only",` +
			`"usage":{"input_tokens":7855,"output_tokens":13}}`)}

	waitFor(t, "the turn's tokens on the session record", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.InputTokens != nil
	})
	got, err := m.getRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatalf("getRun: %v", err)
	}
	// The tokens ARE recorded: what the driver reported is not in question.
	if got.InputTokens == nil || *got.InputTokens != 7855 {
		t.Fatalf("input_tokens = %v, want 7855", got.InputTokens)
	}
	if got.OutputTokens == nil || *got.OutputTokens != 13 {
		t.Fatalf("output_tokens = %v, want 13", got.OutputTokens)
	}
	// And the cost is UNKNOWN, not zero.
	if got.CostMicroUSD != nil {
		t.Fatalf("cost_micro_usd = %d (PRESENT); a turn whose price was never reported "+
			"must read as unknown, and 0 claims it was free", *got.CostMicroUSD)
	}

	// A LATER frame that does report money still lands: staying NULL is not the
	// same as refusing to ever record a price.
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(
		`{"type":"result","subtype":"success","session_id":"sess-tokens-only",` +
			`"total_cost_usd":0.5,"usage":{"input_tokens":7855,"output_tokens":13}}`)}
	waitFor(t, "the price of a later priced turn", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.CostMicroUSD != nil
	})
	priced, _ := m.getRun(ctx, tenant, dto.RunRef)
	if *priced.CostMicroUSD != 500_000 {
		t.Fatalf("cost_micro_usd = %d, want 500000", *priced.CostMicroUSD)
	}
}
