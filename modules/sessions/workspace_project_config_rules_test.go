// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
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

// #3076 (security review): OpenCode substitutes {file:path} and {env:NAME} in the
// raw text before it parses opencode.json, so a key or a comment can turn into a
// flagged key the parsed text does not show. Every token is flagged, a {file:}
// is bound to the content of the file it names, and every top-level key that is
// not one of the few harmless ones is flagged.
func TestProjectConfigOpenCodeFlagsSubstitutionsAndUnknownKeys(t *testing.T) {
	flagged := func(t *testing.T, files map[string]string) (map[string]any, string) {
		t.Helper()
		dir := t.TempDir()
		for rel, content := range files {
			if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		entries, hash, err := readProjectConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.File == "opencode.json" {
				return e.Flagged, hash
			}
		}
		return nil, hash
	}
	if got, _ := flagged(t, map[string]string{"opencode.json": `{"$schema":"https://opencode.ai/config.json","theme":"dark","model":"m"}`}); len(got) != 0 {
		t.Fatalf("harmless keys flagged: %v", got)
	}
	for _, key := range []string{"agent", "mcp", "permission", "plugin", "provider", "skills"} {
		config := `{"` + key + `":{"probe":true}}`
		if got, _ := flagged(t, map[string]string{"opencode.json": config}); got[key] == nil {
			t.Fatalf("%s is not flagged: %v", key, got)
		}
	}
	key := map[string]string{"opencode.json": `{"{file:k.txt}":["./x.js"]}`, "k.txt": "plugin"}
	got, before := flagged(t, key)
	if !strings.HasPrefix(fmt.Sprint(got["token {file:k.txt}"]), "sha256:") {
		t.Fatalf("a {file:} key is not bound to its file: %v", got)
	}
	key["k.txt"] = "plugin2"
	if _, after := flagged(t, key); after == before {
		t.Fatal("changing the file a {file:} names keeps the hash")
	}
	if got, _ := flagged(t, map[string]string{"opencode.json": "/* {file:k.txt} */ {\"theme\":\"dark\"}", "k.txt": "*/ \"plugin\":[\"x\"], /*"}); got["token {file:k.txt}"] == nil {
		t.Fatalf("a {file:} in a comment is not flagged: %v", got)
	}
	if got, _ := flagged(t, map[string]string{"opencode.json": `{"theme":"{env:THEME}"}`}); got["token {env:THEME}"] == nil {
		t.Fatalf("an {env:} token is not flagged: %v", got)
	}
}

// #3076 (security review): a link in .opencode/ is bound to what it points to, and
// a file over the parse limit to its whole content, so a change keeps no approval.
func TestProjectConfigOpenCodeFolderBindsLinkTargetsAndLargeFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, content []byte) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hash := func() string {
		t.Helper()
		_, h, err := readProjectConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	write("src/x.js", []byte("export const X = 1\n"))
	if err := os.MkdirAll(filepath.Join(dir, ".opencode", "plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../src/x.js", filepath.Join(dir, ".opencode", "plugin", "x.js")); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, maxProjectConfigBytes+512)
	write(".opencode/tool/big.js", big)
	first := hash()
	write("src/x.js", []byte("export const X = 2\n"))
	second := hash()
	if second == first {
		t.Fatal("changing a linked plugin keeps the hash")
	}
	big[len(big)-1] = 'x' // the same size
	write(".opencode/tool/big.js", big)
	if hash() == second {
		t.Fatal("changing a large file without changing its size keeps the hash")
	}
}

