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
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
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
)

// projectConfigFile is one repo-resident file a tool reads, relative to the
// folder the session runs in. The order of the table is the canonical order of
// the hash.
type projectConfigFile struct {
	driver string
	path   string
	format string // "json" (comments allowed), "toml" or "dotenv"
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
}

var claudeSettingsRules = projectConfigRules{
	keys: []string{
		"hooks", "apiKeyHelper", "statusLine", "mcpServers", "enableAllProjectMcpServers", "enabledMcpjsonServers",
		"awsAuthRefresh", "awsCredentialExport", "otelHeadersHelper", "fileSuggestion",
		"enabledPlugins", "extraKnownMarketplaces",
	},
	envKeys: []string{"env"},
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
	// Gemini CLI loads these as environment variables, where its base URL is set.
	{driver: providerDriverGemini, path: ".gemini/.env", format: "dotenv", rules: projectConfigRules{allEnv: true}},
	{driver: providerDriverGemini, path: ".env", format: "dotenv", rules: projectConfigRules{allEnv: true}},
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
}

func projectConfigFileDriver(path string) string {
	for _, f := range projectConfigFiles {
		if f.path == path {
			return f.driver
		}
	}
	return ""
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
	info, err := root.Stat(rel)
	if errors.Is(err, os.ErrNotExist) {
		if _, lerr := root.Lstat(rel); lerr != nil {
			return entry, false // no such file, and no dangling link either
		}
	}
	if err != nil {
		// A link that leaves the folder, a loop or a permission error: the tool
		// follows it where this reader may not, so it is flagged by what names it.
		if target, lerr := os.Readlink(filepath.Join(dir, rel)); lerr == nil {
			return unreadable("symbolic link to %s cannot be read inside the folder", target)
		}
		return unreadable("cannot be read inside the folder")
	}
	if !info.Mode().IsRegular() {
		return unreadable("is not a regular file (%s)", strings.ToLower(info.Mode().Type().String()))
	}
	file, err := root.Open(rel)
	if err != nil {
		return unreadable("cannot be opened")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxProjectConfigBytes+1))
	if err != nil {
		return unreadable("cannot be read")
	}
	if len(raw) > maxProjectConfigBytes {
		// The hash covers every byte: a change anywhere voids an approval.
		sum := sha256.New()
		sum.Write(raw)
		if _, err := io.Copy(sum, file); err != nil {
			return unreadable("cannot be read")
		}
		return unreadable("is larger than %d bytes (%d bytes); sha256 of its content %s", maxProjectConfigBytes, info.Size(), hex.EncodeToString(sum.Sum(nil)))
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(raw)) == 0 {
		return entry, false
	}
	doc, err := decodeProjectConfig(raw, f.format)
	if err != nil {
		return unreadable("is not valid %s (%v); sha256 of its content %s", f.format, err, sha256Hex(raw))
	}
	entry.Flagged = f.rules.flag(doc)
	return entry, len(entry.Flagged) > 0
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
		if slices.Contains(r.keys, key) || (r.baseURL && strings.HasSuffix(strings.ToLower(key), "_base_url")) {
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
			for i < len(raw) && raw[i] != '\n' {
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
	for _, e := range entries {
		if projectConfigFileDriver(e.File) != driver {
			continue
		}
		name := e.File
		if len(e.Flagged) > 0 {
			keys := make([]string, 0, len(e.Flagged))
			for key := range e.Flagged {
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
