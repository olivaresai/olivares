// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// OpenCode reads agents and commands from $HOME/.opencode and the rule files its
// inline configuration lists in `instructions`, appended to the account's own
// (measured on 1.19.3). It reads their bodies verbatim, with no {env:} or {file:}
// substitution, so the session's private HOME carries them and the account
// configuration directory is left as it is.
const openCodeComponentsDir = ".opencode"

// openCodeBuiltinAgents are OpenCode's own agents (`opencode agent list`, 1.19.3).
// A delivered file of the same name would replace one, including the primary
// agent a launch pins (driver_opencode.go).
var openCodeBuiltinAgents = []string{"build", "plan", "general", "explore", "compaction", "summary", "title"}

// openCodeTemplateFile is OpenCode's command file reference (ConfigMarkdown
// FILE_REGEX, 1.19.3). It reads the file while it expands a command, before and
// despite the session's read rules. Go has no lookbehind: the pattern is any @
// that does not follow a word character or a backtick, so it refuses at least
// what OpenCode expands.
var openCodeTemplateFile = regexp.MustCompile("(?:^|[^A-Za-z0-9_`])@")

// openCodeTemplateSplice is a placeholder right after ! or right before a backtick
// or @, which lets typed arguments complete one of the two (#2998): `$ARGUMENTS`
// followed by a backtick and the argument "!" become !`, run as shell. It is also a
// $ and capital letters right before a placeholder: OpenCode replaces $N before
// $ARGUMENTS, so "$ARG$1UMENTS" becomes a new $ARGUMENTS once $1 is replaced, and
// typed arguments, which hold no $, can form one only there.
var openCodeTemplateSplice = regexp.MustCompile("!\\$(?:ARGUMENTS|[0-9])|\\$(?:ARGUMENTS|[0-9]+)[`@]|\\$[A-Z]*\\$(?:ARGUMENTS|[0-9])")

// claudeToolPermissions maps a Claude Code tool name to the OpenCode permissions
// that govern the same action.
var claudeToolPermissions = map[string][]string{
	"Read": {"read"}, "Write": {"edit"}, "Edit": {"edit"}, "MultiEdit": {"edit"}, "NotebookEdit": {"edit"},
	"Bash": {"bash"}, "Grep": {"grep"}, "Glob": {"glob", "list"}, "LS": {"list"},
	"WebFetch": {"webfetch"}, "WebSearch": {"websearch"}, "TodoWrite": {"todowrite"},
	"Task": {"task"}, "Agent": {"task"}, "Skill": {"skill"},
}

type openCodeFrontmatter struct {
	Description string            `yaml:"description"`
	Mode        string            `yaml:"mode,omitempty"`
	Permission  map[string]string `yaml:"permission,omitempty"`
}

// openCodeComponent writes a verified Claude Code agent or command as OpenCode
// reads it. Only the description passes through; a model, a tool pre-approval or
// a permission would choose the provider or widen the session's own rules. An
// agent becomes a subagent whose tool lists only narrow: a built-in tool its
// `tools` does not name, or its `disallowedTools` names, is denied; any other
// stays under the session's permission rules (OpenCode lets an agent's
// `tools: true` override them).
func openCodeComponent(kind, file string, b []byte) ([]byte, error) {
	name := strings.TrimSuffix(path.Base(file), ".md")
	refuse := func(why string) error {
		return &runErr{503, fmt.Sprintf("assigned %s %q cannot be delivered to OpenCode: %s", kind, name, why)}
	}
	front, body := "", string(b)
	if lines := strings.Split(body, "\n"); strings.TrimSuffix(lines[0], "\r") == "---" {
		end := slices.IndexFunc(lines[1:], func(l string) bool { return strings.TrimSuffix(l, "\r") == "---" })
		if end < 0 {
			return nil, refuse("its frontmatter is not closed")
		}
		front, body = strings.Join(lines[1:end+1], "\n"), strings.Join(lines[end+2:], "\n")
	}
	var meta struct {
		Description     string    `yaml:"description"`
		Tools           yaml.Node `yaml:"tools"`
		DisallowedTools yaml.Node `yaml:"disallowedTools"`
	}
	if err := yaml.Unmarshal([]byte(front), &meta); err != nil {
		return nil, refuse("its frontmatter is not valid YAML")
	}
	out := openCodeFrontmatter{Description: meta.Description}
	if out.Description == "" {
		out.Description = name
	}
	switch kind {
	case "command":
		// OpenCode's SHELL_REGEX is !`...`; an opener left open counts too,
		// because a typed backtick would close it.
		if strings.Contains(body, "!`") {
			return nil, refuse("OpenCode runs its shell interpolation (!`...`) outside the session's approvals; remove it from the command")
		}
		if openCodeTemplateFile.MatchString(body) {
			return nil, refuse("OpenCode reads its file references (@path) outside the session's approvals; remove them from the command")
		}
		if openCodeTemplateSplice.MatchString(body) {
			return nil, refuse("a $ARGUMENTS or $1 placeholder next to !, ` or @, or after a $ and capital letters, lets typed arguments become shell or file interpolation that OpenCode runs outside the session's approvals; separate them with a space")
		}
	case "agent":
		if slices.Contains(openCodeBuiltinAgents, name) {
			return nil, refuse("it would replace OpenCode's built-in agent of that name")
		}
		out.Mode = "subagent"
		tools, ok := claudeToolNames(meta.Tools)
		disallowed, ok2 := claudeToolNames(meta.DisallowedTools)
		if !ok || !ok2 {
			return nil, refuse("its tools must be a list of Claude Code tool names")
		}
		deny := map[string]string{}
		if tools != nil {
			for _, permissions := range claudeToolPermissions {
				for _, p := range permissions {
					deny[p] = "deny"
				}
			}
			for _, tool := range tools {
				for _, p := range claudeToolPermissions[tool] {
					delete(deny, p)
				}
			}
		}
		for _, tool := range disallowed {
			for _, p := range claudeToolPermissions[tool] {
				deny[p] = "deny"
			}
		}
		if len(deny) > 0 {
			out.Permission = deny
		}
	}
	encoded, err := yaml.Marshal(out)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(encoded) + "---\n" + body), nil
}

