// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// skills.go is the OpenClaw/ClawHub SKILL SUPPLY-CHAIN scanner. Where
// govern.go only COUNTS the skills an install loads (an edge carrying the
// directory name, never content), this scanner READS each discovered skill —
// UNTRUSTED, marketplace-supplied content that becomes agent instructions and,
// via metadata.openclaw, declares runtime side effects — and grades it against
// four independent signals:
//
//   - POSTURE (the CLA-14 SKILL.md pattern, reusing connectors/internal
//     textscan+redact): instruction-injection in the description (tier-1, loaded
//     every session) and body (tier-2), hidden-Unicode / homoglyph identity,
//     secret shapes, load-time execution (!`cmd`, curl|sh, npx/uvx), and bundled
//     scripts/opaque binaries (the ClawHub spec defines NO sandbox and NO signing).
//   - CLAWHUB METADATA (metadata.openclaw): an install[] block (the ClawHavoc
//     "fake pre-requisite" AMOS-dropper vector), requires.config reading a
//     credential file (the ~/.clawdbot/.env exfil surface Koi documented), and
//     credential-shaped env declarations.
//   - SIGNED DENY-LIST (an injected connectors/threatfeed view): the skill's
//     content DIGEST against known-malicious sha256 IOCs, its embedded URLs
//     against IOC domains/urls, and its text against agentic-attack patterns.
//     Deny-closed: a configured-but-unverifiable feed is surfaced LOUDLY, never
//     treated as clean.
//   - DRIFT / AUTHORIZATION: the current content digest against an operator
//     approved baseline (a skill changed after approval is a TOCTOU compromise)
//     and against an authorized-skills allowlist. The baseline/allowlist are
//     authored under modules/governance dual-control; the connector consumes
//     them read-only.
//
// Output is MINIMAL-DATA (docs/SECURITY-HARDENING.md), exactly like the scanner: one
// finding per issue (sanitized title + hashed detail — never the skill text,
// never a secret) plus one per-skill score summary. The scanner reads content;
// it never EMITS content and never EXECUTES a skill.
package openclaw

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	"github.com/olivaresai/olivares/connectors/threatfeed"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	findingSkillSupplyChain = "skill_supply_chain"
	subjectSkillSupply      = "openclaw.skill"
)

// maxSkillMDBytes caps how much of a SKILL.md is read; a larger file is scanned
// to the cap and the truncation is itself a finding (a partial scan must never
// look complete).
const maxSkillMDBytes = 256 * 1024

// maxSkillBundleFiles bounds the per-skill bundled-file walk (a hostile skill
// must not drag the scan into an unbounded tree).
const maxSkillBundleFiles = 2048

// skillMDNames is the accepted SKILL.md filename set (ClawHub accepts SKILL.md,
// skill.md, and legacy skills.md).
var skillMDNames = []string{"SKILL.md", "skill.md", "skills.md"}

// Load-time / remote-exec signatures (local copies — the originals live in
// connectors/claude-config, which must not modify).
var (
	dynamicInjectionRe = regexp.MustCompile("(?m)^\\s*!`[^`]+`")
	curlPipeShellRe    = regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^|\n]{0,200}\|\s*(?:ba|z|da)?sh\b`)
	pkgRunnerRe        = regexp.MustCompile(`(?i)\b(?:npx|uvx|bunx|pnpx)\s+\S`)
	// httpURLRe extracts http(s) URLs from a body for IOC matching.
	httpURLRe = regexp.MustCompile("(?i)\\bhttps?://[^\\s\"'`)>\\]]+")
)

// credentialConfigMarkers name config paths whose read is a credential-access
// signal (the ClawHavoc ~/.clawdbot/.env exfil surface and its cousins).
var credentialConfigMarkers = []string{
	".env", "clawdbot", ".ssh", "id_rsa", "id_ed25519", "credentials",
	".aws", "gcloud", "token", ".netrc", "keychain", ".npmrc",
}

// skillSeverityPenalty / skillGrade replicate the posture scale so an
// OpenClaw skill grade reads on the same A–F scale as an MCP or Claude skill.
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

