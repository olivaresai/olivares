// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// The frames in testdata are what the installed tools sent when their provider
// answered a turn with a limit, recorded from the real binaries against a loopback
// stub (claude 2.1.299, codex-cli 0.162.1). The Claude Code pair is the subscription
// login (a rate_limit_event, then a result typed usage_limit_reached); the API-key
// frames are a plain HTTP 429. Codex sends its windows, then an error typed
// usageLimitExceeded, then the failed turn.
const (
	claudeUsageLimitFrames = "testdata/claude-2.1.299-usage-limit.jsonl"
	claudeRateLimitFrames  = "testdata/claude-2.1.299-rate-limit.jsonl"
	codexUsageLimitFrames  = "testdata/codex-0.162.1-usage-limit.jsonl"

	// The reset times inside the recordings, epoch seconds.
	claudeUsageLimitResetsAt = "2026-10-14T15:45:43Z" // rate_limit_info.resetsAt 1791992743
	codexUsageLimitResetsAt  = "2026-10-14T15:46:28Z" // primary window resetsAt 1791992788
)

func recordedFrames(t *testing.T, path string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the recorded frames: %v", err)
	}
	return bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
}

// The tool's own result for a turn the account's plan could not serve is one
// structured fact on the run: the kind, the tool's reset time and the account. A
// result that replays the same limit records it once, and the next successful turn
// clears it.
func TestClaudeUsageLimitResultIsAFactOnTheRun(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-limit"}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-limit"
	})
	feed := func(frames ...[]byte) {
		for _, f := range frames {
			fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: f}
		}
	}

	frames := recordedFrames(t, claudeUsageLimitFrames)
	feed(frames...)
	waitFor(t, "the limit on the run", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.Limit != nil
	})
	got, _ := m.getRun(ctx, tenant, dto.RunRef)
	if dto.ProviderProfileRef == "" || got.Limit.Kind != "usage" || got.Limit.ResetsAt != claudeUsageLimitResetsAt || got.Limit.AccountRef != dto.ProviderProfileRef {
		t.Fatalf("limit = %+v, want usage until %s on account %q", got.Limit, claudeUsageLimitResetsAt, dto.ProviderProfileRef)
	}

	// The same result again is the same fact, not a second event, and it keeps the
	// reset time the first one carried.
	mode := 0
	sentinel := func() {
		mode++
		feed([]byte(fmt.Sprintf(`{"type":"system","subtype":"status","permissionMode":%q}`, []string{"", "plan", "acceptEdits", "default", "bypassPermissions"}[mode])))
		want := toolModeValue([]string{"", "plan", "acceptEdits", "default", "bypassPermissions"}[mode])
		waitFor(t, "the frames were read", func() bool {
			d, _ := m.getRun(ctx, tenant, dto.RunRef)
			return d.ToolMode == want
		})
	}
	feed(frames[len(frames)-1])
	sentinel()
	if n := countNamedRunEvents(t, st, tenant, dto.RunRef, "limit_reached"); n != 1 {
		t.Fatalf("limit_reached events = %d, want 1", n)
	}
	if again, _ := m.getRun(ctx, tenant, dto.RunRef); again.Limit == nil || again.Limit.ResetsAt != claudeUsageLimitResetsAt {
		t.Fatalf("a restated result changed the fact to %+v", again.Limit)
	}

	// A turn that succeeds clears it.
	feed([]byte(resultFrame))
	waitFor(t, "the limit cleared by a successful turn", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.Limit == nil
	})

	// And it can happen again: a second limit is a second fact and a second event.
	feed(frames...)
	waitFor(t, "the second limit", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.Limit != nil
	})
	if n := countNamedRunEvents(t, st, tenant, dto.RunRef, "limit_reached"); n != 2 {
		t.Fatalf("limit_reached events after a second limit = %d, want 2", n)
	}
	feed([]byte(resultFrame))
	waitFor(t, "the second limit cleared", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.Limit == nil
	})

	// A window event belongs to the result that follows it: one that a failed turn of
	// another cause came between does not lend its reset time to a later limit.
	feed(frames[0], []byte(`{"type":"result","subtype":"success","is_error":true,"api_error_status":500,"result":"API Error: 500"}`),
		[]byte(`{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"api_error":"usage_limit_reached","result":"x"}`))
	waitFor(t, "the third limit", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.Limit != nil
	})
	if third, _ := m.getRun(ctx, tenant, dto.RunRef); third.Limit.ResetsAt != "" {
		t.Fatalf("a stale window event gave the limit a reset time: %+v", third.Limit)
	}
}

