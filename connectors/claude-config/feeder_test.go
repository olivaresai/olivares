// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// capturingSink collects the observations a source emits.
type capturingSink struct{ edges []model.EdgeObservation }

func (s *capturingSink) Emit(_ context.Context, obs model.Observation) error {
	if e, ok := obs.(model.EdgeObservation); ok {
		s.edges = append(s.edges, e)
	}
	return nil
}

// bodySecret is planted in a subagent's BODY (never its frontmatter) to prove the
// feeder reads structural metadata only and never emits a prompt body.
const bodySecret = "sk-ant-SECRET-DO-NOT-LEAK-abcdef0123456789"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureTree builds a representative Claude config tree under a temp root and returns it.
func fixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// A subagent: identity from frontmatter `name`; the BODY carries a secret that must
	// NEVER be emitted.
	writeFile(t, filepath.Join(root, ".claude", "agents", "reviewer.md"),
		"---\nname: code-reviewer\ndescription: Reviews code\ntools: Read, Grep\nmodel: opus\n---\n\nYou are a reviewer. Internal credential: "+bodySecret+"\n")
	// A Skill: identity is the DIRECTORY name (deploy), not the display name.
	writeFile(t, filepath.Join(root, ".claude", "skills", "deploy", "SKILL.md"),
		"---\nname: Deploy Helper\ndescription: Ship it\nallowed-tools: Bash\n---\n\nDeploy steps: "+bodySecret+"\n")
	// A legacy command (merged into skills): identity is the filename stem.
	writeFile(t, filepath.Join(root, ".claude", "commands", "release.md"), "Cut a release.\n")
	// An output-style: identity from frontmatter `name`.
	writeFile(t, filepath.Join(root, ".claude", "output-styles", "diagrams.md"),
		"---\nname: Diagrams first\ndescription: Lead with a diagram\nkeep-coding-instructions: true\n---\n\nStart with a mermaid diagram.\n")
	// A marketplace catalog listing two plugins.
	writeFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"),
		`{"name":"team-market","plugins":[{"name":"mp-one","source":"./mp-one"},{"name":"mp-two","source":"./mp-two"}]}`)
	// A plugin with its own bundled skill.
	writeFile(t, filepath.Join(root, "myplugin", ".claude-plugin", "plugin.json"),
		`{"name":"myplugin","description":"A plugin","version":"1.0.0"}`)
	writeFile(t, filepath.Join(root, "myplugin", "skills", "hello", "SKILL.md"),
		"---\ndescription: greet\n---\n\nGreet the user.\n")
	// settings-declared hooks. The COMMAND strings carry a secret that must
	// never be emitted (only the event-name keys are capabilities); the empty
	// PreCompact list declares nothing. settings.local.json adds another event.
	writeFile(t, filepath.Join(root, ".claude", "settings.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/usr/local/bin/pep --token `+bodySecret+`"}]}],"MessageDisplay":[{"hooks":[{"type":"command","command":"/usr/local/bin/scrub"}]}],"PreCompact":[]}}`)
	writeFile(t, filepath.Join(root, ".claude", "settings.local.json"),
		`{"hooks":{"Stop":[{"hooks":[{"type":"prompt","prompt":"check open findings"}]}]}}`)
	// project-declared MCP servers — names only; the entry bodies carry
	// credential-shaped material that must never be emitted.
	writeFile(t, filepath.Join(root, ".mcp.json"),
		`{"mcpServers":{"github":{"command":"npx","args":["-y","gh-mcp"],"env":{"GITHUB_TOKEN":"`+bodySecret+`"}},"jira":{"url":"https://mcp.example/jira"}}}`)
	return root
}

// gather runs the feeder once over root and returns the captured edges.
func gather(t *testing.T, root string) []model.EdgeObservation {
	t.Helper()
	f := New()
	if err := f.Open(context.Background(), cfg(root)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &capturingSink{}
	if err := f.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.edges
}

// cfg builds the feeder's operator config (the directory to scan).
func cfg(root string) sdk.Config { return sdk.Config{Settings: map[string]string{"root": root}} }

func TestFeederDiscoversDeclaredCapabilities(t *testing.T) {
	root := fixtureTree(t)
	edges := gather(t, root)

	got := map[string]bool{}
	for _, e := range edges {
		// Every declared edge is config-sourced and originates from the workspace.
		if e.Source != model.SignalConfig {
			t.Errorf("edge %s/%s Source = %q, want config", e.ResourceKind, e.ResourceRef, e.Source)
		}
		if e.OriginKind != "workspace" {
			t.Errorf("edge %s/%s OriginKind = %q, want workspace", e.ResourceKind, e.ResourceRef, e.OriginKind)
		}
		// MINIMAL-DATA: the body secret must never appear in any emitted ref.
		if strings.Contains(e.ResourceRef, bodySecret) || strings.Contains(e.OriginRef, bodySecret) {
			t.Fatalf("SECRET LEAKED into an emitted edge: %s/%s", e.ResourceKind, e.ResourceRef)
		}
		got[e.ResourceKind+"/"+e.ResourceRef] = true
	}

	want := []string{
		"config.subagent/code-reviewer", // frontmatter name
		"config.skill/deploy",           // skill = directory name
		"config.skill/release",          // legacy command = filename stem
		"config.output_style/Diagrams first",
		"config.plugin/myplugin", // plugin.json
		"config.plugin/mp-one",   // marketplace entry
		"config.plugin/mp-two",   // marketplace entry
		"config.skill/hello",     // plugin-bundled skill
		// settings-declared hook events (the event-name key is the
		// capability; commands/prompts are never read beyond presence) and
		// project-declared MCP servers (names only).
		"config.hook/PreToolUse",
		"config.hook/MessageDisplay",
		"config.hook/Stop", // from settings.local.json
		"config.mcp_server/github",
		"config.mcp_server/jira",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing declared capability %q (got %v)", w, keys(got))
		}
	}
	// An EMPTY hook event list declares nothing.
	if got["config.hook/PreCompact"] {
		t.Error("an empty hook event list must not declare a capability")
	}
}

// TestFeederIdempotentReemission proves re-gathering the same tree yields the SAME set
// of declared capabilities (the reactor's (origin,capability) upsert then merges them).
func TestFeederIdempotentReemission(t *testing.T) {
	root := fixtureTree(t)
	first := keySet(gather(t, root))
	second := keySet(gather(t, root))
	if len(first) != len(second) {
		t.Fatalf("re-emission changed the capability count: %d vs %d", len(first), len(second))
	}
	for k := range first {
		if !second[k] {
			t.Errorf("re-emission dropped %q", k)
		}
	}
}

// TestFeederMissingRoot proves an unreadable root is an honest error, not a silent
// empty discovery.
func TestFeederMissingRoot(t *testing.T) {
	f := New()
	if err := f.Open(context.Background(), cfg("")); err == nil {
		t.Error("Open with empty root must error")
	}
	f2 := New()
	_ = f2.Open(context.Background(), cfg(filepath.Join(t.TempDir(), "does-not-exist")))
	if err := f2.Gather(context.Background(), &capturingSink{}); err == nil {
		t.Error("Gather over a missing root must error, not silently discover nothing")
	}
}

// TestSubagentToolListDecomposition: a subagent with `allowed-tools` in its
// frontmatter emits config.subagent_tool edges — each tool is a DISTINCT capability
// layer under the agent.
func TestSubagentToolListDecomposition(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".claude", "agents", "builder.md"),
		"---\nname: code-builder\ndescription: Builds code.\nallowed-tools: Bash Read Edit\n---\n\nBuild things.\n")
	writeFile(t, filepath.Join(root, ".claude", "agents", "reader.md"),
		"---\nname: code-reader\ndescription: Reads code.\n---\n\nRead code.\n")

	edges := gather(t, root)
	got := keySet(edges)

	// code-builder should have the parent edge AND per-tool edges.
	wantEdges := []string{
		"config.subagent/code-builder",
		"config.subagent_tool/code-builder:Bash",
		"config.subagent_tool/code-builder:Read",
		"config.subagent_tool/code-builder:Edit",
		"config.subagent/code-reader",
	}
	for _, w := range wantEdges {
		if !got[w] {
			t.Errorf("missing edge %q (got %v)", w, keys(got))
		}
	}

	// code-reader (no allowed-tools) should NOT have subagent_tool edges.
	for k := range got {
		if strings.HasPrefix(k, "config.subagent_tool/code-reader:") {
			t.Errorf("agent without allowed-tools must not emit tool edges, got %q", k)
		}
	}
}

// TestSubagentToolListYAMLArray: allowed-tools as a YAML list (not space-separated
// string) also produces tool edges.
func TestSubagentToolListYAMLArray(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".claude", "agents", "multi.md"),
		"---\nname: multi-tool\nallowed-tools:\n  - Bash\n  - WebFetch\n---\n\nDo things.\n")

	edges := gather(t, root)
	got := keySet(edges)

	for _, w := range []string{
		"config.subagent_tool/multi-tool:Bash",
		"config.subagent_tool/multi-tool:WebFetch",
	} {
		if !got[w] {
			t.Errorf("missing edge %q from YAML-list form", w)
		}
	}
}

// TestSubagentToolListMinimalData: a subagent body secret must never appear in any
// emitted tool edge ref.
func TestSubagentToolListMinimalData(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".claude", "agents", "leaky.md"),
		"---\nname: leaky-agent\nallowed-tools: Read\n---\n\nSecret: "+bodySecret+"\n")

	edges := gather(t, root)
	for _, e := range edges {
		if strings.Contains(e.ResourceRef, bodySecret) || strings.Contains(e.OriginRef, bodySecret) {
			t.Fatalf("SECRET LEAKED into subagent tool edge: %s/%s", e.ResourceKind, e.ResourceRef)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keySet(edges []model.EdgeObservation) map[string]bool {
	out := map[string]bool{}
	for _, e := range edges {
		out[e.ResourceKind+"/"+e.ResourceRef] = true
	}
	return out
}