// openCodeTemplateShell is OpenCode's SHELL_REGEX (1.19.3). A skill's own text
// holds no typed backtick (Input refuses one), so only a closed pair runs.
var openCodeTemplateShell = regexp.MustCompile("!`[^`]+`")

// openCodeFileToken is OpenCode's FILE_REGEX (1.19.3) with its path captured. Go has
// no lookbehind, so the character before @ is matched instead, and JavaScript's \\s
// (which also ends a path at NBSP, U+3000, a BOM and the other Unicode spaces) is
// spelled out, so a path ends where OpenCode ends it.
var openCodeFileToken = regexp.MustCompile("(?:^|[^A-Za-z0-9_`])@(\\.?[^" + openCodeJSSpace + "`,.]*(?:\\.[^" + openCodeJSSpace + "`,.]+)*)")

const openCodeJSSpace = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// openCodePlaceholder is a placeholder OpenCode replaces before it runs or reads
// anything ($N, then $ARGUMENTS; 1.19.3 SessionPrompt.command).
var openCodePlaceholder = regexp.MustCompile(`\$(?:ARGUMENTS|[0-9]+)`)

// openCodeSkill refuses a skill whose /command form would act outside the session's
// approvals (#3005). OpenCode offers each skill as a command and expands its
// SKILL.md like a template (measured on 1.19.3 and 1.19.4): it replaces the
// placeholders, then runs every !`shell` and reads every @path that exists, through
// symlinks, resolved against the folder's git top level, or / when git does not
// take the folder for a repository. Refused: the shell; a placeholder that a typed
// or empty argument turns into shell or a file reference (openCodeSkillSplices,
// or one inside an @path); an @~ path; an @path whose name the read rules deny
// (driver_opencode.go: *.env and *.env.*, but not *.env.example); and an @path
// that names something outside the folder from the folder or any folder above it,
// lexically or through symlinks, so which one git picks does not matter; a bare
// word refuses only for a file, since a folder is only listed. An @name
// that is no file but an agent (a built-in one or one of agents, those delivered)
// starts that agent without the session's task ask. 96 of the 312 built-in skills
// mention @dataclass, an @scope/package, an email or a TypeScript "@/lib/x" import,
// which OpenCode reads only if such a file exists. The check runs at launch: a path
// created later, and an agent of the account's or the folder's own configuration
// (#3076), are not seen.
func openCodeSkill(dir, file string, b []byte, agents []string) error {
	refuse := func(why string) error {
		return &runErr{503, fmt.Sprintf("assigned skill %q cannot be delivered to OpenCode: %s", path.Dir(file), why)}
	}
	// The whole file is scanned: where OpenCode's own frontmatter parser ends is
	// not this reader's to guess.
	text := string(b)
	if openCodeTemplateShell.MatchString(text) {
		return refuse("OpenCode runs its shell interpolation (!`...`) outside the session's approvals when the skill is used as a /command; remove it from the skill")
	}
	if openCodeSkillSplices(text) {
		return refuse("a $ARGUMENTS or $1 placeholder next to !, ` or @, or after a $ and capital letters, lets typed arguments become shell or file interpolation that OpenCode runs outside the session's approvals; separate them with a space")
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil || !filepath.IsAbs(realDir) {
		realDir = "" // nothing is inside: every existing path refuses
	}
	for _, match := range openCodeFileToken.FindAllStringSubmatch(text, -1) {
		token := match[1]
		if token == "" {
			continue // OpenCode skips an empty path
		}
		if slices.Contains(openCodeBuiltinAgents, token) || slices.Contains(agents, token) || slices.Contains(openCodeObjectKeys, token) {
			return refuse(fmt.Sprintf("OpenCode starts the agent @%s without the session's task approval when the skill is used as a /command; remove the mention from the skill", token))
		}
		clean := filepath.Clean(token)
		// A bare word (no "/", no leading "." or "~") names a folder only to be
		// listed, as @media or @dev would from /: only a file it names is read.
		bare := !strings.ContainsRune(token, '/') && !strings.HasPrefix(token, ".")
		denied := openCodePlaceholder.MatchString(token) || strings.HasPrefix(token, "~") ||
			!strings.HasSuffix(clean, ".env.example") && (strings.HasSuffix(clean, ".env") || strings.Contains(clean, ".env."))
		for _, start := range []string{dir, realDir} {
			for base := start; !denied && start != ""; base = filepath.Dir(base) {
				candidate := clean // path.resolve(worktree, token)
				if !filepath.IsAbs(token) {
					candidate = filepath.Join(base, token)
				}
				denied = !openCodeAbsentOrWithin(realDir, candidate, bare)
				if filepath.IsAbs(token) || base == filepath.Dir(base) {
					break
				}
			}
		}
		if denied {
			return refuse(fmt.Sprintf("OpenCode reads its file reference @%s outside the session's approvals when the skill is used as a /command; remove it from the skill", token))
		}
	}
	return nil
}

// openCodeSkillSplices reports a placeholder that typed or empty arguments turn into
// shell or a file reference (openCodeTemplateSplice). One right before a backtick
// matters only when the text up to the next backtick, which a typed "!" would make
// OpenCode run, holds more than whitespace, "|" and ",": shell does work with no
// letter or digit (a fork bomb, a globbed /???/???). perl-patterns' "`$1, $2, $3` |
// `" would run " | ", which runs nothing.
func openCodeSkillSplices(text string) bool {
	for _, m := range openCodeTemplateSplice.FindAllStringIndex(text, -1) {
		if text[m[0]] == '!' || text[m[1]-1] != '`' {
			return true
		}
		rest := text[m[1]:]
		end := strings.IndexByte(rest, '`')
		if end >= 0 && strings.Trim(rest[:end], " \t\r\n|,") != "" {
			return true
		}
	}
	return false
}

// openCodeObjectKeys are the keys every plain JavaScript object inherits. OpenCode
// looks an @name up among its agents as such a key (1.19.3 Agent.get), so each one
// is an agent there.
var openCodeObjectKeys = []string{"constructor", "toString", "toLocaleString", "valueOf", "hasOwnProperty",
	"isPrototypeOf", "propertyIsEnumerable", "__proto__", "__defineGetter__", "__defineSetter__",
	"__lookupGetter__", "__lookupSetter__"}

// openCodeAbsentOrWithin reports whether p certainly does not exist, is a folder
// when folderOK, or is, after its symlinks, realDir or below it. Any other error is
// not absence.
func openCodeAbsentOrWithin(realDir, p string, folderOK bool) bool {
	real, err := filepath.EvalSymlinks(p)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil || realDir == "" {
		return false
	}
	if info, err := os.Stat(real); folderOK && err == nil && info.IsDir() {
		return true
	}
	rel, err := filepath.Rel(realDir, real)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// claudeToolNames reads a Claude Code agent's tool list: a comma-separated string
// or a YAML list. Absent is nil; an empty list is a list that names nothing.
// Bash(git diff:*) names Bash: the session's own rules decide what it may run.
func claudeToolNames(node yaml.Node) ([]string, bool) {
	var raw []string
	switch {
	case node.Kind == 0, node.Tag == "!!null":
		return nil, true
	case node.Kind == yaml.ScalarNode:
		raw = strings.Split(node.Value, ",")
	case node.Kind == yaml.SequenceNode:
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return nil, false
			}
			raw = append(raw, item.Value)
		}
	default:
		return nil, false
	}
	names := []string{}
	for _, tool := range raw {
		tool, _, _ = strings.Cut(strings.TrimSpace(tool), "(")
		names = append(names, tool)
	}
	return names, true
}

// addOpenCodeInstructions lists rule files in the launch's inline configuration,
// keeping every key the driver already set byte for byte.
func addOpenCodeInstructions(spec *LaunchSpec, files []string) error {
	cfg := map[string]json.RawMessage{}
	i := slices.IndexFunc(spec.Env, func(e EnvVar) bool { return e.Name == envOpenCodeConfigContent })
	if i >= 0 {
		if err := json.Unmarshal([]byte(spec.Env[i].Value), &cfg); err != nil {
			return fmt.Errorf("OpenCode inline configuration: %w", err)
		}
	}
	var instructions []string
	if raw, ok := cfg["instructions"]; ok {
		if err := json.Unmarshal(raw, &instructions); err != nil {
			return fmt.Errorf("OpenCode inline instructions: %w", err)
		}
	}
	encoded, err := json.Marshal(append(instructions, files...))
	if err != nil {
		return err
	}
	cfg["instructions"] = encoded
	body, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if i < 0 {
		spec.Env = append(spec.Env, EnvVar{Name: envOpenCodeConfigContent, Value: string(body)})
		return nil
	}
	spec.Env[i].Value = string(body)
	return nil
}