// A 429 the tool does not type as a usage limit is a rate limit: the kind says so
// and no reset time is invented for it. A result that failed for another reason
// carries no limit at all.
func TestClaudeOnlyALimitSetsTheLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames [][]byte
		want   string // "" = no limit
	}{
		{"http 429 without a typed usage limit", recordedFrames(t, claudeRateLimitFrames), "rate"},
		{"usage limit result without a window event",
			[][]byte{[]byte(`{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"api_error":"usage_limit_reached","result":"You've hit your session limit"}`)}, "usage"},
		{"server error", [][]byte{[]byte(`{"type":"result","subtype":"success","is_error":true,"api_error_status":500,"result":"API Error: 500"}`)}, ""},
		{"error without a status", [][]byte{[]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`)}, ""},
		{"a window event alone is not a failed turn",
			[][]byte{[]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1791992743,"rateLimitType":"five_hour"}}`)}, ""},
		{"a warning is not a limit", [][]byte{
			[]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1791992743,"rateLimitType":"seven_day","utilization":0.84}}`),
			[]byte(resultFrame)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{initSID: "sess-limit"}
			m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
			ctx := context.Background()
			dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative,
				Actor: "user:u1", ActorKind: "user",
			})
			if err != nil {
				t.Fatalf("createRun: %v", err)
			}
			waitFor(t, "session id capture", func() bool {
				d, _ := m.getRun(ctx, tenant, dto.RunRef)
				return d.ClaudeSessionID == "sess-limit"
			})
			for _, f := range tc.frames {
				fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: f}
			}
			// A status frame after them is the sentinel that every frame above was read.
			fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"system","subtype":"status","permissionMode":"plan"}`)}
			waitFor(t, "the frames were read", func() bool {
				d, _ := m.getRun(ctx, tenant, dto.RunRef)
				return d.ToolMode != ""
			})
			if tc.want == "" {
				if got, _ := m.getRun(ctx, tenant, dto.RunRef); got.Limit != nil {
					t.Fatalf("limit = %+v, want none", got.Limit)
				}
				if n := countNamedRunEvents(t, st, tenant, dto.RunRef, "limit_reached"); n != 0 {
					t.Fatalf("limit_reached events = %d, want 0", n)
				}
				return
			}
			waitFor(t, "the limit on the run", func() bool {
				d, _ := m.getRun(ctx, tenant, dto.RunRef)
				return d.Limit != nil
			})
			got, _ := m.getRun(ctx, tenant, dto.RunRef)
			if got.Limit.Kind != tc.want || got.Limit.ResetsAt != "" {
				t.Fatalf("limit = %+v, want kind %s and no reset time", got.Limit, tc.want)
			}
		})
	}
}

// Codex sends the account's windows, then an error typed usageLimitExceeded, then the
// failed turn. The run carries the usage limit with the exhausted window's reset
// time, and the next completed turn clears it.
func TestCodexUsageLimitIsAFactOnTheRun(t *testing.T) {
	abs, err := filepath.Abs(codexUsageLimitFrames)
	if err != nil {
		t.Fatal(err)
	}
	m, st, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-limit", Account: "apikey", CompleteTurn: true, FirstTurnReplay: abs,
	})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the limit on the run", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.Limit != nil
	})
	got, _ := m.getRun(ctx, tenant, dto.RunRef)
	if got.Limit.Kind != "usage" || got.Limit.ResetsAt != codexUsageLimitResetsAt || got.Limit.AccountRef != prof.Ref {
		t.Fatalf("limit = %+v, want usage until %s on account %q", got.Limit, codexUsageLimitResetsAt, prof.Ref)
	}
	if n := countNamedRunEvents(t, st, tenant, dto.RunRef, "limit_reached"); n != 1 {
		t.Fatalf("limit_reached events = %d, want 1", n)
	}

	// The second turn completes: the limit is gone.
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "again"); err != nil {
		t.Fatalf("second input: %v", err)
	}
	waitFor(t, "the limit cleared by a completed turn", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.Limit == nil
	})
}

