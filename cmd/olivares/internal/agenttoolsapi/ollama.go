// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// OLLAMA ON THIS SERVER (HU-R17, refresh 06). The console installed Ollama, and the
// person then had to start `ollama serve` and pull a model by hand. The Ollama row now
// starts the INSTALLED Ollama as a managed child of the engine: loopback only, models
// under the engine's data directory, low priority, confined when the composition wires
// it, in its own process group, and stopped with the engine. A person may instead ask
// for the Ollama image: the same lifecycle, models directory and provider
// registration run through Podman or Docker; the installed program stays the default,
// and the container is hardened and given the host GPU when the runtime has one. The
// low-priority renice applies to the child; the container's model work is not reniced.
// It pulls a model by name and reports Ollama's own progress, and once the service
// answers the composition registers its endpoint as a provider. No GPU choice is made
// here: Ollama decides.

const (
	ollamaDefaultAddr  = "127.0.0.1:11434"
	ollamaReadyTimeout = 60 * time.Second
	ollamaStopGrace    = 10 * time.Second
	ollamaPullLimit    = 6 * time.Hour
	maxOllamaPulls     = 20
	maxOllamaTail      = 4 << 10
	ollamaLowPriority  = 10
)

// ollamaModelName is what Ollama accepts as a model reference (name[:tag], an
// optional namespace or registry host), bounded.
var ollamaModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// Service states.
const (
	ollamaStopped  = "stopped"
	ollamaStarting = "starting"
	ollamaRunning  = "running"
	ollamaFailed   = "failed"
)

// OllamaConfig is the composition's part of the service.
type OllamaConfig struct {
	// Addr is the loopback host:port the service listens on; empty is 127.0.0.1:11434,
	// the endpoint the Providers form already suggests.
	Addr string
	// ModelsDir (OLLAMA_MODELS) and HomeDir (the child's HOME, where Ollama keeps its
	// key) are absolute directories the engine owns.
	ModelsDir string
	HomeDir   string
	// Register runs once the service answers, with its endpoint, the person who
	// started it and the tenant they started it from (Root on first-hour check: only that
	// tenant gets the record), and again after each model download succeeds, so the
	// record's model list follows what the service holds (first-hour check). A failure at start
	// is reported on the row; the service keeps running.
	Register func(ctx context.Context, actor auth.Principal, tenant model.TenantID, endpoint string) error
	// StateFile remembers that a person started the service, and for which tenant, so
	// an engine restart starts it again (RestartOllama). It is the engine's own file,
	// outside ModelsDir and HomeDir, which the child writes. Empty: nothing is kept.
	StateFile string
	// Audit records a start the engine makes by itself (RestartOllama) as a system
	// action; the composition writes it into the audit log. Without it, or when the
	// write fails, the engine starts nothing by itself: no start goes unrecorded.
	Audit func(ctx context.Context, draft model.AuditDraft) error
	// Container is the container flavour: the Ollama image run by Podman or Docker with
	// the models directory mounted, when a person asks for it. Nil means this node has
	// no such flavour and a container start is refused.
	Container *OllamaContainer
	// Getenv reads the engine's effective environment: process values take precedence
	// over activation-manifest defaults, as every other settable value does. Nil reads
	// the process environment.
	Getenv func(string) string
}

