// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
)

// THE CONTAINER FLAVOUR. A node with Podman or Docker may run the Ollama
// image as this engine's managed container instead of the installed program: the
// models directory is mounted at /models, the host port is published on the loopback
// address the service uses, and the same start, stop, pull and restart-with-the-engine
// apply. The installed program stays the default; the person asks for the container.

const (
	// OllamaContainerImageEnv names the image the container flavour runs.
	OllamaContainerImageEnv = "OLIVARES_OLLAMA_CONTAINER_IMAGE"
	// DefaultOllamaContainerImage is Ollama's official image.
	DefaultOllamaContainerImage = "docker.io/ollama/ollama:latest"
	// ollamaROCmImage is the build for an AMD host, which needs the ROCm libraries in
	// the image.
	ollamaROCmImage = "docker.io/ollama/ollama:rocm"

	ollamaContainerName  = "olivares-ollama"
	ollamaContainerLabel = "ai.olivares.ollama"
	ollamaContainerPort  = "11434"
	// ollamaContainerReadyTimeout is longer than the child process': the first start may
	// have to pull the image first.
	ollamaContainerReadyTimeout = 10 * time.Minute
)

// The flavours a status row reports.
const (
	ollamaRuntimeProcess   = "process"
	ollamaRuntimeContainer = "container"
)

// ollamaFlavour is the flavour word for a start that asked for the container or not.
func ollamaFlavour(container bool) string {
	if container {
		return ollamaRuntimeContainer
	}
	return ollamaRuntimeProcess
}

// OllamaContainer is the composition's container flavour. A nil OllamaConfig.Container
// means this node has no such flavour and the start refuses it.
type OllamaContainer struct {
	// Runtime is the podman or docker executable. Empty: discover it from the same
	// setting container sessions use (OLIVARES_SESSION_CONTAINER_RUNTIME).
	Runtime string
	// Image is the Ollama image. Empty: OLIVARES_OLLAMA_CONTAINER_IMAGE, or the
	// official image (the ROCm build on an AMD host).
	Image string
	// SELinux relabels the models mount when the host enforces it (the z option).
	// Discovery fills it; a composition that names Runtime states it.
	SELinux bool
	// GPU is the host GPU a container may be given: "nvidia", "amd" or "" for none.
	// DetectOllamaGPU answers it.
	GPU string
}

// resolveOllamaContainer finds the runtime, image and mount of a container start, or
// says what the node is missing. It is called before anything is started. A remembered
// start (was, non-nil) supplies the runtime, image and SELinux mode that actually ran:
// the restart repeats the person's start, and only the runtime that created a container
// can remove it, whatever the settings say today. Containers switched off refuse both
// paths, then and now.
func (m *Module) resolveOllamaContainer(cfg OllamaConfig, was *ollamaRemembered) (OllamaContainer, error) {
	c := cfg.Container
	if c == nil {
		return OllamaContainer{}, errors.New("This node cannot run Ollama in a container; start the installed Ollama instead.")
	}
	getenv := cfg.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.EqualFold(strings.TrimSpace(getenv(sessions.ContainerRuntimeEnv)), "off") {
		return OllamaContainer{}, fmt.Errorf("containers are switched off on this node (%s=off); start the installed Ollama instead", sessions.ContainerRuntimeEnv)
	}
	if name := remoteDaemonSetting(getenv); name != "" {
		return OllamaContainer{}, fmt.Errorf("Ollama runs in a container on this node's own container daemon, and %s points at another one; unset it, or start the installed Ollama instead", name)
	}
	out := OllamaContainer{Runtime: c.Runtime, Image: strings.TrimSpace(c.Image), SELinux: c.SELinux, GPU: strings.ToLower(strings.TrimSpace(c.GPU))}
	// The composition names a runtime only as code it trusts; still refuse a value that
	// is not an absolute executable path, so a future composition fed by operator input
	// cannot plant a name or an option where a program belongs.
	if out.Runtime != "" && !usableRuntime(out.Runtime) {
		return OllamaContainer{}, fmt.Errorf("the configured container runtime %q is not an absolute executable path", out.Runtime)
	}
	if out.Runtime != "" && !supportedOllamaRuntime(out.Runtime) {
		return OllamaContainer{}, fmt.Errorf("the configured container runtime %q is not Podman or Docker", out.Runtime)
	}
	if was != nil && was.Container {
		// A remembered runtime that is not an existing executable path is not evidence:
		// discover today's instead of running something else (or failing on a program
		// that is gone).
		if runtime := strings.TrimSpace(was.Runtime); trustedOllamaRuntime(runtime) {
			if _, err := os.Stat(runtime); err == nil {
				out.Runtime = runtime
			}
		}
		if image := strings.TrimSpace(was.Image); image != "" {
			out.Image = image
		}
		out.SELinux = was.SELinux
	}
	if out.Runtime == "" {
		cli, err := sessions.DetectContainerCLI(getenv, m.findProgram)
		switch {
		case errors.Is(err, sessions.ErrNoContainerRuntime):
			return OllamaContainer{}, errors.New("Ollama can run in a container when this node has Podman or Docker; neither was found. Install one, or start the installed Ollama instead.")
		case errors.Is(err, sessions.ErrContainerRuntimeOff):
			return OllamaContainer{}, fmt.Errorf("containers are switched off on this node (%s=off); start the installed Ollama instead", sessions.ContainerRuntimeEnv)
		case err != nil:
			return OllamaContainer{}, fmt.Errorf("the container runtime setting is not usable: %w", err)
		}
		out.Runtime = cli.Path
		out.SELinux = cli.SELinux
	}
	if out.Image == "" {
		out.Image = strings.TrimSpace(getenv(OllamaContainerImageEnv))
	}
	if out.Image == "" {
		out.Image = DefaultOllamaContainerImage
		if out.GPU == "amd" {
			out.Image = ollamaROCmImage
		}
	}
	// An image that starts with '-' would be read by the CLI as an option.
	if strings.HasPrefix(out.Image, "-") || strings.ContainsAny(out.Image, " \t\r\n\x00") {
		return OllamaContainer{}, fmt.Errorf("the Ollama container image %q is not an image name", out.Image)
	}
	if !filepath.IsAbs(cfg.ModelsDir) || strings.ContainsAny(cfg.ModelsDir, ":,\n\r\x00") {
		return OllamaContainer{}, errors.New("the directory for Ollama's models cannot be mounted in a container")
	}
	return out, nil
}