func TestCodexOnlyALimitErrorSetsTheLimit(t *testing.T) {
	turn := func(status, info string) string {
		return `{"method":"turn/completed","params":{"threadId":"01a13a73-2fe6-7593-a275-596356a9eacf","turn":{"id":"01a13a73-30ab-7e02-8f7a-3a0f8f930986","items":[],"status":"` +
			status + `","error":{"message":"boom","codexErrorInfo":` + info + `}}}}`
	}
	for _, tc := range []struct {
		name string
		line string
		want string // "" = no limit
	}{
		{"rate limit exceeded", turn("failed", `"rateLimitExceeded"`), "rate"},
		{"usage limit exceeded without windows", turn("failed", `"usageLimitExceeded"`), "usage"},
		{"another error", turn("failed", `"unauthorized"`), ""},
		{"an error without a type", turn("failed", `null`), ""},
		{"an interrupted turn", turn("interrupted", `"usageLimitExceeded"`), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "frames.jsonl")
			if err := os.WriteFile(path, []byte(tc.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
			setCodexFixture(t, prof, codexFixture{ThreadID: "thread-limit", Account: "apikey", CompleteTurn: true, FirstTurnReplay: path})
			ctx := context.Background()
			dto, err := codexLaunch(t, m, tenant, prof)
			if err != nil {
				t.Fatalf("createRun: %v", err)
			}
			if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
				t.Fatalf("input: %v", err)
			}
			if tc.want != "" {
				waitFor(t, "the limit on the run", func() bool {
					d, _ := m.getRun(ctx, tenant, dto.RunRef)
					return d.Limit != nil
				})
				got, _ := m.getRun(ctx, tenant, dto.RunRef)
				if got.Limit.Kind != tc.want || got.Limit.ResetsAt != "" {
					t.Fatalf("limit = %+v, want kind %s and no reset time", got.Limit, tc.want)
				}
				return
			}
			// A second turn that completes comes after everything the first one sent, in
			// the same order the runtime applies it; once it has cleared the launch's
			// limit state, "no limit" is a finding.
			waitFor(t, "the replayed turn ended", func() bool { return codexIdle(m, tenant, dto.RunRef) })
			if err := m.sendTextInput(ctx, tenant, dto.RunRef, "again"); err != nil {
				t.Fatalf("second input: %v", err)
			}
			waitFor(t, "the second turn was applied", func() bool {
				m.rt.mu.Lock()
				lr := m.rt.live[liveKey(tenant, dto.RunRef)]
				m.rt.mu.Unlock()
				if lr == nil {
					return false
				}
				lr.mu.Lock()
				defer lr.mu.Unlock()
				return lr.limitClear
			})
			if got, _ := m.getRun(ctx, tenant, dto.RunRef); got.Limit != nil {
				t.Fatalf("limit = %+v, want none", got.Limit)
			}
		})
	}
}

func codexIdle(m *Module, tenant model.TenantID, runRef string) bool {
	m.rt.mu.Lock()
	lr := m.rt.live[liveKey(tenant, runRef)]
	m.rt.mu.Unlock()
	return lr != nil && lr.session != nil && lr.session.ActiveTurn() == ""
}

