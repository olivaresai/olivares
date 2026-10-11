// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// Codex reports its usage as the THREAD's running total (thread/tokenUsage/updated),
// with input counting the cached part (measured on codex-cli 0.160.1: total_tokens =
// input + output). A turn's usage reaches the run's counters and the spend ledger
// as the increase, and a repeated total credits nothing.
func TestCodexTurnUsageIsCreditedToTheRun(t *testing.T) {
	sink := &recordingCostSink{}
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome, WithSessionCostSink(sink))
	setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-usage", Account: "apikey", CompleteTurn: true,
		TurnUsage: []codexFixtureTokens{
			{InputTokens: 22175, CachedInputTokens: 7168, OutputTokens: 142},
			{InputTokens: 22175, CachedInputTokens: 7168, OutputTokens: 142},
			{InputTokens: 53268, CachedInputTokens: 29184, OutputTokens: 414},
		},
	})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the turn's usage on the run", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.OutputTokens != nil && *d.OutputTokens == 414
	})
	got, _ := m.getRun(ctx, tenant, dto.RunRef)
	if got.InputTokens == nil || *got.InputTokens != 53268 {
		t.Fatalf("input_tokens = %v, want Codex's total input 53268", got.InputTokens)
	}
	if got.UsageModelRef != "gpt-5.6-sol" {
		t.Fatalf("usage_model_ref = %q, want the model Codex named for the thread", got.UsageModelRef)
	}
	// Codex reports tokens, not money: the cost stays unknown here.
	if got.CostMicroUSD != nil {
		t.Fatalf("cost_micro_usd = %d; Codex reported no money", *got.CostMicroUSD)
	}
	waitFor(t, "two spend rows", func() bool { return len(sink.all()) == 2 })
	rows := sink.all()
	if rows[0].InputTokens != 22175 || rows[0].CacheReadTokens != 7168 || rows[0].OutputTokens != 142 ||
		rows[1].InputTokens != 53268-22175 || rows[1].OutputTokens != 414-142 {
		t.Fatalf("spend rows = %+v", rows)
	}
	if rows[0].ProviderRef != providerDriverCodex || rows[0].ModelRef != "gpt-5.6-sol" || rows[0].CostFromProviderClient {
		t.Fatalf("spend row attribution = %+v", rows[0])
	}
}

// A resumed Codex thread reports its whole earlier total at once, before this launch
// takes a turn (measured: 7,091,447 tokens on a thread/resume with no turn). That
// total was already credited, or was never this run's: it is the baseline, and only
// what this launch's own turns add is credited, whether the report arrives before
// or after the handshake has bound the thread.
func TestCodexResumeDoesNotCreditTheThreadsEarlierTotal(t *testing.T) {
	t.Run("before the handshake binds", func(t *testing.T) { testCodexResumeBaseline(t, false) })
	t.Run("after the handshake binds", func(t *testing.T) { testCodexResumeBaseline(t, true) })
}

func testCodexResumeBaseline(t *testing.T, late bool) {
	sink := &recordingCostSink{}
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome, WithSessionCostSink(sink))
	setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-resumed-usage", Account: "apikey", CompleteTurn: true,
		TurnUsage: []codexFixtureTokens{{InputTokens: 1000, OutputTokens: 10}},
	})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "first"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the first launch's usage", func() bool { return len(sink.all()) == 1 })
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stopRun: %v", err)
	}

	replay := ""
	if late {
		replay = filepath.Join(t.TempDir(), "replay")
	}
	setCodexFixture(t, prof, codexFixture{
		Account: "apikey", CompleteTurn: true, RecordPath: filepath.Join(t.TempDir(), "resume.json"),
		ResumeUsage: &codexFixtureTokens{InputTokens: 1000, OutputTokens: 10}, ResumeUsageAfterPath: replay,
		TurnUsage: []codexFixtureTokens{{InputTokens: 1500, OutputTokens: 25}},
	})
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser, ""); err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	if late {
		// resumeRun returned: the thread is bound. Only now does the replay arrive.
		if err := os.WriteFile(replay, []byte("go"), 0o600); err != nil {
			t.Fatalf("release the replay: %v", err)
		}
	}
	// The replayed total has reached this launch's usage account (as its baseline,
	// or, wrongly, as credit): only then is "nothing credited" a finding.
	waitFor(t, "the replayed total on the resumed run", func() bool {
		m.rt.mu.Lock()
		lr := m.rt.live[liveKey(tenant, dto.RunRef)]
		m.rt.mu.Unlock()
		if lr == nil {
			return false
		}
		lr.mu.Lock()
		defer lr.mu.Unlock()
		return lr.usage.credited["gpt-5.6-sol"].InputTokens == 1000
	})
	if rows := sink.all(); len(rows) != 1 {
		t.Fatalf("a resume with no turn posted %d spend rows: %+v", len(rows), rows)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "second"); err != nil {
		t.Fatalf("input after resume: %v", err)
	}
	waitFor(t, "the resumed turn's usage", func() bool { return len(sink.all()) == 2 })
	if row := sink.all()[1]; row.InputTokens != 500 || row.OutputTokens != 15 {
		t.Fatalf("the resumed turn credited %+v, want only its own 500 in / 15 out", row)
	}
	got, _ := m.getRun(ctx, tenant, dto.RunRef)
	if got.InputTokens == nil || *got.InputTokens != 1500 || got.OutputTokens == nil || *got.OutputTokens != 25 {
		t.Fatalf("run counters = %v in / %v out, want 1500 / 25", got.InputTokens, got.OutputTokens)
	}
}

