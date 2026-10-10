// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

// Browser preview of the app a session serves on a loopback port.
//
//	GET  /runs/{ref}/preview         the ports the session's own processes listen on
//	POST /runs/{ref}/preview {port}  a short-lived URL /session/preview/<token>/ for one
//
// The engine serves that URL (ServePreviewHTTP) by dialing the port inside the
// session's own network, so the port itself is never exposed. The token is the
// only credential: the page runs sandboxed in an opaque origin, which cannot read
// or use the console's session, and the engine's cookies never reach the app.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	previewTTL = time.Hour
	// previewRecheck bounds how long a port the session closed keeps answering.
	previewRecheck = 2 * time.Second
	maxPreviews    = 256
	// maxRunPreviews bounds one session's open previews: each reload opens one.
	maxRunPreviews = 8
)

// previewCSP keeps the previewed page in an opaque origin, also when it is
// opened in its own tab, and lets only the console frame it.
const previewCSP = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads; frame-ancestors 'self'"

// previewResponseHeaders are the app's response headers the preview passes on
// (canonical names): the content's own description, caching, redirects and a
// WebSocket upgrade for a dev server's live reload.
var previewResponseHeaders = map[string]bool{
	"Content-Type": true, "Content-Length": true, "Content-Encoding": true, "Content-Language": true,
	"Content-Disposition": true, "Content-Range": true, "Accept-Ranges": true, "Etag": true,
	"Last-Modified": true, "Cache-Control": true, "Expires": true, "Vary": true, "Date": true,
	"Location": true, "Allow": true, "Retry-After": true,
	"Connection": true, "Upgrade": true, "Sec-Websocket-Accept": true, "Sec-Websocket-Protocol": true,
	"Sec-Websocket-Extensions": true,
}

// previewDialer is a live process that can reach a port in its own network.
type previewDialer interface {
	DialPreview(ctx context.Context, address string) (net.Conn, error)
}

// listenPort is one port the session listens on, and the loopback address the
// engine dials for it.
type listenPort struct {
	Port    int
	Address string
}

// sessionPorts lists a live process tree's ports (preview_linux.go); tests
// replace it to stand in for a process they do not start.
var sessionPorts = sessionListenPorts

type previewGrant struct {
	tenant  model.TenantID
	runRef  string
	proc    Process
	port    int
	expires time.Time
	proxy   *httputil.ReverseProxy
	opened  uint64 // order of opening on this node

	mu      sync.Mutex
	address string
	checked time.Time
}

type previewGrants struct {
	mu      sync.Mutex
	byToken map[string]*previewGrant
	opened  uint64
}

// put stores grant under token. Expired grants and grants whose run ended (live
// reports false) are dropped first, so a stopped session frees its previews.
func (g *previewGrants) put(token string, grant *previewGrant, now time.Time, live func(*previewGrant) bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byToken == nil {
		g.byToken = map[string]*previewGrant{}
	}
	// A run at its limit gives up its oldest preview, so reloading one session
	// never fills the node for the others.
	var oldest *previewGrant
	oldestToken, runs := "", 0
	for t, old := range g.byToken {
		if !now.Before(old.expires) || !live(old) {
			delete(g.byToken, t)
			continue
		}
		if old.tenant == grant.tenant && old.runRef == grant.runRef {
			runs++
			if oldest == nil || old.opened < oldest.opened {
				oldest, oldestToken = old, t
			}
		}
	}
	if runs >= maxRunPreviews {
		delete(g.byToken, oldestToken)
	}
	if len(g.byToken) >= maxPreviews {
		return false
	}
	g.opened++
	grant.opened = g.opened
	g.byToken[token] = grant
	return true
}

func (g *previewGrants) get(token string, now time.Time) *previewGrant {
	g.mu.Lock()
	defer g.mu.Unlock()
	grant := g.byToken[token]
	if grant != nil && !now.Before(grant.expires) {
		delete(g.byToken, token)
		return nil
	}
	return grant
}

func (g *previewGrants) drop(token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.byToken, token)
}

