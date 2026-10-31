// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A cloned repository can carry agent configuration that runs a program or
// redirects traffic. The engine launches sessions in arbitrary folders, so that
// configuration is untrusted input at launch: it is read here, never executed,
// and a launch whose tool would honor it waits for a person to approve the
// SHA-256 of exactly the flagged part (an exact-hash consent, like a reviewed
// commit id).
const (
	colWsProjectConfigHash  = "project_config_approved_sha256"
	colWsProjectConfigActor = "project_config_approved_by"
	colWsProjectConfigAt    = "project_config_approved_at"

	// maxProjectConfigBytes bounds the file that is parsed; a larger one is
	// flagged and bound by the hash of its whole content.
	maxProjectConfigBytes = 1 << 20
	// maxProjectConfigTreeFiles, maxProjectConfigTreeBytes, maxProjectConfigTreeLinks
	// and maxProjectConfigTreeDirs bound the folder of a "tree" entry that is hashed at each launch; a larger
	// folder cannot be bound and is refused.
	maxProjectConfigTreeFiles = 8192
	maxProjectConfigTreeBytes = 64 << 20
	maxProjectConfigTreeLinks = 8
	maxProjectConfigTreeDirs  = 1024
)

// projectConfigFile is one repo-resident file a tool reads, relative to the
// folder the session runs in. The order of the table is the canonical order of
// the hash.
type projectConfigFile struct {
	driver string
	path   string
	format string // "json" (comments allowed), "toml", "dotenv" or "tree" (a folder, every file bound)
	rules  projectConfigRules
}

// projectConfigRules names what is flagged in a file: whole top-level keys,
// dotted paths inside nested tables, and tables of environment variables.
type projectConfigRules struct {
	keys    []string
	paths   []string
	envKeys []string
	baseURL bool // any top-level key that ends in _base_url
	profile bool // named [profiles.<name>] tables carry the same keys
	allEnv  bool // the document is itself a table of environment variables
	// allowKeys, when set, flags every top-level key that is not one of them.
	allowKeys []string
	// substitutions flags every {file:path} and {env:NAME} in the raw text, which
	// the tool replaces before it parses the file; a {file:} is bound to the
	// content of the file it names.
	substitutions bool
}

var claudeSettingsRules = projectConfigRules{
	keys: []string{
		"hooks", "apiKeyHelper", "statusLine", "mcpServers", "enableAllProjectMcpServers", "enabledMcpjsonServers",
		"awsAuthRefresh", "awsCredentialExport", "otelHeadersHelper", "fileSuggestion",
		"enabledPlugins", "extraKnownMarketplaces",
	},
	envKeys: []string{"env"},
}

// openCodeConfigRules flag every opencode.json key but the few that only shape
// how OpenCode looks or which model it asks (OpenCode 1.19.4's configuration
// schema): rules, agents, plugins, MCP servers, providers, commands, skills and
// the programs it starts are all configuration that changes what a session may
// do, run or reach. OpenCode substitutes {file:} and {env:} in the raw text, so
// every one of those is flagged too, wherever it is.
var openCodeConfigRules = projectConfigRules{
	allowKeys:     []string{"$schema", "theme", "keybinds", "logLevel", "model", "small_model", "username", "share", "autoupdate", "snapshot", "layout"},
	substitutions: true,
}

var projectConfigFiles = []projectConfigFile{
	{driver: providerDriverClaude, path: ".claude/settings.json", format: "json", rules: claudeSettingsRules},
	{driver: providerDriverClaude, path: ".claude/settings.local.json", format: "json", rules: claudeSettingsRules},
	{driver: providerDriverClaude, path: ".mcp.json", format: "json", rules: projectConfigRules{keys: []string{"mcpServers"}}},
	{driver: providerDriverCodex, path: ".codex/config.toml", format: "toml", rules: projectConfigRules{
		keys:    []string{"hooks", "notify", "mcp_servers", "model_providers", "otel"},
		envKeys: []string{"shell_environment_policy.set"},
		baseURL: true,
		profile: true,
	}},
	{driver: providerDriverGemini, path: ".gemini/settings.json", format: "json", rules: projectConfigRules{
		keys:  []string{"hooks", "mcpServers", "toolDiscoveryCommand", "toolCallCommand", "proxy"},
		paths: []string{"tools.discoveryCommand", "tools.callCommand", "mcp.serverCommand", "telemetry.otlpEndpoint"},
	}},
	// OpenCode merges the session folder's opencode.json(c) under the launch's own
	// configuration, so a key the launch does not set is added (#3076), and it loads
	// .opencode/ (agents, commands, modes, plugins and tools, which run inside it).
	{driver: providerDriverOpenCode, path: "opencode.json", format: "json", rules: openCodeConfigRules},
	{driver: providerDriverOpenCode, path: "opencode.jsonc", format: "json", rules: openCodeConfigRules},
	{driver: providerDriverOpenCode, path: ".opencode", format: "tree"},
	// Gemini CLI loads these as environment variables, where its base URL is set.
	{driver: providerDriverGemini, path: ".gemini/.env", format: "dotenv", rules: projectConfigRules{allEnv: true}},
	{driver: providerDriverGemini, path: ".env", format: "dotenv", rules: projectConfigRules{allEnv: true}},
	// Cursor CLI: project permissions and the two other settings that approve work
	// without asking, its MCP servers and its hooks, which can answer "allow".
	{driver: providerDriverCursor, path: ".cursor/cli.json", format: "json", rules: projectConfigRules{keys: []string{"permissions", "approvalMode", "autoAcceptWebSearch"}}},
	{driver: providerDriverCursor, path: ".cursor/mcp.json", format: "json", rules: projectConfigRules{keys: []string{"mcpServers"}}},
	{driver: providerDriverCursor, path: ".cursor/hooks.json", format: "json", rules: projectConfigRules{keys: []string{"hooks"}}},
}

