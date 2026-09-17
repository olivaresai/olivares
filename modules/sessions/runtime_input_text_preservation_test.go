// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// Text preservation on `POST /runs/{ref}/input`, exercised over the module's HTTP
// surface with its real authentication and store, against a local child process that
// records what it received.
//
// Three properties:
//   - nonblank text reaches the child as submitted, including leading indentation,
//     interior and trailing newlines and non-ASCII UTF-8, on the ordinary route and
//     on the work-fenced one;
//   - blank text, text sent together with line/message, and a missing or stale work
//     fence are refused before anything reaches the child;
//   - the work-fenced byte bound applies to the text as submitted.
//
// Each acceptance compares the string recorded at the child, since a method count
// cannot distinguish preserved text from edited text.

// preservedOperatorText carries indentation, an interior newline, non-ASCII UTF-8 and
// a trailing newline in one string.
const preservedOperatorText = "  \tsangría inicial y tabulador\nsegunda línea — ⚙ 三行目\n"

// preservedSteerText is the second turn's text; a steer is a separate provider method
// under the same contract.
const preservedSteerText = "\n   continúa aquí\n\n"

// TestRuntimeDriverInputPreservesTheOperatorTextAtTheChild covers the ordinary
// (unfenced) route on a driver-backed run.
func TestRuntimeDriverInputPreservesTheOperatorTextAtTheChild(t *testing.T) {
	f := newCodexTextFixture(t, "runtime-text-plain", "thread-http-text")

	// The turn that starts the conversation carries the text byte for byte.
	accepted := f.post(map[string]any{"text": preservedOperatorText})
	if accepted.code != http.StatusAccepted || accepted.body["accepted"] != true {
		t.Fatalf("text input = %d %s", accepted.code, accepted.raw)
	}
	f.waitForInputs(t, 1)
	if got := f.inputs(t); got[0] != preservedOperatorText {
		t.Fatalf("the child received %q, want the operator's own text %q", got[0], preservedOperatorText)
	}
	if got := f.methodCount(t, codexMethodTurnStart); got != 1 {
		t.Fatalf("turn/start count = %d, want 1", got)
	}

	// The steer carries its text unchanged too.
	steered := f.post(map[string]any{"text": preservedSteerText})
	if steered.code != http.StatusAccepted {
		t.Fatalf("steering text = %d %s", steered.code, steered.raw)
	}
	f.waitForInputs(t, 2)
	if got := f.inputs(t); got[1] != preservedSteerText {
		t.Fatalf("the steer carried %q, want %q", got[1], preservedSteerText)
	}
	if got := f.methodCount(t, codexMethodTurnSteer); got != 1 {
		t.Fatalf("turn/steer count = %d, want 1", got)
	}

	// A blank turn is still refused, before the child: preserving whitespace is not
	// the same as accepting a turn made only of whitespace.
	blank := f.post(map[string]any{"text": " \t\n  "})
	if blank.code != http.StatusBadRequest {
		t.Fatalf("whitespace-only text = %d %s, want 400", blank.code, blank.raw)
	}
	if msg := errorMessageOf(blank); msg != "input requires a non-empty line, message or text" {
		t.Fatalf("blank refusal message = %q", msg)
	}
	// A request carrying both shapes at once is refused.
	mixed := f.post(map[string]any{"text": preservedOperatorText, "line": "{\"raw\":true}"})
	if mixed.code != http.StatusBadRequest {
		t.Fatalf("text and line together = %d %s, want 400", mixed.code, mixed.raw)
	}
	if msg := errorMessageOf(mixed); msg != "send text, or line/message, not both" {
		t.Fatalf("mixed refusal message = %q", msg)
	}
	// A raw line alone stays refused on a driver-backed run, so this row cannot pass
	// by turning text into a frame.
	raw := f.post(map[string]any{"line": "{\"raw\":true}"})
	if raw.code == http.StatusAccepted {
		t.Fatalf("a raw line was accepted on a driver-backed run: %d %s", raw.code, raw.raw)
	}
	// None of the three refusals reached the process.
	if got := f.inputs(t); len(got) != 2 {
		t.Fatalf("a refused input crossed the process boundary: %d inputs %q", len(got), got)
	}
}

