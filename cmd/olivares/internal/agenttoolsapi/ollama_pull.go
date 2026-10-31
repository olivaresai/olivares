// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// Model downloads of the Ollama service: the routes that start and read one, and the
// worker that follows Ollama's own progress.

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
			// Succeeded once the tenant's record lists the new model.
			m.registerOllamaAgain()
			finish("succeeded", "")
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
	if err := register(ctx, actor, tenant, m.ollamaEndpoint()); err != nil {
		// The pull itself worked; say on the row that the tenant's record did not follow,
		// or the new model is silently unusable from sessions. The cause is kept too:
		// without it the operator cannot tell a sealed record from a refused write.
		note := "Its new model list could not be added to Providers: " + boundedText(err.Error())
		slog.Warn("ollama pull: the tenant's provider record did not follow the new model", "tenant", tenant.String(), "error", boundedText(err.Error()))
		m.mu.Lock()
		m.ollama.message = appendNote(m.ollama.message, note)
		m.mu.Unlock()
	}
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