// previewPorts lists the loopback ports a live run's own processes listen on.
// A run that is not live on this node listens on nothing.
func (m *Module) previewPorts(tenant model.TenantID, ref string) (Process, []listenPort, error) {
	lr, live := m.rt.getLive(tenant, ref)
	if !live {
		return nil, nil, nil
	}
	if _, ok := lr.proc.(previewDialer); !ok || lr.proc.PID() <= 0 {
		return nil, nil, nil
	}
	ports, err := sessionPorts(lr.proc.PID())
	return lr.proc, ports, err
}

// handleRunPreviewPorts lists the loopback ports the run's own processes listen
// on, which a browser preview can open. A run not live on this node has none.
func (m *Module) handleRunPreviewPorts(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	_, ports, err := m.previewPorts(mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		m.debugf("sessions: preview port listing failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("the session's ports cannot be read on this host"))
		return
	}
	out := []int{}
	for _, p := range ports {
		out = append(out, p.Port)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ports": out})
}

type previewRequest struct {
	Port int `json:"port"`
}

// handleOpenRunPreview opens a browser preview of one port the run listens on and
// returns its URL, which expires within the hour and when the session stops.
func (m *Module) handleOpenRunPreview(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	var body previewRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if body.Port < 1 || body.Port > 65535 {
		writeJSON(w, http.StatusBadRequest, errorBody("port must be between 1 and 65535"))
		return
	}
	proc, ports, err := m.previewPorts(mc.Tenant, ref)
	if err != nil {
		m.debugf("sessions: preview port listing failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("the session's ports cannot be read on this host"))
		return
	}
	if proc == nil {
		writeJSON(w, http.StatusConflict, errorBody("the session is not running on this node"))
		return
	}
	i := slices.IndexFunc(ports, func(p listenPort) bool { return p.Port == body.Port })
	if i < 0 {
		writeJSON(w, http.StatusNotFound, errorBody("no process of this session listens on port "+strconv.Itoa(body.Port)))
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("could not create the preview"))
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := m.clock.Now().Time()
	grant := &previewGrant{tenant: mc.Tenant, runRef: ref, proc: proc, port: body.Port,
		expires: now.Add(previewTTL), address: ports[i].Address, checked: now}
	grant.proxy = m.newPreviewProxy(grant)
	if !m.previews.put(token, grant, now, m.previewLive) {
		writeJSON(w, http.StatusTooManyRequests, errorBody("too many previews are open on this node; try again later"))
		return
	}
	// The preview is a credential into the session's app: one that the ledger
	// does not record is not handed out.
	if err := m.auditPreview(r, mc, ref, body.Port); err != nil {
		m.previews.drop(token)
		m.debugf("sessions: run-preview audit failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("the preview could not be recorded in the audit ledger; try again"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"url": api.SessionPreviewPathPrefix + token + "/", "port": body.Port, "expires_at": grant.expires.UTC().Format(time.RFC3339),
	})
}

func (m *Module) auditPreview(r *http.Request, mc api.ModuleContext, ref string, port int) error {
	return mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		_, e := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: "sessions.run.preview", TargetKind: runKind,
			Meta: map[string]any{"run_ref": ref, "port": port},
		})
		return e
	})
}

// ServePreviewHTTP serves /session/preview/<token>/<path> from the granted port.
func (m *Module) ServePreviewHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Del("X-Frame-Options")
	h.Set("Content-Security-Policy", previewCSP)
	token, rest, slash := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), api.SessionPreviewPathPrefix), "/")
	grant := m.previews.get(token, m.clock.Now().Time())
	if grant == nil || !m.previewLive(grant) {
		if grant != nil {
			m.previews.drop(token)
		}
		http.Error(w, "This preview has ended. Open it again from the session.", http.StatusNotFound)
		return
	}
	if !slash {
		http.Redirect(w, r, token+"/", http.StatusPermanentRedirect)
		return
	}
	address, ok := m.previewAddress(grant)
	if !ok {
		http.Error(w, "The session no longer listens on port "+strconv.Itoa(grant.port)+".", http.StatusBadGateway)
		return
	}
	// The page has an opaque origin, so its own requests are cross-origin. The
	// token in the path is the credential; no cookie is accepted here.
	h.Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		h.Set("Access-Control-Allow-Methods", r.Header.Get("Access-Control-Request-Method"))
		if v := r.Header.Get("Access-Control-Request-Headers"); v != "" {
			h.Set("Access-Control-Allow-Headers", v)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	p, err := url.PathUnescape(rest)
	if err != nil {
		http.Error(w, "The preview path is not a valid URL path.", http.StatusBadRequest)
		return
	}
	out := r.Clone(context.WithValue(r.Context(), previewAddressKey{}, address))
	out.URL.Path, out.URL.RawPath = "/"+p, "/"+rest
	// A dev server's live-reload stream or a long download outlives the engine's
	// write timeout; the preview's own end is the session's (best-effort, as in stream.go).
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	out.Host = "localhost:" + strconv.Itoa(grant.port)
	grant.proxy.ServeHTTP(w, out)
}

