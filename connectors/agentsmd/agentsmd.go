// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package agentsmd is the AGENTS.md INTEGRITY/DRIFT scanner (CUR-7): the
// instruction-file sibling of the managed-settings drift verifier. An
// AGENTS.md (Agentic AI Foundation/Linux Foundation since 2025-12-09; freeform
// markdown, 60k+ repos) — and its siblings AGENTS.override.md, CLAUDE.md,
// CLAUDE.local.md — is repo-committed text that agent runtimes treat as
// AUTHORITATIVE INSTRUCTIONS, often auto-loaded (VS Code injects AGENTS.md into
// every chat request by default). The format itself defines NO integrity,
// signing or provenance mechanism (verified against agents.md + the AAIF repos,
// 2026-06-10), so the control plane imposes one:
//
//   - INTEGRITY/DRIFT (the managed-settings driftFindings pattern): the operator
//     authors a BASELINE of approved instruction files (relative path → SHA-256);
//     the connector hashes the live tree and reports every divergence — an
//     ALTERED file (the NVIDIA indirect-injection chain wrote AGENTS.md from a
//     malicious build dependency, 2026-04), an UNBASELINED file (an unreviewed
//     instruction surface a nearest-file-wins consumer will silently honor),
//     and a MISSING baselined file. The baseline also emits PERMITTED policy
//     edges, so module III sees authored-vs-observed for this surface too.
//   - INSTRUCTION-INJECTION posture (the documented attack classes): invisible
//     Unicode ("Rules File Backdoor", Pillar Security 2025-03), injection
//     markers tuned for instruction files (authority claims, "do not mention
//     this in the PR summary" second-order injection, exfiltration, safety
//     override — imperatives and tool-ordering are NOT flagged here, they are
//     this format's legitimate idiom), markers CONCEALED inside HTML comment
//     blocks (visible to the agent, invisible in rendered review), secret
//     shapes, and remote-exec (curl|sh) the agent is instructed to run.
//
// MINIMAL-DATA (docs/SECURITY-HARDENING.md): findings carry a sanitized relative path + a hashed
// detail; file content is read for scanning and hashing but NEVER emitted.
// A missing/unreadable target is a FINDING (or silence for an optional file),
// never a Gather error; only a transient root fault aborts the pass.
package agentsmd

