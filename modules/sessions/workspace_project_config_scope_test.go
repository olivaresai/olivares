// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// An approval binds the content of a file the engine cannot parse too: one
// oversized file approved must not approve another, and a changed tail of
// invalid JSON must not keep an approval.
func TestProjectConfigApprovalBindsTheContentOfUnreadableFiles(t *testing.T) {
	big := func(fill byte) string { return strings.Repeat(string(fill), 1<<20+1) }
	for _, tc := range []struct {
		name, before, after string
	}{
		{"oversized", big('a'), big('b')},
		{"not json", `{"hooks": `, `{"hooks": 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProjectConfigFixture(t)
			profile := f.profile(providerDriverClaude)
			f.write(".claude/settings.json", tc.before)
			f.wantStart(profile, http.StatusConflict)
			hash := f.reportHash()
			if len(hash) != 64 {
				t.Fatalf("report: %s", f.report().raw)
			}
			if r := f.approve(hash); r.code != http.StatusOK {
				t.Fatalf("approve: %d %s", r.code, r.raw)
			}
			f.wantStart(profile, http.StatusCreated)
			f.write(".claude/settings.json", tc.after)
			f.wantStart(profile, http.StatusConflict)
		})
	}
}

// One hash covers the folder's flagged configuration of every tool; a change in
// the file of a tool that is not launching still voids it, and only the tool
// that reads a flagged file is refused.
func TestProjectConfigOneApprovalCoversTheWholeFolder(t *testing.T) {
	f := newProjectConfigFixture(t)
	claude, codex := f.profile(providerDriverClaude), f.profile(providerDriverCodex)
	f.write(".mcp.json", `{"mcpServers":{"a":{"command":"one"}}}`)
	f.write(".codex/config.toml", "notify = [\"one\"]\n")
	f.wantStart(claude, http.StatusConflict)
	f.wantStart(codex, http.StatusConflict)
	if r := f.approve(f.reportHash()); r.code != http.StatusOK {
		t.Fatalf("approve: %d %s", r.code, r.raw)
	}
	f.wantStart(claude, http.StatusCreated)
	f.wantStart(codex, http.StatusCreated)
	f.write(".codex/config.toml", "notify = [\"two\"]\n")
	f.wantStart(claude, http.StatusConflict)
	f.wantStart(codex, http.StatusConflict)
}

// A TOML value JSON cannot carry must not collapse the hash to a constant: two
// different files with a nan keep different hashes.
func TestProjectConfigHashSurvivesNonFiniteNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".codex")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	hashOf := func(command string) string {
		t.Helper()
		body := "[mcp_servers.x]\ncommand = \"" + command + "\"\npad = nan\n"
		if err := os.WriteFile(filepath.Join(path, "config.toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		entries, hash, err := readProjectConfig(dir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("read: %v %v", entries, err)
		}
		return hash
	}
	a, b := hashOf("/tmp/one"), hashOf("/tmp/two")
	if a == b || a == sha256Hex(nil) {
		t.Fatalf("hashes: %s %s", a, b)
	}
}

// An approval is complete only with who and when, and only for its own hash.
func TestProjectConfigApprovalCoversOnlyAMatchingCompleteRecord(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	full := &projectConfigApproval{SHA256: hash, Actor: "user:u1", At: "2026-10-14T09:00:00Z"}
	for _, tc := range []struct {
		name     string
		approval *projectConfigApproval
		hash     string
		want     bool
	}{
		{"complete and matching", full, hash, true},
		{"no approval", nil, hash, false},
		{"no content flagged", full, "", false},
		{"another hash", full, strings.Repeat("f", 64), false},
		{"without who", &projectConfigApproval{SHA256: hash, At: full.At}, hash, false},
		{"without when", &projectConfigApproval{SHA256: hash, Actor: full.Actor}, hash, false},
	} {
		if got := tc.approval.covers(tc.hash); got != tc.want {
			t.Errorf("%s: covers = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The directory read is the one the child runs in: a worktree holds the tracked
// files of its commit, so an untracked local file of the folder is not honored
// there and a file committed on the branch is.
func TestProjectConfigReadsTheWorktreeTheSessionRunsIn(t *testing.T) {
	h, ctx := newWorktreeHarness(t)
	start := func(worktree bool) error {
		_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(worktree))
		return err
	}
	want409 := func(label string, err error) {
		t.Helper()
		re, ok := err.(*runErr)
		if !ok || re.status != http.StatusConflict || !strings.Contains(re.msg, "approve-project-config") {
			t.Fatalf("%s: %v, want a 409 naming the approval step", label, err)
		}
	}
	// Untracked and local: the folder is refused, its worktree does not carry it.
	if err := os.MkdirAll(filepath.Join(h.repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(h.repo, ".claude", "settings.local.json")
	if err := os.WriteFile(local, []byte(`{"apiKeyHelper":"./key.sh"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	want409("folder", start(false))
	if err := start(true); err != nil {
		t.Fatalf("worktree without the untracked file: %v", err)
	}
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	// Tracked: the worktree carries it, and a refused start leaves no worktree.
	if err := os.WriteFile(filepath.Join(h.repo, ".mcp.json"), []byte(`{"mcpServers":{"a":{"command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, h.repo, "add", ".mcp.json")
	worktreeGitOut(t, h.repo, "commit", "-qm", "project mcp server")
	before := h.branches()
	want409("worktree", start(true))
	if after := h.branches(); after != before {
		t.Fatalf("a refused worktree start left a branch:\n%s\nwas:\n%s", after, before)
	}
	_, hash, err := readProjectConfig(h.repo)
	if err != nil || len(hash) != 64 {
		t.Fatalf("hash: %q %v", hash, err)
	}
	if _, err := h.m.approveProjectConfig(ctx, h.tenant, h.ws, hash, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	if err := start(true); err != nil {
		t.Fatalf("approved worktree start: %v", err)
	}
}

// A run with no registered folder keeps its own directory; nothing there can
// hold an approval, so flagged files in it are refused when it resumes.
func TestProjectConfigRefusesFlaggedFilesOfAnUnregisteredDirectoryOnResume(t *testing.T) {
	runner := &fakeRunner{initSID: "sess-own-dir"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	ctx := t.Context()
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "provider identity", func() bool {
		stored, err := m.getRun(ctx, tenant, run.RunRef)
		return err == nil && stored.ClaudeSessionID == "sess-own-dir"
	})
	if _, err := m.stopRun(ctx, tenant, run.RunRef, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	if run.WorkspacePath == "" {
		t.Fatal("the run reports no directory")
	}
	if err := os.WriteFile(filepath.Join(run.WorkspacePath, ".mcp.json"), []byte(`{"mcpServers":{"a":{"command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = m.resumeRun(ctx, tenant, run.RunRef, "user:u1", "user", "")
	re, ok := err.(*runErr)
	if !ok || re.status != http.StatusConflict || !strings.Contains(re.msg, "no registered folder") {
		t.Fatalf("resume: %v, want a 409 naming the unregistered directory", err)
	}
}

// Each rule row of the table, pinned by literal names so that deleting one is a
// red test: every key below must be flagged in its file, and the benign keys
// next to them must not be.
func TestProjectConfigEveryFlaggedKeyIsPinned(t *testing.T) {
	doc := func(keys ...string) map[string]any {
		out := map[string]any{"model": "m", "theme": "t", "permissions": map[string]any{"allow": []any{"Bash(ls)"}}}
		for _, key := range keys {
			out[key] = map[string]any{"x": "y"}
		}
		return out
	}
	flagged := func(path string, in map[string]any) []string {
		t.Helper()
		for _, f := range projectConfigFiles {
			if f.path == path {
				var got []string
				for key := range f.rules.flag(in) {
					got = append(got, key)
				}
				return got
			}
		}
		t.Fatalf("no rules for %s", path)
		return nil
	}
	for path, keys := range map[string][]string{
		".claude/settings.json":       {"hooks", "apiKeyHelper", "statusLine", "mcpServers", "enableAllProjectMcpServers", "enabledMcpjsonServers", "awsAuthRefresh", "awsCredentialExport", "otelHeadersHelper", "fileSuggestion", "enabledPlugins", "extraKnownMarketplaces"},
		".claude/settings.local.json": {"hooks", "apiKeyHelper", "statusLine", "mcpServers"},
		".mcp.json":                   {"mcpServers"},
		".codex/config.toml":          {"hooks", "notify", "mcp_servers", "model_providers", "otel", "openai_base_url", "chatgpt_base_url"},
		".gemini/settings.json":       {"hooks", "mcpServers", "toolDiscoveryCommand", "toolCallCommand", "proxy"},
		".cursor/cli.json":            {"permissions"},
		".cursor/mcp.json":            {"mcpServers"},
		".cursor/hooks.json":          {"hooks"},
	} {
		if got := flagged(path, doc(keys...)); len(got) != len(keys) {
			t.Errorf("%s: flagged %v of %v (benign keys must stay out)", path, got, keys)
		}
	}
	nested := map[string]any{"tools": map[string]any{"discoveryCommand": "d", "callCommand": "c", "sandbox": false}, "mcp": map[string]any{"serverCommand": "s"}, "telemetry": map[string]any{"otlpEndpoint": "http://h"}}
	if got := flagged(".gemini/settings.json", nested); len(got) != 4 {
		t.Errorf("gemini nested paths: %v", got)
	}
	for _, name := range []string{"ANTHROPIC_BASE_URL", "x_api_base", "HTTPS_PROXY", "NODE_OPTIONS", "LD_PRELOAD", "BASH_ENV", "PATH", "OTEL_EXPORTER_OTLP_ENDPOINT", "AWS_ENDPOINT_URL_S3", "GIT_SSH_COMMAND"} {
		if !flaggedEnvName(name) {
			t.Errorf("%s is not flagged", name)
		}
	}
	for _, name := range []string{"NO_PROXY", "HOME", "FOO", "ANTHROPIC_MODEL", "DEBUG"} {
		if flaggedEnvName(name) {
			t.Errorf("%s is flagged", name)
		}
	}
	// Values that configure nothing do not count.
	empty := map[string]any{"hooks": map[string]any{}, "statusLine": nil, "enableAllProjectMcpServers": false, "mcpServers": []any{}, "env": map[string]any{"ANTHROPIC_BASE_URL": ""}}
	if got := flagged(".claude/settings.json", empty); len(got) != 0 {
		t.Errorf("empty values flagged: %v", got)
	}
}

// Gemini loads KEY=value files as environment; its base URL is set there.
func TestProjectConfigFlagsGeminiDotenv(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# keys\nGEMINI_API_KEY=abc\nexport GOOGLE_GEMINI_BASE_URL=\"http://proxy.invalid\"\nHTTPS_PROXY='http://p'\nDEBUG=1\n"
	if err := os.WriteFile(filepath.Join(dir, ".gemini", ".env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, _, err := readProjectConfig(dir)
	if err != nil || len(entries) != 1 || entries[0].File != ".gemini/.env" {
		t.Fatalf("entries: %v %v", entries, err)
	}
	got := entries[0].Flagged
	if len(got) != 2 || got["env.GOOGLE_GEMINI_BASE_URL"] != "http://proxy.invalid" || got["env.HTTPS_PROXY"] != "http://p" {
		t.Fatalf("flagged: %v", got)
	}
}

// Nesting that would exhaust a recursive parser is flagged unread, not parsed,
// whatever string or comment syntax of the format precedes it: a quote that the
// format does not treat as a string start must not hide the brackets after it.
func TestProjectConfigDeepNestingIsFlaggedNotParsed(t *testing.T) {
	deep := strings.Repeat("[", 5000) + strings.Repeat("]", 5000)
	for _, tc := range []struct{ name, path, body string }{
		{"toml plain", ".codex/config.toml", "a = " + deep + "\n"},
		{"toml literal string holding a quote", ".codex/config.toml", "a = ['\"', " + deep + "]\n"},
		{"toml multi-line string ending in a quote", ".codex/config.toml", "a = [\"\"\"x\"\"\"\", " + deep + "]\n"},
		{"toml multi-line literal ending in a quote", ".codex/config.toml", "a = ['''x'''', " + deep + "]\n"},
		{"json comment holding a quote", ".claude/settings.json", `{"hooks": /* " */ ` + deep + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(tc.path)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.path), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			entries, _, err := readProjectConfig(dir)
			if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Unreadable, "nested deeper") {
				t.Fatalf("entries: %+v %v", entries, err)
			}
		})
	}
}

// A dotted key, a table header and an inline table nest the parsed document one
// level per segment with no bracket at all, and the TOML parser's work grows
// with the square of the segments (measured: 10,000 segments, 1.2 GB).
func TestProjectConfigLongKeyPathsAreFlaggedNotParsed(t *testing.T) {
	path := strings.Repeat("a.", 200) + "a"
	short := strings.Repeat("a.", 40) + "a"
	quoted := strings.Repeat("'x'.", 200) + "x"
	for _, tc := range []struct{ name, body string }{
		{"dotted key", path + " = 1\n"},
		{"quoted segments", quoted + " = 1\n"},
		{"table header", "[" + path + "]\nx = 1\n"},
		{"array of tables", "[[" + path + "]]\n"},
		{"inline table", "t = {" + path + " = 1}\n"},
		{"after a float", "x = 1.5\n" + path + " = 1\n"},
		// Each counter alone stays under the bound; the parsed document does not.
		{"dotted keys opening inline tables", nestedKeyTables(60, 60) + "\n"},
		{"dotted key opening deep inline tables", short + " = " + strings.Repeat("{t = ", 30) + "1" + strings.Repeat("}", 30) + "\n"},
		{"header over dotted keys", "[" + short + "]\n" + short + " = 1\n"},
		{"array header over dotted keys", "[[" + short + "]]\n" + short + " = 1\n"},
		{"header over inline tables", "[" + short + "]\nt = " + strings.Repeat("{t = ", 60) + "1" + strings.Repeat("}", 60) + "\n"},
		{"sibling tables near the size cap", siblingTables(64, 64)},
		{"keys under nested arrays of tables", arrayTables(16) + strings.Repeat("b.", 47) + "b = 1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			entries, _, err := readProjectConfig(dir)
			runtime.ReadMemStats(&after)
			if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Unreadable, "nested deeper") {
				t.Fatalf("entries: %+v %v", entries, err)
			}
			if got := after.TotalAlloc - before.TotalAlloc; got > 64<<20 {
				t.Fatalf("a file refused unparsed allocated %d bytes", got)
			}
		})
	}
}

// The bound is one sum of key segments, header segments and brackets: a file
// that reaches exactly maxProjectConfigDepth is read, one level more is not.
func TestProjectConfigDepthBoundIsOneSum(t *testing.T) {
	segments := func(n int) string { return strings.Repeat("a.", n-1) + "a" }
	header := "[" + segments(30) + "]\n"
	for _, tc := range []struct {
		name string
		body func(extra int) string
	}{
		{"brackets", func(extra int) string {
			return "v = " + strings.Repeat("[", 64+extra) + strings.Repeat("]", 64+extra) + "\n"
		}},
		{"key segments and brackets", func(extra int) string {
			return segments(10) + " = " + strings.Repeat("[", 55+extra) + strings.Repeat("]", 55+extra) + "\n"
		}},
		{"header segments and inline tables", func(extra int) string {
			return header + "t = " + strings.Repeat("{t = ", 34+extra) + "1" + strings.Repeat("}", 34+extra) + "\n"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if exceedsConfigDepth([]byte(tc.body(0)), true) {
				t.Fatal("a file of exactly the bound was refused")
			}
			if !exceedsConfigDepth([]byte(tc.body(1)), true) {
				t.Fatal("a file one level past the bound was read")
			}
		})
	}
}

// arrayTables returns [[a]], [[a.a]] and so on to n segments: every segment
// names an array of tables, so the last header nests 2n levels.
func arrayTables(n int) string {
	var body strings.Builder
	for i := 1; i <= n; i++ {
		body.WriteString("[[" + strings.Repeat("a.", i-1) + "a]]\n")
	}
	return body.String()
}

// nestedKeyTables returns n inline tables, each opened by a key of the given
// number of segments: k.k.k = {k.k.k = {...}}.
func nestedKeyTables(n, segments int) string {
	key := strings.Repeat("k.", segments-1) + "k"
	return key + strings.Repeat(" = {"+key, n-1) + " = 1" + strings.Repeat("}", n-1)
}

// siblingTables fills a file to the size cap with [s<i>] tables, each holding
// nestedKeyTables(tables, segments).
func siblingTables(tables, segments int) string {
	var body strings.Builder
	for i := 0; ; i++ {
		unit := fmt.Sprintf("[s%d]\n%s\n", i, nestedKeyTables(tables, segments))
		if body.Len()+len(unit) > maxProjectConfigBytes {
			return body.String()
		}
		body.WriteString(unit)
	}
}

// A file nested just under the bound and filled to the size cap is parsed at a
// known cost: measured on go1.26.9 with toml v1.6.0, 1.5 GB allocated over its
// life, 0.25 GB live, 2.9 s. Before the depth
// was counted as one sum, 133 KB of siblings nested 4,100 levels took 3.9 GB.
func TestProjectConfigSiblingTablesNearTheSizeCapParseInBoundedMemory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte(siblingTables(8, 7)), 0o644); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	entries, _, err := readProjectConfig(dir)
	runtime.ReadMemStats(&after)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries: %+v %v", entries, err)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 3<<30 {
		t.Fatalf("reading the file allocated %d bytes", got)
	}
}

// Brackets inside strings and comments, and dots that are not one key path, are
// not nesting: a file that holds them is read and flagged by its keys, not
// refused as too deep.
func TestProjectConfigBracketsInStringsAndCommentsAreNotNesting(t *testing.T) {
	many := strings.Repeat("[", 100)
	floats := "[" + strings.Repeat("0.5, ", 100) + "0.5]"
	body := "# " + many + "\nnotify = [\"x\"]\nname = '" + many + "'\n" +
		"text = \"\"\"\n" + many + "\n\"\"\"\nlit = '''\n" + many + "\n'''\nq = \"say \\\" " + many + "\"\n" +
		"values = " + floats + "\nshort.dotted.key = 1.25\n[tool.section.name]\nversion = \"1.2.3\"\n" +
		"# " + strings.Repeat("a.", 100) + "\ndots = \"" + strings.Repeat("a.", 100) + "\"\n"
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, _, err := readProjectConfig(dir)
	if err != nil || len(entries) != 1 || entries[0].Unreadable != "" || len(entries[0].Flagged) != 1 {
		t.Fatalf("entries: %+v %v", entries, err)
	}
}

// The hash covers an oversized file to its last byte: an approval of one
// version must not hold for a version that differs only far into the file.
func TestProjectConfigHashCoversTheWholeOversizedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	const size = 64<<20 + 4096 // sparse: reads as zeros, takes no disk
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	reported := func() string {
		t.Helper()
		entries, _, err := readProjectConfig(dir)
		if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Unreadable, "larger than") {
			t.Fatalf("entries: %+v %v", entries, err)
		}
		return entries[0].Unreadable
	}
	before := reported()
	if _, err := f.WriteAt([]byte("x"), size-1); err != nil {
		t.Fatal(err)
	}
	if reported() == before {
		t.Fatal("a change after the 64 MiB mark kept the same hash")
	}
}