// skillDenylist is the minimal view the scanner needs of a signed threat feed
// (connectors/threatfeed). It is injected so the scanner is unit-testable with a
// fake and the connector stays decoupled from any specific feed wiring (the
// concrete adapter over threatfeed.Manager is built in openclaw.go Open).
type skillDenylist interface {
	// MatchIndicator reports a deny-list hit for a typed IOC (e.g. "sha256",
	// "url", "domain") and returns the matching indicator's severity label.
	MatchIndicator(typ, value string) (severity string, matched bool)
	// MatchPatterns returns the ids of every agentic-attack signature that hits.
	MatchPatterns(text string) []string
	// Expired reports whether the loaded feed has passed its expiry. Open loads
	// the pack once but Gather re-polls; an expired feed stops matching, so the
	// scanner must treat expiry as loud (deny-closed), not as "no match".
	Expired() bool
}

// skillScanPolicy carries the operator-supplied inputs that turn inventory-only
// scanning into enforcement signaling. All fields nil/empty = posture-only.
type skillScanPolicy struct {
	denylist      skillDenylist       // signed feed view (nil = no feed)
	denylistError string              // why a CONFIGURED feed failed to load (deny-closed)
	baseline      map[string]string   // skill name → approved content digest (nil = no drift policy)
	authorized    map[string]struct{} // authorized skill names (nil = no allowlist policy)
}

// skillIssue is one detected problem before it becomes a finding.
type skillIssue struct {
	severity  model.Severity
	title     string
	detailKey string
	owaspASI  []string
	owaspLLM  []string
}

// parsedSkill is the scanned, minimal view of one skill directory.
type parsedSkill struct {
	name      string // directory name — the identity clients key on
	fm        *skillFrontmatter
	fmRaw     string // frontmatter text for a single secret pass
	body      string
	truncated bool
	digest    string // hex sha256 of SKILL.md + sorted bundled-file digests
	scripts   int
	classes   []string
	binaries  int
}

// skillFrontmatter is the typed slice of a ClawHub SKILL.md frontmatter the
// scanner reasons about (unknown fields are ignored by yaml).
type skillFrontmatter struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description"`
	Version     string        `yaml:"version"`
	License     string        `yaml:"license"`
	Metadata    skillMetadata `yaml:"metadata"`
}

type skillMetadata struct {
	OpenClaw openclawSkillMeta `yaml:"openclaw"`
}

type openclawSkillMeta struct {
	Requires   skillRequires  `yaml:"requires"`
	PrimaryEnv string         `yaml:"primaryEnv"`
	EnvVars    []skillEnvVar  `yaml:"envVars"`
	Always     bool           `yaml:"always"`
	OS         []string       `yaml:"os"`
	Install    []skillInstall `yaml:"install"`
}

type skillRequires struct {
	Env     []string `yaml:"env"`
	Bins    []string `yaml:"bins"`
	AnyBins []string `yaml:"anyBins"`
	Config  []string `yaml:"config"`
}

type skillEnvVar struct {
	Name     string `yaml:"name"`
	Required bool   `yaml:"required"`
}

type skillInstall struct {
	Kind    string   `yaml:"kind"`
	Formula string   `yaml:"formula"`
	Package string   `yaml:"package"`
	Bins    []string `yaml:"bins"`
	Run     string   `yaml:"run"`
}

// skillSupplyChainFindings scans every discovered skill in the install and emits
// its supply-chain findings. It reuses the skillSources discovery already done
// for the config posture (govern.go) so both signals describe the same tree.
func (s *Source) skillSupplyChainFindings(c clawConfig) []model.FindingReport {
	at := s.clock().UTC()
	var out []model.FindingReport
	seen := map[string]struct{}{}
	for _, src := range c.skillSources {
		for _, sk := range enumerateSkillDirs(src) {
			key := filepath.Clean(sk.dir)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, s.scanOneSkill(sk.dir, sk.name, at)...)
		}
	}
	return out
}

type skillDirRef struct {
	dir  string
	name string
}

// enumerateSkillDirs resolves the concrete skill directories under one source,
// mirroring countSkillDir: a source dir that IS a skill (holds a SKILL.md
// directly) is one skill; otherwise each named immediate child is a skill.
func enumerateSkillDirs(src skillSource) []skillDirRef {
	if skillMDPath(src.Dir) != "" {
		return []skillDirRef{{dir: src.Dir, name: filepath.Base(src.Dir)}}
	}
	out := make([]skillDirRef, 0, len(src.Names))
	for _, name := range src.Names {
		out = append(out, skillDirRef{dir: filepath.Join(src.Dir, name), name: name})
	}
	return out
}