// TestRuntimeWorkFencedDriverInputPreservesTheOperatorTextAtTheChild covers the same
// contract on the fenced plane, to which a work-bound run is confined, and the byte
// bound that plane applies to one turn.
func TestRuntimeWorkFencedDriverInputPreservesTheOperatorTextAtTheChild(t *testing.T) {
	f := newCodexTextFixture(t, "runtime-text-fenced", "thread-http-text-work")
	fence := bindRunToFreshWorkLease(t, f.m, f.h, f.tenant, f.runRef, f.live.claim.SID)

	// The refusals of the fenced plane happen before any effect.
	unfenced := f.post(map[string]any{"text": preservedOperatorText})
	if unfenced.code != http.StatusConflict {
		t.Fatalf("unfenced text on a work-bound run = %d %s, want 409", unfenced.code, unfenced.raw)
	}
	stale := f.post(map[string]any{"text": preservedOperatorText, "work_lease_fence": fence + 1})
	if stale.code != http.StatusConflict || workAPIErrorCode(stale) != "stale_fence" {
		t.Fatalf("stale fenced text = %d %s", stale.code, stale.raw)
	}
	// The work-text ceiling applies to the text as submitted, so surrounding
	// whitespace counts towards it. The content here is five bytes and the submitted
	// text is over the ceiling; the refusal is invalid_command, before the child.
	oversized := strings.Repeat(" ", maxWorkTextInputBytes) + "hello\n"
	tooBig := f.post(map[string]any{"text": oversized, "work_lease_fence": fence})
	if tooBig.code != http.StatusBadRequest || workAPIErrorCode(tooBig) != "invalid_command" {
		t.Fatalf("oversized untrimmed fenced text = %d %s, want 400 invalid_command", tooBig.code, tooBig.raw)
	}
	// A blank turn is refused on this plane too, by the same handler branch.
	blank := f.post(map[string]any{"text": "   \n", "work_lease_fence": fence})
	if blank.code != http.StatusBadRequest {
		t.Fatalf("whitespace-only fenced text = %d %s, want 400", blank.code, blank.raw)
	}
	if got := f.inputs(t); len(got) != 0 {
		t.Fatalf("a refused fenced input crossed the process boundary: %q", got)
	}
	if got := f.methodCount(t, codexMethodTurnStart); got != 0 {
		t.Fatalf("a refused fenced input started %d turns", got)
	}

	// The exact fence carries the submitted bytes to the child.
	accepted := f.post(map[string]any{"text": preservedOperatorText, "work_lease_fence": fence})
	if accepted.code != http.StatusAccepted || accepted.body["accepted"] != true {
		t.Fatalf("fenced text = %d %s", accepted.code, accepted.raw)
	}
	f.waitForInputs(t, 1)
	if got := f.inputs(t); got[0] != preservedOperatorText {
		t.Fatalf("the child received %q, want the operator's own text %q", got[0], preservedOperatorText)
	}
	if got := f.methodCount(t, codexMethodTurnStart); got != 1 {
		t.Fatalf("turn/start count = %d, want 1", got)
	}
}

