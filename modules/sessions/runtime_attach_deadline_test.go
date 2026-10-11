// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Shorten only the HTTP writer's clock so an idle-gap regression does not take
// thirty seconds. The real HTTP/2 stream still enforces and clears its deadline.
type attachDeadlineWriter struct{ http.ResponseWriter }

func (w attachDeadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w attachDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		deadline = time.Now().Add(500 * time.Millisecond)
	}
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
}

func TestAttachHTTP2IdleGapPreservesOutput(t *testing.T) {
	h, runner, admin, tenant := attachHTTPHarness(t)
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{
		"transport": "stream-json", "name": "idle-deadline",
	})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.srv.Handler().ServeHTTP(attachDeadlineWriter{w}, r)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/m/sessions/runs/"+ref+"/attach?from=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("X-Olivares-Tenant", tenant.String())
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ProtoMajor != 2 {
		t.Fatalf("attach response = %s over %s", response.Status, response.Proto)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != ": connected\n" {
		t.Fatalf("initial frame = %q, error = %v", line, err)
	}
	// No Write or Flush is in progress during this gap. A successfully drained
	// stream must outlive its per-write bound while waiting for the tool.
	time.Sleep(time.Second)
	process := runner.lastProc()
	process.out <- OutputFrame{Stream: streamStdout, Data: []byte("IDLE-GAP-OUTPUT")}
	process.finish(0)
	tail, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("idle attach reset before output: %v", err)
	}
	if !strings.Contains(string(tail), "IDLE-GAP-OUTPUT") || !strings.Contains(string(tail), "event: end") {
		t.Fatalf("tool output/end lost after idle gap: %s", tail)
	}
}