// The reset time is when the windows that are used up all lift: the latest of them, and
// never a window that had already reset when the turn ended.
func TestCodexLimitTakesTheLatestUsedUpWindow(t *testing.T) {
	const completedAt = 1791981990
	update := func(primaryPct, primaryReset, secondaryPct, secondaryReset int) string {
		return fmt.Sprintf(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":%d,"windowDurationMins":300,"resetsAt":%d},"secondary":{"usedPercent":%d,"windowDurationMins":10080,"resetsAt":%d}}}}`,
			primaryPct, primaryReset, secondaryPct, secondaryReset)
	}
	failed := `{"method":"turn/completed","params":{"threadId":"01a13a73-2fe6-7593-a275-596356a9eacf","turn":{"id":"01a13a73-30ab-7e02-8f7a-3a0f8f930986","status":"failed","completedAt":1791981990,"error":{"message":"x","codexErrorInfo":"usageLimitExceeded"}}}}`
	rfc := func(epoch int) string { return time.Unix(int64(epoch), 0).UTC().Format(time.RFC3339) }
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"both used up: the later reset", []string{update(100, 1791992788, 100, 1791999999), failed}, rfc(1791999999)},
		{"the earlier one listed last", []string{update(100, 1791999999, 100, 1791992788), failed}, rfc(1791999999)},
		{"a window that reset before the turn ended is stale", []string{update(100, completedAt-100, 100, 1791992788), failed}, rfc(1791992788)},
		{"a stale window alone names no reset", []string{update(100, completedAt-100, 60, 1791992788), failed}, ""},
		{"a window not used up names no reset", []string{update(60, 1791992788, 42, 1791999999), failed}, ""},
		{"a later sparse update keeps the other window", []string{update(100, 1791992788, 100, 1791999999),
			`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1791992788}}}}`, failed}, rfc(1791999999)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "frames.jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(tc.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
			setCodexFixture(t, prof, codexFixture{ThreadID: "thread-limit", Account: "apikey", FirstTurnReplay: path})
			ctx := context.Background()
			dto, err := codexLaunch(t, m, tenant, prof)
			if err != nil {
				t.Fatalf("createRun: %v", err)
			}
			if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
				t.Fatalf("input: %v", err)
			}
			waitFor(t, "the limit on the run", func() bool {
				d, _ := m.getRun(ctx, tenant, dto.RunRef)
				return d.Limit != nil
			})
			if got, _ := m.getRun(ctx, tenant, dto.RunRef); got.Limit.ResetsAt != tc.want {
				t.Fatalf("resets_at = %q, want %q", got.Limit.ResetsAt, tc.want)
			}
		})
	}
}

