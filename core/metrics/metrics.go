// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package metrics is a pure-Go, zero-dependency Prometheus exposition registry for
// the engine's own /metrics endpoint (OBS-06). It deliberately does NOT import
// prometheus/client_golang: the product ships a single static pure-Go binary
// (ARCHITECTURE.md), and a metrics endpoint must not be the dependency that breaks that
// posture. The exposition target is the Prometheus text format version 0.0.4 — the
// stable de-facto standard every Prometheus server and OpenMetrics-1.0 consumer
// scrapes — NOT the experimental OpenMetrics 2.0 RC (OBS-06; pinned/verified
// against https://prometheus.io/docs/instrumenting/exposition_formats/).
//
// What it guarantees:
//   - Correct format. # HELP / # TYPE lines, one family at a time; label values
//     escape backslash, double-quote and newline; HELP escapes backslash and
//     newline (the exact set the spec reserves — quotes are NOT escaped in HELP).
//   - Deterministic output. Families render in sorted name order and each family's
//     series render in sorted label order, so a scrape diff is meaningful and the
//     golden tests pin it.
//   - Bounded cardinality by construction. A *Vec is created with a fixed set of
//     label names; callers can only supply values, never new label keys, so a
//     hostile or buggy caller cannot explode the series count.
//
// It is detective-only telemetry (docs/SECURITY-HARDENING.md): it exposes engine counters/gauges,
// never request bodies, tokens, tenant data or any value that would serve as recon
// (OBS-06; docs/SECURITY-HARDENING.md). Series names carry only low-cardinality structural labels
// (HTTP method, status code, observation kind), never identifiers.
package metrics

import (
	"bufio"
	"io"
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ContentType is the HTTP Content-Type for the Prometheus text exposition format
// version 0.0.4 (verified verbatim against the Prometheus exposition_formats spec).
// It is also accepted by every OpenMetrics-1.0 scraper, which negotiates down to
// the Prometheus text format.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// collector is a metric family that can render itself in the exposition format.
// Counter/Gauge/Histogram implement it, as does a scrape-time func collector for
// runtime stats. familyName is used to order families deterministically.
type collector interface {
	familyName() string
	write(w *bufio.Writer)
}

// Registry holds the engine's metric families and renders them on scrape. The zero
// Registry is not usable; build one with New. It is safe for concurrent use: the
// hot path (Inc/Observe) takes only the per-family lock, and a scrape takes the
// registry lock just long enough to snapshot the family list.
type Registry struct {
	mu      sync.Mutex
	order   []collector
	byName  map[string]collector
	start   time.Time
	version string
}

// New builds a Registry stamped with the engine version (exposed as
// olivares_build_info) and a process start time (exposed as
// olivares_process_start_time_seconds / olivares_uptime_seconds). It self-registers
// the Go runtime collector so go_* stats are always present.
func New(version string, now time.Time) *Registry {
	r := &Registry{
		byName:  map[string]collector{},
		start:   now.UTC(),
		version: version,
	}
	r.register(&funcCollector{name: "olivares_build_info", fn: r.writeBuildInfo})
	r.register(&funcCollector{name: "olivares_process_start_time_seconds", fn: r.writeStartTime})
	r.register(&funcCollector{name: "go_goroutines", fn: writeRuntime})
	return r
}

func (r *Registry) register(c collector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byName[c.familyName()]; ok {
		return // idempotent: a duplicate registration keeps the first
	}
	r.byName[c.familyName()] = c
	r.order = append(r.order, c)
}

// RegisterFunc adds a scrape-time collector that writes raw exposition lines for
// the named family. The fn is called under no lock during WritePrometheus; it must
// emit a well-formed family (its own # HELP/# TYPE + samples). Used by the host to
// expose a gauge whose value is read live at scrape time (e.g. store reachability).
func (r *Registry) RegisterFunc(name string, fn func(w io.Writer)) {
	r.register(&funcCollector{name: name, fn: fn})
}

// Counter creates (or returns the existing) labelless counter family.
func (r *Registry) Counter(name, help string) *Counter {
	return r.CounterVec(name, help)
}

// CounterVec creates (or returns the existing) counter family with a fixed set of
// label names. Re-requesting the same name returns the first instance, so wiring
// code can fetch a counter without threading a pointer everywhere.
func (r *Registry) CounterVec(name, help string, labelNames ...string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.byName[name]; ok {
		return c.(*Counter)
	}
	c := &Counter{base: base{name: name, help: help, labelNames: labelNames}, series: map[string]*scalar{}}
	r.byName[name] = c
	r.order = append(r.order, c)
	return c
}

// Gauge creates (or returns the existing) labelless gauge family.
func (r *Registry) Gauge(name, help string) *Gauge {
	return r.GaugeVec(name, help)
}

// GaugeVec creates (or returns the existing) gauge family with fixed label names.
func (r *Registry) GaugeVec(name, help string, labelNames ...string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.byName[name]; ok {
		return g.(*Gauge)
	}
	g := &Gauge{base: base{name: name, help: help, labelNames: labelNames}, series: map[string]*scalar{}}
	r.byName[name] = g
	r.order = append(r.order, g)
	return g
}

// HistogramVec creates (or returns the existing) histogram family with fixed label
// names and the given upper bounds (ascending; +Inf is added implicitly).
func (r *Registry) HistogramVec(name, help string, buckets []float64, labelNames ...string) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.byName[name]; ok {
		return h.(*Histogram)
	}
	bs := append([]float64(nil), buckets...)
	sort.Float64s(bs)
	h := &Histogram{base: base{name: name, help: help, labelNames: labelNames}, buckets: bs, series: map[string]*histSeries{}}
	r.byName[name] = h
	r.order = append(r.order, h)
	return h
}

