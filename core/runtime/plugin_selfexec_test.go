// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// These tests run real plugin processes without building anything: the test binary
// copies itself, and TestMain (loader_test.go) serves the fixture the copy's file
// name selects. They run in the default suite, so the supervision they measure is
// checked on every run and not only under -tags e2e.

const (
	streamOutputBinary = "olv-stream-out" // comm is cut to 15 characters
	streamSourceBinary = "olv-stream-src"
)

// streamOutput is an output plugin whose Notify succeeds. With fail=1 it returns an
// error while the process stays alive (the connector's own failure); with crash=1
// the process exits in Notify (a plugin in a crash loop).
type streamOutput struct{ fail, crash bool }

func (*streamOutput) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "olivares.test-output", Type: sdk.TypeOutput, APIVersion: sdk.APIVersion}
}

func (o *streamOutput) Open(_ context.Context, cfg sdk.Config) error {
	o.fail = cfg.Settings["fail"] == "1"
	o.crash = cfg.Settings["crash"] == "1"
	return nil
}

func (o *streamOutput) Notify(context.Context, sdk.Notification) error {
	if o.crash {
		os.Exit(3)
	}
	if o.fail {
		return errors.New("streamoutput: notify failed on request")
	}
	return nil
}

func (*streamOutput) Close(context.Context) error { return nil }

// streamSource is a streaming source plugin: each Gather emits one edge whose resource
// is the plugin's pid, then blocks until canceled, so a test can tell a restarted
// process from the first one.
type streamSource struct{}

func (*streamSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "olivares.test-stream", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (*streamSource) Open(context.Context, sdk.Config) error { return nil }
func (*streamSource) Gather(ctx context.Context, sink sdk.Sink) error {
	if err := sink.Emit(ctx, model.EdgeObservation{
		OriginKind:   "agent",
		OriginRef:    "stream-fixture",
		ResourceKind: "test.process",
		ResourceRef:  "pid-" + strconv.Itoa(os.Getpid()),
		Mode:         model.ModeRead,
		Source:       model.SignalOTEL,
		Confidence:   model.ConfidenceApproximate,
		ObservedAt:   time.Now().UTC(),
	}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}
func (*streamSource) Close(context.Context) error { return nil }

// selfExecPlugin copies this test binary under name into a directory that can run
// programs and returns the copy's path.
func selfExecPlugin(t *testing.T, name string) string {
	t.Helper()
	if goruntime.GOOS != "linux" {
		t.Skip("the plugin processes are found through /proc")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(execCapableDir(t), name)
	if err := os.WriteFile(bin, image, 0o711); err != nil {
		t.Fatal(err)
	}
	return bin
}

func digestOf(t *testing.T, path string) string {
	t.Helper()
	image, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	return hex.EncodeToString(sum[:])
}

// dispenseOutput launches an output plugin first-party (empty digest) or pinned.
func dispenseOutput(rt *runtime.Runtime, bin, digest string) (sdk.OutputConnector, *goplugin.Client, error) {
	if digest == "" {
		return rt.DispenseOutputPlugin(bin)
	}
	return rt.DispenseOutputPluginVerified(bin, digest)
}

// openOutput dispenses, opens and tracks one output plugin the way the notify
// dispatcher does.
func openOutput(t *testing.T, rt *runtime.Runtime, bin, digest string, settings map[string]string) (sdk.OutputConnector, *goplugin.Client) {
	t.Helper()
	conn, client, err := dispenseOutput(rt, bin, digest)
	if err != nil {
		t.Fatalf("dispense: %v", err)
	}
	if err := conn.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("open: %v", err)
	}
	rt.TrackOutputPlugin(conn, client)
	return conn, client
}

// startRuntime starts rt and stops it when the test ends, unless stop ran earlier.
func startRuntime(t *testing.T, rt *runtime.Runtime) (stop func()) {
	t.Helper()
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = rt.Stop(ctx)
		})
	}
	t.Cleanup(stop)
	return stop
}

