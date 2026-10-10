// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

func TestNativeStatusSharesRepeatedReadsAndTracksLoginChanges(t *testing.T) {
	for _, driver := range []string{"claude", "codex"} {
		t.Run(driver, func(t *testing.T) {
			var m *Module
			call, home := newSignInServer(t, func(module *Module, _ string) { m = module })
			tenant := model.TenantID(filepath.Base(home))
			var starts atomic.Int32
			previous := toolCommand
			toolCommand = func(ctx context.Context, program string, args ...string) *exec.Cmd {
				starts.Add(1)
				return exec.CommandContext(ctx, program, args...)
			}
			t.Cleanup(func() { toolCommand = previous })
			check := func(want bool, count int32) {
				t.Helper()
				for range 3 {
					installed, signedIn, err := m.LoginStatus(t.Context(), tenant, driver)
					if err != nil || !installed || signedIn != want {
						t.Fatalf("status = %v %v %v", installed, signedIn, err)
					}
				}
				if code, status := call("GET", "/v1/m/agenttools/sign-in?driver="+driver, nil); code != 200 || status["signed_in"] != want {
					t.Fatalf("API status = %d %v", code, status)
				}
				if got := starts.Load(); got != count {
					t.Errorf("native status starts = %d, want %d", got, count)
				}
			}
			check(false, 1)
			file := filepath.Join(home, driver, "."+driver, "auth.json")
			if driver == "claude" {
				file = filepath.Join(home, driver, ".claude", ".credentials.json")
			}
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			check(true, 2)
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			check(false, 3)
		})
	}
}

func TestOpenCodeStatusSharesConcurrentAndRepeatedReads(t *testing.T) {
	var m *Module
	call, home := newSignInServer(t, func(module *Module, _ string) { m = module })
	tenant := model.TenantID(filepath.Base(home))
	var starts atomic.Int32
	previous := toolCommand
	toolCommand = func(ctx context.Context, program string, args ...string) *exec.Cmd {
		starts.Add(1)
		return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 0.1; printf 'OpenAI oauth\\n'")
	}
	t.Cleanup(func() { toolCommand = previous })
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			installed, signedIn, err := m.LoginStatus(t.Context(), tenant, "opencode")
			if err != nil || !installed || !signedIn {
				t.Errorf("status = %v, %v, %v", installed, signedIn, err)
			}
		})
	}
	wg.Wait()
	// The API and the profile resolver must share the same native result.
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=opencode", nil); code != 200 || st["signed_in"] != true {
		t.Fatalf("API status = %d %v", code, st)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("native status starts = %d, want 1 for concurrent resolver and API reads", got)
	}
}

func TestOpenCodeStatusTimeoutIsAnError(t *testing.T) {
	var m *Module
	_, home := newSignInServer(t, func(module *Module, _ string) { m = module })
	previous := toolCommand
	toolCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sleep", "30")
	}
	t.Cleanup(func() { toolCommand = previous })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, signedIn, err := m.LoginStatus(ctx, model.TenantID(filepath.Base(home)), "opencode")
	if !errors.Is(err, context.DeadlineExceeded) || signedIn {
		t.Fatalf("timed out status = signed_in:%v err:%v, want deadline error", signedIn, err)
	}
}

func TestOpenCodeStatusBoundsInheritedOutputPipes(t *testing.T) {
	call, _ := newSignInServer(t)
	previous := toolCommand
	toolCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 2 & printf 'OpenAI oauth\\n'")
	}
	t.Cleanup(func() { toolCommand = previous })
	start := time.Now()
	code, st := call("GET", "/v1/m/agenttools/sign-in?driver=opencode", nil)
	if code != 503 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "timed out") || time.Since(start) > time.Second {
		t.Fatalf("inherited pipe = %d %v elapsed:%v, want bounded failure", code, st, time.Since(start))
	}
}

