// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner is a deterministic cmdRunner: it branches by subcommand and records
// every invocation (so a test can assert no secret ever reaches argv).
type fakeRunner struct {
	planExit    int    // exit for `plan` (forward)
	destroyExit int    // exit for `plan -destroy`
	refreshExit int    // exit for `plan -refresh-only`
	applyExit   int    // exit for `apply`
	showJSON    string // stdout for `show -json`
	calls       [][]string
	envs        [][]string
}

func (f *fakeRunner) run(_ context.Context, _ string, env []string, name string, args ...string) ([]byte, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "plan":
		switch {
		case hasArg(args, "-destroy"):
			return nil, f.destroyExit, nil
		case hasArg(args, "-refresh-only"):
			return nil, f.refreshExit, nil
		default:
			return nil, f.planExit, nil
		}
	case "show":
		return []byte(f.showJSON), 0, nil
	case "apply":
		return nil, f.applyExit, nil
	}
	return nil, 0, nil
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// tofuWorkspace creates a workspace dir with a .terraform backend metadata file of
// the given backend type ("" or "local" => unlocked; "s3" etc => remote).
func tofuWorkspace(t *testing.T, backendType string) string {
	t.Helper()
	dir := t.TempDir()
	if backendType != "absent" {
		if err := os.MkdirAll(filepath.Join(dir, ".terraform"), 0o755); err != nil {
			t.Fatal(err)
		}
		meta := `{"version":3,"backend":{"type":"` + backendType + `"}}`
		if err := os.WriteFile(filepath.Join(dir, ".terraform", "terraform.tfstate"), []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newTofuFor(t *testing.T, dir string, fr *fakeRunner) (*TofuBackend, Desired) {
	t.Helper()
	tb := NewTofuBackend(TofuConfig{
		Binary:        "tofu",
		WorkdirRoot:   filepath.Dir(dir),
		CredentialEnv: []string{"VAULT_TOKEN"},
		Timeout:       time.Minute,
	})
	tb.runner = fr
	d := desired("tofu")
	d.Target = "tofu.workspace/" + filepath.Base(dir)
	return tb, d
}

func mockCred() Credential {
	return Credential{ID: "mock:prod:write", Token: "SECRET-TOKEN-write", NotAfter: nowFunc().Add(time.Hour), Scheme: "mock"}
}

func TestTofuRejectsLocalState(t *testing.T) {
	for _, bt := range []string{"local", "", "absent"} {
		dir := tofuWorkspace(t, bt)
		tb, d := newTofuFor(t, dir, &fakeRunner{planExit: 2})
		_, err := tb.Plan(context.Background(), d, mockCred())
		if !errors.Is(err, ErrStateUnlocked) {
			t.Fatalf("backend %q: plan must refuse non-remote/unlocked state, got %v", bt, err)
		}
	}
}

func TestTofuPlanExitCodes(t *testing.T) {
	dir := tofuWorkspace(t, "s3")
	// exit 0 => idempotent noop (empty diff)
	tb, d := newTofuFor(t, dir, &fakeRunner{planExit: 0})
	p, err := tb.Plan(context.Background(), d, mockCred())
	if err != nil || !p.Diff.Empty() {
		t.Fatalf("exit 0 must be a noop, got diff=%+v err=%v", p.Diff, err)
	}
	// exit 1 => error
	tb, d = newTofuFor(t, dir, &fakeRunner{planExit: 1})
	if _, err := tb.Plan(context.Background(), d, mockCred()); err == nil {
		t.Fatalf("exit 1 must be an error")
	}
	// exit 2 => diff parsed from show -json
	fr := &fakeRunner{planExit: 2, showJSON: `{"resource_changes":[
		{"address":"docker_container.bot","type":"docker_container","change":{"actions":["create"]}},
		{"address":"docker_volume.old","type":"docker_volume","change":{"actions":["delete"]}},
		{"address":"docker_network.n","type":"docker_network","change":{"actions":["update"]}},
		{"address":"docker_image.i","type":"docker_image","change":{"actions":["delete","create"]}},
		{"address":"docker_noop.x","type":"docker_noop","change":{"actions":["no-op"]}}
	]}`}
	tb, d = newTofuFor(t, dir, fr)
	p, err = tb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("exit 2 plan err = %v", err)
	}
	if len(p.Diff.Creates) != 1 || len(p.Diff.Deletes) != 1 || len(p.Diff.Updates) != 2 {
		t.Fatalf("diff parse wrong: creates=%d deletes=%d updates=%d", len(p.Diff.Creates), len(p.Diff.Deletes), len(p.Diff.Updates))
	}
	if p.Diff.BlastRadius != BlastDestructive {
		t.Fatalf("a delete + replace must make the diff Destructive, got %v", p.Diff.BlastRadius)
	}
	// the replace must be marked Destructive
	var sawReplace bool
	for _, it := range p.Diff.Updates {
		if it.Action == "replace" {
			sawReplace = true
			if !it.Destructive {
				t.Fatalf("a replace must be Destructive")
			}
		}
	}
	if !sawReplace {
		t.Fatalf("the [delete,create] change must map to a replace")
	}
}

func TestTofuObserveRefreshOnly(t *testing.T) {
	dir := tofuWorkspace(t, "gcs")
	tb, d := newTofuFor(t, dir, &fakeRunner{refreshExit: 0})
	rs, err := tb.Observe(context.Background(), d, mockCred())
	if err != nil || !rs.InSync {
		t.Fatalf("refresh exit 0 must be in-sync, got %+v err=%v", rs, err)
	}
	tb, d = newTofuFor(t, dir, &fakeRunner{refreshExit: 2})
	rs, err = tb.Observe(context.Background(), d, mockCred())
	if err != nil || rs.InSync || len(rs.Drift) == 0 {
		t.Fatalf("refresh exit 2 must report drift, got %+v err=%v", rs, err)
	}
}

func TestTofuCredentialNeverInArgv(t *testing.T) {
	dir := tofuWorkspace(t, "s3")
	fr := &fakeRunner{planExit: 2, applyExit: 0, showJSON: `{"resource_changes":[{"address":"docker_container.bot","type":"docker_container","change":{"actions":["create"]}}]}`}
	tb, d := newTofuFor(t, dir, fr)
	cred := mockCred()
	p, err := tb.Plan(context.Background(), d, cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tb.Apply(context.Background(), p, cred); err != nil {
		t.Fatal(err)
	}
	// The token must NEVER appear in argv (it would be visible in `ps`); it lives in env.
	for _, call := range fr.calls {
		for _, a := range call {
			if strings.Contains(a, cred.Token) {
				t.Fatalf("credential material leaked into argv: %v", call)
			}
		}
	}
	// The token IS injected into the child env under the configured variable.
	var injected bool
	for _, env := range fr.envs {
		for _, kv := range env {
			if kv == "VAULT_TOKEN="+cred.Token {
				injected = true
			}
		}
	}
	if !injected {
		t.Fatalf("the attested credential must be injected into the child env (VAULT_TOKEN)")
	}
}

func TestTofuChildEnvRequiresCredentialMapping(t *testing.T) {
	dir := tofuWorkspace(t, "s3")
	tb := NewTofuBackend(TofuConfig{Binary: "tofu", WorkdirRoot: filepath.Dir(dir)}) // no CredentialEnv, no AllowAmbient
	tb.runner = &fakeRunner{planExit: 2}
	d := desired("tofu")
	d.Target = "tofu.workspace/" + filepath.Base(dir)
	_, err := tb.Plan(context.Background(), d, mockCred())
	if err == nil || !strings.Contains(err.Error(), "CredentialEnv") {
		t.Fatalf("without a credential mapping (and no AllowAmbientCreds) the backend must refuse, got %v", err)
	}
}

// TestTofuDeleteBlockedByGateE2E wires the tofu backend behind the Executor and
// proves a plan that deletes is blocked by the blast-radius gate before apply.
func TestTofuDeleteBlockedByGateE2E(t *testing.T) {
	dir := tofuWorkspace(t, "s3")
	fr := &fakeRunner{planExit: 2, applyExit: 0, showJSON: `{"resource_changes":[{"address":"docker_container.bot","type":"docker_container","change":{"actions":["delete"]}}]}`}
	tb, d := newTofuFor(t, dir, fr)
	e := New(WithBackend(tb, "tofu"), WithCredentialSource(mockCredSource(time.Hour)))
	_, err := e.Apply(context.Background(), d)
	if !errors.Is(err, ErrBlastRadius) {
		t.Fatalf("a tofu plan that deletes must be blocked by the blast-radius gate, got %v", err)
	}
	// apply must never have been reached
	for _, call := range fr.calls {
		if len(call) > 1 && call[1] == "apply" {
			t.Fatalf("a gate-blocked plan must never reach apply")
		}
	}
}

func TestTofuDestroyPlanAndRetire(t *testing.T) {
	dir := tofuWorkspace(t, "s3")
	fr := &fakeRunner{destroyExit: 2, applyExit: 0, showJSON: `{"resource_changes":[{"address":"docker_container.bot","type":"docker_container","change":{"actions":["delete"]}}]}`}
	tb, d := newTofuFor(t, dir, fr)
	p, err := tb.DestroyPlan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("destroy plan err = %v", err)
	}
	if p.Intent != IntentDestroy {
		t.Fatalf("destroy plan intent = %v, want IntentDestroy", p.Intent)
	}
	if len(p.Diff.Deletes) != 1 || p.Diff.BlastRadius != BlastDestructive {
		t.Fatalf("destroy plan must carry 1 destructive delete, got %+v", p.Diff)
	}
	// Through the Executor: retire is a deliberate, allowed teardown by default policy.
	e := New(WithBackend(tb, "tofu"), WithCredentialSource(mockCredSource(time.Hour)))
	if _, err := e.Retire(context.Background(), d); err != nil {
		t.Fatalf("governed retire (teardown) should succeed, got %v", err)
	}
	// And it must have reached apply (of the destroy plan).
	var applied bool
	for _, call := range fr.calls {
		if len(call) > 1 && call[1] == "apply" {
			applied = true
		}
	}
	if !applied {
		t.Fatalf("retire must apply the destroy plan")
	}
}

func TestTofuObserveUnlockedStateIsHonestGap(t *testing.T) {
	dir := tofuWorkspace(t, "local") // local backend => not remote/locked
	tb, d := newTofuFor(t, dir, &fakeRunner{refreshExit: 0})
	rs, err := tb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatalf("observe of an unlocked-state workspace must not error (honest gap), got %v", err)
	}
	if rs.Observable {
		t.Fatalf("observe of a non-remote/unlocked workspace must report Observable=false (gap), got %+v", rs)
	}
}