// usableRuntime reports whether path can stand where a program is exec'd: an absolute
// path with no whitespace or NUL. A relative name would be a PATH lookup, and a value
// with a space is a setting, not a program.
func usableRuntime(path string) bool {
	return filepath.IsAbs(path) && !strings.ContainsAny(path, " \t\r\n\x00")
}

// trustedOllamaRuntime is a path the engine may exec as a container CLI: usable and
// Podman or Docker.
func trustedOllamaRuntime(path string) bool {
	return usableRuntime(path) && supportedOllamaRuntime(path)
}

// supportedOllamaRuntime is a container CLI by its exact file name: a program that only
// starts with "podman" is not one, so a record cannot point the engine's exec at it.
func supportedOllamaRuntime(path string) bool {
	switch filepath.Base(path) {
	case "podman", "podman-remote", "docker":
		return true
	}
	return false
}

// remoteDaemonSetting names the environment setting that points the container CLI at a
// daemon other than this node's own, or "" when none does. The container publishes the
// endpoint on its daemon's host: on another machine the loopback address here would be
// answered by whatever binds it locally, and the models mount would name a path there.
// A named connection or context cannot be told apart from a remote one here, so it is
// refused too.
func remoteDaemonSetting(getenv func(string) string) string {
	for _, name := range []string{"DOCKER_HOST", "CONTAINER_HOST"} {
		if v := strings.TrimSpace(getenv(name)); v != "" && !strings.HasPrefix(v, "unix://") {
			return name
		}
	}
	if strings.TrimSpace(getenv("CONTAINER_CONNECTION")) != "" {
		return "CONTAINER_CONNECTION"
	}
	if v := strings.TrimSpace(getenv("DOCKER_CONTEXT")); v != "" && v != "default" {
		return "DOCKER_CONTEXT"
	}
	return ""
}

// ollamaDaemonEnvNames are the CLI environment values that can select or relocate
// a container daemon. Only their hash is kept in the start record.
var ollamaDaemonEnvNames = []string{
	"CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINERS_CONF",
	"DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT",
	"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "HOME", "USER",
}

func ollamaDaemonFingerprint(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	var connection strings.Builder
	for _, name := range ollamaDaemonEnvNames {
		connection.WriteString(name)
		connection.WriteByte(0)
		connection.WriteString(getenv(name))
		connection.WriteByte(0)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(connection.String())))
}

func sameOllamaDaemonEnvironment(was ollamaRemembered, getenv func(string) string) bool {
	// Records written before the fingerprint existed keep their legacy behavior. Every
	// new container start records one, so a later connection change is not removal proof.
	return was.Daemon == "" || was.Daemon == ollamaDaemonFingerprint(getenv)
}

