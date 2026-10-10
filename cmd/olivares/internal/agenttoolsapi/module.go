// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package agenttoolsapi exposes the host installer through the authenticated module seam.
package agenttoolsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const maxRetained = 1000

// Plan exposes the original approval document and a small console summary.
type Plan struct {
	Digest       string `json:"digest"`
	Driver       string `json:"driver"`
	Version      string `json:"version"`
	Verification string `json:"verification"`
	Executable   string `json:"executable"`
	Document     any    `json:"document"`
	v1           *toolinstall.Plan
	v2           *toolinstall.PlanV2
	requested    string
	tenant       model.TenantID
	expires      time.Time
}

// Job is an immutable snapshot when returned to the caller. Receipt is the engine's receipt.
type Job struct {
	ID         model.ID  `json:"id"`
	Digest     string    `json:"plan_digest"`
	Driver     string    `json:"driver"`
	Version    string    `json:"version"`
	State      string    `json:"state"`
	Progress   string    `json:"progress"`
	Error      string    `json:"error,omitempty"`
	AuditError string    `json:"audit_error,omitempty"`
	Receipt    any       `json:"receipt,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type savedJob struct {
	Job
	Tenant model.TenantID `json:"tenant_id"`
}

type Module struct {
	engine   *toolinstall.Engine
	root     string
	readOnly bool
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	plans    map[string]*Plan
	jobs     map[model.ID]*savedJob
	files    *os.Root
	active   bool

	// resolveProgram and signIns serve the tool sign-in (signin.go).
	resolveProgram func(driver string) string
	// loginHome says where a tenant's own login of a tool lives (SetLoginHome);
	// nil refuses every sign-in and status read.
	loginHome   LoginHomeFunc
	signIns     map[model.ID]*SignIn
	statusReads map[statusSelection]*statusRead
	// providerEntries caches each tool instance's snapshot (providers.go).
	providerEntries map[string]*providerEntry
	// signedIn is told the tenant of each sign-in the tool confirmed (OnSignedIn).
	signedIn func(model.TenantID)
	// ollama is the local model service this engine runs (ollama.go).
	ollama ollamaService
}

var _ api.Module = (*Module)(nil)

// New owns its job journal and cancellation. The caller closes it at shutdown.
func New(ctx context.Context, engine *toolinstall.Engine, root string, readOnly bool) (*Module, error) {
	if engine == nil || !filepath.IsAbs(root) {
		return nil, errors.New("agent tools require an engine and absolute tools root")
	}
	ctx, cancel := context.WithCancel(ctx)
	m := &Module{engine: engine, root: root, readOnly: readOnly, ctx: ctx, cancel: cancel, plans: map[string]*Plan{}, jobs: map[model.ID]*savedJob{}}
	dir := root + ".jobs"
	if !readOnly {
		if err := os.MkdirAll(dir, 0700); err != nil {
			cancel()
			return nil, err
		}
		// Retain a newly created journal directory before any job effect.
		parent, err := os.Open(filepath.Dir(dir))
		if err != nil {
			cancel()
			return nil, err
		}
		syncErr := parent.Sync()
		closeErr := parent.Close()
		if syncErr != nil {
			cancel()
			return nil, syncErr
		}
		if closeErr != nil {
			cancel()
			return nil, closeErr
		}
	}
	files, err := os.OpenRoot(dir)
	if readOnly && errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		cancel()
		return nil, err
	}
	m.files = files
	if err = m.recover(); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}
func (m *Module) Close() {
	m.mu.Lock()
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
	if m.files != nil {
		m.files.Close()
	}
}
func (*Module) APINamespace() string           { return "agenttools" }
func (*Module) Permissions() []auth.Permission { return nil }

// Global credentials have no tenant-scoped witness. The system door uses the
// same deployment authority as console secrets without relaxing that evidence.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	door, ok := reg.(api.SystemRouteRegistrar)
	if !ok {
		panic("agenttools requires system-scope module registration")
	}
	door.HandleSystem("GET", "/inventory", m.handleInventory)
	door.HandleSystem("GET", "/detect", m.handleDetect)
	door.HandleSystem("POST", "/plans", m.handlePlan)
	door.HandleSystem("POST", "/installs", m.handleInstall)
	door.HandleSystem("GET", "/jobs/{id}", m.handleJob)
	door.HandleSystem("GET", "/providers", m.handleProviders)
	door.HandleSystem("GET", "/sign-in", m.handleSignInStatus)
	door.HandleSystem("POST", "/sign-in", m.handleSignInStart)
	door.HandleSystem("GET", "/sign-in/{id}", m.handleSignInGet)
	door.HandleSystem("POST", "/sign-in/{id}/code", m.handleSignInCode)
	door.HandleSystem("DELETE", "/sign-in/{id}", m.handleSignInCancel)
	door.HandleSystem("GET", "/ollama", m.handleOllamaStatus)
	door.HandleSystem("POST", "/ollama/start", m.handleOllamaStart)
	door.HandleSystem("POST", "/ollama/stop", m.handleOllamaStop)
	door.HandleSystem("POST", "/ollama/pulls", m.handleOllamaPull)
	door.HandleSystem("GET", "/ollama/pulls/{id}", m.handleOllamaPullGet)
}

// handleInventory lists managed host tools, release integrity, verification
// policies and the five most recently updated installation jobs. System admin only.
func (m *Module) handleInventory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, m.inventory)(w, r, mc)
}

// handleDetect discovers host executables without reading provider credential homes.
// An explicit detected path requires the administrative step-up before bounded version execution;
// observations and selected probes require a retained audit event.
func (m *Module) handleDetect(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, m.detect)(w, r, mc)
}

// handlePlan resolves an official release and returns the digest-bound version,
// verification policy and destination for system administrator review before install.
func (m *Module) handlePlan(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, m.plan)(w, r, mc)
}

// handleInstall starts one audited host installation from an approved plan behind
// the administrative step-up. The request UUID deduplicates retries; progress and
// results persist in a job.
func (m *Module) handleInstall(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(true, m.install)(w, r, mc)
}

// handleJob returns bounded installation progress, state, errors and the verified
// receipt to a system administrator. Interrupted jobs are reported, never replayed.
func (m *Module) handleJob(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, m.job)(w, r, mc)
}

// guard admits a system administrator session; stepUp additionally demands the
// deployment's administrative step-up policy (auth.StepUpSatisfied).
func (m *Module) guard(stepUp bool, h api.ModuleHandler) api.ModuleHandler {
	return func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		w.Header().Set("Cache-Control", "no-store")
		if !mc.Principal.Superadmin || mc.Principal.Kind != auth.KindUser {
			fail(w, 403, "forbidden", "A system administrator session is required.")
			return
		}
		if stepUp && !auth.StepUpSatisfied(r.Context(), mc.Principal) {
			fail(w, 403, "step_up_required", "Complete the administrative step-up this deployment requires before installing tools.")
			return
		}
		if _, confined := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); confined {
			fail(w, 403, "forbidden", "Host tools require deployment-wide authority.")
			return
		}
		h(w, r, mc)
	}
}
func audit(ctx context.Context, mc api.ModuleContext, action string, id model.ID, meta map[string]any) error {
	if mc.Data == nil {
		return errors.New("audit store unavailable")
	}
	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(), Action: "agenttools." + action, TargetKind: "agenttools.job", TargetID: id, Meta: meta})
		if err == nil && ev.Seq == 0 {
			return errors.New("audit event was not retained")
		}
		return err
	})
}

type toolPresence struct {
	Program string `json:"program"`
	Present bool   `json:"present"`
}

// sessionToolPresence lists unmanaged tools and the published Gemini presence field.
// It uses the launch program resolver and executable lookup; it never runs a tool.
func (m *Module) sessionToolPresence() map[string]toolPresence {
	presence := map[string]toolPresence{}
	for _, facts := range driverfacts.All() {
		// Gemini presence predates its installer and remains a published API field.
		if !facts.Session || facts.Installer != "" && facts.Key != "gemini-cli" {
			continue
		}
		_, err := exec.LookPath(m.program(facts.Key))
		presence[facts.Key] = toolPresence{Program: facts.Program,
			Present: err == nil}
	}
	return presence
}

func (m *Module) inventory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if err := audit(r.Context(), mc, "inventory", "", nil); err != nil {
		unavailable(w)
		return
	}
	inv, err := m.engine.List(r.Context(), m.root)
	if err != nil {
		installerError(w, err)
		return
	}
	m.mu.Lock()
	recent := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		recent = append(recent, Job{ID: job.ID, Driver: job.Driver, Version: job.Version, State: job.State, UpdatedAt: job.UpdatedAt})
	}
	m.mu.Unlock()
	sort.Slice(recent, func(i, j int) bool { return recent[i].UpdatedAt.After(recent[j].UpdatedAt) })
	if len(recent) > 5 {
		recent = recent[:5]
	}
	write(w, 200, map[string]any{"drivers": m.engine.DriverKeys(), "presence": m.sessionToolPresence(), "verification_levels": m.engine.VerificationLevels(), "platform": toolinstall.HostPlatform(), "inventory": inv, "read_only": m.readOnly, "jobs": recent})
}
func (m *Module) detect(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	driver := r.URL.Query().Get("driver")
	selected := r.URL.Query().Get("probe_path")
	if selected != "" {
		if !auth.StepUpSatisfied(r.Context(), mc.Principal) {
			fail(w, 403, "step_up_required", "Complete the administrative step-up this deployment requires before probing this host executable.")
			return
		}
		if m.readOnly {
			fail(w, 409, "read_only", "This control plane cannot execute an unregistered host tool.")
			return
		}
		if !filepath.IsAbs(selected) || filepath.Clean(selected) != selected || len(selected) > 4096 {
			fail(w, 400, "bad_request", "Select an exact detected executable path.")
			return
		}
	}
	if _, err := m.engine.Capability(driver); err != nil {
		installerError(w, err)
		return
	}
	if err := audit(r.Context(), mc, "detect", "", map[string]any{"driver": driver, "probe_path": selected}); err != nil {
		unavailable(w)
		return
	}
	home, _ := os.UserHomeDir()
	// A selected path executes alone. Discovery must not spend its deadline
	// probing unrelated registered releases, or run those releases twice.
	opts := toolinstall.DetectOptions{Driver: driver, Root: m.root, PathEnv: os.Getenv("PATH"), Home: home, NoAuthObservation: true, Probe: selected == ""}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	candidates, err := m.engine.Detect(ctx, opts)
	if err != nil && candidates == nil {
		installerError(w, err)
		return
	}
	if selected != "" {
		found := false
		for _, candidate := range candidates {
			if candidate.Path == selected || candidate.Resolved == selected {
				found = true
				break
			}
		}
		if !found {
			fail(w, 400, "bad_request", "Select a path returned by this driver's host detection.")
			return
		}
		opts.ProbePaths = []string{selected}
		candidates, err = m.engine.Detect(ctx, opts)
		if err != nil && candidates == nil {
			installerError(w, err)
			return
		}
	}
	if candidates == nil {
		candidates = []toolinstall.Candidate{}
	}
	result := map[string]any{"candidates": candidates}
	if err != nil {
		result["probe_error"] = "One or more selected probes failed. Inspect each candidate's reason."
	}
	write(w, 200, result)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := api.DecodeRequestBody(w, r, v, api.RequestBodySpec{MaxBytes: 4096}); err != nil {
		fail(w, 400, "bad_request", api.RequestBodyErrorMessage(err, "Provide a valid request with only the supported fields."))
		return false
	}
	return true
}
func (m *Module) plan(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in struct {
		Driver  string `json:"driver"`
		Version string `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version == "" || len(in.Version) > 128 {
		fail(w, 400, "bad_request", "Choose a release version or vendor channel.")
		return
	}
	capab, err := m.engine.Capability(in.Driver)
	if err != nil {
		installerError(w, err)
		return
	}
	if err = audit(r.Context(), mc, "plan", "", map[string]any{"driver": in.Driver, "version": in.Version}); err != nil {
		unavailable(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	p := &Plan{Driver: in.Driver, requested: in.Version, tenant: mc.Tenant, expires: time.Now().Add(10 * time.Minute)}
	if capab == toolinstall.ProviderCapabilityV1 {
		p.v1, err = m.engine.Plan(ctx, toolinstall.Request{Driver: in.Driver, Version: in.Version, Platform: toolinstall.HostPlatform(), DestRoot: m.root})
		if err == nil {
			p.Digest = p.v1.Digest
			p.Version = p.v1.Version
			p.Executable = p.v1.Destination.Executable
			p.Verification = "openpgp"
			p.Document = p.v1
		}
	} else {
		platform, _, pe := toolinstall.PlatformV2For(in.Driver, toolinstall.HostPlatform())
		if pe != nil {
			installerError(w, pe)
			return
		}
		p.v2, err = m.engine.PlanV2(ctx, toolinstall.RequestV2{Driver: in.Driver, Version: in.Version, Platform: platform, DestRoot: m.root})
		if err == nil {
			p.Digest = p.v2.Digest
			p.Version = p.v2.Selection.Version
			p.Executable = p.v2.Selection.Destination.Executable
			p.Verification = p.v2.Selection.Verification.Kind
			p.Document = p.v2
		}
	}
	if err != nil {
		installerError(w, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, old := range m.plans {
		if time.Now().After(old.expires) {
			delete(m.plans, key)
		}
	}
	if len(m.plans) >= maxRetained {
		fail(w, 429, "rate_limited", "Too many pending plans. Wait for a plan to expire.")
		return
	}
	// A digest is not a tenant identifier: the same selection may be approved in two tenants.
	m.plans[mc.Tenant.String()+":"+p.Digest] = p
	write(w, 200, p)
}
func (m *Module) install(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.readOnly {
		fail(w, 503, "read_only", "Tool installation is disabled on this read-only control plane.")
		return
	}
	var in struct {
		Digest string   `json:"plan_digest"`
		ID     model.ID `json:"request_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, err := model.ParseID(string(in.ID))
	if err != nil || id != in.ID || id.IsZero() {
		fail(w, 400, "bad_request", "A canonical nonzero request ID is required.")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.jobs[id]; old != nil {
		if old.Tenant != mc.Tenant {
			fail(w, 404, "not_found", "Job not found.")
			return
		}
		if old.Digest != in.Digest {
			fail(w, 409, "conflict", "This request ID already identifies a different plan.")
			return
		}
		if err = audit(r.Context(), mc, "install.replay", id, nil); err != nil {
			unavailable(w)
			return
		}
		write(w, 202, old.Job)
		return
	}
	p := m.plans[mc.Tenant.String()+":"+in.Digest]
	if p == nil || time.Now().After(p.expires) {
		fail(w, 409, "plan_expired", "Preview the installation again before installing.")
		return
	}
	if m.active {
		fail(w, 409, "install_busy", "Another host tool installation is running.")
		return
	}
	if len(m.jobs) >= maxRetained {
		fail(w, 409, "journal_full", "The tool job journal is full. Archive it before another installation.")
		return
	}
	j := &savedJob{Tenant: mc.Tenant, Job: Job{ID: id, Digest: p.Digest, Driver: p.Driver, Version: p.Version, State: "running", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}}
	if err = audit(r.Context(), mc, "install.requested", id, map[string]any{"driver": p.Driver, "version": p.Version, "plan_digest": p.Digest}); err != nil {
		unavailable(w)
		return
	}
	if err = m.save(j); err != nil {
		unavailable(w)
		return
	}
	m.jobs[id] = j
	m.active = true
	m.wg.Add(1)
	go m.run(j, p, mc)
	w.Header().Set("Location", "/v1/m/agenttools/jobs/"+id.String())
	write(w, 202, j.Job)
}
func (m *Module) job(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	if j == nil || j.Tenant != mc.Tenant {
		fail(w, 404, "not_found", "Job not found.")
		return
	}
	if err := audit(r.Context(), mc, "job.read", id, nil); err != nil {
		unavailable(w)
		return
	}
	write(w, 200, j.Job)
}
func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func unavailable(w http.ResponseWriter) {
	fail(w, 503, "unavailable", "Tool operation storage or audit is unavailable. No new installation was started.")
}
func installerError(w http.ResponseWriter, err error) {
	code := string(toolinstall.KindOf(err))
	status := 422
	switch toolinstall.KindOf(err) {
	case toolinstall.KindInvalidRequest, toolinstall.KindUnsupportedProvider, toolinstall.KindUnsupportedPlatform:
		status = 400
	case toolinstall.KindPlanChanged, toolinstall.KindLocked, toolinstall.KindConflict:
		status = 409
	case toolinstall.KindTransport:
		status = 502
	}
	if code == "" {
		code = "tool_install_failed"
	}
	fail(w, status, code, fmt.Sprint(err))
}