// #3076 (security re-review): what the gate cannot bind in a workspace's OpenCode
// files is refused, whatever is approved: an {env:} (OpenCode replaces it first, so
// "{fi{env:UNSET}le:k}" becomes {file:k}), a {file:} in a file the gate cannot
// decode (a trailing comma OpenCode accepts) is still bound, a {file:} path with
// spaces OpenCode would read as written, and a folder whose links fan out.
func TestProjectConfigOpenCodeRefusesWhatItCannotBind(t *testing.T) {
	entry := func(t *testing.T, files map[string]string, want string) projectConfigEntry {
		t.Helper()
		dir := t.TempDir()
		for rel, content := range files {
			path := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		entries, _, err := readProjectConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.File == want {
				return e
			}
		}
		t.Fatalf("%s is not reported: %+v", want, entries)
		return projectConfigEntry{}
	}
	if e := entry(t, map[string]string{"opencode.json": `{"{fi{env:UNSET}le:k}":{}}`, "k": "plugin"}, "opencode.json"); e.Refused == "" {
		t.Fatalf("an {env:} token is not refused: %+v", e)
	}
	if e := entry(t, map[string]string{"opencode.json": `{"{file:k}":{},}`, "k": "plugin"}, "opencode.json"); !strings.HasPrefix(fmt.Sprint(e.Flagged["token {file:k}"]), "sha256:") {
		t.Fatalf("a {file:} in a file the gate cannot decode is not bound: %+v", e)
	}
	if e := entry(t, map[string]string{"opencode.json": `{"{file: k}":{}}`, "k": "plugin", " k": "plugin"}, "opencode.json"); e.Refused == "" {
		t.Fatalf("a {file:} path with spaces is not refused: %+v", e)
	}
	dir := t.TempDir()
	base := filepath.Join(dir, ".opencode", "d")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ { // eight links to the parent: 8^depth folders to visit
		if err := os.Symlink(".", filepath.Join(base, fmt.Sprintf("l%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	entries, _, err := readProjectConfig(dir)
	if err != nil || len(entries) != 1 || entries[0].Refused == "" {
		t.Fatalf("a folder whose links fan out = %+v %v, want it refused", entries, err)
	}
}

// #3076 (security review, round 3): an OpenCode file or folder that is a link out of
// the session folder is refused, not approvable by its name: what OpenCode loads
// through it is not in the folder. So is a {file:~/...}, which OpenCode reads from
// the home folder.
func TestProjectConfigOpenCodeRefusesLinksOutAndHomePaths(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "opencode.json"), []byte(`{"plugin":["x"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"opencode.json", ".opencode"} {
		dir := t.TempDir()
		target := outside
		if name == "opencode.json" {
			target = filepath.Join(outside, "opencode.json")
		}
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		entries, _, err := readProjectConfig(dir)
		if err != nil || len(entries) != 1 || entries[0].Refused == "" {
			t.Fatalf("%s linked out of the folder = %+v %v, want it refused", name, entries, err)
		}
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "~"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, content := range map[string]string{"opencode.json": `{"{file:~/k}":{}}`, "~/k": "plugin"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, _, err := readProjectConfig(dir)
	if err != nil || len(entries) != 1 || entries[0].Refused == "" {
		t.Fatalf("a {file:~/...} = %+v %v, want it refused", entries, err)
	}
}

// #3076 (review of e1c239f5, finding 1): an approval binds everything OpenCode
// loads through {file:}. A key that is itself a token keeps its value in the hash
// (OpenCode substitutes keys too, so {"{file:k}": ...} with k = "mcp" is an MCP
// server), and .opencode/opencode.json(c), which OpenCode loads through the same
// substituting reader, binds the files its tokens name, resolved from .opencode/.
func TestProjectConfigOpenCodeBindsEveryFileToken(t *testing.T) {
	hash := func(t *testing.T, files map[string]string) (string, []projectConfigEntry) {
		t.Helper()
		dir := t.TempDir()
		for rel, content := range files {
			path := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		entries, h, err := readProjectConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		return h, entries
	}
	key := func(command string) map[string]string {
		return map[string]string{"opencode.json": `{"{file:k}":{"probe":{"type":"local","command":["` + command + `"]}}}`, "k": "mcp"}
	}
	if a, _ := hash(t, key("true")); a == func() string { b, _ := hash(t, key("/bin/sh")); return b }() {
		t.Fatal("the value of a {file:} key is not in the hash")
	}
	folder := func(cmd string) map[string]string {
		return map[string]string{".opencode/opencode.json": `{"mcp":{"p":{"type":"local","command":["{file:../cmd}"]}}}`, "cmd": cmd}
	}
	a, entries := hash(t, folder("true"))
	if b, _ := hash(t, folder("/tmp/evil")); a == b {
		t.Fatalf("a {file:} in .opencode/opencode.json is not bound to its file: %+v", entries)
	}
	if _, entries := hash(t, map[string]string{".opencode/opencode.jsonc": `{"theme":"{env:X}"}`}); len(entries) != 1 || entries[0].Refused == "" {
		t.Fatalf("an {env:} in .opencode/opencode.jsonc is not refused: %+v", entries)
	}
}

// #3076 (review, finding 2): a {file:} target is opened only when it is a regular
// file, and read only up to the limit: a FIFO cannot hang the check and a huge
// sparse file cannot make the engine allocate it.
func TestProjectConfigOpenCodeFileTokenReadIsBounded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(`{"theme":"{file:f}","model":"{file:big}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "f"), 0o600); err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(dir, "big"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(1 << 30); err != nil { // sparse: no disk, 1 GiB if read whole
		t.Fatal(err)
	}
	big.Close()
	done := make(chan []projectConfigEntry, 1)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	go func() {
		entries, _, _ := readProjectConfig(dir)
		done <- entries
	}()
	select {
	case entries := <-done:
		runtime.ReadMemStats(&after)
		if len(entries) != 1 || entries[0].Refused == "" {
			t.Fatalf("a FIFO and a 1 GiB file named by {file:} = %+v, want the file refused", entries)
		}
		if grown := after.TotalAlloc - before.TotalAlloc; grown > 64<<20 {
			t.Fatalf("reading a {file:} target allocated %d bytes, want it read up to the limit only", grown)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a {file:} naming a FIFO hangs the check")
	}
}

// #3076 (review, finding 5): a folder inside .opencode/ the check cannot list is
// refused, not bound by a constant "cannot be read".
func TestProjectConfigOpenCodeFolderWalkErrorIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root lists every folder")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, ".opencode", "plugin")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "x.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o300); err != nil { // OpenCode can open x.js by name; the check cannot list it
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	entries, _, err := readProjectConfig(dir)
	if err != nil || len(entries) != 1 || entries[0].Refused == "" {
		t.Fatalf("an unlistable folder in .opencode = %+v %v, want it refused", entries, err)
	}
}

// Npm.install reifies when any declared dependency is missing from the lock's
// root entry. Such an install would execute code outside the approved tree.
func TestProjectConfigOpenCodeRefusesDependenciesThatWouldBeInstalled(t *testing.T) {
	for _, group := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		t.Run(group, func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(dir, ".opencode")
			if err := os.MkdirAll(filepath.Join(base, "node_modules"), 0o755); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{
				"package.json":      fmt.Sprintf(`{"%s":{"unlocked":"*"}}`, group),
				"package-lock.json": `{"packages":{"":{"dependencies":{"@opencode-ai/plugin":"1.19.4"}}}}`,
			} {
				if err := os.WriteFile(filepath.Join(base, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			entries, _, err := readProjectConfig(dir)
			if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Refused, "install dependencies") {
				t.Fatalf("unlocked dependency not refused: %+v %v", entries, err)
			}
		})
	}
}

func TestProjectConfigOpenCodeDependencyKeysAreCaseSensitive(t *testing.T) {
	for _, lock := range []string{
		`{"Packages":{"":{"dependencies":{"@opencode-ai/plugin":"1.19.4"}}}}`,
		`{"packages":{"":{"Dependencies":{"@opencode-ai/plugin":"1.19.4"}}}}`,
	} {
		dir := t.TempDir()
		base := filepath.Join(dir, ".opencode")
		if err := os.MkdirAll(filepath.Join(base, "node_modules"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "package.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "package-lock.json"), []byte(lock), 0o644); err != nil {
			t.Fatal(err)
		}
		entries, _, err := readProjectConfig(dir)
		if err != nil || len(entries) != 1 || entries[0].Refused == "" {
			t.Fatalf("case-folded lock accepted: %+v %v", entries, err)
		}
	}
}
