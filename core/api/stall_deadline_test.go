// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// slowIOModule has the two shapes the server's whole-request deadlines cut: an
// upload that arrives slowly, and a reply that waits on work longer than the
// write deadline (a session launch waits on git and a driver handshake).
type slowIOModule struct{ work time.Duration }

// heldDeadline is the write deadline the /held route sets for itself.
const heldDeadline = 100 * time.Millisecond

func (slowIOModule) APINamespace() string { return "slowio" }

func (slowIOModule) Permissions() []auth.Permission { return []auth.Permission{"slowio:thing:write"} }

func (m slowIOModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("POST", "/uploads", "slowio:thing:write", func(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestTimeout)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int64{"bytes": n})
	})
	reg.Handle("POST", "/launches", "slowio:thing:write", func(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
		select {
		case <-time.After(m.work):
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "running"})
	})
	// A route that reads past the end of its body and then works keeps its
	// request context: the work outlives the stall deadline.
	reg.Handle("POST", "/drains", "slowio:thing:write", func(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusRequestTimeout)
			return
		}
		_, _ = r.Body.Read(make([]byte, 1))
		select {
		case <-time.After(m.work):
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "running"})
	})
	// A route that bounds its own reply (an SSE frame, a held publication) keeps
	// that bound: here it expires before the work ends.
	reg.Handle("POST", "/held", "slowio:thing:write", func(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(heldDeadline)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		time.Sleep(m.work)
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "late"})
	})
}

// trickle is a request body that hands out one chunk per interval. With stall
// set it sends one chunk and then nothing until stall is closed.
type trickle struct {
	chunks   int
	size     int
	interval time.Duration
	stall    chan struct{}
	sent     int
}

func (b *trickle) Read(p []byte) (int, error) {
	if b.chunks == 0 {
		return 0, io.EOF
	}
	if b.stall != nil && b.sent > 0 {
		<-b.stall
		return 0, io.ErrUnexpectedEOF
	}
	time.Sleep(b.interval)
	b.chunks--
	b.sent++
	n := min(b.size, len(p))
	for i := range p[:n] {
		p[i] = 'x'
	}
	return n, nil
}

// stallServer serves the real API router on a real socket through NewHTTPServer,
// with the server's own whole-request deadlines shortened so the test runs in
// seconds: every route here takes longer than they allow.
func stallServer(t *testing.T, work time.Duration, configure ...func(*api.Server, *http.Server)) (base, token, tenant string) {
	t.Helper()
	h := newHarness(t, slowIOModule{work: work})
	token = h.adminLogin()
	tenant = h.createOrg(token, "stall").String()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := h.srv.NewHTTPServer(listener.Addr().String())
	srv.ReadTimeout = 200 * time.Millisecond
	srv.WriteTimeout = 400 * time.Millisecond
	for _, c := range configure {
		c(h.srv, srv)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	t.Cleanup(func() {
		_ = srv.Close()
		if err := <-done; err != http.ErrServerClosed {
			t.Errorf("serve: %v", err)
		}
	})
	return "http://" + listener.Addr().String(), token, tenant
}

func stallPost(t *testing.T, url, token, tenant string, body io.Reader) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Olivares-Tenant", tenant)
	client := &http.Client{Timeout: 20 * time.Second}
	defer client.CloseIdleConnections()
	return client.Do(req)
}

// A slow upload that keeps sending is never cut by the whole-request read
// deadline, and its reply is not lost to the write deadline armed when the
// request headers arrived.
func TestAPIUploadOutlivesServerReadDeadline(t *testing.T) {
	base, token, tenant := stallServer(t, 0)
	body := &trickle{chunks: 10, size: 1 << 10, interval: 100 * time.Millisecond}
	res, err := stallPost(t, base+"/v1/m/slowio/uploads", token, tenant, body)
	if err != nil {
		t.Fatalf("slow upload cut by the server: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(raw), fmt.Sprintf(`"bytes":%d`, 10<<10)) {
		t.Fatalf("slow upload = %d %s, want 200 with all %d bytes", res.StatusCode, raw, 10<<10)
	}
}

// A reply that waits on work longer than the server's write deadline still
// reaches the client: the route's own budget bounds the work, not the transport.
func TestAPIReplyOutlivesServerWriteDeadline(t *testing.T) {
	base, token, tenant := stallServer(t, time.Second)
	res, err := stallPost(t, base+"/v1/m/slowio/launches", token, tenant, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("reply lost after the server write deadline: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"running"`) {
		t.Fatalf("slow launch = %d %s, want 200 running", res.StatusCode, raw)
	}
}

// A client that stops sending its body is still cut, by the stall deadline: the
// server's whole-request ReadTimeout is off here, so without the stall bound the
// upload would hang until the client gave up.
func TestAPIUploadStallIsCut(t *testing.T) {
	stall := 300 * time.Millisecond
	base, token, tenant := stallServer(t, 0, func(a *api.Server, srv *http.Server) {
		a.SetStallForTest(stall)
		srv.ReadTimeout = 0
	})
	body := &trickle{chunks: 10, size: 1 << 10, interval: 10 * time.Millisecond, stall: make(chan struct{})}
	defer close(body.stall)
	started := time.Now()
	res, err := stallPost(t, base+"/v1/m/slowio/uploads", token, tenant, body)
	if err != nil {
		t.Fatalf("stalled upload: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if took := time.Since(started); res.StatusCode != http.StatusRequestTimeout || !strings.Contains(string(raw), "i/o timeout") || took > 10*stall {
		t.Fatalf("stalled upload = %d %q after %s, want 408 i/o timeout after about %s", res.StatusCode, raw, took, stall)
	}
}

// A route that sets its own write deadline keeps it: the stall deadline does not
// stretch a bound the route chose.
func TestAPIRouteWriteDeadlineIsKept(t *testing.T) {
	base, token, tenant := stallServer(t, 4*heldDeadline)
	res, err := stallPost(t, base+"/v1/m/slowio/held", token, tenant, strings.NewReader("{}"))
	if err == nil {
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("held reply = %d %s, want it cut at the route's own %s deadline", res.StatusCode, raw, heldDeadline)
	}
}

// A read after the body ended moves no deadline: net/http is then reading the
// connection in the background, and a deadline on that read would cancel the
// request's context halfway through the route's work.
func TestAPIReadAfterBodyEndKeepsContext(t *testing.T) {
	stall := 200 * time.Millisecond
	base, token, tenant := stallServer(t, 4*stall, func(a *api.Server, _ *http.Server) { a.SetStallForTest(stall) })
	res, err := stallPost(t, base+"/v1/m/slowio/drains", token, tenant, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("request context cancelled after the body ended: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"running"`) {
		t.Fatalf("drain = %d %s, want 200 running", res.StatusCode, raw)
	}
}
