// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

// The container runner is exercised through a stand-in for the container CLI: a shell
// script that records what it was asked to do and then behaves like a container whose
// program echoes its input. What these tests pin is what Olivares asks of the CLI
// (arguments, environment, the way the secrets travel, the cleanup call) and that
// the bridged streams are the ones the rest of the module already reads. They do not
// prove podman or Docker accept those arguments; that is the real-use run.

const fakeRuntimeScript = `#!/bin/sh
LOG='%s'
case "$1" in
  rm)
    printf '%%s\n' "$@" >> "$LOG/rm.log"
    if [ -f "$LOG/rmfail" ]; then echo "boom: daemon unreachable" >&2; exit 1; fi
    exit 0
    ;;
  run)
    printf '%%s\n' "$@" > "$LOG/argv"
    env > "$LOG/clientenv"
    printf 'ready\n'
    while IFS= read -r line; do printf 'echo:%%s\n' "$line"; done
    exit 0
    ;;
esac
exit 64
`

type fakeRuntime struct {
	rt  ContainerRuntime
	log string
}

// newFakeRuntime writes the stand-in under the given CLI name (the runner tells podman
// and Docker apart by it).
func newFakeRuntime(t *testing.T, name string) fakeRuntime {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	if err := os.Mkdir(log, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(fmt.Sprintf(fakeRuntimeScript, log)), 0o700); err != nil {
		t.Fatal(err)
	}
	return fakeRuntime{rt: ContainerRuntime{Path: path, Image: "registry.example/olivares-session:test"}, log: log}
}

func (f fakeRuntime) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.log, name))
	if err != nil {
		t.Fatalf("the container CLI did not write %s: %v", name, err)
	}
	return string(b)
}

func (f fakeRuntime) argv(t *testing.T) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(f.read(t, "argv"), "\n"), "\n")
}

