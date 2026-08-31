// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	assert.Equal(t, Name, d.Name)
	assert.Equal(t, sdk.TypeSource, d.Type)
	assert.Equal(t, sdk.APIVersion, d.APIVersion)
	assert.NotEmpty(t, d.ConfigFields)
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("stdin ok", func(t *testing.T) {
		s := New()
		require.NoError(t, s.Open(ctx, cfgWith("events_path", "-")))
		assert.True(t, s.useStdin)
	})
	t.Run("missing file errors", func(t *testing.T) {
		s := New()
		err := s.Open(ctx, cfgWith("events_path", filepath.Join(t.TempDir(), "nope.jsonl")))
		assert.Error(t, err)
	})
	t.Run("directory errors", func(t *testing.T) {
		s := New()
		err := s.Open(ctx, cfgWith("events_path", t.TempDir()))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "directory")
	})
	t.Run("existing file ok", func(t *testing.T) {
		s := New()
		path := writeStream(t, "")
		require.NoError(t, s.Open(ctx, cfgWith("events_path", path)))
		assert.False(t, s.useStdin)
	})
}

func TestGatherEmitsGoldenEdges(t *testing.T) {
	p := procFix{execID: "x1", pid: 100, binary: "/usr/bin/claude", args: "claude --print", container: "abc123def4567890", node: "n1"}
	path := writeStream(t,
		jsonStr(t, exitEvent(procFix{execID: "x0", binary: "/bin/true"}, ts(0))), // ignored
		jsonStr(t, fileKprobe(p, "/etc/passwd", mayRead, ts(1*time.Second))),
		jsonStr(t, fileKprobe(p, "/var/log/app.log", mayWrite, ts(2*time.Second))),
		jsonStr(t, connectKprobe(p, "93.184.216.34", 443, "example.com", ts(3*time.Second))),
	)

	s := New()
	require.NoError(t, s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"events_path": path, "follow": "false",
	}}))
	sink := &fakeSink{}
	require.NoError(t, s.Gather(context.Background(), sink))

	edges := sink.edges()
	require.Len(t, edges, 3)
	assert.Empty(t, sink.findings(), "no anti-evasion findings when detection is off")

	// All edges share the kernel-observed identity and provenance.
	for _, e := range edges {
		assert.Equal(t, originIdentity, e.OriginKind)
		assert.Equal(t, "container:abc123def456/claude", e.OriginRef)
		assert.Equal(t, model.SignalEBPF, e.Source)
		assert.Equal(t, model.ConfidenceApproximate, e.Confidence)
	}

	assert.Equal(t, resFile, edges[0].ResourceKind)
	assert.Equal(t, "/etc/passwd", edges[0].ResourceRef)
	assert.Equal(t, model.ModeRead, edges[0].Mode)

	assert.Equal(t, "/var/log/app.log", edges[1].ResourceRef)
	assert.Equal(t, model.ModeWrite, edges[1].Mode)

	assert.Equal(t, resNet, edges[2].ResourceKind)
	assert.Equal(t, "tcp://example.com:443", edges[2].ResourceRef, "SNI used as the endpoint host")
	assert.Equal(t, model.ModeReadWrite, edges[2].Mode)

	// ObservedAt is the kernel event time (the SDK-named de-dup natural key); a
	// wrong timestamp on the emitted edge would otherwise go uncaught.
	assert.True(t, edges[0].ObservedAt.Equal(at(1*time.Second)), "edge keeps the kernel event time")
	assert.True(t, edges[1].ObservedAt.Equal(at(2*time.Second)))
	assert.True(t, edges[2].ObservedAt.Equal(at(3*time.Second)))
}

func TestGatherSkipsMalformedLines(t *testing.T) {
	p := procFix{binary: "/usr/bin/cat", node: "n1"}
	path := writeStream(t,
		jsonStr(t, fileKprobe(p, "/etc/hosts", mayRead, ts(0))),
		"{ this is not valid json",
		jsonStr(t, connectKprobe(p, "1.2.3.4", 443, "", ts(time.Second))),
	)
	s := New()
	require.NoError(t, s.Open(context.Background(), sdk.Config{Settings: map[string]string{"events_path": path, "follow": "false"}}))
	sink := &fakeSink{}
	require.NoError(t, s.Gather(context.Background(), sink))

	edges := sink.edges()
	require.Len(t, edges, 2, "the malformed line is skipped; both valid events still emit")
	assert.Equal(t, resFile, edges[0].ResourceKind)
	assert.Equal(t, resNet, edges[1].ResourceKind)
}

func TestGatherNetworkSkbAndIPv6(t *testing.T) {
	p := procFix{binary: "/usr/bin/curl", node: "n1"}
	path := writeStream(t, jsonStr(t, connectKprobeSkb(p, "2001:db8::1", 8443, "", ts(0))))
	s := New()
	require.NoError(t, s.Open(context.Background(), sdk.Config{Settings: map[string]string{"events_path": path, "follow": "false"}}))
	sink := &fakeSink{}
	require.NoError(t, s.Gather(context.Background(), sink))

	edges := sink.edges()
	require.Len(t, edges, 1)
	assert.Equal(t, resNet, edges[0].ResourceKind)
	assert.Equal(t, "tcp://[2001:db8::1]:8443", edges[0].ResourceRef, "skb-arg carrier + IPv6 endpoint")
}