// scanOneSkill scans a single skill directory and returns its findings: one per
// issue plus the per-skill score summary. An unreadable skill is skipped
// silently (the count/edge already exists; discovery never aborts on one file).
func (s *Source) scanOneSkill(dir, name string, at time.Time) []model.FindingReport {
	ps, ok := parseSkillDir(dir, name)
	if !ok {
		return nil
	}
	issues := skillIssues(ps)
	issues = append(issues, skillDenylistIssues(ps, s.skillScan)...)
	issues = append(issues, skillDriftIssues(ps, s.skillScan)...)

	var out []model.FindingReport
	score := 100
	worst := model.SeverityInfo
	counted := 0
	for _, is := range issues {
		out = append(out, skillIssueFinding(ps.name, is, at))
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
	out = append(out, skillScoreFinding(ps.name, score, counted, worst, at))
	return out
}

// parseSkillDir loads the skill's SKILL.md (capped), parses its frontmatter,
// inventories bundled files, and computes the content digest. ok=false when no
// SKILL.md can be read at all.
func parseSkillDir(dir, name string) (parsedSkill, bool) {
	mdPath := skillMDPath(dir)
	if mdPath == "" {
		return parsedSkill{}, false
	}
	raw, truncated, ok := readCappedFile(mdPath, maxSkillMDBytes)
	if !ok {
		return parsedSkill{}, false
	}
	ps := parsedSkill{name: name, truncated: truncated}
	fmBlock, body := splitSkillFrontmatter(raw)
	ps.body = body
	if fmBlock != "" {
		ps.fmRaw = fmBlock
		var fm skillFrontmatter
		if yaml.Unmarshal([]byte(fmBlock), &fm) == nil {
			ps.fm = &fm
		}
	}
	ps.scripts, ps.classes, ps.binaries = inventorySkillFiles(dir)
	ps.digest = skillContentDigest(dir, mdPath)
	return ps, true
}

// skillMDPath returns the path to the skill's SKILL.md (first accepted name), or
// "" when none resolves to a REGULAR file. os.Stat follows a symlink (so a
// legitimate symlink→file skill is still scanned) but IsRegular excludes a
// symlink→FIFO/device (which would hang or misbehave on open).
func skillMDPath(dir string) string {
	for _, n := range skillMDNames {
		p := filepath.Join(dir, n)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// splitSkillFrontmatter splits a SKILL.md into its YAML frontmatter block ("" when
// absent) and the markdown body.
func splitSkillFrontmatter(raw string) (fmBlock, body string) {
	s := strings.TrimPrefix(raw, "\ufeff")
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != "---" {
		return "", s
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(strings.TrimSuffix(lines[i], "\r")) == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n")
		}
	}
	return "", s
}

// inventorySkillFiles walks a skill directory (bounded) counting bundled script
// files by language class and opaque binaries. Names only, never contents.
func inventorySkillFiles(dir string) (scripts int, classes []string, binaries int) {
	seen := map[string]struct{}{}
	entries := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		entries++
		if entries > maxSkillBundleFiles {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if path != dir && isExcludedWalkDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks / FIFOs / devices — never traverse or open them
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

var scriptExts = map[string]string{
	".sh": "sh", ".bash": "sh", ".zsh": "sh", ".py": "py", ".js": "js",
	".ts": "ts", ".mjs": "js", ".cjs": "js", ".rb": "rb", ".pl": "pl",
	".ps1": "ps1", ".bat": "bat", ".cmd": "bat",
}

var binaryExts = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true,
	".wasm": true, ".o": true, ".a": true, ".pyc": true,
}

// isExcludedWalkDir reports the ONLY directories excluded from the content digest
// and file inventory: VCS metadata. Every other directory — including arbitrary
// dot-prefixed ones like .cache/ or .assets/ — IS walked and hashed, so a hostile
// skill cannot hide payload from the digest (and thus from drift/IOC matching)
// under a hidden directory.
func isExcludedWalkDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".bzr":
		return true
	default:
		return false
	}
}

// skillContentDigest returns a stable hex sha256 over the skill's SKILL.md bytes
// plus every bundled file's (relative-path, content-digest) pair, sorted — so a
// change to ANY byte of ANY bundled file changes the digest (drift/IOC key).
// Bounded and read-only.
func skillContentDigest(dir, mdPath string) string {
	type fileHash struct{ rel, sum string }
	var files []fileHash
	entries := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		entries++
		if entries > maxSkillBundleFiles {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if path != dir && isExcludedWalkDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks / FIFOs / devices — a hostile skill must not make
			// the digest read (or block on) a file outside its own tree
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		files = append(files, fileHash{rel: filepath.ToSlash(rel), sum: fileSHA256(path)})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	h := sha256.New()
	for _, f := range files {
		_, _ = io.WriteString(h, f.rel)
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, f.sum)
		_, _ = io.WriteString(h, "\n")
	}
	_ = mdPath // SKILL.md is included in the walk above; kept for clarity.
	return hex.EncodeToString(h.Sum(nil))
}

// fileSHA256 returns the hex sha256 of a file (bounded read), or "" on error.
func fileSHA256(path string) string {
	f, err := os.Open(path) //nolint:gosec // operator/local skill file, read-only
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxSkillMDBytes*4)); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// readCappedFile reads up to limit bytes, reporting truncation and read success.
func readCappedFile(path string, limit int64) (content string, truncated, ok bool) {
	f, err := os.Open(path) //nolint:gosec // operator/local skill file, read-only
	if err != nil {
		return "", false, false
	}
	defer func() { _ = f.Close() }()
	buf, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", false, false
	}
	if int64(len(buf)) > limit {
		return string(buf[:limit]), true, true
	}
	return string(buf), false, true
}

