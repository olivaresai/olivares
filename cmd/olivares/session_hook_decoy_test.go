// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// decoyProbe is a deliberately small engine on the real seams: it plants one file at launch,
// recognizes a proven session naming it at the hook, and reports through the composition's
// own sinks. The Business engine is the same shape; what this test proves is the wiring
// around it: the launch asks it, the hook names the run the credential proved, and the
// finding and the stop reach the run through the real session module.
type decoyProbe struct {
	deps sessionDecoyDeps
	stop bool

	mu      sync.Mutex
	plants  []probePlant
	cleaned int
	path    string
}

type probePlant struct {
	tenant        model.TenantID
	run, home, wd string
}

func (p *decoyProbe) PlantSessionDecoys(_ context.Context, tenant model.TenantID, run, home, workDir string) (func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plants = append(p.plants, probePlant{tenant, run, home, workDir})
	if home == "" {
		return nil, nil
	}
	p.path = filepath.Join(home, ".aws", "credentials")
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p.path, []byte("decoy\n"), 0o600); err != nil {
		return nil, err
	}
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.cleaned++
		_ = os.Remove(p.path)
	}, nil
}

func (p *decoyProbe) Inspect(ctx context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	p.mu.Lock()
	path := p.path
	p.mu.Unlock()
	if in.SessionRef == "" || path == "" {
		return claudeapi.ContentInspectionDecision{Forward: true}
	}
	for _, ch := range in.Channels {
		if !strings.Contains(ch.Text, path) {
			continue
		}
		tenant := model.TenantID(in.Tenant)
		_ = p.deps.Finding(ctx, tenant, in.SessionRef, sdkmodel.FindingReport{
			Kind: "decoy_credential_touched", Severity: sdkmodel.SeverityHigh,
			Title: "Decoy credential touched: " + path, OccurredAt: time.Now().UTC(),
		})
		if p.stop {
			_ = p.deps.Stop(ctx, tenant, in.SessionRef, "a decoy credential in the session home was touched")
			return claudeapi.ContentInspectionDecision{Status: http.StatusForbidden, Reason: "blocked: decoy touched"}
		}
	}
	return claudeapi.ContentInspectionDecision{Forward: true}
}

func newDecoyHook(t *testing.T, stop bool) (*denialBudgetHook, *decoyProbe) {
	t.Helper()
	probe := &decoyProbe{stop: stop}
	f := newDenialBudgetHookWith(t, sessions.DenialBudget{Consecutive: 50, Total: 50},
		func(m *sessions.Module, c *sessionHookCredentials) {
			probe.deps = newSessionDecoyDeps(c.SessionCredentials, m)
			m.SessionDecoys = probe
		},
		func(d *hookpep.Decider) { d.Inspector = probe })
	return f, probe
}

// findingSources returns the source (the finding's kind) and the title of every decoy
// finding on every live session timeline of the tenant. Other findings, such as the
// guardrail's own, are not this test's subject.
func (f *denialBudgetHook) findingSources() (sources, titles []string) {
	f.t.Helper()
	var live struct {
		Items []struct {
			LiveRef string `json:"live_ref"`
		} `json:"items"`
	}
	if code := f.h.reqInto(http.MethodGet, "/v1/m/sessions/live", f.h.adminToken, f.h.tenantA, nil, &live); code != http.StatusOK {
		f.t.Fatalf("live=%d", code)
	}
	for _, it := range live.Items {
		var tl struct {
			Items []struct {
				Kind   string `json:"kind"`
				Source string `json:"source"`
				Title  string `json:"title"`
			} `json:"items"`
		}
		if code := f.h.reqInto(http.MethodGet, "/v1/m/sessions/live/by-id/"+it.LiveRef+"/timeline", f.h.adminToken, f.h.tenantA, nil, &tl); code != http.StatusOK {
			f.t.Fatalf("timeline=%d", code)
		}
		for _, e := range tl.Items {
			if e.Kind == "finding" && e.Source == "decoy_credential_touched" {
				sources, titles = append(sources, e.Source), append(titles, e.Title)
			}
		}
	}
	return sources, titles
}

func readOf(path string) string {
	return `{"hook_event_name":"PreToolUse","tool_name":"Read","session_id":"claimed-by-the-payload","tool_input":{"file_path":"` + path + `"}}`
}

func waitForDecoyState(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The launch plants through the edition's source in the profile's own home, a read of the
// file at the hook raises one finding in the run's own findings, and nothing else does.
func TestSessionHookDecoyTouchIsReportedInTheRunsFindings(t *testing.T) {
	f, probe := newDecoyHook(t, false)

	probe.mu.Lock()
	plants := append([]probePlant(nil), probe.plants...)
	path := probe.path
	probe.mu.Unlock()
	if len(plants) != 1 || plants[0].run != f.run || plants[0].home == "" || plants[0].wd == "" || path == "" {
		t.Fatalf("launch asked the source %+v, want once for the run with its home and working directory", plants)
	}
	if plants[0].home == plants[0].wd {
		t.Fatal("the home and the working directory are the same place")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the decoy was not planted: %v", err)
	}

	if d, _ := f.fire(t.Context(), readOf("/tmp/not-a-decoy")); d != "allow" {
		t.Fatalf("an ordinary read = %s", d)
	}
	if sources, _ := f.findingSources(); len(sources) != 0 {
		t.Fatalf("an ordinary read raised findings: %v", sources)
	}
	if d, _ := f.fire(t.Context(), readOf(path)); d != "allow" {
		t.Fatalf("with stop_session off the touching read = %s, want allow", d)
	}
	waitForDecoyState(t, "the decoy finding on the run's timeline", func() bool {
		sources, titles := f.findingSources()
		return len(sources) == 1 && sources[0] == "decoy_credential_touched" && titles[0] == "Decoy credential touched: "+path
	})
	if state := f.runState(); state == "stopped" {
		t.Fatal("a finding alone ended the session")
	}

	// The run's end takes the decoys away.
	if code, _ := f.h.req(http.MethodPost, "/v1/m/sessions/runs/"+f.run+"/stop", f.h.adminToken, f.h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("stop=%d", code)
	}
	waitForDecoyState(t, "the decoy to be removed with the run", func() bool {
		probe.mu.Lock()
		defer probe.mu.Unlock()
		return probe.cleaned == 1
	})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the decoy outlived the run: %v", err)
	}
}

// With the stop on, the touching call is refused and the run ends through the module's own
// policy stop.
func TestSessionHookDecoyTouchCanStopTheRun(t *testing.T) {
	f, probe := newDecoyHook(t, true)
	probe.mu.Lock()
	path := probe.path
	probe.mu.Unlock()
	if d, _ := f.fire(t.Context(), readOf(path)); d != "deny" {
		t.Fatalf("the touching read = %s, want deny", d)
	}
	waitForDecoyState(t, "the run to stop", func() bool { return f.runState() == "stopped" })
	waitForDecoyState(t, "the finding to be recorded", func() bool {
		sources, _ := f.findingSources()
		return len(sources) == 1
	})
}