func (f fakeRuntime) ran(name string) bool {
	_, err := os.Stat(filepath.Join(f.log, name))
	return err == nil
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

// containerSpec is a launch whose program is a real file, as the host's tool is.
func containerSpec(t *testing.T) LaunchSpec {
	t.Helper()
	program := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return LaunchSpec{
		Program: program, Args: []string{"--print", "--verbose"},
		Dir: "/workspace", Isolation: IsolationContainer, WaitDelay: time.Second,
		Env: []EnvVar{{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sk-ant-secret-value"}},
	}
}

func launchContainer(t *testing.T, f fakeRuntime, spec LaunchSpec) Process {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	p, err := NewContainerRunner(f.rt).Launch(ctx, spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = p.Stop(stopCtx)
	})
	return p
}

func containerFrame(t *testing.T, p Process) string {
	t.Helper()
	select {
	case f, ok := <-p.Output():
		if !ok {
			t.Fatal("the output closed before the expected frame")
		}
		return string(f.Data)
	case <-time.After(10 * time.Second):
		t.Fatal("no output frame within 10s")
		return ""
	}
}

func TestContainerRunner_BridgesStdioAndRemovesTheContainer(t *testing.T) {
	f := newFakeRuntime(t, "podman")
	p := launchContainer(t, f, containerSpec(t))

	if got := containerFrame(t, p); got != "ready" {
		t.Fatalf("first frame = %q, want the program's own output", got)
	}
	if err := p.Send(context.Background(), []byte("hello")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := containerFrame(t, p); got != "echo:hello" {
		t.Fatalf("frame = %q, want the line echoed through the container's stdin and stdout", got)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// --rm only removes a container that exited cleanly; a killed client leaves it.
	// The runner asks the CLI to remove it by the name it gave it.
	argv := f.argv(t)
	name := argv[indexOf(argv, "--name")+1]
	if !strings.HasPrefix(name, "olivares-session-") {
		t.Fatalf("container name = %q, want the olivares-session- prefix", name)
	}
	if rm := f.read(t, "rm.log"); !strings.Contains(rm, name) {
		t.Fatalf("rm.log = %q, want the container %q removed", rm, name)
	}
}

func TestContainerRunner_ArgumentsAreHardenedAndCarryNoSecret(t *testing.T) {
	f := newFakeRuntime(t, "podman")
	spec := containerSpec(t)
	hostDir, homeDir := t.TempDir(), t.TempDir()
	hooksDir := filepath.Join(hostDir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	spec.Binds = []ContainerBind{
		{HostPath: homeDir, Target: homeDir},
		{HostPath: hooksDir, Target: "/workspace/.git/hooks", ReadOnly: true},
	}
	spec.Workspace = &WorkspaceMount{HostPath: hostDir, ContainerTarget: "/workspace"}
	spec.Env = append(spec.Env, EnvVar{Name: "HOME", Value: homeDir})
	p := launchContainer(t, f, spec)
	_ = containerFrame(t, p)

	argv := f.argv(t)
	for _, want := range []string{"run", "--rm", "-i", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges"} {
		if indexOf(argv, want) < 0 {
			t.Errorf("argv lacks %q: %q", want, argv)
		}
	}
	image := indexOf(argv, f.rt.Image)
	if image < 0 {
		t.Fatalf("argv lacks the image: %q", argv)
	}
	if got := argv[image+1:]; strings.Join(got, " ") != spec.Program+" --print --verbose" {
		t.Errorf("after the image = %q, want the program and its arguments untouched", got)
	}
	if i := indexOf(argv, "-w"); i < 0 || argv[i+1] != "/workspace" {
		t.Errorf("working directory = %q, want /workspace", argv)
	}
	for _, mount := range []string{
		spec.Program + ":" + spec.Program + ":ro",
		hostDir + ":/workspace",
		homeDir + ":" + homeDir,
		hooksDir + ":/workspace/.git/hooks:ro",
	} {
		if i := indexOf(argv, mount); i < 1 || argv[i-1] != "-v" {
			t.Errorf("no -v %s in %q", mount, argv)
		}
	}
	joined := strings.Join(argv, "\n")
	if strings.Contains(joined, "sk-ant-secret-value") {
		t.Errorf("a secret value reached argv, which every user on the host can read: %q", argv)
	}
	// The secret travels by name: the CLI copies it from its own environment.
	if i := indexOf(argv, "ANTHROPIC_AUTH_TOKEN"); i < 1 || argv[i-1] != "-e" {
		t.Errorf("no -e ANTHROPIC_AUTH_TOKEN in %q", argv)
	}
	if !strings.Contains(f.read(t, "clientenv"), "ANTHROPIC_AUTH_TOKEN=sk-ant-secret-value\n") {
		t.Errorf("the CLI's environment lacks the value it must copy into the container")
	}
	// HOME is one of the names the CLI needs for itself, so the session's HOME is inline
	// and the CLI keeps its own.
	if i := indexOf(argv, "HOME="+homeDir); i < 1 || argv[i-1] != "-e" {
		t.Errorf("no -e HOME=%s in %q", homeDir, argv)
	}
	if i := indexOf(argv, "--entrypoint"); i < 0 || argv[i+1] != "" {
		t.Errorf("no empty --entrypoint in %q: the image's own entrypoint would wrap the program", argv)
	}
}

func TestContainerRunner_EngineEnvironmentDoesNotReachTheCLIOrTheContainer(t *testing.T) {
	t.Setenv("OLIVARES_SIGNING_KEY", "engine-only-secret")
	t.Setenv("ANTHROPIC_API_KEY", "host-key")
	f := newFakeRuntime(t, "podman")
	p := launchContainer(t, f, containerSpec(t))
	_ = containerFrame(t, p)

	clientEnv := f.read(t, "clientenv")
	if strings.Contains(clientEnv, "engine-only-secret") || strings.Contains(clientEnv, "host-key") {
		t.Errorf("the CLI inherited the engine's secrets: %q", clientEnv)
	}
	if joined := strings.Join(f.argv(t), "\n"); strings.Contains(joined, "OLIVARES_SIGNING_KEY") || strings.Contains(joined, "ANTHROPIC_API_KEY") {
		t.Errorf("an engine variable was passed into the container: %q", f.argv(t))
	}
}

func TestContainerRunner_TheSessionsHomeDoesNotReplaceTheCLIsOwn(t *testing.T) {
	t.Setenv("HOME", "/home/engine-user")
	f := newFakeRuntime(t, "podman")
	spec := containerSpec(t)
	spec.Env = append(spec.Env, EnvVar{Name: "HOME", Value: t.TempDir()})
	p := launchContainer(t, f, spec)
	_ = containerFrame(t, p)
	if clientEnv := f.read(t, "clientenv"); !strings.Contains(clientEnv, "HOME=/home/engine-user\n") {
		t.Errorf("the CLI lost its own HOME to the session's, so it would look for its storage in the wrong place: %q", clientEnv)
	}
}

func TestContainerRunner_HomeThatNoMountCoversGetsItsOwnTmpfs(t *testing.T) {
	f := newFakeRuntime(t, "podman")
	spec := containerSpec(t)
	home := t.TempDir()
	config := filepath.Join(home, ".claude")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	spec.Env = append(spec.Env, EnvVar{Name: "HOME", Value: home})
	spec.Binds = []ContainerBind{{HostPath: config, Target: config}}
	p := launchContainer(t, f, spec)
	_ = containerFrame(t, p)
	var tmpfs []string
	argv := f.argv(t)
	for i, a := range argv {
		if a == "--tmpfs" {
			tmpfs = append(tmpfs, argv[i+1])
		}
	}
	if !strings.Contains(strings.Join(tmpfs, " "), home+":") {
		t.Errorf("tmpfs = %q, want one at HOME so the tool can write its state", tmpfs)
	}
}

func TestContainerRunner_SessionWithoutHomeGetsOneInTmp(t *testing.T) {
	f := newFakeRuntime(t, "podman")
	p := launchContainer(t, f, containerSpec(t))
	_ = containerFrame(t, p)
	if argv := f.argv(t); indexOf(argv, "HOME=/tmp") < 1 || argv[indexOf(argv, "HOME=/tmp")-1] != "-e" {
		t.Errorf("argv = %q, want -e HOME=/tmp when the launch names none", argv)
	}
}

func TestContainerRunner_UserMapping(t *testing.T) {
	uid, gid := os.Geteuid(), os.Getegid()
	t.Run("docker runs as the engine user", func(t *testing.T) {
		f := newFakeRuntime(t, "docker")
		p := launchContainer(t, f, containerSpec(t))
		_ = containerFrame(t, p)
		argv := f.argv(t)
		if i := indexOf(argv, "--user"); i < 0 || argv[i+1] != fmt.Sprintf("%d:%d", uid, gid) {
			t.Errorf("argv = %q, want --user %d:%d so files written in the folder are the engine user's", argv, uid, gid)
		}
		if indexOf(argv, "--userns=keep-id") >= 0 {
			t.Errorf("--userns=keep-id is podman's: %q", argv)
		}
	})
	t.Run("the generated passwd uses the child's temporary root", func(t *testing.T) {
		if uid == 0 {
			t.Skip("the engine user is root: no passwd file is written")
		}
		root := t.TempDir()
		confine.UseChildTempRoot(root)
		t.Cleanup(func() { confine.UseChildTempRoot("") })
		f := newFakeRuntime(t, "docker")
		p := launchContainer(t, f, containerSpec(t))
		_ = containerFrame(t, p)
		host, ok := passwdMount(f.argv(t))
		if !ok {
			t.Fatalf("argv = %q, want a generated passwd mount", f.argv(t))
		}
		rel, err := filepath.Rel(root, host)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("passwd = %q, want it under the child's temporary root %q", host, root)
		}
	})
	t.Run("docker gives the engine user an entry in /etc/passwd", func(t *testing.T) {
		if uid == 0 {
			t.Skip("the engine user is root: the image has root's entry")
		}
		f := newFakeRuntime(t, "docker")
		p := launchContainer(t, f, containerSpec(t))
		_ = containerFrame(t, p)
		host, ok := passwdMount(f.argv(t))
		if !ok {
			t.Fatalf("argv = %q, want a read-only /etc/passwd mount so OpenSSH finds uid %d", f.argv(t), uid)
		}
		data, err := os.ReadFile(host)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), fmt.Sprintf(":x:%d:%d:", uid, gid)) {
			t.Errorf("passwd = %q, want an entry for uid %d gid %d", data, uid, gid)
		}
	})
	t.Run("the engine user's passwd file goes when the session ends", func(t *testing.T) {
		if uid == 0 {
			t.Skip("the engine user is root: no passwd file is written")
		}
		f := newFakeRuntime(t, "docker")
		p := launchContainer(t, f, containerSpec(t))
		_ = containerFrame(t, p)
		host, ok := passwdMount(f.argv(t))
		if !ok {
			t.Fatalf("argv = %q, want a read-only /etc/passwd mount", f.argv(t))
		}
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := p.Stop(stopCtx); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(host); os.IsNotExist(err) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s still exists after the session ended", host)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	t.Run("rootless podman keeps the engine user's id", func(t *testing.T) {
		if uid == 0 {
			t.Skip("the engine user is root: podman is rootful and has no id to keep")
		}
		f := newFakeRuntime(t, "podman")
		p := launchContainer(t, f, containerSpec(t))
		_ = containerFrame(t, p)
		if argv := f.argv(t); indexOf(argv, "--userns=keep-id") < 0 || indexOf(argv, "--user") >= 0 {
			t.Errorf("argv = %q, want --userns=keep-id and no --user", argv)
		} else if _, ok := passwdMount(argv); ok {
			t.Errorf("argv = %q: podman writes the engine user's passwd entry itself, so no /etc/passwd mount", argv)
		}
	})
}

func TestContainerRunner_ASessionThatMountsPasswdKeepsItsOwnAndGetsNoSecond(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the engine user is root: no passwd file is written")
	}
	f := newFakeRuntime(t, "docker")
	spec := containerSpec(t)
	passwd := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(passwd, []byte("root:x:0:0:root:/:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec.Binds = []ContainerBind{{HostPath: passwd, Target: "/etc/passwd", ReadOnly: true}}
	p := launchContainer(t, f, spec)
	_ = containerFrame(t, p)
	argv := f.argv(t)
	var passwdMounts []string
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-v" && strings.Contains(argv[i+1], ":/etc/passwd") {
			passwdMounts = append(passwdMounts, argv[i+1])
		}
	}
	if len(passwdMounts) != 1 || !strings.HasPrefix(passwdMounts[0], passwd+":/etc/passwd") {
		t.Errorf("passwd mounts = %q, want only the session's own %s", passwdMounts, passwd)
	}
}

func TestContainerRunner_ARefusedLaunchRemovesItsPasswdDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the engine user is root: no passwd file is written")
	}
	tmp := t.TempDir()
	confine.UseChildTempRoot(tmp)
	t.Cleanup(func() { confine.UseChildTempRoot("") })
	f := newFakeRuntime(t, "docker")
	spec := containerSpec(t)
	spec.Binds = []ContainerBind{{HostPath: "/no/such/host/path", Target: "/workspace/x"}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := NewContainerRunner(f.rt).Launch(ctx, spec); err == nil {
		t.Fatal("Launch accepted a bind whose host path does not exist")
	}
	left, err := filepath.Glob(filepath.Join(tmp, "s-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("a refused launch left %q behind", left)
	}
}

func TestContainerRunner_SELinuxLabelsTheBinds(t *testing.T) {
	f := newFakeRuntime(t, "podman")
	f.rt.SELinux = true
	restore := hostSELinuxType
	hostSELinuxType = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { hostSELinuxType = restore })
	spec := containerSpec(t)
	homeDir, hooksDir := t.TempDir(), t.TempDir()
	policy := t.TempDir()
	spec.Binds = []ContainerBind{
		{HostPath: homeDir, Target: homeDir},
		{HostPath: hooksDir, Target: "/workspace/.git/hooks", ReadOnly: true},
		{HostPath: policy, Target: "/etc/claude-code", ReadOnly: true, NoRelabel: true},
	}
	p := launchContainer(t, f, spec)
	_ = containerFrame(t, p)
	argv := f.argv(t)
	// A bind marked NoRelabel stays as the host has it: the engine's own program, the host's policy.
	for _, mount := range []string{homeDir + ":" + homeDir + ":z", hooksDir + ":/workspace/.git/hooks:ro,z", policy + ":/etc/claude-code:ro"} {
		if indexOf(argv, mount) < 0 {
			t.Errorf("no -v %s in %q", mount, argv)
		}
	}
}

