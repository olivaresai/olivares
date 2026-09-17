// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"sort"
	"strconv"
)

// policysnapshot.go is ONE pure method: a content digest over the COMPLETE effective
// governance posture a request was decided under. It adds no loader, no schema, no API
// and no stored column — Policy keeps composing the row exactly as it did.
//
// It exists because the effective posture includes the private DLP rule map, and a
// caller outside this package cannot see it. cmd can read Configured, FailOpen,
// ResponseDLPMode, the gate flags and the ceilings through their exported fields, so it
// could hash an approximation — and that approximation would be a LIE in the one
// direction that matters: two requests decided under DIFFERENT egress rules would bind
// the same digest, and the evidence would say the governance was identical when the
// thing that actually denied or allowed the content had changed. A digest that cannot
// see the deciding input is worse than no digest, because it looks like one.
//
// It is a CONTENT digest, not a version: nothing here reads a row id, a stored revision
// or a timestamp. Two tenants with the same effective posture produce the same value, and
// that is correct — the digest answers "under which rules", not "from which row".
const proxyPolicyDigestDomain = "olivares.inferenceproxy.policy.v1"

// EvidenceDigest returns the v1 domain-separated SHA-256 over the complete effective
// policy: every configuration flag, the recording posture and ITS provenance, all four
// ceilings, and the effective seeded-plus-tenant DLP rule set.
//
// The DLP rules are emitted in (class, action) order, so the digest is a property of the
// RULE SET and not of the order a loader happened to walk a map in — a Go map iterates
// randomly, so a digest that consumed it directly would differ between two calls on the
// SAME policy. Every frame is length-prefixed, so no combination of field values can be
// re-partitioned into a different combination that hashes the same.
func (p ProxyPolicy) EvidenceDigest() [sha256.Size]byte {
	h := sha256.New()
	writeDigestField(h, "domain", proxyPolicyDigestDomain)
	writeDigestField(h, "configured", strconv.FormatBool(p.Configured))
	writeDigestField(h, "fail_open", strconv.FormatBool(p.FailOpen))
	writeDigestField(h, "response_dlp_mode", p.ResponseDLPMode)
	writeDigestField(h, "record_mandatory", strconv.FormatBool(p.RecordMandatory))
	writeDigestField(h, "record_mandatory_chosen", strconv.FormatBool(p.RecordMandatoryChosen))
	writeDigestField(h, "gate_model_access", strconv.FormatBool(p.GateModelAccess))
	writeDigestField(h, "gate_budget", strconv.FormatBool(p.GateBudget))
	writeDigestField(h, "gate_residency", strconv.FormatBool(p.GateResidency))
	writeDigestField(h, "gate_context_window", strconv.FormatBool(p.GateContextWindow))
	writeDigestField(h, "gate_dlp_request", strconv.FormatBool(p.GateDLPRequest))
	writeDigestField(h, "gate_dlp_response", strconv.FormatBool(p.GateDLPResponse))
	writeDigestField(h, "ceilings_enforce", strconv.FormatBool(p.Ceilings.Enforce))
	writeDigestField(h, "ceilings_max_tokens", strconv.FormatInt(p.Ceilings.MaxTokens, 10))
	writeDigestField(h, "ceilings_max_tool_uses", strconv.FormatInt(p.Ceilings.MaxToolUses, 10))
	writeDigestField(h, "ceilings_task_budget_tokens", strconv.FormatInt(p.Ceilings.TaskBudgetTokens, 10))

	rules := make([][2]string, 0, len(p.dlp.rules))
	for class, action := range p.dlp.rules {
		rules = append(rules, [2]string{class, action})
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i][0] != rules[j][0] {
			return rules[i][0] < rules[j][0]
		}
		return rules[i][1] < rules[j][1]
	})
	writeDigestField(h, "dlp_rules", strconv.Itoa(len(rules)))
	for _, rule := range rules {
		writeDigestField(h, "dlp_class", rule[0])
		writeDigestField(h, "dlp_action", rule[1])
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// writeDigestField frames one name/value pair unambiguously: an 8-byte big-endian
// length before each side. Concatenating names and values without the lengths would
// let ("ab","c") and ("a","bc") collide.
func writeDigestField(h hash.Hash, name, value string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(name)))
	_, _ = h.Write(n[:])
	_, _ = h.Write([]byte(name))
	binary.BigEndian.PutUint64(n[:], uint64(len(value)))
	_, _ = h.Write(n[:])
	_, _ = h.Write([]byte(value))
}
