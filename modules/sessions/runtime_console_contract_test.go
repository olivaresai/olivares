// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// consoleEnvAllowPermitted is the exact list the launch dialog's positive control
// types, kept identical in web/src/features/agentops/run-create-profile.test.tsx. That
// test's transport is MOCKED, so it can only prove what the browser posts; this file is
// where the same names meet the ACTUAL server. Neither half is inferred from the other,
// and the pair is what makes either worth anything.
//
// The last three are prefix-collision witnesses: the profile-owned check is on the EXACT
// name and the server's own reserved rule is a PREFIX for CLAUDE_CODE_ but an EXACT match
// for CLAUDE_CONFIG_DIR, so each of these must pass while its neighbour is refused.
var consoleEnvAllowPermitted = []string{
	"PATH", "TERM", "MY_PROJECT_FLAG",
	"HOMEBREW_PREFIX", "HOME_BACKUP", "CLAUDE_CONFIG_DIR_EXTRA",
}

// TestConsoleSendContractOnADriverRunIsTextNotLine is the SERVER half of the console
// correction, on the exact route the console uses: the UNFENCED operator input of a
// running profiled Codex session, with no work fence, over authenticated HTTP against
// a real store and an owned Codex JSON-RPC child.
//
// It exists because the composed console could launch an operable Codex profile and
// then sent every turn as `{"line": …}`. The engine was right and the console was
// wrong, so this pins the engine's side of that pair: a raw frame is refused BEFORE
// the child hears anything, and the typed turn is accepted and reaches it exactly
// once. Both directions are asserted on the SAME run in the same order the operator
// would hit them, because "the raw one is refused" and "the typed one works" are only
// worth something together — a server that refused both would satisfy either alone.
//
// ⛔ THE REFUSAL IS THE POINT, NOT AN INCONVENIENCE. The child is an owned JSON-RPC
// peer: an arbitrary line on its stdin is its whole method surface, approvals
// included. This test must never be "fixed" by making the server accept a raw line
// for a driver run.
func TestConsoleSendContractOnADriverRunIsTextNotLine(t *testing.T) {
	m, h, admin, tenant, prof := codexHTTPHarness(t, "console-send-contract")
	record := setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-console-contract", Account: "apikey",
	})
	runRef, _ := codexHTTPRun(t, m, h, admin, tenant, prof)
	inputPath := "/v1/m/sessions/runs/" + runRef + "/input"

	before := readFixtureRecord(t, record)
	startsBefore := countMethod(before.Methods, codexMethodTurnStart)
	methodsBefore := len(before.Methods)

	// 1. The raw line the console used to send: refused, and the child never hears it.
	raw := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"line": `{"id":99,"method":"turn/start","params":{}}`,
	}, tenantHdr(tenant))
	if raw.code != http.StatusBadRequest {
		t.Fatalf("raw line to a driver run = %d %s, want 400", raw.code, raw.raw)
	}
	afterRaw := readFixtureRecord(t, record)
	if got := len(afterRaw.Methods); got != methodsBefore {
		t.Fatalf("the refused raw line added %d child method(s); a pre-effect refusal adds none",
			got-methodsBefore)
	}

	// 2. The typed turn the console sends now: 202, and exactly one turn/start.
	typed := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"text": "hello",
	}, tenantHdr(tenant))
	if typed.code != http.StatusAccepted {
		t.Fatalf("typed text to a driver run = %d %s, want 202", typed.code, typed.raw)
	}
	if accepted, _ := typed.body["accepted"].(bool); !accepted {
		t.Fatalf("202 body = %s, want the closed {\"accepted\":true} the contract publishes", typed.raw)
	}
	waitFor(t, "the turn reached the owned child", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart) == startsBefore+1
	})
	if got := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart); got != startsBefore+1 {
		t.Fatalf("turn/start count = %d, want exactly one more than the %d before", got, startsBefore)
	}

	// 3. Sending both is not a third contract: it is a malformed request, refused
	//    before either interpretation is chosen. The console never builds this body,
	//    and the server does not pick a winner if something else does.
	both := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"text": "hello", "line": `{"id":1}`,
	}, tenantHdr(tenant))
	if both.code != http.StatusBadRequest {
		t.Fatalf("text+line together = %d %s, want 400", both.code, both.raw)
	}

	// The console's non-terminal action reaches the same authenticated route and
	// owned child. A successful interrupt must leave the next message usable.
	live, ok := m.rt.getLive(tenant, runRef)
	if !ok {
		t.Fatal("launched run has no owned live handle")
	}
	waitFor(t, "active turn before console interrupt", func() bool { return live.session.ActiveTurn() != "" })
	interrupted := h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/interrupt", admin, nil, tenantHdr(tenant))
	if interrupted.code != http.StatusOK || interrupted.body["state"] != stateRunning {
		t.Fatalf("console interrupt = %d %s, want 200 and running", interrupted.code, interrupted.raw)
	}
	peer := readFixtureRecord(t, record)
	if countMethod(peer.Methods, codexMethodTurnInterrupt) != 1 {
		t.Fatal("console interruption did not reach the owned child exactly once")
	}
	next := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{"text": "next turn"}, tenantHdr(tenant))
	if next.code != http.StatusAccepted {
		t.Fatalf("console send after interrupt = %d %s, want 202", next.code, next.raw)
	}
	waitFor(t, "next turn on the same conversation", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart) == startsBefore+2
	})
	if countMethod(readFixtureRecord(t, record).Methods, codexMethodThreadStart) != 1 {
		t.Fatal("interruption replaced the conversation")
	}
}

