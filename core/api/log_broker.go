// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const defaultLogBufferSize = 10000

// LogEntry is a structured log entry for the log viewer SSE stream.
type LogEntry struct {
	Timestamp string         `json:"timestamp"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Module    string         `json:"module,omitempty"`
	Attrs     map[string]any `json:"attrs,omitempty"`
}

// LogFilter is the shared buffer/stream filter contract. Levels is an exact
// set and takes precedence over Min; Min preserves the legacy threshold query;
// leaving both empty passes every captured level. Module is a case-insensitive
// substring match.
type LogFilter struct {
	Levels []slog.Level
	Min    *slog.Level
	Module string
}

func (f LogFilter) matches(level slog.Level, module string) bool {
	if len(f.Levels) > 0 {
		found := false
		for _, allowed := range f.Levels {
			if level == allowed {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	} else if f.Min != nil && level < *f.Min {
		return false
	}
	return f.Module == "" ||
		strings.Contains(strings.ToLower(module), strings.ToLower(f.Module))
}

func (f LogFilter) clone() LogFilter {
	out := f
	out.Levels = append([]slog.Level(nil), f.Levels...)
	if f.Min != nil {
		minLevel := *f.Min
		out.Min = &minLevel
	}
	return out
}

// logRing is a fixed-capacity ring buffer of log entries, safe for concurrent use.
type logRing struct {
	mu    sync.Mutex
	buf   []LogEntry
	head  int
	count int
	cap   int
}

func newLogRing(capacity int) *logRing {
	if capacity <= 0 {
		capacity = defaultLogBufferSize
	}
	return &logRing{buf: make([]LogEntry, capacity), cap: capacity}
}

func (r *logRing) push(e LogEntry) {
	r.mu.Lock()
	r.buf[r.head] = e
	r.head = (r.head + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}
	r.mu.Unlock()
}

func (r *logRing) snapshot(filter LogFilter, limit int) ([]LogEntry, int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if limit <= 0 || limit > r.count {
		limit = r.count
	}

	// NEWEST-FIRST SELECTION, CHRONOLOGICAL ORDER (2026-08-06). This walked FORWARD from
	// the oldest entry and stopped once it had `limit`, so `limit=N` returned the N OLDEST
	// surviving entries. The one consumer is the console log viewer, which uses this as the
	// seed before it attaches the SSE stream (web/src/features/logs/logs-view.tsx) and then
	// takes `.slice(-MAX_ENTRIES)` — i.e. it already assumes the newest are at the END. So on
	// a ring of 10 000 with the default limit, an operator opening the viewer during an
	// incident was handed ancient history and lost precisely the errors immediately before
	// they connected, with no cursor to recover the gap and nothing saying a newer tail had
	// been dropped.
	//
	// Walking BACKWARDS from the newest and reversing keeps the returned page in
	// chronological order (what the consumer renders) while making it the newest N (what the
	// consumer needs). matched counts every entry the filter accepts across the WHOLE ring,
	// so the handler can report a total that is a total rather than a page size.
	result := make([]LogEntry, 0, limit)
	oldest := (r.head - r.count + r.cap) % r.cap

	matched := 0
	for i := r.count - 1; i >= 0; i-- {
		idx := (oldest + i) % r.cap
		e := r.buf[idx]
		if !filter.matches(levelFromString(e.Level), e.Module) {
			continue
		}
		matched++
		if len(result) < limit {
			result = append(result, e)
		}
	}
	for l, r2 := 0, len(result)-1; l < r2; l, r2 = l+1, r2-1 {
		result[l], result[r2] = result[r2], result[l]
	}
	return result, matched
}

// logPubSub fans out log entries to SSE subscribers.
type logPubSub struct {
	mu   sync.Mutex
	next int
	subs map[int]logSub
}

type logSub struct {
	ch     chan LogEntry
	filter LogFilter
}

func newLogPubSub() *logPubSub {
	return &logPubSub{subs: make(map[int]logSub)}
}

func (ps *logPubSub) subscribe(filter LogFilter) (<-chan LogEntry, func()) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	id := ps.next
	ps.next++
	ch := make(chan LogEntry, 64)
	ps.subs[id] = logSub{ch: ch, filter: filter.clone()}
	return ch, func() {
		ps.mu.Lock()
		defer ps.mu.Unlock()
		if _, ok := ps.subs[id]; ok {
			delete(ps.subs, id)
			close(ch)
		}
	}
}

func (ps *logPubSub) publish(e LogEntry, level slog.Level) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, sub := range ps.subs {
		if !sub.filter.matches(level, e.Module) {
			continue
		}
		select {
		case sub.ch <- e:
		default:
		}
	}
}

// LogBroker is a slog.Handler that captures log entries into a ring buffer and
// broadcasts them to SSE subscribers. It wraps an existing handler so all logs
// are still written to the original destination.
type LogBroker struct {
	inner        slog.Handler
	ring         *logRing
	pubsub       *logPubSub
	captureLevel *slog.LevelVar
	group        string
	attrs        []slog.Attr
	// redactor is the canonical credential catalog, injected by the composition
	// root (core must not import /modules — scripts/check-boundary.sh). It runs
	// AFTER the core-owned floor, never instead of it: see log_redact.go.
	redactor func(string) (string, int)
}

// LogBrokerOption configures a LogBroker at construction. It is variadic rather
// than a wider signature so an existing embedder keeps compiling — and, more to
// the point, keeps the floor: an embedder that passes no option is protected by
// logRedactFloor, not left unprotected.
type LogBrokerOption func(*LogBroker)

// WithLogRedactor supplies the canonical credential redactor. The composition
// root wires modules/security.RedactCredentials here; the returned count is
// ignored by the broker, which needs the text and not the tally.
func WithLogRedactor(redact func(string) (string, int)) LogBrokerOption {
	return func(b *LogBroker) { b.redactor = redact }
}

// NewLogBroker wraps an existing slog.Handler, capturing entries into a ring
// buffer of the given capacity and broadcasting to SSE subscribers. captureLevel
// is independent from the wrapped handler's own output level.
func NewLogBroker(inner slog.Handler, bufSize int, captureLevel *slog.LevelVar, opts ...LogBrokerOption) *LogBroker {
	if captureLevel == nil {
		captureLevel = &slog.LevelVar{}
	}
	b := &LogBroker{
		inner:        inner,
		ring:         newLogRing(bufSize),
		pubsub:       newLogPubSub(),
		captureLevel: captureLevel,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Enabled admits a record when EITHER side wants it: the console ring (capture
// level) or the wrapped terminal handler. The two floors are fully DECOUPLED —
// raising the capture level must never silence records the terminal would
// print, and lowering it to debug must never spam a terminal pinned at info
// (Handle re-checks each side before delivering).
func (b *LogBroker) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= b.CaptureLevel() || b.inner.Enabled(ctx, level)
}

func (b *LogBroker) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= b.CaptureLevel() {
		// EVERYTHING below this line is console-bound and therefore scrubbed
		// (log_redact.go). The wrapped handler at the bottom of this function
		// still gets the ORIGINAL record: the split is by surface, not by level.
		entry := LogEntry{
			Timestamp: r.Time.UTC().Format(time.RFC3339Nano),
			Level:     r.Level.String(),
			Message:   b.redactLogMessage(r.Message),
			Module:    b.scrubLogModule(b.group),
		}

		attrs := make(map[string]any)
		for _, a := range b.attrs {
			if a.Key == "module" {
				entry.Module = b.scrubLogModule(a.Value.String())
			} else {
				attrs[b.scrubLogKey(a.Key)] = b.scrubLogValue(a.Value)
			}
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "module" {
				entry.Module = b.scrubLogModule(a.Value.String())
			} else {
				attrs[b.scrubLogKey(a.Key)] = b.scrubLogValue(a.Value)
			}
			return true
		})
		if len(attrs) > 0 {
			entry.Attrs = attrs
		}

		b.ring.push(entry)
		b.pubsub.publish(entry, r.Level)
	}

	if b.inner.Enabled(ctx, r.Level) {
		return b.inner.Handle(ctx, r)
	}
	return nil
}

// WithAttrs and WithGroup must carry `redactor` forward. They are not an edge
// case: EVERY module logger is a derived handler (the composition root hands each
// module `log.With("module", name)`), so a derivation that dropped the redactor
// would leave the canonical catalog wired to a handler nothing logs through, with
// the floor quietly doing all the work and no test the wiser.
func (b *LogBroker) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogBroker{
		inner:        b.inner.WithAttrs(attrs),
		ring:         b.ring,
		pubsub:       b.pubsub,
		captureLevel: b.captureLevel,
		group:        b.group,
		attrs:        append(append([]slog.Attr{}, b.attrs...), attrs...),
		redactor:     b.redactor,
	}
}

func (b *LogBroker) WithGroup(name string) slog.Handler {
	return &LogBroker{
		inner:        b.inner.WithGroup(name),
		ring:         b.ring,
		pubsub:       b.pubsub,
		captureLevel: b.captureLevel,
		group:        name,
		attrs:        b.attrs,
		redactor:     b.redactor,
	}
}

// CaptureLevel returns the minimum level currently retained and published.
func (b *LogBroker) CaptureLevel() slog.Level {
	if b.captureLevel == nil {
		return slog.LevelInfo
	}
	return b.captureLevel.Level()
}

// Subscribe registers a subscriber for entries matching filter.
func (b *LogBroker) Subscribe(filter LogFilter) (<-chan LogEntry, func()) {
	return b.pubsub.subscribe(filter)
}

// Buffer returns the NEWEST `limit` entries matching filter, in chronological order,
// plus how many entries in the whole ring matched. The second value is what lets the
// handler answer "how many are there" separately from "how many did I send" — until
// 2026-08-06 it answered the second and called it the first.
func (b *LogBroker) Buffer(filter LogFilter, limit int) ([]LogEntry, int) {
	return b.ring.snapshot(filter, limit)
}

func levelFromString(s string) slog.Level {
	level, ok := namedLogLevel(s)
	if !ok {
		return slog.LevelInfo
	}
	return level
}

func namedLogLevel(s string) (slog.Level, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return slog.LevelDebug, true
	case "INFO":
		return slog.LevelInfo, true
	case "WARN":
		return slog.LevelWarn, true
	case "ERROR":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}