func TestReadLoopReturnsErrorOnFault(t *testing.T) {
	s := New()
	require.NoError(t, s.Open(context.Background(), cfgWith("events_path", "-")))
	line := jsonStr(t, fileKprobe(procFix{binary: "/bin/cat"}, "/etc/hosts", mayRead, ts(0))) + "\n"
	r := &errReader{data: []byte(line), err: errors.New("disk gone")}

	err := s.readLoop(context.Background(), r, func(model.Observation) {})
	require.Error(t, err, "a non-EOF read fault is returned, not swallowed")
	assert.Contains(t, err.Error(), "read events")
}

func TestGatherShutdownNotWedgedByBlockedSink(t *testing.T) {
	path := writeStream(t, jsonStr(t, fileKprobe(procFix{binary: "/bin/cat", node: "n1"}, "/etc/hosts", mayRead, ts(0))))
	s := New()
	s.shutdownGrace = 50 * time.Millisecond // bound the watchdog for a fast test
	require.NoError(t, s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"events_path": path, "follow": "true", // stay running until ctx cancel
	}}))

	ctx, cancel := context.WithCancel(context.Background())
	sink := &blockingSink{entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	// Wait until the dispatcher is parked inside the blocked Emit on the first edge.
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("sink.Emit was never reached")
	}
	cancel()
	select {
	case <-done: // the watchdog cancels emitCtx after the grace, freeing the drain
	case <-time.After(2 * time.Second):
		t.Fatal("Gather wedged on a blocked sink after ctx cancel")
	}
}

func TestGatherReturnsErrorWhenSourceVanishes(t *testing.T) {
	path := writeStream(t, jsonStr(t, fileKprobe(procFix{binary: "/bin/cat"}, "/etc/hosts", mayRead, ts(0))))
	s := New()
	require.NoError(t, s.Open(context.Background(), sdk.Config{Settings: map[string]string{"events_path": path, "follow": "false"}}))
	require.NoError(t, os.Remove(path)) // open will now fail inside Gather

	err := s.Gather(context.Background(), &fakeSink{})
	require.Error(t, err, "a real open fault is returned so the engine can retry")
}

func TestGatherHonorsContextCancel(t *testing.T) {
	path := writeStream(t, jsonStr(t, fileKprobe(procFix{binary: "/bin/cat", node: "n1"}, "/etc/hosts", mayRead, ts(0))))
	s := New()
	require.NoError(t, s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"events_path": path, "follow": "true", // tail: Gather blocks at EOF until canceled
	}}))

	ctx, cancel := context.WithCancel(context.Background())
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	require.Eventually(t, func() bool { return len(sink.edges()) == 1 }, time.Second, 5*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "a ctx-driven stop is a clean return")
	case <-time.After(2 * time.Second):
		t.Fatal("Gather did not return after ctx cancel")
	}
}

func TestGatherAntiEvasionEndToEnd(t *testing.T) {
	fixedNow := time.Date(2026, 6, 3, 13, 0, 0, 0, time.UTC) // well past the window

	t.Run("agent without cooperative telemetry is flagged", func(t *testing.T) {
		p := procFix{execID: "a1", binary: "/usr/bin/claude", args: "claude", node: "n1"}
		path := writeStream(t, jsonStr(t, fileKprobe(p, "/etc/shadow", mayRead, ts(0))))
		sink := runGatherWithEvasion(t, path, fixedNow)

		findings := sink.findings()
		require.Len(t, findings, 1)
		assert.Equal(t, findingAntiEvasion, findings[0].Kind)
		assert.Equal(t, model.SeverityLow, findings[0].Severity)
	})

	t.Run("agent that connects to OTLP is not flagged", func(t *testing.T) {
		p := procFix{execID: "a2", binary: "/usr/bin/claude", args: "claude", node: "n1"}
		path := writeStream(t,
			jsonStr(t, connectKprobe(p, "127.0.0.1", 4317, "", ts(0))), // cooperative connection
			jsonStr(t, fileKprobe(p, "/etc/shadow", mayRead, ts(1*time.Second))),
		)
		sink := runGatherWithEvasion(t, path, fixedNow)
		assert.Empty(t, sink.findings(), "a cooperative agent is not an evasion")
	})
}

// runGatherWithEvasion opens and runs the connector with evasion detection enabled
// and a fixed clock (so the post-window sweep fires deterministically).
func runGatherWithEvasion(t *testing.T, path string, now time.Time) *fakeSink {
	t.Helper()
	s := New()
	require.NoError(t, s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"events_path":    path,
		"follow":         "false",
		"detect_evasion": "true",
		"evasion_window": "1ms",
	}}))
	s.now = func() time.Time { return now }
	sink := &fakeSink{}
	require.NoError(t, s.Gather(context.Background(), sink))
	return sink
}

// --- helpers -----------------------------------------------------------------

func cfgWith(k, v string) sdk.Config {
	return sdk.Config{Settings: map[string]string{k: v}}
}

func jsonStr(t *testing.T, v any) string {
	t.Helper()
	return string(jsonLine(t, v))
}

func writeStream(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	var content string
	for _, l := range lines {
		content += l + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}
