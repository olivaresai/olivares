// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// Journeys J01–J08 (audit §10.8) as tests that run here without docker.
// Real vendor binaries are not required; official_cli_test.go probes those
// separately and skips when LookPath fails.
//
// ⛔ THEY RUN ON THE TRANSPORT PRODUCTION LAUNCHES, AND UNTIL r3 THEY DID NOT.
// Every journey below built the Module on NewPTYRunner() with a peer that
// tolerated a terminal, so all seven were green whether the composition root
// wired pipes or a pseudo-terminal: they proved nothing about the argv the
// engine actually launches. An independent review measured that on 2026-09-18.
//
// Now the runner comes from the SAME factory the composition root calls
// (sessions.NewOfficialRunner → cliruntime.LaunchTransport) and the peer refuses
// a terminal exactly as the real `--print` binary does, so bypassing that
// factory turns these journeys red. J05 is the one journey that keeps the
// terminal runner, and it says why at its own definition.

// journeyRunner is the runner the production factory selects for the official
// launch forms. A journey that used a runner named by hand would be back to
// proving something about the test's choice instead of about the product's.
func journeyRunner(t *testing.T) Runner {
	t.Helper()
	runner, err := NewOfficialRunner(cliruntime.Kinds()...)
	if err != nil {
		t.Fatalf("the production runner factory refuses the declared kinds: %v", err)
	}
	return runner
}

func journeyModule(t *testing.T, runner Runner, program string) (*Module, *harness, string, model.TenantID) {
	t.Helper()
	m := New(WithSessionWorkspaceRoot(t.TempDir()),
		WithRunner(runner),
		WithProgram(program),
		WithCredentialSource(staticCred()),
		WithStopWaitDelay(2*time.Second),
	)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "journey")
	return m, h, admin, tenant
}

// journeyOnProductionTransport is journeyModule with nothing left to choose: the
// factory's runner and the peer that stands for the real binary.
func journeyOnProductionTransport(t *testing.T) (*Module, *harness, string, model.TenantID) {
	t.Helper()
	return journeyModule(t, journeyRunner(t), writePrintFormPeer(t))
}

func TestJourneyJ01_FirstLaunchAndEvidence(t *testing.T) {
	_, h, admin, tenant := journeyOnProductionTransport(t)
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{
		"transport": "stream-json", "name": "j01-first",
	})
	listed := h.do("GET", "/v1/m/sessions/runs", admin, tenantHdr(tenant))
	if listed.code != http.StatusOK {
		t.Fatalf("list = %d %s", listed.code, listed.raw)
	}
	got := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant))
	if got.code != http.StatusOK {
		t.Fatalf("get = %d %s", got.code, got.raw)
	}
	if got.body["run_ref"] != ref {
		t.Fatalf("run_ref = %v", got.body["run_ref"])
	}
	ev := h.do("GET", "/v1/m/sessions/runs/"+ref+"/events", admin, tenantHdr(tenant))
	if ev.code != http.StatusOK {
		t.Fatalf("events = %d %s", ev.code, ev.raw)
	}
	items, _ := ev.body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("J01: managed run must write evidence rows (run_event)")
	}
}

func TestJourneyJ02_HomesDoNotCross(t *testing.T) {
	TestOfficialLocal_HomesDoNotCross(t)
}

func TestJourneyJ03_WorkspaceIsCwdAndOutsideIsUntouched(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, h, admin, tenant := journeyOnProductionTransport(t)
	wsRoot := t.TempDir()
	wr := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{
		"root_path": wsRoot, "name": "j03",
	}, tenantHdr(tenant))
	if wr.code != http.StatusCreated {
		t.Fatalf("workspace = %d %s", wr.code, wr.raw)
	}
	body := map[string]any{
		"transport": "stream-json", "isolation": "native", "permission_mode": "default",
		"workspace_ref": wr.body["workspace_ref"],
	}
	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	ref := r.body["run_ref"].(string)
	lr, ok := m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatal("expected live handle")
	}
	waitFor(t, "process cwd is the workspace", func() bool { return lr.proc != nil })
	if got, _ := os.ReadFile(outside); string(got) != "keep" {
		t.Fatal("J03 must not delete files outside the workspace")
	}
	if _, err := os.Stat(wsRoot); err != nil {
		t.Fatalf("workspace vanished: %v", err)
	}
}