// killPlugins kills every plugin process called name, as a crash would. A plugin
// plugjail moved to a dedicated uid cannot be signaled from here; the given clients
// then stand in, which ends the same processes.
func killPlugins(t *testing.T, name string, clients ...*goplugin.Client) map[int]bool {
	t.Helper()
	killed := map[int]bool{}
	for _, pid := range pluginChildren(t, name) {
		killed[pid] = true
		if syscall.Kill(pid, syscall.SIGKILL) != nil {
			for _, c := range clients {
				c.Kill()
			}
			break
		}
	}
	if len(killed) == 0 {
		t.Fatalf("no %s process to kill: the plugin did not start, or its process cannot be found", name)
	}
	// A delivery that races ahead of the kill would succeed and prove nothing.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		gone := true
		for _, pid := range pluginChildren(t, name) {
			gone = gone && !killed[pid]
		}
		if gone {
			break
		}
	}
	return killed
}

// deliverUntil calls Notify every 200 ms until it succeeds or 20 s pass.
func deliverUntil(conn sdk.OutputConnector) error {
	var err error
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if err = conn.Notify(context.Background(), sdk.Notification{Type: "finding.reported"}); err == nil {
			return nil
		}
	}
	return err
}

func waitNoChildren(t *testing.T, name, what string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); countPluginChildren(t, name) != 0; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

// countPluginChildren counts THIS process's direct children whose comm matches name.
func countPluginChildren(t *testing.T, name string) int {
	t.Helper()
	return len(pluginChildren(t, name))
}

// pluginChildren lists THIS process's direct children whose comm matches
// name. /proc/<pid>/stat is world-readable, so it works whether or not plugjail
// moved the child to a dedicated uid. comm is truncated to 15 characters by the
// kernel, which is why the caller passes a short name.
func pluginChildren(t *testing.T, name string) []int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("/proc is not readable here, so the process count cannot be measured: %v", err)
	}
	me := os.Getpid()
	var pids []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, cerr := strconv.Atoi(e.Name())
		if cerr != nil {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if rerr != nil {
			continue // the process exited between the listing and the read
		}
		// comm is parenthesized and may contain spaces; the fields after the LAST
		// ')' are state (3) then ppid (4).
		close := strings.LastIndex(string(raw), ")")
		open := strings.Index(string(raw), "(")
		if close < 0 || open < 0 || close <= open {
			continue
		}
		comm := string(raw[open+1 : close])
		rest := strings.Fields(string(raw[close+1:]))
		if len(rest) < 2 {
			continue
		}
		ppid, perr := strconv.Atoi(rest[1])
		if perr != nil || ppid != me || pid == me || rest[0] == "Z" {
			continue // a zombie has exited and only waits to be reaped
		}
		if comm == name {
			pids = append(pids, pid)
		}
	}
	return pids
}

// processPerSource waits until every named source has emitted one fixture edge and
// returns the plugin process each one reported.
func processPerSource(t *testing.T, got <-chan event.Event, names []string) map[string]string {
	t.Helper()
	seen := map[string]string{}
	deadline := time.After(30 * time.Second)
	for len(seen) < len(names) {
		select {
		case e := <-got:
			if edge, ok := event.EdgeOf(e); ok {
				seen[e.Source] = edge.ResourceRef
			}
		case <-deadline:
			t.Fatalf("only %v of %v reported a plugin process", seen, names)
		}
	}
	return seen
}

// TestKilledSourcePluginIsRestartedByTheDefaultSuite: a plugin source whose process
// is killed while Gather runs is started again and reports from a NEW process.
func TestKilledSourcePluginIsRestartedByTheDefaultSuite(t *testing.T) {
	bin := selfExecPlugin(t, streamSourceBinary)
	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "counter", got: make(chan event.Event, 32)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.LoadSourcePluginNamed("stream-boot", bin, sdk.Config{}, "tenant-s"); err != nil {
		t.Fatalf("load: %v", err)
	}
	startRuntime(t, rt)
	names := []string{"stream-boot"}
	first := processPerSource(t, mod.got, names)

	killPlugins(t, streamSourceBinary)
	second := processPerSource(t, mod.got, names)
	if second["stream-boot"] == first["stream-boot"] {
		t.Errorf("the source reported %s again; want a new plugin process after the kill", first["stream-boot"])
	}
}

