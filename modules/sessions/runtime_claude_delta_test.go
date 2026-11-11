// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

func TestClaudePartialReplyBypassesReplayAndRecorder(t *testing.T) {
	rec := &countingRecorder{}
	h, runner, admin, tenant := attachHTTPHarness(t, WithRecorder(rec), WithLaunchGate(&spyGate{inner: LaunchDecision{Allowed: true, RecordIO: true}}))
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json"})
	proc := runner.lastProc()
	lr, _ := h.m.rt.getLive(tenant, ref)
	defer proc.finish(0)
	server := httptest.NewServer(h.srv.Handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/m/sessions/runs/"+ref+"/attach", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("X-Olivares-Tenant", tenant.String())
	// Keep the turn open until attach has delivered text.
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for i := 0; i < 3000; i++ {
			select {
			case proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}}`)}:
			case <-ctx.Done():
				return
			}
		}
	}()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	events := collectSSE(res.Body, 0, func(es []sseEvt) bool {
		for _, e := range es {
			if e.Event == "delta" {
				return true
			}
		}
		return false
	})
	var live attachFrame
	for _, e := range events {
		if e.Event == "delta" {
			if err := json.Unmarshal([]byte(e.Data), &live); err != nil {
				t.Fatal(err)
			}
		}
	}
	if live.Line == "" || live.Seq != 0 {
		t.Fatalf("no unsequenced partial before turn end (%d events)", len(events))
	}
	if got := len(lr.ring.readFrom(0).frames); got != 0 {
		t.Fatalf("partials filled replay: %d frames", got)
	}
	if seqs, _ := rec.snapshot(); len(seqs) != 0 {
		t.Fatalf("partials recorded: %d", len(seqs))
	}
	if !strings.Contains(strings.Join(cliruntime.ClaudeArgs(cliruntime.LaunchRequest{}), " "), "--include-partial-messages") {
		t.Fatal("partial messages not enabled")
	}
	<-sent
	final := fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":%q}]}}`, strings.Repeat("x", 3000))
	proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(final)}
	rd := ringWithAtLeast(lr.ring, 1, time.Second)
	if len(rd.frames) != 1 || string(rd.frames[0].Data) != final {
		t.Fatalf("final reply not retained: %+v", rd)
	}
	waitFor(t, "final recording", func() bool { seqs, _ := rec.snapshot(); return len(seqs) == 1 })
}
