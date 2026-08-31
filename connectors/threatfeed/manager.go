// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package threatfeed

// Manager applies signed rule-packs at runtime WITHOUT a restart: verify the
// signature against a pinned key → validate → anti-rollback + expiry → compile
// (RE2) → ATOMIC swap of the active pack → audit, keeping the previous pack so a bad
// pack can be rolled back instantly. Lookups (blocked MCP / indicator / pattern
// match) take a read lock and never block the swap for long, so a running engine
// serves the new rules the instant Apply returns. Stdlib only (no core import).

import (
	"crypto/ed25519"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ApplyResult reports the outcome of a hot rule-pack apply, including the MEASURED
// apply duration (the swap is O(pack size) to compile + O(1) to swap).
type ApplyResult struct {
	Applied         bool          `json:"applied"`
	Version         uint64        `json:"version"`
	PreviousVersion uint64        `json:"previous_version"`
	Indicators      int           `json:"indicators"`
	Patterns        int           `json:"patterns"`
	BlockedMCP      int           `json:"blocked_mcp"`
	Duration        time.Duration `json:"duration_ns"`
}

// RuleApplyEvent is emitted to the audit sink on every apply/rollback so the change
// is on the evidence ledger (the caller wires the sink to core/audit). It carries no
// secret and no rule content — only the version delta and outcome.
type RuleApplyEvent struct {
	Action          string // "rulepack.applied" | "rulepack.rolledback"
	Version         uint64
	PreviousVersion uint64
	Reason          string
}

// Manager holds the active (and previous, for rollback) compiled rule-pack.
type Manager struct {
	mu       sync.RWMutex
	active   *compiledPack
	previous *compiledPack
	trusted  []ed25519.PublicKey
	audit    func(RuleApplyEvent)
	now      func() time.Time
}

// Option configures a Manager.
type Option func(*Manager)

// WithAuditSink wires an audit callback invoked (outside the write lock) on each
// apply/rollback.
func WithAuditSink(fn func(RuleApplyEvent)) Option { return func(m *Manager) { m.audit = fn } }

// WithClock injects the clock used for EXPIRY decisions (tests). The apply-duration
// measurement always uses the real monotonic clock.
func WithClock(now func() time.Time) Option { return func(m *Manager) { m.now = now } }

// NewManager builds a manager that trusts the given publisher keys. With zero keys,
// Apply always fails deny-closed (the engine runs on its compiled-in base only).
func NewManager(trusted []ed25519.PublicKey, opts ...Option) *Manager {
	m := &Manager{trusted: trusted, now: time.Now}
	for _, o := range opts {
		o(m)
	}
	return m
}

type compiledPack struct {
	pack       RulePack
	mcp        map[string]bool      // lowercased blocked MCP names/urls
	indicators map[string]Indicator // "type|lower(value)"
	regexes    []compiledPattern
	substrs    []Pattern
}

type compiledPattern struct {
	pat *regexp.Regexp
	src Pattern
}

func compile(p RulePack) (*compiledPack, error) {
	cp := &compiledPack{
		pack:       p,
		mcp:        make(map[string]bool, len(p.BlockedMCP)),
		indicators: make(map[string]Indicator, len(p.Indicators)),
	}
	for _, s := range p.BlockedMCP {
		cp.mcp[strings.ToLower(strings.TrimSpace(s))] = true
	}
	for _, ind := range p.Indicators {
		cp.indicators[indicatorKey(ind.Type, ind.Value)] = ind
	}
	for _, pat := range p.Patterns {
		if pat.Regex {
			re, err := regexp.Compile(pat.Match)
			if err != nil {
				return nil, fmt.Errorf("threatfeed: pattern %q is not a valid regexp: %w", pat.ID, err)
			}
			cp.regexes = append(cp.regexes, compiledPattern{pat: re, src: pat})
		} else {
			cp.substrs = append(cp.substrs, pat)
		}
	}
	return cp, nil
}

func indicatorKey(typ, value string) string {
	return strings.ToLower(strings.TrimSpace(typ)) + "|" + strings.ToLower(strings.TrimSpace(value))
}

// Apply verifies, validates and atomically installs a new rule-pack, returning the
// measured apply duration. It refuses a pack that does not verify, is expired, or
// whose version is not strictly greater than the active one (anti-rollback). The
// previous pack is retained for Rollback.
func (m *Manager) Apply(packJSON, sig []byte) (ApplyResult, error) {
	start := time.Now()
	p, err := VerifyRulePack(packJSON, sig, m.trusted)
	if err != nil {
		return ApplyResult{}, err
	}
	m.mu.RLock()
	cur := m.active
	m.mu.RUnlock()
	if cur != nil && p.Version <= cur.pack.Version {
		return ApplyResult{}, fmt.Errorf("threatfeed: anti-rollback — pack version %d is not newer than the active %d", p.Version, cur.pack.Version)
	}
	if p.ExpiredAt(m.now()) {
		return ApplyResult{}, fmt.Errorf("threatfeed: refusing an already-expired rule-pack (expires_at %s)", p.ExpiresAt)
	}
	cp, err := compile(p)
	if err != nil {
		return ApplyResult{}, err
	}

	m.mu.Lock()
	prevVersion := uint64(0)
	if m.active != nil {
		prevVersion = m.active.pack.Version
	}
	m.previous = m.active
	m.active = cp
	m.mu.Unlock()

	res := ApplyResult{
		Applied: true, Version: p.Version, PreviousVersion: prevVersion,
		Indicators: len(p.Indicators), Patterns: len(p.Patterns), BlockedMCP: len(p.BlockedMCP),
		Duration: time.Since(start),
	}
	m.emit(RuleApplyEvent{Action: "rulepack.applied", Version: p.Version, PreviousVersion: prevVersion})
	return res, nil
}

// Rollback restores the previous pack (one level). It is the instant undo for a pack
// that verified+applied but proved bad in practice.
func (m *Manager) Rollback() error {
	m.mu.Lock()
	if m.previous == nil {
		m.mu.Unlock()
		return fmt.Errorf("threatfeed: no previous rule-pack to roll back to")
	}
	restored := m.previous.pack.Version
	bad := uint64(0)
	if m.active != nil {
		bad = m.active.pack.Version
	}
	m.active, m.previous = m.previous, nil
	m.mu.Unlock()
	m.emit(RuleApplyEvent{Action: "rulepack.rolledback", Version: restored, PreviousVersion: bad, Reason: "operator rollback"})
	return nil
}

func (m *Manager) emit(ev RuleApplyEvent) {
	if m.audit != nil {
		m.audit(ev)
	}
}

// Active returns a copy of the active rule-pack and whether one is installed.
func (m *Manager) Active() (RulePack, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.active == nil {
		return RulePack{}, false
	}
	return m.active.pack, true
}

// ManagerStatus is the minimal-data summary for the console/CLI (no rule content).
type ManagerStatus struct {
	Loaded      bool   `json:"loaded"`
	Version     uint64 `json:"version"`
	IssuedAt    string `json:"issued_at,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Expired     bool   `json:"expired"`
	Indicators  int    `json:"indicators"`
	Patterns    int    `json:"patterns"`
	BlockedMCP  int    `json:"blocked_mcp"`
	TrustedKeys int    `json:"trusted_keys"`
	CanRollback bool   `json:"can_rollback"`
}

// Status summarizes the manager for status rendering.
func (m *Manager) Status() ManagerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := ManagerStatus{TrustedKeys: len(m.trusted), CanRollback: m.previous != nil}
	if m.active != nil {
		p := m.active.pack
		st.Loaded = true
		st.Version = p.Version
		st.IssuedAt = p.IssuedAt
		st.ExpiresAt = p.ExpiresAt
		st.Expired = p.ExpiredAt(m.now())
		st.Indicators = len(p.Indicators)
		st.Patterns = len(p.Patterns)
		st.BlockedMCP = len(p.BlockedMCP)
	}
	return st
}

// --- lookups (read-locked; safe during an Apply swap) -----------------------

// activeUsable returns the active compiled pack if it is loaded and not expired.
func (m *Manager) activeUsable() *compiledPack {
	if m.active == nil || m.active.pack.ExpiredAt(m.now()) {
		return nil
	}
	return m.active
}

// IsBlockedMCP reports whether an MCP server (by name or URL) is on the deny-list.
func (m *Manager) IsBlockedMCP(nameOrURL string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := m.activeUsable()
	if cp == nil {
		return false
	}
	return cp.mcp[strings.ToLower(strings.TrimSpace(nameOrURL))]
}

// MatchIndicator returns the matching deny-list indicator, if any.
func (m *Manager) MatchIndicator(typ, value string) (Indicator, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := m.activeUsable()
	if cp == nil {
		return Indicator{}, false
	}
	ind, ok := cp.indicators[indicatorKey(typ, value)]
	return ind, ok
}

// MatchPatterns returns every agentic-attack pattern that hits text (substring,
// case-insensitive; or RE2 for Regex patterns).
func (m *Manager) MatchPatterns(text string) []Pattern {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := m.activeUsable()
	if cp == nil {
		return nil
	}
	var hits []Pattern
	lower := strings.ToLower(text)
	for _, p := range cp.substrs {
		if strings.Contains(lower, strings.ToLower(p.Match)) {
			hits = append(hits, p)
		}
	}
	for _, rp := range cp.regexes {
		if rp.pat.MatchString(text) {
			hits = append(hits, rp.src)
		}
	}
	return hits
}
