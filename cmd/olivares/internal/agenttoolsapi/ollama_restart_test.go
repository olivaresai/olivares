// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// nextEngine is the engine after a restart: a new module over the same directories,
// program and state file, started the way boot starts it, auditing into st's system
// chain as boot.go does.
func (f *ollamaFixture) nextEngine(t *testing.T, st store.Store) *Module {
	return f.nextEngineRecording(t, st, nil)
}

// nextEngineRecording is nextEngine that also hands each draft it audits to seen
// (the stored chain keeps a commitment to the metadata, not the map).
func (f *ollamaFixture) nextEngineRecording(t *testing.T, st store.Store, seen func(model.AuditDraft)) *Module {
	t.Helper()
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	m.SetProgramResolver(func(driver string) string { return filepath.Join(f.bin, driver) })
	cfg := f.cfg
	cfg.Audit = func(ctx context.Context, draft model.AuditDraft) error {
		if seen != nil {
			seen(draft)
		}
		return st.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
			_, err := sc.Audit().Append(ctx, draft)
			return err
		})
	}
	m.UseOllama(cfg)
	m.RestartOllama()
	return m
}

// auditStore is an engine store with its system chain, as the engine has.
func auditStore(t *testing.T) store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(s store.SystemScope) error { _, e := s.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	return st
}

// restartRows are the automatic-restart rows in st's system chain.
func restartRows(t *testing.T, st store.Store) []model.AuditEvent {
	t.Helper()
	var rows []model.AuditEvent
	err := st.View(context.Background(), model.SystemTenantID, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error {
			if ev.Action == "agenttools.ollama.restart" {
				rows = append(rows, ev)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func ollamaStateWithin(m *Module, want string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if m.ollamaStatus(context.Background()).State == want {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// HU2-14: after an engine restart the Ollama a person had started stayed "Not running"
// until someone pressed Start, and the tools it serves failed. The start is kept
// outside the child's directories; the next engine starts it again and registers it in
// the same tenant; Stop forgets it.
func TestOllamaComesBackWithTheEngineUntilStopped(t *testing.T) {
	call, f := newOllamaServer(t)
	code, org := call("POST", "/v1/system/orgs", map[string]string{"name": "Second", "slug": "second"})
	if code != 201 {
		t.Fatalf("org %d %v", code, org)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]string{"tenant_id": org["tenant_id"].(string)}); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	waitOllama(t, call, "running")
	f.mod.Close() // the engine stops
	if listening(f.addr) {
		t.Fatal("Ollama outlived the engine")
	}
	for _, dir := range []string{f.cfg.ModelsDir, f.cfg.HomeDir} {
		if rel, err := filepath.Rel(dir, f.cfg.StateFile); err == nil && filepath.IsLocal(rel) {
			t.Fatalf("the state file %s is inside %s, which the child writes", f.cfg.StateFile, dir)
		}
	}
	if _, err := os.Stat(f.cfg.StateFile); err != nil {
		t.Fatalf("the person's start was not kept: %v", err)
	}
	f.mu.Lock()
	before := f.registers
	f.mu.Unlock()

	next := f.nextEngine(t, auditStore(t))
	if !ollamaStateWithin(next, ollamaRunning, 20*time.Second) {
		t.Fatalf("after the restart Ollama is %+v, want running", next.ollamaStatus(context.Background()))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		registers, tenant := f.registers, f.tenant
		f.mu.Unlock()
		if registers > before {
			if tenant.String() != org["tenant_id"] {
				t.Fatalf("registered again for %q, want %v", tenant, org["tenant_id"])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the restarted Ollama was not registered again")
		}
		time.Sleep(50 * time.Millisecond)
	}
	next.Close()

	// Stop is the person's choice: the next engine leaves it stopped.
	call2, f2 := newOllamaServer(t)
	call2("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call2, "running")
	if code, st := call2("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 || st["state"] != "stopped" {
		t.Fatalf("stop = %d %v", code, st)
	}
	f2.mod.Close()
	if _, err := os.Stat(f2.cfg.StateFile); !os.IsNotExist(err) {
		t.Fatalf("Stop left the start kept: %v", err)
	}
	after := f2.nextEngine(t, auditStore(t))
	if ollamaStateWithin(after, ollamaRunning, 2*time.Second) || listening(f2.addr) {
		t.Fatal("an Ollama the person stopped came back with the engine")
	}
}

// SR2C on 773059bd: the engine's own restart of a person's Ollama was not audited. It
// is one system row per boot, written before the start, naming who started it and
// when, the tenant and the endpoint; with no audit written, nothing is started.
func TestOllamaRestartWritesItsAuditRow(t *testing.T) {
	call, f := newOllamaServer(t)
	code, org := call("POST", "/v1/system/orgs", map[string]string{"name": "Third", "slug": "third"})
	if code != 201 {
		t.Fatalf("org %d %v", code, org)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]string{"tenant_id": org["tenant_id"].(string)}); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	waitOllama(t, call, "running")
	f.mod.Close()

	st := auditStore(t)
	var drafts []model.AuditDraft
	next := f.nextEngineRecording(t, st, func(d model.AuditDraft) { drafts = append(drafts, d) })
	if !ollamaStateWithin(next, ollamaRunning, 20*time.Second) {
		t.Fatalf("after the restart Ollama is %+v, want running", next.ollamaStatus(context.Background()))
	}
	rows := restartRows(t, st)
	if len(rows) != 1 || len(drafts) != 1 || rows[0].ActorKind != model.ActorSystem || rows[0].Actor != drafts[0].Actor {
		t.Fatalf("restart rows %+v from drafts %+v, want one system row per boot", rows, drafts)
	}
	meta := drafts[0].Meta
	reason, _ := meta["reason"].(string)
	if !strings.HasPrefix(reason, "restarted with the engine; started by user:") || !strings.Contains(reason, " at 20") ||
		meta["register_in"] != org["tenant_id"] || meta["endpoint"] != "http://"+f.addr {
		t.Fatalf("restart row metadata = %v, want who started it and when, the tenant and the endpoint", meta)
	}
	next.Close()

	// No audit, no start: a failed write leaves the service stopped and says why.
	cfg := f.cfg
	cfg.Audit = func(context.Context, model.AuditDraft) error { return errors.New("audit store unavailable") }
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	m.SetProgramResolver(func(driver string) string { return filepath.Join(f.bin, driver) })
	m.UseOllama(cfg)
	m.RestartOllama()
	if got := m.ollamaStatus(context.Background()); got.State != ollamaFailed || !strings.Contains(got.Message, "audit record could not be written") || listening(f.addr) {
		t.Fatalf("with no audit the restart gave %+v (listening=%v), want no start and the reason", got, listening(f.addr))
	}
}
