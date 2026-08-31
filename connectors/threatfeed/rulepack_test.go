// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package threatfeed

import (
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"
	"time"
)

func mustPack(t *testing.T, p RulePack, priv ed25519.PrivateKey) ([]byte, []byte) {
	t.Helper()
	if p.IssuedAt == "" {
		p.IssuedAt = "2026-07-09T00:00:00Z"
	}
	b, err := MarshalRulePack(p)
	if err != nil {
		t.Fatal(err)
	}
	return b, SignRulePack(b, priv)
}

func TestRulePackSignVerifyTampered(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	b, sig := mustPack(t, RulePack{Version: 1, BlockedMCP: []string{"evil-mcp"}}, priv)

	if _, err := VerifyRulePack(b, sig, []ed25519.PublicKey{pub}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Deny-closed with no trusted keys.
	if _, err := VerifyRulePack(b, sig, nil); err == nil {
		t.Fatal("zero trusted keys must fail deny-closed")
	}
	// Tampered body.
	bad := make([]byte, len(b))
	copy(bad, b)
	bad[len(bad)/2] ^= 0x01
	if _, err := VerifyRulePack(bad, sig, []ed25519.PublicKey{pub}); err == nil {
		t.Fatal("tampered pack must be rejected")
	}
	// Domain separation: the sig must NOT verify over the untagged bytes.
	if ed25519.Verify(pub, b, sig) {
		t.Fatal("rule-pack signature verified without the domain tag (cross-protocol replay)")
	}
}

func TestManagerApplyLookupRollback(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var events []RuleApplyEvent
	m := NewManager([]ed25519.PublicKey{pub}, WithAuditSink(func(e RuleApplyEvent) { events = append(events, e) }))

	// v1: blocks evil-mcp + a phishing domain + an injection pattern.
	b1, s1 := mustPack(t, RulePack{
		Version:    1,
		BlockedMCP: []string{"Evil-MCP"},
		Indicators: []Indicator{{Type: IndicatorDomain, Value: "bad.example", Severity: "HIGH"}},
		Patterns:   []Pattern{{ID: "inj-1", Match: "ignore previous instructions"}},
	}, priv)
	res, err := m.Apply(b1, s1)
	if err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	if !res.Applied || res.Version != 1 || res.Duration <= 0 {
		t.Fatalf("apply v1 result wrong: %+v", res)
	}
	// Lookups reflect v1 immediately (no restart).
	if !m.IsBlockedMCP("evil-mcp") {
		t.Fatal("blocked MCP not in effect after apply (case-insensitive)")
	}
	if _, ok := m.MatchIndicator(IndicatorDomain, "BAD.example"); !ok {
		t.Fatal("indicator not in effect after apply")
	}
	if hits := m.MatchPatterns("please Ignore Previous Instructions now"); len(hits) != 1 {
		t.Fatalf("pattern not matched, got %d hits", len(hits))
	}

	// v2: different rules — hot-swap.
	b2, s2 := mustPack(t, RulePack{Version: 2, BlockedMCP: []string{"other-mcp"}}, priv)
	if _, err := m.Apply(b2, s2); err != nil {
		t.Fatalf("apply v2: %v", err)
	}
	if m.IsBlockedMCP("evil-mcp") || !m.IsBlockedMCP("other-mcp") {
		t.Fatal("v2 did not replace v1's rules")
	}

	// Anti-rollback: re-applying v1 (or v2) is refused.
	if _, err := m.Apply(b1, s1); err == nil {
		t.Fatal("anti-rollback: applying an older version must fail")
	}
	if _, err := m.Apply(b2, s2); err == nil {
		t.Fatal("anti-rollback: re-applying the same version must fail")
	}

	// Rollback restores v1's rules.
	if err := m.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !m.IsBlockedMCP("evil-mcp") {
		t.Fatal("rollback did not restore v1's rules")
	}
	// A second rollback has nothing to restore (one level).
	if err := m.Rollback(); err == nil {
		t.Fatal("a second rollback should report nothing to restore")
	}

	// Audit fired for both applies + the rollback.
	if len(events) != 3 {
		t.Fatalf("expected 3 audit events (2 apply + 1 rollback), got %d", len(events))
	}
}

func TestManagerExpiry(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	fixed := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	m := NewManager([]ed25519.PublicKey{pub}, WithClock(func() time.Time { return fixed }))

	// Already expired at apply → refused.
	bExp, sExp := mustPack(t, RulePack{Version: 1, IssuedAt: "2026-07-08T00:00:00Z", ExpiresAt: "2026-07-09T00:00:00Z", BlockedMCP: []string{"x"}}, priv)
	if _, err := m.Apply(bExp, sExp); err == nil {
		t.Fatal("an already-expired pack must be refused on apply")
	}

	// Applied while valid, then time moves past expiry → lookups go silent (stale
	// pack never served) and Status reports expired.
	bOK, sOK := mustPack(t, RulePack{Version: 2, IssuedAt: "2026-07-09T00:00:00Z", ExpiresAt: "2026-07-10T00:00:00Z", BlockedMCP: []string{"y"}}, priv)
	if _, err := m.Apply(bOK, sOK); err != nil {
		t.Fatalf("apply valid pack: %v", err)
	}
	if !m.IsBlockedMCP("y") {
		t.Fatal("valid pack should be in effect")
	}
	fixed = time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC) // past expiry
	if m.IsBlockedMCP("y") {
		t.Fatal("an expired active pack must not be served")
	}
	if st := m.Status(); !st.Expired {
		t.Fatal("Status must report the active pack expired")
	}
}

// TestManagerConcurrentReadDuringApply exercises the RWMutex under -race: readers
// hammer lookups while a writer hot-swaps packs.
func TestManagerConcurrentReadDuringApply(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := NewManager([]ed25519.PublicKey{pub})
	b1, s1 := mustPack(t, RulePack{Version: 1, BlockedMCP: []string{"a"}}, priv)
	if _, err := m.Apply(b1, s1); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.IsBlockedMCP("a")
					_ = m.MatchPatterns("hello world")
					_, _ = m.Active()
				}
			}
		}()
	}
	for v := uint64(2); v < 60; v++ {
		b, s := mustPack(t, RulePack{Version: v, BlockedMCP: []string{"a"}}, priv)
		if _, err := m.Apply(b, s); err != nil {
			t.Fatalf("apply v%d: %v", v, err)
		}
	}
	close(stop)
	wg.Wait()
}