// A viewer reads no flagged configuration and approves nothing.
func TestProjectConfigReportNeedsAdministration(t *testing.T) {
	f := newProjectConfigFixture(t)
	viewer := f.h.viewerToken(f.admin, f.tenant, "project-config-viewer@example.invalid")
	f.write(".mcp.json", `{"mcpServers":{"a":{"command":"x","env":{"API_TOKEN":"secret-value"}}}}`)
	path := "/v1/m/sessions/workspaces/" + f.ref + "/project-config"
	if r := f.h.do("GET", path, viewer, tenantHdr(f.tenant)); r.code < 400 {
		t.Fatalf("viewer read the report: %d %s", r.code, r.raw)
	}
	shown := f.report()
	if shown.code != http.StatusOK || strings.Contains(shown.raw, "secret-value") {
		t.Fatalf("report: %d %s", shown.code, shown.raw)
	}
}

// A Claude launch that carries the engine's hooks excludes the project sources
// itself and is let through, with the exclusion recorded; the same launch
// without the hook endpoint reads the files and stays refused.
func TestProjectConfigClaudeLaunchWithHooksIsExcludedAndRecorded(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithClaudeHookPEP(t.TempDir(), "/usr/local/bin/olivares"))
	ctx := t.Context()
	dir := t.TempDir()
	ref := registerTestWorkspace(t, m, tenant, dir)
	ws, err := m.resolveLaunchWorkspace(ctx, tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := CreateRunParams{Transport: TransportStreamJSON, WorkspaceRef: ref, WorkspaceDir: ws.rootReal, Actor: "user:u1", ActorKind: "user",
		ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}
	verdict := m.checkProjectConfig(ctx, tenant, p, ws)
	if verdict.refusal == nil || !m.claudeHooksMayApply(p) {
		t.Fatalf("verdict = %+v, hooks may apply = %v", verdict, m.claudeHooksMayApply(p))
	}
	pep := []EnvVar{{Name: envHookPEPURL, Value: "https://127.0.0.1:8443/v1/hooks"}, {Name: envHookPEPToken, Value: "launch-token"}}
	if err := m.projectConfigStartError(ctx, tenant, p, ws, verdict, pep); err != nil {
		t.Fatalf("a hooked launch was refused: %v", err)
	}
	if events := acctAuditsOf(t, m, tenant, "sessions.workspace.project-config-excluded"); len(events) != 1 {
		t.Fatalf("exclusion records = %d, want 1", len(events))
	}
	if err := m.projectConfigStartError(ctx, tenant, p, ws, verdict, nil); err == nil {
		t.Fatal("a launch without the hook endpoint reads the files and must be refused")
	}
}

// The files are read again right before the spawn: a launch whose folder
// changed after its check reaches no runner.
func TestProjectConfigIsCheckedAgainBeforeTheSpawn(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr))
	ctx := t.Context()
	dir := t.TempDir()
	ref := registerTestWorkspace(t, m, tenant, dir)
	ws, err := m.resolveLaunchWorkspace(ctx, tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	p := CreateRunParams{Transport: TransportStreamJSON, WorkspaceRef: ref, WorkspaceDir: ws.rootReal,
		ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"a":{"command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, proc, stage, err := m.launchChild(ctx, ctx, tenant, "run-recheck", &p, childDecision{ws: ws})
	if err == nil || proc != nil || stage != "project agent configuration check" {
		t.Fatalf("stage = %q proc = %v err = %v, want the configuration check", stage, proc, err)
	}
	if len(fr.specs) != 0 {
		t.Fatalf("the runner was asked %d times", len(fr.specs))
	}
}
