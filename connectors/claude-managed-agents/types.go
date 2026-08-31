// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
)

// CMA resource id prefixes (verified, jun-2026). These are REFERENCES the connector
// ingests — never credential material, memory content or payloads (docs/SECURITY-HARDENING.md).
const (
	prefixVault       = "vlt_"
	prefixVaultCred   = "vcrd_"
	prefixMemoryStore = "memstore_"
	prefixMemoryVer   = "memver_"
	prefixEnvironment = "env_"
	prefixWork        = "work_"
	prefixOutcome     = "outc_"
	prefixSkillCustom = "skill_"
	prefixDream       = "drm_"  // Dreams research preview
	prefixThread      = "sthr_" // multi-agent session thread
	prefixAgentDef    = "agent_"
)

// Resource / subject kinds. These literal strings are the contract between this Apache
// connector (which emits them) and the AGPL modules that recognize them (inventory,
// security, capabilities): they MUST agree by VALUE — there is no shared import across the
// license boundary (LICENSING.md; scripts/check-boundary.sh). The "anthropic." namespace
// reuses the established connectors/claude convention (resVault/managedAgentSubject).
const (
	kindVault         = "anthropic.vault"
	kindVaultCred     = "anthropic.vault_credential"
	kindMemoryStore   = "anthropic.memory_store"
	kindMemoryVersion = "anthropic.memory_version"
	kindSkill         = "anthropic.skill"
	kindEnvironment   = "anthropic.environment"
	kindManagedAgent  = "anthropic.managed_agent" // a session / managed-agent run (incl. threads)
	kindPermPolicy    = "anthropic.permission_policy"
	kindOutcome       = "anthropic.outcome"
	kindDream         = "anthropic.dream"      // a Dreams memory-curation job
	kindAgentTool     = "anthropic.agent_tool" // a built-in/custom tool declared on an agent's tools[]
	kindAgentDef      = "anthropic.agent"      // an agent DEFINITION (vs kindManagedAgent, a run)
)

// Origin kinds for CMA edges (the access-map "who").
const (
	originSession     = "session"
	originAgent       = "agent"
	originWorkspace   = "workspace"
	originEnvironment = "environment"
)

// FindingReport.Kind values the connector emits. The security module persists a CMA
// finding only when it is ≥High (any kind) or matches the ANT2-14 HITL-queue pairs —
// kind governance with subject anthropic.managed_agent or anthropic.memory_store
// (modules/security onEvent carve-out, by VALUE); lower-severity governance/posture
// facts ride the bus to the ledger/notify subscribers only. forensic mirrors the
// claude connector's session-event convention; self_audit records the connector's own
// posture (e.g. a degraded poll) so the ledger carries proof of coverage.
const (
	findingGovernance = "governance"
	findingPosture    = "posture"
	findingForensic   = "forensic"
	findingSelfAudit  = "self_audit"
)

// OWASP Agentic Security Initiative (ASI) taxonomy refs used on CMA findings (2026).
const (
	asiIdentityAbuse = "ASI03" // Identity & Privilege Abuse — vault/credential lateral movement
	asiMemoryPoison  = "ASI06" // Memory poisoning / tampering — read_write memory write targets
	asiSupplyChain   = "ASI09" // Tool/skill supply-chain — unpinned or executable skills
)

// parseTime parses an RFC3339 / RFC3339Nano timestamp, returning the zero time on any
// parse failure (the caller falls back to the observation time).
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// labelRef returns a scrubbed, display-safe reference label, defaulting to fallback when
// the ref is blank (so a finding/edge never renders a bare empty reference).
func labelRef(ref, fallback string) string {
	if r := strings.TrimSpace(redact.Clean(ref)); r != "" {
		return r
	}
	return fallback
}