// skillIssues derives the posture + ClawHub-metadata issues for one parsed
// skill. Pure (deterministic; testable without a filesystem beyond the parse).
func skillIssues(ps parsedSkill) []skillIssue {
	var out []skillIssue
	add := func(sev model.Severity, title, key string) {
		out = append(out, skillIssue{severity: sev, title: title, detailKey: key})
	}

	// --- identity ---------------------------------------------------------------
	if classes, n := textscan.ScanInvisible(ps.name); n > 0 {
		add(model.SeverityHigh,
			"skill directory name hides "+strconv.Itoa(n)+" invisible character(s) ["+strings.Join(classes, ",")+"] — spoofing/poisoning",
			"invisible-dirname classes="+strings.Join(classes, ","))
	}
	if scripts, confusable := textscan.MixedScript(ps.name); confusable {
		add(model.SeverityHigh,
			"skill directory name mixes scripts ["+strings.Join(scripts, ",")+"] — homoglyph impersonation candidate",
			"homoglyph-dirname scripts="+strings.Join(scripts, ","))
	}

	// --- frontmatter ------------------------------------------------------------
	if ps.fm == nil {
		add(model.SeverityMedium,
			"SKILL.md has no parseable YAML frontmatter (the ClawHub format requires name + description)",
			"no-frontmatter")
	} else {
		out = append(out, frontmatterIssues(ps)...)
		out = append(out, metadataIssues(ps)...)
		if redact.ContainsSecret(ps.fmRaw) {
			add(model.SeverityHigh,
				"skill frontmatter embeds a credential/secret shape — secret exposure",
				"secret-in-frontmatter")
		}
	}

	// --- body: tier-2 instructions ---------------------------------------------
	for _, id := range textscan.InstructionFileMarkers(textscan.ScanInjection(ps.body)) {
		out = append(out, skillIssue{
			severity:  textscan.MarkerSeverity(id),
			title:     "skill body contains an instruction-injection marker [" + id + "]",
			detailKey: "injection-body rule=" + id,
			owaspASI:  []string{"ASI01"},
			owaspLLM:  []string{"LLM01:2025"},
		})
	}
	if classes, n := textscan.ScanInvisible(ps.body); n > 0 {
		add(model.SeverityHigh,
			"skill body hides "+strconv.Itoa(n)+" invisible character(s) ["+strings.Join(classes, ",")+"] — concealed instruction",
			"invisible-body classes="+strings.Join(classes, ","))
	}
	if redact.ContainsSecret(ps.body) {
		add(model.SeverityHigh,
			"skill body embeds a credential/secret shape — secret exposure",
			"secret-in-body")
	}
	if n := len(dynamicInjectionRe.FindAllString(ps.body, -1)); n > 0 {
		add(model.SeverityHigh,
			"skill executes shell at LOAD time via "+strconv.Itoa(n)+" !`…` dynamic-context-injection line(s)",
			"dynamic-injection count="+strconv.Itoa(n))
	}
	if curlPipeShellRe.MatchString(ps.body) {
		add(model.SeverityHigh,
			"skill body pipes a remote download into a shell (curl|sh remote-exec)",
			"curl-pipe-shell")
	}
	if pkgRunnerRe.MatchString(ps.body) {
		add(model.SeverityLow,
			"skill body invokes an ephemeral package runner (npx/uvx) — code resolved from a remote registry at use time",
			"pkg-runner")
	}
	if ps.truncated {
		add(model.SeverityLow,
			"SKILL.md exceeds "+strconv.Itoa(maxSkillMDBytes/1024)+" KiB; only the first "+strconv.Itoa(maxSkillMDBytes/1024)+" KiB was scanned (partial scan)",
			"truncated")
	}

	// --- bundled files ----------------------------------------------------------
	if ps.scripts > 0 {
		add(model.SeverityInfo,
			"skill bundles "+strconv.Itoa(ps.scripts)+" script file(s) ["+strings.Join(ps.classes, ",")+"] — scripts run with the agent's tool permissions (no sandbox)",
			"scripts count="+strconv.Itoa(ps.scripts)+" classes="+strings.Join(ps.classes, ","))
	}
	if ps.binaries > 0 {
		add(model.SeverityMedium,
			"skill bundles "+strconv.Itoa(ps.binaries)+" opaque binary file(s) — unauditable executable surface",
			"binaries count="+strconv.Itoa(ps.binaries))
	}
	return out
}

