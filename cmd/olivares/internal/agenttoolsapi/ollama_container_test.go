// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// The container flavour of the Ollama service: the person may ask for the
// Ollama image instead of the installed program, with the same models directory
// mounted, the same endpoint registered, and the same start, stop and
// restart-with-the-engine. A host with no runtime refuses it and says so.

// useContainer gives the fixture the container flavour driven by the fake CLI. Image
// is what the test names: empty exercises the setting and its default.
func (f *ollamaFixture) useContainer(t *testing.T, c OllamaContainer) {
	t.Helper()
	if f.containerCLI == "" {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		f.containerLog = filepath.Join(t.TempDir(), "container-calls.log")
		f.containerCLI = filepath.Join(f.bin, "podman")
		writeFakeContainerCLI(t, f.containerCLI, self, f.containerLog)
	}
	c.Runtime = f.containerCLI
	if err := os.WriteFile(f.containerLog+".owner", []byte(ollamaContainerOwner(f.cfg.HomeDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	f.cfg.Container = &c
	f.mod.UseOllama(f.cfg)
}

// rememberContainerStart writes the state file a killed engine leaves behind: it says
// the container flavour ran, with the runtime and image that ran it.
func (f *ollamaFixture) rememberContainerStart(t *testing.T) {
	t.Helper()
	raw := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, f.containerCLI)
	if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeFakeContainerCLI writes the runtime the container tests drive. It records every
// call's argv and, for `run`, starts the fake Ollama on the host port the -p mapping
// names and in the models directory the -v mount names, as the runtime would.
func writeFakeContainerCLI(t *testing.T, path, self, log string) {
	t.Helper()
	script := `#!/bin/sh
{
  printf '%s\n' '---'
  for a in "$@"; do printf '%s\n' "$a"; done
} >> '` + log + `'
if [ "$1" = inspect ]; then
  owner=1
  [ -f '` + log + `.owner' ] && owner=$(cat '` + log + `.owner')
  [ -e '` + log + `.inspect-warn' ] && echo 'WARNING: Error loading config file: /home/x/.docker/config.json: permission denied' >&2
  [ -e '` + log + `.inspect-missing' ] && { echo 'Error: No such container: olivares-ollama' >&2; exit 1; }
  [ -e '` + log + `.inspect-stopped' ] && { printf 'false %s\n' "$owner"; exit 0; }
  [ -e '` + log + `.inspect-foreign' ] && { printf 'true 0\n'; exit 0; }
  if [ -e '` + log + `.inspect-foreign-after-run' ] && grep -q '^run$' '` + log + `' 2>/dev/null; then printf 'true 0\n'; exit 0; fi
  printf 'true %s\n' "$owner"; exit 0
fi
if [ "$1" = rm ]; then
  [ -e '` + log + `.missing-rm' ] && { echo 'Error: No such container: olivares-ollama' >&2; exit 1; }
  [ -e '` + log + `.context-missing-rm' ] && { echo 'Error: context "other" does not exist' >&2; exit 1; }
  [ -e '` + log + `.fail-rm' ] && { echo 'Error: cannot remove container: permission denied' >&2; exit 1; }
  exit 0
fi
[ "$1" = run ] || exit 2
[ -e '` + log + `.dump-env' ] && env > '` + log + `.env'
[ -e '` + log + `.fail-run' ] && { echo 'Error: image not found' >&2; exit 125; }
publish=; models=; prev=
for a in "$@"; do
  case "$prev" in
    -p) publish=${a%:11434} ;;
    -v) models=${a%%:*} ;;
  esac
  prev=$a
done
OLLAMA_HOST=$publish OLLAMA_MODELS=$models OLLAMA_NO_CLOUD=1 OLIVARES_FAKE_OLLAMA=serve exec '` + self + `' -test.run='^TestFakeOllamaProcess$'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// containerCalls reads the recorded calls: one []string per invocation.
func containerCalls(t *testing.T, log string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var cur []string
	for _, line := range strings.Split(string(raw), "\n") {
		switch line {
		case "---":
			if cur != nil {
				calls = append(calls, cur)
			}
			cur = []string{}
		case "":
		default:
			if cur != nil {
				cur = append(cur, line)
			}
		}
	}
	if cur != nil {
		calls = append(calls, cur)
	}
	return calls
}

// containerRuns are the recorded calls that ran an image.
func containerRuns(t *testing.T, log string) [][]string {
	t.Helper()
	var runs [][]string
	for _, call := range containerCalls(t, log) {
		if len(call) > 0 && call[0] == "run" {
			runs = append(runs, call)
		}
	}
	return runs
}

// oneContainerRun is the single recorded `run`, which a start must make exactly once.
func oneContainerRun(t *testing.T, log string) []string {
	t.Helper()
	runs := containerRuns(t, log)
	if len(runs) != 1 {
		t.Fatalf("the runtime ran %d times, want 1: %q", len(runs), containerCalls(t, log))
	}
	return runs[0]
}

// argsHave reports whether seq appears in args as consecutive elements.
func argsHave(args []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		ok := true
		for j := range seq {
			if args[i+j] != seq[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// the regression exit criterion 1: Start runs the Ollama image as the engine's managed
// container, the models directory mounted, the endpoint registered as today, and the
// installed program is not needed. Stop removes the container with the engine.
func TestOllamaContainerStartsTheImageWithTheModelsMounted(t *testing.T) {
	call, f := newOllamaServer(t)
	f.mod.SetProgramResolver(func(string) string { return "" }) // no installed Ollama: the image is enough
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	org := seedOrg(t, f.store, "Second", "second")
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"tenant_id": org.String(), "container": true}); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	st := waitOllama(t, call, "running")
	if st["runtime"] != "container" || st["endpoint"] != "http://"+f.addr {
		t.Fatalf("status = %v, want a running container at %s", st, f.addr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.registered() == "" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if f.registered() != "http://"+f.addr {
		t.Fatalf("registered endpoint = %q, want http://%s", f.registered(), f.addr)
	}
	args := oneContainerRun(t, f.containerLog)
	for _, seq := range [][]string{
		{"run", "--rm"},
		{"--name", "olivares-ollama"},
		{"--label", "ai.olivares.ollama=" + ollamaContainerOwner(f.cfg.HomeDir)},
		{"--read-only"},
		{"--tmpfs", "/tmp:rw,exec,nosuid,nodev"},
		{"--cap-drop=ALL"},
		{"--security-opt", "no-new-privileges"},
		{"-p", f.addr + ":11434"},
		{"-v", f.models + ":/models"},
		{"-e", "OLLAMA_HOST=0.0.0.0:11434"},
		{"-e", "OLLAMA_MODELS=/models"},
		{"-e", "OLLAMA_NO_CLOUD=1"},
		{"docker.io/ollama/ollama:test", "serve"},
	} {
		if !argsHave(args, seq...) {
			t.Errorf("container command lacks %q\n  got %q", seq, args)
		}
	}
	if argsHave(args, "--gpus") {
		t.Errorf("a GPU was given although the test host named none: %q", args)
	}
	f.mod.Close() // the engine stops
	if listening(f.addr) {
		t.Fatal("the Ollama container outlived the engine")
	}
	calls := containerCalls(t, f.containerLog)
	last := calls[len(calls)-1]
	if !argsHave(last, "rm", "-f", "olivares-ollama") {
		t.Fatalf("the last runtime call was %q, want the container removed", last)
	}
}

// the regression exit criterion 2: the models live in the mounted directory, so they survive
// a Stop and an engine restart, and the restarted service is the same flavour.
func TestOllamaContainerKeepsItsModelsAcrossARestart(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	org := seedOrg(t, f.store, "Second", "second")
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"tenant_id": org.String(), "container": true}); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	waitOllama(t, call, "running")
	code, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"})
	if code != 202 {
		t.Fatalf("pull = %d %v", code, p)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, got := call("GET", "/v1/m/agenttools/ollama/pulls/"+p["id"].(string), nil)
		if got["state"] == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the download never finished: %v", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
	modelFile := filepath.Join(f.models, url.QueryEscape("qwen2.5:0.5b"))
	if _, err := os.Stat(modelFile); err != nil {
		t.Fatalf("the model was not stored through the mount: %v", err)
	}
	f.mod.Close() // the engine stops; the container and its models stay
	st := auditStore(t)
	var drafts []model.AuditDraft
	next := f.nextEngineRecording(t, st, func(d model.AuditDraft) { drafts = append(drafts, d) })
	if !ollamaStateWithin(next, ollamaRunning, 20*time.Second) {
		t.Fatalf("after the restart Ollama is %+v, want running", next.ollamaStatus(context.Background()))
	}
	got := next.ollamaStatus(context.Background())
	if got.Runtime != "container" {
		t.Fatalf("restarted runtime = %q, want container", got.Runtime)
	}
	if len(got.Models) != 1 || got.Models[0] != "qwen2.5:0.5b" {
		t.Fatalf("models after the restart = %v, want the downloaded one", got.Models)
	}
	if _, err := os.Stat(modelFile); err != nil {
		t.Fatalf("the model went with the restart: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Meta["runtime"] != "container" {
		t.Fatalf("restart audit = %+v, want one row naming the container runtime", drafts)
	}
	if runs := containerRuns(t, f.containerLog); len(runs) != 2 {
		t.Fatalf("the runtime ran %d times, want the start and the restart: %q", len(runs), runs)
	}
	next.Close()
}

// The shipped default is unchanged: without the flag the installed program is the
// service even when the node can run containers.
func TestOllamaUsesTheChildProcessUnlessThePersonAsksForAContainer(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", nil); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	if st := waitOllama(t, call, "running"); st["runtime"] != "process" {
		t.Fatalf("runtime = %v, want the child process by default", st["runtime"])
	}
	if calls := containerCalls(t, f.containerLog); len(calls) != 0 {
		t.Fatalf("the container runtime was used without the flag: %q", calls)
	}
}

// A node with no runtime, or one where containers are off, refuses the container start
// with a sentence that names what to install or change, and starts nothing.
func TestOllamaContainerRefusesWithNoRuntime(t *testing.T) {
	t.Setenv(sessions.ContainerRuntimeEnv, "auto")
	call, f := newOllamaServer(t)
	f.mod.SetProgramResolver(func(string) string { return "" })
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	f.cfg.Container = &OllamaContainer{} // runtime discovered, not named
	f.mod.UseOllama(f.cfg)
	f.mod.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	if code != 409 {
		t.Fatalf("start with no runtime = %d %v, want 409", code, st)
	}
	msg := st["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "Podman") || !strings.Contains(msg, "Docker") {
		t.Fatalf("message = %q, want the runtimes to install", msg)
	}
	if _, err := os.Stat(f.cfg.StateFile); !os.IsNotExist(err) {
		t.Fatalf("a refused start was remembered: %v", err)
	}
	// A node whose composition wired no container flavour refuses it too.
	f.cfg.Container = nil
	f.mod.UseOllama(f.cfg)
	code, st = call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "container") {
		t.Fatalf("start with no container flavour = %d %v, want 409 and the reason", code, st)
	}
	if listening(f.addr) {
		t.Fatal("a refused container start left something listening")
	}
}

// The image is a setting: a pinned name is run as given, and a value that would be
// read as an option or has whitespace is refused.
func TestOllamaContainerImageSetting(t *testing.T) {
	t.Run("an image named in the setting is the one run", func(t *testing.T) {
		t.Setenv(OllamaContainerImageEnv, "registry.example/team/ollama:7")
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{})
		call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		waitOllama(t, call, "running")
		args := oneContainerRun(t, f.containerLog)
		if !argsHave(args, "registry.example/team/ollama:7", "serve") {
			t.Fatalf("the runtime ran %q, want the image from the setting", args)
		}
	})
	t.Run("an image that is not a name is refused", func(t *testing.T) {
		t.Setenv(OllamaContainerImageEnv, "a b")
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{})
		code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "image") {
			t.Fatalf("start with a bad image = %d %v, want 409 and the image name", code, st)
		}
	})
}

// the regression: "and the GPU where the host has one". The composition names the host GPU;
// the container is given it with the flag its runtime takes, and an AMD host with no
// named image runs Ollama's ROCm build.
func TestOllamaContainerGetsTheHostGPUItWasGiven(t *testing.T) {
	for _, c := range []struct {
		gpu  string
		want [][]string
	}{
		{"nvidia", [][]string{{"--gpus", "all"}}},
		{"amd", [][]string{{"--device", "/dev/kfd"}, {"--device", "/dev/dri"}, {"--group-add", "video"}}},
	} {
		t.Run(c.gpu, func(t *testing.T) {
			call, f := newOllamaServer(t)
			f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test", GPU: c.gpu})
			call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
			waitOllama(t, call, "running")
			args := oneContainerRun(t, f.containerLog)
			for _, seq := range c.want {
				if !argsHave(args, seq...) {
					t.Errorf("the %s GPU needs %q\n  got %q", c.gpu, seq, args)
				}
			}
		})
	}
	t.Run("an AMD host with no named image runs the ROCm build", func(t *testing.T) {
		t.Setenv(OllamaContainerImageEnv, "")
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{GPU: "amd"})
		call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		waitOllama(t, call, "running")
		args := oneContainerRun(t, f.containerLog)
		if !argsHave(args, "docker.io/ollama/ollama:rocm", "serve") {
			t.Fatalf("the AMD image = %q, want the ROCm build", args)
		}
	})
}

// A container the previous engine left behind holds the name and the port: the next
// start removes it first, so a killed engine does not make the service unstartable.
func TestOllamaContainerRemovesAStaleContainerBeforeStarting(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	waitOllama(t, call, "running")
	calls := containerCalls(t, f.containerLog)
	if len(calls) < 3 || calls[0][0] != "inspect" || !argsHave(calls[1], "rm", "-f", "olivares-ollama") || !argsHave(calls[2], "run") {
		t.Fatalf("calls = %q, want ownership checked and the stale container removed before the run", calls)
	}
}

// Stop must remove a container no live process of this engine owns: after a killed
// engine the daemon keeps serving, and a Stop that only answers "stopped" would leave
// it holding the endpoint.
func TestOllamaContainerStopRemovesAnOrphanedContainer(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	f.rememberContainerStart(t) // the killed engine's record is what says a container may exist
	code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
	if code != 200 || st["state"] != "stopped" {
		t.Fatalf("stop = %d %v, want stopped", code, st)
	}
	calls := containerCalls(t, f.containerLog)
	if len(calls) != 2 || calls[0][0] != "inspect" || !argsHave(calls[1], "rm", "-f", "olivares-ollama") {
		t.Fatalf("calls = %q, want ownership checked and the orphaned container removed", calls)
	}
	if _, err := os.Stat(f.cfg.StateFile); !os.IsNotExist(err) {
		t.Fatalf("the state file survived the stop: %v", err)
	}
}

// A start whose record could not be written leaves a container the engine cannot name
// from the state file, so Stop checks the engine's fixed container name even with no
// remembered container start. A surviving container is never reported as stopped; a
// node where containers are off has none to remove.
func TestOllamaContainerStopChecksTheFixedNameWithoutAContainerStart(t *testing.T) {
	t.Run("no such container is a clean stop", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		if err := os.WriteFile(f.containerLog+".missing-rm", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 || st["state"] != "stopped" {
			t.Fatalf("stop = %d %v, want stopped", code, st)
		}
		calls := containerCalls(t, f.containerLog)
		if len(calls) != 2 || calls[0][0] != "inspect" || !argsHave(calls[1], "rm", "-f", "olivares-ollama") {
			t.Fatalf("calls = %q, want ownership checked and the fixed name removed", calls)
		}
	})
	t.Run("a removal that fails with nothing to show a container ran is not a refusal", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		if err := os.WriteFile(f.containerLog+".fail-rm", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 || st["state"] != "stopped" {
			t.Fatalf("stop = %d %v, want stopped: no record says a container ran", code, st)
		}
	})
	t.Run("a node with containers off has nothing to remove", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		f.cfg.Container.Runtime = ""
		t.Setenv(sessions.ContainerRuntimeEnv, "off")
		f.mod.UseOllama(f.cfg)
		if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 || st["state"] != "stopped" {
			t.Fatalf("stop = %d %v, want stopped", code, st)
		}
		if calls := containerCalls(t, f.containerLog); len(calls) != 0 {
			t.Fatalf("the runtime was called with containers off: %q", calls)
		}
	})
}

// The shipped default is the installed program, and boot composes the container flavour
// with no runtime. A person who never asked for a container still gets the Stop they had
// before it existed, even where the docker CLI is on PATH but cannot reach its daemon
// (user not in the docker group, daemon down) or the runtime setting names a program that
// is not installed. Only a record that says a container ran, or one that cannot be read,
// keeps the refusal: there a surviving container is the real risk.
func TestOllamaStopWithoutAContainerStartIgnoresAnUnreachableContainerCLI(t *testing.T) {
	t.Setenv(sessions.ContainerRuntimeEnv, "") // the host's own setting must not pick the path
	// daemonDown gives the fixture a docker CLI that cannot reach its daemon, and says
	// whether it was ever asked: a test that never reaches the CLI proves nothing.
	daemonDown := func(t *testing.T) (call func(string, string, any) (int, map[string]any), f *ollamaFixture, asked func() bool) {
		t.Helper()
		call, f = newOllamaServer(t)
		dir := t.TempDir()
		docker, marker := filepath.Join(dir, "docker"), filepath.Join(dir, "asked")
		script := "#!/bin/sh\ntouch '" + marker + "'\necho 'permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock' >&2\nexit 1\n"
		if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		f.mod.lookPath = func(name string) (string, error) {
			if name == "docker" {
				return docker, nil
			}
			return "", errors.New("not found")
		}
		f.cfg.Container = &OllamaContainer{} // as boot.go composes it: no runtime, no image
		f.mod.UseOllama(f.cfg)
		return call, f, func() bool { _, err := os.Stat(marker); return err == nil }
	}
	stopped := func(t *testing.T, call func(string, string, any) (int, map[string]any)) {
		t.Helper()
		code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
		if code != 200 || st["state"] != "stopped" {
			t.Fatalf("stop = %d %v, want 200 stopped", code, st)
		}
		if msg, _ := st["message"].(string); msg != "" {
			t.Fatalf("message = %q, want none: this person never asked for a container", msg)
		}
	}
	t.Run("never started", func(t *testing.T) {
		call, _, asked := daemonDown(t)
		logs := captureLog(t)
		stopped(t, call)
		if !asked() {
			t.Fatal("the container CLI was never asked, so this case does not reach the removal it protects")
		}
		if !strings.Contains(logs.String(), "permission denied") {
			t.Fatalf("log = %q, want what the CLI answered: the stop stands but the failure is not dropped", logs.String())
		}
	})
	t.Run("the installed program stopped twice", func(t *testing.T) {
		call, _, asked := daemonDown(t)
		if code, st := call("POST", "/v1/m/agenttools/ollama/start", nil); code != 202 {
			t.Fatalf("start = %d %v", code, st)
		}
		waitOllama(t, call, "running")
		stopped(t, call)
		stopped(t, call)
		if !asked() {
			t.Fatal("the second stop never asked the container CLI")
		}
	})
	t.Run("a record that names the installed program needs no container CLI", func(t *testing.T) {
		call, f, asked := daemonDown(t)
		if err := os.WriteFile(f.cfg.StateFile, []byte(`{"container":false}`), 0o600); err != nil {
			t.Fatal(err)
		}
		stopped(t, call)
		if asked() {
			t.Fatal("the container CLI was asked although the record proves the installed program ran")
		}
	})
	t.Run("a runtime setting naming a program that is not installed", func(t *testing.T) {
		call, _, _ := daemonDown(t)
		t.Setenv(sessions.ContainerRuntimeEnv, "podman")
		stopped(t, call)
	})
	t.Run("a runtime setting that is not a runtime", func(t *testing.T) {
		call, _, _ := daemonDown(t)
		t.Setenv(sessions.ContainerRuntimeEnv, "lxc")
		stopped(t, call)
	})
	ran, unreadable := `{"container":true,"image":"docker.io/ollama/ollama:test"}`, "{not json"
	for _, tc := range []struct{ name, record, runtime, want string }{
		{"a record that says a container ran", ran, "", "could not be removed"},
		{"a record that cannot be read", unreadable, "", "could not be removed"},
		{"a record that says a container ran, with a runtime setting that is not a runtime", ran, "lxc", `is "lxc"`},
		{"a record that cannot be read, with a runtime setting that is not a runtime", unreadable, "lxc", `is "lxc"`},
	} {
		t.Run(tc.name+" keeps the refusal", func(t *testing.T) {
			call, f, _ := daemonDown(t)
			if tc.runtime != "" {
				t.Setenv(sessions.ContainerRuntimeEnv, tc.runtime)
			}
			if err := os.WriteFile(f.cfg.StateFile, []byte(tc.record), 0o600); err != nil {
				t.Fatal(err)
			}
			code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
			if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), tc.want) {
				t.Fatalf("stop = %d %v, want 409 containing %q", code, st, tc.want)
			}
		})
	}
}

// The row of an engine that ran the container and could not remove it promises a retry
// ("Stop still retries the removal"), and the record of that start may be gone by then
// (deleted by hand, or lost with the disk). The retry must still refuse while the
// removal fails, or the person is told "stopped" about a container the engine itself
// knows it left behind.
func TestOllamaStopRetriesAContainerThisEngineRanWhoseRecordIsGone(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	waitOllama(t, call, "running")
	// The start was remembered (a start that cannot be is refused); the file is then lost
	// while this engine still holds the container.
	if err := os.Remove(f.cfg.StateFile); err != nil {
		t.Fatal(err)
	}
	marker := f.containerLog + ".fail-rm"
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []string{"live stop", "retry with no record"} {
		code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
		if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "could not be removed") {
			t.Fatalf("%s = %d %v, want 409 and the removal failure", attempt, code, st)
		}
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 || st["state"] != "stopped" {
		t.Fatalf("stop once the removal works = %d %v, want stopped", code, st)
	}
}

// The state file says the installed program ran while this engine holds a container it
// started and could not remove (the record was restored from an older copy, or edited
// by hand). The row is the evidence, so the retry must still refuse: the stale record
// is no proof that nothing runs.
func TestOllamaStopRetriesAContainerThisEngineRanOverAStaleRecord(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	waitOllama(t, call, "running")
	if err := os.WriteFile(f.cfg.StateFile, []byte(`{"container":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := f.containerLog + ".fail-rm"
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []string{"live stop", "retry over the stale record"} {
		code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
		if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "could not be removed") {
			t.Fatalf("%s = %d %v, want 409 and the removal failure", attempt, code, st)
		}
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 || st["state"] != "stopped" {
		t.Fatalf("stop once the removal works = %d %v, want stopped", code, st)
	}
}

// A record that says a container ran, on a node that can no longer drive any runtime,
// stops cleanly but says on the row that a container may still be serving: the stop
// stands, the silence would not.
func TestOllamaContainerStopWarnsWhenNoRuntimeCanRemoveTheContainer(t *testing.T) {
	stopExpectsWarning := func(t *testing.T, call func(string, string, any) (int, map[string]any)) {
		t.Helper()
		code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
		if code != 200 || st["state"] != "stopped" {
			t.Fatalf("stop = %d %v, want stopped", code, st)
		}
		if msg, _ := st["message"].(string); !strings.Contains(msg, "may still be serving") {
			t.Fatalf("message = %q, want the surviving-container warning", msg)
		}
	}
	t.Run("the flavour is gone from the composition", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		raw := `{"container":true,"image":"docker.io/ollama/ollama:test"}`
		if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		f.cfg.Container = nil
		f.mod.UseOllama(f.cfg)
		stopExpectsWarning(t, call)
	})
	t.Run("containers are switched off after the start", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		raw := `{"container":true,"image":"docker.io/ollama/ollama:test"}`
		if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		f.cfg.Container.Runtime = ""
		t.Setenv(sessions.ContainerRuntimeEnv, "off")
		f.mod.UseOllama(f.cfg)
		stopExpectsWarning(t, call)
		if calls := containerCalls(t, f.containerLog); len(calls) != 0 {
			t.Fatalf("the runtime was called with containers off: %q", calls)
		}
	})
	t.Run("an unreadable record with no flavour", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		if err := os.WriteFile(f.cfg.StateFile, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.cfg.Container = nil
		f.mod.UseOllama(f.cfg)
		stopExpectsWarning(t, call)
	})
}

// A legacy record that names no runtime cannot say which daemon ran the container, so
// even a removal that succeeds through today's runtime is not proof: the stop stands,
// warns, and keeps the record for the retry.
func TestOllamaContainerStopTreatsALegacyRecordAsUnproven(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	raw := `{"container":true,"image":"docker.io/ollama/ollama:test"}`
	if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
	if code != 200 || st["state"] != "stopped" {
		t.Fatalf("stop = %d %v, want stopped", code, st)
	}
	if msg, _ := st["message"].(string); !strings.Contains(msg, "may still be serving") {
		t.Fatalf("message = %q, want the unproven-removal warning", msg)
	}
	if _, err := os.Stat(f.cfg.StateFile); err != nil {
		t.Fatalf("the unproven stop forgot the record: %v", err)
	}
	rms := 0
	for _, c := range containerCalls(t, f.containerLog) {
		if len(c) > 0 && c[0] == "rm" {
			rms++
		}
	}
	if rms == 0 {
		t.Fatalf("no removal ran, so nothing was proven or warned about")
	}
}

// A removal that really fails is reported, not swallowed: a start says why the stale
// container blocks it, and a Stop refuses to claim a stop that did not happen.
func TestOllamaContainerReportsARemovalFailure(t *testing.T) {
	fail := func(t *testing.T, f *ollamaFixture) {
		t.Helper()
		if err := os.WriteFile(f.containerLog+".fail-rm", nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("start", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		fail(t, f)
		code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "could not be removed") {
			t.Fatalf("start = %d %v, want 409 and the removal failure", code, st)
		}
		if runs := containerRuns(t, f.containerLog); len(runs) != 0 {
			t.Fatalf("the runtime ran although the stale container stayed: %q", runs)
		}
		if _, err := os.Stat(f.cfg.StateFile); !os.IsNotExist(err) {
			t.Fatalf("the failed first start left a record behind: %v", err)
		}
	})
	t.Run("stop, the record cannot be read", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		if err := os.WriteFile(f.cfg.StateFile, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		fail(t, f)
		code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
		if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "could not be removed") {
			t.Fatalf("stop = %d %v, want 409 and the removal failure", code, st)
		}
	})
	t.Run("stop, the remembered runtime is gone", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		record := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, filepath.Join(f.bin, "gone-runtime"))
		if err := os.WriteFile(f.cfg.StateFile, []byte(record), 0o600); err != nil {
			t.Fatal(err)
		}
		code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
		if code != 200 || !strings.Contains(st["message"].(string), "may still be serving") {
			t.Fatalf("stop = %d %v, want the unproven-cleanup warning", code, st)
		}
		if calls := containerCalls(t, f.containerLog); len(calls) != 0 {
			t.Fatalf("an unproven runtime was called: %q", calls)
		}
	})
	t.Run("stop", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		f.rememberContainerStart(t)
		fail(t, f)
		code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
		if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "could not be removed") {
			t.Fatalf("stop = %d %v, want 409 and the removal failure", code, st)
		}
		if _, err := os.Stat(f.cfg.StateFile); err != nil {
			t.Fatalf("the start was forgotten although the container stayed: %v", err)
		}
	})
}