// flaggedEnvNames load code into a tool or change what it trusts.
var flaggedEnvNames = []string{
	"NODE_OPTIONS", "NODE_EXTRA_CA_CERTS", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES",
	"BASH_ENV", "PATH", "PYTHONPATH", "GIT_SSH_COMMAND", "GIT_EXTERNAL_DIFF", "SSL_CERT_FILE", "LD_AUDIT",
	"NODE_TLS_REJECT_UNAUTHORIZED", "CLAUDE_CODE_SHELL_PREFIX", "ANTHROPIC_CUSTOM_HEADERS",
}

// flaggedEnvName reports a variable that redirects a tool's traffic or loads
// code into it. NO_PROXY only narrows a proxy, so it is not flagged.
func flaggedEnvName(name string) bool {
	upper := strings.ToUpper(name)
	if slices.Contains(flaggedEnvNames, upper) {
		return true
	}
	return strings.HasSuffix(upper, "_BASE_URL") || strings.HasSuffix(upper, "_API_BASE") ||
		strings.HasSuffix(upper, "_ENDPOINT") || strings.HasPrefix(upper, "AWS_ENDPOINT_URL") ||
		(strings.HasSuffix(upper, "_PROXY") && upper != "NO_PROXY")
}

type projectConfigEntry struct {
	File       string         `json:"file"`
	Flagged    map[string]any `json:"flagged,omitempty"`
	Unreadable string         `json:"unreadable,omitempty"`
	// Refused names why no approval can let this file be used: what the tool
	// would load cannot be bound by a hash (a folder over its limits, a link out).
	Refused string `json:"refused,omitempty"`
	// contentSHA256 is the canonical document hash without OpenCode's generated
	// $schema key. Nested config uses it so every other edit voids approval.
	contentSHA256 string
}

func projectConfigFileDriver(path string) string {
	for _, f := range projectConfigFiles {
		if f.path == path {
			return f.driver
		}
	}
	return ""
}

// projectConfigFlaggedFor is what a launch of driver is held for in one entry: the entry's own
// flags for the tool whose row names the file. Cursor also loads the hooks of Claude Code's two
// project settings files (cursor.com/docs/reference/third-party-hooks), so those files hold a
// Cursor launch for their hooks, or when they cannot be read; their other keys are Claude's.
func projectConfigFlaggedFor(e projectConfigEntry, driver string) (map[string]any, bool) {
	if projectConfigFileDriver(e.File) == driver {
		return e.Flagged, true
	}
	if driver != providerDriverCursor || (e.File != ".claude/settings.json" && e.File != ".claude/settings.local.json") {
		return nil, false
	}
	if hooks, ok := e.Flagged["hooks"]; ok {
		return map[string]any{"hooks": hooks}, true
	}
	return nil, e.Unreadable != ""
}

// readProjectConfig reads every known file of dir. It opens files read-only
// inside dir and parses text; nothing in them is evaluated. Only files with
// flagged keys, or files that cannot be read as the tool's format, appear: an
// unreadable file is flagged, because it cannot be shown safe, and its entry
// carries the hash of its content so a change still voids an approval. The hash
// covers all entries.
func readProjectConfig(dir string) ([]projectConfigEntry, string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", fmt.Errorf("open folder: %w", err)
	}
	defer root.Close()
	var entries []projectConfigEntry
	for _, f := range projectConfigFiles {
		if entry, ok := readProjectConfigFile(root, dir, f); ok {
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return nil, "", nil
	}
	canonical, err := json.Marshal(entries) // map keys are sorted at every depth
	if err != nil {
		return nil, "", fmt.Errorf("hash project configuration: %w", err)
	}
	return entries, sha256Hex(canonical), nil
}

