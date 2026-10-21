// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const startModelServerLlama = "/v1/m/agenttools/model-servers/llama-cpp/start"
const stopModelServerLlama = "/v1/m/agenttools/model-servers/llama-cpp/stop"

// processAlive is true for a process that runs: a zombie nobody reaped yet is gone.
func processAlive(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(raw)
	i := strings.LastIndex(s, ")")
	return i >= 0 && len(s) > i+2 && s[i+2] != 'Z' && s[i+2] != 'X'
}

func readPID(t *testing.T, file string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if raw, err := os.ReadFile(file); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid in %s", file)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitGone(t *testing.T, pid int, why string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still alive: %s", pid, why)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// auditActions counts the actions in the system ledger.
func (f *modelServerFixture) auditActions(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	err := f.store.View(context.Background(), model.SystemTenantID, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error { out[ev.Action]++; return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// captureReadable records the read-only folders the module hands the confined child.
func (f *modelServerFixture) captureReadable() (get func() []string) {
	var mu sync.Mutex
	var ro []string
	f.mod.SetChildCommand(func(ctx context.Context, w, r []string, program string, args ...string) (*exec.Cmd, error) {
		mu.Lock()
		ro = r
		mu.Unlock()
		return toolCommand(ctx, program, args...), nil
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(ro)
	}
}

// vLLM starts workers of its own. Stopping the server (or the engine) must end the whole
// process group, not only the first process.
func TestModelServerStopAndEngineStopEndEveryWorker(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "worker.pid")
	f := newModelServerFixtureWith(t, nil, "sleep 300 >/dev/null 2>&1 &\necho $! > "+pidfile)
	req := body(map[string]string{"model": f.modelFile})
	if code, out := f.call("POST", startModelServerLlama, req); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	waitModelServer(t, f.call, "llama-cpp", "running")
	worker := readPID(t, pidfile)
	if !processAlive(worker) {
		t.Fatal("the fixture's worker never ran")
	}
	f.call("POST", stopModelServerLlama, nil)
	waitGone(t, worker, "Stop left a worker of the server running")

	_ = os.Remove(pidfile)
	if code, out := f.call("POST", startModelServerLlama, req); code != 202 {
		t.Fatalf("second start = %d %v", code, out)
	}
	waitModelServer(t, f.call, "llama-cpp", "running")
	worker = readPID(t, pidfile)
	f.mod.Close()
	waitGone(t, worker, "the engine stopping left a worker of the server running")
}

// Two starts of the same server at once (a console double click, a CLI retry beside the
// console) must leave one child: the loser answers with the state of the winner.
func TestModelServerConcurrentStartsRunOneChild(t *testing.T) {
	count := filepath.Join(t.TempDir(), "spawned")
	f := newModelServerFixtureWith(t, nil, "echo spawned >> "+count)
	req := body(map[string]string{"model": f.modelFile})
	codes := make([]int, 8)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = f.call("POST", startModelServerLlama, req)
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 202 {
			t.Fatalf("start %d = %d, want 202 (the winner's state)", i, c)
		}
	}
	waitModelServer(t, f.call, "llama-cpp", "running")
	raw, _ := os.ReadFile(count)
	if n := strings.Count(string(raw), "spawned"); n != 1 {
		t.Fatalf("%d children were spawned by 8 concurrent starts, want 1", n)
	}
	if _, n := f.last(); n != 1 {
		t.Fatalf("registered %d times, want once", n)
	}
	f.call("POST", stopModelServerLlama, nil)
	waitModelServer(t, f.call, "llama-cpp", "stopped")
}

// The same start again changes nothing; another model, or the same model for another
// organization, is refused while the server runs. Start and stop each leave one audit row.
func TestModelServerRepeatedAndConflictingStarts(t *testing.T) {
	f := newModelServerFixture(t, nil)
	other := filepath.Join(t.TempDir(), "other.gguf")
	if err := os.WriteFile(other, []byte("GGUF"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := body(map[string]string{"model": f.modelFile})
	f.call("POST", startModelServerLlama, req)
	waitModelServer(t, f.call, "llama-cpp", "running")
	first, _ := f.last()

	if code, out := f.call("POST", startModelServerLlama, req); code != 202 {
		t.Fatalf("the same start again = %d %v, want 202", code, out)
	}
	if again, n := f.last(); n != 1 || again.key != first.key {
		t.Fatalf("the same start again registered %d times / changed the key", n)
	}
	code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": other}))
	if kind, _ := apiError(out); code != 409 || kind != "already_running" {
		t.Fatalf("another model while running = %d %v, want 409 already_running", code, out)
	}
	otherOrg := map[string]string{"model": f.modelFile, "tenant_id": model.NewID().String()}
	code, out = f.call("POST", startModelServerLlama, otherOrg)
	if kind, _ := apiError(out); code != 409 || kind != "already_running" {
		t.Fatalf("the same model for another organization = %d %v, want 409 already_running", code, out)
	}
	if _, n := f.last(); n != 1 {
		t.Fatalf("a refused start registered again (%d)", n)
	}
	if code, out := f.call("POST", stopModelServerLlama, nil); code != 200 || out["state"] != "stopped" {
		t.Fatalf("stop = %d %v", code, out)
	}
	actions := f.auditActions(t)
	if actions["agenttools.model_server.start"] != 1 || actions["agenttools.model_server.stop"] != 1 {
		t.Fatalf("audit rows = %v, want one start and one stop", actions)
	}
	// Stopping a server that does not run is not an error.
	if code, out := f.call("POST", stopModelServerLlama, nil); code != 200 || out["state"] != "stopped" {
		t.Fatalf("stop of a stopped server = %d %v", code, out)
	}
}

// What may be named as a model, for each server.
func TestModelServerModelReferences(t *testing.T) {
	f := newModelServerFixture(t, nil)
	modelDir := t.TempDir()
	for name, tc := range map[string]struct{ kind, model string }{
		"a repository with a .gguf name":      {"llama-cpp", "x/y.gguf"},
		"an upper-case .GGUF name":            {"llama-cpp", "x/Y.GGUF:Q4"},
		"an option":                           {"llama-cpp", "-x/y"},
		"an empty quantization":               {"llama-cpp", "x/y:"},
		"two slashes":                         {"llama-cpp", "x//y"},
		"a space":                             {"llama-cpp", "a b/c"},
		"a directory for llama.cpp":           {"llama-cpp", modelDir},
		"a file for vLLM":                     {"vllm", f.modelFile},
		"a vLLM name with a quantization":     {"vllm", "x/y:Q4"},
		"a vLLM option":                       {"vllm", "--served-model-name=x"},
		"the filesystem root for vLLM":        {"vllm", "/"},
		"a missing vLLM directory":            {"vllm", "/nonexistent/model-dir"},
		"a trailing slash is not clean":       {"vllm", modelDir + "/"},
		"a double slash is not clean":         {"vllm", strings.Replace(modelDir, "/", "//", 1)},
		"a .gguf directory":                   {"llama-cpp", modelDir + "/x.gguf/"},
		"a model longer than any path allows": {"llama-cpp", "/" + strings.Repeat("a", 2000)},
	} {
		t.Run(name, func(t *testing.T) {
			code, out := f.call("POST", "/v1/m/agenttools/model-servers/"+tc.kind+"/start", body(map[string]string{"model": tc.model}))
			if code != 400 {
				t.Fatalf("start with %q = %d %v, want 400", tc.model, code, out)
			}
		})
	}
	if _, n := f.last(); n != 0 {
		t.Fatalf("a refused model registered %d records", n)
	}
}

func TestModelServerAcceptsAHuggingFaceReferenceAndAModelDirectory(t *testing.T) {
	f := newModelServerFixture(t, nil)
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": "bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M"})); code != 202 {
		t.Fatalf("llama.cpp with a repository = %d %v", code, out)
	}
	row := waitModelServer(t, f.call, "llama-cpp", "running")
	if args := fetch(t, strings.TrimSuffix(row["endpoint"].(string), "/v1")+"/fake/args"); !strings.HasPrefix(args, "-hf bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M --host 127.0.0.1") {
		t.Fatalf("llama.cpp arguments %q, want -hf <repository:quantization>", args)
	}

	dir := t.TempDir()
	if code, out := f.call("POST", "/v1/m/agenttools/model-servers/vllm/start", body(map[string]string{"model": dir})); code != 202 {
		t.Fatalf("vLLM with a directory = %d %v", code, out)
	}
	row = waitModelServer(t, f.call, "vllm", "running")
	if args := fetch(t, strings.TrimSuffix(row["endpoint"].(string), "/v1")+"/fake/args"); !strings.HasPrefix(args, "serve "+dir+" --host 127.0.0.1") {
		t.Fatalf("vLLM arguments %q, want serve <directory>", args)
	}
}

// A model reached through a link: the server is given the resolved file, the id it lists is
// the link's name, and the child may read the folder the file is really in.
func TestModelServerFollowsALinkToTheRealFile(t *testing.T) {
	f := newModelServerFixture(t, nil)
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "linked-name.gguf")
	if err := os.Symlink(f.modelFile, link); err != nil {
		t.Fatal(err)
	}
	readable := f.captureReadable()
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": link})); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	row := waitModelServer(t, f.call, "llama-cpp", "running")
	args := fetch(t, strings.TrimSuffix(row["endpoint"].(string), "/v1")+"/fake/args")
	real, _ := filepath.EvalSymlinks(f.modelFile)
	if !strings.Contains(args, "-m "+real+" --alias linked-name") {
		t.Fatalf("arguments %q, want the resolved file and the link's name as the alias", args)
	}
	if ro := readable(); !slices.Contains(ro, filepath.Dir(real)) || slices.Contains(ro, linkDir) {
		t.Fatalf("readable = %v, want the real file's folder and not the link's", ro)
	}
}

// The child may read its model and its program, never the whole filesystem or the engine
// user's home (where that user's own tool logins are).
func TestModelServerNeverGrantsTheHomeOrTheFilesystem(t *testing.T) {
	f := newModelServerFixture(t, nil)
	home := t.TempDir()
	t.Setenv("HOME", home) // after the fixture, which points HOME at its decoy engine home

	// a model file directly in the home folder
	inHome := filepath.Join(home, "model.gguf")
	if err := os.WriteFile(inHome, []byte("GGUF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": inHome})); code != 400 {
		t.Fatalf("a model directly in the home folder = %d %v, want 400", code, out)
	}

	// a program whose parent folder would reach the home: only its own folder is granted
	readable := f.captureReadable()
	t.Setenv("HOME", filepath.Dir(f.bin))
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile})); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	waitModelServer(t, f.call, "llama-cpp", "running")
	if ro := readable(); !slices.Contains(ro, f.bin) || slices.Contains(ro, filepath.Dir(f.bin)) {
		t.Fatalf("readable = %v, want the program's own folder %s and not its parent", ro, f.bin)
	}
	f.call("POST", stopModelServerLlama, nil)
	waitModelServer(t, f.call, "llama-cpp", "stopped")

	// a program that sits in the home folder itself cannot be given a folder of its own
	t.Setenv("HOME", f.bin)
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile})); code != 409 {
		t.Fatalf("a program in the home folder = %d %v, want 409", code, out)
	}
}

