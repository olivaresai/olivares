// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
)

// A provider instance is a tool plus its own configuration directory:
// the default login of the user running the engine (~/.claude, CODEX_HOME), and the
// login a tenant made through Olivares (tool-logins). The snapshot is what the tool
// itself reports through its read-only commands and protocol calls, never a
// credential file and never a token in argv. Hooks and MCP are off for every probe.

// providerRefresh is how often one instance may be probed again: a request inside it
// is answered from the last snapshot.
const providerRefresh = time.Minute

// Snapshot states.
const (
	stateReady        = "ready"
	stateNotSignedIn  = "not_signed_in"
	stateNotInstalled = "not_installed"
	stateUnknown      = "unknown" // the tool reports no sign-in state (Grok Build)
	stateError        = "error"
)

// ProviderLimit is one usage window the tool reports for a plan login.
type ProviderLimit struct {
	Label    string     `json:"label"`
	Percent  float64    `json:"percent"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Severity string     `json:"severity,omitempty"`
}

// ProviderModel is one model the tool offers this login.
type ProviderModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// ProviderSnapshot is everything known about one tool instance. Plan and method are
// the tool's raw values; Email is masked. A stale snapshot is the last good one, with
// Error naming the command that failed and what it said.
type ProviderSnapshot struct {
	Instance    string          `json:"instance"`
	Driver      string          `json:"driver"`
	Default     bool            `json:"default"`
	ConfigDir   string          `json:"config_dir"`
	State       string          `json:"state"`
	Installed   bool            `json:"installed"`
	Version     string          `json:"version,omitempty"`
	AuthMethod  string          `json:"auth_method,omitempty"`
	Plan        string          `json:"plan,omitempty"`
	Email       string          `json:"email,omitempty"`
	Limits      []ProviderLimit `json:"limits"`
	Models      []ProviderModel `json:"models"`
	Notes       []string        `json:"notes,omitempty"`
	NextCommand string          `json:"next_command,omitempty"`
	CheckedAt   time.Time       `json:"checked_at"`
	Source      string          `json:"source"`
	Stale       bool            `json:"stale,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// providerInstance says where and how one instance is probed. err is why its
// environment could not be built.
type providerInstance struct {
	id, driver, configDir string
	own                   bool
	env                   []string
	err                   error
}

// providerEntry is one instance's last answer, protected by Module.mu.
// done lets concurrent readers share a probe without blocking background reads.
type providerEntry struct {
	done      chan struct{}
	snap      ProviderSnapshot
	have      bool
	attempted time.Time
}

func providerKey(driver, configDir string) string { return driver + "\x00" + configDir }

// forgetProviders drops a tool's snapshots: its login just changed.
func (m *Module) forgetProviders(driver string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.providerEntries {
		if strings.HasPrefix(key, driver+"\x00") {
			delete(m.providerEntries, key)
		}
	}
}

// ownLogin is the environment of the default instance: the engine user's own home
// and the tool's configuration variable only if the engine itself has it set. It is
// the only place the product reads a login nobody gave it, and only to report it.
func ownLogin(driver string) (env []string, configDir string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", err
	}
	facts, _ := driverfacts.Lookup(driver)
	env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "TERM=dumb", "NO_COLOR=1"}
	names := []string{facts.ConfigHomeEnv}
	if driver == "opencode" {
		names = append(names, "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME")
	}
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	env = append(env, hostNetworkEnv()...)
	configDir = os.Getenv(facts.ConfigHomeEnv)
	if driver == "gemini-cli" && configDir != "" {
		configDir = filepath.Join(configDir, ".gemini")
	}
	if configDir == "" {
		configDir = filepath.Join(home, facts.ConfigDir)
	}
	return env, configDir, nil
}

// maskEmail keeps the first letter and the domain.
func maskEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" {
		return "***"
	}
	first, _ := utf8.DecodeRuneInString(local)
	return string(first) + "***@" + domain
}

// handleProviders reports one snapshot per tool instance on this node. tenant_id
// adds the logins that organization made through Olivares.
func (m *Module) handleProviders(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		var tenant model.TenantID
		if raw := r.URL.Query().Get("tenant_id"); raw != "" {
			t, ok := tenantOf(raw)
			if !ok {
				fail(w, 400, "bad_request", "tenant_id must name an organization.")
				return
			}
			tenant = t
		}
		if err := audit(r.Context(), mc, "providers", "", nil); err != nil {
			unavailable(w)
			return
		}
		background := r.URL.Query().Get("background")
		if background != "" && background != "true" && background != "false" {
			fail(w, 400, "bad_request", "background must be true or false.")
			return
		}
		snaps, refreshing, err := m.providerSnapshotsRead(r.Context(), tenant, background == "true")
		if err != nil {
			loginHomeError(w, err)
			return
		}
		body := map[string]any{"providers": snaps}
		if background == "true" {
			body["refreshing"] = refreshing
		}
		write(w, 200, body)
	})(w, r, mc)
}