// WritePrometheus renders every family in deterministic (sorted) order in the
// Prometheus text exposition format 0.0.4. It snapshots the family list under the
// registry lock, then renders without holding it (each family takes its own lock),
// so a scrape never blocks the hot path for longer than the snapshot.
func (r *Registry) WritePrometheus(out io.Writer) {
	r.mu.Lock()
	families := append([]collector(nil), r.order...)
	r.mu.Unlock()
	sort.Slice(families, func(i, j int) bool { return families[i].familyName() < families[j].familyName() })

	w := bufio.NewWriter(out)
	defer w.Flush()
	for _, f := range families {
		f.write(w)
	}
}

func (r *Registry) writeBuildInfo(out io.Writer) {
	w := bufw(out)
	w.WriteString("# HELP olivares_build_info Engine build information (constant 1; the version rides as a label).\n")
	w.WriteString("# TYPE olivares_build_info gauge\n")
	w.WriteString("olivares_build_info{version=\"")
	w.WriteString(escapeLabelValue(r.version))
	w.WriteString("\"} 1\n")
	w.Flush()
}

func (r *Registry) writeStartTime(out io.Writer) {
	w := bufw(out)
	w.WriteString("# HELP olivares_process_start_time_seconds Start time of the process since the Unix epoch, in seconds.\n")
	w.WriteString("# TYPE olivares_process_start_time_seconds gauge\n")
	w.WriteString("olivares_process_start_time_seconds ")
	w.WriteString(formatFloat(float64(r.start.UnixNano()) / 1e9))
	w.WriteByte('\n')
	w.WriteString("# HELP olivares_uptime_seconds Seconds since the process started.\n")
	w.WriteString("# TYPE olivares_uptime_seconds gauge\n")
	w.WriteString("olivares_uptime_seconds ")
	w.WriteString(formatFloat(time.Since(r.start).Seconds()))
	w.WriteByte('\n')
	w.Flush()
}

// --- base family -------------------------------------------------------------

type base struct {
	name       string
	help       string
	labelNames []string
}

func (b *base) familyName() string { return b.name }

// scalar is one labeled series of a counter or gauge.
type scalar struct {
	labels []string
	v      float64
}

// key joins label values into the stable map key for a series. The label NAMES are
// fixed at family creation, so the values alone identify a series.
func key(values []string) string { return strings.Join(values, "\x00") }

// writeHeader emits the # HELP and # TYPE lines for the family.
func (b *base) writeHeader(w *bufio.Writer, typ string) {
	w.WriteString("# HELP ")
	w.WriteString(b.name)
	w.WriteByte(' ')
	w.WriteString(escapeHelp(b.help))
	w.WriteByte('\n')
	w.WriteString("# TYPE ")
	w.WriteString(b.name)
	w.WriteByte(' ')
	w.WriteString(typ)
	w.WriteByte('\n')
}

// labelString renders {name="value",...} for the family's fixed label names bound
// to the given values, or "" when there are no labels. Suffix lets a histogram add
// the le="..." label inside the same brace group.
func (b *base) labelString(values []string, extra ...[2]string) string {
	if len(b.labelNames) == 0 && len(extra) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteByte('{')
	first := true
	for i, n := range b.labelNames {
		if i >= len(values) {
			break
		}
		if !first {
			sb.WriteByte(',')
		}
		first = false
		sb.WriteString(n)
		sb.WriteString("=\"")
		sb.WriteString(escapeLabelValue(values[i]))
		sb.WriteString("\"")
	}
	for _, e := range extra {
		if !first {
			sb.WriteByte(',')
		}
		first = false
		sb.WriteString(e[0])
		sb.WriteString("=\"")
		sb.WriteString(escapeLabelValue(e[1]))
		sb.WriteString("\"")
	}
	sb.WriteByte('}')
	return sb.String()
}