func TestModelServerRefusesWhereItCannotRun(t *testing.T) {
	f := newModelServerFixture(t, nil)
	req := body(map[string]string{"model": f.modelFile})

	f.mod.UseModelServers(ModelServerConfig{})
	if code, out := f.call("POST", startModelServerLlama, req); code != 503 {
		t.Fatalf("with no place for model servers = %d %v, want 503", code, out)
	}
	f.mod.UseModelServers(f.cfg)

	f.mod.readOnly = true
	if code, out := f.call("POST", startModelServerLlama, req); code != 409 {
		t.Fatalf("on a read-only control plane = %d %v, want 409", code, out)
	}
	f.mod.RestartModelServers() // must start nothing either
	if st := f.mod.modelServerStatus(context.Background(), "llama-cpp"); st.State != "stopped" {
		t.Fatalf("a read-only control plane started %+v", st)
	}
}

// Register failing leaves a server nobody can reach through Providers. The row says what failed,
// in the engine's words, instead of a hint that cannot help.
func TestModelServerSaysWhyItsEndpointCouldNotBeRegistered(t *testing.T) {
	f := newModelServerFixture(t, nil)
	f.failWith = errors.New("a provider named llama.cpp on this server already exists at another address; rename it, then start the server again")
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile})); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	row := waitModelServer(t, f.call, "llama-cpp", "running")
	msg, _ := row["message"].(string)
	if !strings.Contains(msg, "could not be added to Providers") || !strings.Contains(msg, "already exists at another address") {
		t.Fatalf("message = %q, want the cause", msg)
	}
}