func readProjectConfigFile(root *os.Root, dir string, f projectConfigFile) (projectConfigEntry, bool) {
	entry := projectConfigEntry{File: f.path}
	rel := filepath.FromSlash(f.path)
	unreadable := func(format string, args ...any) (projectConfigEntry, bool) {
		entry.Unreadable = fmt.Sprintf(format, args...)
		return entry, true
	}
	// cannotBind is a file whose content this reader cannot take: for OpenCode's
	// files, which the tool reads through its own substitutions, no approval can bind
	// what it would load.
	cannotBind := func(format string, args ...any) (projectConfigEntry, bool) {
		if f.rules.substitutions {
			entry.Refused = "cannot be read inside the folder as a regular file, so no approval can bind what the tool would load"
		}
		return unreadable(format, args...)
	}
	info, err := root.Stat(rel)
	if errors.Is(err, os.ErrNotExist) {
		if _, lerr := root.Lstat(rel); lerr != nil {
			return entry, false // no such file, and no dangling link either
		}
	}
	if err != nil {
		// A link that leaves the folder, a loop or a permission error: the tool
		// follows it where this reader may not, so it is flagged by what names it.
		// For OpenCode's files nothing a hash could bind is in the folder, so no
		// approval can let them be used.
		if f.rules.substitutions || f.format == "tree" {
			entry.Refused = "is a link out of the folder or cannot be read inside it, so no approval can bind what the tool would load"
		}
		if target, lerr := os.Readlink(filepath.Join(dir, rel)); lerr == nil {
			return unreadable("symbolic link to %s cannot be read inside the folder", target)
		}
		return unreadable("cannot be read inside the folder")
	}
	if f.format == "tree" {
		if !info.IsDir() {
			entry.Refused = "is not a folder, so no approval can bind what OpenCode would load"
			return unreadable("is not a folder (%s)", strings.ToLower(info.Mode().Type().String()))
		}
		return readProjectConfigTree(root, dir, entry, rel)
	}
	if !info.Mode().IsRegular() {
		return cannotBind("is not a regular file (%s)", strings.ToLower(info.Mode().Type().String()))
	}
	file, err := openRegularWithin(root, f.path)
	if err != nil {
		return cannotBind("cannot be opened")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxProjectConfigBytes+1))
	if err != nil {
		return cannotBind("cannot be read")
	}
	if len(raw) > maxProjectConfigBytes {
		if f.rules.substitutions {
			entry.Refused = "is too large for its {file:} and {env:} tokens to be read, so no approval can bind what the tool would load"
			return unreadable("is larger than %d bytes (%d bytes)", maxProjectConfigBytes, info.Size())
		}
		// The hash covers every byte: a change anywhere voids an approval.
		sum := sha256.New()
		sum.Write(raw)
		if _, err := io.Copy(sum, file); err != nil {
			return cannotBind("cannot be read")
		}
		return unreadable("is larger than %d bytes (%d bytes); sha256 of its content %s", maxProjectConfigBytes, info.Size(), hex.EncodeToString(sum.Sum(nil)))
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(raw)) == 0 {
		return entry, false
	}
	// The tool substitutes tokens in the raw text before it parses it, so they are
	// read first, and bound also in a file this reader cannot decode.
	var subs map[string]any
	var substituted []byte
	if f.rules.substitutions {
		subs, entry.Refused, substituted = configSubstitutions(root, filepath.ToSlash(filepath.Dir(rel)), raw)
	}
	doc, err := decodeProjectConfig(raw, f.format)
	if err != nil {
		entry.Flagged = subs
		if f.rules.substitutions {
			entry.Refused = "is not valid JSONC, so no approval can inspect what OpenCode would load"
		}
		return unreadable("is not valid %s (%v); sha256 of its content %s", f.format, err, sha256Hex(raw))
	}
	if f.rules.substitutions {
		entry.contentSHA256 = openCodeConfigContentSHA256(doc)
		if entry.Refused == "" {
			effectiveDoc := doc
			if !bytes.Equal(substituted, raw) {
				effectiveDoc, err = decodeProjectConfig(substituted, "json")
				if err != nil {
					entry.Refused = "uses {file:} whose substituted configuration cannot be inspected"
				}
			}
			if entry.Refused == "" {
				entry.Refused = openCodeExternalReferenceRefusal(effectiveDoc)
			}
		}
	}
	entry.Flagged = f.rules.flag(doc)
	for token, bound := range subs {
		entry.Flagged[token] = bound
	}
	return entry, len(entry.Flagged) > 0
}

// openCodeExternalReferenceRefusal names configuration that points OpenCode at
// code or instruction files outside the approval hash. A {file:} token only
// substitutes another path; it does not bind that path's target. Reviewed plugin
// files under .opencode remain supported.
func openCodeExternalReferenceRefusal(doc map[string]any) string {
	for _, key := range []string{"plugin", "instructions"} {
		if meaningful(doc[key]) {
			return key + " names content outside the files in the approval hash, so no approval can bind it"
		}
	}
	return ""
}

// openCodeConfigContentSHA256 covers every value, not only the flagged subset,
// but excludes the $schema key OpenCode adds itself. Canonical JSON makes the
// hash independent of that generated edit's formatting.
func openCodeConfigContentSHA256(doc map[string]any) string {
	withoutSchema := make(map[string]any, len(doc))
	for key, value := range doc {
		if key != "$schema" {
			withoutSchema[key] = value
		}
	}
	canonical, _ := json.Marshal(withoutSchema)
	return sha256Hex(canonical)
}

// openCodeSubstitutionContent is OpenCode's trim followed by
// JSON.stringify(content).slice(1,-1), so inserted text cannot change JSON structure.
func openCodeSubstitutionContent(content []byte) []byte {
	// ECMAScript's trim set includes U+FEFF but not U+0085, unlike Go's
	// unicode.IsSpace. Match it before applying JSON.stringify's escaping.
	trimmed := strings.TrimFunc(string(content), func(r rune) bool {
		return r == 0xFEFF || (unicode.IsSpace(r) && r != '\u0085')
	})
	quoted, _ := json.Marshal(trimmed)
	return quoted[1 : len(quoted)-1]
}

// configSubstitutionToken is OpenCode's {file:path} and {env:NAME}.
var configSubstitutionToken = regexp.MustCompile(`\{(file|env):([^}]*)\}`)