// TestGrokRuntimeDriverInputPreservesTheOperatorTextAtTheChild covers the second
// protocol driver. The input handler is shared, so one driver's result does not cover
// the other. This peer already recorded the prompt text it received and was not
// modified for this row.
func TestGrokRuntimeDriverInputPreservesTheOperatorTextAtTheChild(t *testing.T) {
	fx := newGrokHTTPFixture(t, "grok-text-preserved", AuthSourceAccountHome)
	record := setGrokFixture(t, fx.prof, grokFixture{SessionID: "conv-text-preserved"})
	runRef := fx.createRun(t)

	accepted := fx.input(preservedOperatorText, runRef)
	if accepted.code != http.StatusAccepted || accepted.body["accepted"] != true {
		t.Fatalf("text input = %d %s", accepted.code, accepted.raw)
	}
	waitFor(t, "the prompt reached the child", func() bool {
		return len(readGrokFixtureRecord(t, record).PromptTexts) == 1
	})
	if got := readGrokFixtureRecord(t, record).PromptTexts[0]; got != preservedOperatorText {
		t.Fatalf("the child received %q, want the operator's own text %q", got, preservedOperatorText)
	}
	// The blank refusal is the handler's, before any prompt.
	blank := fx.input(" \n\t", runRef)
	if blank.code != http.StatusBadRequest {
		t.Fatalf("whitespace-only text = %d %s, want 400", blank.code, blank.raw)
	}
	if got := readGrokFixtureRecord(t, record).PromptTexts; len(got) != 1 {
		t.Fatalf("the refused blank input reached the child: %q", got)
	}
}

// --- harness -----------------------------------------------------------------

// codexTextFixture is one profiled, driver-backed run behind the real HTTP server,
// with the peer's record path. It is the wiring the existing fenced-text row uses,
// factored out because these rows need both planes on the same kind of run.
type codexTextFixture struct {
	m      *Module
	h      *harness
	admin  string
	tenant model.TenantID
	prof   ProviderProfile
	record string
	runRef string
	live   *liveRun
}

func newCodexTextFixture(t *testing.T, org, thread string) *codexTextFixture {
	t.Helper()
	m := New(
		WithRunner(NewProcRunner()),
		WithProviderDriver(NewCodexDriver()),
		WithDriverProgram(providerDriverCodex, os.Args[0]),
		WithProductVersion("test"),
		WithStopWaitDelay(2*time.Second),
		WithDriverTimeouts(20*time.Second, 2*time.Second),
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	)
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.EnableProfiledLaunches()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, org)

	config, home := t.TempDir(), t.TempDir()
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: config, UserHome: home,
		DisplayName: org, AuthSource: AuthSourceAccountHome,
	})
	record := setCodexFixture(t, prof, codexFixture{ThreadID: thread, Account: "apikey"})

	created := h.doJSON(http.MethodPost, "/v1/m/sessions/runs", admin, map[string]any{
		"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		"provider_profile_ref": prof.Ref,
	}, tenantHdr(tenant))
	runRef, _ := created.body["run_ref"].(string)
	if created.code != http.StatusCreated || runRef == "" {
		t.Fatalf("create profiled run = %d %s", created.code, created.raw)
	}
	live, ok := m.rt.getLive(tenant, runRef)
	if !ok || live.claim.SID == "" {
		t.Fatalf("run %s has no admission claim", runRef)
	}
	t.Cleanup(func() {
		_ = live.proc.Stop(context.Background())
		select {
		case <-live.finalizedCh:
		case <-time.After(5 * time.Second):
		}
	})
	return &codexTextFixture{
		m: m, h: h, admin: admin, tenant: tenant, prof: prof,
		record: record, runRef: runRef, live: live,
	}
}

func (f *codexTextFixture) post(body map[string]any) resp {
	return f.h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+f.runRef+"/input",
		f.admin, body, tenantHdr(f.tenant))
}

func (f *codexTextFixture) inputs(t *testing.T) []string {
	t.Helper()
	return readFixtureRecord(t, f.record).Inputs
}

func (f *codexTextFixture) methodCount(t *testing.T, method string) int {
	t.Helper()
	return countMethod(readFixtureRecord(t, f.record).Methods, method)
}

// waitForInputs waits until the peer has recorded n inputs. The record is written
// before the reply, so the accepted response can be ahead of the file and a bare read
// would be a race.
func (f *codexTextFixture) waitForInputs(t *testing.T, n int) {
	t.Helper()
	waitFor(t, "the fixture peer recorded the input", func() bool {
		return len(readFixtureRecord(t, f.record).Inputs) >= n
	})
}

func errorMessageOf(r resp) string {
	body, _ := r.body["error"].(map[string]any)
	if body == nil {
		return ""
	}
	msg, _ := body["message"].(string)
	return msg
}