// A server that never finishes loading is said so; the stop the engine then makes is not
// reported as the server stopping on its own.
func TestModelServerSaysItDidNotAnswerInTime(t *testing.T) {
	f := newModelServerFixtureWith(t, nil, "export OLIVARES_FAKE_NO_HEALTH=1")
	f.cfg.ReadyTimeout = time.Second
	f.mod.UseModelServers(f.cfg)
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile})); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	waitModelServer(t, f.call, "llama-cpp", "failed")
	time.Sleep(time.Second) // the child's end must not rewrite the reason
	row := waitModelServer(t, f.call, "llama-cpp", "failed")
	if msg, _ := row["message"].(string); !strings.Contains(msg, "did not answer in time") || strings.Contains(msg, "on its own") {
		t.Fatalf("message = %q, want the timeout and not 'stopped on its own'", msg)
	}
	if _, n := f.last(); n != 0 {
		t.Fatal("a server that never answered was registered")
	}
}

// A server that echoes its key in what it prints must not have it shown in the row.
func TestModelServerNeverShowsItsKeyInTheRow(t *testing.T) {
	f := newModelServerFixtureWith(t, nil, "export OLIVARES_FAKE_EXIT=1 OLIVARES_FAKE_EXIT_KEY=1")
	f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile}))
	row := waitModelServer(t, f.call, "llama-cpp", "failed")
	msg, _ := row["message"].(string)
	if strings.Contains(msg, "olv-") || !strings.Contains(msg, "[key]") {
		t.Fatalf("message = %q, want the key replaced", msg)
	}
}

