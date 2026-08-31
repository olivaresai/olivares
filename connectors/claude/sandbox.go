// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file OBSERVES the Claude Code SANDBOX lockdown + egress allowlist (ANT2-13) and
// models the resulting CONTAINMENT POSTURE. It is read-only: it reads the settings
// file's sandbox.* block and emits posture findings — it never authors the policy
// (that is). It surfaces the postures that WEAKEN containment (unsandboxed
// commands allowed; fail-open if the sandbox is unavailable) and the egress allowlist,
// and it records the verbatim caveat that the egress proxy does NOT do TLS inspection
// — so DENIED domains can be reached by DOMAIN-FRONTING. That caveat is surfaced as a
// documented limitation, never papered over as full coverage (docs/SECURITY-HARDENING.md).
//
// Authority (verbatim, jun-2026): code.claude.com/docs/en/sandboxing + network-config.
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// FindingReport subjects for the sandbox/egress posture.
const (
	subjectSandbox = "claude.sandbox"
	subjectEgress  = "claude.egress"
)

// defaultEgressAllowlist is the documented default egress allowlist a locked-down
// Claude Code sandbox permits (verified jun-2026). It is the baseline a posture view
// compares the observed allowlist against; it is reference data, not fabricated.
var defaultEgressAllowlist = []string{
	"api.anthropic.com", "claude.ai", "platform.claude.com",
	"downloads.claude.ai", "raw.githubusercontent.com",
}

// DefaultEgressAllowlist returns the documented default egress allowlist (a copy).
func DefaultEgressAllowlist() []string {
	return append([]string(nil), defaultEgressAllowlist...)
}

// SandboxConfig is the observed sandbox.* lockdown. Pointer bools distinguish
// "explicitly set" from "absent" so the connector flags only an EXPLICIT weakening,
// never a default it cannot see.
type SandboxConfig struct {
	FailIfUnavailable        *bool    `json:"failIfUnavailable"`
	AllowUnsandboxedCommands *bool    `json:"allowUnsandboxedCommands"`
	Filesystem               fsRules  `json:"filesystem"`
	AllowedDomains           []string `json:"allowedDomains"`
	DeniedDomains            []string `json:"deniedDomains"`
	AllowManagedDomainsOnly  *bool    `json:"allowManagedDomainsOnly"`
}

// fsRules is the sandbox filesystem allow/deny posture.
type fsRules struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// settingsFile is the slice of a Claude Code settings file the sandbox observer reads.
type settingsFile struct {
	Sandbox *SandboxConfig `json:"sandbox"`
}

// parseSandbox decodes the settings file and returns its sandbox block (nil when the
// file carries none). A malformed file is an error (a broken posture is surfaced).
func parseSandbox(body []byte) (*SandboxConfig, error) {
	var f settingsFile
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("claude: parse sandbox settings: %w", err)
	}
	return f.Sandbox, nil
}

// SandboxPostureFindings computes the posture findings for a sandbox config (ANT2-13).
// It is a pure function (the testable core): it flags an EXPLICITLY weakened posture
// (unsandboxed commands allowed → high; fail-open if unavailable → medium), summarizes
// the egress allowlist, and ALWAYS records the domain-fronting caveat when an egress
// control is present (the proxy does not TLS-inspect). A nil config yields no findings.
func SandboxPostureFindings(sb *SandboxConfig, at time.Time) []model.FindingReport {
	if sb == nil {
		return nil
	}
	var out []model.FindingReport

	if sb.AllowUnsandboxedCommands != nil && *sb.AllowUnsandboxedCommands {
		out = append(out, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityHigh,
			SubjectKind: subjectSandbox,
			SubjectRef:  "allowUnsandboxedCommands",
			Title:       "Sandbox allows unsandboxed commands (containment can be bypassed)",
			DetailHash:  redact.Hash("sandbox.allowUnsandboxedCommands=true weakens OS-level containment (ANT2-13)"),
			OccurredAt:  at,
		})
	}
	if sb.FailIfUnavailable != nil && !*sb.FailIfUnavailable {
		out = append(out, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectSandbox,
			SubjectRef:  "failIfUnavailable",
			Title:       "Sandbox fails OPEN when unavailable (commands run unsandboxed)",
			DetailHash:  redact.Hash("sandbox.failIfUnavailable=false runs unsandboxed when the sandbox cannot start (ANT2-13)"),
			OccurredAt:  at,
		})
	}

	// Egress allowlist posture + the verbatim domain-fronting caveat. An egress control
	// exists when domains are allow/deny-listed; the caveat is that the proxy does NOT
	// inspect TLS, so a denied domain can be reached by domain-fronting.
	if len(sb.AllowedDomains) > 0 || len(sb.DeniedDomains) > 0 || (sb.AllowManagedDomainsOnly != nil && *sb.AllowManagedDomainsOnly) {
		out = append(out, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityInfo,
			SubjectKind: subjectEgress,
			SubjectRef:  "allowlist",
			Title:       fmt.Sprintf("Egress allowlist observed (%d allowed, %d denied) — proxy does NOT TLS-inspect (domain-fronting caveat)", len(sb.AllowedDomains), len(sb.DeniedDomains)),
			DetailHash:  redact.Hash("egress allow=[" + strings.Join(sb.AllowedDomains, ",") + "] deny=[" + strings.Join(sb.DeniedDomains, ",") + "]; proxy does not TLS-inspect → denied domains reachable via domain-fronting (ANT2-13 caveat, NOT covered)"),
			OccurredAt:  at,
		})
	}
	return out
}

// gatherSandbox reads the observed settings file (if configured) and emits the sandbox
// containment posture. A missing file is a no-op; an unreadable/malformed file is a
// medium posture finding. It never authors the policy.
func (s *Source) gatherSandbox(at time.Time, dispatch func(model.Observation)) {
	if s.cfg.sandboxPath == "" {
		return
	}
	body, err := os.ReadFile(s.cfg.sandboxPath)
	if err != nil {
		dispatch(model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectSandbox,
			SubjectRef:  s.cfg.sandboxPath,
			Title:       "sandbox settings not readable",
			DetailHash:  redact.Hash("sandbox settings read error: " + err.Error()),
			OccurredAt:  at,
		})
		return
	}
	sb, err := parseSandbox(body)
	if err != nil {
		dispatch(model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectSandbox,
			SubjectRef:  s.cfg.sandboxPath,
			Title:       "sandbox settings malformed",
			DetailHash:  redact.Hash("sandbox settings parse error"),
			OccurredAt:  at,
		})
		return
	}
	for _, f := range SandboxPostureFindings(sb, at) {
		dispatch(f)
	}
}
