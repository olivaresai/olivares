// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestUnboundedTransportOutlivesTheDefaultTimeout is the regression test for the
// defect the sol-max contrast found in the E4 work: `agent session attach` asked
// for "no timeout" by passing 0, and cliTransport reads 0 as "unspecified" and
// substitutes its ten-second default. http.Client.Timeout covers reading the
// BODY, so a live SSE attach — which is a body that stays open by design — died
// mid-stream after ten seconds. The merge-base used a bare http.Client whose
// zero Timeout genuinely means unlimited.
//
// The test keeps a response body open past the default and reads from it. With
// the default applied it fails; with Unbounded it does not. It uses a short
// stand-in for the default so the test costs milliseconds, not ten seconds.
func TestUnboundedTransportOutlivesTheDefaultTimeout(t *testing.T) {
	// A server that streams a line every 20ms and never finishes on its own.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := range 40 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			fmt.Fprintf(w, "data: %d\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	resolved := cliResolvedConfig{Server: srv.URL}

	read := func(t *testing.T, client *http.Client, want int) (int, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer func() { _ = resp.Body.Close() }()
		lines := 0
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if len(sc.Bytes()) > 0 {
				lines++
			}
			if lines >= want {
				return lines, nil
			}
		}
		return lines, sc.Err()
	}

	t.Run("a bounded client dies mid-body", func(t *testing.T) {
		client, _, err := cliTransport(cliTransportOptions{
			Resolved: resolved, Timeout: 60 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, rerr := read(t, client, 20); rerr == nil {
			t.Fatalf("a %v client read %d lines of an open stream without failing; "+
				"the guarantee below is then vacuous", 60*time.Millisecond, got)
		}
	})

	t.Run("Unbounded keeps the stream open", func(t *testing.T) {
		client, _, err := cliTransport(cliTransportOptions{
			Resolved: resolved, Unbounded: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if client.Timeout != 0 {
			t.Fatalf("Unbounded produced Timeout=%v; a long-lived attach must have none", client.Timeout)
		}
		if got, rerr := read(t, client, 20); rerr != nil {
			t.Fatalf("the unbounded client failed after %d lines: %v", got, rerr)
		}
	})

	t.Run("zero still means the default, not unlimited", func(t *testing.T) {
		client, _, err := cliTransport(cliTransportOptions{Resolved: resolved})
		if err != nil {
			t.Fatal(err)
		}
		if client.Timeout != defaultCLIRequestTimeout {
			t.Fatalf("Timeout=%v for an unspecified timeout, want the default %v",
				client.Timeout, defaultCLIRequestTimeout)
		}
	})
}

// TestAttachOutlivesTheDefaultTimeoutEndToEnd drives the REAL command against a
// real SSE server, because the structural version of this test did not do its
// job: pointing `agent session attach` back at the bounded transport left it
// passing. A test that asserts a helper's properties cannot see which helper the
// caller picked.
//
// defaultCLIRequestTimeout is shortened so "outlives the default" is provable in
// milliseconds; the command is given no --timeout, exactly as an operator would.
func TestAttachOutlivesTheDefaultTimeoutEndToEnd(t *testing.T) {
	restore := defaultCLIRequestTimeout
	t.Cleanup(func() { defaultCLIRequestTimeout = restore })
	defaultCLIRequestTimeout = 80 * time.Millisecond

	const lines = 25
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := range lines {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			// The real attach protocol: an `event:` selector then its `data:`.
			fmt.Fprintf(w, "event: output\ndata: {\"line\":\"line-%d\"}\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	t.Chdir(t.TempDir())
	t.Setenv("OLIVARES_CLI_CONFIG", t.TempDir()+"/config.yaml")
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"agent", "session", "attach", "run-123",
		"--server", srv.URL, "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"})

	done := make(chan error, 1)
	go func() { _, err := root.ExecuteC(); done <- err }()

	select {
	case err := <-done:
		// The stream ran to its end. Anything resembling a client-side deadline
		// is the regression: the whole point of an attach is that it stays open.
		if err != nil && strings.Contains(err.Error(), "Client.Timeout") {
			t.Fatalf("the attach died on the client deadline instead of following the "+
				"stream: %v\nread so far:\n%s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the attach did not finish; the stream server should have ended it")
	}
	if got := strings.Count(out.String(), "line-"); got < lines/2 {
		t.Fatalf("read only %d of %d streamed lines before stopping:\n%s", got, lines, out.String())
	}
}