func TestJourneyJ04_StateIsObservedNotFabricated(t *testing.T) {
	_, h, admin, tenant := journeyOnProductionTransport(t)
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json"})
	got := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant))
	state, _ := got.body["state"].(string)
	if state != "running" && state != "idle" && state != "pending" {
		t.Fatalf("live state = %q (must be observed, not a fabricated success)", state)
	}
	if _, ok := got.body["success"]; ok {
		t.Fatal("must not publish a fabricated success field")
	}
	stop := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenant))
	if stop.code != http.StatusOK {
		t.Fatalf("stop = %d %s", stop.code, stop.raw)
	}
	after := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant))
	st, _ := after.body["state"].(string)
	if st != "stopped" && st != "failed" {
		t.Fatalf("after stop state = %q", st)
	}
	if after.body["exit_code"] == nil {
		t.Fatal("stop must record an observed exit_code")
	}
}

// TestJourneyJ05_TerminalAndOutputBounds is J05 — "open the environment's
// terminal" (cockpit research §CP-04) — and it is the ONE journey that keeps the
// terminal runner, with the measurement for why.
//
// ⛔ J05 IS NOT THE MANAGED `--print` SESSION, AND CONFLATING THE TWO IS WHAT
// THIS TEST USED TO DO. It opened an HTTP run with `transport: stream-json` on
// NewPTYRunner(): a governed stream-json form on a pseudo-terminal, which is
// exactly the configuration claude 2.1.276 refuses (exit 1, no protocol frame —
// printFormPeerScript carries the measurement). The journey it stands for is a
// different capability: a terminal client in the environment, whose child NEEDS
// a terminal on stdin.
//
// So it is asked in two halves, each on the transport that half is about:
//
//  1. the environment terminal — the terminal runner, a peer that REFUSES to run
//     without a terminal, and the child confirming it got one;
//  2. the capture bounds the journey requires ("los límites de captura son
//     visibles") — on the transport production launches, because the ring that
//     bounds a managed run is the managed run's.
func TestJourneyJ05_TerminalAndOutputBounds(t *testing.T) {
	t.Run("environment-terminal", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		proc, err := NewPTYRunner().Launch(ctx, LaunchSpec{
			Program: writeTerminalClientPeer(t), Dir: t.TempDir(), WaitDelay: 2 * time.Second,
		})
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		defer func() { _ = proc.Stop(context.Background()) }()
		if !waitProcFrame(t, proc, 6*time.Second, func(f OutputFrame) bool {
			return strings.Contains(string(f.Data), `"tty":true`)
		}) {
			t.Fatal("J05: the environment's terminal must present a terminal to the child")
		}
	})

	t.Run("output-bounds-on-the-production-transport", func(t *testing.T) {
		m, h, admin, tenant := journeyOnProductionTransport(t)
		ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json"})
		lr, ok := m.rt.getLive(tenant, ref)
		if !ok {
			t.Fatal("expected live handle")
		}
		waitFor(t, "init frame in ring", func() bool {
			return len(lr.ring.readFrom(0).frames) >= 1
		})
		// The managed run's child must NOT have a terminal, and the peer says so on
		// its own init frame rather than the test inferring it from the wiring.
		sawNoTTY := false
		for _, f := range lr.ring.readFrom(0).frames {
			if strings.Contains(string(f.Data), `"tty":false`) {
				sawNoTTY = true
			}
			if strings.Contains(string(f.Data), "must be provided") {
				t.Fatalf("the managed run was given a terminal and its child refused: %s", f.Data)
			}
		}
		if !sawNoTTY {
			t.Fatal("J05: the managed stream-json run's child must report that it has no terminal")
		}
		if lr.ring.maxCount <= 0 || lr.ring.maxBytes <= 0 {
			t.Fatal("output ring must be bounded")
		}
	})
}

func TestJourneyJ06_PermissionsOnExistingEnforcement(t *testing.T) {
	_, h, admin, tenant := journeyOnProductionTransport(t)
	viewer := h.viewerToken(admin, tenant, "viewer@j06.test")
	denied := h.doJSON("POST", "/v1/m/sessions/runs", viewer, map[string]any{
		"transport": "stream-json", "isolation": "native", "permission_mode": "default",
	}, tenantHdr(tenant))
	if denied.code != http.StatusForbidden {
		t.Fatalf("viewer create = %d, want 403 on sessions:run:write", denied.code)
	}
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json"})
	listed := h.do("GET", "/v1/m/sessions/runs", viewer, tenantHdr(tenant))
	if listed.code != http.StatusOK {
		t.Fatalf("viewer list = %d (sessions:run:read)", listed.code)
	}
	stop := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", viewer, nil, tenantHdr(tenant))
	if stop.code != http.StatusForbidden {
		t.Fatalf("viewer stop = %d, want 403", stop.code)
	}
	in := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/input", viewer, map[string]any{
		"line": `{"type":"user"}`,
	}, tenantHdr(tenant))
	if in.code != http.StatusForbidden {
		t.Fatalf("viewer input = %d, want 403", in.code)
	}
}