// A start that fails after its record was written restores what the record said before:
// an earlier record may be the only thing that says a container serves, and the failed
// start's own values (a runtime or image that never served) must not replace them.
func TestOllamaContainerStartRestoresTheEarlierRecordOnFailure(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	previous := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:earlier"}`, f.containerCLI)
	if err := os.WriteFile(f.cfg.StateFile, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.containerLog+".fail-rm", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "could not be removed") {
		t.Fatalf("start = %d %v, want 409 and the removal failure", code, st)
	}
	kept, err := os.ReadFile(f.cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kept), "docker.io/ollama/ollama:earlier") {
		t.Fatalf("the failed start left its own record %s instead of the earlier one", kept)
	}
}

// The other flavour is a different service: asking for it while one runs is refused
// instead of answering "running" for a request that was never applied.
func TestOllamaContainerRefusesAFlavourSwitchWhileRunning(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	waitOllama(t, call, "running")
	code, st := call("POST", "/v1/m/agenttools/ollama/start", nil)
	if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "already running") {
		t.Fatalf("switch while running = %d %v, want 409 and the reason", code, st)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("same flavour while running = %d %v, want 202", code, st)
	}
}

// A container that exits at once leaves the row failed with no runtime: the service
// did not run, so the row must not claim the node can serve it.
func TestOllamaContainerFailureClearsTheRuntime(t *testing.T) {
	call, f := newOllamaServer(t)
	f.mod.SetProgramResolver(func(string) string { return "" })
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.containerLog+".fail-run", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	st := waitOllama(t, call, "failed")
	if st["runtime"] != nil {
		t.Fatalf("runtime after a failed start = %v, want none", st["runtime"])
	}
	if installed, _ := st["installed"].(bool); installed {
		t.Fatalf("installed = true after a failed start: %v", st)
	}
}