// the regression: on the Olivares Server the daemon labels a :z bind container_file_t, which would take the tool's folder and
// the session's workspace away from native sessions (olivares_tool_exec_t, olivares_workspace_t). A bind whose host path
// carries an olivares_* label keeps it; any other bind is still labeled for the container.
func TestContainerRunner_KeepsOlivaresLabelsOnTheHost(t *testing.T) {
	f := newFakeRuntime(t, "docker")
	f.rt.SELinux = true
	spec := containerSpec(t)
	tools, workspace, project := t.TempDir(), t.TempDir(), t.TempDir()
	restore := hostSELinuxType
	hostSELinuxType = func(path string) (string, error) {
		switch path {
		case tools:
			return "olivares_tool_exec_t", nil
		case workspace:
			return "olivares_workspace_t", nil
		}
		return "user_home_t", nil
	}
	t.Cleanup(func() { hostSELinuxType = restore })
	spec.Binds = []ContainerBind{
		{HostPath: tools, Target: tools, ReadOnly: true},
		{HostPath: workspace, Target: workspace},
		{HostPath: project, Target: "/project"},
	}
	p := launchContainer(t, f, spec)
	_ = containerFrame(t, p)
	argv := f.argv(t)
	for _, mount := range []string{tools + ":" + tools + ":ro", workspace + ":" + workspace, project + ":/project:z"} {
		if indexOf(argv, mount) < 0 {
			t.Errorf("no -v %s in %q", mount, argv)
		}
	}
}

