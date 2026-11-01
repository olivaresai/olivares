// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/skills"
)

// openCodeComponentsRun is a stored OpenCode run whose launch spec carries the
// account configuration directory and the driver's own inline configuration.
func openCodeComponentsRun(t *testing.T, files []skills.DeliveryFile) (*Module, model.TenantID, string, string) {
	t.Helper()
	runner := &fakeRunner{initSID: "opencode-components"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()), WithConfinement([]string{t.TempDir()}, true))
	run, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := mutateRunForWorkTest(m, tenant, run.RunRef, func(r model.Record) { r[colRunProfileDriver] = providerDriverOpenCode }); err != nil {
		t.Fatal(err)
	}
	members := make([]skills.SelectedMember, 0, len(files))
	for _, f := range files {
		name := f.Kind + ":" + strings.TrimSuffix(f.Path, ".md")
		if f.Kind == "" {
			name = filepath.Dir(f.Path) // a portable skill's member name is its folder
		}
		members = append(members, skills.SelectedMember{Kind: f.Kind, Name: name})
	}
	root := t.TempDir()
	if err := m.UseSessionSkills(sessionSkillsFixture{selection: skills.Selection{Digest: "fixture", Members: members}, files: files}, root); err != nil {
		t.Fatal(err)
	}
	return m, tenant, run.RunRef, root
}

func splitTestFrontmatter(t *testing.T, b []byte) (map[string]any, string) {
	t.Helper()
	front, body, ok := strings.Cut(strings.TrimPrefix(string(b), "---\n"), "\n---\n")
	if !strings.HasPrefix(string(b), "---\n") || !ok {
		t.Fatalf("no frontmatter: %q", b)
	}
	out := map[string]any{}
	if err := yaml.Unmarshal([]byte(front), &out); err != nil {
		t.Fatal(err)
	}
	return out, body
}

