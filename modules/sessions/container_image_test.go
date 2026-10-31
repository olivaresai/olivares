// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Whether the image container sessions run in is on the host. The runtime's own answer
// is what counts, so the stand-ins below speak the two dialects the real CLIs do:
// `podman image exists` says present or absent in its exit code (0, 1; 125 is an error),
// and `docker image inspect` fails with "No such image" when the image is absent and
// with some other message when the daemon cannot be asked.

const (
	imagePresent = "localhost/olivares-session:present"
	imageMissing = "localhost/olivares-session:missing"
	imageBroken  = "localhost/olivares-session:broken"
)

const fakePodmanImageScript = `#!/bin/sh
printf '%%s\n' "$@" >> '%[1]s/calls'
[ "$1 $2" = "image exists" ] || exit 64
case "$3" in
  %[2]s) exit 0 ;;
  %[3]s) echo "Error: cannot connect to the podman store" >&2; exit 125 ;;
  *) exit 1 ;;
esac
`

const fakeDockerImageScript = `#!/bin/sh
printf '%%s\n' "$@" >> '%[1]s/calls'
[ "$1 $2" = "image inspect" ] || exit 64
case "$5" in
  %[2]s) echo sha256:0123; exit 0 ;;
  %[3]s) echo "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?" >&2; exit 1 ;;
  *) echo "Error response from daemon: No such image: $5" >&2; exit 1 ;;
esac
`

// imageState is the runtime's answer without the reason it could not give one.
func imageState(rt ContainerRuntime) RunnerImageState {
	state, _ := rt.CheckImage(context.Background())
	return state
}

// imageCLI writes a stand-in runtime named name and returns it with its call log.
func imageCLI(t *testing.T, name, script, image string) (ContainerRuntime, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	body := fmt.Sprintf(script, dir, imagePresent, imageBroken)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return ContainerRuntime{Path: path, Image: image}, filepath.Join(dir, "calls")
}