// An endpoint that answers is not proof that this engine's container serves it: during
// an image pull anything on the loopback address can answer first. When the runtime
// cannot confirm the engine's own container is running, the start fails and the
// endpoint is never registered as the tenant's provider.
func TestOllamaContainerIsVerifiedBeforeRegistration(t *testing.T) {
	for _, tc := range []struct{ name, marker, reason string }{
		{"the container is not running", ".inspect-stopped", "not running"},
		{"the runtime cannot inspect it", ".inspect-missing", "could not be confirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call, f := newOllamaServer(t)
			f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
			if err := os.WriteFile(f.containerLog+tc.marker, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
				t.Fatalf("start = %d %v", code, st)
			}
			st := waitOllama(t, call, "failed")
			if msg, _ := st["message"].(string); !strings.Contains(msg, "could not confirm") || !strings.Contains(msg, tc.reason) {
				t.Fatalf("message = %q, want the verification failure naming %q", msg, tc.reason)
			}
			if got := f.registered(); got != "" {
				t.Fatalf("registered %q although the engine's container was not confirmed", got)
			}
		})
	}
}

// An image that starts with '-' would be read by Podman or Docker as an option, so it is
// refused before the runtime is called.
func TestOllamaContainerRefusesAnOptionAsImage(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "--privileged"})
	code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "is not an image name") {
		t.Fatalf("start = %d %v, want 409 and the image refusal", code, st)
	}
	if calls := containerCalls(t, f.containerLog); len(calls) != 0 {
		t.Fatalf("the runtime was called with an option as the image: %q", calls)
	}
}