// #2889: OpenCode reads agents and commands from $HOME/.opencode and rule files
// listed in its inline `instructions` (measured on 1.19.3), so a session gets
// them without touching the account configuration directory. A Claude-format
// agent's tool list only narrows: tools it does not list are denied, listed
// ones stay under the session's own permission rules.
func TestRuntimeOpenCodeComponentsUseHomeOpenCodeAndInstructions(t *testing.T) {
	agent := "---\nname: review\ndescription: \"Reviews: code\"\ntools: Read, Grep, Glob\nmodel: sonnet\ncolor: blue\n---\nYou review code.\n"
	command := "---\ndescription: Review a change\nallowed-tools: Bash(git diff:*)\nargument-hint: [file]\nmodel: sonnet\n---\nReview $ARGUMENTS.\n"
	m, tenant, runRef, _ := openCodeComponentsRun(t, []skills.DeliveryFile{
		{Kind: "agent", Path: "review.md", Bytes: []byte(agent), Mode: 0444},
		{Kind: "command", Path: "review.md", Bytes: []byte(command), Mode: 0444},
		{Kind: "rule", Path: "selection.md", Bytes: []byte("project rule"), Mode: 0444},
		{Kind: "agent", Path: "none.md", Bytes: []byte("---\nname: none\ndescription: d\ntools: []\n---\nx\n"), Mode: 0444},
		{Kind: "agent", Path: "all.md", Bytes: []byte("---\nname: all\ndescription: d\ndisallowedTools:\n  - Bash\n  - WebFetch\n---\nx\n"), Mode: 0444},
	})
	config := t.TempDir()
	spec := LaunchSpec{Dir: t.TempDir(), Isolation: IsolationNative, Confinement: &confine.Policy{}, Env: []EnvVar{
		{Name: envUserHome, Value: t.TempDir()}, {Name: envOpenCodeConfigDir, Value: config},
		{Name: envOpenCodeConfigContent, Value: `{"permission":{"bash":"ask"}}`},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := m.prepareSessionSkills(t.Context(), ctx, tenant, runRef, CreateRunParams{Actor: "user:u1", ActorKind: model.ActorUser}, &spec); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, e := range spec.Env {
		env[e.Name] = e.Value
	}
	if env[envOpenCodeConfigDir] != config {
		t.Fatalf("account configuration directory changed: %q", env[envOpenCodeConfigDir])
	}
	native := filepath.Join(env[envUserHome], ".opencode")
	if !slices.Contains(spec.Confinement.Sealed, native) {
		t.Fatalf("component directory %s is not sealed: %v", native, spec.Confinement.Sealed)
	}

	b, err := os.ReadFile(filepath.Join(native, "agents", "review.md"))
	if err != nil {
		t.Fatal(err)
	}
	front, body := splitTestFrontmatter(t, b)
	want := map[string]any{"description": "Reviews: code", "mode": "subagent", "permission": map[string]any{
		"bash": "deny", "edit": "deny", "webfetch": "deny", "websearch": "deny", "todowrite": "deny", "task": "deny", "skill": "deny"}}
	if !reflect.DeepEqual(front, want) || body != "You review code.\n" {
		t.Fatalf("agent = %v %q", front, body)
	}

	for name, want := range map[string]map[string]any{
		// An empty list names no tool; disallowedTools narrows an inherited set.
		"none": {"bash": "deny", "edit": "deny", "read": "deny", "grep": "deny", "glob": "deny", "list": "deny", "webfetch": "deny", "websearch": "deny", "todowrite": "deny", "task": "deny", "skill": "deny"},
		"all":  {"bash": "deny", "webfetch": "deny"},
	} {
		b, err := os.ReadFile(filepath.Join(native, "agents", name+".md"))
		if err != nil {
			t.Fatal(err)
		}
		if front, _ := splitTestFrontmatter(t, b); !reflect.DeepEqual(front["permission"], want) {
			t.Fatalf("%s permission = %v", name, front["permission"])
		}
	}

	b, err = os.ReadFile(filepath.Join(native, "commands", "review.md"))
	if err != nil {
		t.Fatal(err)
	}
	front, body = splitTestFrontmatter(t, b)
	if !reflect.DeepEqual(front, map[string]any{"description": "Review a change"}) || body != "Review $ARGUMENTS.\n" {
		t.Fatalf("command = %v %q", front, body)
	}

	var cfg struct {
		Permission   map[string]any `json:"permission"`
		Instructions []string       `json:"instructions"`
	}
	if err := json.Unmarshal([]byte(env[envOpenCodeConfigContent]), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Permission["bash"] != "ask" || len(cfg.Instructions) != 1 || !strings.HasPrefix(cfg.Instructions[0], native+string(filepath.Separator)) {
		t.Fatalf("inline configuration = %s", env[envOpenCodeConfigContent])
	}
	if got, err := os.ReadFile(cfg.Instructions[0]); err != nil || string(got) != "project rule" {
		t.Fatalf("rule = %q %v", got, err)
	}
}

// OpenCode runs a command's !`shell` and reads its @file before, and despite,
// the session's bash and read rules (measured on 1.19.3), and a tool list that
// grants overrides them. Such content is refused before anything is written.
func TestRuntimeOpenCodeComponentsRefuseUngovernedContentBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		file   skills.DeliveryFile
		reason string
	}{
		{"shell", skills.DeliveryFile{Kind: "command", Path: "diff.md", Bytes: []byte("---\ndescription: d\n---\nChanges: !`git diff`\n")}, "shell"},
		{"shell-no-frontmatter", skills.DeliveryFile{Kind: "command", Path: "diff.md", Bytes: []byte("Changes: !`git diff`\n")}, "shell"},
		{"file", skills.DeliveryFile{Kind: "command", Path: "read.md", Bytes: []byte("Summarize @README.md\n")}, "file reference"},
		{"file-in-parens", skills.DeliveryFile{Kind: "command", Path: "read.md", Bytes: []byte("Summarize (@.env)\n")}, "file reference"},
		// #2998: typed arguments would complete the interpolation.
		// An opener left open is closed by a typed backtick (review of #3010).
		{"unclosed-opener", skills.DeliveryFile{Kind: "command", Path: "run.md", Bytes: []byte("Run !`git log $ARGUMENTS\n")}, "shell"},
		{"splice-bang", skills.DeliveryFile{Kind: "command", Path: "run.md", Bytes: []byte("Run !$ARGUMENTS\n")}, "placeholder"},
		{"splice-backtick", skills.DeliveryFile{Kind: "command", Path: "run.md", Bytes: []byte("Run `$ARGUMENTS` now.\n")}, "placeholder"},
		{"splice-file", skills.DeliveryFile{Kind: "command", Path: "run.md", Bytes: []byte("See ($1@x)\n")}, "placeholder"},
		// OpenCode replaces $N before $ARGUMENTS, so removing an empty $1 can form a
		// $ARGUMENTS that the splice check never saw (review of #3050).
		{"hidden-shell", skills.DeliveryFile{Kind: "command", Path: "run.md", Bytes: []byte("Run !$ARG$1UMENTS`id`\n")}, "placeholder"},
		{"hidden-file", skills.DeliveryFile{Kind: "command", Path: "run.md", Bytes: []byte("Run $ARG$1UMENTS@/etc/passwd\n")}, "placeholder"},
		{"builtin-agent", skills.DeliveryFile{Kind: "agent", Path: "build.md", Bytes: []byte("---\ndescription: d\n---\nx\n")}, "built-in"},
		{"agent-tools-map", skills.DeliveryFile{Kind: "agent", Path: "review.md", Bytes: []byte("---\ndescription: d\ntools:\n  bash: true\n---\nx\n")}, "tools"},
		{"scoped-rule", skills.DeliveryFile{Kind: "rule", Scoped: true, Path: "selection.md", Bytes: []byte("rule")}, "path-scoped rules"},
		{"importing-rule", skills.DeliveryFile{Kind: "rule", Imports: true, Path: "selection.md", Bytes: []byte("@x.md")}, "rules with file imports"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.file.Mode = 0444
			m, tenant, runRef, root := openCodeComponentsRun(t, []skills.DeliveryFile{tc.file})
			spec := LaunchSpec{Dir: t.TempDir(), Isolation: IsolationNative, Confinement: &confine.Policy{}, Env: []EnvVar{{Name: envUserHome, Value: t.TempDir()}}}
			err := m.prepareSessionSkills(t.Context(), t.Context(), tenant, runRef, CreateRunParams{}, &spec)
			var refusal *runErr
			if !errors.As(err, &refusal) || refusal.status != 503 || !strings.Contains(err.Error(), tc.reason) || !strings.Contains(err.Error(), "OpenCode") {
				t.Fatalf("missing named refusal: %v", err)
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatalf("refused delivery wrote files: %v %v", entries, err)
			}
		})
	}
}

