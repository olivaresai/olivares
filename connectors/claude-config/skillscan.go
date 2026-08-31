// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeconfig

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// skillscan.go is the Agent Skills POSTURE/PROVENANCE SCANNER (CUR-7): the
// SKILL.md sibling of the MCP posture scanner (connectors/mcp/posture.go).
// Where the CLA-14 feeder (feeder.go) only DECLARES that a skill exists (an edge
// carrying the directory name, never content), this scanner READS each discovered
// SKILL.md — UNTRUSTED, repo- or marketplace-supplied content that becomes agent
// instructions — and grades its posture:
//
//   - spec conformance against the agentskills.io open spec (VERIFIED 2026-06-10):
//     frontmatter is YAML with EXACTLY {name, description, license, allowed-tools,
//     metadata, compatibility}; name 1–64 lowercase/digits/hyphens matching the
//     directory; description 1–1024 required; compatibility ≤500;
//   - allowed-tools breadth — the spec marks the field experimental PRE-APPROVAL,
//     not restriction (Claude Code: "every tool remains callable"; it only
//     suppresses prompts), so a skill pre-approving unrestricted shell is a
//     self-granted permission escalation;
//   - instruction-injection / hidden-Unicode / secret shapes in the description
//     (tier-1: loaded into EVERY session) and the body (tier-2 instructions),
//     via the shared connectors/internal/textscan primitives;
//   - load-time execution: Claude Code's !`cmd` dynamic-context-injection lines
//     run a shell BEFORE the model sees the content; curl|sh remote-exec in the
//     body; bundled scripts/ (which run with the agent's tool permissions — the
//     spec defines NO sandbox and NO signing/integrity mechanism);
//   - marketplace provenance (B1): a skill bundled by a plugin listed in a
//     local marketplace catalog inherits that provenance; an operator allowlist
//     of known marketplace names turns an unknown origin into a finding.
//
// Output is MINIMAL-DATA (docs/SECURITY-HARDENING.md), exactly like posture.go: one finding per
// issue (sanitized title + hashed detail — never the skill text, never a secret)
// plus one per-skill posture-score summary (grade A–F on the scale). The
// scanner emits findings about content; it still never EMITS content.

// Finding vocabulary.
const (
	// findingSkillPosture marks a per-issue skill posture finding and the
	// per-skill score summary (the skill_posture analog of mcp_posture).
	findingSkillPosture = "skill_posture"
	// subjectSkill aligns with the resSkill declared-capability edges so the two
	// signals describe the same entity.
	subjectSkill = resSkill
)

// maxSkillBytes caps how much of a SKILL.md is read for scanning. The spec
// recommends bodies under 500 lines / ~5k tokens, so 256 KiB covers any
// legitimate skill; a larger file is scanned to the cap and the truncation is
// itself surfaced as a finding (a partial scan must never look complete).
const maxSkillBytes = 256 * 1024

// maxScriptEntries bounds the per-skill directory walk (a hostile skill must not
// drag discovery into an unbounded tree).
const maxScriptEntries = 2048

// skillSpecFields is the CLOSED frontmatter field set of the agentskills.io spec
// (the reference validator rejects anything else; VERIFIED 2026-06-10).
var skillSpecFields = map[string]bool{
	"name": true, "description": true, "license": true,
	"allowed-tools": true, "metadata": true, "compatibility": true,
}

// Spec limits (agentskills.io, VERIFIED 2026-06-10).
const (
	specNameMax        = 64
	specDescriptionMax = 1024
	specCompatMax      = 500
)

// dynamicInjectionRe matches Claude Code's !`command` dynamic context injection:
// a line whose content is executed by a shell BEFORE the model sees the skill
// (preprocessing) — load-time code execution, not model-mediated.
var dynamicInjectionRe = regexp.MustCompile("(?m)^\\s*!`[^`]+`")