// Docker names the engine user on the command line, and an SELinux host relabels the
// models mount; a regression in either would run the container as the wrong user or
// without access to the mount.
func TestOllamaContainerDockerAndSELinuxArguments(t *testing.T) {
	t.Run("docker runs as the engine user", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		docker := filepath.Join(f.bin, "docker")
		writeFakeContainerCLI(t, docker, self, f.containerLog)
		f.cfg.Container.Runtime = docker
		f.mod.UseOllama(f.cfg)
		call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		waitOllama(t, call, "running")
		args := oneContainerRun(t, f.containerLog)
		if argsHave(args, "--userns=keep-id") {
			t.Errorf("docker was given podman's rootless user namespace: %q", args)
		}
		want := fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
		if !argsHave(args, "--user", want) {
			t.Errorf("docker lacks --user %s: %q", want, args)
		}
	})
	t.Run("SELinux relabels the models mount", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test", SELinux: true})
		call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		waitOllama(t, call, "running")
		args := oneContainerRun(t, f.containerLog)
		if !argsHave(args, "-v", f.models+":/models:z") {
			t.Errorf("the models mount is not relabeled: %q", args)
		}
	})
	t.Run("rootless podman keeps the engine user's id", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("the engine user is root here, so the rootless mapping does not apply")
		}
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"}) // the fake CLI is named podman
		call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		waitOllama(t, call, "running")
		args := oneContainerRun(t, f.containerLog)
		if !argsHave(args, "--userns=keep-id") {
			t.Errorf("rootless podman lacks --userns=keep-id: %q", args)
		}
		if argsHave(args, "--user") {
			t.Errorf("podman was given docker's --user: %q", args)
		}
	})
	t.Run("podman-remote counts as podman", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("the engine user is root here, so the rootless mapping does not apply")
		}
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		remote := filepath.Join(f.bin, "podman-remote")
		writeFakeContainerCLI(t, remote, self, f.containerLog)
		f.cfg.Container.Runtime = remote
		f.mod.UseOllama(f.cfg)
		call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		waitOllama(t, call, "running")
		args := oneContainerRun(t, f.containerLog)
		if !argsHave(args, "--userns=keep-id") || argsHave(args, "--user") {
			t.Errorf("podman-remote was not treated as podman: %q", args)
		}
	})
}

// A models directory that cannot be written as one -v volume (a relative path, or one
// holding a colon) is refused before the runtime is called, instead of mounting
// something else.
func TestOllamaContainerRefusesAModelsDirItCannotMount(t *testing.T) {
	for name, dir := range map[string]string{
		"a relative path":     "rel/models",
		"a path with a colon": filepath.Join(t.TempDir(), "we:ird"),
	} {
		t.Run(name, func(t *testing.T) {
			call, f := newOllamaServer(t)
			f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
			f.cfg.ModelsDir = dir
			f.mod.UseOllama(f.cfg)
			code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
			if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "cannot be mounted") {
				t.Fatalf("start = %d %v, want 409 and the mount refusal", code, st)
			}
			if calls := containerCalls(t, f.containerLog); len(calls) != 0 {
				t.Fatalf("the runtime was called with an unmountable models dir: %q", calls)
			}
		})
	}
}

// A configured runtime that is not an absolute executable path is refused: a relative
// name or one with a space is a setting, not a program to exec.
func TestOllamaContainerRefusesARuntimeThatIsNotAProgram(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	f.cfg.Container.Runtime = "podman --something" // a setting, not a program
	f.mod.UseOllama(f.cfg)
	code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "absolute executable path") {
		t.Fatalf("start = %d %v, want 409 and the runtime refusal", code, st)
	}
	if calls := containerCalls(t, f.containerLog); len(calls) != 0 {
		t.Fatalf("the runtime was called although it was refused: %q", calls)
	}
}

// An absolute executable that is not Podman or Docker is refused just the same: the
// engine will not drive the fixed-name container's lifecycle with an arbitrary program.
func TestOllamaContainerRefusesARuntimeThatIsNotAContainerCLI(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	f.cfg.Container.Runtime = "/bin/false" // absolute and executable, but no container CLI
	f.mod.UseOllama(f.cfg)
	code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "not Podman or Docker") {
		t.Fatalf("start = %d %v, want 409 and the family refusal", code, st)
	}
	if calls := containerCalls(t, f.containerLog); len(calls) != 0 {
		t.Fatalf("the runtime was called although it was refused: %q", calls)
	}
}