// A start the engine could not keep would not come back after a restart: it is refused, and
// nothing runs.
func TestModelServerIsNotStartedWhenItsStartCannotBeKept(t *testing.T) {
	addr := freeAddr(t)
	f := newModelServerFixture(t, map[string]string{"llama-cpp": addr})
	if err := os.MkdirAll(filepath.Join(f.dir, "llama-cpp.json"), 0o700); err != nil { // a place a file cannot be put
		t.Fatal(err)
	}
	code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile}))
	if kind, msg := apiError(out); code != 409 || kind != "not_started" || !strings.Contains(msg, "could not keep this start") {
		t.Fatalf("start = %d %v, want 409 saying the start could not be kept", code, out)
	}
	if listening(addr) {
		t.Fatal("a child runs although its start could not be kept")
	}
	if st := f.mod.modelServerStatus(context.Background(), "llama-cpp"); st.State != "stopped" {
		t.Fatalf("state = %+v, want stopped", st)
	}
}

// What keeps a server from coming back after an engine restart is said on its row, with no
// audit row and no child.
func TestModelServerRestartSaysWhyItDidNotComeBack(t *testing.T) {
	for name, tc := range map[string]struct {
		prepare func(f *modelServerFixture)
		gone    bool // the program is no longer on the PATH
		want    string
	}{
		"its record is not readable": {
			prepare: func(f *modelServerFixture) {
				_ = os.WriteFile(filepath.Join(f.dir, "llama-cpp.json"), []byte("{not json"), 0o600)
			},
			want: "not readable",
		},
		"its record names no organization": {
			prepare: func(f *modelServerFixture) {
				_ = os.WriteFile(filepath.Join(f.dir, "llama-cpp.json"), []byte(`{"tenant_id":"","model":"x/y"}`), 0o600)
			},
			want: "names no organization",
		},
		"its model is gone": {
			prepare: func(f *modelServerFixture) { _ = os.Remove(f.modelFile) },
			want:    "was not found",
		},
		"its program is gone": {gone: true, want: "no longer on the engine's PATH"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newModelServerFixture(t, nil)
			if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile})); code != 202 {
				t.Fatalf("start = %d %v", code, out)
			}
			waitModelServer(t, f.call, "llama-cpp", "running")
			f.mod.Close()
			if tc.prepare != nil {
				tc.prepare(f)
			}
			look := f.mod.lookPath
			if tc.gone {
				look = func(string) (string, error) { return "", errors.New("not found") }
			}
			var drafts []model.AuditDraft
			next := f.nextEngineLooking(t, func(_ context.Context, d model.AuditDraft) error { drafts = append(drafts, d); return nil }, look)
			st := next.modelServerStatus(context.Background(), "llama-cpp")
			if st.State != "failed" || !strings.Contains(st.Message, tc.want) || len(drafts) != 0 {
				t.Fatalf("after the restart: %+v with %d audit rows, want failed saying %q and no audit row", st, len(drafts), tc.want)
			}
			if _, n := f.last(); n != 1 {
				t.Fatalf("registered %d times, want only the first start", n)
			}
		})
	}
}