func TestOpenCodeStatusCanceledWaiterDoesNotCancelSharedRead(t *testing.T) {
	var m *Module
	_, home := newSignInServer(t, func(module *Module, _ string) { m = module })
	tenant := model.TenantID(filepath.Base(home))
	previous := toolCommand
	started, release := make(chan struct{}, 2), make(chan struct{})
	toolCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'OpenAI oauth\\n'")
	}
	t.Cleanup(func() { toolCommand = previous })
	finished := make(chan error, 1)
	go func() {
		_, signedIn, err := m.LoginStatus(t.Context(), tenant, "opencode")
		if err == nil && !signedIn {
			err = errors.New("lost the signed-in result")
		}
		finished <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, _, err := m.LoginStatus(ctx, tenant, "opencode")
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiter error = %v", err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if len(started) != 0 {
		t.Fatal("waiter started another native command")
	}
}

func TestOpenCodeSignInCompletionInvalidatesStatus(t *testing.T) {
	call, _ := newSignInServer(t, func(_ *Module, bin string) {
		// Deliberately leave auth.json unchanged: completion itself must force a
		// native status read, even when the tool uses an OS credential store.
		script := strings.ReplaceAll(stubOpenCode, "$XDG_DATA_HOME/opencode/auth.json", "$HOME/login-state")
		if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	})
	const path = "/v1/m/agenttools/sign-in?driver=opencode"
	if code, st := call("GET", path, nil); code != 200 || st["signed_in"] != false {
		t.Fatalf("fresh status = %d %v", code, st)
	}
	code, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "opencode"})
	if code != 202 {
		t.Fatalf("login start = %d %v", code, flow)
	}
	pollSignIn(t, call, flow["id"].(string), "signed_in")
	if code, st := call("GET", path, nil); code != 200 || st["signed_in"] != true {
		t.Fatalf("completed status = %d %v", code, st)
	}
}

func TestOpenCodeInstallAttemptInvalidatesStatus(t *testing.T) {
	var m *Module
	_, home := newSignInServer(t, func(module *Module, _ string) { m = module })
	tenant := model.TenantID(filepath.Base(home))
	var starts atomic.Int32
	previous := toolCommand
	toolCommand = func(ctx context.Context, program string, args ...string) *exec.Cmd {
		starts.Add(1)
		return exec.CommandContext(ctx, program, args...)
	}
	t.Cleanup(func() { toolCommand = previous })
	for range 2 {
		if _, _, err := m.LoginStatus(t.Context(), tenant, "opencode"); err != nil {
			t.Fatal(err)
		}
	}
	if starts.Load() != 1 {
		t.Fatal("the pre-install status was not cached")
	}
	// Even a refused installation invalidates the status; a partial installation
	// may have changed the executable before reporting its failure.
	j := &savedJob{Job: Job{ID: model.NewID(), Driver: "opencode"}, Tenant: tenant}
	m.wg.Add(1)
	m.run(j, &Plan{Driver: "opencode", v1: &toolinstall.Plan{}}, api.ModuleContext{})
	if j.State != "failed" {
		t.Fatalf("empty catalog install = %s, want refused", j.State)
	}
	if _, _, err := m.LoginStatus(t.Context(), tenant, "opencode"); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 2 {
		t.Fatal("install attempt reused the old status")
	}
}

func TestOpenCodeCachedStatusStillAuthorizesTheHome(t *testing.T) {
	var m *Module
	_, home := newSignInServer(t, func(module *Module, _ string) { m = module })
	tenant := model.TenantID(filepath.Base(home))
	allowed := true
	resolve := m.loginHome
	m.SetLoginHome(func(ctx context.Context, tenant model.TenantID, driver, ref string) (string, string, error) {
		if !allowed {
			return "", "", errors.New("account no longer available")
		}
		return resolve(ctx, tenant, driver, ref)
	})
	if _, _, err := m.LoginStatus(t.Context(), tenant, "opencode"); err != nil {
		t.Fatal(err)
	}
	allowed = false
	if _, _, err := m.LoginStatus(t.Context(), tenant, "opencode"); err == nil {
		t.Fatal("cached status bypassed the revoked home")
	}
}

type statusWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *statusWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestOpenCodeStatusSurvivesInitiatingCallerCancellation(t *testing.T) {
	var m *Module
	_, home := newSignInServer(t, func(module *Module, _ string) { m = module })
	tenant := model.TenantID(filepath.Base(home))
	var starts atomic.Int32
	started := make(chan struct{})
	previous := toolCommand
	toolCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		if starts.Add(1) == 1 {
			close(started)
			return exec.CommandContext(ctx, "/bin/sleep", "30")
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'OpenAI oauth\\n'")
	}
	t.Cleanup(func() { toolCommand = previous })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	leader := make(chan error, 1)
	go func() {
		_, _, err := m.LoginStatus(ctx, tenant, "opencode")
		leader <- err
	}()
	<-started
	waiterCtx := &statusWaitContext{Context: t.Context(), waiting: make(chan struct{})}
	waiter := make(chan error, 1)
	go func() {
		_, signedIn, err := m.LoginStatus(waiterCtx, tenant, "opencode")
		if err == nil && !signedIn {
			err = errors.New("lost the signed-in result")
		}
		waiter <- err
	}()
	<-waiterCtx.waiting
	cancel()
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Errorf("leader = %v, want cancellation", err)
	}
	if err := <-waiter; err != nil {
		t.Fatalf("live caller inherited another request's cancellation: %v", err)
	}
	if starts.Load() != 2 {
		t.Fatalf("native starts = %d, want canceled read and replacement", starts.Load())
	}
}

func TestOpenCodeStatusRecoversImmediatelyAfterNativeFailure(t *testing.T) {
	call, _ := newSignInServer(t)
	var starts atomic.Int32
	previous := toolCommand
	toolCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		if starts.Add(1) == 1 {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'private-native-status'; exit 1")
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'OpenAI oauth\\n'")
	}
	t.Cleanup(func() { toolCommand = previous })
	const path = "/v1/m/agenttools/sign-in?driver=opencode"
	code, st := call("GET", path, nil)
	if code != 503 {
		t.Fatalf("failed command = %d %v", code, st)
	}
	message := st["error"].(map[string]any)["message"].(string)
	if strings.Contains(message, "private-native-status") {
		t.Fatal("native output escaped through the API")
	}
	if code, st := call("GET", path, nil); code != 200 || st["signed_in"] != true {
		t.Fatalf("recovered command = %d %v", code, st)
	}
	if starts.Load() != 2 {
		t.Fatalf("native starts = %d, want immediate retry", starts.Load())
	}
}

func TestOpenCodeStatusTracksNativeLoginAndProgramChanges(t *testing.T) {
	var m *Module
	var bin string
	_, home := newSignInServer(t, func(module *Module, dir string) { m, bin = module, dir })
	tenant := model.TenantID(filepath.Base(home))
	var starts atomic.Int32
	previous := toolCommand
	toolCommand = func(ctx context.Context, program string, args ...string) *exec.Cmd {
		starts.Add(1)
		return exec.CommandContext(ctx, program, args...)
	}
	t.Cleanup(func() { toolCommand = previous })
	check := func(wantSignedIn bool, wantStarts int32) {
		t.Helper()
		for range 2 {
			installed, signedIn, err := m.LoginStatus(t.Context(), tenant, "opencode")
			if err != nil || !installed || signedIn != wantSignedIn {
				t.Fatalf("status = %v, %v, %v", installed, signedIn, err)
			}
		}
		if got := starts.Load(); got != wantStarts {
			t.Fatalf("native starts = %d, want %d", got, wantStarts)
		}
	}
	check(false, 1)
	authFile := filepath.Join(home, "opencode", ".local", "share", "opencode", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authFile, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	check(true, 2)
	if err := os.Remove(authFile); err != nil {
		t.Fatal(err)
	}
	check(false, 3)
	// An atomic reinstall can keep both the path and timestamp unchanged.
	program := filepath.Join(bin, "opencode")
	info, err := os.Stat(program)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program+".new", []byte(stubOpenCode), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(program+".new", info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(program+".new", program); err != nil {
		t.Fatal(err)
	}
	check(false, 4)
}