// The wire shape other clients read: kind, RFC 3339 reset time, the account.
func TestRunLimitWireShape(t *testing.T) {
	raw, err := json.Marshal(runDTO{Limit: &runLimit{Kind: "usage", ResetsAt: claudeUsageLimitResetsAt, AccountRef: "ppf_x"}})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Limit map[string]string `json:"limit"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Limit["kind"] != "usage" || wire.Limit["resets_at"] != claudeUsageLimitResetsAt || wire.Limit["account_ref"] != "ppf_x" {
		t.Fatalf("limit on the wire = %v", wire.Limit)
	}
	raw, _ = json.Marshal(runDTO{})
	if bytes.Contains(raw, []byte(`"limit"`)) {
		t.Fatalf("a run with no limit carries one: %s", raw)
	}
}

// A stored value that is not a limit is no fact.
func TestStoredRunLimitReadsOnlyAFact(t *testing.T) {
	for _, tc := range []struct {
		name, stored string
		want         bool
	}{
		{"a fact", `{"kind":"usage","resets_at":"` + claudeUsageLimitResetsAt + `","account_ref":"ppf_a"}`, true},
		{"nothing stored", "", false},
		{"not json", "usage", false},
		{"no kind", `{"resets_at":"` + claudeUsageLimitResetsAt + `"}`, false},
	} {
		if got := storedRunLimit(model.Record{colRunUsageLimit: tc.stored}) != nil; got != tc.want {
			t.Errorf("%s: shown = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A usage limit whose tool named no reset time takes it from the organization's own login
// of that tool, and only from it: a run on a home of its own has no such window.
func TestLoginWindowResetIsOnlyForTheOrganizationsOwnLogin(t *testing.T) {
	root := t.TempDir()
	const tenant = "tnt_limit"
	_, own, ok := ToolLoginHome(root, tenant, providerDriverClaude)
	if !ok {
		t.Fatal("no own login home")
	}
	want := time.Date(2026, 10, 14, 21, 0, 0, 0, time.UTC)
	var asked []string
	m := &Module{toolLoginsRoot: root}
	m.rt = newRuntimeState(&Dependencies{LimitResets: func(_ context.Context, _ model.TenantID, instance string) (time.Time, bool) {
		asked = append(asked, instance)
		return want, true
	}})
	run := func(home string) *liveRun {
		return &liveRun{tenant: tenant, profile: &ProviderHomeSnapshot{
			Driver: providerDriverClaude, AuthSource: AuthSourceAccountHome, ConfigHome: home,
		}}
	}
	if got := m.loginWindowReset(context.Background(), run(own)); !got.Equal(want) || len(asked) != 1 || asked[0] != "claude/olivares" {
		t.Fatalf("own login: reset %v, asked %v, want %v from claude/olivares", got, asked, want)
	}
	if got := m.loginWindowReset(context.Background(), run(t.TempDir())); !got.IsZero() || len(asked) != 1 {
		t.Fatalf("a home of its own: reset %v, asked %v", got, asked)
	}
	if got := m.loginWindowReset(context.Background(), &liveRun{tenant: tenant}); !got.IsZero() {
		t.Fatalf("a run with no profile: reset %v", got)
	}
}

// The reset time a limit records: the tool's own wins; a usage limit without one falls
// back to the organization's login windows if they are still ahead; a rate limit never
// borrows a plan window.
func TestLimitResetsAtPrefersTheToolsOwn(t *testing.T) {
	root := t.TempDir()
	const tenant = "tnt_limit"
	_, own, _ := ToolLoginHome(root, tenant, providerDriverClaude)
	m, _, _, _ := newRuntimeHarness(t)
	m.toolLoginsRoot = root
	window := m.now().Add(2 * time.Hour)
	m.rt.LimitResets = func(context.Context, model.TenantID, string) (time.Time, bool) { return window, true }
	lr := &liveRun{tenant: tenant, profile: &ProviderHomeSnapshot{Driver: providerDriverClaude, AuthSource: AuthSourceAccountHome, ConfigHome: own}}
	tool := m.now().Add(time.Hour)
	ctx := context.Background()
	if got := m.limitResetsAt(ctx, lr, limitReport{Kind: limitKindUsage, ResetsAt: tool}); !got.Equal(tool) {
		t.Errorf("the tool's own reset = %v, want %v", got, tool)
	}
	if got := m.limitResetsAt(ctx, lr, limitReport{Kind: limitKindUsage}); !got.Equal(window) {
		t.Errorf("fallback = %v, want the login window %v", got, window)
	}
	if got := m.limitResetsAt(ctx, lr, limitReport{Kind: limitKindRate}); !got.IsZero() {
		t.Errorf("a rate limit borrowed a plan window: %v", got)
	}
	past := m.now().Add(-time.Hour)
	m.rt.LimitResets = func(context.Context, model.TenantID, string) (time.Time, bool) { return past, true }
	if got := m.limitResetsAt(ctx, lr, limitReport{Kind: limitKindUsage}); !got.IsZero() {
		t.Errorf("a window that had already lifted gave a reset: %v", got)
	}
}

// Each field of a window event is read on its own: a reset time the tool shapes
// differently costs the reset time, not the rejection, and a reset time no plan has
// (beyond the year 2100) is no reset time.
func TestClaudeWindowEventFieldsAreReadOnTheirOwn(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		rejected   bool
		resetsAt   int64
	}{
		{"all fields", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1791992743}}`, true, 1791992743},
		{"reset time is not a number", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":"soon"}}`, true, 0},
		{"fractional reset time", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1791992743.5}}`, true, 0},
		{"reset time beyond any plan", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":9000000000000000000}}`, true, 0},
		{"allowed", `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1791992743}}`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := parseLimitFrame([]byte(tc.line))
			if !ok || f.Rejected != tc.rejected || f.ResetsAt.Unix() != tc.resetsAt && !(tc.resetsAt == 0 && f.ResetsAt.IsZero()) {
				t.Fatalf("parse = %+v ok=%v, want rejected=%v resetsAt=%d", f, ok, tc.rejected, tc.resetsAt)
			}
		})
	}
}

// When both the five-hour and the weekly window are rejected, the person can go on
// only when the later one lifts, whichever order the tool reported them in.
func TestClaudeLatestRejectedWindowDecidesTheReset(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-limit"}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-limit"
	})
	weekly, fiveHour := clk.get().Add(48*time.Hour).Truncate(time.Second), clk.get().Add(5*time.Hour).Truncate(time.Second)
	frames := recordedFrames(t, claudeUsageLimitFrames)
	for _, at := range []time.Time{weekly, fiveHour} {
		fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(fmt.Sprintf(
			`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":%d}}`, at.Unix()))}
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: frames[len(frames)-1]}
	waitFor(t, "the limit on the run", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.Limit != nil
	})
	got, _ := m.getRun(ctx, tenant, dto.RunRef)
	if want := weekly.UTC().Format(time.RFC3339); got.Limit.ResetsAt != want {
		t.Fatalf("resets_at = %q, want the later window %q", got.Limit.ResetsAt, want)
	}
}