// OpenCode 1.19.3 also offers each skill as a slash command, and a command of the same
// name hides it (command/index.ts: if (commands[item.name]) continue). The same
// ambiguous alias Claude Code refuses is refused for OpenCode (review of #3003).
func TestRuntimeOpenCodeRefusesASkillAndCommandWithOneSlashName(t *testing.T) {
	m, tenant, runRef, root := openCodeComponentsRun(t, []skills.DeliveryFile{
		{Path: "review/SKILL.md", Bytes: []byte("---\nname: review\ndescription: d\n---\nskill\n"), Mode: 0444},
		{Kind: "command", Path: "review.md", Bytes: []byte("command\n"), Mode: 0444},
	})
	spec := LaunchSpec{Dir: t.TempDir(), Isolation: IsolationNative, Confinement: &confine.Policy{}, Env: []EnvVar{{Name: envUserHome, Value: t.TempDir()}}}
	err := m.prepareSessionSkills(t.Context(), t.Context(), tenant, runRef, CreateRunParams{}, &spec)
	var refusal *runErr
	if !errors.As(err, &refusal) || refusal.status != 503 || !strings.Contains(err.Error(), "OpenCode") {
		t.Fatalf("missing named refusal: %v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("refused delivery wrote files: %v %v", entries, err)
	}
}

// addOpenCodeInstructions appends to instructions the launch already lists, keeps the
// other keys byte for byte, and fails closed on an inline configuration it cannot read.
func TestOpenCodeInstructionsAppendAndFailClosed(t *testing.T) {
	spec := LaunchSpec{Env: []EnvVar{{Name: envOpenCodeConfigContent, Value: `{"instructions":["/a.md"],"permission":{"*":"ask","read":{"*":"ask","*.env":"deny"}}}`}}}
	if err := addOpenCodeInstructions(&spec, []string{"/b.md"}); err != nil {
		t.Fatal(err)
	}
	if got, want := spec.Env[0].Value, `{"instructions":["/a.md","/b.md"],"permission":{"*":"ask","read":{"*":"ask","*.env":"deny"}}}`; got != want {
		t.Fatalf("inline configuration = %s, want %s", got, want)
	}
	for _, bad := range []string{`{"instructions":`, `{"instructions":"/a.md"}`} {
		spec := LaunchSpec{Env: []EnvVar{{Name: envOpenCodeConfigContent, Value: bad}}}
		if err := addOpenCodeInstructions(&spec, []string{"/b.md"}); err == nil {
			t.Fatalf("malformed inline configuration %q was accepted", bad)
		}
	}
}

// #3005: OpenCode offers each skill as a /command and expands its SKILL.md like a
// command template: it runs !`shell` and reads an @path, outside the session's
// approvals (measured on 1.19.3 and 1.19.4). Only what OpenCode would run, and @paths
// that leave the folder or name a file the read policy denies, refuse delivery:
// 96 of the 312 built-in skills mention @dataclass, @scope/pkg or an email.
func TestRuntimeOpenCodeRefusesSkillsWhoseCommandFormRunsOutsideApprovals(t *testing.T) {
	for _, tc := range []struct {
		body   string
		refuse bool
		folder string // "" is a folder in no git repository; "repo" and "repo/sub" are in one; "broken" has an empty .git
	}{
		{"Context: !`git status`", true, ""},
		{"Read @/etc/passwd", true, ""},
		{"Read @~/.ssh/config", true, ""},
		// The test writes outside.md beside the folder; @/lib/csrf (a TypeScript
		// alias) and ../missing.md name no file. Outside a repository OpenCode
		// resolves ../ against /, so ../{outside} names it.
		{"Read @../{outside}", true, ""},
		{`import { csrf } from "@/lib/csrf"`, false, ""},
		{"Read @../missing.md", false, ""},
		// OpenCode ends a path at any JavaScript space (review finding).
		{"Read @/etc/passwd\u00a0now", true, ""},
		{"Read @.env\u3000now", true, ""},
		{"Read @.env.d/key", true, ""},
		{"---\nnote: !`id`\n---", true, ""},
		{"Read @.env", true, ""},
		{"Read (@config/.env.local)", true, ""},
		{"Use @dataclass(frozen=True) for records.", false, ""},
		{"Install @modelcontextprotocol/sdk.", false, ""},
		{"Mail dev@example.test.", false, ""},
		{"Copy @.env.example first.", false, ""},
		// OpenCode resolves an @path against the git top level, or / in no repository
		// (review of #3050). {outside} is outside.md beside the folders, without its
		// leading /; repo/docs/plan.md exists.
		{"Read @{outside}", true, ""},
		{"Read @docs/plan.md", false, "repo"},
		{"Read @docs/plan.md", true, "repo/sub"},
		{"Read @../outside.md", true, "repo/sub"},
		{"Read @docs/plan.md", false, ""},
		// A stat that fails for another reason than absence refuses.
		{"Read @/{outside}/x", true, ""},
		// OpenCode expands placeholders before it runs or reads anything: with no
		// argument they vanish, and a typed ! or path completes the rest.
		{"!$1`id`", true, ""},
		{"!$ARGUMENTS`id`", true, ""},
		{"@$1/etc/passwd", true, ""},
		{"@$1~/.ssh/id_rsa", true, ""},
		{"@.$1env", true, ""},
		{"Run $ARGUMENTS`id`", true, ""},
		{"Costs $5 a month, see $ARGUMENTS.", false, ""},
		// Review of #3050 (security-reviewer). OpenCode replaces $N before $ARGUMENTS.
		{"!$ARG$1UMENTS`id`", true, ""},
		{"$ARG$1UMENTS@/etc/passwd", true, ""},
		// It strips a trailing slash before it reads.
		{"Read @.env/", true, "repo"},
		// A .git that git does not accept is no repository to OpenCode: it resolves
		// against /. "broken" has an empty .git file.
		{"Read @{outside}", true, "broken"},
		// It reads through a symlink: repo/link points at secret/, beside the folders.
		{"Read @link/x", true, "repo"},
		// It skips an empty path.
		{"Use @ here.", false, ""},
		// An @name that names no file but an agent starts that agent without the task
		// rule's ask (review of #3050): a built-in subagent, or the delivered reviewer.
		{"Delegate this to @general.", true, ""},
		{"Ask @reviewer to check it.", true, ""},
		// OpenCode looks an agent up as a key of a plain JavaScript object, so an
		// inherited key is an agent too (review of #3050, round 3).
		{"See @constructor.", true, ""},
		{"See @__proto__ here.", true, ""},
		// the maintainer's review of the catalog census: a mention is not a path. A typed "!"
		// before "| " would run only punctuation, and a bare word that names a folder
		// above (media/, dev/ beside the folders, and /media, /dev) lists only names.
		{"| `$1, $2, $3` | `$+{name}` (named captures) |", false, ""},
		{"Use @media queries for print.", false, ""},
		{"Start @dev servers with the script.", false, ""},
		// A bare word that names a file above still reads its content.
		{"Read @notes.txt first.", true, ""},
		// Shell does work without letters or digits (review of the narrowing): only
		// whitespace, "|" and "," between the backticks run nothing.
		{"`$1` :(){ :|:& };: `x`", true, ""},
		{"`$1` /???/??? `x`", true, ""},
		// The folders above are walked physically too: "link/proj" is reached through a
		// symlink to real/deep, and real/secret2 sits two levels above the real folder.
		{"Read @secret2", true, "link/proj"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			base := t.TempDir()
			outside := filepath.Join(base, "outside.md")
			for _, dir := range []string{"folder", "repo/.git", "repo/sub", "repo/docs", "broken", "secret", "real/deep/proj", "media", "dev"} {
				if err := os.MkdirAll(filepath.Join(base, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, file := range []string{outside, filepath.Join(base, "repo/docs/plan.md"), filepath.Join(base, "repo/.env"), filepath.Join(base, "secret/x"), filepath.Join(base, "broken/.git"), filepath.Join(base, "real/secret2"), filepath.Join(base, "notes.txt")} {
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join(base, "secret"), filepath.Join(base, "repo/link")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(base, "real/deep"), filepath.Join(base, "link")); err != nil {
				t.Fatal(err)
			}
			folder := filepath.Join(base, "folder")
			if tc.folder != "" {
				folder = filepath.Join(base, tc.folder)
			}
			body := "---\nname: research\ndescription: d\n---\n" + strings.ReplaceAll(tc.body, "{outside}", strings.TrimPrefix(outside, "/")) + "\n"
			m, tenant, runRef, root := openCodeComponentsRun(t, []skills.DeliveryFile{{Path: "research/SKILL.md", Bytes: []byte(body), Mode: 0444},
				{Kind: "agent", Path: "reviewer.md", Bytes: []byte("---\ndescription: d\n---\nreview\n"), Mode: 0444}})
			spec := LaunchSpec{Dir: folder, Isolation: IsolationNative, Confinement: &confine.Policy{}, Env: []EnvVar{{Name: envUserHome, Value: t.TempDir()}}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := m.prepareSessionSkills(t.Context(), ctx, tenant, runRef, CreateRunParams{Actor: "user:u1", ActorKind: model.ActorUser}, &spec)
			if !tc.refuse {
				if err != nil {
					t.Fatalf("a skill OpenCode would not expand outside the folder was refused: %v", err)
				}
				return
			}
			var refusal *runErr
			if !errors.As(err, &refusal) || refusal.status != 503 || !strings.Contains(err.Error(), "OpenCode") {
				t.Fatalf("missing named refusal: %v", err)
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatalf("refused delivery wrote files: %v %v", entries, err)
			}
		})
	}
}
