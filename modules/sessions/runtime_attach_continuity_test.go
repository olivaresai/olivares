// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

type sseEvt struct {
	Event string
	Data  string
}

func parseSSE(raw string) []sseEvt {
	return collectSSE(strings.NewReader(raw), 0, nil)
}

func collectSSE(r io.Reader, limit int, done func([]sseEvt) bool) []sseEvt {
	br := bufio.NewReader(r)
	var out []sseEvt
	var event, data string
	flush := func() {
		if event == "" && data == "" {
			return
		}
		if event == "" {
			event = "message"
		}
		out = append(out, sseEvt{Event: event, Data: data})
		event, data = "", ""
	}
	for {
		if limit > 0 && len(out) >= limit {
			return out
		}
		if done != nil && done(out) {
			return out
		}
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				flush()
				if done != nil && done(out) {
					return out
				}
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				payload := strings.TrimPrefix(line, "data:")
				if strings.HasPrefix(payload, " ") {
					payload = payload[1:]
				}
				if data != "" {
					data += "\n"
				}
				data += payload
			}
		}
		if err != nil {
			flush()
			return out
		}
	}
}

func collectSSETimeout(t *testing.T, r io.Reader, d time.Duration, done func([]sseEvt) bool) []sseEvt {
	t.Helper()
	ch := make(chan []sseEvt, 1)
	go func() { ch <- collectSSE(r, 0, done) }()
	select {
	case evts := <-ch:
		return evts
	case <-time.After(d):
		t.Fatal("timed out reading SSE")
		return nil
	}
}

func noticeOf(t *testing.T, evts []sseEvt) attachNotice {
	t.Helper()
	for _, e := range evts {
		if e.Event != "notice" {
			continue
		}
		var n attachNotice
		if err := json.Unmarshal([]byte(e.Data), &n); err != nil {
			t.Fatalf("notice json: %v %q", err, e.Data)
		}
		return n
	}
	t.Fatalf("no notice in %#v", evts)
	return attachNotice{}
}

func launchHTTPRun(t *testing.T, h *harness, admin string, tenant model.TenantID, body map[string]any) string {
	t.Helper()
	wr := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{
		"root_path": t.TempDir(), "name": "ws-attach",
	}, tenantHdr(tenant))
	if wr.code != http.StatusCreated {
		t.Fatalf("workspace = %d %s", wr.code, wr.raw)
	}
	wsRef, _ := wr.body["workspace_ref"].(string)
	body["workspace_ref"] = wsRef
	if body["isolation"] == nil {
		body["isolation"] = "native"
	}
	if body["permission_mode"] == nil {
		body["permission_mode"] = "default"
	}
	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	ref, _ := r.body["run_ref"].(string)
	if ref == "" {
		t.Fatalf("run_ref missing: %s", r.raw)
	}
	return ref
}

func attachHTTPHarness(t *testing.T, opts ...Option) (*harness, *fakeRunner, string, model.TenantID) {
	t.Helper()
	fr := &fakeRunner{}
	args := append([]Option{WithRunner(fr), WithCredentialSource(staticCred())}, opts...)
	m := New(args...)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	return h, fr, admin, tenant
}

func TestAttachHTTP_NotLiveNoticeIsTypedAndNotEnd(t *testing.T) {
	h, _, admin, tenant := attachHTTPHarness(t)
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{
		"transport": "stream-json", "name": "notice-nl",
	})
	if r := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}
	if r := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/cleanup", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("cleanup = %d %s", r.code, r.raw)
	}
	r := h.do("GET", "/v1/m/sessions/runs/"+ref+"/attach?from=0", admin, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("attach = %d %s", r.code, r.raw)
	}
	if !strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %q", r.header.Get("Content-Type"))
	}
	evts := parseSSE(r.raw)
	n := noticeOf(t, evts)
	if n.IOUnavailable != attachIOUnavailableNotLiveOnNode {
		t.Fatalf("io_unavailable = %q want %s (%+v)", n.IOUnavailable, attachIOUnavailableNotLiveOnNode, n)
	}
	if n.Type != "notice" || n.Detail == "" {
		t.Fatalf("additive fields lost: %+v", n)
	}
	for _, e := range evts {
		if e.Event == "end" {
			t.Fatalf("I/O-absence notice must not emit end: %#v", evts)
		}
		if e.Event == "output" {
			t.Fatalf("notice path must not emit output: %#v", evts)
		}
	}
}