func TestContainerRuntime_ImageStateAsksTheRuntimeItself(t *testing.T) {
	for _, cli := range []struct{ name, script string }{
		{"podman", fakePodmanImageScript},
		{"docker", fakeDockerImageScript},
	} {
		for image, want := range map[string]RunnerImageState{
			imagePresent: RunnerImagePresent,
			imageMissing: RunnerImageMissing,
			// A runtime that cannot be asked has not said the image is absent.
			imageBroken: RunnerImageUnknown,
		} {
			t.Run(cli.name+" "+image, func(t *testing.T) {
				rt, _ := imageCLI(t, cli.name, cli.script, image)
				if got := imageState(rt); got != want {
					t.Fatalf("ImageState = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestContainerRuntime_ImageStateIsUnknownWhenTheRuntimeIsGone(t *testing.T) {
	rt := ContainerRuntime{Path: filepath.Join(t.TempDir(), "podman"), Image: imageMissing}
	if got := imageState(rt); got != RunnerImageUnknown {
		t.Fatalf("ImageState = %q, want unknown", got)
	}
}

// The readiness read reports the image and starts nothing: the only thing the
// runtime is asked is the image query, never `run`.
func TestRunnerWithContainers_InspectLaunchReportsAMissingImage(t *testing.T) {
	rt, calls := imageCLI(t, "podman", fakePodmanImageScript, imageMissing)
	runner := NewRunnerWithContainers(NewProcRunner(), rt).(RunnerInspector)

	obs, err := runner.InspectLaunch(context.Background(), RunnerInspection{Program: "/bin/sh", Isolation: IsolationContainer})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Isolation != RunnerIsolationSupported || obs.Image != RunnerImageMissing {
		t.Fatalf("observation = %+v, want a supported runner and a missing image", obs)
	}
	got, _ := os.ReadFile(calls)
	if strings.TrimSpace(string(got)) != "image\nexists\n"+imageMissing {
		t.Fatalf("the runtime was asked %q; the read may only query the image", got)
	}

	// Native isolation never touches the container runtime.
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	obs, err = runner.InspectLaunch(context.Background(), RunnerInspection{Program: "/bin/sh", Isolation: IsolationNative})
	if err != nil || obs.Image != RunnerImageUnknown {
		t.Fatalf("native observation = %+v (err %v), want no image answer", obs, err)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Fatal("a native inspection asked the container runtime")
	}
}

// imageRunner is the composed container runner over a stand-in CLI, as the engine wires it.
func imageRunner(t *testing.T, image string) (Runner, string) {
	t.Helper()
	rt, calls := imageCLI(t, "podman", fakePodmanImageScript, image)
	return NewRunnerWithContainers(NewProcRunner(), rt), calls
}

func TestLaunchReadiness_AMissingContainerImageIsNamedNotRunnerUnsupported(t *testing.T) {
	bins := newReadinessProgramFixtures(t)
	cases := []struct {
		image     string
		wantState ReadinessState
		wantCode  string
	}{
		{imageMissing, ReadinessNotConfigured, codeContainerImageMissing},
		{imagePresent, ReadinessReady, codeRunnerReady},
		// Asked, no usable answer: not ready, and not a false "build it" either.
		{imageBroken, ReadinessUnknown, codeInspectionUnavailable},
	}
	for _, c := range cases {
		t.Run(c.image, func(t *testing.T) {
			be := readinessEngines(t)[0] // a clean estate per case: setup runs once per database
			runner, _ := imageRunner(t, c.image)
			m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithProgram(bins.present),
				WithCredentialSource(mintRefusingCredentialSource{t}))
			m.UseExecutionEnvironmentRef(testEnvRef)
			f := newReadinessFixture(t, be, m)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

			doc, r := f.readiness(ref, "isolation=container")
			if r.code != http.StatusOK {
				t.Fatalf("= %d %s", r.code, r.raw)
			}
			doc.wantCheck(t, CheckRunner, c.wantState, c.wantCode)
			if c.wantState == ReadinessNotConfigured {
				if doc.ConfigurationState != ReadinessNotConfigured {
					t.Fatalf("configuration_state = %q", doc.ConfigurationState)
				}
				for _, ch := range doc.Checks {
					if ch.Check == CheckRunner && ch.Remediation != remediationProvideSessionImage {
						t.Fatalf("remediation = %q, want %q", ch.Remediation, remediationProvideSessionImage)
					}
				}
			}
			// Another isolation is not held back by the container image.
			if doc, _ := f.readiness(ref, "isolation=native"); doc.ConfigurationState != ReadinessReady {
				t.Fatalf("native = %q", doc.ConfigurationState)
			}
		})
	}
}

// imageInspectingFake gives the module-level launch tests a runner whose image answer
// they choose; it counts launches so a refusal can be shown to have started nothing.
type imageInspectingFake struct {
	*fakeRunner
	image RunnerImageState
}

func (r imageInspectingFake) InspectLaunch(context.Context, RunnerInspection) (RunnerObservation, error) {
	return RunnerObservation{Isolation: RunnerIsolationSupported, Image: r.image, Program: RunnerProgramExecutable}, nil
}

func TestCreateRefusesAContainerRunWhoseImageIsMissingAndStartsNothing(t *testing.T) {
	for image, wantRefused := range map[RunnerImageState]bool{
		RunnerImageMissing: true, RunnerImagePresent: false, RunnerImageUnknown: false,
		// No answer from the runtime is not a verdict: the launch reaches it and the
		// runtime itself refuses if the image really is absent.
		RunnerImageUnavailable: false,
	} {
		t.Run(string(image)+"/"+fmt.Sprint(wantRefused), func(t *testing.T) {
			runner := imageInspectingFake{fakeRunner: &fakeRunner{initSID: "s"}, image: image}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
			ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: t.TempDir(), Actor: actorU, ActorKind: actorKindU})
			if err != nil {
				t.Fatal(err)
			}
			_, err = createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationContainer, WorkspaceRef: ws.WorkspaceRef,
				Actor: actorU, ActorKind: actorKindU,
			})
			if !wantRefused {
				if err != nil {
					t.Fatalf("a run with image %q was refused: %v", image, err)
				}
				return
			}
			var coded *codedRunErr
			if !isStatus(err, http.StatusUnprocessableEntity) || !errors.As(err, &coded) || coded.code != codeContainerImageMissing {
				t.Fatalf("err = %v, want a 422 with code %s", err, codeContainerImageMissing)
			}
			if len(runner.specs) != 0 {
				t.Fatalf("a refused run still reached the runner: %+v", runner.specs)
			}
		})
	}
}

// The refusal is the same on the two other doors a launch comes through: resuming a run,
// and the REST route. The wire code is what a client reads, so it is pinned there.
func TestResumeRefusesAMissingContainerImageBeforeLaunch(t *testing.T) {
	runner := imageInspectingFake{fakeRunner: &fakeRunner{initSID: "s"}, image: RunnerImageMissing}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	const ref = "01900000-0000-7000-8000-000000000036"
	if err := st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(t.Context(), model.Record{
			colRunRef: ref, colTransport: string(TransportStreamJSON),
			colPermissionMode: "default", colIsolation: string(IsolationContainer),
			colState: stateFailed, colLastEventSeq: int64(0),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := m.resumeRun(t.Context(), tenant, ref, actorU, actorKindU, "")
	var coded *codedRunErr
	if !isStatus(err, http.StatusUnprocessableEntity) || !errors.As(err, &coded) || coded.code != codeContainerImageMissing {
		t.Fatalf("resume = %v; want 422 with code %s", err, codeContainerImageMissing)
	}
	if got := listRunEvents(t, st, tenant, ref); len(got) != 0 {
		t.Fatalf("refused resume added lifecycle events: %+v", got)
	}
	if len(runner.specs) != 0 {
		t.Fatalf("a refused resume reached the runner: %+v", runner.specs)
	}
}

func TestRunsEndpointRefusesAMissingContainerImageBeforeAnyEffect(t *testing.T) {
	root := t.TempDir()
	minted := 0
	runner := imageInspectingFake{fakeRunner: &fakeRunner{initSID: "s"}, image: RunnerImageMissing}
	m := New(WithSessionWorkspaceRoot(root), WithRunner(runner), WithCredentialSource(
		CredentialSourceFunc(func(ctx context.Context, req CredentialRequest) (Credential, error) {
			minted++
			return Credential{ID: "image-test", Token: "test-token", Scheme: "mock", NotAfter: farFuture}, nil
		})))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "image")

	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"isolation": "container"}, tenantHdr(tenant))
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.raw, `"code":"container_image_missing"`) ||
		!strings.Contains(r.raw, "olivares doctor") {
		t.Fatalf("create = %d %s; want 422 container_image_missing naming doctor", r.code, r.raw)
	}
	if minted != 0 || len(runner.specs) != 0 {
		t.Errorf("the refusal minted %d credentials and reached the runner %d times", minted, len(runner.specs))
	}
	list := h.do("GET", "/v1/m/sessions/runs", admin, tenantHdr(tenant))
	if items, ok := list.body["items"].([]any); list.code != http.StatusOK || !ok || len(items) != 0 {
		t.Errorf("the refusal left run rows: %d %s", list.code, list.raw)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Errorf("the refusal left workspaces: %v, %v", entries, err)
	}
}