// the regression, Root 2026-10-26: on the Olivares Server a session's container runs in the tool's own SELinux domain
// (olivares_tool_t), which holds exactly a native session's rights; container_t is never given the accounts' files.
// The engine sets the label from the program's own label, never from the request, and only where the Olivares policy
// labels the program (olivares_tool_exec_t): another SELinux host keeps the runtime's default.
func TestContainerRunner_RunsTheSessionInTheToolsDomainOnTheServer(t *testing.T) {
	for name, c := range map[string]struct {
		selinux bool
		label   string
		want    bool
	}{
		"Server: the program is olivares_tool_exec_t": {true, "olivares_tool_exec_t", true},
		"Server: Claude has its own executable type":  {true, "olivares_tool_claude_exec_t", true},
		"another SELinux host":                        {true, "bin_t", false},
		"no SELinux":                                  {false, "olivares_tool_exec_t", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeRuntime(t, "docker")
			f.rt.SELinux = c.selinux
			spec := containerSpec(t)
			restore := hostSELinuxType
			hostSELinuxType = func(path string) (string, error) {
				if !c.selinux {
					return "", errors.New("security.selinux is not supported")
				}
				if path == spec.Program {
					return c.label, nil
				}
				return "", nil
			}
			t.Cleanup(func() { hostSELinuxType = restore })
			p := launchContainer(t, f, spec)
			_ = containerFrame(t, p)
			argv := f.argv(t)
			i, image := indexOf(argv, "label=type:olivares_tool_t"), indexOf(argv, f.rt.Image)
			if got := i > 0 && argv[i-1] == "--security-opt" && i < image; got != c.want {
				t.Fatalf("--security-opt label=type:olivares_tool_t before the image: %v, want %v (%q)", got, c.want, argv)
			}
		})
	}
}

func TestContainerRunner_RefusesWhenTheLabelProbeFails(t *testing.T) {
	f := newFakeRuntime(t, "docker")
	f.rt.SELinux = true
	restore := hostSELinuxType
	hostSELinuxType = func(string) (string, error) { return "", errors.New("permission denied") }
	t.Cleanup(func() { hostSELinuxType = restore })
	p, err := NewContainerRunner(f.rt).Launch(context.Background(), containerSpec(t))
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("a label probe failure must refuse the launch, got process %v, error %v", p, err)
	}
	if f.ran("argv") {
		t.Fatal("the container CLI was started despite the label probe failure")
	}
}