// TestKilledOutputPluginIsRestarted: a notify destination whose plugin process dies
// is started again, and its deliveries resume in a NEW process. It is measured on the
// two ways a destination plugin is launched: first-party and checksum-pinned
// external (the pin is checked again on every relaunch).
func TestKilledOutputPluginIsRestarted(t *testing.T) {
	bin := selfExecPlugin(t, streamOutputBinary)
	rt := runtime.New(runtime.Options{Logger: quiet()})
	stop := startRuntime(t, rt)
	conns := map[string]sdk.OutputConnector{}
	var clients []*goplugin.Client
	for name, digest := range map[string]string{"first-party": "", "pinned": digestOf(t, bin)} {
		conn, client := openOutput(t, rt, bin, digest, nil)
		conns[name] = conn
		clients = append(clients, client)
		if err := conn.Notify(context.Background(), sdk.Notification{Type: "finding.reported"}); err != nil {
			t.Fatalf("%s: first notify: %v", name, err)
		}
	}
	killed := killPlugins(t, streamOutputBinary, clients...)

	for name, conn := range conns {
		if err := deliverUntil(conn); err != nil {
			t.Fatalf("%s: still failing 20 s after its plugin process was killed: %v", name, err)
		}
	}
	after := pluginChildren(t, streamOutputBinary)
	if len(after) != len(conns) {
		t.Errorf("%d plugin processes after the restart, want %d", len(after), len(conns))
	}
	for _, pid := range after {
		if killed[pid] {
			t.Errorf("plugin process %d is one of the killed ones; want new processes", pid)
		}
	}
	stop()
	waitNoChildren(t, streamOutputBinary, "Stop left an output plugin process behind")
}

// TestFailingNotifyOfLiveOutputPluginIsNotRestarted: when the plugin process is
// alive and its Notify fails, the failure is the connector's own. The error reaches
// the caller and the process is neither killed nor started again.
func TestFailingNotifyOfLiveOutputPluginIsNotRestarted(t *testing.T) {
	bin := selfExecPlugin(t, streamOutputBinary)
	rt := runtime.New(runtime.Options{Logger: quiet()})
	conn, _ := openOutput(t, rt, bin, "", map[string]string{"fail": "1"})
	startRuntime(t, rt)
	before := pluginChildren(t, streamOutputBinary)
	if len(before) != 1 {
		t.Fatalf("%d plugin processes after the load, want 1", len(before))
	}
	for i := 0; i < 3; i++ {
		if err := conn.Notify(context.Background(), sdk.Notification{Type: "finding.reported"}); err == nil {
			t.Fatal("notify succeeded; the fixture was asked to fail")
		}
	}
	if after := pluginChildren(t, streamOutputBinary); len(after) != 1 || after[0] != before[0] {
		t.Errorf("plugin processes %v after a connector failure, want the original %v", after, before)
	}
}

// TestRestartedOutputPluginKeepsItsSettings: the new process opens with the settings
// the destination was opened with, not with empty ones.
func TestRestartedOutputPluginKeepsItsSettings(t *testing.T) {
	bin := selfExecPlugin(t, streamOutputBinary)
	rt := runtime.New(runtime.Options{Logger: quiet()})
	conn, client := openOutput(t, rt, bin, "", map[string]string{"fail": "1"})
	startRuntime(t, rt)
	killed := killPlugins(t, streamOutputBinary, client)
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		_ = conn.Notify(context.Background(), sdk.Notification{Type: "finding.reported"})
		if after := pluginChildren(t, streamOutputBinary); len(after) == 1 && !killed[after[0]] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no new plugin process 20 s after the kill")
		}
	}
	if err := conn.Notify(context.Background(), sdk.Notification{Type: "finding.reported"}); err == nil {
		t.Error("the restarted plugin delivered; it should have opened with fail=1 like the first process")
	}
}