// What the runtime is asked, and what it is told: the stand-in records its environment.
const fakeEnvImageScript = `#!/bin/sh
printf '%%s\n' "$@" >> '%[1]s/calls'
env > '%[1]s/env'
exit 0
`

func TestContainerRuntime_ImageQueryUsesTheCLIsEnvironmentAndNeverTheEngines(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///run/user/1000/podman/podman.sock")
	t.Setenv("OLIVARES_ENGINE_ONLY_SECRET", "engine-only-secret")
	dir := t.TempDir()
	path := filepath.Join(dir, "podman")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(fakeEnvImageScript, dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	rt := ContainerRuntime{Path: path, Image: imagePresent}
	if got := imageState(rt); got != RunnerImagePresent {
		t.Fatalf("ImageState = %q", got)
	}
	env, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "DOCKER_HOST=unix:///run/user/1000/podman/podman.sock") {
		t.Errorf("the CLI lost the variable that tells it where its daemon is: %q", env)
	}
	if strings.Contains(string(env), "engine-only-secret") {
		t.Errorf("the CLI inherited the engine's secret: %q", env)
	}
}

// The readiness read is open to every viewer: a second ask within the cache window does not
// start the runtime again, and a different runtime or image is a different question.
func TestContainerRuntime_ImageAnswerIsKeptBrieflyPerRuntimeAndImage(t *testing.T) {
	rt, calls := imageCLI(t, "podman", fakePodmanImageScript, imageMissing)
	for i := 0; i < 3; i++ {
		if got := imageState(rt); got != RunnerImageMissing {
			t.Fatalf("ask %d: ImageState = %q", i, got)
		}
	}
	if got, _ := os.ReadFile(calls); strings.Count(string(got), "image\n") != 1 {
		t.Fatalf("the runtime was started more than once for one question: %q", got)
	}
	other := rt
	other.Image = imagePresent
	if got := imageState(other); got != RunnerImagePresent {
		t.Fatalf("another image: ImageState = %q, want its own answer", got)
	}
	otherRuntime, otherCalls := imageCLI(t, "podman", fakePodmanImageScript, imageMissing)
	if got := imageState(otherRuntime); got != RunnerImageMissing {
		t.Fatalf("another runtime: ImageState = %q, want its own answer", got)
	}
	if got, _ := os.ReadFile(otherCalls); !strings.Contains(string(got), "image\n") {
		t.Fatal("another runtime was not asked")
	}
}

