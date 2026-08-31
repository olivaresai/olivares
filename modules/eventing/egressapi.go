// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The egress-policy surface: read what is in force, and ask what it would say.
//
// A control an operator cannot SEE is not one they can rely on, and a refusal a
// tenant cannot EXPLAIN is one they will open a ticket about. Until this existed the
// policy was a JSON file on the host and a 400 with no context: an author whose
// destination was refused could not tell an operator's rule from a typo, and an
// operator could not tell whether their file had been read at all — a boot log line
// is not an answer to "is it on right now".
//
// What it deliberately does NOT do is show a tenant the rules. The precise denial
// code is a membership oracle (see egressAuthoringError), and the rules themselves
// are worse: they name an operator's internal collectors. Both endpoints answer only
// about the destination the caller already supplied.

// egressPolicyStatusDTO is what a caller may know about the policy: whether one is in
// force, and where it came from. Never a rule, never a count — a count of rules is a
// small leak that compounds with the dry-run into an enumeration.
type egressPolicyStatusDTO struct {
	// InForce reports that an operator has authored a destination policy that applies
	// to this tenant.
	InForce bool `json:"in_force"`
	// Source names the policy for correlation with the operator's configuration. It
	// is an identifier the operator chose, never policy content.
	Source string `json:"source,omitempty"`
	// Unavailable reports that a policy exists but could not be READ right now, which
	// is why deliveries are being requeued rather than refused. It is the distinction
	// an operator needs to tell a misconfiguration from an outage.
	Unavailable bool `json:"unavailable,omitempty"`
	// Mode is this deployment's durable disposition for the control (unit G):
	// enforced, legacy_compat or policy_optional. It is a fact about the DEPLOYMENT, not
	// about another tenant, and it is the one thing that makes a refusal actionable —
	// "enforced with no policy authored" tells the caller the remediation belongs to a
	// platform operator, which is exactly what they need and cannot guess.
	Mode string `json:"mode"`
	// ModeUnavailable reports that the durable disposition itself could not be read, so
	// destinations are being refused retryably rather than because of any policy.
	ModeUnavailable bool `json:"mode_unavailable,omitempty"`
	// ClassifiedMode is what the engine decided when it first met this deployment, and
	// it never changes. Reporting it alongside Mode is what lets a reader tell an
	// inherited disposition from a chosen one.
	ClassifiedMode string `json:"classified_mode,omitempty"`
	// EnforcementCommitted reports that somebody decided this control is authoritative,
	// which is one-way.
	EnforcementCommitted bool `json:"enforcement_committed"`
	// Fence reports whether a binary that does not carry the egress gate can still author a
	// destination against this database (unit H). It is deployment-wide and READ tier: it is
	// not another tenant's data and not policy content, and it is the difference between "this
	// deployment enforces destinations" and "…and every writer is actually gated". An operator or
	// author who cannot see the second cannot tell what the first is worth.
	Fence egressFenceDTO `json:"writer_fence"`
	// Compat summarizes what THIS tenant relies on compatibility mode for. It is served
	// ONLY to a caller who may also read the itemized report, and that is a correction: an
	// earlier revision served it at read tier on the theory that counts disclose nothing.
	// They do. StillNeeded is computed by testing each grandfathered authority against the
	// operator's policy, so for a tenant with one known destination it answers "is my
	// destination on the operator's allow-list?" — the exact membership oracle unit F
	// collapsed every denial message to avoid, reopened through a count.
	Compat *egressCompatSummaryDTO `json:"compat,omitempty"`
}

