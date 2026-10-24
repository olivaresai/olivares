// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// THE CONTAINER RUNNER: isolation=container, wired.
//
// A container session is the same child as a native one (the same program, argv and
// environment, the same stdio bridge) started inside `podman run` or `docker run`
// instead of beside the engine. The engine drives the CLI; it never talks to a daemon
// of its own and never asks for a privilege the CLI does not already give its user.
// podman is the platform's own on AlmaLinux and RHEL 10 (rootless, SELinux-aware), and
// Docker answers the same arguments.
//
// WHAT THE CONTAINER IS: the tool's program is the host's, mounted read-only at its
// own path (version selection and signature checks stay where they are); everything
// else the child sees is what the module decided in LaunchSpec.Workspace and
// LaunchSpec.Binds. The root filesystem is read-only, no capability is kept and no
// privilege can be gained. The session's network is the container runtime's default,
// so a launch that needs the provider network boundary (a NetworkPolicy, which only
// the native runner holds) is refused here instead of running with a wider network. The
// one exception is a launch whose environment names the engine on a loopback address
// (its tool-call hooks, MCP server or inference gateway): the container then shares the
// host's network, which is the network a native session has, because that is the only
// way to reach those endpoints.
//
// ⛔ A SECRET NEVER TRAVELS IN ARGV. Argv is readable by every user on the host; a
// process's environment is readable only by its own user, which is where a native
// child's secrets already are. The explicit environment is therefore passed with
// `-e NAME`, which makes the CLI copy the value from its own environment into the
// container: argv carries the name and never the value. The few names the CLI itself
// reads (HOME, PATH, XDG_RUNTIME_DIR, …) are the exception: the session's value goes in
// argv as `-e NAME=value`, because the CLI must keep its own. Those are not secrets.
// The CLI's own environment is otherwise a short allowlist, never the engine's.

// SessionContainerLabel marks every container the engine starts, so one that outlives
// a crashed engine can be found: `podman ps --filter label=ai.olivares.session`.
const SessionContainerLabel = "ai.olivares.session"

const containerNamePrefix = "olivares-session-"

// containerRemoveTimeout bounds the cleanup call after a session ends.
const containerRemoveTimeout = 10 * time.Second

// containerClientEnv are the host variables the CLI itself needs: where its storage,
// runtime directory and socket are. They configure the CLI; none of them reaches the
// container.
var containerClientEnv = []string{
	"PATH", "HOME", "USER", "LANG", "LC_ALL", "TMPDIR",
	"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "DBUS_SESSION_BUS_ADDRESS",
	"CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINERS_CONF",
	"DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT",
}

// containerSessionMayName are the CLI-owned names a session may carry a value for: where
// its own home and temporary files are. The rest steer the CLI (its daemon, its
// credentials), and a session's value for one would sit in argv.
var containerSessionMayName = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LANG": true, "LC_ALL": true, "TMPDIR": true,
	"XDG_RUNTIME_DIR": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
}

// ContainerRuntime is the container CLI the engine drives and the image sessions run in.
type ContainerRuntime struct {
	// Path is the podman or docker executable. Its base name tells the two apart.
	Path string
	// Image is where a session runs. It must hold what the tool shells out to (git,
	// a shell) and be compatible with the host's program, which is mounted into it.
	Image string
	// SELinux is true when the host enforces SELinux: mounts are then labelled (z) so
	// the container may read them.
	SELinux bool
	// Warn reports what the runner cannot return to a caller: a container it could not
	// remove after its session ended. Nil says nothing.
	Warn func(msg string, kv ...any)
}

func (rt ContainerRuntime) isPodman() bool {
	return strings.HasPrefix(filepath.Base(rt.Path), "podman")
}

// ready is the effect-free check behind the readiness read: the CLI is still there.
func (rt ContainerRuntime) ready() bool {
	_, err := exec.LookPath(rt.Path)
	return err == nil && rt.Image != ""
}

type containerRunner struct {
	rt     ContainerRuntime
	native *procRunner // the same pipes, pumps and teardown as a native child
}