func TestContainerRunner_RefusesWhenTheProgramLabelProbeFails(t *testing.T) {
	f := newFakeRuntime(t, "docker")
	f.rt.SELinux = true
	spec := containerSpec(t)
	restore := hostSELinuxType
	programProbes := 0
	hostSELinuxType = func(path string) (string, error) {
		if path != spec.Program {
			return "bin_t", nil // another bind, including Docker's generated passwd file
		}
		programProbes++
		if programProbes == 1 {
			return "bin_t", nil // the program's read-only bind
		}
		return "", errors.New("program label permission denied")
	}
	t.Cleanup(func() { hostSELinuxType = restore })
	p, err := NewContainerRunner(f.rt).Launch(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "program label permission denied") || programProbes != 2 {
		t.Fatalf("the program label probe must refuse the launch, got process %v, error %v after %d program probes", p, err, programProbes)
	}
	if f.ran("argv") {
		t.Fatal("the container CLI was started despite the program label probe failure")
	}
}

func TestContainerRunner_RefusesWhatItCannotHoldBeforeStartingAnything(t *testing.T) {
	cases := map[string]func(*LaunchSpec){
		"an invalid provider network boundary": func(s *LaunchSpec) { s.NetworkPolicy = &egress.Policy{} },
		"native isolation":                     func(s *LaunchSpec) { s.Isolation = IsolationNative },
		"a mount path with a colon":            func(s *LaunchSpec) { s.Binds = []ContainerBind{{HostPath: "/srv/a:b", Target: "/srv/a"}} },
		"a relative mount target":              func(s *LaunchSpec) { s.Binds = []ContainerBind{{HostPath: os.TempDir(), Target: "a"}} },
		"a mount source that is missing": func(s *LaunchSpec) {
			s.Binds = []ContainerBind{{HostPath: filepath.Join(os.TempDir(), "olivares-no-such-dir"), Target: "/srv/a"}}
		},
		"a program the host does not have": func(s *LaunchSpec) { s.Program = filepath.Join(os.TempDir(), "olivares-no-such-tool") },
		"two sources for one mount point": func(s *LaunchSpec) {
			s.Binds = []ContainerBind{{HostPath: os.TempDir(), Target: "/srv/a"}, {HostPath: "/", Target: "/srv/a"}}
		},
		"a variable that would steer the container CLI": func(s *LaunchSpec) { s.Env = append(s.Env, EnvVar{Name: "LD_PRELOAD", Value: "/tmp/x.so"}) },
		"a registry credential variable":                func(s *LaunchSpec) { s.Env = append(s.Env, EnvVar{Name: "REGISTRY_AUTH_FILE", Value: "/tmp/auth"}) },
		"the CLI's daemon address, which argv would show": func(s *LaunchSpec) {
			s.Env = append(s.Env, EnvVar{Name: "DOCKER_HOST", Value: "tcp://secret-token@evil:2375"})
		},
		"the session bus address": func(s *LaunchSpec) {
			s.Env = append(s.Env, EnvVar{Name: "DBUS_SESSION_BUS_ADDRESS", Value: "unix:path=/x"})
		},
		"no program": func(s *LaunchSpec) { s.Program = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeRuntime(t, "podman")
			spec := containerSpec(t)
			mutate(&spec)
			if p, err := NewContainerRunner(f.rt).Launch(context.Background(), spec); err == nil {
				_ = p.Stop(context.Background())
				t.Fatalf("Launch accepted %s", name)
			}
			if f.ran("argv") {
				t.Errorf("the container CLI was started for a launch that had to be refused")
			}
		})
	}
}

func TestContainerRunner_OneSourceMountedTwiceIsOneMountAndTheStricterWins(t *testing.T) {
	f := newFakeRuntime(t, "podman")
	spec := containerSpec(t)
	dir := t.TempDir()
	spec.Binds = []ContainerBind{{HostPath: dir, Target: "/srv/a"}, {HostPath: dir, Target: "/srv/a", ReadOnly: true}}
	p := launchContainer(t, f, spec)
	_ = containerFrame(t, p)
	var mounts []string
	argv := f.argv(t)
	for i, a := range argv {
		if a == "-v" && strings.Contains(argv[i+1], ":/srv/a") {
			mounts = append(mounts, argv[i+1])
		}
	}
	if len(mounts) != 1 || !strings.HasSuffix(mounts[0], ":/srv/a:ro") {
		t.Errorf("mounts of /srv/a = %q, want one, read-only", mounts)
	}
}

func TestContainerRunner_NamesTheCLIOwnsTakeTheSessionsValueInArgvAndOthersTravelByName(t *testing.T) {
	f := newFakeRuntime(t, "docker")
	spec := containerSpec(t)
	spec.Env = append(spec.Env, EnvVar{Name: "XDG_CONFIG_HOME", Value: "/srv/cfg"}, EnvVar{Name: "OPENCODE_THING", Value: "secret-thing"})
	p := launchContainer(t, f, spec)
	_ = containerFrame(t, p)
	argv := f.argv(t)
	if i := indexOf(argv, "XDG_CONFIG_HOME=/srv/cfg"); i < 1 || argv[i-1] != "-e" {
		t.Errorf("a name the CLI reads for itself must carry the session's value inline: %q", argv)
	}
	if i := indexOf(argv, "OPENCODE_THING"); i < 1 || argv[i-1] != "-e" || strings.Contains(strings.Join(argv, "\n"), "secret-thing") {
		t.Errorf("any other name travels by name only: %q", argv)
	}
	if indexOf(argv, "--init") < 0 {
		t.Errorf("docker keeps its init outside the Olivares tool domain: %q", argv)
	}
}

