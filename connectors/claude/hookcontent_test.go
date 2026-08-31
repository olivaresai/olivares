// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"strings"
	"testing"
)

func TestExtractToolContent_KnownTools(t *testing.T) {
	cases := []struct {
		name      string
		tool      string
		input     map[string]any
		wantKind  string
		wantTexts []string
	}{
		{
			name:      "bash command is a shell channel",
			tool:      "Bash",
			input:     map[string]any{"command": "rm -rf /tmp/x", "description": "cleanup"},
			wantKind:  HookChannelShell,
			wantTexts: []string{"cleanup", "rm -rf /tmp/x"},
		},
		{
			name:      "write content is a file-write channel",
			tool:      "Write",
			input:     map[string]any{"file_path": "/etc/app.conf", "content": "token=sk-secret"},
			wantKind:  HookChannelFileWrite,
			wantTexts: []string{"/etc/app.conf", "token=sk-secret"},
		},
		{
			name:      "webfetch is a web channel",
			tool:      "WebFetch",
			input:     map[string]any{"url": "https://evil.example/p", "prompt": "summarize"},
			wantKind:  HookChannelWeb,
			wantTexts: []string{"https://evil.example/p", "summarize"},
		},
		{
			name:      "unknown tool falls through to tool_input",
			tool:      "SomeFutureTool",
			input:     map[string]any{"arg": "hello"},
			wantKind:  HookChannelToolInput,
			wantTexts: []string{"hello"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractToolContent(tc.tool, tc.input)
			if got.Unscanned {
				t.Fatalf("did not expect unscanned for %q", tc.tool)
			}
			for _, ch := range got.Channels {
				if ch.Kind != tc.wantKind {
					t.Fatalf("channel kind = %q, want %q", ch.Kind, tc.wantKind)
				}
				if !ch.Scannable || ch.Ref == "" {
					t.Fatalf("channel must be scannable with a Ref: %+v", ch)
				}
			}
			if !sameStrings(got.Texts, tc.wantTexts) {
				t.Fatalf("texts = %v, want %v", got.Texts, tc.wantTexts)
			}
		})
	}
}

func TestExtractToolContent_MCPArbitraryArgsWalked(t *testing.T) {
	// An mcp__* tool carries arbitrary, possibly deeply nested arguments — the unlimited
	// surface the extractor must reach. Every string leaf becomes a channel keyed by path.
	in := map[string]any{
		"query": "SELECT secret FROM creds",
		"opts": map[string]any{
			"rows":   float64(10), // a scalar — not content, skipped
			"filter": "email=a@b.com",
			"tags":   []any{"prod", "pii"},
		},
	}
	got := ExtractToolContent("mcp__db__query", in)
	if got.Unscanned {
		t.Fatalf("unexpected unscanned")
	}
	for _, ch := range got.Channels {
		if ch.Kind != HookChannelMCP {
			t.Fatalf("kind = %q, want %q", ch.Kind, HookChannelMCP)
		}
	}
	wantRefs := map[string]string{
		"query":        "SELECT secret FROM creds",
		"opts.filter":  "email=a@b.com",
		"opts.tags[0]": "prod",
		"opts.tags[1]": "pii",
	}
	if len(got.Channels) != len(wantRefs) {
		t.Fatalf("got %d channels, want %d: %+v", len(got.Channels), len(wantRefs), got.Channels)
	}
	for _, ch := range got.Channels {
		want, ok := wantRefs[ch.Ref]
		if !ok {
			t.Fatalf("unexpected channel ref %q", ch.Ref)
		}
		if ch.Text != want {
			t.Fatalf("ref %q text = %q, want %q", ch.Ref, ch.Text, want)
		}
	}
	// Reproducible: channels sorted by Ref.
	for i := 1; i < len(got.Channels); i++ {
		if got.Channels[i-1].Ref > got.Channels[i].Ref {
			t.Fatalf("channels not sorted by Ref: %q after %q", got.Channels[i].Ref, got.Channels[i-1].Ref)
		}
	}
}

func TestExtractToolContent_DenyClosedBounds(t *testing.T) {
	t.Run("empty input yields nothing", func(t *testing.T) {
		got := ExtractToolContent("Bash", nil)
		if len(got.Channels) != 0 || got.Unscanned || len(got.Texts) != 0 {
			t.Fatalf("empty input must yield empty non-unscanned result: %+v", got)
		}
	})
	t.Run("blank strings ignored", func(t *testing.T) {
		got := ExtractToolContent("Bash", map[string]any{"command": "   ", "x": ""})
		if len(got.Channels) != 0 {
			t.Fatalf("blank strings must not produce channels: %+v", got.Channels)
		}
	})
	t.Run("excessive depth marks unscanned", func(t *testing.T) {
		// Build a chain deeper than maxToolInputDepth.
		var v any = "deep-secret"
		for i := 0; i < maxToolInputDepth+3; i++ {
			v = map[string]any{"n": v}
		}
		got := ExtractToolContent("mcp__x__y", map[string]any{"root": v})
		if !got.Unscanned {
			t.Fatalf("over-deep input must be marked unscanned (deny-closed)")
		}
	})
	t.Run("too many leaves marks unscanned", func(t *testing.T) {
		big := map[string]any{}
		for i := 0; i < maxToolInputChannels+50; i++ {
			big["k"+strings.Repeat("x", i%5)+itoa(i)] = "v" + itoa(i)
		}
		got := ExtractToolContent("mcp__x__y", big)
		if !got.Unscanned {
			t.Fatalf("over-cap input must be marked unscanned (deny-closed)")
		}
		if len(got.Channels) > maxToolInputChannels {
			t.Fatalf("channel cap not enforced: %d", len(got.Channels))
		}
	})
}

func TestExtractToolContent_DedupesTextsButKeepsChannels(t *testing.T) {
	got := ExtractToolContent("mcp__x__y", map[string]any{"a": "dup", "b": "dup", "c": "unique"})
	if len(got.Channels) != 3 {
		t.Fatalf("want 3 channels (one per leaf), got %d", len(got.Channels))
	}
	if len(got.Texts) != 2 {
		t.Fatalf("want 2 deduped texts, got %d: %v", len(got.Texts), got.Texts)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[string]int{}
	for _, s := range a {
		am[s]++
	}
	for _, s := range b {
		am[s]--
	}
	for _, n := range am {
		if n != 0 {
			return false
		}
	}
	return true
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