// configSubstitutions names each {file:} and {env:} token of raw, wherever it is (a
// key, a value or a comment), a {file:} with the sha256 of the file it names, read
// inside the folder, and says why no approval can bind them when that is so: an
// {env:} (OpenCode replaces it first, and its value, or an empty string that joins
// the text around it into a new token, is not in the folder), a {file:} path the
// tool would read as written with its spaces, from the home folder or from an
// absolute path (none of them is in the folder), one not readable inside the folder,
// or more files or bytes than a folder tree may hold.
//
// Each is named "token <token>", apart from the file's keys: a key may itself be a
// token (OpenCode substitutes keys too), and its value stays bound under its own
// name. A {file:} path is read from base, the folder of the file that names it, once
// however many tokens name it, and all of them under one budget: a config of a
// hundred thousand tokens must not make every check read a hundred thousand files.
// The third return is OpenCode's effective substituted text, built in one pass so
// external configuration introduced by a substitution can be inspected too. Content
// is trimmed and JSON-escaped, and capped at 64 MiB before it can expand further.
func configSubstitutions(root *os.Root, base string, raw []byte) (map[string]any, string, []byte) {
	out, refused := map[string]any{}, ""
	if bytes.Contains(raw, []byte("{env:")) {
		refused = "uses {env:}, whose value no approval can bind"
	}
	sums := map[string]string{} // cleaned path -> "sha256:...", or "" when it cannot be read inside the folder
	contents := map[string][]byte{}
	effective := bytes.NewBuffer(make([]byte, 0, len(raw)))
	last := 0
	var read, effectiveBytes int64
	for _, m := range configSubstitutionToken.FindAllSubmatchIndex(raw, -1) {
		token, kind, arg := string(raw[m[0]:m[1]]), string(raw[m[2]:m[3]]), string(raw[m[4]:m[5]])
		if kind == "env" {
			out["token "+token] = "the environment variable " + arg
			continue
		}
		unbound := func() {
			out["token "+token] = "a file that cannot be read inside the folder"
			refused = token + " names a file that cannot be read inside the folder, so no approval can bind what the tool would read"
		}
		if arg == "" || arg != strings.TrimSpace(arg) || strings.HasPrefix(arg, "~") || filepath.IsAbs(arg) {
			unbound()
			continue
		}
		path := filepath.ToSlash(filepath.Join(base, arg))
		sum, seen := sums[path]
		if !seen {
			content, err := readSmallRegularWithin(root, path)
			if read += int64(len(content)); len(sums) >= maxProjectConfigTreeFiles || read > maxProjectConfigTreeBytes {
				refused = fmt.Sprintf("names more than %d files or %d bytes through {file:}, so no approval can bind what the tool would read", maxProjectConfigTreeFiles, maxProjectConfigTreeBytes)
				break
			}
			if err == nil {
				sum = "sha256:" + sha256Hex(content)
				contents[path] = content
			}
			sums[path] = sum
		}
		if sum == "" {
			unbound()
			continue
		}
		out["token "+token] = sum
		content := openCodeSubstitutionContent(contents[path])
		if effectiveBytes+int64(len(content)) > maxProjectConfigTreeBytes {
			refused = "substitutes more than 64 MiB through {file:}, so no approval can inspect the expanded configuration"
			break
		}
		effectiveBytes += int64(len(content))
		effective.Write(raw[last:m[0]])
		effective.Write(content)
		last = m[1]
	}
	if last == 0 {
		return out, refused, raw
	}
	effective.Write(raw[last:])
	return out, refused, effective.Bytes()
}