// Stop is refused on a read-only control plane, as Start is: a plane that cannot write
// cannot honestly claim it ended the service and cleaned up its container.
func TestOllamaStopRefusesOnAReadOnlyControlPlane(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	f.mod.readOnly = true
	code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
	if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "cannot stop services") {
		t.Fatalf("stop on a read-only plane = %d %v, want 409", code, st)
	}
}

// A restart whose start record cannot be read says so on the row and starts nothing:
// the record may name a running container, and silence would hide it.
func TestOllamaContainerRestartSaysSoWhenTheStartRecordCannotBeRead(t *testing.T) {
	_, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.cfg.StateFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	next := f.nextEngineRecording(t, auditStore(t), func(model.AuditDraft) {})
	defer next.Close()
	st := next.ollamaStatus(context.Background())
	if st.State != ollamaFailed || !strings.Contains(st.Message, "cannot be read") || !strings.Contains(st.Message, "may still be serving") {
		t.Fatalf("row after an unreadable record = %+v, want failed, the reason and the container warning", st)
	}
	if runs := containerRuns(t, f.containerLog); len(runs) != 0 {
		t.Fatalf("a restart started a container from an unreadable record: %q", runs)
	}
}

// The engine does not start a service it cannot name: a start whose record cannot be
// kept is refused before anything runs, so no container can outlive an engine that
// cannot identify it. A directory where the record goes fails the write for every uid.
func TestOllamaContainerStartIsRefusedWhenTheRecordCannotBeKept(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.Mkdir(f.cfg.StateFile, 0o700); err != nil {
		t.Fatal(err)
	}
	code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "could not be kept") {
		t.Fatalf("start = %d %v, want 409 and the record failure", code, st)
	}
	if runs := containerRuns(t, f.containerLog); len(runs) != 0 {
		t.Fatalf("a start that cannot be remembered ran a container: %q", runs)
	}
	if _, err := os.Stat(f.cfg.StateFile + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("the refused start left its temporary record behind: %v", err)
	}
}

// A start over a record that cannot be read proceeds (this start's record replaces it),
// but the row must not read clean: a container from that unreadable start may still be
// serving on another connection, and the failed-start path already says the same thing.
func TestOllamaContainerStartNotesAnUnreadableEarlierRecord(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.cfg.StateFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d, want 202 over an unreadable earlier record", code)
	}
	st := waitOllama(t, call, "running")
	if msg, _ := st["message"].(string); !strings.Contains(msg, "could not be read") {
		t.Fatalf("message = %q, want the unreadable-record note to survive the start", msg)
	}
}

// A pull that ends with a failing re-registration leaves a note on the row: without it
// the tenant's record silently lacks the new model.
func TestOllamaContainerPullNotesAFailingReRegistration(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	org := seedOrg(t, f.store, "Second", "second") // re-registration runs for a named tenant only
	if code, _ := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true, "tenant_id": org.String()}); code != 202 {
		t.Fatal("start refused")
	}
	waitOllama(t, call, "running")
	f.cfg.Register = func(context.Context, auth.Principal, model.TenantID, string) error {
		return errors.New("the record is sealed")
	}
	f.mod.UseOllama(f.cfg)
	code, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"})
	if code != 202 {
		t.Fatalf("pull = %d %v", code, p)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, st := call("GET", "/v1/m/agenttools/ollama", nil)
		// The cause is part of the note: "could not be added" alone leaves the operator
		// unable to tell a sealed record from a refused write.
		if msg, _ := st["message"].(string); strings.Contains(msg, "could not be added to Providers") && strings.Contains(msg, "the record is sealed") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the row never noted the failed re-registration: %v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A Stop whose record names a runtime whose program is gone warns instead of failing
// forever: the missing binary proves nothing can be driven, and the stop stands.
func TestOllamaContainerStopWhenTheRememberedRuntimeIsGone(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	gone := filepath.Join(f.bin, "gone-runtime")
	raw := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, gone)
	if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	f.cfg.Container.Runtime = "" // a configured runtime would hide the remembered one's fate
	f.mod.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	f.mod.UseOllama(f.cfg)
	code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
	if code != 200 || st["state"] != "stopped" {
		t.Fatalf("stop = %d %v, want stopped", code, st)
	}
	if msg, _ := st["message"].(string); !strings.Contains(msg, "may still be serving") {
		t.Fatalf("message = %q, want the surviving-container warning", msg)
	}
}

// A container name is unique per daemon, not per host: when the recorded runtime is
// gone and Stop drives another one, that CLI answers about its own daemon. Its "rm"
// succeeding (or saying "no such container") proves nothing about the container the
// missing runtime still owns, so the stop stands with the warning, never in silence.
func TestOllamaContainerStopDoesNotTakeAnotherRuntimesRemovalAsProof(t *testing.T) {
	for _, tc := range []struct {
		name            string
		noSuchContainer bool
	}{
		{"the other runtime removes something", false},
		{"the other runtime answers no such container", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call, f := newOllamaServer(t)
			f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
			gone := filepath.Join(f.bin, "gone-runtime")
			raw := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, gone)
			if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.noSuchContainer {
				if err := os.WriteFile(f.containerLog+".missing-rm", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
			if code != 200 || st["state"] != "stopped" {
				t.Fatalf("stop = %d %v, want stopped", code, st)
			}
			if msg, _ := st["message"].(string); !strings.Contains(msg, "may still be serving") {
				t.Fatalf("message = %q, want the surviving-container warning", msg)
			}
			calls := containerCalls(t, f.containerLog)
			if len(calls) != 0 {
				t.Fatalf("calls = %q, want no removal through the unproven runtime", calls)
			}
		})
	}
}

// A warning means removal was not proven. The record must survive so a second Stop
// cannot turn the same surviving container into a clean, silent stop.
func TestOllamaContainerStopKeepsUnprovenCleanupRemembered(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	f.cfg.Audit = func(context.Context, model.AuditDraft) error { return nil }
	f.mod.UseOllama(f.cfg)
	gone := filepath.Join(f.bin, "gone-runtime")
	raw := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, gone)
	if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
		if code != 200 || st["state"] != "stopped" {
			t.Fatalf("stop %d = %d %v, want stopped with a warning", attempt, code, st)
		}
		if msg, _ := st["message"].(string); !strings.Contains(msg, "may still be serving") {
			t.Fatalf("stop %d message = %q, want the surviving-container warning", attempt, msg)
		}
		if _, err := os.Stat(f.cfg.StateFile); err != nil {
			t.Fatalf("stop %d forgot the unproven cleanup: %v", attempt, err)
		}
	}
	f.mod.RestartOllama()
	// The row is the verdict: a start would be asynchronous, so a container count taken
	// right after the restart could pass before the container ever ran.
	if _, row := call("GET", "/v1/m/agenttools/ollama", nil); row["state"] != "failed" || !strings.Contains(row["message"].(string), "could not prove") {
		t.Fatalf("row after the restart = %v, want the failure that names the unproven removal", row)
	}
	if runs := containerRuns(t, f.containerLog); len(runs) != 0 {
		t.Fatalf("an unproven Stop revived Ollama on engine restart: %q", runs)
	}
	if _, err := os.Stat(f.cfg.StateFile); err != nil {
		t.Fatalf("restart discarded the cleanup-only record: %v", err)
	}
}

// The CLI path alone does not identify a daemon: its connection environment can
// change while the path stays the same. A changed connection is not removal proof.
func TestOllamaContainerStopDetectsDaemonConnectionChange(t *testing.T) {
	call, f := newOllamaServer(t)
	values := map[string]string{"DOCKER_HOST": "unix:///run/one.sock"}
	f.cfg.Getenv = func(name string) string { return values[name] }
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := f.mod.rememberOllama(model.TenantID(""), "test", true, f.containerCLI, "docker.io/ollama/ollama:test", false); err != nil {
		t.Fatal(err)
	}
	values["DOCKER_HOST"] = "unix:///run/two.sock"
	code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
	msg, _ := st["message"].(string)
	if code != 200 || !strings.Contains(msg, "may still be serving") {
		t.Fatalf("stop = %d %v, want the unproven-cleanup warning", code, st)
	}
	if _, err := os.Stat(f.cfg.StateFile); err != nil {
		t.Fatalf("stop forgot the original daemon's record: %v", err)
	}
	for _, c := range containerCalls(t, f.containerLog) {
		if len(c) > 0 && c[0] == "rm" {
			t.Fatalf("stop removed a same-named container through the changed daemon: %q", containerCalls(t, f.containerLog))
		}
	}
}

