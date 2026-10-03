// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// OLLAMA ON THIS SERVER (HU-R17, refresh 06). The console installed Ollama, and the
// person then had to start `ollama serve` and pull a model by hand. The Ollama row now
// starts the INSTALLED Ollama as a managed child of the engine: loopback only, models
// under the engine's data directory, low priority, confined when the composition wires
// it, in its own process group, and stopped with the engine. It pulls a model by name
// and reports Ollama's own progress, and once the service answers the composition
// registers its endpoint as a provider. No GPU choice is made here: Ollama decides.

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
	// Command builds the child with the paths it may write (rw) and read (ro); the
	// composition confines it there. Nil runs the program unconfined.
	Command func(ctx context.Context, rw, ro []string, program string, args ...string) (*exec.Cmd, error)
	// Register runs once the service answers, with its endpoint, the person who
	// started it and the tenant they started it from (Root on FH 033: only that
	// tenant gets the record), and again after each model download succeeds, so the
	// record's model list follows what the service holds (FH 087). A failure at start
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
}

// ollamaRemembered is what StateFile holds while the service should run: for which
// tenant, and who started it when (the restart's audit row names them).
type ollamaRemembered struct {
	TenantID  string `json:"tenant_id,omitempty"`
	StartedBy string `json:"started_by,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

// OllamaStatus is the row's view of the service.
type OllamaStatus struct {
	Installed bool     `json:"installed"`
	State     string   `json:"state"`
	Message   string   `json:"message,omitempty"`
	Endpoint  string   `json:"endpoint,omitempty"`
	Models    []string `json:"models"`
}

// OllamaPull is one model download, with Ollama's own progress.
type OllamaPull struct {
	ID        model.ID `json:"id"`
	Model     string   `json:"model"`
	State     string   `json:"state"`
	Status    string   `json:"status,omitempty"`
	Completed int64    `json:"completed"`
	Total     int64    `json:"total"`
	Error     string   `json:"error,omitempty"`
	started   time.Time
}

type ollamaService struct {
	cfg      OllamaConfig
	state    string
	message  string
	stopping bool
	stop     context.CancelFunc
	done     chan struct{}
	pulls    map[model.ID]*OllamaPull
	// owner and ownerTenant started the running service; a download registers for them.
	owner       auth.Principal
	ownerTenant model.TenantID
}

// UseOllama enables the service with the composition's directories, confinement and
// registration. Without it the routes answer that this node cannot run Ollama.
func (m *Module) UseOllama(cfg OllamaConfig) {
	if cfg.Addr == "" {
		cfg.Addr = ollamaDefaultAddr
	}
	m.mu.Lock()
	m.ollama.cfg = cfg
	m.mu.Unlock()
}

func (m *Module) ollamaEndpoint() string { return "http://" + m.ollama.cfg.Addr }

// handleOllamaStatus reports whether Ollama is installed, whether this engine runs it,
// its endpoint and the models it holds. System admin only.
func (m *Module) handleOllamaStatus(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		write(w, 200, m.ollamaStatus(r.Context()))
	})(w, r, mc)
}

func (m *Module) ollamaStatus(ctx context.Context) OllamaStatus {
	m.mu.Lock()
	out := OllamaStatus{State: m.ollama.state, Message: m.ollama.message, Models: []string{}}
	running := m.ollama.state == ollamaRunning
	m.mu.Unlock()
	if out.State == "" {
		out.State = ollamaStopped
	}
	out.Installed = m.program("ollama") != ""
	if running {
		out.Endpoint = m.ollamaEndpoint()
		if models, err := m.ollamaModels(ctx); err == nil {
			out.Models = models
		}
	}
	return out
}

// handleOllamaStart starts the installed Ollama as a managed child of the engine and
// returns at once; the row reads "running" once the service answers.
func (m *Module) handleOllamaStart(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(true, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		if m.readOnly {
			fail(w, 409, "read_only", "This control plane cannot start services.")
			return
		}
		m.mu.Lock()
		cfg, state := m.ollama.cfg, m.ollama.state
		m.mu.Unlock()
		if cfg.ModelsDir == "" || cfg.HomeDir == "" {
			fail(w, 503, "unavailable", "This node has no place for Ollama's models.")
			return
		}
		if state == ollamaStarting || state == ollamaRunning {
			write(w, 202, m.ollamaStatus(r.Context()))
			return
		}
		program := m.program("ollama")
		if program == "" {
			fail(w, 409, "tool_not_installed", "Install Ollama first, then start it.")
			return
		}
		// Which tenant gets the endpoint as a provider (Root on FH 033): the one the
		// administrator works in, named explicitly, because a system route ignores the
		// tenant selection. None named: nothing is registered, and the row says where
		// it answers so a tenant adds it in Providers.
		var in struct {
			TenantID string `json:"tenant_id"`
		}
		if err := api.DecodeRequestBody(w, r, &in, api.RequestBodySpec{MaxBytes: 4096, Optional: true}); err != nil {
			fail(w, 400, "bad_request", "Provide a valid request with only the supported fields.")
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
		if err := audit(r.Context(), mc, "ollama.start", "", map[string]any{"endpoint": m.ollamaEndpoint(), "register_in": in.TenantID}); err != nil {
			unavailable(w)
			return
		}
		if err := m.startOllama(program, mc.Principal, tenant); err != nil {
			fail(w, 409, "not_started", err.Error())
			return
		}
		m.rememberOllama(tenant, mc.Principal.Actor())
		write(w, 202, m.ollamaStatus(r.Context()))
	})(w, r, mc)
}

// handleOllamaStop stops the Ollama this engine started. Models stay on disk.
func (m *Module) handleOllamaStop(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		if err := audit(r.Context(), mc, "ollama.stop", "", nil); err != nil {
			unavailable(w)
			return
		}
		m.forgetOllama()
		m.stopOllama()
		write(w, 200, m.ollamaStatus(r.Context()))
	})(w, r, mc)
}

// RestartOllama starts the service again at engine start when a person started it and
// did not stop it (HU2-14): after a restart it stayed "Not running" until someone
// pressed Start, and the tools it serves failed meanwhile. The engine starts it as
// itself, for the tenant the person named; a failure is shown on the row.
func (m *Module) RestartOllama() {
	m.mu.Lock()
	cfg := m.ollama.cfg
	m.mu.Unlock()
	if m.readOnly || cfg.StateFile == "" || cfg.ModelsDir == "" || cfg.HomeDir == "" || cfg.Audit == nil {
		return
	}
	raw, err := os.ReadFile(cfg.StateFile)
	if err != nil {
		return
	}
	var was ollamaRemembered
	if json.Unmarshal(raw, &was) != nil {
		return
	}
	var tenant model.TenantID
	if was.TenantID != "" {
		if tenant, err = model.ParseTenantID(was.TenantID); err != nil {
			return
		}
	}
	program := m.program("ollama")
	if program == "" {
		return
	}
	actor, err := auth.NewSystemOperator("engine start", "restart the Ollama a person started before the engine stopped")
	if err != nil {
		return
	}
	// The person's Start was audited (handleOllamaStart); this start is the engine's,
	// once per boot, and is recorded as such before it happens.
	reason := "restarted with the engine; started by " + was.StartedBy + " at " + was.StartedAt
	if err := cfg.Audit(m.ctx, model.AuditDraft{
		Actor: actor.Actor(), ActorKind: actor.ActorKind(), Action: "agenttools.ollama.restart", TargetKind: "agenttools.job",
		Meta: map[string]any{"reason": reason, "register_in": was.TenantID, "endpoint": m.ollamaEndpoint()},
	}); err != nil {
		m.mu.Lock()
		m.ollama.state = ollamaFailed
		m.ollama.message = "Ollama was running before the engine restarted; it was not started again because its audit record could not be written."
		m.mu.Unlock()
		return
	}
	if err := m.startOllama(program, actor, tenant); err != nil {
		m.mu.Lock()
		m.ollama.state = ollamaFailed
		m.ollama.message = "Ollama was running before the engine restarted and could not start again: " + err.Error()
		m.mu.Unlock()
	}
}

// rememberOllama keeps the person's start in StateFile; forgetOllama drops it on Stop.
func (m *Module) rememberOllama(tenant model.TenantID, startedBy string) {
	m.mu.Lock()
	path := m.ollama.cfg.StateFile
	m.mu.Unlock()
	if path == "" {
		return
	}
	was := ollamaRemembered{StartedBy: startedBy, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	if !tenant.IsZero() {
		was.TenantID = tenant.String()
	}
	raw, err := json.Marshal(was)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func (m *Module) forgetOllama() {
	m.mu.Lock()
	path := m.ollama.cfg.StateFile
	m.mu.Unlock()
	if path != "" {
		_ = os.Remove(path)
	}
}

// handleOllamaPull downloads a model into the running Ollama and returns the
// download to follow; Ollama's own progress is read from it.
func (m *Module) handleOllamaPull(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(true, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		var in struct {
			Model string `json:"model"`
		}
		if !decode(w, r, &in) {
			return
		}
		name := strings.TrimSpace(in.Model)
		if !ollamaModelName.MatchString(name) {
			fail(w, 400, "bad_request", "Name a model the way Ollama does, for example qwen2.5:0.5b.")
			return
		}
		m.mu.Lock()
		running := m.ollama.state == ollamaRunning
		m.mu.Unlock()
		if !running {
			fail(w, 409, "not_running", "Start Ollama first, then download a model.")
			return
		}
		p := &OllamaPull{ID: model.NewID(), Model: name, State: "running", started: time.Now()}
		if err := audit(r.Context(), mc, "ollama.pull", p.ID, map[string]any{"model": name}); err != nil {
			unavailable(w)
			return
		}
		m.mu.Lock()
		if m.ollama.pulls == nil {
			m.ollama.pulls = map[model.ID]*OllamaPull{}
		}
		m.ollama.pulls[p.ID] = p
		m.forgetOldPullsLocked()
		view := *p
		m.mu.Unlock()
		m.wg.Add(1)
		go m.runOllamaPull(p.ID, name)
		write(w, 202, view)
	})(w, r, mc)
}

// handleOllamaPullGet reads one model download: its state and Ollama's progress.
func (m *Module) handleOllamaPullGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		m.mu.Lock()
		p, ok := m.ollama.pulls[model.ID(chi.URLParam(r, "id"))]
		var view OllamaPull
		if ok {
			view = *p
		}
		m.mu.Unlock()
		if !ok {
			fail(w, 404, "not_found", "This download is no longer known. Start it again.")
			return
		}
		write(w, 200, view)
	})(w, r, mc)
}

// startOllama spawns `ollama serve` and watches it until it answers or ends.
func (m *Module) startOllama(program string, actor auth.Principal, tenant model.TenantID) error {
	m.mu.Lock()
	cfg := m.ollama.cfg
	m.mu.Unlock()
	tmp := filepath.Join(cfg.HomeDir, "tmp")
	for _, dir := range []string{cfg.ModelsDir, cfg.HomeDir, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return errors.New("the directory for Ollama's models could not be created on this node")
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
	build := cfg.Command
	if build == nil {
		build = func(ctx context.Context, _, _ []string, program string, args ...string) (*exec.Cmd, error) {
			return toolCommand(ctx, program, args...), nil
		}
	}
	// The release directory holds the program and its runtime libraries (lib/ollama).
	release := filepath.Dir(filepath.Dir(program))
	cmd, err := build(ctx, []string{cfg.ModelsDir, cfg.HomeDir}, []string{release}, program, "serve")
	if err != nil {
		cancel()
		return errors.New("Ollama could not be prepared on this node")
	}
	// OLLAMA_NO_CLOUD is Ollama's own switch for its cloud features (cloud models, web
	// search, the ollama.com connection): HU2 saw the product-started Ollama reach
	// ollama.com on start. The model a person asks to download still comes from the
	// registry.
	cmd.Env = []string{
		"HOME=" + cfg.HomeDir, "TMPDIR=" + tmp, "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8",
		"OLLAMA_HOST=" + cfg.Addr, "OLLAMA_MODELS=" + cfg.ModelsDir, "OLLAMA_NO_CLOUD=1",
	}
	cmd.Dir = cfg.HomeDir
	tail := &tailBuffer{max: maxOllamaTail}
	cmd.Stdout, cmd.Stderr = tail, tail
	// Its own process group: stopping it (or the engine) ends the server and every
	// runner it spawned, not only the first process.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = ollamaStopGrace
	if err := cmd.Start(); err != nil {
		cancel()
		return errors.New("Ollama could not be started on this node")
	}
	pid := cmd.Process.Pid
	// Low priority: model work must not starve the control plane on the same host.
	_ = syscall.Setpriority(syscall.PRIO_PGRP, pid, ollamaLowPriority)
	done := make(chan struct{})
	m.mu.Lock()
	m.ollama.state, m.ollama.message, m.ollama.stopping = ollamaStarting, "", false
	m.ollama.stop, m.ollama.done = cancel, done
	m.mu.Unlock()

	m.wg.Add(2)
	go func() { // the service ends: by Stop, by the engine stopping, or on its own
		defer m.wg.Done()
		defer close(done)
		err := cmd.Wait()
		_ = syscall.Kill(-pid, syscall.SIGKILL) // nothing of the group outlives it
		cancel()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.ollama.done != done {
			return
		}
		if m.ollama.stopping || m.ctx.Err() != nil {
			m.ollama.state, m.ollama.message = ollamaStopped, ""
			return
		}
		m.ollama.state = ollamaFailed
		m.ollama.message = "Ollama stopped on its own."
		if last := tail.lastLine(); last != "" {
			m.ollama.message = "Ollama stopped on its own: " + last
		} else if err != nil {
			m.ollama.message = "Ollama stopped on its own: " + err.Error()
		}
	}()
	go func() { // the service answers: running, and its endpoint is registered
		defer m.wg.Done()
		if !m.waitOllamaReady(ctx) {
			m.mu.Lock()
			if m.ollama.done == done && m.ollama.state == ollamaStarting && !m.ollama.stopping && m.ctx.Err() == nil {
				m.ollama.state, m.ollama.message = ollamaFailed, "Ollama did not answer within a minute."
			}
			m.mu.Unlock()
			cancel()
			return
		}
		m.mu.Lock()
		if m.ollama.done != done || m.ollama.state != ollamaStarting {
			m.mu.Unlock()
			return
		}
		m.ollama.state = ollamaRunning
		m.ollama.owner, m.ollama.ownerTenant = actor, tenant
		register := m.ollama.cfg.Register
		m.mu.Unlock()
		if register == nil {
			return
		}
		if tenant.IsZero() {
			m.mu.Lock()
			if m.ollama.done == done {
				m.ollama.message = "Running. To use it, add " + m.ollamaEndpoint() + " in Providers."
			}
			m.mu.Unlock()
			return
		}
		rctx, rcancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer rcancel()
		if err := register(rctx, actor, tenant, m.ollamaEndpoint()); err != nil {
			m.mu.Lock()
			if m.ollama.done == done {
				m.ollama.message = "Running. Its endpoint could not be added to Providers; add " +
					m.ollamaEndpoint() + " there."
			}
			m.mu.Unlock()
		}
	}()
	return nil
}

// stopOllama ends the service this engine started and waits for it, bounded.
func (m *Module) stopOllama() {
	m.mu.Lock()
	stop, done := m.ollama.stop, m.ollama.done
	if stop == nil || done == nil {
		m.mu.Unlock()
		return
	}
	m.ollama.stopping = true
	m.mu.Unlock()
	stop()
	select {
	case <-done:
	case <-time.After(ollamaStopGrace + 5*time.Second):
	}
}

func (m *Module) waitOllamaReady(ctx context.Context) bool {
	deadline := time.Now().Add(ollamaReadyTimeout)
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

// runOllamaPull asks the service for the model and follows its streamed progress.
func (m *Module) runOllamaPull(id model.ID, name string) {
	defer m.wg.Done()
	finish := func(state, msg string) {
		m.mu.Lock()
		if p, ok := m.ollama.pulls[id]; ok {
			p.State, p.Error = state, msg
		}
		m.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(m.ctx, ollamaPullLimit)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"model": name, "stream": true})
	req, _ := http.NewRequestWithContext(ctx, "POST", m.ollamaEndpoint()+"/api/pull", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		finish("failed", "Ollama could not be reached for the download.")
		return
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 64<<10)
	for scanner.Scan() {
		var line struct {
			Status    string `json:"status"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if line.Error != "" {
			finish("failed", "Ollama: "+line.Error)
			return
		}
		m.mu.Lock()
		if p, ok := m.ollama.pulls[id]; ok {
			p.Status = line.Status
			if line.Total > 0 {
				p.Total, p.Completed = line.Total, line.Completed
			}
		}
		m.mu.Unlock()
		if line.Status == "success" {
			finish("succeeded", "")
			m.registerOllamaAgain()
			return
		}
	}
	if ctx.Err() != nil && m.ctx.Err() == nil {
		finish("failed", "The download did not finish within six hours.")
		return
	}
	finish("failed", "The download ended before Ollama reported success.")
}

// registerOllamaAgain runs Register for whoever started the running service, after a
// download: the tenant's record then lists the new model. Nothing is registered for a
// service started outside a tenant; a failure leaves the download's success as it is.
func (m *Module) registerOllamaAgain() {
	m.mu.Lock()
	register, actor, tenant := m.ollama.cfg.Register, m.ollama.owner, m.ollama.ownerTenant
	running := m.ollama.state == ollamaRunning
	m.mu.Unlock()
	if register == nil || tenant.IsZero() || !running {
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	_ = register(ctx, actor, tenant, m.ollamaEndpoint())
}

func (m *Module) forgetOldPullsLocked() {
	for len(m.ollama.pulls) > maxOllamaPulls {
		var oldest model.ID
		var at time.Time
		for id, p := range m.ollama.pulls {
			if p.State != "running" && (oldest == "" || p.started.Before(at)) {
				oldest, at = id, p.started
			}
		}
		if oldest == "" {
			return
		}
		delete(m.ollama.pulls, oldest)
	}
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
