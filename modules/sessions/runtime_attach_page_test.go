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
	"testing"
	"time"
)

// A long session opens on its last frames (?tail=) and reads earlier ones a page at a time (?before=), through the
// attach route and its checks.
func TestAttachHTTP_PagesTheBufferedOutput(t *testing.T) {
	h, fr, admin, tenant := attachHTTPHarness(t, WithSessionWorkspaceRoot(t.TempDir()))
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json", "name": "pages"})
	lr, ok := h.m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatal("expected a live handle")
	}
	for i := 1; i <= 10; i++ {
		fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(fmt.Sprintf("line-%d", i))}
	}
	waitFor(t, "ten buffered frames", func() bool { return len(lr.ring.readFrom(0).frames) == 10 })
	first := lr.ring.readFrom(0).frames[0].Seq

	ts := httptest.NewServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	attach := func(query string, done func([]sseEvt) bool) []sseEvt {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/m/sessions/runs/"+ref+"/attach?"+query, nil)
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
			t.Fatalf("attach?%s = %d", query, res.StatusCode)
		}
		return collectSSETimeout(t, res.Body, 3*time.Second, done)
	}
	outputs := func(evts []sseEvt) (seqs []int64, events []string) {
		for _, e := range evts {
			events = append(events, e.Event)
			if e.Event != "output" {
				continue
			}
			var f attachFrame
			if err := json.Unmarshal([]byte(e.Data), &f); err != nil {
				t.Fatalf("output json: %v", err)
			}
			seqs = append(seqs, f.Seq)
		}
		return seqs, events
	}
	// earlier reads the page event: whether the buffer holds frames before the ones sent.
	earlier := func(evts []sseEvt) []bool {
		var out []bool
		for _, e := range evts {
			if e.Event != "page" {
				continue
			}
			var p attachPage
			if err := json.Unmarshal([]byte(e.Data), &p); err != nil {
				t.Fatalf("page json: %v %q", err, e.Data)
			}
			out = append(out, p.Earlier)
		}
		return out
	}
	count := func(n int) func([]sseEvt) bool {
		return func(evts []sseEvt) bool { s, _ := outputs(evts); return len(s) >= n }
	}

	// Opening on the last three frames: first the word that earlier ones are buffered, then the three, and nothing
	// claims a loss.
	evts := attach("tail=3", count(3))
	seqs, events := outputs(evts)
	if events[0] != "page" || fmt.Sprint(earlier(evts)) != "[true]" {
		t.Fatalf("tail=3 did not open with page earlier=true: %v %v", events, earlier(evts))
	}
	if fmt.Sprint(seqs) != fmt.Sprint([]int64{first + 7, first + 8, first + 9}) {
		t.Fatalf("tail=3 served %v (events %v), want the last three from %d", seqs, events, first+7)
	}
	for _, e := range events {
		if e == "lag" {
			t.Fatalf("tail=3 reported a lag: %v", events)
		}
	}
	// A tail longer than the buffer is the whole buffer.
	evts = attach("tail=50", count(10))
	if seqs, _ = outputs(evts); len(seqs) != 10 || seqs[0] != first || fmt.Sprint(earlier(evts)) != "[false]" {
		t.Fatalf("tail=50 served %v, earlier %v", seqs, earlier(evts))
	}

	// An earlier page: the frames before the cursor, then the end of the page, then the stream closes.
	evts = attach(fmt.Sprintf("from=%d&before=%d", first+4, first+7), nil)
	seqs, events = outputs(evts)
	if fmt.Sprint(seqs) != fmt.Sprint([]int64{first + 4, first + 5, first + 6}) {
		t.Fatalf("the page served %v (events %v)", seqs, events)
	}
	if events[len(events)-1] != "page" || fmt.Sprint(earlier(evts)) != "[true]" {
		t.Fatalf("the page did not end with page earlier=true: %v %v", events, earlier(evts))
	}
	// The first page says nothing is left before it.
	evts = attach(fmt.Sprintf("from=%d&before=%d", first, first+2), nil)
	if seqs, _ = outputs(evts); len(seqs) != 2 || fmt.Sprint(earlier(evts)) != "[false]" {
		t.Fatalf("the first page served %v, earlier %v", seqs, earlier(evts))
	}
}