// readProjectConfigTree flags a folder the tool loads as a whole: every file in it,
// a linked one included, is named with the sha256 of its content, so an approval
// binds those reviewed files and a change anywhere voids it. The folder's .gitignore, which
// OpenCode writes itself, is left out. Dependencies must already be prepared so
// OpenCode will not install code outside the approved tree. A folder
// that cannot be bound this way (over its file or byte limit, a link out of it or a
// link loop) is refused whatever is approved.
func readProjectConfigTree(root *os.Root, dir string, entry projectConfigEntry, rel string) (projectConfigEntry, bool) {
	files := map[string]any{}
	refusedWhy := ""
	var total int64
	dirs := 0
	type subdir struct {
		at, prefix string
		depth      int
	}
	var walk func(at, prefix string, depth int) error
	walk = func(at, prefix string, depth int) error {
		if dirs++; depth > maxProjectConfigTreeLinks || dirs > maxProjectConfigTreeDirs {
			return errProjectConfigTreeUnbound
		}
		folder, err := openDirWithin(root, at)
		if err != nil {
			// A folder this check cannot list may still be reached by OpenCode by name.
			return errProjectConfigTreeUnbound
		}
		defer func() {
			if folder != nil {
				_ = folder.Close()
			}
		}()
		var subdirs []subdir
		for {
			// Read in batches: a huge directory is refused before fs.ReadDir could
			// allocate for every entry it holds.
			items, readErr := folder.ReadDir(64)
			for _, d := range items {
				path, name := at+"/"+d.Name(), prefix+d.Name()
				info, err := fs.Stat(root.FS(), path) // follows a link, inside the folder only
				if err != nil {
					return errProjectConfigTreeUnbound
				}
				// Only the generated regular file at .opencode/.gitignore is
				// exempt. A directory of that name, at any depth, is configuration.
				if name == ".gitignore" && at == filepath.ToSlash(rel) && d.Type().IsRegular() {
					continue
				}
				switch {
				case info.IsDir():
					deeper := depth
					if d.Type()&fs.ModeSymlink != 0 {
						deeper++ // a link may loop: only links count toward the depth
					}
					subdirs = append(subdirs, subdir{path, name + "/", deeper})
					if dirs+len(subdirs) > maxProjectConfigTreeDirs {
						return errProjectConfigTreeUnbound
					}
					continue
				case !info.Mode().IsRegular():
					return errProjectConfigTreeUnbound
				}
				if len(files) >= maxProjectConfigTreeFiles {
					return errProjectConfigTreeUnbound
				}
				if prefix == "" && (d.Name() == "opencode.json" || d.Name() == "opencode.jsonc") {
					// OpenCode loads these through the same substituting reader as the
					// session folder's own: they are read and bound the same way, their
					// {file:} tokens from .opencode/, by canonical content without "$schema"
					// (which OpenCode writes itself), plus the values it flags.
					total += info.Size()
					if total > maxProjectConfigTreeBytes {
						return errProjectConfigTreeUnbound
					}
					sub, _ := readProjectConfigFile(root, dir, projectConfigFile{driver: providerDriverOpenCode, path: path, format: "json", rules: openCodeConfigRules})
					if sub.Refused != "" {
						refusedWhy = name + " " + sub.Refused
						return errProjectConfigTreeUnbound
					}
					files[name] = map[string]any{"sha256": sub.contentSHA256, "flagged": sub.Flagged, "unreadable": sub.Unreadable}
					continue
				}
				sum, n, err := fileSHA256Within(root, path, maxProjectConfigTreeBytes-total)
				if err != nil {
					return errProjectConfigTreeUnbound
				}
				total += n
				files[name] = "sha256:" + sum
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return errProjectConfigTreeUnbound
			}
		}
		err = folder.Close()
		folder = nil
		if err != nil {
			return errProjectConfigTreeUnbound
		}
		// Keep the old lexical traversal, but do not hold parent directories open
		// while descending. An approval's hash does not depend on this order.
		slices.SortFunc(subdirs, func(a, b subdir) int { return strings.Compare(a.at, b.at) })
		for _, sub := range subdirs {
			if err := walk(sub.at, sub.prefix, sub.depth); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(filepath.ToSlash(rel), "", 0); err != nil {
		if refusedWhy != "" {
			entry.Refused = refusedWhy
			return entry, true
		}
		entry.Refused = fmt.Sprintf("holds more than %d entries, %d bytes or %d folders, a link out of the folder, a link loop or a folder or file this check cannot read, so no approval can bind what OpenCode would load", maxProjectConfigTreeFiles, maxProjectConfigTreeBytes, maxProjectConfigTreeDirs)
		return entry, true
	}
	entry.Flagged = files
	entry.Refused = openCodeDependencyRefusal(root, rel)
	return entry, len(files) > 0 || entry.Refused != ""
}

// openCodeDependencyRefusal mirrors pinned OpenCode's Npm.install decision: it
// reifies when node_modules is absent or a declared dependency is not locked.
// Refuse that state before approval, since reify resolves and writes new code.
// A prepared tree is hashed in full above and Npm.install leaves it unchanged.
func openCodeDependencyRefusal(root *os.Root, rel string) string {
	const reason = "requires preparation: install dependencies in .opencode with the pinned OpenCode version before reviewing and approving; its automatic installer would otherwise load code outside the approved hash"
	modules, err := openDirWithin(root, filepath.ToSlash(filepath.Join(rel, "node_modules")))
	if err != nil {
		return reason
	}
	if err := modules.Close(); err != nil {
		return reason
	}

	// Maps preserve JSON's case-sensitive keys; Go struct fields would accept
	// "Packages" even though OpenCode reads only "packages" and would reinstall.
	var pkg, lock map[string]json.RawMessage
	for name, target := range map[string]any{"package.json": &pkg, "package-lock.json": &lock} {
		raw, err := readSmallRegularWithin(root, filepath.ToSlash(filepath.Join(rel, name)))
		if err != nil || json.Unmarshal(raw, target) != nil {
			return reason
		}
	}
	var packages map[string]map[string]json.RawMessage
	if json.Unmarshal(lock["packages"], &packages) != nil {
		return reason
	}
	names := func(pkg map[string]json.RawMessage) (map[string]bool, bool) {
		out := map[string]bool{}
		for _, key := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
			raw, exists := pkg[key]
			if !exists {
				continue
			}
			var deps map[string]json.RawMessage
			if json.Unmarshal(raw, &deps) != nil {
				return nil, false
			}
			for name := range deps {
				out[name] = true
			}
		}
		return out, true
	}
	locked, valid := names(packages[""])
	if !valid || !locked["@opencode-ai/plugin"] {
		return reason
	}
	declared, valid := names(pkg)
	if !valid {
		return reason
	}
	for name := range declared {
		if !locked[name] {
			return reason
		}
	}

	return ""
}

// errProjectConfigTreeUnbound is a folder no approval can bind.
var errProjectConfigTreeUnbound = errors.New("project configuration folder cannot be bound")

// openRegularWithin opens a regular file of the folder.
func openRegularWithin(root *os.Root, path string) (*os.File, error) {
	return openWithin(root, path, true)
}

// openDirWithin opens a directory of the folder.
func openDirWithin(root *os.Root, path string) (*os.File, error) {
	return openWithin(root, path, false)
}

// openWithin opens without blocking and judges the type on the open handle, so
// an entry swapped for a FIFO after a stat cannot hang the check.
func openWithin(root *os.Root, path string, regular bool) (*os.File, error) {
	f, err := root.OpenFile(filepath.FromSlash(path), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if (regular && !info.Mode().IsRegular()) || (!regular && !info.IsDir()) {
		f.Close()
		return nil, fmt.Errorf("%s is not the expected file type", path)
	}
	return f, nil
}

// readSmallRegularWithin reads a file of the folder that a configuration names: only
// a regular file is opened (a FIFO would hang the check), and only up to the parse
// limit is read (a large file is not loaded to be refused). A file over the limit
// returns what was read of it along with the error, so a caller can charge the work.
func readSmallRegularWithin(root *os.Root, path string) ([]byte, error) {
	f, err := openRegularWithin(root, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxProjectConfigBytes+1))
	if err != nil {
		return raw, err
	}
	if len(raw) > maxProjectConfigBytes {
		return raw, fmt.Errorf("%s is larger than %d bytes", path, maxProjectConfigBytes)
	}
	return raw, nil
}

// fileSHA256Within hashes at most maxBytes of a file of the folder. A file
// that grew after its stat, or that exceeds the remaining tree budget, is not
// read to EOF: its content cannot be approved within that budget.
func fileSHA256Within(root *os.Root, path string, maxBytes int64) (string, int64, error) {
	f, err := openRegularWithin(root, path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", n, err
	}
	if n > maxBytes {
		return "", n, errProjectConfigTreeUnbound
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// maxProjectConfigDepth bounds the nesting a file may have: a parser that
// recurses over a 1 MiB file of opening brackets must not take the engine down.
const maxProjectConfigDepth = 64

// exceedsConfigDepth bounds the nesting of the parsed document, counted outside
// strings and comments with the string syntax of the format being read: JSON
// and dotenv have "..." only; TOML also has single-quoted literal strings,
// triple-quoted multi-line strings of both kinds (a run of up to two quotes may
// end the content before the closing three) and # comments. A scan that took a
// quote of another syntax for a string start would skip the brackets after it.
// Every bracket nests one level. In TOML each dot of a key path nests one more,
// with no bracket: the dots of the key that opens an inline table or array add
// to its depth, and the segments of a [table] or [[array]] header add to every
// key under it, twice over once a [[array]] header was seen, because each
// segment that names an array of tables nests an array and its element. One
// sum is checked, because the parser's cost follows the depth
// of the document, not any one of its parts. A dot in a value (a float, a date)
// counts until the next , bracket or line end, which can only overcount.
func exceedsConfigDepth(raw []byte, isTOML bool) bool {
	var outer []int // the depth before each open bracket
	depth, dots, header := 0, 0, 0
	inHeader, inValue, arrays := false, false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '"' || (isTOML && c == '\''):
			i = endOfConfigString(raw, i, isTOML)
		case !isTOML:
			switch c {
			case '[', '{':
				depth++
			case ']', '}':
				depth = max(depth-1, 0)
			}
		case c == '#':
			for i+1 < len(raw) && raw[i+1] != '\n' {
				i++
			}
		case c == '.' || (inHeader && c == '['): // [[ nests the array element
			dots++
			arrays = arrays || c == '['
		case inHeader && c == ']':
			header, dots, inHeader = dots+1, 0, false
			if arrays {
				header *= 2
			}
		case c == '[' && len(outer) == 0 && !inValue:
			inHeader, header, dots = true, 0, 0
		case c == '[' || c == '{':
			outer = append(outer, depth)
			depth, dots = depth+dots+1, 0
		case c == ']' || c == '}':
			if n := len(outer); n > 0 {
				depth, outer = outer[n-1], outer[:n-1]
			}
			dots = 0
		case c == '=':
			inValue = true
		case c == ',' || c == '\n':
			dots = 0
			inValue = inValue && (c == ',' || len(outer) > 0) // a top-level [ starts a header again
		}
		if header+depth+dots > maxProjectConfigDepth {
			return true
		}
	}
	return false
}

// endOfConfigString returns the index of the last byte of the string that
// starts at raw[start]; an unterminated string ends the scan.
func endOfConfigString(raw []byte, start int, isTOML bool) int {
	quote := raw[start]
	escapes := quote == '"'
	multiline := isTOML && bytes.HasPrefix(raw[start:], bytes.Repeat([]byte{quote}, 3))
	i := start + 1
	if multiline {
		i = start + 3
	}
	for ; i < len(raw); i++ {
		switch {
		case escapes && raw[i] == '\\':
			i++
		case raw[i] == '\n' && !multiline:
			return i - 1 // a single-line string ends before the line end, which the caller reads
		case raw[i] != quote:
		case !multiline:
			return i
		case bytes.HasPrefix(raw[i:], bytes.Repeat([]byte{quote}, 3)):
			for i+3 < len(raw) && raw[i+3] == quote { // the extra quotes are content
				i++
			}
			return i + 2
		}
	}
	return len(raw)
}

func decodeProjectConfig(raw []byte, format string) (map[string]any, error) {
	if format == "json" {
		raw = stripJSONComments(raw) // before the depth scan: a comment can hold a quote
	}
	if exceedsConfigDepth(raw, format == "toml") {
		return nil, fmt.Errorf("nested deeper than %d levels", maxProjectConfigDepth)
	}
	var doc map[string]any
	switch format {
	case "dotenv":
		return decodeDotenv(raw), nil
	case "toml":
		if _, err := toml.Decode(string(raw), &doc); err != nil {
			return nil, errors.New("TOML syntax")
		}
		return normalizeConfigValue(doc).(map[string]any), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep large integers exact: the hash is over the values
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	// A second value would be read differently by a lenient parser.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("more than one JSON value")
	}
	return doc, nil
}

// decodeDotenv reads KEY=value lines the way dotenv loaders do; a malformed line
// names nothing and is ignored, as it is by them.
func decodeDotenv(raw []byte) map[string]any {
	doc := map[string]any{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if name = strings.TrimSpace(name); ok && name != "" {
			doc[name] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return doc
}

// normalizeConfigValue turns the TOML values JSON cannot carry (NaN, infinities)
// into text, so every parsed file has a hash.
func normalizeConfigValue(value any) any {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Sprintf("float:%v", v)
		}
	case map[string]any:
		for key, item := range v {
			v[key] = normalizeConfigValue(item)
		}
	case []any:
		for i, item := range v {
			v[i] = normalizeConfigValue(item)
		}
	case []map[string]any:
		for i, item := range v {
			v[i] = normalizeConfigValue(item).(map[string]any)
		}
	}
	return value
}

// meaningful reports a value that configures something: an empty table, an
// empty list, false and null configure nothing and are not flagged.
func meaningful(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case map[string]any:
		return len(v) > 0
	case []any:
		return len(v) > 0
	case []map[string]any:
		return len(v) > 0
	}
	return true
}

