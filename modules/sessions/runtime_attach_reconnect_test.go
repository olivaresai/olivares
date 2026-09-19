// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// attachPeerScript is a local protocol peer, not an official authenticated
// provider account. It emits two sequenced batches with a pause so the first
// HTTP attach can be cut while the child is still alive.
const attachPeerScript = `#!/bin/sh
trap 'exit 0' TERM
printf '{"type":"system","subtype":"init","session_id":"sess-attach-1"}\n'
i=0
while [ "$i" -lt 8 ]; do
  printf '{"type":"assistant","n":%s}\n' "$i"
  i=$((i + 1))
done
sleep 2
while [ "$i" -lt 16 ]; do
  printf '{"type":"assistant","n":%s}\n' "$i"
  i=$((i + 1))
done
exec sleep 30
`

func writeAttachPeer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "attach-peer.sh")
	if err := os.WriteFile(path, []byte(attachPeerScript), 0o755); err != nil {
		t.Fatalf("write attach peer: %v", err)
	}
	return path
}

func TestAttachHTTP_CutWhileLiveThenCursorReconciles(t *testing.T) {
	script := writeAttachPeer(t)
	m := New(WithSessionWorkspaceRoot(t.TempDir()), 
		WithRunner(NewProcRunner()),
		WithProgram(script),
		WithCredentialSource(staticCred()),
		WithStopWaitDelay(2*time.Second),
	)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{
		"transport": "stream-json", "name": "cut-live",
	})
	lr, ok := m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatal("expected a live handle")
	}
	waitAttach(t, "first batch in ring", 8*time.Second, func() bool {
		n := len(lr.ring.readFrom(0).frames)
		return n >= 8 && n < 16
	})

	ts := httptest.NewServer(h.srv.Handler())
	t.Cleanup(ts.Close)

	first, firstURL := openAttach(t, ts, admin, tenant.String(), ref, 0)
	defer first.cancel()
	batch1 := collectSSETimeout(t, first.res.Body, 5*time.Second, func(evts []sseEvt) bool {
		return countOutputs(evts) >= 4
	})
	first.cancel()
	_ = first.res.Body.Close()
	if !strings.Contains(firstURL, "from=0") {
		t.Fatalf("first attach must use from=0, got %s", firstURL)
	}

	seen := map[int64]string{}
	var last int64
	for _, e := range batch1 {
		if e.Event != "output" {
			continue
		}
		f := mustFrame(t, e.Data)
		if _, dup := seen[f.Seq]; dup {
			t.Fatalf("duplicate seq %d on first attach", f.Seq)
		}
		seen[f.Seq] = f.Line
		if f.Seq > last {
			last = f.Seq
		}
	}
	if len(seen) == 0 {
		t.Fatal("first attach delivered no output")
	}
	cursor := last + 1

	waitAttach(t, "second batch in ring", 8*time.Second, func() bool {
		return len(lr.ring.readFrom(0).frames) >= 17
	})
	ringTotal := len(lr.ring.readFrom(0).frames)

	second, secondURL := openAttach(t, ts, admin, tenant.String(), ref, cursor)
	defer second.cancel()
	batch2 := collectSSETimeout(t, second.res.Body, 5*time.Second, func(evts []sseEvt) bool {
		return countOutputs(evts) >= ringTotal-len(seen)
	})
	second.cancel()
	_ = second.res.Body.Close()
	if !strings.Contains(secondURL, "from="+strconv.FormatInt(cursor, 10)) {
		t.Fatalf("second attach must resume at from=%d, got %s", cursor, secondURL)
	}

	for _, e := range batch2 {
		if e.Event != "output" {
			continue
		}
		f := mustFrame(t, e.Data)
		if f.Seq < cursor {
			t.Fatalf("second attach replayed seq %d below cursor %d", f.Seq, cursor)
		}
		if _, dup := seen[f.Seq]; dup {
			t.Fatalf("duplicate seq %d across attaches", f.Seq)
		}
		seen[f.Seq] = f.Line
	}

	ring := lr.ring.readFrom(0)
	if ring.gap {
		t.Fatal("default ring must retain the full sequenced tail")
	}
	if len(seen) != len(ring.frames) {
		t.Fatalf("union %d frames, ring %d (loss or extra)", len(seen), len(ring.frames))
	}
	for _, f := range ring.frames {
		if _, ok := seen[f.Seq]; !ok {
			t.Fatalf("lost seq %d (%s)", f.Seq, f.Data)
		}
	}
	if _, live := m.rt.getLive(tenant, ref); !live {
		t.Fatal("child must still be live after the cut")
	}
}

type attachConn struct {
	res    *http.Response
	cancel context.CancelFunc
}

func openAttach(t *testing.T, ts *httptest.Server, token, tenant, ref string, from int64) (attachConn, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	url := ts.URL + "/v1/m/sessions/runs/" + ref + "/attach?from=" + strconv.FormatInt(from, 10)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Olivares-Tenant", tenant)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		cancel()
		t.Fatalf("attach status = %d", res.StatusCode)
	}
	return attachConn{res: res, cancel: cancel}, url
}

func mustFrame(t *testing.T, data string) attachFrame {
	t.Helper()
	var f attachFrame
	if err := json.Unmarshal([]byte(data), &f); err != nil {
		t.Fatalf("frame json: %v %q", err, data)
	}
	return f
}

func countOutputs(evts []sseEvt) int {
	n := 0
	for _, e := range evts {
		if e.Event == "output" {
			n++
		}
	}
	return n
}

func waitAttach(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
