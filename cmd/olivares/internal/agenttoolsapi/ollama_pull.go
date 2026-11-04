// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

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
	// cancel ends the download's request to Ollama; cancelled says a person asked for it.
	cancel    context.CancelFunc
	cancelled bool
	// completed says Ollama already downloaded the model; only provider synchronization remains.
	completed bool
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
		ctx, cancel := context.WithTimeout(m.ctx, ollamaPullLimit)
		p := &OllamaPull{ID: model.NewID(), Model: name, State: "running", started: time.Now(), cancel: cancel}
		if err := audit(r.Context(), mc, "ollama.pull", p.ID, map[string]any{"model": name}); err != nil {
			cancel()
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
		go m.runOllamaPull(ctx, p.ID, name)
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

// handleOllamaModelRemove deletes one installed model from the running Ollama.
// The models directory belongs to the engine, so a terminal's remove command would not reach it.
func (m *Module) handleOllamaModelRemove(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
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
		if m.readOnly {
			fail(w, 409, "read_only", "This control plane cannot remove models.")
			return
		}
		m.mu.Lock()
		running := m.ollama.state == ollamaRunning
		m.mu.Unlock()
		if !running {
			fail(w, 409, "not_running", "Start Ollama first, then remove a model.")
			return
		}
		if err := audit(r.Context(), mc, "ollama.model_remove", "", map[string]any{"model": name}); err != nil {
			unavailable(w)
			return
		}
		code, msg := m.ollamaDeleteModel(r.Context(), name)
		switch {
		case code == http.StatusNotFound:
			fail(w, 404, "not_found", "This model is not installed.")
			return
		case code != http.StatusOK:
			fail(w, 502, "ollama_error", msg)
			return
		}
		if err := m.registerOllamaAgain(); err != nil {
			fail(w, 502, "provider_sync_failed", "The model was removed, but its provider record could not be updated: "+err.Error())
			return
		}
		write(w, 200, m.ollamaStatus(r.Context()))
	})(w, r, mc)
}

// ollamaDeleteModel asks the service to delete a model (DELETE /api/delete) and returns
// its status code; on a failure, the message is Ollama's own line.
func (m *Module) ollamaDeleteModel(ctx context.Context, name string) (int, string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"model": name})
	req, _ := http.NewRequestWithContext(ctx, "DELETE", m.ollamaEndpoint()+"/api/delete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return http.StatusBadGateway, "Ollama could not be reached to remove the model."
	}
	defer resp.Body.Close()
	var reply struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&reply)
	if reply.Error != "" {
		return resp.StatusCode, "Ollama: " + reply.Error
	}
	return resp.StatusCode, "Ollama answered " + resp.Status + " to the removal."
}

// handleOllamaPullCancel ends a running download: the request to Ollama is closed, which
// stops its pull (what it fetched stays for the next try). A download that already ended
// is returned as it is.
func (m *Module) handleOllamaPullCancel(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		id := model.ID(chi.URLParam(r, "id"))
		m.mu.Lock()
		p, ok := m.ollama.pulls[id]
		running := ok && p.State == "running" && !p.completed
		m.mu.Unlock()
		if !ok {
			fail(w, 404, "not_found", "This download is no longer known.")
			return
		}
		if running {
			if err := audit(r.Context(), mc, "ollama.pull_cancel", id, nil); err != nil {
				unavailable(w)
				return
			}
			m.mu.Lock()
			if p.State == "running" && !p.completed {
				// Answer with the end state now; the worker finds the same state when its
				// stream closes.
				p.cancelled, p.State, p.Error = true, "failed", "Download cancelled."
				p.cancel()
			}
			m.mu.Unlock()
		}
		m.mu.Lock()
		view := *p
		m.mu.Unlock()
		write(w, 200, view)
	})(w, r, mc)
}

// runOllamaPull asks the service for the model and follows its streamed progress.
func (m *Module) runOllamaPull(ctx context.Context, id model.ID, name string) {
	defer m.wg.Done()
	finish := func(state, msg string) {
		m.mu.Lock()
		if p, ok := m.ollama.pulls[id]; ok {
			if p.cancelled && !p.completed {
				state, msg = "failed", "Download cancelled."
			}
			p.State, p.Error = state, msg
			p.cancel()
		}
		m.mu.Unlock()
	}
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
	lastLine := ""
	for scanner.Scan() {
		var line struct {
			Status    string `json:"status"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			lastLine = strings.TrimSpace(scanner.Text())
			if len(lastLine) > 200 {
				lastLine = lastLine[:200]
			}
			continue
		}
		lastLine = ""
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
			m.mu.Lock()
			if p, ok := m.ollama.pulls[id]; ok {
				p.completed = true
			}
			m.mu.Unlock()
			// Ollama has the model even if the tenant's provider record cannot follow it.
			// Keep that fact and return the synchronization failure as a visible warning.
			if err := m.registerOllamaAgain(); err != nil {
				finish("succeeded", "its provider record could not be updated: "+err.Error())
			} else {
				finish("succeeded", "")
			}
			return
		}
	}
	lastStatus := func() string {
		m.mu.Lock()
		defer m.mu.Unlock()
		if lastLine != "" {
			return " Its last line: " + lastLine
		}
		if p, ok := m.ollama.pulls[id]; ok && p.Status != "" {
			return " Its last line: " + p.Status
		}
		return ""
	}
	if err := scanner.Err(); err != nil {
		finish("failed", "Ollama's progress stream failed: "+err.Error()+lastStatus())
		return
	}
	if ctx.Err() != nil && m.ctx.Err() == nil {
		finish("failed", "The download did not finish within six hours."+lastStatus())
		return
	}
	finish("failed", "The download ended before Ollama reported success."+lastStatus())
}

// registerOllamaAgain runs Register for whoever started the running service, after a
// download or removal: the tenant's record then lists what the service holds. Nothing is
// registered for a service started outside a tenant. Its error reaches the operation's result.
func (m *Module) registerOllamaAgain() error {
	m.mu.Lock()
	register, actor, tenant := m.ollama.cfg.Register, m.ollama.owner, m.ollama.ownerTenant
	running := m.ollama.state == ollamaRunning
	m.mu.Unlock()
	if register == nil || tenant.IsZero() || !running {
		return nil
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
		return err
	}
	return nil
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
