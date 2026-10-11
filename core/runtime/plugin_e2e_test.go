// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build e2e

// This file holds the heavyweight out-of-process plugin test. It builds the
// reference source connector into a real plugin binary, launches it as a
// separate process and verifies the full go-plugin path: handshake, streaming
// Gather over gRPC, and process teardown on Stop. It is gated behind `-tags e2e`
// so the default `task test` stays hermetic and fast; the fast contract test for
// the gRPC adapters lives in sdk/plugin (TestPluginGRPCConn). Run it with:
//
//	go test -tags e2e -run TestOutOfProcess ./core/runtime/...
package runtime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

func TestOutOfProcessSourcePlugin(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping out-of-process plugin build")
	}

	// Build the example-source connector into a plugin binary.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	// execCapableDir y no t.TempDir(): este caso COMPILA un binario y lo lanza fuera de proceso,
	// asi que necesita un directorio donde de verdad se pueda ejecutar. Con t.TempDir() en un
	// montaje noexec el fallo sale como «el loader no arranco el plugin» — acusando al codigo por
	// una propiedad de la maquina. Es el mismo defecto que loader_test.go tenia, medido el
	// 2026-08-19 en ci-runner-8.
	bin := filepath.Join(execCapableDir(t), "example-source")
	build := exec.Command("go", "build", "-o", bin, "github.com/olivaresai/olivares/connectors/example/cmd/example-source")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}

	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "counter", got: make(chan event.Event, 16)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	// Load the connector out-of-process; it emits 4 edges over gRPC.
	if err := rt.LoadSourcePlugin(bin, sdk.Config{Settings: map[string]string{"count": "4"}}, "tenant-oop"); err != nil {
		t.Fatalf("load plugin: %v", err)
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = rt.Stop(ctx) // also kills the plugin process
	})

	for i := 0; i < 4; i++ {
		select {
		case e := <-mod.got:
			if e.Source != "olivares.example-source" || e.Tenant != "tenant-oop" {
				t.Errorf("unexpected event from plugin: %+v", e)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("did not receive edge %d from out-of-process plugin", i)
		}
	}
}