// ollamaRemembered is what StateFile holds while the service should run: for which
// tenant, and who started it when (the restart's audit row names them), and whether it
// ran as a container, with the runtime and image that ran it so the restart repeats the
// person's start instead of whatever the settings say later.
type ollamaRemembered struct {
	TenantID  string `json:"tenant_id,omitempty"`
	StartedBy string `json:"started_by,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	Container bool   `json:"container,omitempty"`
	Runtime   string `json:"runtime,omitempty"`
	Image     string `json:"image,omitempty"`
	// Daemon is the hash of the connection environment that selected the runtime's
	// daemon. CleanupOnly remembers an unproven removal without asking for a restart.
	Daemon      string `json:"daemon,omitempty"`
	CleanupOnly bool   `json:"cleanup_only,omitempty"`
	// SELinux is the relabel mode the first start used: a restart mounts the same way
	// even when the host check cannot run again.
	SELinux bool `json:"selinux,omitempty"`
}

// OllamaStatus is the row's view of the service.
type OllamaStatus struct {
	Installed bool     `json:"installed"`
	State     string   `json:"state"`
	Message   string   `json:"message,omitempty"`
	Endpoint  string   `json:"endpoint,omitempty"`
	Models    []string `json:"models"`
	// Runtime says which flavour serves the row: "process" for the installed program,
	// "container" for the image. Empty while nothing runs.
	Runtime string `json:"runtime,omitempty"`
}

type ollamaService struct {
	cfg     OllamaConfig
	state   string
	message string
	// unproven says the message carries a note about state a clean stop cannot
	// resolve: a container from a record that could not be read may still be
	// serving on another connection. A stop that proves its own cleanup keeps
	// that note; a fresh start replaces it, like the message itself.
	unproven bool
	runtime  string
	// lifecycle serializes Start and Stop: two starts must not run the same fixed
	// container name, and a start must not remove the container a Stop is ending.
	lifecycle sync.Mutex
	stopping  bool
	stop      context.CancelFunc
	done      chan struct{}
	// stopErr is why the service could not be ended (a container that survived); Stop
	// answers it instead of claiming a stop that did not happen.
	stopErr error
	pulls   map[model.ID]*OllamaPull
	// owner and ownerTenant started the running service; a download registers for them.
	owner       auth.Principal
	ownerTenant model.TenantID
}

// UseOllama enables the service with the composition's directories and
// registration. Without it the routes answer that this node cannot run Ollama.
func (m *Module) UseOllama(cfg OllamaConfig) {
	if cfg.Addr == "" {
		cfg.Addr = ollamaDefaultAddr
	}
	if cfg.Getenv == nil {
		cfg.Getenv = os.Getenv
	}
	m.mu.Lock()
	m.ollama.cfg = cfg
	m.mu.Unlock()
}

func (m *Module) ollamaEndpoint() string { return "http://" + m.ollama.cfg.Addr }

// appendNote adds a sentence to the row's message without erasing what it already says:
// a warning from an earlier step (a start that could not be remembered) must survive a
// later one.
func appendNote(message, note string) string {
	if message == "" {
		return note
	}
	return message + " " + note
}

// handleOllamaStatus reports whether Ollama is installed, whether this engine runs it,
// its endpoint and the models it holds. System admin only.
func (m *Module) handleOllamaStatus(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		write(w, 200, m.ollamaStatus(r.Context()))
	})(w, r, mc)
}

func (m *Module) ollamaStatus(ctx context.Context) OllamaStatus {
	m.mu.Lock()
	out := OllamaStatus{State: m.ollama.state, Message: m.ollama.message, Models: []string{}, Runtime: m.ollama.runtime}
	running := m.ollama.state == ollamaRunning
	m.mu.Unlock()
	if out.State == "" {
		out.State = ollamaStopped
	}
	// A container this engine is starting or serves is the service even when no
	// program is installed; a failed start is not, so the row does not claim a node
	// can run Ollama after the runtime refused it.
	serving := out.State == ollamaRunning || out.State == ollamaStarting
	out.Installed = m.program("ollama") != "" || (serving && out.Runtime == ollamaRuntimeContainer)
	if running {
		out.Endpoint = m.ollamaEndpoint()
		if models, err := m.ollamaModels(ctx); err == nil {
			out.Models = models
		}
	}
	return out
}

// handleOllamaStart starts Ollama as a managed service of the engine, from the installed
// program or, when the request asks for it, from Ollama's image in a Podman or Docker
// container, and returns at once; the row reads "running" once the service answers.
func (m *Module) handleOllamaStart(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(true, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		if m.readOnly {
			fail(w, 409, "read_only", "This control plane cannot start services.")
			return
		}
		m.ollama.lifecycle.Lock()
		defer m.ollama.lifecycle.Unlock()
		m.mu.Lock()
		cfg, state, currentRuntime := m.ollama.cfg, m.ollama.state, m.ollama.runtime
		m.mu.Unlock()
		if cfg.ModelsDir == "" || cfg.HomeDir == "" {
			fail(w, 503, "unavailable", "This node has no place for Ollama's models.")
			return
		}
		// Which tenant gets the endpoint as a provider (Root on first-hour check): the one the
		// administrator works in, named explicitly, because a system route ignores the
		// tenant selection. None named: nothing is registered, and the row says where
		// it answers so a tenant adds it in Providers.
		var in struct {
			TenantID string `json:"tenant_id"`
			// Container asks for the Ollama image instead of the installed program.
			Container bool `json:"container"`
		}
		if err := api.DecodeRequestBody(w, r, &in, api.RequestBodySpec{MaxBytes: 4096, Optional: true}); err != nil {
			fail(w, 400, "bad_request", api.RequestBodyErrorMessage(err, "Provide a valid request with only the supported fields."))
			return
		}
		if state == ollamaStarting || state == ollamaRunning {
			requested := ollamaFlavour(in.Container)
			// The other flavour is a different service: saying "running" to this request
			// would report a state the person did not ask for.
			if currentRuntime != "" && requested != currentRuntime {
				fail(w, 409, "already_running", "Ollama is already running as "+ollamaRuntimeWord(currentRuntime)+"; stop it before starting it as "+ollamaRuntimeWord(requested)+".")
				return
			}
			write(w, 202, m.ollamaStatus(r.Context()))
			return
		}
		var tenant model.TenantID
		if in.TenantID != "" {
			var err error
			if tenant, err = model.ParseTenantID(in.TenantID); err != nil {
				fail(w, 400, "bad_request", "tenant_id must be a tenant id.")
				return
			}
		}
		// The installed program is the shipped default; the container flavour needs no
		// program on this node, only a runtime the engine can drive.
		program := ""
		var c OllamaContainer
		if in.Container {
			// Resolve before the audit: a node without a usable runtime is refused without
			// a start row, and the row names the image that will run.
			resolved, err := m.resolveOllamaContainer(cfg, nil)
			if err != nil {
				fail(w, 409, "not_started", err.Error())
				return
			}
			c = resolved
		} else {
			program = m.program("ollama")
			if program == "" {
				fail(w, 409, "tool_not_installed", "Install Ollama first, then start it.")
				return
			}
		}
		meta := map[string]any{"endpoint": m.ollamaEndpoint(), "register_in": in.TenantID, "runtime": ollamaFlavour(in.Container)}
		if in.Container {
			meta["image"] = c.Image
		}
		if err := audit(r.Context(), mc, "ollama.start", "", meta); err != nil {
			unavailable(w)
			return
		}
		// What the record said before this start: a failed start must leave it as it was,
		// because an earlier record may be the only thing that says a container serves.
		previous, hadPrevious, previousErr := m.rememberedOllama()
		if hadPrevious && previous.Container {
			// A start may retry or replace a container only through the daemon that owns
			// it. Switching flavours or daemons first needs a proven Stop, or the old
			// container would be forgotten while it still serves.
			if !in.Container {
				fail(w, 409, "not_started", "Stop the previous Ollama container before starting the installed program.")
				return
			}
			if !sameRuntimeDaemon(c.Runtime, previous.Runtime) || !sameOllamaDaemonEnvironment(previous, cfg.Getenv) {
				fail(w, 409, "not_started", "The previous Ollama container's connection changed; restore it and stop that container before starting another.")
				return
			}
		}
		// The record is written before anything runs: the engine does not start a service
		// it cannot name. Without it the service does not come back with the engine, and a
		// container it started would outlive an engine that cannot identify it.
		if err := m.rememberOllama(tenant, mc.Principal.Actor(), in.Container, c.Runtime, c.Image, c.SELinux); err != nil {
			fail(w, 409, "not_started", "Ollama was not started: "+err.Error())
			return
		}
		if err := m.startOllama(program, mc.Principal, tenant, in.Container, c); err != nil {
			switch {
			case hadPrevious:
				if rerr := m.writeRememberedOllama(previous); rerr != nil {
					err = fmt.Errorf("%w; the record of this start could not be restored either: %s", err, boundedText(rerr.Error()))
				}
			case previousErr != nil:
				// The earlier record could not be read; the new one stays, and the error
				// says that whether a container serves is unknown.
				err = fmt.Errorf("%w; the start record that was there before could not be read, so whether a container serves is unknown", err)
			default:
				if ferr := m.forgetOllama(); ferr != nil {
					err = fmt.Errorf("%w; the record of this start could not be removed either: %s", err, boundedText(ferr.Error()))
				}
			}
			fail(w, 409, "not_started", err.Error())
			return
		}
		if previousErr != nil {
			// The record that was there before could not be read; this start's record has
			// replaced it, and whether a container from that earlier start still serves is
			// unknown. The failure path above says so when this start fails; the row says
			// it when the start succeeds, instead of reading clean. unproven keeps the
			// note through the paths below that end this service cleanly: none of them
			// can touch a container whose daemon the unreadable record did not name.
			m.mu.Lock()
			m.ollama.message = appendNote(m.ollama.message, "the record of an earlier start could not be read; a container from it may still be serving on another connection")
			m.ollama.unproven = true
			m.mu.Unlock()
		}
		write(w, 202, m.ollamaStatus(r.Context()))
	})(w, r, mc)
}

// handleOllamaStop stops the Ollama this engine started. Models stay on disk.
func (m *Module) handleOllamaStop(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		if m.readOnly {
			fail(w, 409, "read_only", "This control plane cannot stop services.")
			return
		}
		if err := audit(r.Context(), mc, "ollama.stop", "", nil); err != nil {
			unavailable(w)
			return
		}
		m.ollama.lifecycle.Lock()
		defer m.ollama.lifecycle.Unlock()
		// An unreadable record does not stop the stop: a proven removal forgets it;
		// an unproven one keeps the original record and its warning for the retry.
		was, remembered, readErr := m.rememberedOllama()
		stopped, stopErr := m.stopOllama()
		if stopErr != nil {
			fail(w, 409, "not_stopped", stopErr.Error())
			return
		}
		warning := ""
		if !stopped {
			// No live child: a container left by a killed engine is still serving on the
			// endpoint. Stop must remove it, not report a stop that did not happen; when
			// no runtime can be driven to try, the row says so instead of falling silent.
			var err error
			warning, err = m.removeIdleOllamaContainer(was, remembered, readErr != nil)
			if err != nil {
				// The row says what the caller was told: a console that missed this 409
				// must not read a clean stop while the container may still serve.
				m.mu.Lock()
				m.ollama.state, m.ollama.runtime = ollamaFailed, ollamaRuntimeContainer
				m.ollama.message = appendNote(m.ollama.message, "Ollama did not stop: "+err.Error())
				m.mu.Unlock()
				fail(w, 409, "not_stopped", err.Error())
				return
			}
		}
		recordAction := "start could not be forgotten"
		var recordErr error
		if warning != "" {
			// The person's intent is stopped, but removal is not proven. Keep a
			// cleanup-only record so the next Stop retries and an engine restart does
			// not revive the service or drop the warning.
			recordAction = "cleanup state could not be kept"
			if readErr == nil {
				was.Container, was.CleanupOnly = true, true
				recordErr = m.writeRememberedOllama(was)
			}
		} else {
			recordErr = m.forgetOllama()
		}
		if recordErr != nil {
			m.mu.Lock()
			message := "Ollama stopped, but its " + recordAction + ": " + recordErr.Error()
			if warning != "" {
				message += " " + warning
			}
			m.ollama.message = appendNote(m.ollama.message, message)
			m.mu.Unlock()
			fail(w, 409, "not_forgotten", recordErr.Error())
			return
		}
		m.mu.Lock()
		m.ollama.state, m.ollama.runtime, m.ollama.stopErr = ollamaStopped, "", nil
		if m.ollama.unproven {
			// The unreadable-record note names a container this stop cannot prove
			// gone; the warning appends to it instead of replacing it.
			m.ollama.message = appendNote(m.ollama.message, warning)
		} else {
			m.ollama.message = warning
		}
		m.mu.Unlock()
		write(w, 200, m.ollamaStatus(r.Context()))
	})(w, r, mc)
}

// startOllama starts the installed `ollama serve`, or the Ollama image as a managed
// container, and watches it until it answers or ends. c is resolved by the caller, so
// a restart uses the runtime, image and mount mode the record names without a second
// detection pass.
func (m *Module) startOllama(program string, actor auth.Principal, tenant model.TenantID, container bool, c OllamaContainer) error {
	m.mu.Lock()
	cfg := m.ollama.cfg
	previous := m.ollama.done
	m.mu.Unlock()
	tmp := filepath.Join(cfg.HomeDir, "tmp")
	for _, dir := range []string{cfg.ModelsDir, cfg.HomeDir, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return errors.New("the directory for Ollama's models could not be created on this node")
		}
	}
	// A start after a failed one must not race the previous watcher's cleanup: that
	// watcher removes the fixed container name, and could delete the container this
	// start is about to run. It closes done once its cleanup is finished.
	if previous != nil {
		select {
		case <-previous:
		case <-time.After(3*ollamaStopGrace + 5*time.Second): // WaitDelay plus the bounded inspect and rm
			return errors.New("the previous Ollama has not finished stopping; try again in a moment")
		}
	}
	// A container the previous engine left behind (the daemon owns it, so --rm could not
	// run when the CLI was killed) holds both the fixed name and the port, and must be
	// removed before asking whether the address is free. A removal that really failed is
	// reported, never raced.
	if container {
		if rerr := m.removeOllamaContainer(c.Runtime, cfg.Getenv); rerr != nil {
			return errors.New("a previous Ollama container could not be removed: " + rerr.Error())
		}
	}
	// One service per address: another Ollama (or anything else) there is said, not raced.
	// The probe goes through the admission point, which admits only a loopback address:
	// Ollama answers without authentication.
	l, err := netbind.Listen(m.ctx, "tcp", cfg.Addr, netbind.Policy{Component: "Ollama", Purpose: "local model service"})
	if errors.Is(err, netbind.ErrPublicPlaintextBind) {
		return err
	}
	if err != nil {
		return fmt.Errorf("something else already listens on %s; stop it first", cfg.Addr)
	}
	_ = l.Close()
	ctx, cancel := context.WithCancel(m.ctx)
	var cmd *exec.Cmd
	var runtime string
	if container {
		runtime = c.Runtime
		cmd = exec.CommandContext(ctx, c.Runtime, c.command(cfg)...) // #nosec G204 -- the runtime path is operator-detected and the arguments are built here without a shell
		cmd.Dir = "/"
		// The CLI's own environment only: its storage, runtime directory and socket.
		cmd.Env = ollamaContainerClientEnv(cfg.Getenv)
		// The CLI forwards a TERM to the container; the default KILL would leave a
		// container the daemon owns running after the engine stopped.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = ollamaStopGrace
	} else {
		// The release directory holds the program and its runtime libraries (lib/ollama).
		release := filepath.Dir(filepath.Dir(program))
		cmd, err = m.build()(ctx, []string{cfg.ModelsDir, cfg.HomeDir}, []string{release}, program, "serve")
		if err != nil {
			cancel()
			return errors.New("Ollama could not be prepared on this node")
		}
		// OLLAMA_NO_CLOUD is Ollama's own switch for its cloud features (cloud models, web
		// search, the ollama.com connection): real use saw the product-started Ollama reach
		// ollama.com on start. The model a person asks to download still comes from the
		// registry.
		cmd.Env = []string{
			"HOME=" + cfg.HomeDir, "TMPDIR=" + tmp, "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8",
			"OLLAMA_HOST=" + cfg.Addr, "OLLAMA_MODELS=" + cfg.ModelsDir, "OLLAMA_NO_CLOUD=1",
		}
		cmd.Dir = cfg.HomeDir
		// Its own process group: stopping it (or the engine) ends the server and every
		// runner it spawned, not only the first process.
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Setpgid = true
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
		cmd.WaitDelay = ollamaStopGrace
	}
	tail := &tailBuffer{max: maxOllamaTail}
	cmd.Stdout, cmd.Stderr = tail, tail
	// Register the watchers before the process exists: Close cancels and waits, and a
	// start racing shutdown must be waited for, or a daemon-owned container outlives
	// the engine that started it.
	m.mu.Lock()
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		cancel()
		return errors.New("the engine is stopping; Ollama was not started")
	}
	m.wg.Add(2)
	m.mu.Unlock()
	if err := cmd.Start(); err != nil {
		m.wg.Done()
		m.wg.Done()
		cancel()
		if container {
			return fmt.Errorf("the Ollama container could not be started on this node: %w", err)
		}
		return errors.New("Ollama could not be started on this node")
	}
	pid := cmd.Process.Pid
	if !container {
		// Low priority: model work must not starve the control plane on the same host.
		// Ollama keeps running when the priority cannot be set; the protection is
		// absent, so say so instead of passing silently.
		if perr := syscall.Setpriority(syscall.PRIO_PGRP, pid, ollamaLowPriority); perr != nil {
			slog.Warn("ollama: the started program did not get the low priority", "pid", pid, "error", boundedText(perr.Error()))
		}
	}
	flavour := ollamaFlavour(container)
	done := make(chan struct{})
	m.mu.Lock()
	m.ollama.state, m.ollama.message, m.ollama.stopping, m.ollama.unproven = ollamaStarting, "", false, false
	m.ollama.runtime = flavour
	m.ollama.stop, m.ollama.done, m.ollama.stopErr = cancel, done, nil
	m.mu.Unlock()

	go func() { // the service ends: by Stop, by the engine stopping, or on its own
		defer m.wg.Done()
		defer close(done)
		err := cmd.Wait()
		var removeErr error
		if container {
			// --rm removes a container that exited; a Stop that ended the CLI before the
			// daemon did would leave it, so the name is removed here too, bounded. A
			// removal that really failed is reported: otherwise the row says stopped
			// while the container still serves.
			removeErr = m.removeOllamaContainer(runtime, cfg.Getenv)
		} else {
			_ = syscall.Kill(-pid, syscall.SIGKILL) // nothing of the group outlives it
		}
		cancel()
		m.mu.Lock()
		if m.ollama.done != done {
			m.mu.Unlock()
			return
		}
		// This watcher is the one that owns the generation, and its cleanup is finished:
		// clear the handles so a later Stop does not wait on it again, and retries the
		// removal when this one failed.
		m.ollama.stop, m.ollama.done = nil, nil
		if m.ollama.stopping || m.ctx.Err() != nil {
			if removeErr != nil {
				m.ollama.state, m.ollama.runtime, m.ollama.stopErr = ollamaFailed, flavour, removeErr
				// A note the row already carried (an unreadable earlier record, a pull) names
				// state that the refused stop leaves in place; the removal failure appends.
				m.ollama.message = appendNote(m.ollama.message, "Ollama stopped, but its container could not be removed: "+removeErr.Error())
			} else {
				// stopErr is cleared too: a start that failed its verify kept one, and a
				// stop that ended cleanly must not answer with that stale failure. A note
				// about state this stop cannot prove gone (an unreadable earlier record)
				// stays on the row.
				m.ollama.state, m.ollama.runtime, m.ollama.stopErr = ollamaStopped, "", nil
				if !m.ollama.unproven {
					m.ollama.message = ""
				}
			}
			m.mu.Unlock()
			return
		}
		// A start that already recorded why it failed keeps that reason: the process
		// ending is the consequence of it, not a new, vaguer one.
		if m.ollama.state != ollamaFailed || m.ollama.message == "" {
			m.ollama.state, m.ollama.runtime = ollamaFailed, ""
			reason := "Ollama stopped on its own."
			if last := tail.lastLine(); last != "" {
				reason = "Ollama stopped on its own: " + last
			} else if err != nil {
				reason = "Ollama stopped on its own: " + err.Error()
			}
			// The note the row carried names state that outlives the process (an
			// unreadable earlier record); the self-stop reason appends to it.
			m.ollama.message = appendNote(m.ollama.message, reason)
		}
		if removeErr != nil {
			// The container may still serve: keep the flavour and the reason, so the row
			// does not look like a plain exit and Stop still retries the removal.
			m.ollama.runtime, m.ollama.stopErr = flavour, removeErr
			m.ollama.message += " Its container could not be removed: " + removeErr.Error()
		}
		m.mu.Unlock()
	}()
	go func() { // the service answers: running, and its endpoint is registered
		defer m.wg.Done()
		readyTimeout, within := ollamaReadyTimeout, "a minute"
		if container {
			readyTimeout, within = ollamaContainerReadyTimeout, "ten minutes"
		}
		if !m.waitOllamaReady(ctx, readyTimeout) {
			m.mu.Lock()
			if m.ollama.done == done && m.ollama.state == ollamaStarting && !m.ollama.stopping && m.ctx.Err() == nil {
				m.ollama.state, m.ollama.runtime = ollamaFailed, ""
				// A note the row already carried (an unreadable earlier record) names
				// state that outlives this failure; the reason appends.
				m.ollama.message = appendNote(m.ollama.message, "Ollama did not answer within "+within+".")
			}
			m.mu.Unlock()
			cancel()
			return
		}
		if container {
			if verr := verifyOllamaContainer(runtime, cfg.Getenv, ollamaContainerOwner(cfg.HomeDir)); verr != nil {
				// Something answered on the loopback address, but not provably this
				// engine's container: registering it would send the tenant's prompts to
				// whatever holds the port. End the start and say why.
				m.mu.Lock()
				if m.ollama.done == done && m.ollama.state == ollamaStarting && !m.ollama.stopping {
					m.ollama.state, m.ollama.runtime, m.ollama.stopErr = ollamaFailed, flavour, verr
					// The unreadable-record note names a container this failure does not
					// resolve; the reason appends to it.
					m.ollama.message = appendNote(m.ollama.message, "Ollama answered, but the engine could not confirm that its own container is serving: "+verr.Error())
				}
				m.mu.Unlock()
				cancel()
				return
			}
		}
		m.mu.Lock()
		if m.ollama.done != done || m.ollama.state != ollamaStarting {
			m.mu.Unlock()
			return
		}
		m.ollama.owner, m.ollama.ownerTenant = actor, tenant
		register := m.ollama.cfg.Register
		m.mu.Unlock()
		// Running is published once the endpoint is registered, so a client that
		// rereads Providers when it sees running finds it there.
		message := ""
		switch {
		case register == nil:
		case tenant.IsZero():
			message = "Running. To use it, add " + m.ollamaEndpoint() + " in Providers."
		default:
			rctx, rcancel := context.WithTimeout(m.ctx, 30*time.Second)
			err := register(rctx, actor, tenant, m.ollamaEndpoint())
			rcancel()
			if err != nil {
				message = "Running. Its endpoint could not be added to Providers; add " + m.ollamaEndpoint() + " there."
			}
		}
		m.mu.Lock()
		if m.ollama.done == done && m.ollama.state == ollamaStarting {
			m.ollama.state = ollamaRunning
			if message != "" {
				// Append, never replace: the row may already hold the warning that this
				// start could not be remembered (so it will not start again with the
				// engine), and running must not erase it.
				m.ollama.message = appendNote(m.ollama.message, message)
			}
		}
		m.mu.Unlock()
	}()
	return nil
}

// stopOllama ends the service this engine started and waits for it, bounded. It
// reports whether a live service existed, so Stop knows when it must remove a
// container no process of this engine owns.
func (m *Module) stopOllama() (bool, error) {
	m.mu.Lock()
	stop, done := m.ollama.stop, m.ollama.done
	if stop == nil || done == nil {
		m.mu.Unlock()
		return false, nil
	}
	m.ollama.stopping = true
	m.mu.Unlock()
	stop()
	select {
	case <-done:
	case <-time.After(3*ollamaStopGrace + 5*time.Second): // WaitDelay plus the bounded inspect and rm
		// The row must say what the caller is told, or a console watcher sees plain
		// "running" after a stop that did not finish. It is appended: a note that was
		// already there (a start that could not be remembered, a pull) must survive.
		m.mu.Lock()
		m.ollama.message = appendNote(m.ollama.message, "Ollama did not stop within the time the engine waits; it may still be running.")
		m.mu.Unlock()
		return true, errors.New("Ollama did not stop within the time the engine waits; it may still be running")
	}
	m.mu.Lock()
	err := m.ollama.stopErr
	m.mu.Unlock()
	return true, err
}

func (m *Module) waitOllamaReady(ctx context.Context, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, "GET", m.ollamaEndpoint()+"/api/version", nil)
		if resp, err := client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
	return false
}

// ollamaModels lists the models the running service holds (GET /api/tags).
func (m *Module) ollamaModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", m.ollamaEndpoint()+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tags); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tags.Models))
	for _, t := range tags.Models {
		out = append(out, t.Name)
	}
	return out, nil
}

// tailBuffer keeps the last bytes a child printed, to say why it ended.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) lastLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(ansiEscape.ReplaceAllString(string(t.buf), "")), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) > 200 {
		last = last[:200]
	}
	return last
}