// Stopping is the person's answer to a server that failed: the row stops saying it failed. A row
// that only says why a server did not come back after a restart has no process to stop.
func TestModelServerStopClearsAFailedRow(t *testing.T) {
	f := newModelServerFixtureWith(t, nil, "export OLIVARES_FAKE_EXIT=1")
	f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile}))
	waitModelServer(t, f.call, "llama-cpp", "failed")
	if code, out := f.call("POST", stopModelServerLlama, nil); code != 200 || out["state"] != "stopped" || out["message"] != nil {
		t.Fatalf("stop of a failed server = %d %v, want it stopped with no message", code, out)
	}

	if err := os.WriteFile(filepath.Join(f.dir, "llama-cpp.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	next := f.nextEngine(t, func(context.Context, model.AuditDraft) error { return nil })
	if st := next.modelServerStatus(context.Background(), "llama-cpp"); st.State != "failed" {
		t.Fatalf("after the restart: %+v, want a failed row", st)
	}
	next.stopModelServer("llama-cpp")
	if st := next.modelServerStatus(context.Background(), "llama-cpp"); st.State != "stopped" || st.Message != "" {
		t.Fatalf("after Stop: %+v, want the row cleared", st)
	}
}

// A server that stopped but whose start could not be forgotten would come back after the next
// restart; Stop says so instead of reporting success.
func TestModelServerStopSaysWhenItCannotForgetItsStart(t *testing.T) {
	f := newModelServerFixture(t, nil)
	f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile}))
	waitModelServer(t, f.call, "llama-cpp", "running")
	state := filepath.Join(f.dir, "llama-cpp.json")
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "keep"), 0o700); err != nil { // something Remove cannot clear
		t.Fatal(err)
	}
	code, out := f.call("POST", stopModelServerLlama, nil)
	if kind, msg := apiError(out); code != 409 || kind != "not_forgotten" || !strings.Contains(msg, "could not be removed") {
		t.Fatalf("stop = %d %v, want 409 not_forgotten", code, out)
	}
	waitModelServer(t, f.call, "llama-cpp", "stopped")
}

// A program installed with pip --user sits in ~/.local/bin: its parent folder, ~/.local, holds
// other tools' logins (~/.local/share/opencode), so only the program's own folder is granted.
func TestModelServerDoesNotGrantTheLoginFoldersOfTheHome(t *testing.T) {
	f := newModelServerFixture(t, nil)
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper, err := os.ReadFile(filepath.Join(f.bin, "llama-server"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "llama-server"), wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	f.mod.lookPath = func(name string) (string, error) { return filepath.Join(bin, name), nil }
	readable := f.captureReadable()
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile})); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	waitModelServer(t, f.call, "llama-cpp", "running")
	if ro := readable(); !slices.Contains(ro, bin) || slices.Contains(ro, filepath.Join(home, ".local")) || slices.Contains(ro, home) {
		t.Fatalf("readable = %v, want ~/.local/bin and neither ~/.local nor the home", ro)
	}
}

// A program that is a link into its own environment (pipx puts ~/.local/bin/vllm there) is read
// from where the link points: its environment, not the folder the link sits in.
func TestModelServerReadsAProgramFromWhereItsLinkPoints(t *testing.T) {
	f := newModelServerFixture(t, nil)
	home := t.TempDir()
	t.Setenv("HOME", home)
	venvBin := filepath.Join(home, ".local", "share", "pipx", "venvs", "tool", "bin")
	linkDir := filepath.Join(home, ".local", "bin")
	for _, d := range []string{venvBin, linkDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wrapper, err := os.ReadFile(filepath.Join(f.bin, "llama-server"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venvBin, "llama-server"), wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(venvBin, "llama-server"), filepath.Join(linkDir, "llama-server")); err != nil {
		t.Fatal(err)
	}
	f.mod.lookPath = func(name string) (string, error) { return filepath.Join(linkDir, name), nil }
	readable := f.captureReadable()
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile})); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	waitModelServer(t, f.call, "llama-cpp", "running")
	env := filepath.Dir(venvBin)
	if ro := readable(); !slices.Contains(ro, env) || slices.Contains(ro, filepath.Join(home, ".local")) {
		t.Fatalf("readable = %v, want the environment %s the link points into", ro, env)
	}
}