func TestContainerRunner_SkipsDockerInitOnlyForTheOlivaresToolDomain(t *testing.T) {
	for _, label := range []string{"olivares_tool_exec_t", "olivares_tool_claude_exec_t"} {
		t.Run(label, func(t *testing.T) {
			f := newFakeRuntime(t, "docker")
			f.rt.SELinux = true
			spec := containerSpec(t)
			restore := hostSELinuxType
			hostSELinuxType = func(string) (string, error) { return label, nil }
			t.Cleanup(func() { hostSELinuxType = restore })
			p := launchContainer(t, f, spec)
			_ = containerFrame(t, p)
			if argv := f.argv(t); indexOf(argv, "--init") >= 0 {
				t.Errorf("runc, not docker-init, performs the olivares_tool_t transition: %q", argv)
			}
		})
	}
}

func TestContainerRunner_EgressStartsInTheRelayDomain(t *testing.T) {
	for _, label := range []string{"olivares_tool_exec_t", "olivares_tool_claude_exec_t"} {
		t.Run(label, func(t *testing.T) {
			f := newFakeRuntime(t, "docker")
			f.rt.SELinux = true
			spec := containerSpec(t)
			spec.NetworkPolicy = &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}}
			restorePrepare := prepareContainer
			prepareContainer = func(context.Context, egress.Policy, []string) ([]string, string, func() error, func(), error) {
				return []string{"--print"}, t.TempDir(), func() error { return nil }, func() {}, nil
			}
			t.Cleanup(func() { prepareContainer = restorePrepare })
			restore := hostSELinuxType
			hostSELinuxType = func(string) (string, error) { return label, nil }
			t.Cleanup(func() { hostSELinuxType = restore })
			p := launchContainer(t, f, spec)
			_ = containerFrame(t, p)
			argv := f.argv(t)
			if indexOf(argv, "label=type:olivares_container_relay_t") < 0 {
				t.Errorf("the egress container does not start in the relay domain: %q", argv)
			}
			if indexOf(argv, "label=type:olivares_tool_t") >= 0 {
				t.Errorf("the egress container still starts in the tool domain: %q", argv)
			}
			if indexOf(argv, "--init") >= 0 {
				t.Errorf("runc, not docker-init, performs the relay transition: %q", argv)
			}
		})
	}
}

func TestContainerRunner_RefusesEgressForANonToolProgram(t *testing.T) {
	f := newFakeRuntime(t, "docker")
	f.rt.SELinux = true
	spec := containerSpec(t)
	spec.NetworkPolicy = &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}}
	restorePrepare := prepareContainer
	prepareContainer = func(context.Context, egress.Policy, []string) ([]string, string, func() error, func(), error) {
		return []string{"--print"}, t.TempDir(), func() error { return nil }, func() {}, nil
	}
	t.Cleanup(func() { prepareContainer = restorePrepare })
	restore := hostSELinuxType
	hostSELinuxType = func(string) (string, error) { return "olivares_exec_t", nil }
	t.Cleanup(func() { hostSELinuxType = restore })
	if _, err := newContainerRunner(f.rt).Launch(context.Background(), spec); err == nil {
		t.Fatal("a non-tool program entered the egress relay")
	}
}

func TestContainerRunner_NoPolicyNeverUsesTheRelayLabel(t *testing.T) {
	f := newFakeRuntime(t, "docker")
	f.rt.SELinux = true
	spec := containerSpec(t)
	restore := hostSELinuxType
	hostSELinuxType = func(string) (string, error) { return "olivares_tool_exec_t", nil }
	t.Cleanup(func() { hostSELinuxType = restore })
	p := launchContainer(t, f, spec)
	_ = containerFrame(t, p)
	argv := f.argv(t)
	if indexOf(argv, "label=type:olivares_container_relay_t") >= 0 {
		t.Errorf("a launch without a network policy uses the relay label: %q", argv)
	}
	if indexOf(argv, "label=type:olivares_tool_t") < 0 {
		t.Errorf("the ordinary tool label is missing: %q", argv)
	}
}

func TestContainerRunner_ProbesTheOriginalProgramBeforeTheEgressWrapper(t *testing.T) {
	f := newFakeRuntime(t, "docker")
	f.rt.SELinux = true
	spec := containerSpec(t)
	original := spec.Program
	spec.NetworkPolicy = &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}}
	restorePrepare := prepareContainer
	prepareContainer = func(context.Context, egress.Policy, []string) ([]string, string, func() error, func(), error) {
		return []string{"--print"}, t.TempDir(), func() error { return nil }, func() {}, nil
	}
	t.Cleanup(func() { prepareContainer = restorePrepare })
	var probes []string
	restore := hostSELinuxType
	hostSELinuxType = func(path string) (string, error) {
		probes = append(probes, path)
		return "olivares_tool_exec_t", nil
	}
	t.Cleanup(func() { hostSELinuxType = restore })
	p := launchContainer(t, f, spec)
	_ = containerFrame(t, p)
	if len(probes) == 0 || probes[len(probes)-1] != original {
		t.Errorf("the label probe saw %v, want original program %q", probes, original)
	}
}