func TestContainerRuntime_CheckImageSaysWhyItCouldNotAnswer(t *testing.T) {
	for name, c := range map[string]struct {
		cli, image string
		script     string
		wantState  RunnerImageState
		wantErr    string
	}{
		"podman could not ask": {"podman", imageBroken, fakePodmanImageScript, RunnerImageUnknown, "exit 125"},
		"docker daemon down":   {"docker", imageBroken, fakeDockerImageScript, RunnerImageUnknown, "Cannot connect"},
		"podman says absent":   {"podman", imageMissing, fakePodmanImageScript, RunnerImageMissing, ""},
		"docker says present":  {"docker", imagePresent, fakeDockerImageScript, RunnerImagePresent, ""},
		"podman exit 1 with a message is a wrapper's own failure": {"podman", "localhost/olivares-session:wrapper",
			"#!/bin/sh\necho 'host-spawn: connection refused' >&2\nexit 1\n", RunnerImageUnknown, "host-spawn"},
		// Podman prints environment warnings while still answering "absent" in its exit
		// code; they are noise, not a verdict.
		"podman says absent with a warning": {"podman", imageMissing,
			"#!/bin/sh\n[ \"$1 $2\" = \"image exists\" ] || exit 64\necho 'WARN[0000] The cgroupv2 manager is set to systemd but there is no systemd user session available' >&2\nexit 1\n",
			RunnerImageMissing, ""},
	} {
		t.Run(name, func(t *testing.T) {
			script := c.script
			if strings.Contains(script, "%[1]s") {
				script = fmt.Sprintf(script, t.TempDir(), imagePresent, imageBroken)
			}
			path := filepath.Join(t.TempDir(), c.cli)
			if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			state, err := ContainerRuntime{Path: path, Image: c.image}.CheckImage(context.Background())
			if state != c.wantState {
				t.Fatalf("state = %q, want %q", state, c.wantState)
			}
			if (c.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
		})
	}
}

// The wording of the Docker-named command, including podman behind it, is read in any case.
func TestContainerRuntime_ImageStateReadsTheDockerDialects(t *testing.T) {
	for said, want := range map[string]RunnerImageState{
		"Error response from daemon: No such image: x":   RunnerImageMissing,
		"Error: NO SUCH IMAGE: x":                        RunnerImageMissing,
		"Error: localhost/x:1: image not known":          RunnerImageMissing,
		"Error response from daemon: pull access denied": RunnerImageUnknown,
	} {
		t.Run(said, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "docker")
			if err := os.WriteFile(path, []byte("#!/bin/sh\necho '"+said+"' >&2\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if got := imageState(ContainerRuntime{Path: path, Image: imageMissing}); got != want {
				t.Fatalf("%q read as %q, want %q", said, got, want)
			}
		})
	}
}