// ollamaContainerOwner distinguishes this engine's fixed-name container from an
// operator's or another engine's on the same daemon. HomeDir is stable across restarts.
func ollamaContainerOwner(homeDir string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(homeDir))))
}

// command is the runtime invocation: the hardened container the engine manages, with
// the models directory mounted and the service's endpoint published on the loopback
// address. The image ends the options and `serve` follows it untouched.
func (c OllamaContainer) command(cfg OllamaConfig) []string {
	args := []string{
		"run", "--rm",
		"--name", ollamaContainerName,
		"--label", ollamaContainerLabel + "=" + ollamaContainerOwner(cfg.HomeDir),
		"--read-only", "--tmpfs", "/tmp:rw,exec,nosuid,nodev",
		"--cap-drop=ALL", "--security-opt", "no-new-privileges",
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if strings.HasPrefix(daemonFamily(c.Runtime), "podman") && uid != 0 {
		// Rootless podman (local or remote) maps the engine user to root inside by default;
		// keep the real id so files in the mounted models directory stay the engine user's.
		args = append(args, "--userns=keep-id")
	} else {
		// A daemon runs as root, so the container runs as the engine user for the same
		// reason.
		args = append(args, "--user", fmt.Sprintf("%d:%d", uid, gid))
	}
	switch c.GPU {
	case "nvidia":
		args = append(args, "--gpus", "all")
	case "amd":
		args = append(args, "--device", "/dev/kfd", "--device", "/dev/dri", "--group-add", "video")
	}
	volume := cfg.ModelsDir + ":/models"
	if c.SELinux {
		volume += ":z"
	}
	args = append(args,
		"-p", cfg.Addr+":"+ollamaContainerPort,
		"-v", volume,
		// The root filesystem is read-only; /tmp is the one writable place, so Ollama's
		// key and temporary files live there.
		"-e", "HOME=/tmp",
		"-e", "OLLAMA_HOST=0.0.0.0:"+ollamaContainerPort,
		"-e", "OLLAMA_MODELS=/models",
		"-e", "OLLAMA_NO_CLOUD=1",
		c.Image, "serve",
	)
	return args
}

// ollamaContainerClientEnv is the CLI's own environment: the same allowlist a session's
// container CLI gets, so the engine's other variables do not reach it. It reads the
// effective environment, so an activation manifest that points at a socket is honored.
// The result is never nil: a nil cmd.Env would inherit every variable, secrets included.
func ollamaContainerClientEnv(getenv func(string) string) []string {
	if getenv == nil {
		getenv = os.Getenv
	}
	out := make([]string, 0, len(sessions.ContainerClientEnvNames()))
	for _, name := range sessions.ContainerClientEnvNames() {
		if value := getenv(name); value != "" {
			out = append(out, name+"="+value)
		}
	}
	return out
}

// ollamaContainerMissing matches only the runtime's answer about this container,
// never an arbitrary resource or context that "does not exist".
func ollamaContainerMissing(out []byte) bool {
	text := strings.ToLower(string(out))
	return strings.Contains(text, ollamaContainerName) &&
		(strings.Contains(text, "no such container") || strings.Contains(text, "no such object"))
}

// inspectOllamaContainer reads ownership as well as state. The fixed name alone is
// not proof that this engine may remove the container or register its endpoint.
func inspectOllamaContainer(runtime string, getenv func(string) string, owner string) (found, running, owned bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), ollamaStopGrace)
	defer cancel()
	cmd := exec.CommandContext(ctx, runtime, "inspect", "--type", "container", "--format", `{{.State.Running}} {{index .Config.Labels "ai.olivares.ollama"}}`, ollamaContainerName) // #nosec G204 -- the runtime path is operator-detected and the name is a constant
	cmd.Dir = "/"
	cmd.Env = ollamaContainerClientEnv(getenv)
	// The state is stdout alone: a runtime that exits 0 can still warn on stderr (Docker
	// does for an unreadable config file), and that is not a field of the answer.
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		out := append(stdout.Bytes(), stderr.Bytes()...)
		if ollamaContainerMissing(out) {
			return false, false, false, nil
		}
		return false, false, false, fmt.Errorf("container inspection failed: %s", cliFailureReason(out, err))
	}
	fields := strings.Fields(stdout.String())
	if len(fields) == 0 {
		return false, false, false, errors.New("the Ollama container inspection returned no state")
	}
	return true, fields[0] == "true", len(fields) > 1 && fields[1] == owner, nil
}