// Which folders may be granted for reading: not the filesystem, the engine user's home, anything
// that holds the home or its login folders, or a folder that cannot be placed.
func TestBroadReadRoot(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "..data", "home") // a folder whose name begins with ".."
	for _, d := range []string{".local/share/pipx/venvs/vllm", "venvs/vllm", ".local/bin"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	for root, want := range map[string]bool{
		"/":                                    true,
		home:                                   true,
		filepath.Dir(home):                     true,
		base:                                   true, // holds ../data/home: a prefix test on ".." gets this wrong
		home + "/.local":                       true,
		home + "/.local/share":                 true,
		home + "/.config":                      true,
		"relative/dir":                         true,
		"":                                     true,
		"/usr":                                 false,
		filepath.Join(base, "x"):               false,
		home + "/.ssh/models":                  true, // inside a login folder
		home + "/.local/share/opencode":        true,
		home + "/.local/share/opencode/models": true,
		"/tmp":                                 true, // shared with every process
		"/dev/shm":                             true,
		"/tmp/models":                          false,
		home + "/venvs/vllm":                   false,
		home + "/.local/bin":                   false,
		home + "/.local/share/pipx/venvs/vllm": false,
	} {
		if got := broadReadRoot(root); got != want {
			t.Errorf("broadReadRoot(%q) = %v, want %v", root, got, want)
		}
	}
}

// The key is replaced in everything the child printed before the last line is cut for a status
// row: a key that straddles the cut would otherwise survive as a prefix.
func TestLastLineWithoutRedactsBeforeCutting(t *testing.T) {
	key := "olv-" + strings.Repeat("ab", 24)
	tb := &tailBuffer{max: 4096}
	_, _ = tb.Write([]byte("noise\n" + strings.Repeat("x", 190) + key + " tail\n"))
	got := tb.lastLineWithout(key)
	if strings.Contains(got, "olv-") || !strings.HasSuffix(got, "[key] tail") {
		t.Fatalf("last line = %q, want the key replaced whole", got)
	}
}

// A status row is cut at a length; a cut inside a multi-byte character drops the character
// rather than leaving bytes that are not text.
func TestBoundedTextNeverCutsACharacter(t *testing.T) {
	// one byte, then two-byte characters: the 240th byte is the first half of a character
	if got := boundedText("x" + strings.Repeat("\u00e9", 200)); !utf8.ValidString(got) || len(got) > 240 || len(got) < 238 {
		t.Fatalf("boundedText = %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}

// Stop ends the registration of the server it stopped: one still waiting on the provider plane
// must not write the stopped server's endpoint and key after the person's Stop.
func TestModelServerStopEndsARegistrationStillInFlight(t *testing.T) {
	f := newModelServerFixture(t, map[string]string{"llama-cpp": freeAddr(t)})
	entered, ended := make(chan struct{}), make(chan error, 1)
	f.cfg.Register = func(ctx context.Context, _ auth.Principal, _ model.TenantID, _, _, _ string) error {
		close(entered)
		select {
		case <-ctx.Done():
			ended <- ctx.Err()
		case <-time.After(10 * time.Second):
			ended <- errors.New("nobody ended the registration")
		}
		return nil
	}
	f.mod.UseModelServers(f.cfg)
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile})); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the server never reached its registration")
	}
	if code, out := f.call("POST", stopModelServerLlama, nil); code != 200 {
		t.Fatalf("stop = %d %v", code, out)
	}
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the registration ended with %v, want it cancelled by the Stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the Stop left the registration running")
	}
}