// Only a localhost/ name is local: a registry, with or without a port, is the runtime's to pull.
func TestContainerRuntime_ImageStateNeverAsksAboutAnImageARegistryServes(t *testing.T) {
	for _, cli := range []struct{ name, script string }{{"podman", fakePodmanImageScript}, {"docker", fakeDockerImageScript}} {
		for _, image := range []string{"docker.io/library/debian:13", "localhost:5000/olivares-session:1", "olivares-session:latest"} {
			t.Run(cli.name+" "+image, func(t *testing.T) {
				rt, calls := imageCLI(t, cli.name, cli.script, image)
				if got := imageState(rt); got != RunnerImageUnknown {
					t.Fatalf("ImageState = %q", got)
				}
				if _, err := os.Stat(calls); err == nil {
					t.Fatal("the runtime was asked about an image it would pull")
				}
			})
		}
	}
}

// A runtime that never answers is given up on at the timeout, and a helper that inherited
// its output cannot hold the call past it; a cancelled request is neither an answer nor a fault.
func TestContainerRuntime_ImageStateGivesUpOnARuntimeThatHangs(t *testing.T) {
	old := imageCheckTimeout
	imageCheckTimeout = 200 * time.Millisecond
	t.Cleanup(func() { imageCheckTimeout = old })
	path := filepath.Join(t.TempDir(), "podman")
	// The background sleep inherits the output pipe, as a credential helper would.
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 3 &\nsleep 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	state, err := ContainerRuntime{Path: path, Image: imageMissing}.CheckImage(context.Background())
	if state != RunnerImageUnknown || err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("CheckImage = %q, %v; want unknown and a timeout reason", state, err)
	}
	if took := time.Since(start); took > 2500*time.Millisecond {
		t.Fatalf("the hung runtime held the call for %s", took)
	}
}

func TestContainerRuntime_ImageStateIsUnknownNotMissingWhenTheRequestIsCancelled(t *testing.T) {
	rt, _ := imageCLI(t, "podman", fakePodmanImageScript, imageMissing)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state, err := rt.CheckImage(ctx)
	if state != RunnerImageUnknown || err != nil {
		t.Fatalf("CheckImage = %q, %v; want unknown and no fault", state, err)
	}
	// The cancelled ask was not kept as the host's answer.
	if got := imageState(rt); got != RunnerImageMissing {
		t.Fatalf("the next ask = %q, want the runtime's own answer", got)
	}
}

// A reader whose request ends while another query is in flight leaves at once: it does not
// sit behind a runtime that has not answered.
func TestContainerRuntime_ACancelledReaderDoesNotWaitBehindAHungRuntime(t *testing.T) {
	old := imageCheckTimeout
	imageCheckTimeout = 2 * time.Second
	t.Cleanup(func() { imageCheckTimeout = old })
	dir := t.TempDir()
	path := filepath.Join(dir, "podman")
	started := filepath.Join(dir, "started")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch '"+started+"'\nexec sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	rt := ContainerRuntime{Path: path, Image: imageMissing}
	holder := make(chan struct{})
	go func() {
		defer close(holder)
		_, _ = rt.CheckImage(context.Background())
	}()
	t.Cleanup(func() { <-holder })
	for i := 0; ; i++ {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if i > 200 {
			t.Fatal("the first query never reached the runtime")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	state, err := rt.CheckImage(ctx)
	if state != RunnerImageUnknown || err != nil {
		t.Fatalf("CheckImage = %q, %v; want unknown and no fault", state, err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the cancelled reader waited %s behind the hung runtime", took)
	}
}

// Readers that arrive while a query is in flight share its answer: the runtime is asked once.
func TestContainerRuntime_ConcurrentReadersShareOneQuery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "podman")
	calls := filepath.Join(dir, "calls")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho ask >> '"+calls+"'\nsleep 0.3\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	rt := ContainerRuntime{Path: path, Image: imageMissing}
	states := make([]RunnerImageState, 4)
	var wg sync.WaitGroup
	for i := range states {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states[i] = imageState(rt)
		}()
	}
	wg.Wait()
	for i, got := range states {
		if got != RunnerImageMissing {
			t.Fatalf("reader %d: ImageState = %q", i, got)
		}
	}
	if got, _ := os.ReadFile(calls); strings.Count(string(got), "ask\n") != 1 {
		t.Fatalf("the runtime was asked more than once for readers that arrived together: %q", got)
	}
}