// A reset time more than a year away is not a plan window: the limit is recorded, the
// time is left out, and nothing parks the account until the next century.
func TestLimitLeavesOutAResetTimeBeyondAYear(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-limit"}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-limit"
	})
	frames := recordedFrames(t, claudeUsageLimitFrames)
	far := clk.get().AddDate(5, 0, 0)
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(fmt.Sprintf(
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":%d}}`, far.Unix()))}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: frames[len(frames)-1]}
	waitFor(t, "the limit on the run", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.Limit != nil
	})
	if got, _ := m.getRun(ctx, tenant, dto.RunRef); got.Limit.Kind != limitKindUsage || got.Limit.ResetsAt != "" {
		t.Fatalf("limit = %+v, want kind usage and no reset time", got.Limit)
	}
}

// A reset time that has passed is not carried onto a limit the tool restates without
// one: the account is limited again, until a time nobody knows, and the stored time
// would say it is not.
func TestLimitDoesNotCarryAResetTimeThatHasPassed(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-limit"}
	m, st, tenant, clk := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-limit"
	})
	frames := recordedFrames(t, claudeUsageLimitFrames)
	for _, f := range frames {
		fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: f}
	}
	waitFor(t, "the limit on the run", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.Limit != nil && d.Limit.ResetsAt == claudeUsageLimitResetsAt
	})
	reset, _ := time.Parse(time.RFC3339, claudeUsageLimitResetsAt)
	clk.set(reset.Add(time.Hour))
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: frames[len(frames)-1]}
	waitFor(t, "the restated limit without a reset time", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.Limit != nil && d.Limit.ResetsAt == ""
	})
	if n := countNamedRunEvents(t, st, tenant, dto.RunRef, "limit_reached"); n != 2 {
		t.Fatalf("limit_reached events = %d, want 2 (the limit, then the same limit with no known reset)", n)
	}
}

// A move ends the fact: it was about the account the session leaves, and moving back
// must not bring it back. Show and the list both carry it over HTTP before the move; the
// resume on the other account carries none.
func TestMoveClearsTheLimit(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	limitOf := func(r resp) any {
		t.Helper()
		if r.code != http.StatusOK {
			t.Fatalf("read = %d %s", r.code, r.raw)
		}
		return r.body["limit"]
	}
	show := func() any {
		return limitOf(f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef, f.admin, nil, f.hdr))
	}
	listed := func() any {
		t.Helper()
		r := f.h.doJSON("GET", "/v1/m/sessions/runs", f.admin, nil, f.hdr)
		if r.code != http.StatusOK {
			t.Fatalf("list = %d %s", r.code, r.raw)
		}
		items, _ := r.body["items"].([]any)
		for _, it := range items {
			if row, _ := it.(map[string]any); row["run_ref"] == f.runRef {
				return row["limit"]
			}
		}
		t.Fatalf("run %s not in the list: %s", f.runRef, r.raw)
		return nil
	}
	for _, frame := range recordedFrames(t, claudeUsageLimitFrames) {
		f.fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: frame}
	}
	waitFor(t, "the limit on the run", func() bool {
		d, _ := f.m.getRun(ctx, f.tenant, f.runRef)
		return d.Limit != nil
	})
	if got, _ := show().(map[string]any); got["kind"] != limitKindUsage {
		t.Fatalf("show limit = %v, want kind usage", got)
	}
	if got, _ := listed().(map[string]any); got["kind"] != limitKindUsage {
		t.Fatalf("list limit = %v, want kind usage", got)
	}
	f.stop(t)
	if r := f.post(t, "move", map[string]any{"provider_profile_ref": f.b}); r.code != http.StatusOK {
		t.Fatalf("move = %d %s", r.code, r.raw)
	}
	rec, err := f.m.loadRun(ctx, f.tenant, f.runRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.String(colRunUsageLimit); got != "" {
		t.Fatalf("the stored limit survived the move: %q", got)
	}
	if r := f.post(t, "resume", nil); r.code != http.StatusOK || r.body["limit"] != nil {
		t.Fatalf("resume on the other account = %d, limit %v: %s", r.code, r.body["limit"], r.raw)
	}
	if got := show(); got != nil {
		t.Fatalf("show after the move and resume still carries limit %v", got)
	}
	if got := listed(); got != nil {
		t.Fatalf("list after the move and resume still carries limit %v", got)
	}
}