// Restart must not remove or replace a container through a different daemon
// connection merely because the CLI path is unchanged.
func TestOllamaContainerRestartRefusesChangedDaemonConnection(t *testing.T) {
	call, f := newOllamaServer(t)
	values := map[string]string{"DOCKER_HOST": "unix:///run/one.sock"}
	f.cfg.Getenv = func(name string) string { return values[name] }
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	f.cfg.Audit = func(context.Context, model.AuditDraft) error { return nil }
	f.mod.UseOllama(f.cfg)
	if err := f.mod.rememberOllama(model.TenantID(""), "test", true, f.containerCLI, "docker.io/ollama/ollama:test", false); err != nil {
		t.Fatal(err)
	}
	values["DOCKER_HOST"] = "unix:///run/two.sock"
	f.mod.RestartOllama()
	if runs := containerRuns(t, f.containerLog); len(runs) != 0 {
		t.Fatalf("restart ran through the changed daemon connection: %q", runs)
	}
	_, row := call("GET", "/v1/m/agenttools/ollama", nil)
	if msg, _ := row["message"].(string); !strings.Contains(msg, "connection changed") {
		t.Fatalf("restart row = %v, want the changed-connection warning", row)
	}
}

func TestOllamaRuntimeIdentityDistinguishesPodmanRemote(t *testing.T) {
	if sameRuntimeDaemon("/usr/bin/podman", "/usr/bin/podman-remote") {
		t.Fatal("local podman and podman-remote were treated as proof of the same daemon")
	}
}

// A fixed name is not ownership. A manually managed or another engine's container
// must survive both a start attempt and the failed start's cleanup.
func TestOllamaContainerStartRefusesForeignFixedName(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.containerLog+".inspect-foreign", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 409 {
		t.Fatalf("start = %d, want 409 for the foreign fixed-name container", code)
	}
	calls := containerCalls(t, f.containerLog)
	if runs := containerRuns(t, f.containerLog); len(runs) != 0 {
		t.Fatalf("start replaced the foreign container: %q", calls)
	}
	for _, c := range calls {
		if len(c) > 0 && c[0] == "rm" {
			t.Fatalf("start removed the foreign container: %q", calls)
		}
	}
}

// Stop without a container start stays idempotent, but it must not delete a
// foreign container that happens to hold the product's fixed name.
func TestOllamaContainerStopPreservesForeignFixedName(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.containerLog+".inspect-foreign", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 || st["state"] != "stopped" {
		t.Fatalf("stop = %d %v, want the ordinary idempotent stop", code, st)
	}
	for _, c := range containerCalls(t, f.containerLog) {
		if len(c) > 0 && c[0] == "rm" {
			t.Fatalf("stop removed the foreign container: %q", containerCalls(t, f.containerLog))
		}
	}
}

// An arbitrary resource whose error says "does not exist" is not proof that the
// Ollama container is absent. Only the runtime's container-specific answer counts.
func TestOllamaContainerStopDoesNotAcceptAMissingContextAsRemoval(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	f.rememberContainerStart(t)
	if err := os.WriteFile(f.containerLog+".context-missing-rm", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
	if code != 409 {
		t.Fatalf("stop = %d %v, want 409 for the unproven removal", code, st)
	}
}

// A remembered container start is not replaced by the installed program: the old
// container would be forgotten while it still serves on the endpoint.
func TestOllamaContainerStartRefusesTheProcessFlavourOverAContainerRecord(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	f.rememberContainerStart(t)
	code, st := call("POST", "/v1/m/agenttools/ollama/start", nil)
	if code != 409 {
		t.Fatalf("start = %d %v, want 409 over an un-stopped container record", code, st)
	}
	if msg := st["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "Stop the previous Ollama container") {
		t.Fatalf("message = %q, want the stop-first refusal", msg)
	}
	if runs := containerRuns(t, f.containerLog); len(runs) != 0 {
		t.Fatalf("start ran %d times over the record, want 0", len(runs))
	}
}

// A start may retry or replace a container only through the connection that owns it:
// a remembered daemon whose connection environment changed is refused, not retried
// through today's, whose daemon knows nothing of the container that may still serve.
func TestOllamaContainerStartRefusesAChangedDaemonConnection(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	raw := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test","daemon":"%s"}`,
		f.containerCLI, strings.Repeat("0", 64))
	if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	if code != 409 {
		t.Fatalf("start = %d %v, want 409 over a changed daemon connection", code, st)
	}
	if msg := st["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "connection changed") {
		t.Fatalf("message = %q, want the changed-connection refusal", msg)
	}
	if runs := containerRuns(t, f.containerLog); len(runs) != 0 {
		t.Fatalf("start ran %d times over the record, want 0", len(runs))
	}
}

// The endpoint is registered only for this engine's own container. When another
// engine's or the operator's container holds the fixed name at verification time,
// the start fails and nothing is registered for the tenant.
func TestOllamaContainerNeverRegistersAnotherEngineContainer(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.containerLog+".inspect-foreign-after-run", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d %v, want 202", code, st)
	}
	st := waitOllama(t, call, "failed")
	if msg, _ := st["message"].(string); !strings.Contains(msg, "could not confirm") || !strings.Contains(msg, "another engine") {
		t.Fatalf("message = %q, want the verification failure naming the other engine", msg)
	}
	if got := f.registered(); got != "" {
		t.Fatalf("registered %q although the fixed-name container is another engine's", got)
	}
}

// Only the runtime the record names may be asked to remove the container: it is the one
// that created it. A second, working runtime configured today is never called, and a
// removal that fails through the recorded one still refuses the stop.
func TestOllamaContainerStopRemovesOnlyThroughTheRecordedRuntime(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	recordedLog := filepath.Join(t.TempDir(), "recorded-runtime.log")
	recorded := filepath.Join(t.TempDir(), "podman") // another path than the configured one, a real CLI name
	writeFakeContainerCLI(t, recorded, self, recordedLog)
	if err := os.WriteFile(recordedLog+".owner", []byte(ollamaContainerOwner(f.cfg.HomeDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, recorded)
	if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordedLog+".fail-rm", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
	if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "could not be removed") {
		t.Fatalf("stop = %d %v, want 409 and the removal failure", code, st)
	}
	// The row says what the caller was told: a console that missed the 409 must not read
	// a clean stop while the container may still serve.
	if _, row := call("GET", "/v1/m/agenttools/ollama", nil); row["state"] != "failed" || !strings.Contains(row["message"].(string), "did not stop") {
		t.Fatalf("row after the refused stop = %v, want the failure and its note", row)
	}
	calls := containerCalls(t, recordedLog)
	if len(calls) != 2 || calls[0][0] != "inspect" || !argsHave(calls[1], "rm", "-f", "olivares-ollama") {
		t.Fatalf("the recorded runtime calls = %q, want ownership checked and one removal", calls)
	}
	if calls := containerCalls(t, f.containerLog); len(calls) != 0 {
		t.Fatalf("the configured runtime was called although the record names another: %q", calls)
	}
}

// A Stop whose cleanup-only record cannot be written keeps the surviving-container
// warning with the record failure: two facts, one row, neither dropped.
func TestOllamaContainerStopCornerKeepsTheWarningWhenCleanupRecordFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes from a read-only directory, so the unforgotten record cannot be forced here")
	}
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	// The record names a runtime whose program is gone, so removal can only warn; and
	// the record sits in a read-only directory, so forgetting it fails too.
	gone := filepath.Join(f.bin, "gone-runtime")
	raw := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, gone)
	dir := t.TempDir()
	f.cfg.StateFile = filepath.Join(dir, "started.json")
	if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	f.cfg.Container.Runtime = "" // discovery would find a runtime; force the warning path
	f.mod.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	f.mod.UseOllama(f.cfg)
	code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
	if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "could not be kept") {
		t.Fatalf("stop = %d %v, want 409 not_forgotten", code, st)
	}
	_, row := call("GET", "/v1/m/agenttools/ollama", nil)
	msg, _ := row["message"].(string)
	if !strings.Contains(msg, "could not be kept") || !strings.Contains(msg, "may still be serving") {
		t.Fatalf("row = %q, want the cleanup-record failure and the container warning", msg)
	}
}

// A stop whose record cannot be kept appends to what the row already said: the warning
// an earlier unproven stop left (a container that may still be serving) must survive the
// record failure's own message, not be replaced by it.
func TestOllamaContainerStopKeepsAnEarlierNoteWhenTheRecordCannotBeKept(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes from a read-only directory, so the unforgotten record cannot be forced here")
	}
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	f.cfg.Audit = func(context.Context, model.AuditDraft) error { return nil }
	f.mod.UseOllama(f.cfg)
	dir := t.TempDir()
	f.cfg.StateFile = filepath.Join(dir, "started.json")
	gone := filepath.Join(f.bin, "gone-runtime")
	raw := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, gone)
	if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	f.mod.UseOllama(f.cfg)
	code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
	if code != 200 || !strings.Contains(st["message"].(string), "may still be serving") {
		t.Fatalf("first stop = %d %v, want the unproven-cleanup warning", code, st)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 409 {
		t.Fatalf("second stop = %d %v, want 409 not_forgotten", code, st)
	}
	_, row := call("GET", "/v1/m/agenttools/ollama", nil)
	msg, _ := row["message"].(string)
	if !strings.Contains(msg, "may still be serving") || !strings.Contains(msg, "could not be kept") {
		t.Fatalf("row = %q, want the earlier warning kept beside the record failure", msg)
	}
}

