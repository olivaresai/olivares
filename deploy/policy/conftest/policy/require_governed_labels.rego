# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

# require_governed_labels enforces the identity-bound half of the Olivares
# posture (ARCHITECTURE.md — every action is attributable to a tenant and an identity).
# A Deployment must carry both governance labels:
#
#   olivares.ai/tenant     — the tenant the workload belongs to
#   olivares.ai/identity   — the workload identity it runs as
#
# Without these, an observed access flow cannot be bound to a subject, and the
# control plane's ABAC stance (deny unless the actor is known) cannot evaluate.
# The gate refuses an unattributable workload at merge time — the same
# deny-by-default shape the runtime PDP applies, mirrored client-side. It does
# NOT call the PDP and does NOT decide access; it only requires the manifest to
# carry the identity binding the PDP will later key on.
package main

import rego.v1

# required_labels is the identity-binding label set. Both are mandatory; a
# missing or empty value is a violation (an empty string is not an identity).
required_labels := {"olivares.ai/tenant", "olivares.ai/identity"}

# deny fires once per missing/blank governance label on a Deployment. Only
# Deployments are gated here (per the identity-bound posture for the deployable
# unit); broadening to other kinds is a deliberate future decision, not an
# accident of the rule.
deny contains msg if {
	input.kind == "Deployment"
	some label in required_labels
	not has_nonempty_label(label)
	msg := sprintf(
		"%s/%s: missing governance label %q — Deployments must be identity-bound (olivares.ai/tenant + olivares.ai/identity)",
		[input.kind, name, label],
	)
}

# has_nonempty_label reports whether metadata.labels carries `label` with a
# non-empty string value. A label present but set to "" does not count.
has_nonempty_label(label) if {
	value := object.get(input, ["metadata", "labels", label], "")
	value != ""
}

name := object.get(input, ["metadata", "name"], "<unnamed>")