// frontmatterIssues checks the ClawHub frontmatter basics and injection in the
// tier-1 description (loaded into every session).
func frontmatterIssues(ps parsedSkill) []skillIssue {
	var out []skillIssue
	add := func(sev model.Severity, title, key string) {
		out = append(out, skillIssue{severity: sev, title: title, detailKey: key})
	}
	name := strings.TrimSpace(ps.fm.Name)
	switch {
	case name == "":
		add(model.SeverityMedium, "frontmatter `name` is missing/empty (ClawHub requires it)", "name-missing")
	default:
		if name != ps.name {
			add(model.SeverityMedium,
				"frontmatter name "+quoteSafe(textscan.SanitizeDisplay(name))+" does not match the skill directory "+quoteSafe(textscan.SanitizeDisplay(ps.name)),
				"name-dir-mismatch")
		}
		if classes, n := textscan.ScanInvisible(name); n > 0 {
			add(model.SeverityHigh,
				"frontmatter name hides "+strconv.Itoa(n)+" invisible character(s) ["+strings.Join(classes, ",")+"]",
				"invisible-name classes="+strings.Join(classes, ","))
		}
	}
	desc := strings.TrimSpace(ps.fm.Description)
	if desc == "" {
		add(model.SeverityMedium,
			"frontmatter `description` is missing/empty (clients skip or mis-trigger the skill)",
			"description-missing")
	} else {
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
	return out
}

// metadataIssues derives the ClawHub metadata.openclaw supply-chain issues.
func metadataIssues(ps parsedSkill) []skillIssue {
	var out []skillIssue
	add := func(sev model.Severity, title, key string, asi ...string) {
		out = append(out, skillIssue{severity: sev, title: title, detailKey: key, owaspASI: asi})
	}
	m := ps.fm.Metadata.OpenClaw

	// install[]: the ClawHavoc AMOS "fake pre-requisite" vector.
	if len(m.Install) > 0 {
		sev := model.SeverityMedium
		reason := "declares install steps run before use"
		key := "install count=" + strconv.Itoa(len(m.Install))
		for _, ins := range m.Install {
			if curlPipeShellRe.MatchString(ins.Run) || pkgRunnerRe.MatchString(ins.Run) {
				sev = model.SeverityHigh
				reason = "install runs a remote-fetch/exec command"
				key = "install-remote-exec"
				break
			}
			if k := strings.ToLower(strings.TrimSpace(ins.Kind)); k != "" && !knownInstallKind(k) {
				sev = model.SeverityHigh
				reason = "install uses a non-standard kind " + quoteSafe(textscan.SanitizeDisplay(ins.Kind))
				key = "install-unknown-kind kind=" + textscan.SanitizeDisplay(ins.Kind)
			}
		}
		add(sev, "skill "+reason+" (ClawHavoc dropped malware via fake install pre-requisites)", key, "ASI02")
	}

	// requires.config: reading a credential file is the exfil surface.
	for _, cfg := range m.Requires.Config {
		if marker := credentialConfigMarker(cfg); marker != "" {
			add(model.SeverityHigh,
				"skill declares it reads a credential-bearing config path [contains "+marker+"] — credential-access / exfil surface",
				"requires-config-credential marker="+marker, "ASI02", "LLM06:2025")
		}
	}

	// credential-shaped primaryEnv / envVars: a declared secret dependency.
	credEnv := credentialEnvNames(m)
	if len(credEnv) > 0 {
		add(model.SeverityInfo,
			"skill declares "+strconv.Itoa(len(credEnv))+" credential-shaped environment dependency(ies)",
			"credential-env count="+strconv.Itoa(len(credEnv)))
	}

	if m.Always {
		add(model.SeverityLow,
			"skill is marked always-active (metadata.openclaw.always=true) — its instructions load in every session (tier-1)",
			"always-active")
	}
	return out
}

func knownInstallKind(k string) bool {
	switch k {
	case "brew", "homebrew", "apt", "apt-get", "dnf", "yum", "pacman", "pip", "pipx",
		"npm", "pnpm", "yarn", "cargo", "go", "gem", "scoop", "winget", "choco":
		return true
	default:
		return false
	}
}

func credentialConfigMarker(path string) string {
	p := strings.ToLower(strings.TrimSpace(path))
	for _, marker := range credentialConfigMarkers {
		if strings.Contains(p, marker) {
			return marker
		}
	}
	return ""
}

func credentialEnvNames(m openclawSkillMeta) []string {
	seen := map[string]struct{}{}
	consider := func(name string) {
		if credentialShapedKey(name) {
			seen[strings.ToUpper(strings.TrimSpace(name))] = struct{}{}
		}
	}
	consider(m.PrimaryEnv)
	for _, e := range m.EnvVars {
		consider(e.Name)
	}
	for _, e := range m.Requires.Env {
		consider(e)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// skillDenylistIssues matches the parsed skill against the injected signed feed.
// A configured-but-unverifiable feed yields a loud finding (deny-closed).
func skillDenylistIssues(ps parsedSkill, pol skillScanPolicy) []skillIssue {
	var out []skillIssue
	if pol.denylistError != "" {
		out = append(out, skillIssue{
			severity:  model.SeverityHigh,
			title:     "skill deny-list feed is configured but could not be verified — scanning WITHOUT known-bad IOCs (deny-closed alert)",
			detailKey: "denylist-load-error reason=" + redact.Clean(pol.denylistError),
		})
	}
	if pol.denylist == nil {
		return out
	}
	// An expired feed stops matching (threatfeed serves nil once past expiry), so
	// treat expiry as loud — never let it degrade silently to "no IOC matched".
	if pol.denylist.Expired() {
		out = append(out, skillIssue{
			severity:  model.SeverityHigh,
			title:     "skill deny-list feed has EXPIRED — scanning WITHOUT current known-bad IOCs (deny-closed alert; rotate the signed rule-pack)",
			detailKey: "denylist-expired",
		})
		return out
	}
	if ps.digest != "" {
		if sev, ok := pol.denylist.MatchIndicator("sha256", ps.digest); ok {
			out = append(out, skillIssue{
				severity:  atLeastSeverity(sev, model.SeverityCritical),
				title:     "skill content digest matches a KNOWN-MALICIOUS deny-list indicator (sha256) — remove immediately",
				detailKey: "ioc-sha256-match",
				owaspASI:  []string{"ASI02"},
			})
		}
	}
	for _, u := range extractSkillURLs(ps.body) {
		if sev, ok := pol.denylist.MatchIndicator("url", u); ok {
			out = append(out, skillIssue{
				severity:  atLeastSeverity(sev, model.SeverityHigh),
				title:     "skill body references a deny-listed URL indicator — exfil/callback candidate",
				detailKey: "ioc-url-match host=" + redact.SanitizeURL(u),
				owaspASI:  []string{"ASI02"},
			})
		}
		if host := urlHost(u); host != "" {
			if sev, ok := pol.denylist.MatchIndicator("domain", host); ok {
				out = append(out, skillIssue{
					severity:  atLeastSeverity(sev, model.SeverityHigh),
					title:     "skill body references a deny-listed domain indicator — exfil/callback candidate",
					detailKey: "ioc-domain-match host=" + redact.Clean(host),
					owaspASI:  []string{"ASI02"},
				})
			}
		}
	}
	scanText := ps.fmRaw + "\n" + ps.body
	for _, id := range pol.denylist.MatchPatterns(scanText) {
		out = append(out, skillIssue{
			severity:  model.SeverityHigh,
			title:     "skill matches a deny-listed agentic-attack signature [" + textscan.SanitizeDisplay(id) + "]",
			detailKey: "pattern-match id=" + textscan.SanitizeDisplay(id),
			owaspASI:  []string{"ASI01"},
		})
	}
	return out
}

// skillDriftIssues compares the skill against the approved baseline and the
// authorized allowlist.
func skillDriftIssues(ps parsedSkill, pol skillScanPolicy) []skillIssue {
	var out []skillIssue
	if pol.baseline != nil {
		if approved, ok := pol.baseline[ps.name]; ok {
			if ps.digest != "" && !strings.EqualFold(approved, ps.digest) {
				out = append(out, skillIssue{
					severity:  model.SeverityHigh,
					title:     "skill content changed after approval — its current digest no longer matches the approved baseline (drift/TOCTOU)",
					detailKey: "drift-baseline-mismatch",
					owaspASI:  []string{"ASI02"},
				})
			}
		}
	}
	if pol.authorized != nil {
		if _, ok := pol.authorized[ps.name]; !ok {
			out = append(out, skillIssue{
				severity:  model.SeverityMedium,
				title:     "skill is NOT on the fleet authorized-skills allowlist",
				detailKey: "skill-unauthorized",
			})
		}
	}
	return out
}

// extractSkillURLs returns the (bounded) set of http(s) URLs in a body.
func extractSkillURLs(body string) []string {
	matches := httpURLRe.FindAllString(body, 64)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		m = strings.TrimRight(m, ".,);]")
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}

func urlHost(raw string) string {
	raw = strings.TrimSpace(raw)
	i := strings.Index(raw, "://")
	if i < 0 {
		return ""
	}
	rest := raw[i+3:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	if c := strings.IndexByte(rest, ':'); c >= 0 {
		rest = rest[:c]
	}
	return strings.ToLower(rest)
}

// atLeastSeverity floors a feed-supplied severity label to a minimum, so a feed
// that under-labels a known-malicious digest still surfaces at the floor.
func atLeastSeverity(label string, floor model.Severity) model.Severity {
	sev := parseSeverityLabel(label)
	if sev.AtLeast(floor) {
		return sev
	}
	return floor
}

func parseSeverityLabel(label string) model.Severity {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "critical":
		return model.SeverityCritical
	case "high":
		return model.SeverityHigh
	case "medium", "moderate":
		return model.SeverityMedium
	case "low":
		return model.SeverityLow
	default:
		return model.SeverityInfo
	}
}

// skillIssueFinding builds one minimal-data finding for a detected issue.
func skillIssueFinding(name string, is skillIssue, at time.Time) model.FindingReport {
	safeRef := textscan.SanitizeDisplay(name)
	return model.FindingReport{
		Kind:        findingSkillSupplyChain,
		Severity:    is.severity,
		SubjectKind: subjectSkillSupply,
		SubjectRef:  safeRef,
		Title:       "skill " + quoteSafe(safeRef) + ": " + is.title,
		DetailHash:  redact.Hash("skill-supply skill=" + safeRef + " " + is.detailKey),
		OccurredAt:  at,
		OWASPASI:    is.owaspASI,
		OWASPLLM:    is.owaspLLM,
	}
}

// skillScoreFinding builds the per-skill supply-chain score summary.
func skillScoreFinding(name string, score, issues int, worst model.Severity, at time.Time) model.FindingReport {
	grade := skillGrade(score)
	sev := model.SeverityInfo
	if issues > 0 {
		sev = worst
	}
	safeRef := textscan.SanitizeDisplay(name)
	return model.FindingReport{
		Kind:        findingSkillSupplyChain,
		Severity:    sev,
		SubjectKind: subjectSkillSupply,
		SubjectRef:  safeRef,
		Title: "Skill supply-chain: grade " + grade + " (" + strconv.Itoa(score) + "/100), " +
			strconv.Itoa(issues) + " issue(s) — UNTRUSTED skill content",
		DetailHash: redact.Hash("skill-supply-score skill=" + safeRef + " score=" + strconv.Itoa(score) + " grade=" + grade),
		OccurredAt: at,
	}
}

func quoteSafe(s string) string { return strconv.Quote(s) }

// --- operator policy loading -------------------------------------------------

// feedDenylistAdapter adapts a verified connectors/threatfeed Manager to the
// minimal skillDenylist view the scanner consumes.
type feedDenylistAdapter struct{ m *threatfeed.Manager }

func (a feedDenylistAdapter) MatchIndicator(typ, value string) (string, bool) {
	ind, ok := a.m.MatchIndicator(typ, value)
	if !ok {
		return "", false
	}
	return ind.Severity, true
}

func (a feedDenylistAdapter) MatchPatterns(text string) []string {
	pats := a.m.MatchPatterns(text)
	ids := make([]string, 0, len(pats))
	for _, p := range pats {
		ids = append(ids, p.ID)
	}
	return ids
}

func (a feedDenylistAdapter) Expired() bool { return a.m.Status().Expired }

// buildSkillScanPolicy assembles the skill-scan policy from operator config:
//
//	skill_denylist_path  — signed threatfeed rule-pack (JSON); its detached
//	                       signature is <path>.sig unless skill_denylist_sig is set.
//	skill_denylist_keys  — comma-separated base64 Ed25519 trusted publisher keys.
//	skill_baseline_path  — JSON object {skill-name: approved-sha256-digest}.
//	authorized_skills    — comma-separated authorized skill names.
//
// The deny-list is DENY-CLOSED: a configured-but-unverifiable feed sets
// denylistError (surfaced loudly per skill) rather than silently scanning clean.
func buildSkillScanPolicy(cfg sdk.Config) skillScanPolicy {
	var pol skillScanPolicy
	if path := strings.TrimSpace(cfg.Get("skill_denylist_path")); path != "" {
		dl, err := loadSignedDenylist(path, strings.TrimSpace(cfg.Get("skill_denylist_sig")), cfg.Get("skill_denylist_keys"))
		if err != nil {
			pol.denylistError = err.Error()
		} else {
			pol.denylist = dl
		}
	}
	if path := strings.TrimSpace(cfg.Get("skill_baseline_path")); path != "" {
		if base, err := loadApprovedBaseline(path); err == nil {
			pol.baseline = base
		}
	}
	if raw := strings.TrimSpace(cfg.Get("authorized_skills")); raw != "" {
		set := map[string]struct{}{}
		for _, name := range strings.Split(raw, ",") {
			if n := strings.TrimSpace(name); n != "" {
				set[n] = struct{}{}
			}
		}
		if len(set) > 0 {
			pol.authorized = set
		}
	}
	return pol
}

// loadSignedDenylist loads and Ed25519-verifies a threatfeed rule-pack against
// the pinned keys, returning a live lookup view. Deny-closed: no keys, a bad
// signature, an expired/rolled-back pack, or a malformed pack all error.
func loadSignedDenylist(packPath, sigPath, keysCSV string) (skillDenylist, error) {
	pubs, err := parseEd25519Keys(keysCSV)
	if err != nil {
		return nil, err
	}
	if len(pubs) == 0 {
		return nil, fmt.Errorf("skill_denylist_keys is empty — deny-list updates are disabled (deny-closed)")
	}
	packBytes, err := os.ReadFile(packPath) //nolint:gosec // operator-supplied rule-pack path
	if err != nil {
		return nil, fmt.Errorf("read rule-pack: %w", err)
	}
	if sigPath == "" {
		sigPath = packPath + ".sig"
	}
	sigB, err := os.ReadFile(sigPath) //nolint:gosec // operator-supplied signature path
	if err != nil {
		return nil, fmt.Errorf("read rule-pack signature: %w", err)
	}
	sigRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB)))
	if err != nil {
		return nil, fmt.Errorf("rule-pack signature is not base64: %w", err)
	}
	mgr := threatfeed.NewManager(pubs)
	if _, err := mgr.Apply(packBytes, sigRaw); err != nil {
		return nil, err
	}
	return feedDenylistAdapter{m: mgr}, nil
}

// parseEd25519Keys decodes comma-separated base64 Ed25519 public keys (stdlib
// only — a connector must not import core/release for key decoding).
func parseEd25519Keys(csv string) ([]ed25519.PublicKey, error) {
	var out []ed25519.PublicKey
	for _, tok := range strings.Split(csv, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(tok)
		if err != nil {
			return nil, fmt.Errorf("trusted key is not base64: %w", err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("trusted key has wrong length %d (want %d)", len(raw), ed25519.PublicKeySize)
		}
		out = append(out, ed25519.PublicKey(raw))
	}
	return out, nil
}

// loadApprovedBaseline reads a {skill-name: approved-sha256} JSON map.
func loadApprovedBaseline(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied baseline path
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out, nil
}