// NewContainerRunner returns the Runner for isolation=container over the stdio pipes.
// It refuses any other isolation: routing is NewRunnerWithContainers' job.
func NewContainerRunner(rt ContainerRuntime) Runner { return newContainerRunner(rt) }

func newContainerRunner(rt ContainerRuntime) *containerRunner {
	return &containerRunner{rt: rt, native: &procRunner{lineCap: maxOutputLine}}
}

func (cr *containerRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if spec.Isolation != IsolationContainer {
		return nil, fmt.Errorf("sessions: the container runner runs isolation %q only, not %q", IsolationContainer, spec.Isolation)
	}
	if strings.TrimSpace(spec.Program) == "" {
		return nil, errors.New("sessions: launch spec has no program")
	}
	if spec.NetworkPolicy != nil {
		// The proxy and network namespace that hold a session to its provider belong
		// to the native runner. Dropping them would give the session the whole network.
		return nil, networkBoundaryErr(errors.New("a container session cannot be held to its provider's endpoint yet; run this provider with native isolation"))
	}
	if err := validateExplicitEnv(spec.Env); err != nil {
		return nil, err
	}
	envArgs, cliEnv, err := containerEnv(spec)
	if err != nil {
		return nil, err
	}
	name, err := containerName()
	if err != nil {
		return nil, err
	}
	passwdBinds, forgetPasswd, err := cr.engineUserBinds()
	if err != nil {
		return nil, err
	}
	args, err := cr.runArgs(spec, name, envArgs, passwdBinds)
	if err != nil {
		forgetPasswd()
		return nil, err
	}
	cmd := exec.CommandContext(ctx, cr.rt.Path, args...) // #nosec G204 -- the runtime path is operator-detected and the arguments are built here without a shell
	cmd.Dir = "/"
	cmd.Env = append(cr.clientEnv(), cliEnv...)
	// The CLI forwards a TERM to the container; the default KILL would leave a
	// container the daemon owns running after the engine stopped.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	if spec.WaitDelay > 0 {
		cmd.WaitDelay = spec.WaitDelay
	}
	p, err := cr.native.launchPipes(cmd, spec.WaitDelay, spec.StripImageData, func() {
		cr.remove(name)
		forgetPasswd()
	})
	if err != nil {
		forgetPasswd()
	}
	return p, err
}

// engineUserBinds gives a Docker session an /etc/passwd entry for the engine's id. Docker
// runs the process as --user uid:gid and writes no entry for that id, and OpenSSH refuses
// to run without one ("No user exists for uid"). Podman writes the entry itself, and root
// has one in every image. The file holds root and the engine user only; it is removed
// when the session ends.
func (cr *containerRunner) engineUserBinds() ([]ContainerBind, func(), error) {
	uid, gid := os.Geteuid(), os.Getegid()
	if cr.rt.isPodman() || uid == 0 {
		return nil, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "olivares-container-")
	if err != nil {
		return nil, func() {}, fmt.Errorf("sessions: the session's passwd directory: %w", err)
	}
	forget := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "passwd")
	if err := os.WriteFile(path, passwdEntries(uid, gid), 0o644); err != nil {
		forget()
		return nil, func() {}, fmt.Errorf("sessions: the session's passwd file: %w", err)
	}
	return []ContainerBind{{HostPath: path, Target: "/etc/passwd", ReadOnly: true}}, forget, nil
}

// passwdEntries is the session's /etc/passwd: root, and the engine's id under the name the
// host gives it ("olivares" when the host has none).
func passwdEntries(uid, gid int) []byte {
	name := "olivares"
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil && u.Username != "" && !strings.ContainsAny(u.Username, ":\n\r\x00") {
		name = u.Username
	}
	return []byte(fmt.Sprintf("root:x:0:0:root:/:/bin/sh\n%s:x:%d:%d:%s:/tmp:/bin/sh\n", name, uid, gid, name))
}

