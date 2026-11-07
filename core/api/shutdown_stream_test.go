// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestShutdownEndsOpenStreams pins that Shutdown of the server NewHTTPServer
// builds does not wait for an open server-sent-events stream: a console tab holds
// one for as long as it is open, and the stream ends only when its request context
// does. Without that, every stop waits out the whole drain budget, and a container
// runtime kills the engine first (Docker stops at 10 s).
func TestShutdownEndsOpenStreams(t *testing.T) {
	h, _, admin := newLogHandlerHarness(t, slog.LevelInfo, 10)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := h.srv.NewHTTPServer(listener.Addr().String())
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	req, err := http.NewRequest("GET", "http://"+listener.Addr().String()+"/v1/console/logs/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+admin)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", res.StatusCode)
	}
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, ": connected") {
		t.Fatalf("stream first line = %q, %v", line, err)
	}

	// The budget is far above what a drain needs and far below the old wait for
	// the client, so a stream that outlives Shutdown fails here, not by timeout.
	const budget = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with an open stream = %v after %s; want nil (the stream must end when shutdown starts)", err, time.Since(start).Round(time.Millisecond))
	}
	if took := time.Since(start); took > budget/2 {
		t.Fatalf("Shutdown took %s with an open stream", took)
	}
	if err := <-serveDone; err != http.ErrServerClosed {
		t.Fatalf("Serve = %v, want ErrServerClosed", err)
	}
	if _, err := io.ReadAll(res.Body); err != nil {
		t.Fatalf("stream did not end cleanly: %v", err)
	}
}