// flag returns the flagged subset of doc keyed by dotted path.
func (r projectConfigRules) flag(doc map[string]any) map[string]any {
	out := map[string]any{}
	if r.allEnv {
		for name, value := range doc {
			if flaggedEnvName(name) && meaningful(value) {
				out["env."+name] = value
			}
		}
		return out
	}
	for key, value := range doc {
		if (r.allowKeys != nil && !slices.Contains(r.allowKeys, key)) || slices.Contains(r.keys, key) || (r.baseURL && strings.HasSuffix(strings.ToLower(key), "_base_url")) {
			if meaningful(value) {
				out[key] = value
			}
		}
	}
	for _, path := range r.paths {
		if value, ok := lookupDotted(doc, path); ok && meaningful(value) {
			out[path] = value
		}
	}
	for _, path := range r.envKeys {
		if env, ok := lookupDotted(doc, path); ok {
			if table, ok := env.(map[string]any); ok {
				for name, value := range table {
					if flaggedEnvName(name) && meaningful(value) {
						out[path+"."+name] = value
					}
				}
			}
		}
	}
	if profiles, ok := doc["profiles"].(map[string]any); ok && r.profile {
		for name, profile := range profiles {
			if table, ok := profile.(map[string]any); ok {
				for key, value := range r.flag(table) {
					out["profiles."+name+"."+key] = value
				}
			}
		}
	}
	return out
}