// TestConcurrentFailuresRestartOutputPluginOnce: deliveries that fail together on a
// dead plugin start one new process between them.
func TestConcurrentFailuresRestartOutputPluginOnce(t *testing.T) {
	bin := selfExecPlugin(t, streamOutputBinary)
	rt := runtime.New(runtime.Options{Logger: quiet()})
	conn, client := openOutput(t, rt, bin, "", nil)
	startRuntime(t, rt)
	killed := killPlugins(t, streamOutputBinary, client)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = conn.Notify(context.Background(), sdk.Notification{Type: "finding.reported"})
		}()
	}
	wg.Wait()
	if err := deliverUntil(conn); err != nil {
		t.Fatalf("deliveries did not resume: %v", err)
	}
	after := pluginChildren(t, streamOutputBinary)
	if len(after) != 1 || killed[after[0]] {
		t.Errorf("plugin processes %v after 8 concurrent failures, want exactly one new one", after)
	}
}

// TestCrashLoopingOutputPluginIsNotRestartedOnEveryDelivery: a plugin that dies on
// every delivery is restarted at once the first time, then no faster than the wait
// allows: attempts no earlier than 0 s, 1 s, 3 s, 7 s... after the first. Deliveries
// every 100 ms would start a process each without a wait, and one per second with a
// wait that never grows. The bound comes from the elapsed time, so a loaded host that
// stretches the loop only raises it.
func TestCrashLoopingOutputPluginIsNotRestartedOnEveryDelivery(t *testing.T) {
	bin := selfExecPlugin(t, streamOutputBinary)
	rt := runtime.New(runtime.Options{Logger: quiet()})
	conn, _ := openOutput(t, rt, bin, "", map[string]string{"crash": "1"})
	startRuntime(t, rt)
	seen := map[int]bool{}
	for _, pid := range pluginChildren(t, streamOutputBinary) {
		seen[pid] = true
	}
	start := time.Now()
	for i := 0; i < 25; i++ {
		_ = conn.Notify(context.Background(), sdk.Notification{Type: "finding.reported"})
		for _, pid := range pluginChildren(t, streamOutputBinary) {
			seen[pid] = true
		}
		time.Sleep(100 * time.Millisecond)
	}
	elapsed := time.Since(start)
	allowed := 1 // the first process
	for at, wait := time.Duration(0), time.Second; at <= elapsed; at, wait = at+wait, wait*2 {
		allowed++ // one restart per attempt time that has passed
	}
	if len(seen) < 2 {
		t.Fatalf("%d plugin processes in %v: the crashed plugin was not restarted", len(seen), elapsed)
	}
	if len(seen) > allowed {
		t.Errorf("%d plugin processes in %v, at most %d with a wait that doubles from 1 s: the restart wait does not hold back a plugin that keeps dying", len(seen), elapsed, allowed)
	}
}