// A build that ignores the key variable would serve anyone on this host and its endpoint would be
// handed to sessions as if it were private. The engine stops it and registers nothing.
func TestModelServerThatDoesNotAskForItsKeyIsStoppedAndNotRegistered(t *testing.T) {
	addr := freeAddr(t)
	f := newModelServerFixtureWith(t, map[string]string{"llama-cpp": addr}, "export OLIVARES_FAKE_OPEN=1")
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile})); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	row := waitModelServer(t, f.call, "llama-cpp", "failed")
	if msg, _ := row["message"].(string); !strings.Contains(msg, "without asking for its key") {
		t.Fatalf("message = %q, want the reason", msg)
	}
	if _, n := f.last(); n != 0 {
		t.Fatalf("an open server was registered %d times", n)
	}
	deadline := time.Now().Add(8 * time.Second)
	for listening(addr) {
		if time.Now().After(deadline) {
			t.Fatal("the open server is still running")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The port is picked before the child binds it. When the child ends and another process answers
// there (even one that does ask for a key), nothing is registered or tested: the child's end
// ends the wait for the server to answer.
func TestModelServerNeverRegistersWithAProcessThatTookItsPort(t *testing.T) {
	addr := freeAddr(t)
	f := newModelServerFixture(t, map[string]string{"llama-cpp": addr})
	stand := make(chan *http.Server, 1)
	f.mod.SetChildCommand(func(ctx context.Context, w, r []string, program string, args ...string) (*exec.Cmd, error) {
		l, err := net.Listen("tcp", addr) // the process that took the port after the engine picked it
		if err != nil {
			return nil, err
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(600 * time.Millisecond) // after the real child has ended
			_, _ = w.Write([]byte("ok"))
		})
		mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			http.Error(w, "empty", http.StatusBadRequest)
		})
		srv := &http.Server{Handler: mux}
		go func() { _ = srv.Serve(l) }()
		stand <- srv
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1"), nil
	})
	t.Cleanup(func() {
		select {
		case srv := <-stand:
			_ = srv.Close()
		default:
		}
	})
	if code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile})); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	time.Sleep(2 * time.Second)
	if _, n := f.last(); n != 0 {
		t.Fatalf("the process that took the port was registered %d times", n)
	}
	if st := f.mod.modelServerStatus(context.Background(), "llama-cpp"); st.State == "running" {
		t.Fatalf("status = %+v, want a server that is not running", st)
	}
}

// A Stop and a Start of one server are ordered: the Stop that arrives while a start is taking the
// slot waits for it, then ends it and forgets it. Otherwise the Stop could delete the record of
// the start that came after it, and that server would not return with the engine.
func TestModelServerStopWaitsForAStartThatIsTakingTheSlot(t *testing.T) {
	addr := freeAddr(t)
	f := newModelServerFixture(t, map[string]string{"llama-cpp": addr})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.mod.SetChildCommand(func(ctx context.Context, w, r []string, program string, args ...string) (*exec.Cmd, error) {
		once.Do(func() { close(entered) })
		<-release
		return toolCommand(ctx, program, args...), nil
	})
	startCode, stopCode := make(chan int, 1), make(chan int, 1)
	go func() {
		code, _ := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile}))
		startCode <- code
	}()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the start never reached the command")
	}
	go func() { code, _ := f.call("POST", stopModelServerLlama, nil); stopCode <- code }()
	select {
	case code := <-stopCode:
		t.Fatalf("Stop answered %d while a start was still taking the slot", code)
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	if code := <-startCode; code != 202 {
		t.Fatalf("start = %d, want 202", code)
	}
	if code := <-stopCode; code != 200 {
		t.Fatalf("stop = %d, want 200", code)
	}
	waitModelServer(t, f.call, "llama-cpp", "stopped")
	if _, err := os.Stat(filepath.Join(f.dir, "llama-cpp.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stopped server's record is still kept: %v", err)
	}
}

// A start that is refused because the command could not be prepared must not leave its record
// behind unsaid: the engine would start the server the person was told did not start, at the
// next boot. When the record cannot be removed either, the refusal says so.
func TestModelServerRefusedStartSaysWhenItsRecordCannotBeRemoved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a folder cannot be made unwritable for root")
	}
	f := newModelServerFixture(t, map[string]string{"llama-cpp": freeAddr(t)})
	f.mod.SetChildCommand(func(ctx context.Context, w, r []string, program string, args ...string) (*exec.Cmd, error) {
		if err := os.Chmod(f.dir, 0o500); err != nil { // the record is already kept; now it cannot be removed
			return nil, err
		}
		return nil, errors.New("the sandbox is not available")
	})
	t.Cleanup(func() { _ = os.Chmod(f.dir, 0o700) })
	code, out := f.call("POST", startModelServerLlama, body(map[string]string{"model": f.modelFile}))
	if _, msg := apiError(out); code != 409 || !strings.Contains(msg, "sandbox is not available") || !strings.Contains(msg, "could not be removed either") {
		t.Fatalf("start = %d %v, want the refusal and that its record stays", code, out)
	}
}
