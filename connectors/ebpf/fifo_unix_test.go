// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package ebpf

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/olivaresai/olivares/sdk"
)

// TestGatherCancelUnblocksFIFORead proves the fix for the blocking-FIFO-read bug:
// a read parked in the kernel on an idle FIFO is interrupted by ctx cancellation
// (the connector closes the reader), so Gather returns promptly on shutdown rather
// than waiting for the next byte. A regular-file test cannot exercise this because
// a regular file returns EOF immediately; only a connected idle FIFO blocks.
func TestGatherCancelUnblocksFIFORead(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "events.fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))

	s := New()
	require.NoError(t, s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"events_path": fifo, "follow": "true",
	}}))

	ctx, cancel := context.WithCancel(context.Background())
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	// Open the write end: this rendezvous unblocks Gather's read-open too. Keep it
	// open so the FIFO never reaches EOF — the read parks in the kernel waiting.
	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	_, err = w.Write([]byte(jsonStr(t, fileKprobe(procFix{binary: "/bin/cat", node: "n1"}, "/etc/hosts", mayRead, ts(0))) + "\n"))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(sink.edges()) == 1 }, 2*time.Second, 5*time.Millisecond,
		"the connector should consume the one event written to the FIFO")

	// No more data is written: the connector's read is now parked in the kernel.
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "a ctx-driven stop is a clean return")
	case <-time.After(2 * time.Second):
		t.Fatal("Gather did not return after ctx cancel — the FIFO read was not interrupted")
	}
}