// TestEnvAllowPositiveNamesTheDialogAdmitsAreTheOnesTheServerAdmits closes the gap a
// mocked browser transport cannot: it asks the REAL server, over authenticated HTTP with
// a real store, what it does with each class of env_allow name the launch dialog decides
// about.
//
// ⛔ IT EXISTS BECAUSE A POSITIVE CONTROL WAS WRONG. The web positive case first listed
// CODEX_MODEL as an ordinary variable. The engine reserves every `CODEX_*` name
// (forbiddenInheritedEnvName, procrunner.go) and validateCreate applies that to every
// env_allow item, so the browser would have posted a body the server refuses and the
// dialog would have said nothing. A mocked-transport test can never notice that; only
// this one can.
func TestEnvAllowPositiveNamesTheDialogAdmitsAreTheOnesTheServerAdmits(t *testing.T) {
	m, h, admin, tenant, prof := codexHTTPHarness(t, "console-env-allow")
	setCodexFixture(t, prof, codexFixture{ThreadID: "thread-env-allow", Account: "apikey"})

	create := func(names []string) resp {
		body := map[string]any{
			"transport": "stream-json", "permission_mode": "default", "isolation": "native",
			"provider_profile_ref": prof.Ref,
		}
		if names != nil {
			body["env_allow"] = names
		}
		return h.doJSON(http.MethodPost, "/v1/m/sessions/runs", admin, body, tenantHdr(tenant))
	}

	t.Run("the names the dialog admits are accepted by the server", func(t *testing.T) {
		// The whole list at once, because the claim is that EVERY one of them passes: a
		// single refused name fails this case, which is exactly what it is here to catch.
		created := create(consoleEnvAllowPermitted)
		if created.code != http.StatusCreated {
			t.Fatalf("env_allow %v = %d %s, want 201", consoleEnvAllowPermitted, created.code, created.raw)
		}
		runRef, _ := created.body["run_ref"].(string)
		if runRef == "" {
			t.Fatalf("accepted launch carried no run_ref: %s", created.raw)
		}
		live, ok := m.rt.getLive(tenant, runRef)
		if !ok {
			t.Fatalf("run %s is not live", runRef)
		}
		t.Cleanup(func() {
			_ = live.proc.Stop(context.Background())
			select {
			case <-live.finalizedCh:
			case <-time.After(5 * time.Second):
			}
		})
	})

	t.Run("CODEX_MODEL is reserved by the server, whatever the dialog thinks", func(t *testing.T) {
		// The name the first version of the web positive control called ordinary.
		refused := create([]string{"PATH", "CODEX_MODEL"})
		if refused.code != http.StatusBadRequest {
			t.Fatalf("env_allow CODEX_MODEL = %d %s, want 400", refused.code, refused.raw)
		}
	})

	t.Run("every name the dialog prevalidates is refused by the server too", func(t *testing.T) {
		// The dialog is a prevalidation, not a second authority: if the server admitted
		// one of these four, the dialog would be inventing a rule rather than mirroring
		// one. All four are refused before any child spawn — HOME by the profile-owned
		// check, the other three by that AND the reserved-prefix rule.
		for _, owned := range []string{"HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "GROK_HOME"} {
			refused := create([]string{"PATH", owned})
			if refused.code != http.StatusBadRequest {
				t.Errorf("env_allow %s = %d %s, want 400", owned, refused.code, refused.raw)
			}
		}
	})

	t.Run("the dialog does not claim to know the whole reserved set", func(t *testing.T) {
		// Stated as a limit rather than left implicit: these are refused by the server
		// and NOT prevalidated by the dialog, which is why its message must not promise
		// that everything outside its four names is accepted.
		for _, reserved := range []string{
			"OLIVARES_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_THING",
			"OPENAI_API_KEY", "GROK_API_KEY", "XAI_API_KEY", "DISABLE_AUTOUPDATER",
		} {
			refused := create([]string{reserved})
			if refused.code != http.StatusBadRequest {
				t.Errorf("env_allow %s = %d %s, want 400", reserved, refused.code, refused.raw)
			}
		}
	})
}