func TestAttachHTTP_RemoteControlNoticeIsTypedAndNotEnd(t *testing.T) {
	h, _, admin, tenant := attachHTTPHarness(t)
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{
		"transport": "remote-control", "name": "notice-rc",
	})
	r := h.do("GET", "/v1/m/sessions/runs/"+ref+"/attach?from=0", admin, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("attach = %d %s", r.code, r.raw)
	}
	evts := parseSSE(r.raw)
	n := noticeOf(t, evts)
	if n.IOUnavailable != attachIOUnavailableRemoteControl {
		t.Fatalf("io_unavailable = %q want %s (%+v)", n.IOUnavailable, attachIOUnavailableRemoteControl, n)
	}
	for _, e := range evts {
		if e.Event == "end" || e.Event == "output" {
			t.Fatalf("remote-control notice must not be process I/O: %#v", evts)
		}
	}
}

func TestAttachHTTP_OtherTenantRejectedBeforeFrames(t *testing.T) {
	h, _, admin, tenant := attachHTTPHarness(t)
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{
		"transport": "stream-json", "name": "iso",
	})
	other := h.createOrg(admin, "globex")
	r := h.do("GET", "/v1/m/sessions/runs/"+ref+"/attach?from=0", admin, tenantHdr(other))
	if r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant attach = %d %s, want 404", r.code, r.raw)
	}
	if strings.Contains(r.raw, "event:") {
		t.Fatalf("cross-tenant attach opened SSE: %q", r.raw)
	}
	if r := h.do("GET", "/v1/m/sessions/runs/"+ref+"/attach", "", tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Fatalf("no-auth attach = %d, want 401 (%s)", r.code, r.raw)
	}
}

func TestAttachHTTP_LagReportsDroppedAndNextSeq(t *testing.T) {
	fr := &fakeRunner{}
	m := New(WithRunner(fr), WithCredentialSource(staticCred()))
	m.rt.ringFrames = 3
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{
		"transport": "stream-json", "name": "lag",
	})
	lr, ok := m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatal("expected a live handle")
	}
	for i := 0; i < 10; i++ {
		fr.lastProc().out <- OutputFrame{
			Stream: streamStdout,
			Data:   []byte(fmt.Sprintf("line-%d", i)),
		}
	}
	waitFor(t, "ring evicted below cursor 1", func() bool {
		rd := lr.ring.readFrom(1)
		return rd.gap && len(rd.frames) == 3
	})

	ts := httptest.NewServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/m/sessions/runs/"+ref+"/attach?from=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("X-Olivares-Tenant", tenant.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("attach = %d", res.StatusCode)
	}
	evts := collectSSETimeout(t, res.Body, 3*time.Second, func(evts []sseEvt) bool {
		hasLag, outs := false, 0
		for _, e := range evts {
			if e.Event == "lag" {
				hasLag = true
			}
			if e.Event == "output" {
				outs++
			}
		}
		return hasLag && outs >= 3
	})
	cancel()
	var lag attachLagWire
	found := false
	var seqs []int64
	for _, e := range evts {
		switch e.Event {
		case "lag":
			if err := json.Unmarshal([]byte(e.Data), &lag); err != nil {
				t.Fatalf("lag json: %v %q", err, e.Data)
			}
			found = true
		case "output":
			var f attachFrame
			if err := json.Unmarshal([]byte(e.Data), &f); err != nil {
				t.Fatalf("output json: %v", err)
			}
			seqs = append(seqs, f.Seq)
		}
	}
	if !found || lag.Dropped <= 0 || lag.NextSeq < 1 {
		t.Fatalf("lag = %+v", lag)
	}
	if len(seqs) == 0 || seqs[0] != lag.NextSeq {
		t.Fatalf("frames %v do not resume at next_seq %d", seqs, lag.NextSeq)
	}
}

func TestAttachHTTP_InventoryStreamIsNotRunIO(t *testing.T) {
	h, fr, admin, tenant := attachHTTPHarness(t)
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{
		"transport": "stream-json", "name": "not-stream",
	})
	marker := "ATTACH-RING-MARKER-NOT-INVENTORY"
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(marker)}
	lr, ok := h.m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatal("expected a live handle")
	}
	waitFor(t, "marker in ring", func() bool {
		for _, f := range lr.ring.readFrom(0).frames {
			if strings.Contains(string(f.Data), marker) {
				return true
			}
		}
		return false
	})

	ts := httptest.NewServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/m/sessions/stream", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("X-Olivares-Tenant", tenant.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", res.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := res.Body.Read(buf)
	chunk := string(buf[:n])
	if strings.Contains(chunk, marker) || strings.Contains(chunk, "event: output") ||
		strings.Contains(chunk, `"type":"lag"`) {
		t.Fatalf("inventory stream carried attach I/O: %q", chunk)
	}
}

type attachLagWire struct {
	Type    string `json:"type"`
	Dropped int64  `json:"dropped"`
	NextSeq int64  `json:"next_seq"`
}