// TestClosedOutputPluginIsNotRestarted: a destination torn down by a live reload or
// remove has its connector closed and its process killed by the caller. A delivery
// still in flight on it fails, and must not start the plugin again: nothing would
// ever tear that process down.
func TestClosedOutputPluginIsNotRestarted(t *testing.T) {
	bin := selfExecPlugin(t, streamOutputBinary)
	var logs bytes.Buffer
	rt := runtime.New(runtime.Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	conn, client := openOutput(t, rt, bin, "", nil)
	// The teardown the notify dispatcher runs on a reload: untrack, Close, Kill, release.
	rt.UntrackOutputPlugin(conn, client)
	_ = conn.Close(context.Background())
	client.Kill()
	rt.RunPluginCleanup(client)

	if err := conn.Notify(context.Background(), sdk.Notification{Type: "finding.reported"}); err == nil {
		t.Fatal("notify succeeded on a closed destination whose process was killed")
	}
	if strings.Contains(logs.String(), "restart") {
		t.Errorf("a closed destination was restarted:\n%s", logs.String())
	}
	if n := countPluginChildren(t, streamOutputBinary); n != 0 {
		t.Errorf("%d plugin processes after a delivery on a closed destination, want 0", n)
	}
}

// TestClosedOutputPluginAfterRestartLeavesNoProcess: the notify dispatcher tears a
// destination down knowing only the first client. Closing the connector must reap
// the process that replaced it.
func TestClosedOutputPluginAfterRestartLeavesNoProcess(t *testing.T) {
	bin := selfExecPlugin(t, streamOutputBinary)
	rt := runtime.New(runtime.Options{Logger: quiet()})
	conn, client := openOutput(t, rt, bin, "", nil)
	t.Cleanup(func() { // a failure before the teardown below must not leave processes
		_ = conn.Close(context.Background())
		client.Kill()
		rt.RunPluginCleanup(client)
	})
	killPlugins(t, streamOutputBinary, client)
	if err := deliverUntil(conn); err != nil {
		t.Fatalf("the plugin was not restarted: %v", err)
	}
	rt.UntrackOutputPlugin(conn, client)
	_ = conn.Close(context.Background())
	client.Kill()
	rt.RunPluginCleanup(client)
	waitNoChildren(t, streamOutputBinary, "Close left the restarted plugin process running")
}

// TestChangedBinaryRefusesOutputPluginRestartAndSaysSo: the relaunch of an external
// plugin checks its pinned digest again. When the binary on disk changed after
// admission, no process starts, and the delivery error says the restart was refused
// instead of looking like a plain transport failure.
func TestChangedBinaryRefusesOutputPluginRestartAndSaysSo(t *testing.T) {
	bin := selfExecPlugin(t, streamOutputBinary)
	rt := runtime.New(runtime.Options{Logger: quiet()})
	conn, client := openOutput(t, rt, bin, digestOf(t, bin), nil)
	startRuntime(t, rt)
	killPlugins(t, streamOutputBinary, client)
	image, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	changed := bin + ".new"
	if err := os.WriteFile(changed, append(image, 0), 0o711); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(changed, bin); err != nil {
		t.Fatal(err)
	}
	var nerr error
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		nerr = conn.Notify(context.Background(), sdk.Notification{Type: "finding.reported"})
		if nerr != nil && strings.Contains(nerr.Error(), "output plugin restart") {
			break
		}
	}
	if nerr == nil || !strings.Contains(nerr.Error(), "output plugin restart") {
		t.Fatalf("delivery error %v does not say that the restart was refused", nerr)
	}
	if n := countPluginChildren(t, streamOutputBinary); n != 0 {
		t.Errorf("%d plugin processes running from a binary that no longer matches its pin", n)
	}
}

// TestPollingSourceShowsFailedWhileItsPluginRestarts: a polling source stays Running
// through a failed pass, but one whose plugin process died and waits to be restarted
// is not running, and Status must say so until the new process is up.
func TestPollingSourceShowsFailedWhileItsPluginRestarts(t *testing.T) {
	bin := selfExecPlugin(t, streamSourceBinary)
	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "counter", got: make(chan event.Event, 32)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	startRuntime(t, rt)
	prepared, err := rt.PrepareSourcePlugin(bin)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := rt.AddPreparedSourceNamed(context.Background(), "stream-poll", prepared, sdk.Config{}, "tenant-s", time.Hour); err != nil {
		t.Fatalf("add: %v", err)
	}
	names := []string{"stream-poll"}
	first := processPerSource(t, mod.got, names)
	killPlugins(t, streamSourceBinary)

	statusOf := func() runtime.Status {
		for _, cs := range rt.Status() {
			if cs.Name == "stream-poll" {
				return cs.Status
			}
		}
		return ""
	}
	failed := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		st := statusOf()
		failed = failed || st == runtime.StatusFailed
		if failed && st == runtime.StatusRunning {
			break
		}
	}
	if !failed {
		t.Error("the source never reported failed while its plugin process was dead and waiting to restart")
	}
	if second := processPerSource(t, mod.got, names); second["stream-poll"] == first["stream-poll"] {
		t.Errorf("the source reported %s again; want a new plugin process", first["stream-poll"])
	}
}