// A reader that ends its request mid-query gives the slot back: the next reader gets its answer.
func TestContainerRuntime_ACancelledQueryFreesTheSlotForTheNextReader(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	hung := filepath.Join(dir, "hung", "podman")
	if err := os.MkdirAll(filepath.Dir(hung), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hung, []byte("#!/bin/sh\ntouch '"+started+"'\nexec sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	holder := make(chan struct{})
	go func() {
		defer close(holder)
		_, _ = ContainerRuntime{Path: hung, Image: imageMissing}.CheckImage(ctx)
	}()
	for i := 0; ; i++ {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if i > 200 {
			t.Fatal("the first query never reached the runtime")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-holder
	rt, _ := imageCLI(t, "podman", fakePodmanImageScript, imageMissing)
	next, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if state, err := rt.CheckImage(next); state != RunnerImageMissing || err != nil {
		t.Fatalf("CheckImage = %q, %v; want the runtime's own answer after a cancelled holder", state, err)
	}
}

// An answer is kept for imageCacheTTL and no longer: after it the runtime is asked again.
func TestContainerRuntime_ImageAnswerExpires(t *testing.T) {
	old := imageCacheTTL
	imageCacheTTL = 50 * time.Millisecond
	t.Cleanup(func() { imageCacheTTL = old })
	rt, calls := imageCLI(t, "podman", fakePodmanImageScript, imageMissing)
	imageState(rt)
	time.Sleep(120 * time.Millisecond)
	imageState(rt)
	if got, _ := os.ReadFile(calls); strings.Count(string(got), "image\n") != 2 {
		t.Fatalf("the runtime was not asked again after the answer expired: %q", got)
	}
}

// The container runner asks only for container isolation, and says so in the log when the
// runtime could not answer, instead of letting the read look ready without a trace.
func TestContainerRunner_InspectLaunchAsksOnlyForContainerAndLogsAFailedQuery(t *testing.T) {
	rt, calls := imageCLI(t, "podman", fakePodmanImageScript, imageBroken)
	var warned []string
	rt.Warn = func(msg string, kv ...any) { warned = append(warned, fmt.Sprint(msg, kv)) }
	cr := newContainerRunner(rt)

	for _, iso := range []Isolation{IsolationNative, IsolationSandbox} {
		obs, err := cr.InspectLaunch(context.Background(), RunnerInspection{Program: "/bin/sh", Isolation: iso})
		if err != nil || obs.Isolation != RunnerIsolationUnsupported || obs.Image != RunnerImageUnknown {
			t.Fatalf("%s: %+v, %v", iso, obs, err)
		}
	}
	if _, err := os.Stat(calls); err == nil {
		t.Fatal("a posture the container runner does not serve asked the runtime")
	}
	obs, err := cr.InspectLaunch(context.Background(), RunnerInspection{Program: "/bin/sh", Isolation: IsolationContainer})
	if err != nil || obs.Isolation != RunnerIsolationSupported || obs.Image != RunnerImageUnavailable {
		t.Fatalf("container: %+v, %v; want the asked-but-unanswered state", obs, err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "exit 125") {
		t.Fatalf("log = %q, want one warning naming the runtime's answer", warned)
	}
}