type previewAddressKey struct{}

// previewLive reports whether the grant's process is still the run's live one.
func (m *Module) previewLive(g *previewGrant) bool {
	lr, live := m.rt.getLive(g.tenant, g.runRef)
	return live && lr.proc == g.proc
}

// previewAddress re-reads the session's ports at most every previewRecheck, so
// a port the session closed stops answering even if another process takes it.
func (m *Module) previewAddress(g *previewGrant) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := m.clock.Now().Time()
	if now.Sub(g.checked) < previewRecheck {
		return g.address, g.address != ""
	}
	ports, err := sessionPorts(g.proc.PID())
	if err != nil {
		// No answer about the port is no port (its process may be gone and the
		// port someone else's); the next request reads again.
		m.debugf("sessions: preview port listing failed", "err", err)
		g.address = ""
		return "", false
	}
	g.checked, g.address = now, ""
	for _, p := range ports {
		if p.Port == g.port {
			g.address = p.Address
		}
	}
	return g.address, g.address != ""
}

func (m *Module) newPreviewProxy(g *previewGrant) *httputil.ReverseProxy {
	dialer := g.proc.(previewDialer)
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			address, _ := ctx.Value(previewAddressKey{}).(string)
			return dialer.DialPreview(ctx, address)
		},
		IdleConnTimeout: 30 * time.Second,
	}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", pr.In.Host
			pr.Out.Host = pr.In.Host
			// The engine session cookie is the user's credential, never the app's.
			pr.Out.Header.Del("Cookie")
		},
		Transport:     transport,
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			// The app's response is served on the engine's origin: a header that
			// sets browser state there (cookies, site data, reporting, Alt-Svc,
			// credentials prompts, a looser Referrer-Policy) never passes; only
			// what describes the content does.
			for name := range resp.Header {
				if !previewResponseHeaders[name] {
					delete(resp.Header, name)
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			m.debugf("sessions: preview proxy failed", "run_ref", g.runRef, "port", g.port, "err", err)
			http.Error(w, "The session's app did not answer on port "+strconv.Itoa(g.port)+".", http.StatusBadGateway)
		},
	}
}

// sessionsRunPreviewResponses publishes what the two preview routes answer.
func sessionsRunPreviewResponses(method, pattern string) (map[string]any, bool) {
	if pattern != "/runs/{ref}/preview" {
		return nil, false
	}
	resp := api.GenericModuleResponses()
	resp["503"] = oas.JSONResp("the session's ports cannot be read on this host")
	if method == http.MethodGet {
		resp["200"] = oas.Obj("description", "the ports the session's own processes listen on",
			"content", oas.Obj("application/json", oas.Obj("schema", oas.Obj(
				"type", "object", "additionalProperties", false, "required", oas.Enum("ports"),
				"properties", oas.Obj("ports", oas.Obj("type", "array",
					"items", oas.Obj("type", "integer", "minimum", 1, "maximum", 65535))),
			))))
		return resp, true
	}
	delete(resp, "200")
	resp["201"] = oas.Obj("description", "the preview's URL on this engine",
		"content", oas.Obj("application/json", oas.Obj("schema", oas.Obj(
			"type", "object", "additionalProperties", false, "required", oas.Enum("url", "port", "expires_at"),
			"properties", oas.Obj(
				"url", oas.Obj("type", "string", "description", "Same-origin path of the preview; the token in it is its only credential."),
				"port", oas.Obj("type", "integer"),
				"expires_at", oas.Obj("type", "string", "format", "date-time"),
			),
		))))
	resp["404"] = oas.JSONResp("no process of this session listens on the port")
	resp["409"] = oas.JSONResp("the session is not running on this node")
	resp["429"] = oas.JSONResp("too many previews are open on this node")
	return resp, true
}