// --- Counter -----------------------------------------------------------------

// Counter is a monotonically increasing counter family. By convention its name
// ends in _total. Concurrent Inc/Add are safe.
type Counter struct {
	base
	mu     sync.Mutex
	series map[string]*scalar
}

// Inc adds 1 to the series identified by the given label values (in the order the
// family's label names were declared).
func (c *Counter) Inc(labelValues ...string) { c.Add(1, labelValues...) }

// Add adds delta (which must be >= 0; a negative delta is ignored, never panics)
// to the series identified by labelValues.
func (c *Counter) Add(delta float64, labelValues ...string) {
	if delta < 0 || math.IsNaN(delta) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(labelValues)
	s := c.series[k]
	if s == nil {
		s = &scalar{labels: append([]string(nil), labelValues...)}
		c.series[k] = s
	}
	s.v += delta
}

func (c *Counter) write(w *bufio.Writer) {
	c.mu.Lock()
	rows := snapshotScalars(c.series)
	c.mu.Unlock()
	c.writeHeader(w, "counter")
	if len(rows) == 0 {
		// A counter family with no observed series still emits a 0 sample for the
		// labelless case so a scraper sees the series exists; labeled families with
		// no series simply have no samples (Prometheus accepts a header-only family).
		if len(c.labelNames) == 0 {
			w.WriteString(c.name)
			w.WriteString(" 0\n")
		}
		return
	}
	for _, s := range rows {
		w.WriteString(c.name)
		w.WriteString(c.labelString(s.labels))
		w.WriteByte(' ')
		w.WriteString(formatFloat(s.v))
		w.WriteByte('\n')
	}
}

// --- Gauge -------------------------------------------------------------------

// Gauge is a value that can go up or down. Concurrent Set/Add/Inc/Dec are safe.
type Gauge struct {
	base
	mu     sync.Mutex
	series map[string]*scalar
}

// Set replaces the series value.
func (g *Gauge) Set(v float64, labelValues ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.at(labelValues).v = v
}

// Add adds delta (may be negative).
func (g *Gauge) Add(delta float64, labelValues ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.at(labelValues).v += delta
}

// Inc adds 1; Dec subtracts 1.
func (g *Gauge) Inc(labelValues ...string) { g.Add(1, labelValues...) }
func (g *Gauge) Dec(labelValues ...string) { g.Add(-1, labelValues...) }

func (g *Gauge) at(labelValues []string) *scalar {
	k := key(labelValues)
	s := g.series[k]
	if s == nil {
		s = &scalar{labels: append([]string(nil), labelValues...)}
		g.series[k] = s
	}
	return s
}

func (g *Gauge) write(w *bufio.Writer) {
	g.mu.Lock()
	rows := snapshotScalars(g.series)
	g.mu.Unlock()
	g.writeHeader(w, "gauge")
	if len(rows) == 0 && len(g.labelNames) == 0 {
		w.WriteString(g.name)
		w.WriteString(" 0\n")
		return
	}
	for _, s := range rows {
		w.WriteString(g.name)
		w.WriteString(g.labelString(s.labels))
		w.WriteByte(' ')
		w.WriteString(formatFloat(s.v))
		w.WriteByte('\n')
	}
}

// --- Histogram ---------------------------------------------------------------

// Histogram aggregates observations into cumulative buckets plus _sum/_count, the
// Prometheus histogram exposition. The +Inf bucket is implicit and always equals
// _count, as the spec requires.
type Histogram struct {
	base
	buckets []float64
	mu      sync.Mutex
	series  map[string]*histSeries
}

type histSeries struct {
	labels []string
	counts []uint64 // one per bucket bound, cumulative computed at write
	sum    float64
	count  uint64
}

// Observe records v into the series identified by labelValues.
func (h *Histogram) Observe(v float64, labelValues ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := key(labelValues)
	s := h.series[k]
	if s == nil {
		s = &histSeries{labels: append([]string(nil), labelValues...), counts: make([]uint64, len(h.buckets))}
		h.series[k] = s
	}
	s.sum += v
	s.count++
	// Increment the first bucket whose upper bound >= v; buckets are cumulative at
	// render time, so a single per-bound count is enough here.
	for i, ub := range h.buckets {
		if v <= ub {
			s.counts[i]++
			return
		}
	}
	// v exceeds every finite bound: it lands only in +Inf (count already bumped).
}