// removeOllamaContainer deletes only this engine's container, bounded. --rm already
// removed it when the process ended cleanly, so absence is success; any other failure
// is returned, because a container that survives keeps serving and holding the port.
func (m *Module) removeOllamaContainer(runtime string, getenv func(string) string) error {
	m.mu.Lock()
	owner := ollamaContainerOwner(m.ollama.cfg.HomeDir)
	m.mu.Unlock()
	found, _, owned, err := inspectOllamaContainer(runtime, getenv, owner)
	if err != nil {
		return fmt.Errorf("the Ollama container could not be removed: %s", err)
	}
	if !found {
		return nil
	}
	if !owned {
		return errors.New("the fixed-name Ollama container belongs to another engine or to the operator; refusing to remove it")
	}
	ctx, cancel := context.WithTimeout(context.Background(), ollamaStopGrace)
	defer cancel()
	cmd := exec.CommandContext(ctx, runtime, "rm", "-f", ollamaContainerName) // #nosec G204 -- the runtime path is operator-detected and the name is a constant
	cmd.Dir = "/"
	cmd.Env = ollamaContainerClientEnv(getenv)
	out, err := cmd.CombinedOutput()
	if err == nil || ollamaContainerMissing(out) {
		return nil
	}
	return fmt.Errorf("the Ollama container could not be removed: %s", cliFailureReason(out, err))
}

// cliFailureReason is the one line of the container CLI's own output that says why a call
// failed, or the exec error when it printed nothing.
func cliFailureReason(out []byte, err error) string {
	if reason := oneLine(firstLine(string(out)), 200); reason != "" {
		return reason
	}
	return err.Error()
}

// removeIdleOllamaContainer removes a container no live process of this engine owns:
// the daemon kept it when the engine was killed, and Stop must not answer "stopped"
// while it still serves on the endpoint. The start it remembers names the runtime that
// ran it, so a setting changed since then does not hide the container. With no usable
// record, a node that has the container flavour still removes by its fixed name: a
// start whose record could not be written leaves a container that must not be denied.
// unreadable says the record exists but cannot be read. When the record (or the
// absence of a readable one) says a container ran and no runtime can be driven to
// remove it, the returned warning says so: the stop stands, the silence would not.
// Removal is proof only through the runtime and connection that ran the container.
// A known different daemon is never asked to remove it; a legacy or unreadable record
// whose origin is unknown keeps the warning even when a removal attempt succeeds.
// With nothing to show a container ran, no failure of that removal refuses the stop: the
// shipped default is the installed program, and a CLI that cannot reach its daemon or a
// runtime setting that is wrong is no reason to keep a person from stopping a service
// that never ran in a container.
func (m *Module) removeIdleOllamaContainer(was ollamaRemembered, remembered, unreadable bool) (string, error) {
	m.mu.Lock()
	cfg, ranContainer := m.ollama.cfg, m.ollama.runtime == ollamaRuntimeContainer
	m.mu.Unlock()
	// mayHaveContainer: this engine's record says a container ran (or cannot say
	// anything, which is not the same as saying it did not), or its own row still holds
	// the container it could not remove: the retry that row promises must not turn into
	// a clean stop because the record of that start could not be written.
	mayHaveContainer := unreadable || (remembered && was.Container) || ranContainer
	warn := func() (string, error) {
		if mayHaveContainer {
			return "Ollama is stopped, but the engine could not confirm that the container it started is gone, so it may still be serving on " + m.ollamaEndpoint() + "; remove it by hand if that address answers.", nil
		}
		return "", nil
	}
	// refuse is the answer to a removal that failed: a refusal where a container may
	// survive, the warning's answer where nothing says one ran.
	refuse := func(err error) (string, error) {
		if mayHaveContainer {
			return "", err
		}
		// The stop stands and the row stays quiet for a person who never asked for a
		// container, but the failure is not dropped: the log says what the CLI answered.
		slog.Warn("ollama stop: the container CLI could not remove the fixed-name container and nothing says one ran", "error", boundedText(err.Error()))
		return warn()
	}
	// unproven: a successful rm proves the container gone only when it ran through the
	// CLI the record names (or this engine's own start, whose runtime is today's). A
	// container name is unique per daemon, not per host: a fallback CLI, or any CLI when
	// the record cannot be read, answers about its own daemon and knows nothing of the
	// one that may still serve.
	unproven := func(runtime string) bool {
		if unreadable {
			return true
		}
		return was.Container && (was.Runtime == "" || !sameRuntimeDaemon(runtime, was.Runtime) || !sameOllamaDaemonEnvironment(was, cfg.Getenv))
	}
	// A known, different daemon is never asked to remove the original container.
	// Legacy records with no runtime may still try: a daemon failure then refuses the
	// stop, while success remains an unproven cleanup with the warning and record kept.
	skipRemoval := func(runtime string) bool {
		return !unreadable && was.Container && was.Runtime != "" &&
			(!sameRuntimeDaemon(runtime, was.Runtime) || !sameOllamaDaemonEnvironment(was, cfg.Getenv))
	}
	runtime := ""
	if remembered && was.Container {
		// A remembered runtime whose program is gone cannot remove anything; fall
		// through to the configured or discovered one instead of failing every Stop on
		// a binary that no longer exists.
		if trustedOllamaRuntime(was.Runtime) {
			if _, err := os.Stat(was.Runtime); err == nil {
				runtime = was.Runtime
			}
		}
	}
	if runtime == "" {
		// A readable record that names the installed program proves no container of this
		// engine exists, and a stopped daemon is then irrelevant. The exception is this
		// engine's own row still holding a container it could not remove: an earlier start
		// can leave that record behind while a later container start could not rewrite it.
		// Then the removal runs.
		if remembered && !was.Container && !ranContainer {
			return "", nil
		}
		// Removal needs the CLI only; a changed image or models directory must not block
		// cleaning up the container that is still serving.
		if cfg.Container == nil {
			return warn()
		}
		if configured := strings.TrimSpace(cfg.Container.Runtime); configured != "" {
			if !trustedOllamaRuntime(configured) {
				return warn()
			}
			if skipRemoval(configured) {
				return warn()
			}
			if err := m.removeOllamaContainer(configured, cfg.Getenv); err != nil {
				return refuse(err)
			}
			if unproven(configured) {
				return warn()
			}
			return "", nil
		}
		cli, err := sessions.DetectContainerCLI(cfg.Getenv, m.findProgram)
		if errors.Is(err, sessions.ErrNoContainerRuntime) || errors.Is(err, sessions.ErrContainerRuntimeOff) {
			return warn()
		}
		if err != nil {
			return refuse(fmt.Errorf("the Ollama container could not be removed: %w", err))
		}
		runtime = cli.Path
	}
	if skipRemoval(runtime) {
		return warn()
	}
	if err := m.removeOllamaContainer(runtime, cfg.Getenv); err != nil {
		return refuse(err)
	}
	if unproven(runtime) {
		return warn()
	}
	return "", nil
}