func lookupDotted(doc map[string]any, path string) (any, bool) {
	var cur any = doc
	for _, part := range strings.Split(path, ".") {
		table, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = table[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// stripJSONComments removes // and /* */ comments outside strings. Gemini CLI
// settings files allow them; a commented file must not read as invalid.
func stripJSONComments(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	inString := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case inString:
			out = append(out, c)
			if c == '\\' && i+1 < len(raw) {
				i++
				out = append(out, raw[i])
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(raw) && raw[i+1] == '/':
			for i < len(raw) && raw[i] != '\n' && raw[i] != '\r' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(raw) && raw[i+1] == '*':
			end := bytes.Index(raw[i+2:], []byte("*/"))
			if end < 0 {
				return append(out, raw[i:]...) // unterminated: leave it to fail to parse
			}
			i += end + 3
		default:
			out = append(out, c)
		}
	}
	return out
}

var secretLookingKey = regexp.MustCompile(`(?i)(token|secret|password|passwd|authorization|api[_-]?key|credential|bearer|cookie)`)

// redactedForDisplay hides credentials a person reviewing the report does not
// need to read: a password inside a URL, and the value under a key that names a
// secret. The hash is always over the real values.
func redactedForDisplay(value any) any {
	switch v := value.(type) {
	case string:
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			changed := false
			if u.User != nil {
				u.User, changed = url.User("xxxxx"), true
			}
			query := u.Query()
			for name := range query {
				if secretLookingKey.MatchString(name) {
					query.Set(name, "xxxxx")
					changed = true
				}
			}
			if changed {
				u.RawQuery = query.Encode()
				return u.String()
			}
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if _, isString := item.(string); isString && secretLookingKey.MatchString(key) {
				out[key] = "xxxxx"
				continue
			}
			out[key] = redactedForDisplay(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactedForDisplay(item)
		}
		return out
	case []map[string]any: // a TOML array of tables
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactedForDisplay(item)
		}
		return out
	}
	return value
}

type projectConfigApproval struct {
	SHA256 string `json:"sha256"`
	Actor  string `json:"actor"`
	At     string `json:"at"`
}

func storedProjectConfigApproval(rec model.Record) *projectConfigApproval {
	if rec.String(colWsProjectConfigHash) == "" {
		return nil
	}
	return &projectConfigApproval{SHA256: rec.String(colWsProjectConfigHash), Actor: rec.String(colWsProjectConfigActor), At: rec.String(colWsProjectConfigAt)}
}

func (a *projectConfigApproval) covers(hash string) bool {
	return a != nil && hash != "" && a.SHA256 == hash && a.Actor != "" && a.At != ""
}

type projectConfigDTO struct {
	WorkspaceRef string                 `json:"workspace_ref"`
	Files        []projectConfigEntry   `json:"files"`
	SHA256       string                 `json:"sha256"`
	Approved     bool                   `json:"approved"`
	Approval     *projectConfigApproval `json:"approval,omitempty"`
}

// handleGetProjectConfig lists the repo-resident agent configuration of a registered folder that runs programs or redirects traffic, with the SHA-256 of that flagged part and whether a person approved it; the files are read and never executed.
func (m *Module) handleGetProjectConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.workspaceAllowed(w, r, mc, permWsAdmin, auth.TrusteeRead) {
		return
	}
	ref := chi.URLParam(r, "ref")
	ws, err := m.resolveWorkspace(r.Context(), mc.Tenant, ref)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	rec, err := m.loadWorkspaceRec(r.Context(), mc.Tenant, ref)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	entries, hash, err := readProjectConfig(ws.rootReal)
	if err != nil {
		writeRunErr(w, denyClosedErr("project configuration could not be read", err))
		return
	}
	approval := storedProjectConfigApproval(rec)
	shown := make([]projectConfigEntry, len(entries))
	for i, e := range entries {
		shown[i] = e
		if e.Flagged != nil {
			shown[i].Flagged = redactedForDisplay(e.Flagged).(map[string]any)
		}
	}
	writeJSON(w, http.StatusOK, projectConfigDTO{WorkspaceRef: ref, Files: shown, SHA256: hash, Approved: approval.covers(hash), Approval: approval})
}