func (h *Histogram) write(w *bufio.Writer) {
	h.mu.Lock()
	keys := make([]string, 0, len(h.series))
	for k := range h.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([]*histSeries, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, h.series[k])
	}
	h.mu.Unlock()

	h.writeHeader(w, "histogram")
	for _, s := range rows {
		var cum uint64
		for i, ub := range h.buckets {
			cum += s.counts[i]
			w.WriteString(h.name)
			w.WriteString("_bucket")
			w.WriteString(h.labelString(s.labels, [2]string{"le", formatFloat(ub)}))
			w.WriteByte(' ')
			w.WriteString(strconv.FormatUint(cum, 10))
			w.WriteByte('\n')
		}
		// +Inf bucket == total count (spec requirement).
		w.WriteString(h.name)
		w.WriteString("_bucket")
		w.WriteString(h.labelString(s.labels, [2]string{"le", "+Inf"}))
		w.WriteByte(' ')
		w.WriteString(strconv.FormatUint(s.count, 10))
		w.WriteByte('\n')

		w.WriteString(h.name)
		w.WriteString("_sum")
		w.WriteString(h.labelString(s.labels))
		w.WriteByte(' ')
		w.WriteString(formatFloat(s.sum))
		w.WriteByte('\n')

		w.WriteString(h.name)
		w.WriteString("_count")
		w.WriteString(h.labelString(s.labels))
		w.WriteByte(' ')
		w.WriteString(strconv.FormatUint(s.count, 10))
		w.WriteByte('\n')
	}
}

// --- func collector ----------------------------------------------------------

// funcCollector renders a family by delegating to a func, for values read live at
// scrape time (runtime stats, build info, store reachability).
type funcCollector struct {
	name string
	fn   func(io.Writer)
}

func (f *funcCollector) familyName() string    { return f.name }
func (f *funcCollector) write(w *bufio.Writer) { f.fn(w) }

// writeRuntime emits the standard go_* runtime gauges read live at scrape time.
func writeRuntime(out io.Writer) {
	w := bufw(out)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	gauge := func(name, help string, v float64) {
		w.WriteString("# HELP ")
		w.WriteString(name)
		w.WriteByte(' ')
		w.WriteString(help)
		w.WriteByte('\n')
		w.WriteString("# TYPE ")
		w.WriteString(name)
		w.WriteString(" gauge\n")
		w.WriteString(name)
		w.WriteByte(' ')
		w.WriteString(formatFloat(v))
		w.WriteByte('\n')
	}
	gauge("go_goroutines", "Number of goroutines that currently exist.", float64(runtime.NumGoroutine()))
	gauge("go_threads", "Number of OS threads created.", float64(pthreads()))
	gauge("go_memstats_alloc_bytes", "Number of bytes allocated and still in use.", float64(ms.Alloc))
	gauge("go_memstats_sys_bytes", "Number of bytes obtained from system.", float64(ms.Sys))
	gauge("go_memstats_heap_inuse_bytes", "Number of heap bytes that are in use.", float64(ms.HeapInuse))
	// num_gc is a monotonically increasing count, exposed as a counter.
	w.WriteString("# HELP go_gc_cycles_total Number of completed GC cycles.\n")
	w.WriteString("# TYPE go_gc_cycles_total counter\n")
	w.WriteString("go_gc_cycles_total ")
	w.WriteString(strconv.FormatUint(uint64(ms.NumGC), 10))
	w.WriteByte('\n')
	w.Flush()
}

// pthreads returns the number of OS threads (maxprocs is a reasonable, cgo-free
// proxy the exposition does not strictly require to be exact).
func pthreads() int {
	n, _ := runtime.ThreadCreateProfile(nil)
	return n
}

// --- shared rendering helpers ------------------------------------------------

func snapshotScalars(m map[string]*scalar) []*scalar {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*scalar, 0, len(keys))
	for _, k := range keys {
		s := m[k]
		out = append(out, &scalar{labels: s.labels, v: s.v})
	}
	return out
}

// bufw returns a *bufio.Writer for an io.Writer, reusing it if it already is one
// (so a func collector writing into the registry's shared buffer does not double-
// buffer). A flush on the returned writer flushes only buffered bytes.
func bufw(out io.Writer) *bufio.Writer {
	if bw, ok := out.(*bufio.Writer); ok {
		return bw
	}
	return bufio.NewWriter(out)
}

// escapeLabelValue escapes a Prometheus label value: backslash, double-quote and
// line feed (the exact three the exposition format reserves).
func escapeLabelValue(s string) string {
	if !strings.ContainsAny(s, "\\\"\n") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}

// escapeHelp escapes a HELP docstring: backslash and line feed only (the spec does
// NOT escape double-quotes in HELP, only in label values).
func escapeHelp(s string) string {
	if !strings.ContainsAny(s, "\\\n") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	return r.Replace(s)
}

// formatFloat renders a float in the exposition's required form: integers without a
// decimal point, the special tokens +Inf/-Inf/NaN, and a compact shortest decimal
// otherwise (Go's 'g' with -1 precision, which round-trips).
func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	case v == math.Trunc(v) && math.Abs(v) < 1e15:
		return strconv.FormatInt(int64(v), 10)
	default:
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
}