func TestContainerRunner_PodmanDoesNotUseDockerInit(t *testing.T) {
	f := newFakeRuntime(t, "podman")
	spec := containerSpec(t)
	p := launchContainer(t, f, spec)
	_ = containerFrame(t, p)
	if argv := f.argv(t); indexOf(argv, "--init") >= 0 {
		t.Errorf("Podman uses its own init support, not Docker's --init: %q", argv)
	}
}

func TestContainerRunner_ARemovalThatFailsIsReportedAndATidyOneIsNot(t *testing.T) {
	for name, tc := range map[string]struct {
		fail     bool
		wantWarn bool
	}{"tidy": {false, false}, "daemon unreachable": {true, true}} {
		t.Run(name, func(t *testing.T) {
			f := newFakeRuntime(t, "podman")
			if tc.fail {
				if err := os.WriteFile(filepath.Join(f.log, "rmfail"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var warned []string
			f.rt.Warn = func(msg string, kv ...any) { warned = append(warned, fmt.Sprint(append([]any{msg}, kv...)...)) }
			p := launchContainer(t, f, containerSpec(t))
			_ = containerFrame(t, p)
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.Stop(stopCtx); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if got := len(warned) > 0; got != tc.wantWarn {
				t.Errorf("warned = %v (%q), want %v", got, warned, tc.wantWarn)
			}
			if tc.wantWarn && !strings.Contains(strings.Join(warned, ""), "olivares-session-") {
				t.Errorf("the warning does not name the container: %q", warned)
			}
		})
	}
}

func TestContainerRunner_ShareTheHostNetworkOnlyToReachTheEngineOnLoopback(t *testing.T) {
	for name, tc := range map[string]struct {
		env  []EnvVar
		mcp  string
		host bool
	}{
		"nothing names the engine":                {nil, "", false},
		"the engine's hook endpoint on 127.0.0.1": {[]EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: "https://127.0.0.1:7/v1/hooks"}}, "", true},
		"the inference gateway on localhost":      {[]EnvVar{{Name: "ANTHROPIC_BASE_URL", Value: "http://localhost:8080"}}, "", true},
		"the inference gateway on ::1":            {[]EnvVar{{Name: "ANTHROPIC_BASE_URL", Value: "http://[::1]:8080/x"}}, "", true},
		"the session's MCP server on loopback":    {nil, "http://127.0.0.1:9/mcp", true},
		"a provider on the internet":              {[]EnvVar{{Name: "ANTHROPIC_BASE_URL", Value: "https://api.example.com"}}, "", false},
		"a value that is not a URL":               {[]EnvVar{{Name: "OLIVARES_NOTE", Value: "127.0.0.1"}}, "", false},
		"a secret whose value is a loopback URL":  {[]EnvVar{{Name: "MY_VAULT_SECRET", Value: "http://127.0.0.1:1/"}}, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeRuntime(t, "podman")
			spec := containerSpec(t)
			spec.Env = append(spec.Env, tc.env...)
			spec.SessionMCPURL = tc.mcp
			p := launchContainer(t, f, spec)
			_ = containerFrame(t, p)
			if got := indexOf(f.argv(t), "--network=host") >= 0; got != tc.host {
				t.Errorf("--network=host = %v, want %v (argv %q)", got, tc.host, f.argv(t))
			}
		})
	}
}

func TestContainerRunner_AnImageThatWouldBeReadAsAnOptionIsRefused(t *testing.T) {
	for _, image := range []string{"", "--privileged", "a b", "x\ny"} {
		f := newFakeRuntime(t, "podman")
		f.rt.Image = image
		if p, err := NewContainerRunner(f.rt).Launch(context.Background(), containerSpec(t)); err == nil {
			_ = p.Stop(context.Background())
			t.Errorf("image %q was accepted", image)
		}
		if f.ran("argv") {
			t.Errorf("image %q reached the container CLI", image)
		}
	}
}

func TestContainerRunner_RuntimeThatCannotStartIsNamedNotMasked(t *testing.T) {
	rt := ContainerRuntime{Path: filepath.Join(t.TempDir(), "podman"), Image: "x"}
	if _, err := NewContainerRunner(rt).Launch(context.Background(), containerSpec(t)); err == nil || !strings.Contains(err.Error(), "podman") {
		t.Fatalf("Launch = %v, want an error naming the missing container CLI", err)
	}
}

func TestRunnerWithContainers_RoutesByIsolation(t *testing.T) {
	f := newFakeRuntime(t, "podman")
	runner := NewRunnerWithContainers(NewProcRunner(), f.rt)

	// container goes to the container CLI.
	p, err := runner.Launch(context.Background(), containerSpec(t))
	if err != nil {
		t.Fatalf("container Launch: %v", err)
	}
	_ = containerFrame(t, p)
	_ = p.Stop(context.Background())
	if !f.ran("argv") {
		t.Errorf("a container launch did not reach the container CLI")
	}

	// native still runs the host process, with no container CLI involved.
	script := filepath.Join(t.TempDir(), "tool.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'native\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	native, err := runner.Launch(context.Background(), LaunchSpec{Program: script, Isolation: IsolationNative, WaitDelay: time.Second, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("native Launch: %v", err)
	}
	if got := containerFrame(t, native); got != "native" {
		t.Errorf("native frame = %q", got)
	}
	_, _ = native.Wait()

	// sandbox stays refused: nothing runs it.
	if _, err := runner.Launch(context.Background(), LaunchSpec{Program: "true", Isolation: IsolationSandbox}); err == nil {
		t.Errorf("a sandbox launch was accepted")
	}
}