// Background reads return completed snapshots immediately. A cold instance is
// omitted until its native answer arrives; refreshing tells the console to poll.
func (m *Module) providerSnapshotsRead(ctx context.Context, tenant model.TenantID, background bool) ([]ProviderSnapshot, bool, error) {
	instances, err := m.providerInstances(ctx, tenant)
	if err != nil {
		return nil, false, err
	}
	snaps := make([]ProviderSnapshot, len(instances))
	pending := make([]bool, len(instances))
	var wg sync.WaitGroup
	for i, in := range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snaps[i], pending[i] = m.providerSnapshotRead(ctx, in, background)
		}()
	}
	wg.Wait()
	out := make([]ProviderSnapshot, 0, len(snaps))
	refreshing := false
	for i, snap := range snaps {
		refreshing = refreshing || pending[i]
		if snap.Instance == "" {
			continue
		}
		// A login made through Olivares is listed only when it is signed in (Grok
		// Build, which reports no state, only when its session file exists); the
		// default instance is always listed.
		if instances[i].own || snap.State == stateReady || snap.State == stateUnknown {
			out = append(out, snap)
		}
	}
	return out, refreshing, ctx.Err()
}

// providerInstances lists the engine user's own login of each tool and, for a
// tenant, the login that organization made through Olivares. A tenant this node
// does not serve is an error, as it is for a sign-in status read.
func (m *Module) providerInstances(ctx context.Context, tenant model.TenantID) ([]providerInstance, error) {
	var out []providerInstance
	for _, facts := range driverfacts.All() {
		if facts.SignIn == "" {
			continue
		}
		own := providerInstance{id: facts.Key, driver: facts.Key, own: true}
		own.env, own.configDir, own.err = ownLogin(facts.Key)
		out = append(out, own)
		if tenant.IsZero() {
			continue
		}
		env, configDir, err := m.toolEnv(ctx, tenant, facts.Key, "")
		if err != nil {
			return nil, err
		}
		if configDir != own.configDir {
			out = append(out, providerInstance{id: driverfacts.OlivaresLoginInstance(facts.Key), driver: facts.Key, configDir: configDir, env: env})
		}
	}
	return out, nil
}

// providerSnapshotRead starts at most one probe per instance per refresh window.
// Synchronous callers wait for that probe; background callers get the last answer.
func (m *Module) providerSnapshotRead(ctx context.Context, in providerInstance, background bool) (ProviderSnapshot, bool) {
	key := providerKey(in.driver, in.configDir)
	m.mu.Lock()
	if m.providerEntries == nil {
		m.providerEntries = map[string]*providerEntry{}
	}
	e := m.providerEntries[key]
	if e == nil {
		e = &providerEntry{}
		m.providerEntries[key] = e
	}
	if m.ctx.Err() == nil && (e.attempted.IsZero() || time.Since(e.attempted) >= providerRefresh && e.done == nil) {
		e.attempted = time.Now()
		e.done = make(chan struct{})
		if e.have {
			e.snap.Stale = true
		}
		m.wg.Add(1)
		go m.refreshProvider(in, e)
	}
	done, snap := e.done, e.snap
	m.mu.Unlock()
	if done == nil {
		return snap, false
	}
	if background {
		return snap, true
	}
	select {
	case <-ctx.Done():
		return snap, true
	case <-done:
		m.mu.Lock()
		defer m.mu.Unlock()
		return e.snap, false
	}
}

func (m *Module) refreshProvider(in providerInstance, e *providerEntry) {
	defer m.wg.Done()
	ctx, cancel := context.WithTimeout(m.ctx, providerProbeTimeout)
	defer cancel()
	snap, err := m.probeProvider(ctx, in)
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case err == nil:
		e.snap, e.have = snap, true
	case e.have:
		e.snap.Stale, e.snap.Error = true, err.Error()
	default:
		snap.State, snap.Error = stateError, err.Error()
		e.snap = snap
	}
	close(e.done)
	e.done = nil
}