// runArgs is everything after the CLI's name. Order matters in one place only: the
// image ends the options and the program and its arguments follow it untouched.
func (cr *containerRunner) runArgs(spec LaunchSpec, name string, envArgs []string, passwdBinds []ContainerBind) ([]string, error) {
	args := []string{
		"run", "--rm", "-i", "--name", name, "--label", SessionContainerLabel + "=1",
		"--read-only", "--tmpfs", "/tmp:rw,exec,nosuid,nodev",
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		// The program is what runs, not whatever the image's entrypoint wraps it in.
		"--entrypoint", "",
	}
	if cr.rt.Image == "" || strings.HasPrefix(cr.rt.Image, "-") || strings.ContainsAny(cr.rt.Image, " \t\r\n\x00") {
		return nil, fmt.Errorf("sessions: %q is not a container image name", cr.rt.Image)
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if cr.rt.isPodman() && uid != 0 {
		// Rootless podman maps the engine user to root inside by default, and tools
		// refuse their unattended modes as root; keep the real id instead.
		args = append(args, "--userns=keep-id")
	} else {
		// A daemon runs as root, so files the session writes in the folder would be
		// root's unless the container runs as the engine user.
		args = append(args, "--user", fmt.Sprintf("%d:%d", uid, gid))
	}
	if !cr.rt.isPodman() {
		args = append(args, "--init") // podman's needs catatonit, which the host may lack
	}
	args = append(args, envArgs...)
	if reachesEngineLoopback(spec) {
		// The tool-call hooks, the session's MCP server and the inference gateway are
		// endpoints on the engine's loopback. A container with a network of its own
		// cannot reach them, so it shares the host's: the network a native session has.
		args = append(args, "--network=host")
	}
	if filepath.IsAbs(spec.Dir) {
		args = append(args, "-w", spec.Dir)
	}

	binds, err := containerBinds(spec)
	if err != nil {
		return nil, err
	}
	if !bindCovers(binds, "/etc/passwd") {
		binds = append(binds, passwdBinds...)
	}
	for _, b := range binds {
		if err := b.valid(); err != nil {
			return nil, err
		}
		args = append(args, "-v", b.volume(cr.rt.SELinux))
	}
	if home := containerHome(spec); home != "" && !bindCovers(binds, home) {
		if strings.ContainsAny(home, ":,\n\r\x00") {
			return nil, fmt.Errorf("sessions: HOME %q cannot be a tmpfs in a container: it has a ':' or ','", home)
		}
		args = append(args, "--tmpfs", fmt.Sprintf("%s:rw,nosuid,nodev,mode=0700,uid=%d,gid=%d", home, uid, gid))
	}
	args = append(args, cr.rt.Image, spec.Program)
	return append(args, spec.Args...), nil
}

// reachesEngineLoopback reports a launch that names the engine on a loopback address: the
// engine's own variables (OLIVARES_*: the hook endpoint), its inference gateway
// (ANTHROPIC_BASE_URL) and the session's MCP server. Nothing else counts, so a value a
// person or a vault supplies cannot turn host networking on.
func reachesEngineLoopback(spec LaunchSpec) bool {
	endpoints := []string{spec.SessionMCPURL}
	for _, e := range spec.Env {
		if strings.HasPrefix(e.Name, "OLIVARES_") || e.Name == "ANTHROPIC_BASE_URL" {
			endpoints = append(endpoints, e.Value)
		}
	}
	for _, endpoint := range endpoints {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return true
		}
	}
	return false
}

// containerBinds is the program, the workspace and the module's other binds, in the
// order they are mounted, one per target. Two mounts of one target are one mount: the
// stricter of the two when they name the same source, a refusal when they do not.
func containerBinds(spec LaunchSpec) ([]ContainerBind, error) {
	var all []ContainerBind
	if filepath.IsAbs(spec.Program) {
		// The host's own program, read-only, at the path the launch already names. A
		// program the host does not have is a configuration fact, not something to
		// leave for the image to fail on.
		real, err := filepath.EvalSymlinks(spec.Program)
		if err != nil {
			return nil, fmt.Errorf("sessions: the tool's program cannot be mounted in the container: %w", err)
		}
		if info, err := os.Stat(real); err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("sessions: the tool's program %q is not a file", spec.Program)
		}
		all = append(all, ContainerBind{HostPath: real, Target: spec.Program, ReadOnly: true})
	}
	if ws := spec.Workspace; ws != nil {
		all = append(all, ContainerBind{HostPath: ws.HostPath, Target: ws.ContainerTarget, ReadOnly: ws.ReadOnly})
	}
	all = append(all, spec.Binds...)

	var binds []ContainerBind
	at := make(map[string]int, len(all))
	for _, b := range all {
		i, dup := at[b.Target]
		switch {
		case !dup:
			at[b.Target] = len(binds)
			binds = append(binds, b)
		case binds[i].HostPath != b.HostPath:
			return nil, fmt.Errorf("sessions: %q is the mount point of both %q and %q", b.Target, binds[i].HostPath, b.HostPath)
		default:
			binds[i].ReadOnly = binds[i].ReadOnly || b.ReadOnly
		}
	}
	return binds, nil
}