func TestRunnerWithContainers_ReportsWhatItCanRun(t *testing.T) {
	f := newFakeRuntime(t, "podman")
	runner := NewRunnerWithContainers(NewProcRunner(), f.rt)
	inspector, ok := runner.(RunnerInspector)
	if !ok {
		t.Fatal("the composed runner must stay inspectable: the launch-readiness read depends on it")
	}
	for iso, want := range map[Isolation]RunnerIsolationState{
		IsolationNative:    RunnerIsolationSupported,
		IsolationContainer: RunnerIsolationSupported,
		IsolationSandbox:   RunnerIsolationUnsupported,
	} {
		obs, err := inspector.InspectLaunch(context.Background(), RunnerInspection{Program: "/bin/sh", Isolation: iso})
		if err != nil || obs.Isolation != want {
			t.Errorf("isolation %q: got %q (err %v), want %q", iso, obs.Isolation, err, want)
		}
	}
	// A runtime that has disappeared since boot is not reported as ready.
	gone := NewRunnerWithContainers(NewProcRunner(), ContainerRuntime{Path: filepath.Join(t.TempDir(), "podman"), Image: "x"})
	obs, err := gone.(RunnerInspector).InspectLaunch(context.Background(), RunnerInspection{Program: "/bin/sh", Isolation: IsolationContainer})
	if err != nil || obs.Isolation != RunnerIsolationUnsupported {
		t.Errorf("missing container CLI: got %q (err %v), want unsupported", obs.Isolation, err)
	}
	if _, ok := runner.(RunnerTransportReporter); !ok {
		t.Errorf("the composed runner dropped the native runner's transport report")
	}
}

// passwdMount is the host file the runtime mounts over the image's /etc/passwd, if the
// argv mounts one.
func passwdMount(argv []string) (string, bool) {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] != "-v" {
			continue
		}
		parts := strings.Split(argv[i+1], ":")
		if len(parts) >= 3 && parts[1] == "/etc/passwd" && strings.Contains(parts[2], "ro") {
			return parts[0], true
		}
	}
	return "", false
}

func TestContainerRunner_BoundNetworkNeverSharesHost(t *testing.T) {
	for _, runtime := range []string{"podman", "docker"} {
		t.Run(runtime, func(t *testing.T) {
			spec := containerSpec(t)
			spec.NetworkPolicy = &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}}
			spec.Env = append(spec.Env, EnvVar{Name: "ANTHROPIC_BASE_URL", Value: "http://127.0.0.1:11434"})
			args, err := newContainerRunner(ContainerRuntime{Path: runtime, Image: "test"}).runArgs(spec, "test", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if indexOf(args, "--network=none") < 0 || indexOf(args, "--network=host") >= 0 {
				t.Fatalf("bound container network is not isolated: %v", args)
			}
		})
	}
}

func TestContainerBindsKeepsNoRelabelOnDuplicateProgram(t *testing.T) {
	spec := containerSpec(t)
	spec.Binds = []ContainerBind{{HostPath: spec.Program, Target: spec.Program, ReadOnly: true, NoRelabel: true}}
	binds, err := containerBinds(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(binds) != 1 || !binds[0].NoRelabel || strings.Contains(binds[0].volume(true), ",z") {
		t.Fatalf("engine program would be relabeled: %+v", binds)
	}
}

func TestContainerRunner_RefusesWhenRelayDoesNotBecomeReady(t *testing.T) {
	f := newFakeRuntime(t, "docker")
	spec := containerSpec(t)
	spec.NetworkPolicy = &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	p, err := NewContainerRunner(f.rt).Launch(ctx, spec)
	if err == nil {
		if p != nil {
			_ = p.Stop(context.Background())
		}
		t.Fatal("reported launch before the network relay was ready")
	}
	if !strings.Contains(err.Error(), "network boundary") {
		t.Fatalf("lost boundary refusal: %v", err)
	}
	if !f.ran("argv") {
		t.Fatal("runtime was not exercised")
	}
	if !f.ran("rm.log") {
		t.Fatal("failed launch left its container behind")
	}
	argv := f.argv(t)
	for i, arg := range argv {
		if arg == "-v" && i+1 < len(argv) && strings.Contains(argv[i+1], "eg-") && !strings.Contains(argv[i+1], ":ro") {
			t.Fatalf("relay directory is writable: %s", argv[i+1])
		}
	}
	if strings.Contains(strings.Join(argv, " "), "pv.sock") {
		t.Fatal("container exposes a writable preview socket")
	}
	if indexOf(argv, "--network=none") < 0 || indexOf(argv, "--network=host") >= 0 {
		t.Fatalf("unconfined launch: %v", argv)
	}
}