// recordingPricer stands in for the release-embedded list prices: one price per
// token, and every call recorded so a test can see what was priced.
type recordingPricer struct {
	mu    sync.Mutex
	calls []string
}

func (p *recordingPricer) price(provider, modelRef string, t TurnTokens) (int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, provider+"/"+modelRef)
	if provider != "openai" || modelRef != "gpt-5.6-sol" {
		return 0, false
	}
	return 2*t.UncachedInput + t.CacheRead + 3*t.CacheWrite + 10*t.Output, true
}

func (p *recordingPricer) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

// Codex reports tokens and no money; the turn's cost is its list price, under the
// provider Codex named for the thread, and the ledger is told it was priced here.
func TestCodexTurnIsPricedAtListPrice(t *testing.T) {
	sink, pricer := &recordingCostSink{}, &recordingPricer{}
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome, WithSessionCostSink(sink), WithListPricer(pricer.price))
	setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-priced", Account: "apikey", CompleteTurn: true,
		TurnUsage: []codexFixtureTokens{{InputTokens: 22175, CachedInputTokens: 7168, OutputTokens: 142}},
	})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	want := int64(2*(22175-7168) + 7168 + 10*142)
	waitFor(t, "the priced turn on the run", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.CostMicroUSD != nil
	})
	if got, _ := m.getRun(ctx, tenant, dto.RunRef); *got.CostMicroUSD != want {
		t.Fatalf("cost_micro_usd = %d, want the list price %d", *got.CostMicroUSD, want)
	}
	waitFor(t, "the spend row", func() bool { return len(sink.all()) == 1 })
	if row := sink.all()[0]; row.CostMicroUSD != want || row.CostFromProviderClient {
		t.Fatalf("spend row = %+v, want %d priced here, not by the tool", row, want)
	}
	if calls := pricer.all(); len(calls) != 1 || calls[0] != "openai/gpt-5.6-sol" {
		t.Fatalf("priced %v, want once under Codex's own provider and model", calls)
	}
}

// A turn whose tool reported money keeps the tool's figure: the list price is
// only for a turn that came with none.
func TestATurnTheToolPricedIsNotRepriced(t *testing.T) {
	sink, pricer := &recordingCostSink{}, &recordingPricer{}
	fr := &fakeRunner{initSID: "sess-priced-by-tool"}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionCostSink(sink), WithListPricer(pricer.price))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-priced-by-tool"
	})
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(resultFrame)}
	waitFor(t, "the spend row", func() bool { return len(sink.all()) == 1 })
	if row := sink.all()[0]; row.CostMicroUSD != 27430 || !row.CostFromProviderClient {
		t.Fatalf("spend row = %+v, want the tool's own 27430", row)
	}
	if calls := pricer.all(); len(calls) != 0 {
		t.Fatalf("a turn the tool priced was priced again: %v", calls)
	}
}

// priceUnpriced prices only what came with no money and names a model: a delta
// its tool priced keeps the tool's figure even when the provider is known, and a
// model with no list price stays unknown.
func TestPriceUnpricedKeepsTheToolsFigure(t *testing.T) {
	pricer := &recordingPricer{}
	m := &Module{Dependencies: &Dependencies{}, rt: &runtimeState{Dependencies: &Dependencies{ListPricer: pricer.price}}}
	deltas := []creditedDelta{
		{ModelRef: "gpt-5.6-sol", Usage: modelUsage{InputTokens: 10, OutputTokens: 1, CostMicroUSD: 7}},
		{ModelRef: "gpt-5.6-sol", Usage: modelUsage{InputTokens: 10, OutputTokens: 1}},
		{ModelRef: "unlisted", Usage: modelUsage{InputTokens: 10, OutputTokens: 1}},
		{ModelRef: "", Usage: modelUsage{InputTokens: 10, OutputTokens: 1}},
	}
	m.priceUnpriced("openai", deltas)
	if deltas[0].PricedMicroUSD != 0 || deltas[1].PricedMicroUSD != 2*10+10*1 || deltas[2].PricedMicroUSD != 0 || deltas[3].PricedMicroUSD != 0 {
		t.Fatalf("priced = %d %d %d %d", deltas[0].PricedMicroUSD, deltas[1].PricedMicroUSD, deltas[2].PricedMicroUSD, deltas[3].PricedMicroUSD)
	}
	if calls := pricer.all(); len(calls) != 2 {
		t.Fatalf("pricer calls = %v, want only the two unpriced deltas with a model", calls)
	}
}