func (b ContainerBind) valid() error {
	for _, p := range []string{b.HostPath, b.Target} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, ":,\n\r\x00") {
			return fmt.Errorf("sessions: %q cannot be mounted in a container: a mount path is absolute, clean and has no ':' or ','", p)
		}
	}
	// Docker makes a missing source a directory owned by root, in the person's folder.
	if _, err := os.Lstat(b.HostPath); err != nil {
		return fmt.Errorf("sessions: %q cannot be mounted in a container: %w", b.HostPath, err)
	}
	return nil
}

// volume is the -v form of the mount. The SELinux label is not applied to a bind marked
// NoRelabel (the engine's own program, the host's policy) or to the host's own program
// under /usr: those are already readable to a container, and relabelling them would
// change what the host itself may run or read.
func (b ContainerBind) volume(selinux bool) string {
	var opts []string
	if b.ReadOnly {
		opts = append(opts, "ro")
	}
	if selinux && !b.NoRelabel && !strings.HasPrefix(b.HostPath, "/usr/") {
		opts = append(opts, "z")
	}
	out := b.HostPath + ":" + b.Target
	if len(opts) > 0 {
		out += ":" + strings.Join(opts, ",")
	}
	return out
}

// containerHome is the HOME the session was given, or "" when it names none. A launch
// that names none is given /tmp (containerEnv).
func containerHome(spec LaunchSpec) string {
	for _, e := range spec.Env {
		if e.Name == envUserHome && filepath.IsAbs(e.Value) {
			return filepath.Clean(e.Value)
		}
	}
	return ""
}

// bindCovers reports whether a mount already provides path (it is a target, or inside one).
func bindCovers(binds []ContainerBind, path string) bool {
	for _, b := range binds {
		if path == b.Target || strings.HasPrefix(path, strings.TrimSuffix(b.Target, "/")+"/") {
			return true
		}
	}
	return false
}

