# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

# Unit tests for require_governed_labels.rego (`conftest verify`).
#
# Every rule in the pack is `deny` in `package main` and evaluated together. A
# "clean" assertion uses a fully conformant manifest; a "violating" assertion
# counts only the messages THIS rule emits (`governance label`) so a deny from
# the root/secret rules cannot fake the result.
package main

import rego.v1

# label_denials is the subset of deny messages this rule owns.
label_denials contains msg if {
	some msg in deny
	contains(msg, "governance label")
}

# A Deployment carrying both labels (and otherwise conformant) is clean.
test_both_labels_present_is_clean if {
	count(deny) == 0 with input as with_labels({"olivares.ai/tenant": "acme", "olivares.ai/identity": "payments-api"})
}

# Missing olivares.ai/identity must be denied by this rule.
test_missing_identity_is_denied if {
	count(label_denials) > 0 with input as with_labels({"olivares.ai/tenant": "acme"})
}

# Missing BOTH labels fires this rule exactly twice (once per required label).
test_missing_both_fires_once_per_label if {
	count(label_denials) == 2 with input as with_labels({})
}

# A present-but-empty label value does not satisfy the rule.
test_empty_label_value_is_denied if {
	count(label_denials) > 0 with input as with_labels({"olivares.ai/tenant": "acme", "olivares.ai/identity": ""})
}

# A non-Deployment kind is out of scope for the label requirement.
test_non_deployment_is_ignored if {
	count(label_denials) == 0 with input as {
		"apiVersion": "v1",
		"kind": "ConfigMap",
		"metadata": {"name": "cm"},
		"data": {"k": "v"},
	}
}

# with_labels builds an otherwise-conformant Deployment (drops root, no inline
# secret) carrying the given labels, so only the label rule is under test.
with_labels(labels) := {
	"apiVersion": "apps/v1",
	"kind": "Deployment",
	"metadata": {"name": "wl", "labels": labels},
	"spec": {"template": {"spec": {
		"securityContext": {"runAsNonRoot": true},
		"containers": [{"name": "app", "image": "app:1.0"}],
	}}},
}
