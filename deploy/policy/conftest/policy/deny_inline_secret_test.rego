# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

# Unit tests for deny_inline_secret.rego, run with `conftest verify` (OPA's
# test_ convention + `with input` overrides).
#
# NOTE on combined evaluation: every rule in this pack is `deny` in `package
# main`, so `conftest verify` evaluates ALL of them against each input (the
# canonical Conftest shape). A "clean" assertion therefore uses a FULLY
# conformant manifest (labels + runAsNonRoot + secretRef); a "violating"
# assertion isolates the inline-secret defect by counting only the messages this
# rule emits (`inline`), so a co-incidental deny from another rule never makes
# the test pass or fail for the wrong reason.
package main

import rego.v1

# inline_denials is the subset of deny messages this rule is responsible for.
inline_denials contains msg if {
	some msg in deny
	contains(msg, "inline")
}

# A fully conformant workload that references a credential via secretKeyRef
# carries no inline secret — the whole pack is clean.
test_env_secret_ref_is_clean if {
	count(deny) == 0 with input as conformant
}

# The conformant workload with the credential pasted inline must be denied by
# THIS rule (the env case).
test_env_inline_literal_is_denied if {
	count(inline_denials) > 0 with input as object.union(conformant, {"spec": {"template": {"spec": {
		"securityContext": {"runAsNonRoot": true},
		"containers": [{"name": "app", "env": [{"name": "API_KEY", "value": "AKIAIOSFODNN7EXAMPLE"}]}],
	}}}})
}

# An inline literal outside the container env shape (a CRD-style field) must be
# denied by the generic walk arm of this rule.
test_inline_field_literal_is_denied if {
	count(inline_denials) > 0 with input as {
		"apiVersion": "example.com/v1",
		"kind": "Widget",
		"metadata": {"name": "bad-widget"},
		"spec": {"password": "hunter2-not-a-ref"},
	}
}

# A manifest with no sensitive fields and no workload shape is clean.
test_no_sensitive_fields_is_clean if {
	count(deny) == 0 with input as {
		"apiVersion": "v1",
		"kind": "ConfigMap",
		"metadata": {"name": "plain"},
		"data": {"url": "https://example.com"},
	}
}

# conformant is a Deployment that satisfies all three rules of the pack: it is
# identity-bound, drops root, and references its credential from a Secret.
conformant := {
	"apiVersion": "apps/v1",
	"kind": "Deployment",
	"metadata": {
		"name": "good",
		"labels": {"olivares.ai/tenant": "acme", "olivares.ai/identity": "payments-api"},
	},
	"spec": {"template": {"spec": {
		"securityContext": {"runAsNonRoot": true},
		"containers": [{
			"name": "app",
			"env": [{
				"name": "API_KEY",
				"valueFrom": {"secretKeyRef": {"name": "app-creds", "key": "api-key"}},
			}],
		}],
	}}},
}