// A stop whose live removal fails keeps a note the row already carried: the record of
// an earlier start that could not be read stays after the stop refuses, and losing the
// note would let the row read clean while a container from that earlier start may
// still serve.
func TestOllamaContainerStopKeepsAnEarlierNoteWhenTheContainersRemovalFails(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.cfg.StateFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d, want 202 over an unreadable earlier record", code)
	}
	if st := waitOllama(t, call, "running"); !strings.Contains(st["message"].(string), "could not be read") {
		t.Fatalf("message = %v, want the unreadable-record note on the running row", st["message"])
	}
	if err := os.WriteFile(f.containerLog+".fail-rm", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 409 {
		t.Fatalf("stop = %d, want 409 for a removal that failed", code)
	}
	_, row := call("GET", "/v1/m/agenttools/ollama", nil)
	msg, _ := row["message"].(string)
	if !strings.Contains(msg, "could not be read") || !strings.Contains(msg, "could not be removed") {
		t.Fatalf("row = %q, want the earlier note kept beside the removal failure", msg)
	}
}

// A service that ends on its own keeps a note the row already carried: the record of
// an earlier start that could not be read survives the exit, and the row must not
// read like a plain exit while a container from that earlier start may still serve.
func TestOllamaContainerKeepsAnEarlierNoteWhenOllamaStopsOnItsOwn(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.cfg.StateFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d, want 202 over an unreadable earlier record", code)
	}
	if st := waitOllama(t, call, "running"); !strings.Contains(st["message"].(string), "could not be read") {
		t.Fatalf("message = %v, want the unreadable-record note on the running row", st["message"])
	}
	resp, err := http.Get("http://" + f.addr + "/fake/exit")
	if err != nil {
		t.Fatalf("the fake Ollama could not be asked to exit: %v", err)
	}
	resp.Body.Close()
	st := waitOllama(t, call, "failed")
	msg, _ := st["message"].(string)
	if !strings.Contains(msg, "could not be read") || !strings.Contains(msg, "stopped on its own") {
		t.Fatalf("row = %q, want the earlier note kept beside the self-stop reason", msg)
	}
}

// A first start on a clean host removes nothing: the runtime's "no such container" is
// success, or every start would fail before it runs the image.
func TestOllamaContainerFirstStartAcceptsNoSuchContainer(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.containerLog+".missing-rm", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d %v, want 202", code, st)
	}
	st := waitOllama(t, call, "running")
	if st["installed"] != true || st["runtime"] != "container" {
		t.Fatalf("status = %v, want an installed, running container", st)
	}
	if runs := containerRuns(t, f.containerLog); len(runs) != 1 {
		t.Fatalf("the runtime ran %d times, want 1: %q", len(runs), runs)
	}
}

// Two starts that arrive together must run the image once: the second sees the first's
// service, instead of racing the fixed container name and the port.
func TestOllamaContainerConcurrentStartsRunItOnce(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		}(i)
	}
	wg.Wait()
	for i, code := range codes {
		if code != 202 {
			t.Fatalf("start %d = %d, want 202", i, code)
		}
	}
	waitOllama(t, call, "running")
	if runs := containerRuns(t, f.containerLog); len(runs) != 1 {
		t.Fatalf("the runtime ran %d times, want 1: %q", len(runs), runs)
	}
}

// Stop of a live container that cannot be removed is a failure, not a clean stop; the
// start stays remembered, and the next Stop retries the removal instead of answering
// "stopped" forever.
func TestOllamaContainerLiveStopFailureIsReportedAndRetried(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	waitOllama(t, call, "running")
	marker := f.containerLog + ".fail-rm"
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
	if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "could not be removed") {
		t.Fatalf("stop = %d %v, want 409 and the removal failure", code, st)
	}
	if _, err := os.Stat(f.cfg.StateFile); err != nil {
		t.Fatalf("the start was forgotten although its container stayed: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 || st["state"] != "stopped" {
		t.Fatalf("retried stop = %d %v, want stopped", code, st)
	}
	rm := 0
	for _, c := range containerCalls(t, f.containerLog) {
		if len(c) > 0 && c[0] == "rm" {
			rm++
		}
	}
	if rm != 3 { // the start's probe, the failed live removal, the retry
		t.Fatalf("rm ran %d times, want 3: %q", rm, containerCalls(t, f.containerLog))
	}
}

// The CLI gets the allowlisted runtime variables and nothing else: an engine secret
// must not reach Podman or Docker, and the list is never nil (nil inherits everything).
func TestOllamaContainerClientEnvIsTheAllowlist(t *testing.T) {
	if got := ollamaContainerClientEnv(func(string) string { return "" }); got == nil || len(got) != 0 {
		t.Fatalf("empty environment = %#v, want a non-nil empty slice", got)
	}
	t.Setenv("OLIVARES_TEST_SECRET_SENTINEL", "must-not-reach-the-runtime")
	t.Setenv("DOCKER_HOST", "unix:///run/user/1000/docker.sock")
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.containerLog+".dump-env", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	waitOllama(t, call, "running")
	raw, err := os.ReadFile(f.containerLog + ".env")
	if err != nil {
		t.Fatal(err)
	}
	env := string(raw)
	if strings.Contains(env, "OLIVARES_TEST_SECRET_SENTINEL") {
		t.Fatalf("an engine variable reached the container CLI:\n%s", env)
	}
	if !strings.Contains(env, "DOCKER_HOST=unix:///run/user/1000/docker.sock") {
		t.Fatalf("the CLI's own runtime setting did not reach it:\n%s", env)
	}
}

// The effective environment is the setting's reader: an activation manifest can pin the
// image or switch containers off, and both are honored.
func TestOllamaContainerReadsTheEffectiveEnvironment(t *testing.T) {
	t.Run("the image", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{}) // no configured image: the setting decides
		f.cfg.Getenv = func(name string) string {
			if name == OllamaContainerImageEnv {
				return "docker.io/example/pinned:1"
			}
			return os.Getenv(name)
		}
		f.mod.UseOllama(f.cfg)
		call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		waitOllama(t, call, "running")
		if args := oneContainerRun(t, f.containerLog); !argsHave(args, "docker.io/example/pinned:1", "serve") {
			t.Fatalf("the image setting was ignored: %q", args)
		}
	})
	t.Run("off", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.cfg.Container = &OllamaContainer{Image: "docker.io/ollama/ollama:test"}
		f.cfg.Getenv = func(name string) string {
			if name == sessions.ContainerRuntimeEnv {
				return "off"
			}
			return os.Getenv(name)
		}
		f.mod.UseOllama(f.cfg)
		code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		if code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "switched off") {
			t.Fatalf("start = %d %v, want 409 and the switched-off reason", code, st)
		}
		if runs := containerRuns(t, f.containerLog); len(runs) != 0 {
			t.Fatalf("the runtime ran although containers are off: %q", runs)
		}
	})
}