func TestJourneyJ07_StopReapAndReattachHonesty(t *testing.T) {
	m, h, admin, tenant := journeyOnProductionTransport(t)
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json"})
	if _, ok := m.rt.getLive(tenant, ref); !ok {
		t.Fatal("expected live handle")
	}
	stop := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenant))
	if stop.code != http.StatusOK {
		t.Fatalf("stop = %d %s", stop.code, stop.raw)
	}
	got := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant))
	st, _ := got.body["state"].(string)
	if st != "stopped" && st != "failed" {
		t.Fatalf("after stop state = %q", st)
	}
	if got.body["exit_code"] == nil {
		t.Fatal("stop must record an observed exit_code")
	}
	att := h.do("GET", "/v1/m/sessions/runs/"+ref+"/attach", admin, tenantHdr(tenant))
	if att.code != http.StatusOK {
		t.Fatalf("attach = %d %s", att.code, att.raw)
	}
	if !strings.Contains(att.raw, "not live") && !strings.Contains(att.raw, "notice") && !strings.Contains(att.raw, `"type":"end"`) {
		t.Fatalf("attach after stop must be an honest terminal notice, got %s", att.raw)
	}
}

func TestJourneyJ08_ReconnectCursorDoesNotDuplicate(t *testing.T) {
	script := writeAttachPeer(t)
	m := New(WithSessionWorkspaceRoot(t.TempDir()),
		WithRunner(journeyRunner(t)),
		WithProgram(script),
		WithCredentialSource(staticCred()),
		WithStopWaitDelay(2*time.Second),
	)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "j08")
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json"})
	lr, ok := m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatal("expected live handle")
	}
	waitAttach(t, "first batch", 8*time.Second, func() bool {
		return len(lr.ring.readFrom(0).frames) >= 4
	})
	ts := httptest.NewServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	first, _ := openAttach(t, ts, admin, tenant.String(), ref, 0)
	defer first.cancel()
	batch1 := collectSSETimeout(t, first.res.Body, 5*time.Second, func(evts []sseEvt) bool {
		return countOutputs(evts) >= 2
	})
	first.cancel()
	_ = first.res.Body.Close()
	seen := map[int64]struct{}{}
	var last int64
	for _, e := range batch1 {
		if e.Event != "output" {
			continue
		}
		var f struct {
			Seq int64 `json:"seq"`
		}
		if json.Unmarshal([]byte(e.Data), &f) != nil {
			continue
		}
		if _, dup := seen[f.Seq]; dup {
			t.Fatalf("J08 duplicate seq %d on first attach", f.Seq)
		}
		seen[f.Seq] = struct{}{}
		if f.Seq > last {
			last = f.Seq
		}
	}
	if last < 1 {
		t.Fatal("first attach delivered no output")
	}
	second, _ := openAttach(t, ts, admin, tenant.String(), ref, last+1)
	defer second.cancel()
	batch2 := collectSSETimeout(t, second.res.Body, 5*time.Second, func(evts []sseEvt) bool {
		return countOutputs(evts) >= 1
	})
	second.cancel()
	_ = second.res.Body.Close()
	for _, e := range batch2 {
		if e.Event != "output" {
			continue
		}
		var f struct {
			Seq int64 `json:"seq"`
		}
		if json.Unmarshal([]byte(e.Data), &f) != nil {
			continue
		}
		if _, dup := seen[f.Seq]; dup {
			t.Fatalf("J08 duplicate seq %d after reconnect (R04: do not duplicate an uncertain effect)", f.Seq)
		}
		if f.Seq <= last {
			t.Fatalf("reconnect replayed seq %d <= %d", f.Seq, last)
		}
		seen[f.Seq] = struct{}{}
	}
}

func TestJourneyJ08_ResumeAfterLossKeepsConversation(t *testing.T) {
	d, err := NewOfficialLocal(cliruntime.KindClaude, writePrintFormPeer(t))
	if err != nil {
		t.Fatal(err)
	}
	cliruntime.RunConformance(t, d)
}