// TestOneSourcePluginBinaryBacksTwoNamedSources is the out-of-process half of the
// "several sources of one kind" acceptance: the SAME first-party plugin binary is
// loaded TWICE under two registration names.
//
// The evidence is deliberately layered, because "two sources" is easy to fake and
// this test exists to refuse the fake:
//
//   - TWO PROCESSES. Each load forks its own subprocess; both are counted as
//     children of the test process, and removing one live leaves exactly one.
//   - TWO CONFIGURATIONS. Each subprocess holds its own settings in its own
//     memory, so the two emit edges against DIFFERENT resources. One shared
//     instance could not report both.
//   - TWO PROVENANCES. The events carry the registration names, not the plugin's
//     shared Descriptor, and their own tenants.
//
// The admission posture is unchanged and re-checked here: a wrong pinned digest
// still refuses, wires nothing, and leaves no subprocess behind.
func TestOneSourcePluginBinaryBacksTwoNamedSources(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("the subprocess count is read from /proc; the rest is covered on every platform by the in-process tests")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping out-of-process plugin build")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	bin := filepath.Join(execCapableDir(t), "example-source")
	build := exec.Command("go", "build", "-o", bin, "github.com/olivaresai/olivares/connectors/example/cmd/example-source")
	build.Dir = repoRoot
	if out, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("build plugin: %v\n%s", berr, out)
	}
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])

	before := countPluginChildren(t, "example-source")

	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "counter", got: make(chan event.Event, 32)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}

	// First-party path, named. Two loads of ONE binary, two configurations.
	if err := rt.LoadSourcePluginNamed("plugin-home-a", bin,
		sdk.Config{Settings: map[string]string{"count": "2", "resource": "home.a"}}, "tenant-a"); err != nil {
		t.Fatalf("load A: %v", err)
	}
	if err := rt.LoadSourcePluginNamed("plugin-home-b", bin,
		sdk.Config{Settings: map[string]string{"count": "2", "resource": "home.b"}}, "tenant-b"); err != nil {
		t.Fatalf("load B (a second source from the same binary must be accepted): %v", err)
	}

	// Verified (checksum-pinned) path, named: a WRONG digest refuses, wires
	// nothing and leaves no child behind.
	wrong := strings.Repeat("ab", sha256.Size)
	if err := rt.LoadSourcePluginVerifiedNamed("plugin-home-c", bin, sdk.Config{}, "tenant-c", wrong); err == nil {
		t.Fatal("a mismatched pinned digest must refuse the launch")
	}
	// And the CORRECT digest admits a third named source from the same binary.
	if err := rt.LoadSourcePluginVerifiedNamed("plugin-home-c", bin,
		sdk.Config{Settings: map[string]string{"count": "2", "resource": "home.c"}}, "tenant-c", digest); err != nil {
		t.Fatalf("load C with the correct digest: %v", err)
	}

	inv := map[string]string{}
	for _, s := range rt.LiveSourceInventory() {
		inv[s.Name] = s.Component
	}
	if len(inv) != 3 {
		t.Fatalf("three named plugin sources expected, got %v", inv)
	}
	for _, name := range []string{"plugin-home-a", "plugin-home-b", "plugin-home-c"} {
		if inv[name] != "olivares.example-source" {
			t.Errorf("%s must report the plugin's own descriptor, got %q", name, inv[name])
		}
	}

	if got := countPluginChildren(t, "example-source") - before; got != 3 {
		t.Fatalf("%d plugin subprocesses running, want 3 (one per registration)", got)
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	// Each subprocess reports ITS OWN resource under ITS OWN registration name.
	seen := map[string]map[string]bool{}
	tenants := map[string]string{}
	for i := 0; i < 6; i++ {
		select {
		case e := <-mod.got:
			edge, ok := event.EdgeOf(e)
			if !ok {
				t.Fatalf("not an edge event: %+v", e)
			}
			if seen[e.Source] == nil {
				seen[e.Source] = map[string]bool{}
			}
			seen[e.Source][edge.ResourceRef] = true
			tenants[e.Source] = e.Tenant
		case <-time.After(20 * time.Second):
			t.Fatalf("only %d of 6 edges arrived; have %v", i, seen)
		}
	}
	for name, wantResource := range map[string]string{
		"plugin-home-a": "home.a", "plugin-home-b": "home.b", "plugin-home-c": "home.c",
	} {
		got := seen[name]
		if !got[wantResource] {
			t.Errorf("%s did not report its own resource %q; saw %v", name, wantResource, got)
		}
		if len(got) != 1 {
			t.Errorf("%s reported more than its own resource: %v", name, got)
		}
	}
	for name, wantTenant := range map[string]string{
		"plugin-home-a": "tenant-a", "plugin-home-b": "tenant-b", "plugin-home-c": "tenant-c",
	} {
		if tenants[name] != wantTenant {
			t.Errorf("%s carried tenant %q, want %q", name, tenants[name], wantTenant)
		}
	}

	// A live remove kills exactly one subprocess and leaves the siblings alone.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rt.RemoveSourceLive(ctx, "plugin-home-a"); err != nil {
		t.Fatalf("remove A: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var running int
	for time.Now().Before(deadline) {
		running = countPluginChildren(t, "example-source") - before
		if running == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if running != 2 {
		t.Fatalf("after removing one source, %d plugin subprocesses remain, want 2", running)
	}
	live := map[string]runtime.Status{}
	for _, s := range rt.LiveSourceInventory() {
		live[s.Name] = s.Status
	}
	if _, present := live["plugin-home-a"]; present {
		t.Error("the removed plugin source is still registered")
	}
	if _, present := live["plugin-home-b"]; !present {
		t.Errorf("removing A unregistered B: %v", live)
	}
	if _, present := live["plugin-home-c"]; !present {
		t.Errorf("removing A unregistered C: %v", live)
	}
}

// TestMalformedPluginRegistrationIsRefusedAndItsSubprocessReaped is the
// out-of-process half of the descriptor regression, and the only place the CLEANUP
// half can be measured at all.
//
// The fixture (core/runtime/testdata/emptydescsource) is a REAL plugin: it
// handshakes, serves over gRPC and dispenses a connector. What it does not do is
// name itself. So the host learns the component is malformed only AFTER a
// subprocess is already running — which is exactly why "refused" is not enough on
// this path. Three things must hold together:
//
//   - the historical refusal, verbatim;
//   - nothing wired, under any name (the registration name is not consumed either);
//   - the subprocess REAPED, with its confinement released — measured by the child
//     count returning to where it started, not by trusting the code path.
func TestMalformedPluginRegistrationIsRefusedAndItsSubprocessReaped(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("the subprocess count is read from /proc; the refusal itself is covered on every platform in named_sources_test.go")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping out-of-process plugin build")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	bin := filepath.Join(execCapableDir(t), "emptydescsource")
	build := exec.Command("go", "build", "-o", bin, "github.com/olivaresai/olivares/core/runtime/testdata/emptydescsource")
	build.Dir = repoRoot
	if out, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("build the nameless-descriptor fixture: %v\n%s", berr, out)
	}
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)

	rt := runtime.New(runtime.Options{Logger: quiet()})
	before := countPluginChildren(t, "emptydescsource")

	const wantErr = "runtime: component descriptor has empty Name"
	cases := []struct {
		what string
		call func() error
	}{
		{"LoadSourcePluginNamed", func() error {
			return rt.LoadSourcePluginNamed("plugin-slot", bin, sdk.Config{}, "acme")
		}},
		{"LoadSourcePluginVerifiedNamed", func() error {
			return rt.LoadSourcePluginVerifiedNamed("plugin-slot", bin, sdk.Config{}, "acme", hex.EncodeToString(sum[:]))
		}},
		{"LoadSourcePlugin (legacy)", func() error {
			return rt.LoadSourcePlugin(bin, sdk.Config{}, "acme")
		}},
	}
	for _, tc := range cases {
		err := tc.call()
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("%s = %v, want %q", tc.what, err, wantErr)
		}
		if inv := rt.LiveSourceInventory(); len(inv) != 0 {
			t.Fatalf("%s wired a malformed plugin: %+v", tc.what, inv)
		}
		// The subprocess must be gone. go-plugin's Kill is synchronous, but the
		// kernel reaps on its own schedule, so allow a bounded settle.
		deadline := time.Now().Add(10 * time.Second)
		var running int
		for time.Now().Before(deadline) {
			running = countPluginChildren(t, "emptydescsource") - before
			if running == 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if running != 0 {
			t.Fatalf("%s left %d plugin subprocess(es) behind after refusing the registration", tc.what, running)
		}
	}

	// The refusals consumed nothing: the registration name is still free, and a
	// well-formed plugin takes it.
	good := filepath.Join(execCapableDir(t), "example-source")
	gb := exec.Command("go", "build", "-o", good, "github.com/olivaresai/olivares/connectors/example/cmd/example-source")
	gb.Dir = repoRoot
	if out, berr := gb.CombinedOutput(); berr != nil {
		t.Fatalf("build the well-formed plugin: %v\n%s", berr, out)
	}
	controlBefore := countPluginChildren(t, "example-source")
	if err := rt.LoadSourcePluginNamed("plugin-slot", good, sdk.Config{Settings: map[string]string{"count": "1"}}, "acme"); err != nil {
		t.Fatalf("the refused plugin reserved %q anyway: %v", "plugin-slot", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Stop only disposes a started runtime. Ensure cleanup also works if
		// an assertion fails before the explicit lifecycle below.
		_ = rt.Start(ctx)
		_ = rt.Stop(ctx)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("start the successful control: %v", err)
	}
	inv := rt.LiveSourceInventory()
	if len(inv) != 1 || inv[0].Name != "plugin-slot" || inv[0].Component != "olivares.example-source" {
		t.Fatalf("inventory after the good load = %+v", inv)
	}
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("stop the successful control: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for countPluginChildren(t, "example-source") != controlBefore {
		if time.Now().After(deadline) {
			t.Fatal("successful control left a plugin subprocess behind after Stop")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestKilledSourcePluginIsRestarted: a plugin source whose process dies while
// Gather runs is relaunched by the runtime and runs again in a NEW process. It is
// measured on the three ways a plugin source is loaded: first-party at boot,
// checksum-pinned external at boot (the pin is checked again on every relaunch),
// and live-added through a prepared source, the last both one-shot and polling.
func TestKilledSourcePluginIsRestarted(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("the plugin processes are found through /proc")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping out-of-process plugin build")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	bin := filepath.Join(execCapableDir(t), "streamsource")
	build := exec.Command("go", "build", "-o", bin, "github.com/olivaresai/olivares/core/runtime/testdata/streamsource")
	build.Dir = repoRoot
	if out, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("build the streaming fixture: %v\n%s", berr, out)
	}
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)

	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "counter", got: make(chan event.Event, 32)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.LoadSourcePluginNamed("stream-boot", bin, sdk.Config{}, "tenant-s"); err != nil {
		t.Fatalf("load first-party: %v", err)
	}
	if err := rt.LoadSourcePluginVerifiedNamed("stream-pinned", bin, sdk.Config{}, "tenant-s", hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("load pinned: %v", err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	prepared, err := rt.PrepareSourcePlugin(bin)
	if err != nil {
		t.Fatalf("prepare live: %v", err)
	}
	if err := rt.AddPreparedSourceNamed(context.Background(), "stream-live", prepared, sdk.Config{}, "tenant-s", 0); err != nil {
		t.Fatalf("add live: %v", err)
	}
	// A polling source takes the other branch of the gather loop.
	polling, err := rt.PrepareSourcePlugin(bin)
	if err != nil {
		t.Fatalf("prepare polling: %v", err)
	}
	if err := rt.AddPreparedSourceNamed(context.Background(), "stream-poll", polling, sdk.Config{}, "tenant-s", time.Hour); err != nil {
		t.Fatalf("add polling: %v", err)
	}

	names := []string{"stream-boot", "stream-pinned", "stream-live", "stream-poll"}
	first := processPerSource(t, mod.got, names)

	for _, pid := range pluginChildren(t, "streamsource") {
		if kerr := syscall.Kill(pid, syscall.SIGKILL); kerr != nil {
			t.Skipf("cannot signal the plugin process %d (it may run under a dedicated uid): %v", pid, kerr)
		}
	}

	second := processPerSource(t, mod.got, names)
	for _, name := range names {
		if second[name] == first[name] {
			t.Errorf("%s reported %s again; want a new plugin process after the kill", name, first[name])
		}
	}
	if n := countPluginChildren(t, "streamsource"); n != len(names) {
		t.Errorf("%d plugin processes running after the restart, want %d", n, len(names))
	}
	status := map[string]runtime.Status{}
	for _, cs := range rt.Status() {
		status[cs.Name] = cs.Status
	}
	for _, name := range names {
		if status[name] != runtime.StatusRunning {
			t.Errorf("%s status %q after the restart, want %q", name, status[name], runtime.StatusRunning)
		}
	}
}

// TestFailingGatherOfLivePluginIsNotRestarted: when the plugin process is alive and
// its Gather fails, the failure is the connector's own. The source is left failed,
// as before supervision, and the process is neither killed nor started again.
func TestFailingGatherOfLivePluginIsNotRestarted(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("the plugin processes are found through /proc")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping out-of-process plugin build")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	bin := filepath.Join(execCapableDir(t), "streamsource")
	build := exec.Command("go", "build", "-o", bin, "github.com/olivaresai/olivares/core/runtime/testdata/streamsource")
	build.Dir = repoRoot
	if out, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("build the streaming fixture: %v\n%s", berr, out)
	}

	rt := runtime.New(runtime.Options{Logger: quiet()})
	if err := rt.LoadSourcePluginNamed("stream-fails", bin, sdk.Config{Settings: map[string]string{"fail": "1"}}, "tenant-s"); err != nil {
		t.Fatalf("load: %v", err)
	}
	before := pluginChildren(t, "streamsource")
	if len(before) != 1 {
		t.Fatalf("%d plugin processes after the load, want 1", len(before))
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	// Three times the first restart delay: a supervisor that mistook the failure
	// for a crash would have replaced the process by now.
	time.Sleep(3 * time.Second)
	var status runtime.Status
	for _, cs := range rt.Status() {
		if cs.Name == "stream-fails" {
			status = cs.Status
		}
	}
	if status != runtime.StatusFailed {
		t.Errorf("status %q, want %q", status, runtime.StatusFailed)
	}
	if after := pluginChildren(t, "streamsource"); len(after) != 1 || after[0] != before[0] {
		t.Errorf("plugin processes %v after a connector failure, want the original %v", after, before)
	}
}

// TestPinnedPluginIsNotRestartedFromAChangedBinary: the relaunch of an external
// plugin checks its pinned digest again. When the binary on disk changed after
// admission, the restart is refused, the source reports the refusal, and no process
// runs; Stop during the retries leaves nothing behind.
func TestPinnedPluginIsNotRestartedFromAChangedBinary(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("the plugin processes are found through /proc")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping out-of-process plugin build")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	dir := execCapableDir(t)
	bin := filepath.Join(dir, "streamsource")
	build := exec.Command("go", "build", "-o", bin, "github.com/olivaresai/olivares/core/runtime/testdata/streamsource")
	build.Dir = repoRoot
	if out, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("build the streaming fixture: %v\n%s", berr, out)
	}
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)

	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "counter", got: make(chan event.Event, 8)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.LoadSourcePluginVerifiedNamed("stream-pinned", bin, sdk.Config{}, "tenant-s", hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("load pinned: %v", err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = rt.Stop(ctx)
		}
	})
	processPerSource(t, mod.got, []string{"stream-pinned"})

	// The same program with one trailing byte: it would still run, so only the pin
	// can refuse it. Renamed into place, so the running process keeps its file.
	changed := filepath.Join(dir, "streamsource.changed")
	if err := os.WriteFile(changed, append(raw, 0), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(changed, bin); err != nil {
		t.Fatal(err)
	}
	for _, pid := range pluginChildren(t, "streamsource") {
		if kerr := syscall.Kill(pid, syscall.SIGKILL); kerr != nil {
			t.Skipf("cannot signal the plugin process %d (it may run under a dedicated uid): %v", pid, kerr)
		}
	}

	var last runtime.ComponentStatus
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		for _, cs := range rt.Status() {
			if cs.Name == "stream-pinned" {
				last = cs
			}
		}
		if last.Status == runtime.StatusFailed && strings.Contains(last.Err, "restart") {
			break
		}
	}
	if last.Status != runtime.StatusFailed || !strings.Contains(last.Err, "checksum") {
		t.Fatalf("status %q (%s) after relaunching a changed binary, want failed with the checksum refusal", last.Status, last.Err)
	}
	if n := countPluginChildren(t, "streamsource"); n != 0 {
		t.Errorf("%d plugin processes running from a binary that no longer matches its pin, want 0", n)
	}
	select {
	case e := <-mod.got:
		t.Errorf("the source emitted after its binary changed: %+v", e)
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	begun := time.Now()
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	stopped = true
	if took := time.Since(begun); took > 5*time.Second {
		t.Errorf("Stop took %v while the restart was backing off, want it to return promptly", took)
	}
	if n := countPluginChildren(t, "streamsource"); n != 0 {
		t.Errorf("%d plugin processes left after Stop, want 0", n)
	}
}

// TestCrashLoopingPluginBacksOff: a plugin that dies right after every start is
// restarted ever more slowly (1 s, 2 s, 4 s ...), not once a second forever.
func TestCrashLoopingPluginBacksOff(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("out-of-process plugin test")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping out-of-process plugin build")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	bin := filepath.Join(execCapableDir(t), "streamsource")
	build := exec.Command("go", "build", "-o", bin, "github.com/olivaresai/olivares/core/runtime/testdata/streamsource")
	build.Dir = repoRoot
	if out, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("build the streaming fixture: %v\n%s", berr, out)
	}

	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "counter", got: make(chan event.Event, 32)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.LoadSourcePluginNamed("stream-crash", bin, sdk.Config{Settings: map[string]string{"crash": "1"}}, "tenant-s"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	// Every start emits one edge. Waits of 1 s and 2 s (each +-20%) fit in 5 s and a
	// third of 4 s does not, so at most 3 starts; a wait that reset after each
	// successful restart would give about 5.
	starts := 0
	for deadline := time.After(5 * time.Second); ; {
		select {
		case <-mod.got:
			starts++
			continue
		case <-deadline:
		}
		break
	}
	if starts < 2 {
		t.Fatalf("%d starts in 5 s: the crashed plugin was not restarted", starts)
	}
	if starts > 3 {
		t.Errorf("%d starts in 5 s: the restart wait does not grow while the plugin keeps dying", starts)
	}
}
