// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/olivaresai/olivares/sdk/model"
)

// --- fakeSink ----------------------------------------------------------------

// fakeSink is a concurrency-safe sdk.Sink that records every emitted observation,
// so a test can assert on what the connector produced.
type fakeSink struct {
	mu  sync.Mutex
	obs []model.Observation
}

func (f *fakeSink) Emit(_ context.Context, o model.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs = append(f.obs, o)
	return nil
}

func (f *fakeSink) all() []model.Observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Observation(nil), f.obs...)
}

func (f *fakeSink) edges() []model.EdgeObservation {
	var out []model.EdgeObservation
	for _, o := range f.all() {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range f.all() {
		if fr, ok := o.(model.FindingReport); ok {
			out = append(out, fr)
		}
	}
	return out
}

// blockingSink is a conformant Sink that is permanently saturated: Emit blocks
// until its context is canceled (the stuck/slow-engine case the shutdown watchdog
// must survive). entered closes when Emit is first reached.
type blockingSink struct {
	entered chan struct{}
	once    sync.Once
}

func (b *blockingSink) Emit(ctx context.Context, _ model.Observation) error {
	b.once.Do(func() { close(b.entered) })
	<-ctx.Done()
	return ctx.Err()
}

// --- Tetragon JSON fixture builders ------------------------------------------
//
// The builders emit the snake_case protojson shape Tetragon produces (the format
// of `tetra getevents -o json`), so the tests exercise the decoder against
// realistic wire text — including verifying every json struct tag.

// procFix is a minimal process fixture.
type procFix struct {
	execID    string
	pid       int
	binary    string
	args      string
	container string
	node      string
}

func (p procFix) processMap() map[string]any {
	m := map[string]any{"binary": p.binary, "arguments": p.args, "pid": p.pid}
	if p.execID != "" {
		m["exec_id"] = p.execID
	}
	if p.container != "" {
		m["docker"] = p.container
	}
	return m
}

func fileKprobe(p procFix, path string, mask int, ts string) map[string]any {
	return map[string]any{
		"process_kprobe": map[string]any{
			"process":       p.processMap(),
			"function_name": "security_file_permission",
			"args": []any{
				map[string]any{"file_arg": map[string]any{"path": path}},
				map[string]any{"int_arg": mask},
			},
		},
		"time":      ts,
		"node_name": p.node,
	}
}

func connectKprobe(p procFix, daddr string, dport int, sni, ts string) map[string]any {
	sock := map[string]any{
		"family": "AF_INET", "type": "SOCK_STREAM", "protocol": "IPPROTO_TCP",
		"saddr": "10.0.0.2", "daddr": daddr, "sport": 54321, "dport": dport,
	}
	args := []any{map[string]any{"sock_arg": sock}}
	if sni != "" {
		args = append(args, map[string]any{"string_arg": sni})
	}
	return map[string]any{
		"process_kprobe": map[string]any{
			"process":       p.processMap(),
			"function_name": "tcp_connect",
			"args":          args,
		},
		"time":      ts,
		"node_name": p.node,
	}
}

func connectKprobeSkb(p procFix, daddr string, dport int, sni, ts string) map[string]any {
	skb := map[string]any{
		"saddr": "10.0.0.2", "daddr": daddr, "sport": 54321, "dport": dport, "proto": "IPPROTO_TCP",
	}
	args := []any{map[string]any{"skb_arg": skb}}
	if sni != "" {
		args = append(args, map[string]any{"string_arg": sni})
	}
	return map[string]any{
		"process_kprobe": map[string]any{
			"process":       p.processMap(),
			"function_name": "tcp_connect",
			"args":          args,
		},
		"time":      ts,
		"node_name": p.node,
	}
}

func exitEvent(p procFix, ts string) map[string]any {
	return map[string]any{
		"process_exit": map[string]any{"process": p.processMap(), "time": ts},
		"time":         ts,
		"node_name":    p.node,
	}
}

func jsonLine(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// tsBase is the fixed clock origin for the ts/at test helpers.
var tsBase = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

// ts builds an RFC3339Nano timestamp offset by d from a fixed base, for
// deterministic ordering in tests.
func ts(d time.Duration) string { return tsBase.Add(d).Format(time.RFC3339Nano) }

// at returns the time.Time that ts(d) encodes (the expected ObservedAt of an edge
// built from an event stamped ts(d)).
func at(d time.Duration) time.Time { return tsBase.Add(d) }

// errReader yields its bytes once, then returns a non-EOF error on every
// subsequent read, exercising the connector's read-fault path.
type errReader struct {
	data []byte
	err  error
}

func (r *errReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}