// The flavour refusal names each direction, and the journey it asks for works: stop the
// container, then start the installed program, and the other way round.
func TestOllamaContainerFlavourSwitchAfterStop(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	waitOllama(t, call, "running")
	if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 {
		t.Fatalf("stop container = %d %v", code, st)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", nil); code != 202 {
		t.Fatalf("start process = %d %v", code, st)
	}
	st := waitOllama(t, call, "running")
	if st["runtime"] != "process" {
		t.Fatalf("runtime = %v, want process", st["runtime"])
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 409 ||
		!strings.Contains(st["error"].(map[string]any)["message"].(string), "already running as the installed program") {
		t.Fatalf("process-to-container switch = %d %v, want 409 naming the installed program", code, st)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 {
		t.Fatalf("stop process = %d %v", code, st)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start container after stopping the process = %d %v", code, st)
	}
	if st := waitOllama(t, call, "running"); st["runtime"] != "container" {
		t.Fatalf("runtime = %v, want container", st["runtime"])
	}
}

// The restart repeats the person's start: the state file names the runtime and image,
// and a setting changed since the start does not swap the image under the earlier
// consent.
func TestOllamaContainerRestartUsesTheRememberedImage(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	waitOllama(t, call, "running")
	raw, err := os.ReadFile(f.cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"container":true`, `"runtime":` + fmt.Sprintf("%q", f.containerCLI), `"image":"docker.io/ollama/ollama:test"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("state file %s lacks %s", raw, want)
		}
	}
	f.mod.Close()
	f.cfg.Container = &OllamaContainer{Runtime: f.containerCLI, Image: "docker.io/ollama/ollama:changed"}
	var drafts []model.AuditDraft
	next := f.nextEngineRecording(t, auditStore(t), func(d model.AuditDraft) { drafts = append(drafts, d) })
	if !ollamaStateWithin(next, ollamaRunning, 20*time.Second) {
		t.Fatalf("after the restart Ollama is %+v", next.ollamaStatus(context.Background()))
	}
	if len(drafts) != 1 || drafts[0].Meta["runtime"] != "container" || drafts[0].Meta["image"] != "docker.io/ollama/ollama:test" {
		t.Fatalf("restart row metadata = %+v, want the container and the image the person started", drafts)
	}
	runs := containerRuns(t, f.containerLog)
	if len(runs) != 2 {
		t.Fatalf("the runtime ran %d times, want the start and the restart: %q", len(runs), runs)
	}
	if !argsHave(runs[1], "docker.io/ollama/ollama:test", "serve") {
		t.Fatalf("the restart ran %q, want the image the person started", runs[1])
	}
	next.Close()
}

// The restart removes and runs on the runtime that actually created the container, not
// on a setting changed meanwhile: only the runtime that owns the container can remove
// it, and the person's earlier start named one.
func TestOllamaContainerRestartUsesTheRememberedRuntime(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	waitOllama(t, call, "running")
	f.mod.Close()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	otherLog := filepath.Join(t.TempDir(), "other-runtime.log")
	other := filepath.Join(f.bin, "docker")
	writeFakeContainerCLI(t, other, self, otherLog)
	f.cfg.Container = &OllamaContainer{Runtime: other, Image: "docker.io/ollama/ollama:test"}
	next := f.nextEngineRecording(t, auditStore(t), func(model.AuditDraft) {})
	if !ollamaStateWithin(next, ollamaRunning, 20*time.Second) {
		t.Fatalf("after the restart Ollama is %+v", next.ollamaStatus(context.Background()))
	}
	if calls := containerCalls(t, otherLog); len(calls) != 0 {
		t.Fatalf("the runtime changed after the start was called: %q", calls)
	}
	if runs := containerRuns(t, f.containerLog); len(runs) != 2 {
		t.Fatalf("the remembered runtime ran %d times, want the start and the restart: %q", len(runs), runs)
	}
	next.Close()
}

// A remembered runtime that is not an executable path is not evidence: a relative name
// or one with a space in it is ignored, and the restart falls back to the configured
// runtime (then today's discovery when none is configured) instead of running whatever
// the record names.
func TestOllamaContainerRestartIgnoresAnUnusableRememberedRuntime(t *testing.T) {
	for name, recorded := range map[string]string{
		"a relative name":   "podman",
		"an embedded space": "/bin/evil podman",
	} {
		t.Run(name, func(t *testing.T) {
			call, f := newOllamaServer(t)
			f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
			call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
			waitOllama(t, call, "running")
			raw := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, recorded)
			if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			f.mod.Close()
			next := f.nextEngineRecording(t, auditStore(t), func(model.AuditDraft) {})
			if !ollamaStateWithin(next, ollamaRunning, 20*time.Second) {
				t.Fatalf("after the restart Ollama is %+v", next.ollamaStatus(context.Background()))
			}
			runs := containerRuns(t, f.containerLog)
			if len(runs) != 2 {
				t.Fatalf("the discovered runtime ran %d times, want the start and the restart: %q", len(runs), runs)
			}
			kept, err := os.ReadFile(f.cfg.StateFile)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(kept), `"runtime":`+fmt.Sprintf("%q", f.containerCLI)) {
				t.Fatalf("state file %s does not name the runtime that ran", kept)
			}
			next.Close()
		})
	}
	t.Run("a program that is gone", func(t *testing.T) {
		call, f := newOllamaServer(t)
		f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
		call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
		waitOllama(t, call, "running")
		gone := filepath.Join(f.bin, "gone-runtime")
		raw := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, gone)
		if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		f.mod.Close()
		next := f.nextEngineRecording(t, auditStore(t), func(model.AuditDraft) {})
		defer next.Close()
		if !ollamaStateWithin(next, ollamaRunning, 20*time.Second) {
			t.Fatalf("after the restart Ollama is %+v, want the discovered runtime to run", next.ollamaStatus(context.Background()))
		}
	})
}

// The restart does not recreate the container through a different runtime family: a
// remembered Docker whose program is gone, resolved today to Podman, is refused — the
// container would come back under a CLI that did not create it.
func TestOllamaContainerRestartRefusesARememberedRuntimeOfAnotherFamily(t *testing.T) {
	_, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	// The recorded runtime is well-formed and of the Docker family, but its program is
	// gone, so resolution falls back to today's discovery (the fake Podman).
	other := filepath.Join(f.bin, "docker")
	raw := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, other)
	if err := os.WriteFile(f.cfg.StateFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	next := f.nextEngineRecording(t, auditStore(t), func(model.AuditDraft) {})
	defer next.Close()
	st := next.ollamaStatus(context.Background())
	if st.State != ollamaFailed || !strings.Contains(st.Message, "restore it before restarting") {
		t.Fatalf("row after the restart = %+v, want failed with the restore-it refusal", st)
	}
	if runs := containerRuns(t, f.containerLog); len(runs) != 0 {
		t.Fatalf("the restart ran the resolved runtime although the remembered one owns the container: %q", runs)
	}
}

// Containers switched off after the person's start stay off across a restart: the
// restart must not run the remembered runtime behind a setting that says otherwise.
func TestOllamaContainerRestartHonorsContainersOff(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	waitOllama(t, call, "running")
	f.mod.Close()
	f.cfg.Getenv = func(name string) string {
		if name == sessions.ContainerRuntimeEnv {
			return "off"
		}
		return os.Getenv(name)
	}
	next := f.nextEngineRecording(t, auditStore(t), func(model.AuditDraft) {})
	if !ollamaStateWithin(next, ollamaFailed, 10*time.Second) {
		t.Fatalf("after the restart Ollama is %+v", next.ollamaStatus(context.Background()))
	}
	msg := next.ollamaStatus(context.Background()).Message
	if !strings.Contains(msg, "switched off") {
		t.Fatalf("message = %q, want the switched-off reason", msg)
	}
	if !strings.Contains(msg, "may still be serving") {
		t.Fatalf("message = %q, want the surviving-container warning", msg)
	}
	if runs := containerRuns(t, f.containerLog); len(runs) != 1 {
		t.Fatalf("the runtime ran %d times, want only the person's start: %q", len(runs), runs)
	}
	next.Close()
}

// The SELinux relabel the first start discovered survives the restart: the record names
// it, so a host check that cannot run again does not mount /models unlabeled.
func TestOllamaContainerRestartKeepsTheSELinuxMount(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test", SELinux: true})
	call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	waitOllama(t, call, "running")
	raw, err := os.ReadFile(f.cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"selinux":true`) {
		t.Fatalf("state file %s does not keep the SELinux mode", raw)
	}
	f.mod.Close()
	// Today's composition no longer reports SELinux; the record still does.
	f.cfg.Container = &OllamaContainer{Runtime: f.containerCLI, Image: "docker.io/ollama/ollama:test"}
	next := f.nextEngineRecording(t, auditStore(t), func(model.AuditDraft) {})
	if !ollamaStateWithin(next, ollamaRunning, 20*time.Second) {
		t.Fatalf("after the restart Ollama is %+v", next.ollamaStatus(context.Background()))
	}
	runs := containerRuns(t, f.containerLog)
	if len(runs) != 2 || !argsHave(runs[1], "-v", f.models+":/models:z") {
		t.Fatalf("the restart mounted %q, want the recorded SELinux relabel", runs)
	}
	next.Close()
}

// A stop that proves its own cleanup still cannot resolve the note about a record
// that could not be read: the container from that record may serve on another
// connection. The clean stop keeps the note instead of reading stopped-and-clean.
func TestOllamaContainerStopKeepsTheUnreadableRecordNoteOnACleanStop(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.cfg.StateFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d, want 202 over an unreadable earlier record", code)
	}
	if st := waitOllama(t, call, "running"); !strings.Contains(st["message"].(string), "could not be read") {
		t.Fatalf("message = %v, want the unreadable-record note on the running row", st["message"])
	}
	if code, _ := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 {
		t.Fatalf("stop = %d, want 200 for a stop whose removal succeeded", code)
	}
	st := waitOllama(t, call, "stopped")
	stoppedMsg, _ := st["message"].(string)
	if !strings.Contains(stoppedMsg, "could not be read") {
		t.Fatalf("row = %q, want the unreadable-record note kept on a clean stop", stoppedMsg)
	}
}

// A start whose verification fails already carries the note that an earlier record
// could not be read; the verification reason appends to it instead of replacing it.
func TestOllamaContainerKeepsTheUnreadableRecordNoteWhenVerificationFails(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.cfg.StateFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.containerLog+".inspect-foreign-after-run", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d, want 202 over an unreadable earlier record", code)
	}
	st := waitOllama(t, call, "failed")
	msg, _ := st["message"].(string)
	if !strings.Contains(msg, "could not be read") || !strings.Contains(msg, "could not confirm") {
		t.Fatalf("row = %q, want the unreadable-record note kept beside the verification failure", msg)
	}
}