// containerEnv is the session's environment as `-e` arguments plus the variables the CLI
// must hold so it can copy them (see the note at the top). The host names a launch
// allowlists (EnvAllow) come along; nothing else of the engine's does.
//
// A name that would change how the CLI itself runs (a library to preload, where it
// finds its daemon or registry credentials, a proxy) is refused unless it is one of the
// names the CLI owns, whose session value goes in argv and leaves the CLI's alone.
func containerEnv(spec LaunchSpec) (args, cliEnv []string, err error) {
	cliOwns := make(map[string]bool, len(containerClientEnv))
	for _, name := range containerClientEnv {
		cliOwns[name] = true
	}
	seen := make(map[string]bool, len(spec.Env))
	add := func(name, value string) error {
		seen[name] = true
		if containerSessionMayName[name] {
			args = append(args, "-e", name+"="+value)
			return nil
		}
		if cliOwns[name] || cliAffectingEnv(name) {
			return fmt.Errorf("sessions: %s cannot be passed to a container session: the container CLI would read it too", name)
		}
		args = append(args, "-e", name)
		cliEnv = append(cliEnv, name+"="+value)
		return nil
	}
	for _, e := range spec.Env {
		if validEnvName(e.Name) && !seen[e.Name] {
			if err := add(e.Name, e.Value); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, name := range spec.EnvAllow {
		name = strings.TrimSpace(name)
		if seen[name] || !validEnvName(name) || forbiddenInheritedEnvName(name) || engineSecretEnvName(name) {
			continue
		}
		if value, ok := os.LookupEnv(name); ok {
			if err := add(name, value); err != nil {
				return nil, nil, err
			}
		}
	}
	if !seen[envUserHome] {
		// The root filesystem is read-only; /tmp is the one place a tool without a home
		// can keep its state.
		args = append(args, "-e", envUserHome+"=/tmp")
	}
	return args, cliEnv, nil
}

// cliAffectingEnv names the variables that change how a container CLI (or the loader
// that starts it) behaves.
func cliAffectingEnv(name string) bool {
	upper := strings.ToUpper(name)
	for _, prefix := range []string{"LD_", "PODMAN_", "CONTAINER_", "CONTAINERS_", "DOCKER_", "REGISTRY_", "SSL_CERT_", "DBUS_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return strings.HasSuffix(upper, "_PROXY")
}

func containerName() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("sessions: container name: %w", err)
	}
	return containerNamePrefix + hex.EncodeToString(raw[:]), nil
}

// clientEnv is the CLI's own environment: the allowlisted names, from the engine's.
func (cr *containerRunner) clientEnv() []string {
	var out []string
	for _, name := range containerClientEnv {
		if value, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}

// remove deletes the container by name once the session has ended. --rm already did
// when the container exited cleanly, so "no such container" is the usual answer and
// says nothing. Any other failure leaves a container running or stopped under the
// session's name, which the runner cannot return to a caller (the session is over): it
// is reported through Warn with the name, and SessionContainerLabel finds it.
func (cr *containerRunner) remove(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), containerRemoveTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cr.rt.Path, "rm", "-f", name) // #nosec G204 -- the name is generated here
	cmd.Dir = "/"
	cmd.Env = cr.clientEnv()
	out, err := cmd.CombinedOutput()
	if err == nil || cr.rt.Warn == nil || strings.Contains(strings.ToLower(string(out)), "no such") {
		return
	}
	text := strings.TrimSpace(string(out))
	if len(text) > 300 {
		text = text[:300]
	}
	cr.rt.Warn("sessions: a session's container could not be removed after it ended; remove it by name",
		"container", name, "error", err.Error(), "output", text)
}

// InspectLaunch answers the readiness read without starting anything: the CLI is
// present and the host's program is an executable.
func (cr *containerRunner) InspectLaunch(_ context.Context, in RunnerInspection) (RunnerObservation, error) {
	out := RunnerObservation{Isolation: RunnerIsolationUnsupported}
	if in.Isolation == IsolationContainer && cr.rt.ready() {
		out.Isolation = RunnerIsolationSupported
	}
	out.Program = inspectProgramMetadata(in.Program)
	return out, nil
}

// containerRouter sends isolation=container to the container runner and everything
// else to the native runner, which keeps refusing what only another runner can hold.
type containerRouter struct {
	native    Runner
	container *containerRunner
}

// NewRunnerWithContainers adds the container runner to a native one: one Runner for the
// module, chosen per launch by the isolation the launch asks for. Without it a
// container launch is refused, as before.
func NewRunnerWithContainers(native Runner, rt ContainerRuntime) Runner {
	return &containerRouter{native: native, container: newContainerRunner(rt)}
}

func (r *containerRouter) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if spec.Isolation == IsolationContainer {
		return r.container.Launch(ctx, spec)
	}
	return r.native.Launch(ctx, spec)
}

func (r *containerRouter) InspectLaunch(ctx context.Context, in RunnerInspection) (RunnerObservation, error) {
	if in.Isolation == IsolationContainer {
		return r.container.InspectLaunch(ctx, in)
	}
	inspector, ok := r.native.(RunnerInspector)
	if !ok {
		return RunnerObservation{}, errors.New("sessions: the native runner cannot be inspected")
	}
	return inspector.InspectLaunch(ctx, in)
}

// ProvidesTransport is the native runner's: a container session bridges the same
// stdio pipes.
func (r *containerRouter) ProvidesTransport() cliruntime.Transport {
	if reporter, ok := r.native.(RunnerTransportReporter); ok {
		return reporter.ProvidesTransport()
	}
	return ""
}