import (
	"context"
	"crypto/sha256"
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

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.agents-md"

const version = "0.1.0"

// Configuration keys.
const (
	cfgRoot            = "root"              // the governed repo root to scan
	cfgScope           = "scope"             // attribution scope (defaults to the root directory name)
	cfgBaseline        = "expected_baseline" // authored baseline: JSON object {relpath: sha256hex}
	cfgFilenames       = "filenames"         // instruction-file names to scan for (comma/JSON list)
	cfgEnforceBaseline = "enforce_baseline"  // when true, emit enforcement finding on altered baselined files
)

// defaultFilenames are the instruction-file variants scanned by default: the
// AAIF standard pair plus Claude Code's memory files (Claude Code reads
// CLAUDE.md, NOT AGENTS.md — verified vs code.claude.com 2026-06-10 — so a
// governed repo's instruction surface spans both families).
var defaultFilenames = []string{"AGENTS.md", "AGENTS.override.md", "CLAUDE.md", "CLAUDE.local.md"}

// Finding vocabulary.
const (
	// findingDrift marks an authored-baseline vs live-tree divergence (the
	// instruction-file analog of managed-settings policy_drift).
	findingDrift = "instructions_drift"
	// findingPosture marks a content threat in an instruction file.
	findingPosture = "instructions_posture"
	// findingEnforced marks an enforcement denial: the file is altered from the
	// authored baseline and enforce_baseline is active.
	findingEnforced = "instructions_enforced"
	// subjectInstructions is the SubjectKind/ResourceKind for one governed
	// instruction file.
	subjectInstructions = "config.instructions"
	// originManagedPolicy attributes the PERMITTED baseline edges (the same
	// origin kind managed-settings uses for authored grants).
	originManagedPolicy = "managed_policy"
	// originWorkspace attributes the OBSERVED file edges.
	originWorkspace = "workspace"
)

// maxScanBytes caps how much of a file is text-scanned; maxHashBytes caps how
// much is read at all (integrity is computed over the FULL content up to this
// bound — a larger file is itself a finding, never a silently partial hash).
const (
	maxScanBytes = 256 * 1024
	maxHashBytes = 4 << 20 // 4 MiB
)

// consumerCapBytes is the documented per-file cap of the largest mainstream
// consumer (Codex project_doc_max_bytes = 32 KiB): content beyond it is
// silently DROPPED there, so an oversized file can hide instructions from one
// consumer while feeding them to another.
const consumerCapBytes = 32 * 1024

// curlPipeShellRe matches an instruction to pipe a remote download into a shell.
var curlPipeShellRe = regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^|\n]{0,200}\|\s*(?:ba|z|da)?sh\b`)

// htmlCommentRe matches HTML comment blocks — content Claude Code strips before
// injection and rendered markdown hides from a reviewer.
var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// config is the resolved connector configuration.
type config struct {
	root      string
	scope     string
	filenames map[string]bool
	// baseline is the authored approved set (relpath → sha256 hex). nil = no
	// baseline configured: the connector inventories + posture-scans but cannot
	// compute drift (there is nothing to diff against).
	baseline map[string]string
	// enforceBaseline, when true, emits an enforcement finding for any baselined
	// file whose live content diverges from the authored SHA-256. Files
	// not in the baseline (unbaselined/missing) emit drift findings only.
	enforceBaseline bool
}

// Source is the AGENTS.md integrity/drift SourceConnector. It is a batch
// source: Gather walks the tree once and returns nil; the engine re-polls.
type Source struct {
	cfg config
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an agents-md connector with default configuration.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "AGENTS.md integrity & injection scan",
		Description: "Walks a governed repo for agent instruction files (AGENTS.md, CLAUDE.md, …), verifies them against an authored SHA-256 baseline (drift: altered/unbaselined/missing) and scans their content for instruction-injection, hidden-Unicode, concealed-comment and secret threats. Minimal-data: sanitized paths + hashed details, never content.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgRoot, Type: sdk.FieldString, Required: true, Description: "repo root to scan (read-only walk; .git/node_modules/vendor trees are skipped)"},
			{Key: cfgScope, Type: sdk.FieldString, Description: "attribution scope ref for the governed repo (defaults to the root directory name)"},
			{Key: cfgBaseline, Type: sdk.FieldString, Description: `OPTIONAL authored baseline as a JSON object {"<relative path>": "<sha256 hex>"}; when set, every divergence (altered/unbaselined/missing) is a drift finding. Unset = inventory + posture only`},
			{Key: cfgFilenames, Type: sdk.FieldString, Description: "instruction-file names to scan for (JSON array or comma list); default AGENTS.md, AGENTS.override.md, CLAUDE.md, CLAUDE.local.md"},
			{Key: cfgEnforceBaseline, Type: sdk.FieldBool, Default: "false", Description: "when true, an instruction file (AGENTS.md/CLAUDE.md) altered from its authored baseline triggers an enforcement denial finding; unbaselined/missing files emit drift findings only"},
		},
	}
}

// Open resolves and validates configuration. A malformed baseline fails LOUD —
// never silently downgrading to inventory-only, which would hide the drift the
// operator asked to detect (the managed-settings expected_policy precedent).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c := config{
		root:  strings.TrimSpace(cfg.Get(cfgRoot)),
		scope: strings.TrimSpace(cfg.Get(cfgScope)),
	}
	if c.root == "" {
		return fmt.Errorf("agents-md: %q is required (the repo root to scan)", cfgRoot)
	}
	if c.scope == "" {
		c.scope = filepath.Base(filepath.Clean(c.root))
	}
	c.filenames = map[string]bool{}
	for _, n := range parseList(cfg.Get(cfgFilenames), defaultFilenames) {
		c.filenames[n] = true
	}
	if raw := strings.TrimSpace(cfg.Get(cfgBaseline)); raw != "" {
		var baseline map[string]string
		dec := json.NewDecoder(strings.NewReader(raw))
		if err := dec.Decode(&baseline); err != nil {
			return fmt.Errorf("agents-md: invalid %s: %w", cfgBaseline, err)
		}
		normalized := make(map[string]string, len(baseline))
		for p, h := range baseline {
			h = strings.ToLower(strings.TrimSpace(h))
			if !isHex64(h) {
				return fmt.Errorf("agents-md: %s[%q] is not a 64-hex SHA-256", cfgBaseline, p)
			}
			normalized[normPath(p)] = h
		}
		c.baseline = normalized
	}
	c.enforceBaseline = cfg.GetBool(cfgEnforceBaseline, false)
	s.cfg = c
	return nil
}

// Close releases resources (none held).
func (s *Source) Close(context.Context) error { return nil }

// Gather walks the governed tree once, emitting:
//   - a PERMITTED policy edge per baselined file (the authored intent),
//   - an OBSERVED config edge per discovered instruction file,
//   - drift findings (altered/unbaselined/missing) when a baseline is authored,
//   - posture findings for content threats in every discovered file.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if info, err := os.Stat(s.cfg.root); err != nil || !info.IsDir() {
		// A transient/misconfigured root is a Gather fault the engine retries —
		// not a finding (there is no observed estate to report on at all).
		return fmt.Errorf("agents-md: root %q is not a readable directory", s.cfg.scope)
	}
	at := time.Now().UTC()

	// PERMITTED side: the authored baseline, in deterministic order.
	for _, rel := range sortedPaths(s.cfg.baseline) {
		if err := sink.Emit(ctx, model.EdgeObservation{
			OriginKind:   originManagedPolicy,
			OriginRef:    s.cfg.scope,
			ResourceKind: subjectInstructions,
			ResourceRef:  rel,
			Mode:         model.ModeUnknown,
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   at,
		}); err != nil {
			return err
		}
	}

	seen := map[string]bool{}
	walkErr := filepath.WalkDir(s.cfg.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate an unreadable entry; never abort discovery on one file
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !s.cfg.filenames[d.Name()] {
			return nil
		}
		rel, rerr := filepath.Rel(s.cfg.root, path)
		if rerr != nil {
			return nil
		}
		rel = normPath(rel)
		seen[rel] = true
		return s.scanFile(ctx, sink, path, rel, at)
	})
	if walkErr != nil {
		return walkErr
	}

	// Baselined files MISSING from the tree (deterministic order).
	for _, rel := range sortedPaths(s.cfg.baseline) {
		if !seen[rel] {
			if err := sink.Emit(ctx, driftFinding(s.cfg.scope, rel, model.SeverityMedium,
				"baselined instruction file is MISSING from the repo", "missing", at)); err != nil {
				return err
			}
		}
	}
	return nil
}

// scanFile emits the observed edge, the integrity verdict and the posture
// findings for one discovered instruction file. An unreadable file is skipped
// (the walk tolerates per-file faults).
func (s *Source) scanFile(ctx context.Context, sink sdk.Sink, path, rel string, at time.Time) error {
	if err := sink.Emit(ctx, model.EdgeObservation{
		OriginKind:   originWorkspace,
		OriginRef:    s.cfg.scope,
		ResourceKind: subjectInstructions,
		ResourceRef:  rel,
		Mode:         model.ModeUnknown,
		Source:       model.SignalConfig,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}); err != nil {
		return err
	}

	raw, oversized, ok := readCapped(path, maxHashBytes)
	if !ok {
		return nil
	}
	if oversized {
		// Integrity over a partial read would be a fake; say so and stop here.
		return sink.Emit(ctx, postureFinding(s.cfg.scope, rel, model.SeverityHigh,
			"instruction file exceeds "+strconv.Itoa(maxHashBytes>>20)+" MiB (pathological; integrity not computed, content not scanned)",
			"oversized", nil, nil, at))
	}

	// Integrity vs the authored baseline.
	if s.cfg.baseline != nil {
		sum := sha256.Sum256([]byte(raw))
		liveHash := hex.EncodeToString(sum[:])
		switch want, baselined := s.cfg.baseline[rel]; {
		case !baselined:
			if err := sink.Emit(ctx, driftFinding(s.cfg.scope, rel, model.SeverityMedium,
				"instruction file present but NOT in the authored baseline (unreviewed instruction surface)",
				"unbaselined hash="+liveHash, at)); err != nil {
				return err
			}
		case want != liveHash:
			if err := sink.Emit(ctx, driftFinding(s.cfg.scope, rel, model.SeverityHigh,
				"instruction file ALTERED since the authored baseline (agents consume the live content)",
				"altered want="+want+" got="+liveHash, at)); err != nil {
				return err
			}
			// policy-gatable enforcement — when enforceBaseline is true AND the
			// file is altered (SHA mismatch against an authored baseline), emit an
			// enforcement finding. Missing/unbaselined files are NOT enforced (the
			// operator must explicitly baseline first).
			if s.cfg.enforceBaseline {
				if err := sink.Emit(ctx, enforcementFinding(rel, at)); err != nil {
					return err
				}
			}
		}
	}

	// Posture over the (capped) content.
	text := raw
	truncated := false
	if len(text) > maxScanBytes {
		text, truncated = text[:maxScanBytes], true
	}
	for _, f := range postureFindings(s.cfg.scope, rel, text, len(raw), truncated, at) {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// postureFindings derives the content-threat findings for one instruction file.
// Pure function of (scope, rel, text) — deterministic and directly testable.
func postureFindings(scope, rel, text string, fullLen int, truncated bool, at time.Time) []model.FindingReport {
	var out []model.FindingReport
	add := func(sev model.Severity, title, key string, asi, llm []string) {
		out = append(out, postureFinding(scope, rel, sev, title, key, asi, llm, at))
	}

	// Injection markers, with the instruction-file subset (imperatives and
	// tool-ordering are this format's legitimate idiom). A marker present in the
	// full text but NOT in the comment-stripped text lives INSIDE an HTML
	// comment: concealed from rendered review while agent runtimes may ingest it
	// — the divergence is the attack, so it grades High regardless of the
	// marker's own grade.
	full := textscan.InstructionFileMarkers(textscan.ScanInjection(text))
	stripped := markerSet(textscan.InstructionFileMarkers(textscan.ScanInjection(htmlCommentRe.ReplaceAllString(text, ""))))
	for _, id := range full {
		if _, visible := stripped[id]; !visible {
			add(model.SeverityHigh,
				"instruction-injection marker ["+id+"] CONCEALED inside an HTML comment block (hidden from rendered review)",
				"injection-concealed rule="+id, []string{"ASI01"}, []string{"LLM01:2025"})
			continue
		}
		add(textscan.MarkerSeverity(id),
			"contains an instruction-injection marker ["+id+"]",
			"injection rule="+id, []string{"ASI01"}, []string{"LLM01:2025"})
	}

	if classes, n := textscan.ScanInvisible(text); n > 0 {
		add(model.SeverityHigh,
			"hides "+strconv.Itoa(n)+" invisible character(s) ["+strings.Join(classes, ",")+"] — concealed instruction (Rules File Backdoor class)",
			"invisible classes="+strings.Join(classes, ","), []string{"ASI01"}, nil)
	}
	if redact.ContainsSecret(text) {
		add(model.SeverityHigh,
			"embeds a credential/secret shape — secret exposure in a repo-committed file",
			"secret", nil, nil)
	}
	if curlPipeShellRe.MatchString(text) {
		add(model.SeverityMedium,
			"instructs piping a remote download into a shell (curl|sh) — agents execute listed commands",
			"curl-pipe-shell", nil, nil)
	}
	if fullLen > consumerCapBytes {
		add(model.SeverityLow,
			"exceeds the 32 KiB cap some consumers apply — content beyond the cap is silently dropped there (split-view risk)",
			"consumer-cap len="+strconv.Itoa(fullLen), nil, nil)
	}
	if truncated {
		add(model.SeverityLow,
			"exceeds "+strconv.Itoa(maxScanBytes/1024)+" KiB; only the first "+strconv.Itoa(maxScanBytes/1024)+" KiB was content-scanned (partial scan)",
			"truncated", nil, nil)
	}
	return out
}

// driftFinding builds one minimal-data integrity-drift finding.
func driftFinding(scope, rel string, sev model.Severity, title, key string, at time.Time) model.FindingReport {
	safe := textscan.SanitizeDisplay(rel)
	return model.FindingReport{
		Kind:        findingDrift,
		Severity:    sev,
		SubjectKind: subjectInstructions,
		SubjectRef:  safe,
		Title:       "instruction file " + strconv.Quote(safe) + ": " + title,
		DetailHash:  redact.Hash("agents-md-drift scope=" + scope + " file=" + safe + " " + key),
		OccurredAt:  at,
	}
}

// enforcementFinding builds the enforcement denial finding for an altered
// instruction file. High severity: the file's content diverges from
// the operator's authored baseline and enforce_baseline is active.
func enforcementFinding(relPath string, at time.Time) model.FindingReport {
	safe := textscan.SanitizeDisplay(relPath)
	return model.FindingReport{
		Kind:        findingEnforced,
		Severity:    model.SeverityHigh,
		SubjectKind: subjectInstructions,
		SubjectRef:  safe,
		Title:       "ENFORCED: instruction file " + strconv.Quote(safe) + " altered from authored baseline — instructions not honored",
		DetailHash:  redact.Hash("instructions-enforced relPath=" + safe),
		OccurredAt:  at,
	}
}

// postureFinding builds one minimal-data content-threat finding.
func postureFinding(scope, rel string, sev model.Severity, title, key string, asi, llm []string, at time.Time) model.FindingReport {
	safe := textscan.SanitizeDisplay(rel)
	return model.FindingReport{
		Kind:        findingPosture,
		Severity:    sev,
		SubjectKind: subjectInstructions,
		SubjectRef:  safe,
		Title:       "instruction file " + strconv.Quote(safe) + ": " + title,
		DetailHash:  redact.Hash("agents-md-posture scope=" + scope + " file=" + safe + " " + key),
		OccurredAt:  at,
		OWASPASI:    asi,
		OWASPLLM:    llm,
	}
}

// --- helpers -----------------------------------------------------------------

// parseList parses a JSON array or comma list, falling back to defaults when
// empty.
func parseList(raw string, defaults []string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaults
	}
	var names []string
	if strings.HasPrefix(raw, "[") {
		if json.Unmarshal([]byte(raw), &names) != nil {
			return defaults
		}
	} else {
		names = strings.Split(raw, ",")
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return defaults
	}
	return out
}

// normPath normalizes a baseline/relative path to slash form without leading
// "./" so authored and walked paths compare equal.
func normPath(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(p)), "./")
}

// sortedPaths returns the baseline paths in deterministic order.
func sortedPaths(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func markerSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// skipDir mirrors the claude-config walk exclusions: heavy/irrelevant trees
// that never hold governed instruction files.
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build", "target", ".venv", "__pycache__", ".next", ".cache", ".idea":
		return true
	}
	return false
}

// readCapped reads up to limit bytes, reporting whether the file was larger.
func readCapped(path string, limit int64) (content string, oversized, ok bool) {
	fh, err := os.Open(path) //nolint:gosec // operator-provided repo path, read-only
	if err != nil {
		return "", false, false
	}
	defer func() { _ = fh.Close() }()
	buf, err := io.ReadAll(io.LimitReader(fh, limit+1))
	if err != nil {
		return "", false, false
	}
	if int64(len(buf)) > limit {
		return "", true, true
	}
	return string(buf), false, true
}
