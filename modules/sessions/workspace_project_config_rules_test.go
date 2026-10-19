// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The check treats a Claude launch as excluded from project sources only when
// that launch really carries --setting-sources "". One predicate decides both,
// and this holds the check to the argv the hook setup builds.
func TestProjectConfigClaudeExclusionMatchesTheLaunchArgv(t *testing.T) {
	dataDir := t.TempDir()
	m := New(WithClaudeHookPEP(dataDir, "/usr/local/bin/olivares"))
	pep := []EnvVar{{Name: envHookPEPURL, Value: "https://127.0.0.1:8443/v1/hooks"}, {Name: envHookPEPToken, Value: "launch-token"}}
	claude := func(transport Transport) CreateRunParams {
		return CreateRunParams{Transport: transport, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}
	}
	for _, tc := range []struct {
		name    string
		p       CreateRunParams
		env     []EnvVar
		applies bool
	}{
		{"stream-json with the PEP endpoint", claude(TransportStreamJSON), pep, true},
		{"stream-json without a PEP endpoint", claude(TransportStreamJSON), nil, false},
		{"remote control", claude(TransportRemoteControl), pep, false},
		{"codex", CreateRunParams{Transport: TransportStreamJSON, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCodex}}, pep, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.claudeHooksApply(tc.p, tc.env); got != tc.applies {
				t.Fatalf("claudeHooksApply = %v, want %v", got, tc.applies)
			}
			spec := LaunchSpec{Env: tc.env}
			if err := m.prepareClaudeHooks(&spec, tc.p, "run-x"); err != nil {
				t.Fatal(err)
			}
			i := slices.Index(spec.Args, "--setting-sources")
			if excluded := i >= 0 && spec.Args[i+1] == ""; excluded != tc.applies {
				t.Fatalf("argv %q excludes project sources = %v, predicate says %v", spec.Args, excluded, tc.applies)
			}
		})
	}
}

// Rules per file: the issue's keys are flagged, ordinary keys are not, and
// URL-like and proxy variables in an env table are.
func TestProjectConfigRulesFlagOnlyWhatRunsOrRedirects(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude/settings.json", `{"model":"m","permissions":{"allow":["Bash(ls)"]},"hooks":{"Stop":[]},"apiKeyHelper":"./k.sh","statusLine":{"type":"command","command":"x"},`+
		`"env":{"ANTHROPIC_BASE_URL":"http://h","OPENAI_BASE_URL":"http://h","https_proxy":"http://p","NODE_OPTIONS":"--require x","FOO":"bar"}}`)
	write(".mcp.json", `{"mcpServers":{"s":{"command":"c"}}}`)
	write(".codex/config.toml", "model = \"m\"\nopenai_base_url = \"http://h\"\nnotify = [\"x\"]\n[shell_environment_policy.set]\nHTTP_PROXY = \"http://p\"\nOK = \"1\"\n[profiles.p]\nchatgpt_base_url = \"http://h\"\n")
	write(".gemini/settings.json", `{"theme":"t","tools":{"discoveryCommand":"d","sandbox":false},"mcp":{"serverCommand":"s","allowed":["a"]},"mcpServers":{"a":{"command":"c"}}}`)

	entries, hash, err := readProjectConfig(dir)
	if err != nil || len(hash) != 64 {
		t.Fatalf("hash = %q err = %v", hash, err)
	}
	got := map[string][]string{}
	for _, e := range entries {
		if e.Unreadable != "" {
			t.Fatalf("%s unreadable: %s", e.File, e.Unreadable)
		}
		for key := range e.Flagged {
			got[e.File] = append(got[e.File], key)
		}
		slices.Sort(got[e.File])
	}
	want := map[string][]string{
		".claude/settings.json": {"apiKeyHelper", "env.ANTHROPIC_BASE_URL", "env.NODE_OPTIONS", "env.OPENAI_BASE_URL", "env.https_proxy", "hooks", "statusLine"},
		".mcp.json":             {"mcpServers"},
		".codex/config.toml":    {"notify", "openai_base_url", "profiles.p.chatgpt_base_url", "shell_environment_policy.set.HTTP_PROXY"},
		".gemini/settings.json": {"mcp.serverCommand", "mcpServers", "tools.discoveryCommand"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flagged keys\n got %v\nwant %v", got, want)
	}
	if _, again, _ := readProjectConfig(dir); again != hash {
		t.Fatalf("the hash is not stable: %s then %s", hash, again)
	}
}

// Large integers in a flagged value stay exact, so two different numbers never
// share a hash through float rounding.
func TestProjectConfigHashKeepsNumbersExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	hashOf := func(content string) string {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		_, hash, err := readProjectConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	a := hashOf(`{"mcpServers":{"s":{"port":9007199254740993}}}`)
	b := hashOf(`{"mcpServers":{"s":{"port":9007199254740992}}}`)
	if a == "" || a == b {
		t.Fatalf("hashes: %q %q", a, b)
	}
}

func TestStripJSONCommentsLeavesStringsAlone(t *testing.T) {
	in := "{ // line\n\"url\": \"http://a//b/*c*/\", /* block */ \"k\": \"v\\\"//x\" }"
	got := string(stripJSONComments([]byte(in)))
	for _, want := range []string{`"http://a//b/*c*/"`, `"v\"//x"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("string content changed: %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "line") || strings.Contains(got, "block") {
		t.Fatalf("comment survived: %q", got)
	}
}
