// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestMinCacheablePrefixTokens(t *testing.T) {
	cases := []struct {
		model   string
		gw      model.Gateway
		wantMin int
		wantOK  bool
	}{
		// First-party surface (verified live table, jun-2026): newest models cache SMALLER.
		{"claude-opus-4-8", model.GatewayDirect, 1024, true},
		{"claude-opus-4-7", model.GatewayDirect, 2048, true},
		{"claude-opus-4-6", model.GatewayDirect, 4096, true},
		{"claude-opus-4-5", model.GatewayDirect, 4096, true},
		{"claude-sonnet-4-6", model.GatewayDirect, 1024, true},
		{"claude-haiku-4-5", model.GatewayDirect, 4096, true},
		{"claude-fable-5", model.GatewayDirect, 512, true},
		{"claude-mythos-5", model.GatewayVertex, 512, true},
		{"claude-opus-4-8", model.GatewayFoundry, 1024, true}, // Foundry uses first-party numbers
		{"claude-opus-4-8", "", 1024, true},                   // empty gateway = first-party
		// Amazon Bedrock: only Fable/Mythos published (1024); the rest are unknown.
		{"claude-fable-5", model.GatewayBedrockMantle, 1024, true},
		{"claude-mythos-5", model.GatewayBedrockMantle, 1024, true},
		{"claude-opus-4-8", model.GatewayBedrockMantle, 0, false},
		// Unknown / unverified model → fail-closed.
		{"some-unknown-model", model.GatewayDirect, 0, false},
		{"claude-mythos-preview", model.GatewayDirect, 2048, true},
	}
	for _, c := range cases {
		min, ok := MinCacheablePrefixTokens(c.model, c.gw)
		if min != c.wantMin || ok != c.wantOK {
			t.Errorf("MinCacheablePrefixTokens(%q, %q) = (%d, %v), want (%d, %v)",
				c.model, c.gw, min, ok, c.wantMin, c.wantOK)
		}
	}
}

func TestCacheMinimumSignal(t *testing.T) {
	inf := newInf(&routeDoer{}, model.GatewayDirect)
	at := time.Unix(0, 0).UTC()

	// Opus 4.8 minimum is 1024; a 500-token prefix is below it → advise.
	f, ok := inf.CacheMinimumSignal("claude-opus-4-8", 500, "sess-1", at)
	if !ok {
		t.Fatalf("expected advisory for a 500-token prefix below the 1024 minimum")
	}
	if f.Severity != model.SeverityInfo || f.SubjectKind != subjectCacheMinimum {
		t.Errorf("finding shape wrong: %+v", f)
	}
	if !strings.Contains(f.Title, "1024") || !strings.Contains(f.Title, "will not cache") {
		t.Errorf("title = %q", f.Title)
	}

	// At/above the minimum → no advisory.
	if _, ok := inf.CacheMinimumSignal("claude-opus-4-8", 1024, "s", at); ok {
		t.Errorf("prefix == minimum must not advise")
	}
	if _, ok := inf.CacheMinimumSignal("claude-opus-4-8", 5000, "s", at); ok {
		t.Errorf("prefix above minimum must not advise")
	}
	// Unverified threshold → never advise (fail-closed).
	if _, ok := inf.CacheMinimumSignal("claude-opus-4-8", 500, "s", time.Time{}); !ok {
		t.Errorf("a zero time should still advise (clock fallback)")
	}
	if _, ok := inf.CacheMinimumSignal("unknown-model", 1, "s", at); ok {
		t.Errorf("unknown threshold must not advise")
	}
}

func TestCacheableAdvisory(t *testing.T) {
	// A small cached prefix on Opus 4.8 (1024 min) → advisory.
	d := &routeDoer{routes: map[string]string{"POST /v1/messages/count_tokens": `{"input_tokens":300}`}}
	inf := newInf(d, model.GatewayDirect)
	req := MessageRequest{
		System:   []ContentBlock{CachedTextBlock("short stable prefix", "")},
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}
	f, ok, err := inf.CacheableAdvisory(context.Background(), req, "sess-9", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("CacheableAdvisory: %v", err)
	}
	if !ok {
		t.Fatalf("expected an advisory for a 300-token cached prefix below 1024")
	}
	if f.SubjectKind != subjectCacheMinimum {
		t.Errorf("subject = %q", f.SubjectKind)
	}

	// No cache breakpoint → no advisory, and NO count call is made.
	d2 := &routeDoer{routes: map[string]string{"POST /v1/messages/count_tokens": `{"input_tokens":1}`}}
	inf2 := newInf(d2, model.GatewayDirect)
	_, ok, err = inf2.CacheableAdvisory(context.Background(), MessageRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}, "s", time.Time{})
	if err != nil || ok {
		t.Errorf("no-breakpoint request must not advise (ok=%v err=%v)", ok, err)
	}
	if d2.lastURL != "" {
		t.Errorf("count_tokens should not be called when there is no breakpoint: %s", d2.lastURL)
	}

	// Prefix at/above the minimum → no advisory.
	d3 := &routeDoer{routes: map[string]string{"POST /v1/messages/count_tokens": `{"input_tokens":5000}`}}
	inf3 := newInf(d3, model.GatewayDirect)
	_, ok, err = inf3.CacheableAdvisory(context.Background(), req, "s", time.Time{})
	if err != nil || ok {
		t.Errorf("a large cached prefix must not advise (ok=%v err=%v)", ok, err)
	}

	// Unverified threshold (Opus 4.8 on Bedrock) → no advisory, no count call.
	d4 := &routeDoer{routes: map[string]string{"POST /v1/messages/count_tokens": `{"input_tokens":1}`}}
	inf4 := newInf(d4, model.GatewayBedrockMantle)
	_, ok, err = inf4.CacheableAdvisory(context.Background(), req, "s", time.Time{})
	if err != nil || ok {
		t.Errorf("unverified Bedrock threshold must not advise (ok=%v err=%v)", ok, err)
	}
	if d4.lastURL != "" {
		t.Errorf("no count_tokens call when threshold is unverified: %s", d4.lastURL)
	}
}
