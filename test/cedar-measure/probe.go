// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"runtime"
	"runtime/metrics"
	"time"
)

const (
	// metricStacks is process-wide: every goroutine's stack memory. With
	// GODEBUG=gcshrinkstackoff=1 a grown stack is not shrunk by the collector,
	// so a reading after the operation is an aggregate retained-stack
	// observation, not an exact per-function peak.
	metricStacks = "/memory/classes/heap/stacks:bytes"
	// metricLive is the heap marked live by the last completed collection.
	metricLive = "/gc/heap/live:bytes"
)

var errMetricUnsupported = errors.New("runtime metric unsupported")

// reading is one ordered observation of the runtime.
type reading struct {
	Seq         int64  `json:"seq"`
	AtNS        int64  `json:"at_ns"`
	TotalAlloc  uint64 `json:"total_alloc_bytes"`
	Mallocs     uint64 `json:"mallocs"`
	StacksBytes uint64 `json:"stacks_bytes"`
	LiveBytes   uint64 `json:"live_heap_bytes"`
}

// mark is an ordered instant without a runtime observation.
type mark struct {
	Seq  int64 `json:"seq"`
	AtNS int64 `json:"at_ns"`
}

// probe numbers every reading and mark in the order taken, so a reading taken
// before the operation it claims to follow is visible in the record.
type probe struct {
	start   time.Time
	seq     int64
	stats   runtime.MemStats
	samples []metrics.Sample
}

func newProbe(start time.Time) (*probe, error) {
	p := &probe{
		start:   start,
		samples: []metrics.Sample{{Name: metricStacks}, {Name: metricLive}},
	}
	metrics.Read(p.samples)
	for _, s := range p.samples {
		if s.Value.Kind() != metrics.KindUint64 {
			return nil, errMetricUnsupported
		}
	}
	return p, nil
}

// read takes a full observation. It allocates nothing on the heap: the
// statistics and sample buffers belong to the probe.
func (p *probe) read() reading {
	runtime.ReadMemStats(&p.stats)
	metrics.Read(p.samples)
	p.seq++
	return reading{
		Seq:         p.seq,
		AtNS:        time.Since(p.start).Nanoseconds(),
		TotalAlloc:  p.stats.TotalAlloc,
		Mallocs:     p.stats.Mallocs,
		StacksBytes: p.samples[0].Value.Uint64(),
		LiveBytes:   p.samples[1].Value.Uint64(),
	}
}

func (p *probe) mark() mark {
	p.seq++
	return mark{Seq: p.seq, AtNS: time.Since(p.start).Nanoseconds()}
}

// measurement is the ordered evidence of one operation.
type measurement struct {
	Baseline reading  `json:"baseline"`
	OpStart  mark     `json:"op_start"`
	OpEnd    mark     `json:"op_end"`
	After    reading  `json:"after"`
	Live     *reading `json:"live,omitempty"`
}

// Deliberate defects, used only by the hosted control phase to show that the
// supervisor refuses them.
const (
	controlNone       = "none"
	controlSkipOp     = "skip-op"
	controlReadBefore = "read-before"
)

// measure runs op once in a fresh goroutine after two collections. The
// baseline and the after reading are taken inside that goroutine, so the stack
// reading includes the operation's grown stack. When retained is true it then
// collects again with the result still reachable and reads the live heap.
func measure(p *probe, op func(), control string, retained bool) measurement {
	var m measurement
	done := make(chan struct{})
	runtime.GC()
	runtime.GC()
	go func() {
		defer close(done)
		m.Baseline = p.read()
		if control == controlReadBefore {
			m.After = p.read()
		}
		m.OpStart = p.mark()
		if control != controlSkipOp {
			op()
		}
		m.OpEnd = p.mark()
		if control != controlReadBefore {
			m.After = p.read()
		}
	}()
	<-done
	if retained {
		runtime.GC()
		live := p.read()
		m.Live = &live
	}
	return m
}