// Codex may report a turn's usage before the turn/start answer is read, and it
// reports other threads' totals (a subagent's) on the same connection. A turn's
// usage is credited in either order, with its cache split kept (the cache-write
// part is not uncached input), and another thread's total never reaches the run.
func TestCodexUsageIsCreditedInEitherOrderAndOnlyForThisThread(t *testing.T) {
	sink, pricer := &recordingCostSink{}, &recordingPricer{}
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome, WithSessionCostSink(sink), WithListPricer(pricer.price))
	setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-order", Account: "apikey", CompleteTurn: true, UsageBeforeTurnReply: true,
		TurnUsage:    []codexFixtureTokens{{InputTokens: 1000, CachedInputTokens: 300, CacheWriteInputTokens: 200, OutputTokens: 50}},
		ForeignUsage: &codexFixtureTokens{InputTokens: 9_000_000, OutputTokens: 9_000},
	})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the turn's spend row", func() bool { return len(sink.all()) >= 1 })
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "again"); err != nil {
		t.Fatalf("second input: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // the second turn repeats the total and adds a foreign one
	rows := sink.all()
	if len(rows) != 1 {
		t.Fatalf("spend rows = %+v, want exactly the one turn of this thread", rows)
	}
	if r := rows[0]; r.InputTokens != 1000 || r.CacheReadTokens != 300 || r.CacheCreationTokens != 200 || r.OutputTokens != 50 {
		t.Fatalf("spend row = %+v, want Codex's total with its cache split", r)
	}
	// uncached 500 at 2, cache read 300 at 1, cache write 200 at 3, output 50 at 10.
	if want := int64(2*500 + 300 + 3*200 + 10*50); rows[0].CostMicroUSD != want {
		t.Fatalf("cost = %d, want %d priced on the split", rows[0].CostMicroUSD, want)
	}
	got, _ := m.getRun(ctx, tenant, dto.RunRef)
	if got.InputTokens == nil || *got.InputTokens != 1000 || got.OutputTokens == nil || *got.OutputTokens != 50 {
		t.Fatalf("run counters = %v / %v, want 1000 / 50", got.InputTokens, got.OutputTokens)
	}
}

// The model a Codex thread answer names reaches the run row, the spend ledger and
// the operator's terminal, so it is kept only when it is a model identifier. A
// name with terminal escapes still has its usage credited, as "model not reported".
func TestCodexModelNameIsKeptOnlyWhenItIsAnIdentifier(t *testing.T) {
	sink := &recordingCostSink{}
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome, WithSessionCostSink(sink))
	setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-odd-model", Account: "apikey", CompleteTurn: true,
		Model:     "gpt\u001b]0;owned\u0007\u001b[2J",
		TurnUsage: []codexFixtureTokens{{InputTokens: 100, OutputTokens: 5}},
	})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the turn's spend row", func() bool { return len(sink.all()) == 1 })
	if row := sink.all()[0]; row.ModelRef != "" || row.InputTokens != 100 {
		t.Fatalf("spend row = %+v, want the tokens with no model", row)
	}
	if got, _ := m.getRun(ctx, tenant, dto.RunRef); got.UsageModelRef != "" || got.InputTokens == nil || *got.InputTokens != 100 {
		t.Fatalf("run = model %q, input %v", got.UsageModelRef, got.InputTokens)
	}
}

func TestModelRefValueKeepsOnlyAModelIdentifier(t *testing.T) {
	for in, want := range map[string]string{
		"gpt-6.1-sol":            "gpt-6.1-sol",
		"claude-opus-5[1m]":      "claude-opus-5[1m]",
		"qwen2.5-coder:7b":       "qwen2.5-coder:7b",
		"openai/gpt-4o":          "openai/gpt-4o",
		" gpt-6-astra ":          "gpt-6-astra",
		"":                       "",
		"gpt\x1b[2J":             "",
		"two words":              "",
		strings.Repeat("a", 129): "",
	} {
		if got := modelRefValue(in); got != want {
			t.Errorf("modelRefValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// A usage report the driver cannot read is said once per session, not dropped in
// silence: a Codex that changed the report's shape would otherwise leave every
// session reading "the tool reported nothing".
func TestCodexUnreadableUsageReportIsSaidOnce(t *testing.T) {
	var mu sync.Mutex
	var warned []string
	credited := 0
	peer := newCodexPeer(t, func(c *DriverSessionConfig) {
		c.Warn = func(msg string, _ ...any) { mu.Lock(); warned = append(warned, msg); mu.Unlock() }
		c.OnUsage = func(resultUsage, bool) { mu.Lock(); credited++; mu.Unlock() }
	})
	s := peer.session.(*codexSession)
	s.mu.Lock()
	s.threadID = "thread-shape"
	s.mu.Unlock()
	bad := `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-shape","turnId":"t1",` +
		`"tokenUsage":{"total":{"inputTokens":"lots","outputTokens":1}}}}`
	for range 2 {
		s.Deliver(OutputFrame{Stream: streamStdout, Data: []byte(bad)})
	}
	waitFor(t, "the warning", func() bool { mu.Lock(); defer mu.Unlock(); return len(warned) > 0 })
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(warned) != 1 || credited != 0 {
		t.Fatalf("warnings = %q, credited = %d; want one warning and nothing credited", warned, credited)
	}
}