// handleApproveProjectConfig records that a person approved the SHA-256 of a folder's flagged agent configuration, so sessions of the tools that read it may start until one character of it changes.
func (m *Module) handleApproveProjectConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.workspaceAllowed(w, r, mc, permWsAdmin, auth.TrusteeModify) {
		return
	}
	var body struct {
		SHA256 string `json:"sha256"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if decoded, err := hex.DecodeString(body.SHA256); err != nil || len(decoded) != 32 || body.SHA256 != strings.ToLower(body.SHA256) {
		writeRunErr(w, badRequest("sha256 must name the exact configuration reviewed (64 lowercase hexadecimal characters)"))
		return
	}
	// A person approves. Neither an agent nor a session the engine runs can
	// approve the configuration its own next session will read.
	if p := mc.Principal; p.AgentIdentity != "" || p.SessionIdentity != "" || p.IsWorkSessionCredential() || p.UserID.IsZero() {
		writeRunErr(w, forbiddenErr("a person must approve the project configuration"))
		return
	}
	approval, err := m.approveProjectConfig(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), body.SHA256, mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

// approveProjectConfig stores who approved which hash and when, with the audit
// entry in the same transaction. The configuration itself is not stored: a
// launch compares the hash of what it finds, so any change voids the approval.
// The newest approval replaces the previous one.
func (m *Module) approveProjectConfig(ctx context.Context, tenant model.TenantID, ref, hash, actor, actorKind string) (projectConfigApproval, error) {
	if m.Data == nil {
		return projectConfigApproval{}, &runErr{http.StatusServiceUnavailable, "workspace registry not available"}
	}
	var out projectConfigApproval
	err := m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		rec, err := findWorkspaceRec(ctx, repo, ref)
		if err != nil {
			return err
		}
		locker, ok := repo.(store.RowLocker[model.Record])
		if !ok {
			return &runErr{http.StatusServiceUnavailable, "workspace registry cannot lock the folder record"}
		}
		if rec, err = locker.Lock(ctx, model.ID(rec.String(model.ColID))); err != nil {
			return err
		}
		out = projectConfigApproval{SHA256: hash, Actor: actor, At: model.NewTimestamp(m.now()).String()}
		rec[colWsProjectConfigHash], rec[colWsProjectConfigActor], rec[colWsProjectConfigAt] = out.SHA256, out.Actor, out.At
		if _, err = repo.Update(ctx, rec); err != nil {
			return err
		}
		return appendWorkspaceAudit(ctx, sc, wsMutationInput{workspaceID: model.ID(rec.String(model.ColID)), workspaceRef: ref, op: "approve-project-config", path: rec.String(colWsRootPath), contentHash: hash, actor: actor, actorKind: actorKind})
	})
	return out, err
}

// projectConfigVerdict is what a launch found in the directory its tool runs
// in. refusal is nil when nothing the tool reads is flagged, or when a person
// approved exactly this content.
type projectConfigVerdict struct {
	refusal error
	files   []string
	hash    string
}

// claudeHooksMayApply says whether a Claude launch can carry the engine's hooks
// and so --setting-sources "". It is decided for good once the launch gate has
// granted its environment (claudeHooksApply).
func (m *Module) claudeHooksMayApply(p CreateRunParams) bool {
	return p.ProviderHome != nil && launchDriverKey(p) == providerDriverClaude && p.Transport == TransportStreamJSON && m.rt.hookDataDir != ""
}

// checkProjectConfig reads the directory the child will run in (p.WorkspaceDir:
// the folder, or the session's own worktree) and compares the hash of its
// flagged configuration with the folder's approval, for the files of this
// launch's tool. It writes nothing. A directory with no registered folder has
// no record to hold an approval, so flagged files there are refused outright.
func (m *Module) checkProjectConfig(ctx context.Context, tenant model.TenantID, p CreateRunParams, ws *resolvedWorkspace) projectConfigVerdict {
	if p.WorkspaceDir == "" || p.ProviderHome == nil {
		return projectConfigVerdict{}
	}
	driver := launchDriverKey(p)
	entries, hash, err := readProjectConfig(p.WorkspaceDir)
	if err != nil {
		return projectConfigVerdict{refusal: denyClosedErr("project configuration could not be read", err)}
	}
	v := projectConfigVerdict{hash: hash}
	var refused []string
	for _, e := range entries {
		flagged, ok := projectConfigFlaggedFor(e, driver)
		if !ok {
			continue
		}
		if e.Refused != "" {
			refused = append(refused, e.File+" "+e.Refused)
		}
		name := e.File
		if len(flagged) > 0 {
			keys := make([]string, 0, len(flagged))
			for key := range flagged {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			name += ": " + strings.Join(keys, ", ")
		}
		v.files = append(v.files, name)
	}
	if len(v.files) == 0 {
		return projectConfigVerdict{}
	}
	if len(refused) > 0 {
		v.refusal = conflictErr(fmt.Sprintf("This folder carries %s configuration that cannot be approved: %s. Change or remove what it names, then start the session again.", driver, strings.Join(refused, "; ")))
		return v
	}
	if ws == nil {
		v.refusal = conflictErr(fmt.Sprintf("This session has no registered folder to hold an approval, and its directory carries %s configuration (%s) that can run programs or redirect traffic; remove it, or run the session in a registered folder.", driver, strings.Join(v.files, "; ")))
		return v
	}
	rec, err := m.loadWorkspaceRec(ctx, tenant, ws.ref)
	if err != nil {
		return projectConfigVerdict{refusal: denyClosedErr("project configuration approval could not be read", err)}
	}
	if storedProjectConfigApproval(rec).covers(hash) {
		return projectConfigVerdict{}
	}
	v.refusal = conflictErr(fmt.Sprintf("This folder carries %s configuration (%s) that can run programs or redirect traffic and no one approved it; review it with olivares agent workspace project-config %s, then run olivares agent workspace approve-project-config %s --sha256 %s.", driver, strings.Join(v.files, "; "), ws.ref, ws.ref, hash))
	return v
}

// projectConfigStartError is the decision once the launch gate has granted its
// environment. A Claude launch that carries the engine's hooks excludes every
// project source itself (--setting-sources ""), so there is nothing for the
// tool to honor: the engine records that it saw flagged files and lets the
// start go. A launch of any other form of a tool that reads them is refused.
func (m *Module) projectConfigStartError(ctx context.Context, tenant model.TenantID, p CreateRunParams, ws *resolvedWorkspace, v projectConfigVerdict, injected []EnvVar) error {
	if v.refusal == nil {
		return nil
	}
	if !m.claudeHooksApply(p, injected) {
		return v.refusal
	}
	if ws != nil && m.Data != nil && v.hash != "" {
		err := m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
			return appendWorkspaceAudit(ctx, sc, wsMutationInput{
				workspaceID: ws.id, workspaceRef: ws.ref, op: "project-config-excluded", path: p.WorkspaceDir, contentHash: v.hash,
				actor: p.Actor, actorKind: p.ActorKind, meta: map[string]any{"files": v.files},
			})
		})
		if err != nil {
			m.warnf("project configuration exclusion was not recorded", "workspace_ref", ws.ref, "err", redactErr(err))
		}
	}
	return nil
}