// curlPipeShellRe matches the canonical remote-exec pattern: piping a download
// straight into a shell.
var curlPipeShellRe = regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^|\n]{0,200}\|\s*(?:ba|z|da)?sh\b`)

// pkgRunnerRe matches ephemeral package runners (npx/uvx) — code resolved from a
// remote registry at use time.
var pkgRunnerRe = regexp.MustCompile(`(?i)\b(?:npx|uvx)\s+\S`)

// scriptExts classifies bundled files as executable scripts (auditable text) per
// the spec's scripts/ convention.
var scriptExts = map[string]string{
	".sh": "sh", ".bash": "sh", ".zsh": "sh", ".py": "py", ".js": "js",
	".ts": "ts", ".mjs": "js", ".rb": "rb", ".pl": "pl", ".ps1": "ps1",
	".bat": "bat", ".cmd": "bat",
}

// binaryExts classifies bundled files as OPAQUE binaries (unauditable surface).
var binaryExts = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true,
	".wasm": true, ".o": true, ".a": true, ".pyc": true,
}

// skillSeverityPenalty / skillGrade replicate the posture scale (score =
// 100 − penalties; A≥90 B≥75 C≥60 D≥40 else F) so a skill grade reads on the
// same scale as an MCP server grade. Kept per-surface (not shared) on purpose:
// the weighting may legitimately diverge per artifact class.
var skillSeverityPenalty = map[model.Severity]int{
	model.SeverityCritical: 40,
	model.SeverityHigh:     25,
	model.SeverityMedium:   12,
	model.SeverityLow:      5,
	model.SeverityInfo:     0,
}

func skillGrade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 75:
		return "B"
	case score >= 60:
		return "C"
	case score >= 40:
		return "D"
	default:
		return "F"
	}
}

// skillProvenance is where a discovered skill came FROM: directly from a config
// tree (zero value), bundled by a plugin, and — when that plugin is listed in a
// local marketplace catalog — via that marketplace (B1 provenance).
type skillProvenance struct {
	plugin      string
	marketplace string
}

// skillIssue is one detected problem, accumulated before becoming a finding.
// detailKey is a stable, non-sensitive string hashed into DetailHash (dedup
// without persisting the offending text). The optional OWASP sets ride the
// Taxonomy fields.
type skillIssue struct {
	severity  model.Severity
	title     string
	detailKey string
	owaspASI  []string
	owaspLLM  []string
}

// parsedSkill is the scanned, minimal view of one skill directory.
type parsedSkill struct {
	dir       string         // directory name — the identity clients key on
	ref       string         // qualified ref for findings ("plugin:skill" or "skill")
	fm        map[string]any // raw frontmatter map; nil = absent/unparseable
	body      string
	truncated bool
	prov      skillProvenance
	scripts   int
	classes   []string
	binaries  int
}

// scanSkillDir reads one skill directory, derives its posture issues and emits
// one finding per issue plus the per-skill score summary. An unreadable SKILL.md
// is skipped silently (the declaration edge already exists; discovery never
// aborts on one bad file — feeder.go convention).
func (f *Feeder) scanSkillDir(ctx context.Context, sink sdk.Sink, dir, name string, prov skillProvenance, at time.Time) error {
	s, ok := parseSkillDir(dir, name, prov)
	if !ok {
		return nil
	}
	issues := skillIssues(s, f.knownMarketplaces)
	issues = append(issues, skillAuthorizationIssue(name, prov, f.authorizedSkills, f.knownMarketplaces)...)
	score := 100
	worst := model.SeverityInfo
	counted := 0
	for _, is := range issues {
		if err := sink.Emit(ctx, skillIssueFinding(s.ref, is, at)); err != nil {
			return err
		}
		score -= skillSeverityPenalty[is.severity]
		if is.severity != model.SeverityInfo {
			counted++
		}
		if is.severity.AtLeast(worst) {
			worst = is.severity
		}
	}
	if score < 0 {
		score = 0
	}
	return sink.Emit(ctx, skillScoreFinding(s.ref, score, counted, worst, at))
}

// parseSkillDir loads SKILL.md (capped) and inventories the skill's bundled
// files. ok=false when SKILL.md cannot be read at all.
func parseSkillDir(dir, name string, prov skillProvenance) (parsedSkill, bool) {
	raw, truncated, ok := readCapped(filepath.Join(dir, "SKILL.md"), maxSkillBytes)
	if !ok {
		return parsedSkill{}, false
	}
	s := parsedSkill{dir: name, ref: name, prov: prov, truncated: truncated}
	if prov.plugin != "" {
		// Claude Code namespaces a plugin-bundled skill as "plugin:skill".
		s.ref = prov.plugin + ":" + name
	}
	fmBlock, body := splitFrontmatter(raw)
	s.body = body
	if fmBlock != nil {
		var fm map[string]any
		if yaml.Unmarshal(fmBlock, &fm) == nil && fm != nil {
			s.fm = fm
		}
	}
	s.scripts, s.classes, s.binaries = inventorySkillFiles(dir)
	return s, true
}

// splitFrontmatter splits a SKILL.md into its YAML frontmatter block (nil when
// absent) and the markdown body, mirroring readFrontmatter's fence handling.
func splitFrontmatter(raw string) (fmBlock []byte, body string) {
	s := strings.TrimPrefix(raw, "\ufeff")
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != "---" {
		return nil, s
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(strings.TrimSuffix(lines[i], "\r")) == "---" {
			return []byte(strings.Join(lines[1:i], "\n")), strings.Join(lines[i+1:], "\n")
		}
	}
	return nil, s
}

// inventorySkillFiles walks a skill directory (bounded) counting bundled script
// files by language class and opaque binaries. It reads NAMES only, never file
// contents (the inventory is the signal; auditing script bodies is out of scope
// and would not be minimal-data).
func inventorySkillFiles(dir string) (scripts int, classes []string, binaries int) {
	seen := map[string]struct{}{}
	entries := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		entries++
		if entries > maxScriptEntries {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if class, ok := scriptExts[ext]; ok {
			scripts++
			seen[class] = struct{}{}
			return nil
		}
		if binaryExts[ext] {
			binaries++
		}
		return nil
	})
	classes = make([]string, 0, len(seen))
	for c := range seen {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	return scripts, classes, binaries
}

// skillIssues derives the posture issues for one parsed skill. It is a pure
// function (deterministic; testable without a filesystem beyond the parse).
func skillIssues(s parsedSkill, knownMarketplaces map[string]struct{}) []skillIssue {
	var out []skillIssue
	add := func(sev model.Severity, title, key string) {
		out = append(out, skillIssue{severity: sev, title: title, detailKey: key})
	}

	// --- frontmatter: spec conformance (agentskills.io) -----------------------
	if s.fm == nil {
		add(model.SeverityMedium,
			"SKILL.md has no parseable YAML frontmatter (the spec requires name + description)",
			"no-frontmatter")
	} else {
		out = append(out, frontmatterIssues(s)...)
		// A secret anywhere in the frontmatter (license/metadata/description) —
		// the whole block is checked at once so a token in ANY field is caught.
		if fmRaw := rawFrontmatterString(s.fm); redact.ContainsSecret(fmRaw) {
			add(model.SeverityHigh,
				"skill frontmatter embeds a credential/secret shape — secret exposure",
				"secret-in-frontmatter")
		}
	}

	// --- identity: the directory name is what clients key on ------------------
	if classes, n := textscan.ScanInvisible(s.dir); n > 0 {
		add(model.SeverityHigh,
			"skill directory name contains "+strconv.Itoa(n)+" hidden character(s) ["+strings.Join(classes, ",")+"] — spoofing/poisoning",
			"invisible-dirname classes="+strings.Join(classes, ","))
	}
	if scripts, confusable := textscan.MixedScript(s.dir); confusable {
		add(model.SeverityHigh,
			"skill directory name mixes scripts ["+strings.Join(scripts, ",")+"] — homoglyph impersonation candidate",
			"homoglyph-dirname scripts="+strings.Join(scripts, ","))
	}

	// --- body: tier-2 instructions ---------------------------------------------
	for _, id := range textscan.InstructionFileMarkers(textscan.ScanInjection(s.body)) {
		out = append(out, skillIssue{
			severity:  textscan.MarkerSeverity(id),
			title:     "skill body contains an instruction-injection marker [" + id + "]",
			detailKey: "injection-body rule=" + id,
			owaspASI:  []string{"ASI01"},
			owaspLLM:  []string{"LLM01:2025"},
		})
	}
	if classes, n := textscan.ScanInvisible(s.body); n > 0 {
		add(model.SeverityHigh,
			"skill body hides "+strconv.Itoa(n)+" invisible character(s) ["+strings.Join(classes, ",")+"] — concealed instruction",
			"invisible-body classes="+strings.Join(classes, ","))
	}
	if redact.ContainsSecret(s.body) {
		add(model.SeverityHigh,
			"skill body embeds a credential/secret shape — secret exposure",
			"secret-in-body")
	}
	if n := len(dynamicInjectionRe.FindAllString(s.body, -1)); n > 0 {
		add(model.SeverityHigh,
			"skill executes shell at LOAD time via "+strconv.Itoa(n)+" !`…` dynamic-context-injection line(s) (runs before the model sees the content)",
			"dynamic-injection count="+strconv.Itoa(n))
	}
	if curlPipeShellRe.MatchString(s.body) {
		add(model.SeverityHigh,
			"skill body pipes a remote download into a shell (curl|sh remote-exec)",
			"curl-pipe-shell")
	}
	if pkgRunnerRe.MatchString(s.body) {
		add(model.SeverityLow,
			"skill body invokes an ephemeral package runner (npx/uvx) — code resolved from a remote registry at use time",
			"pkg-runner")
	}
	if s.truncated {
		add(model.SeverityLow,
			"SKILL.md exceeds "+strconv.Itoa(maxSkillBytes/1024)+" KiB; only the first "+strconv.Itoa(maxSkillBytes/1024)+" KiB was scanned (partial scan)",
			"truncated")
	}

	// --- bundled files ----------------------------------------------------------
	if s.scripts > 0 {
		add(model.SeverityInfo,
			"skill bundles "+strconv.Itoa(s.scripts)+" script file(s) ["+strings.Join(s.classes, ",")+"] — scripts run with the agent's tool permissions (the spec defines no sandbox)",
			"scripts count="+strconv.Itoa(s.scripts)+" classes="+strings.Join(s.classes, ","))
	}
	if s.binaries > 0 {
		add(model.SeverityMedium,
			"skill bundles "+strconv.Itoa(s.binaries)+" opaque binary file(s) — unauditable executable surface",
			"binaries count="+strconv.Itoa(s.binaries))
	}

	// --- provenance (B1) ---------------------------------------------------
	out = append(out, provenanceIssues(s.prov, knownMarketplaces)...)
	return out
}

// frontmatterIssues checks the parsed frontmatter against the agentskills.io
// spec and the allowed-tools pre-approval surface.
func frontmatterIssues(s parsedSkill) []skillIssue {
	var out []skillIssue
	add := func(sev model.Severity, title, key string) {
		out = append(out, skillIssue{severity: sev, title: title, detailKey: key})
	}

	name := fmString(s.fm, "name")
	switch {
	case strings.TrimSpace(name) == "":
		add(model.SeverityMedium, "frontmatter `name` is missing/empty (spec-required)", "name-missing")
	default:
		if classes, n := textscan.ScanInvisible(name); n > 0 {
			add(model.SeverityHigh,
				"frontmatter name contains "+strconv.Itoa(n)+" hidden character(s) ["+strings.Join(classes, ",")+"] — spoofing/poisoning",
				"invisible-name classes="+strings.Join(classes, ","))
		}
		if scripts, confusable := textscan.MixedScript(name); confusable {
			add(model.SeverityHigh,
				"frontmatter name mixes scripts ["+strings.Join(scripts, ",")+"] — homoglyph impersonation candidate",
				"homoglyph-name scripts="+strings.Join(scripts, ","))
		}
		if msg := skillNameViolation(name); msg != "" {
			add(model.SeverityLow, "frontmatter name "+msg+" (agentskills.io naming rules)", "name-rule "+msg)
		}
		// Plain comparison (the reference validator compares NFKC-normalized;
		// stdlib-only, so an NFKC-equivalent-but-different name still flags —
		// over-reporting is the safe side for an identity mismatch). Both values
		// are attacker-controlled: sanitized before they reach a title.
		if name != s.dir {
			add(model.SeverityMedium,
				"frontmatter name "+quoteSafe(textscan.SanitizeDisplay(name))+" does not match the skill directory "+quoteSafe(textscan.SanitizeDisplay(s.dir))+" (clients key the skill by its directory)",
				"name-dir-mismatch")
		}
	}

	desc := fmString(s.fm, "description")
	switch {
	case strings.TrimSpace(desc) == "":
		add(model.SeverityMedium,
			"frontmatter `description` is missing/empty (spec-required; clients skip or mis-trigger the skill)",
			"description-missing")
	default:
		if len(desc) > specDescriptionMax {
			add(model.SeverityLow,
				"frontmatter description exceeds the spec maximum of "+strconv.Itoa(specDescriptionMax)+" characters",
				"description-too-long")
		}
		// The description is TIER-1 content: loaded into EVERY session at start,
		// so it is scanned with the FULL marker set (it has no business
		// instructing the agent at all — that is the body's job).
		for _, id := range textscan.ScanInjection(desc) {
			out = append(out, skillIssue{
				severity:  textscan.MarkerSeverity(id),
				title:     "skill description contains an instruction-injection marker [" + id + "] — loaded into every session",
				detailKey: "injection-description rule=" + id,
				owaspASI:  []string{"ASI01"},
				owaspLLM:  []string{"LLM01:2025"},
			})
		}
		if classes, n := textscan.ScanInvisible(desc); n > 0 {
			add(model.SeverityHigh,
				"skill description hides "+strconv.Itoa(n)+" invisible character(s) ["+strings.Join(classes, ",")+"] — concealed instruction",
				"invisible-description classes="+strings.Join(classes, ","))
		}
	}

	if compat := fmString(s.fm, "compatibility"); len(compat) > specCompatMax {
		add(model.SeverityLow,
			"frontmatter compatibility exceeds the spec maximum of "+strconv.Itoa(specCompatMax)+" characters",
			"compatibility-too-long")
	}

	if extras := nonSpecFields(s.fm); len(extras) > 0 {
		add(model.SeverityLow,
			"frontmatter carries non-spec field(s) ["+strings.Join(extras, ",")+"] — the reference validator rejects them; behavior is client-specific",
			"non-spec-fields "+strings.Join(extras, ","))
	}

	out = append(out, allowedToolsIssues(s.fm)...)
	return out
}

// allowedToolsIssues grades the allowed-tools pre-approval surface. The spec
// marks the field EXPERIMENTAL pre-approval — Claude Code documents that it
// does NOT restrict tools, it only suppresses permission prompts for the listed
// ones — so a broad grant is a self-granted permission escalation, not a
// sandbox.
func allowedToolsIssues(fm map[string]any) []skillIssue {
	raw, present := fm["allowed-tools"]
	if !present {
		return nil
	}
	str, ok := raw.(string)
	if !ok {
		return []skillIssue{{
			severity:  model.SeverityLow,
			title:     "frontmatter allowed-tools is not the spec's space-separated string form",
			detailKey: "allowed-tools-not-string",
		}}
	}
	var out []skillIssue
	for _, entry := range strings.Fields(str) {
		safe := textscan.SanitizeDisplay(entry)
		switch {
		case entry == "*" || entry == "Bash" || entry == "Bash(*)" || entry == "Bash(*:*)":
			out = append(out, skillIssue{
				severity:  model.SeverityHigh,
				title:     "skill pre-approves unrestricted shell/all tools via allowed-tools entry " + quoteSafe(safe) + " (pre-approval, not restriction — self-granted escalation)",
				detailKey: "allowed-tools-shell entry=" + safe,
				owaspASI:  []string{"ASI02"},
			})
		case entry == "Write" || entry == "Edit" || entry == "MultiEdit" || entry == "NotebookEdit":
			out = append(out, skillIssue{
				severity:  model.SeverityMedium,
				title:     "skill pre-approves unrestricted file writes via allowed-tools entry " + quoteSafe(safe),
				detailKey: "allowed-tools-write entry=" + safe,
				owaspASI:  []string{"ASI02"},
			})
		case entry == "WebFetch" || entry == "WebSearch":
			out = append(out, skillIssue{
				severity:  model.SeverityMedium,
				title:     "skill pre-approves unrestricted network access via allowed-tools entry " + quoteSafe(safe) + " (exfiltration channel)",
				detailKey: "allowed-tools-net entry=" + safe,
				owaspASI:  []string{"ASI02"},
			})
		}
	}
	return out
}

// provenanceIssues derives the marketplace-provenance signals (B1): a
// marketplace-delivered skill outside the operator's known-marketplace
// allowlist is a supply-chain finding; with no allowlist configured the
// provenance is inventoried honestly (Info), never guessed clean.
func provenanceIssues(prov skillProvenance, known map[string]struct{}) []skillIssue {
	switch {
	case prov.marketplace != "":
		safeMk := textscan.SanitizeDisplay(prov.marketplace)
		safePl := textscan.SanitizeDisplay(prov.plugin)
		if known != nil {
			if _, ok := known[prov.marketplace]; !ok {
				return []skillIssue{{
					severity:  model.SeverityMedium,
					title:     "skill originates from plugin " + quoteSafe(safePl) + " via marketplace " + quoteSafe(safeMk) + " — NOT on the operator marketplace allowlist",
					detailKey: "marketplace-unlisted mk=" + safeMk + " plugin=" + safePl,
				}}
			}
		}
		return []skillIssue{{
			severity:  model.SeverityInfo,
			title:     "skill provenance: plugin " + quoteSafe(safePl) + " via marketplace " + quoteSafe(safeMk),
			detailKey: "marketplace-provenance mk=" + safeMk + " plugin=" + safePl,
		}}
	case prov.plugin != "":
		safePl := textscan.SanitizeDisplay(prov.plugin)
		return []skillIssue{{
			severity:  model.SeverityInfo,
			title:     "skill provenance: plugin " + quoteSafe(safePl) + " (no marketplace catalog observed)",
			detailKey: "plugin-provenance plugin=" + safePl,
		}}
	default:
		return nil // a local project/user skill: the workspace itself is the provenance
	}
}

// skillAuthorizationIssue checks whether a discovered skill is authorized under
// the two-tier fleet policy:
//
//  1. Explicit name match (highest priority) — the operator's per-name override.
//  2. Marketplace provenance (primary fallback) — a skill from an authorized
//     marketplace passes without an explicit entry.
//  3. Neither matched when a policy IS configured → supply-chain finding.
//
// Both authorizedSkills and knownMarketplaces nil → no policy, inventory only.
// Olivares INVENTORIES and SIGNALS; enforcement is on the host (Claude Code).
func skillAuthorizationIssue(name string, prov skillProvenance, authorizedSkills, knownMarketplaces map[string]struct{}) []skillIssue {
	if authorizedSkills == nil && knownMarketplaces == nil {
		return nil
	}
	// 1. Explicit name match.
	if authorizedSkills != nil {
		if _, ok := authorizedSkills[name]; ok {
			return nil // explicitly authorized
		}
	}
	// 2. Marketplace provenance fallback.
	if prov.marketplace != "" && knownMarketplaces != nil {
		if _, ok := knownMarketplaces[prov.marketplace]; ok {
			return nil // authorized by marketplace provenance
		}
	}
	// 3. A local project skill when only knownMarketplaces is set (no per-name
	// policy) is not an authorization violation — the marketplace check does not
	// apply to local skills.
	if authorizedSkills == nil {
		return nil
	}
	safeName := textscan.SanitizeDisplay(name)
	return []skillIssue{{
		severity:  model.SeverityMedium,
		title:     "NOT on the fleet authorized-skills list (name or marketplace provenance)",
		detailKey: "skill-unauthorized name=" + safeName,
	}}
}

// skillNameViolation reports the first agentskills.io naming rule the name
// breaks, or "". Non-ASCII lowercase letters are tolerated (the reference
// validator accepts i18n letters after NFKC).
func skillNameViolation(name string) string {
	if len(name) > specNameMax {
		return "exceeds 64 characters"
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return "starts or ends with a hyphen"
	}
	if strings.Contains(name, "--") {
		return "contains consecutive hyphens"
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		case r >= 'A' && r <= 'Z':
			return "contains uppercase characters"
		case r < 0x80:
			return "contains a non-alphanumeric ASCII character"
		}
	}
	return ""
}

// nonSpecFields returns the sorted frontmatter keys outside the spec's closed
// field set (capped for a sane title).
func nonSpecFields(fm map[string]any) []string {
	var out []string
	for k := range fm {
		if !skillSpecFields[k] {
			out = append(out, textscan.SanitizeDisplay(k))
		}
	}
	sort.Strings(out)
	if len(out) > 8 {
		out = append(out[:8], "…")
	}
	return out
}

// skillIssueFinding builds one minimal-data finding for a detected issue.
func skillIssueFinding(ref string, is skillIssue, at time.Time) model.FindingReport {
	safeRef := textscan.SanitizeDisplay(ref)
	return model.FindingReport{
		Kind:        findingSkillPosture,
		Severity:    is.severity,
		SubjectKind: subjectSkill,
		SubjectRef:  safeRef,
		Title:       "skill " + quoteSafe(safeRef) + ": " + is.title,
		DetailHash:  redact.Hash("skill-posture skill=" + safeRef + " " + is.detailKey),
		OccurredAt:  at,
		OWASPASI:    is.owaspASI,
		OWASPLLM:    is.owaspLLM,
	}
}

// skillScoreFinding builds the per-skill posture-score summary (scale). A
// clean skill scores 100 / grade A at Info; otherwise the severity is the worst
// issue found. Info-grade inventory issues count toward neither the issue count
// nor the grade label severity, but their (zero) penalty keeps the formula
// uniform.
func skillScoreFinding(ref string, score, issues int, worst model.Severity, at time.Time) model.FindingReport {
	grade := skillGrade(score)
	sev := model.SeverityInfo
	if issues > 0 {
		sev = worst
	}
	safeRef := textscan.SanitizeDisplay(ref)
	return model.FindingReport{
		Kind:        findingSkillPosture,
		Severity:    sev,
		SubjectKind: subjectSkill,
		SubjectRef:  safeRef,
		Title: "Skill posture: grade " + grade + " (" + strconv.Itoa(score) + "/100), " +
			strconv.Itoa(issues) + " issue(s) — UNTRUSTED skill content",
		DetailHash: redact.Hash("skill-posture-score skill=" + safeRef + " score=" + strconv.Itoa(score) + " grade=" + grade),
		OccurredAt: at,
	}
}

// --- small helpers ------------------------------------------------------------

// fmString extracts a frontmatter field as a trimmed string ("" for absent or
// non-string values — a non-string spec field is reported by its own check).
func fmString(fm map[string]any, key string) string {
	if v, ok := fm[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// rawFrontmatterString flattens the frontmatter map for a single secret-shape
// pass over every field value (keys + nested metadata values included).
func rawFrontmatterString(fm map[string]any) string {
	var b strings.Builder
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			b.WriteString(t)
			b.WriteByte('\n')
		case map[string]any:
			for k, vv := range t {
				b.WriteString(k)
				b.WriteByte('=')
				walk(vv)
			}
		case []any:
			for _, vv := range t {
				walk(vv)
			}
		}
	}
	walk(fm)
	return b.String()
}

// quoteSafe quotes an already-sanitized reference for a finding title.
func quoteSafe(s string) string { return strconv.Quote(s) }

// readCapped reads up to limit bytes of a file, reporting whether the file was
// larger (truncated) and whether the read succeeded at all.
func readCapped(path string, limit int64) (content string, truncated, ok bool) {
	fh, err := os.Open(path) //nolint:gosec // operator-provided config path, read-only
	if err != nil {
		return "", false, false
	}
	defer func() { _ = fh.Close() }()
	buf, err := io.ReadAll(io.LimitReader(fh, limit+1))
	if err != nil {
		return "", false, false
	}
	if int64(len(buf)) > limit {
		return string(buf[:limit]), true, true
	}
	return string(buf), false, true
}