// egressFenceDTO is the writer fence's posture. It reports capability levels, never who is
// running what: it cannot enumerate the fleet and does not pretend to.
type egressFenceDTO struct {
	// Armed reports that a writer must prove its capability to introduce or move a destination.
	Armed bool `json:"armed"`
	// Mode is the fence's durable disposition, and Generation the state version a writer must
	// attest against.
	Mode       string `json:"mode,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	// RequiredCapability is what the fence demands; BinaryCapability is what THIS binary declares.
	// Both are reported because an operator debugging a refusal needs the comparison, not the
	// verdict.
	RequiredCapability int64 `json:"required_capability"`
	BinaryCapability   int64 `json:"binary_capability"`
	// Unavailable reports that the fence's own state could not be read, which is neither armed nor
	// dormant — it is unknown, and it is reported as such rather than resolved to the convenient
	// answer.
	Unavailable bool `json:"unavailable,omitempty"`
}

// egressCompatSummaryDTO is the tenant-safe half of the compatibility record.
type egressCompatSummaryDTO struct {
	// Seeded reports that this tenant's pre-existing destinations have been recorded.
	Seeded bool `json:"seeded"`
	// Recorded is how many distinct destinations this deployment had when the line was
	// drawn — ALL of them, including any the policy in force already covers. It was
	// previously called "grandfathered", which claimed something narrower than what the
	// implementation counted.
	Recorded int `json:"recorded"`
	// StillNeeded is how many of those the policy in force does NOT cover — i.e. how many
	// stop working when the operator enforces.
	StillNeeded int `json:"still_needed"`
	// Intact reports that the recorded set still matches its own seed. False is the shape a
	// partial restore produces, and it means the two counts above describe less than the
	// record claims.
	Intact bool `json:"intact"`
	// Unparsable is how many stored endpoints cannot be canonicalized, and therefore
	// could never be grandfathered whatever the operator does.
	Unparsable int `json:"unparsable"`
}

// egressCheckRequest asks what the policy would say about one destination.
type egressCheckRequest struct {
	Endpoint string `json:"endpoint"`
	// SubscriptionID, when supplied, asks the question AS that subscription — which is
	// the only way the answer can match what its deliveries actually get under
	// compatibility mode. Omitting it asks as a destination that does not exist yet,
	// which is the right question before a create and the stricter of the two.
	SubscriptionID string `json:"subscription_id,omitempty"`
}

// egressCheckResponse answers it. The reason is the SAME collapsed message the
// authoring path returns, for the same reason: distinguishing "allowed host, wrong
// port" from "host not allowed" would let a caller enumerate the operator's list by
// probing. A dry-run must not be a better oracle than actually trying.
type egressCheckResponse struct {
	// Permitted is the verdict for the supplied destination.
	Permitted bool `json:"permitted"`
	// Reason explains a refusal in the caller's own terms, naming only what they sent.
	Reason string `json:"reason,omitempty"`
	// PolicyInForce lets a caller tell "permitted because nothing constrains it" from
	// "permitted because a rule allows it" — which is the difference between a
	// destination that will keep working and one that depends on an operator's file.
	PolicyInForce bool `json:"policy_in_force"`
	// Grandfathered reports that the permit came from the compatibility record rather
	// than from the policy — i.e. this destination STOPS WORKING when the operator
	// actuates. A caller who could not tell the two permits apart would read "permitted"
	// as "will keep working", which is the one thing it does not mean.
	Grandfathered bool `json:"grandfathered,omitempty"`
}

// handleEgressPolicyStatus reports whether a destination policy is in force here, and
// what this deployment's disposition for the control is.
//
// The disposition is included at READ tier deliberately. It is not another tenant's
// data and it is not policy content: it is the difference between "your destination
// was refused" and "this deployment has authorized nothing yet, and a platform
// operator has to", which is the only part of a refusal the caller can act on. Unit F
// collapsed every denial to one message to avoid a membership oracle, and this does
// not reopen it — knowing the control is enforced tells a caller nothing about WHICH
// destinations any policy would permit.
func (m *Module) handleEgressPolicyStatus(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	p := m.resolvePolicy(r.Context(), mc.Tenant)
	out := egressPolicyStatusDTO{
		InForce:     p.InForce && !p.Unavailable,
		Source:      p.Ref,
		Unavailable: p.Unavailable,
	}
	// The fence's posture, reported alongside the destination control's. NEVER as "the fleet is
	// proved": armed means a violation now fails visibly, not that no old writer exists.
	out.Fence = egressFenceDTO{BinaryCapability: EgressWriterCapability}
	if fence, ferr := m.resolveFence(r.Context()); ferr != nil {
		out.Fence.Unavailable = true
	} else {
		out.Fence.Armed = fence.Armed
		out.Fence.Mode = string(fence.Mode)
		out.Fence.Generation = fence.Generation
		out.Fence.RequiredCapability = fence.RequiredCapability
	}
	st, rerr := m.resolveRollout(r.Context())
	if rerr != nil {
		out.ModeUnavailable = true
	} else {
		out.Mode = string(st.CurrentMode)
		out.ClassifiedMode = string(st.ClassifiedMode)
		out.EnforcementCommitted = st.EnforcementCommitted
		// Only under compatibility mode is there anything to summarize: in the other two
		// the compatibility record has no effect on any decision, and reporting a count
		// that changes nothing would invite an operator to act on it.
		// The summary is admin-tier information even though it is served from this route, so
		// it is attached only when the CALLER may read the itemized report. A read-tier caller
		// gets the mode — which is a fact about the deployment and tells them who owns the
		// remediation — and nothing that is a function of the operator's allow-list.
		if st.CurrentMode == store.RolloutLegacyCompat && m.compat != nil && m.authz != nil &&
			m.authz.Allowed(r.Context(), mc.Principal, permSubAdmin, mc.Tenant) {
			if rep, err := m.compat.report(r.Context(), mc.Tenant, p); err == nil {
				out.Compat = &egressCompatSummaryDTO{
					Seeded:      rep.Seeded,
					Intact:      rep.Intact,
					Recorded:    len(rep.Authorities),
					StillNeeded: rep.StillNeeded,
					Unparsable:  rep.Unparsed,
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleEgressCompatReport is the itemized compatibility record: which destinations
// this tenant keeps only because the deployment predates the control, and which of
// them the policy in force already covers.
//
// It is ADMIN tier, not read tier, because it names hosts. Those hosts are this
// tenant's own destinations, so it is not a cross-tenant disclosure — but the whole
// point of the report is planning an actuation, which is an administrative act, and
// the list is also the closest thing in this surface to an enumeration of what an
// operator's collectors are called.
func (m *Module) handleEgressCompatReport(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.compat == nil {
		writeJSON(w, http.StatusOK, LegacyExceptionReport{})
		return
	}
	p := m.resolvePolicy(r.Context(), mc.Tenant)
	rep, err := m.compat.report(r.Context(), mc.Tenant, p)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// handleEgressPolicyCheck answers "would this destination be permitted", without
// creating anything.
//
// It exists so an author can find out BEFORE writing a subscription, and so an
// operator can verify a rule they just wrote actually covers the collector they meant
// — which, before this, could only be discovered by creating a subscription and
// watching a delivery fail.
func (m *Module) handleEgressPolicyCheck(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in egressCheckRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	if in.Endpoint == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("endpoint is required"))
		return
	}
	if len(in.Endpoint) > maxEndpointLen {
		writeJSON(w, http.StatusBadRequest, errorBody("endpoint too long"))
		return
	}
	// The transport rule first, exactly as send() applies it, so a dry-run cannot
	// report "permitted" for a URL the sender would refuse.
	if msg := validateEndpointURL(in.Endpoint, m.allowLoopback); msg != "" {
		writeJSON(w, http.StatusOK, egressCheckResponse{Permitted: false, Reason: msg})
		return
	}
	p := m.resolvePolicy(r.Context(), mc.Tenant)
	// A dry run WITH a subscription id asks as that subscription; without one it asks
	// as a create, which is the stricter question and the right one before authoring.
	subRef := model.ID(strings.TrimSpace(in.SubscriptionID))
	purpose := EgressCreate
	if subRef != "" {
		purpose = EgressDryRun
	}
	d, err := m.authorizeDestination(r.Context(), egressRequest{
		Tenant: mc.Tenant, Purpose: purpose, URL: in.Endpoint, SubscriptionRef: subRef,
	})
	if err != nil {
		m.debugf("eventing: egress dry-run could not decide", "code", d.Code, "err", err)
	}
	out := egressCheckResponse{
		Permitted:     d.Permitted,
		PolicyInForce: p.InForce && !p.Unavailable,
		Grandfathered: d.Code == egress.CodeLegacyException,
	}
	if !d.Permitted {
		out.Reason = egressAuthoringError(in.Endpoint, d.Code)
	}
	writeJSON(w, http.StatusOK, out)
}
