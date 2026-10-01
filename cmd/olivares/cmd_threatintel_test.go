// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/threatfeed"
)

// TestPrintFeedStatusStripsFeedURLUserinfo is the H-05 regression test (26.10.x
// security backlog) for the public render funnel: the threat-feed status may
// carry an operator-supplied FeedURL with userinfo credentials, and the CLI must
// never print them. Before the fix printFeedStatus rendered the value verbatim.
func TestPrintFeedStatusStripsFeedURLUserinfo(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	st := threatfeed.FeedStatus{
		Enabled:     true,
		TrustedKeys: 1,
		FeedURL:     "https://feed-operator:s3cret-pass@feeds.example.com/threat/feed.json",
	}
	if err := printFeedStatus(cmd, st); err != nil {
		t.Fatalf("printFeedStatus: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "s3cret-pass") || strings.Contains(out, "feed-operator") {
		t.Errorf("status output must not render URL userinfo: %s", out)
	}
	if !strings.Contains(out, "https://feeds.example.com/threat/feed.json") {
		t.Errorf("status output must keep the userinfo-free URL: %s", out)
	}
}

// TestPrintFeedStatusKeepsUserinfoFreeURL pins the no-behavior-change rule: a
// userinfo-free FeedURL renders byte-identical, and an empty one stays absent.
func TestPrintFeedStatusKeepsUserinfoFreeURL(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	st := threatfeed.FeedStatus{Enabled: true, FeedURL: "https://feeds.example.com/feed.json"}
	if err := printFeedStatus(cmd, st); err != nil {
		t.Fatalf("printFeedStatus: %v", err)
	}
	if !strings.Contains(buf.String(), `"feed_url": "https://feeds.example.com/feed.json"`) {
		t.Errorf("userinfo-free FeedURL must render unchanged: %s", buf.String())
	}

	buf.Reset()
	if err := printFeedStatus(cmd, threatfeed.FeedStatus{Enabled: true}); err != nil {
		t.Fatalf("printFeedStatus: %v", err)
	}
	if strings.Contains(buf.String(), "feed_url") {
		t.Errorf("empty FeedURL must stay omitted: %s", buf.String())
	}
}
