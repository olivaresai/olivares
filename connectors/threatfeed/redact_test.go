// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package threatfeed

import (
	"strings"
	"testing"
)

// TestStripURLUserinfo is the H-05 regression test (26.10.x security backlog):
// an operator-supplied feed URL rendered in diagnostics/status must never carry
// userinfo credentials. Before the fix nothing enforced the FeedURL contract
// comment; the value rendered verbatim.
func TestStripURLUserinfo(t *testing.T) {
	t.Run("strips user and password", func(t *testing.T) {
		got := StripURLUserinfo("https://feed-operator:s3cret-pass@feeds.example.com/threat/feed.json")
		if strings.Contains(got, "s3cret-pass") || strings.Contains(got, "feed-operator") {
			t.Errorf("rendered URL must not carry userinfo: %q", got)
		}
		if got != "https://feeds.example.com/threat/feed.json" {
			t.Errorf("host+path must survive the strip: %q", got)
		}
	})
	t.Run("strips opaque token userinfo", func(t *testing.T) {
		got := StripURLUserinfo("https://tok%40abc@feeds.example.com/v1")
		if strings.Contains(got, "tok") {
			t.Errorf("rendered URL must not carry the token userinfo: %q", got)
		}
		if got != "https://feeds.example.com/v1" {
			t.Errorf("host+path must survive the strip: %q", got)
		}
	})
	t.Run("userinfo-free value is byte-identical", func(t *testing.T) {
		const raw = "https://feeds.example.com/threat/feed.json?channel=a,b#frag"
		if got := StripURLUserinfo(raw); got != raw {
			t.Errorf("well-formed value must render unchanged: got %q want %q", got, raw)
		}
	})
	t.Run("empty stays empty", func(t *testing.T) {
		if got := StripURLUserinfo(""); got != "" {
			t.Errorf("empty FeedURL must stay empty (omitempty): %q", got)
		}
	})
	t.Run("unparsable value is replaced, never echoed", func(t *testing.T) {
		const hostile = "https://user:s3cret@feeds.example.com/\x7f\x00bad"
		got := StripURLUserinfo(hostile)
		if strings.Contains(got, "s3cret") || strings.Contains(got, "feeds.example.com") {
			t.Errorf("unparsable value must not be echoed at all: %q", got)
		}
		if got != unparsableURLMarker {
			t.Errorf("unparsable value must become the fixed marker: %q", got)
		}
	})
}

// TestFeedStatusRedacted covers the DTO method the renderers call: only FeedURL
// changes, every other field is passed through.
func TestFeedStatusRedacted(t *testing.T) {
	st := FeedStatus{
		Enabled:     true,
		TrustedKeys: 1,
		FeedURL:     "https://ops:s3cret@feeds.example.com/feed.json",
		FeedVersion: 7,
	}
	got := st.Redacted()
	if strings.Contains(got.FeedURL, "s3cret") {
		t.Errorf("Redacted FeedURL must not carry userinfo: %q", got.FeedURL)
	}
	if got.FeedURL != "https://feeds.example.com/feed.json" {
		t.Errorf("Redacted FeedURL = %q", got.FeedURL)
	}
	if got.FeedVersion != 7 || !got.Enabled || got.TrustedKeys != 1 {
		t.Errorf("Redacted must not touch other fields: %+v", got)
	}
}