// sameRuntimeDaemon reports whether two CLI paths can drive the same container
// daemon. Local Podman and podman-remote are different until their connection
// fingerprints also match; a container name is unique per daemon, not per host.
func sameRuntimeDaemon(a, b string) bool {
	return daemonFamily(a) == daemonFamily(b)
}

func daemonFamily(path string) string {
	base := filepath.Base(path)
	if strings.HasPrefix(base, "podman-remote") {
		return "podman-remote"
	}
	if strings.HasPrefix(base, "podman") {
		return "podman"
	}
	return base
}

// verifyOllamaContainer confirms that this engine's fixed-name container is running.
// The endpoint alone is not proof: during a pull anything on the loopback address can
// answer first, and another engine's container must never be registered for this tenant.
func verifyOllamaContainer(runtime string, getenv func(string) string, owner string) error {
	found, running, owned, err := inspectOllamaContainer(runtime, getenv, owner)
	if err != nil {
		return fmt.Errorf("its container could not be confirmed as running: %s", err)
	}
	if !found {
		return errors.New("its container could not be confirmed as running: the fixed-name container was not found")
	}
	if !running {
		return errors.New("its container is not running")
	}
	if !owned {
		return errors.New("the fixed-name container belongs to another engine or to the operator")
	}
	return nil
}

// ollamaRuntimeWord names a flavour inside a sentence.
func ollamaRuntimeWord(runtime string) string {
	if runtime == ollamaRuntimeContainer {
		return "a container"
	}
	return "the installed program"
}

// DetectOllamaGPU reports the GPU class a container may be given, from the device nodes
// the host exposes: "nvidia", "amd", or "" when neither is found.
func DetectOllamaGPU() string {
	if _, err := os.Stat("/dev/nvidia0"); err == nil {
		return "nvidia"
	}
	if _, err := os.Stat("/dev/kfd"); err == nil {
		return "amd"
	}
	return ""
}
