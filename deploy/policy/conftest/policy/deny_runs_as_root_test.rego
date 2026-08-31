# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

# Unit tests for deny_runs_as_root.rego (`conftest verify`).
#
# As with the other rules in this pack, every rule is `deny` in `package main`
# and is evaluated together. A "clean" assertion uses a fully conformant
# manifest; a "violating" assertion counts only the messages THIS rule emits
# (`runAsNonRoot`) so another rule's deny cannot mask or fake the result.
package main

import rego.v1

# root_denials is the subset of deny messages this rule owns.
root_denials contains msg if {
	some msg in deny
	contains(msg, "runAsNonRoot")
}

# Pod-level runAsNonRoot=true covers every container — the whole pack is clean.
test_pod_run_as_non_root_is_clean if {
	count(deny) == 0 with input as governed({"spec": {"template": {"spec": {
		"securityContext": {"runAsNonRoot": true},
		"containers": [{"name": "app", "image": "app:1.0"}],
	}}}})
}

# Container-level runAsNonRoot=true on every container — clean.
test_container_run_as_non_root_is_clean if {
	count(deny) == 0 with input as governed({"spec": {"template": {"spec": {"containers": [{
		"name": "app",
		"image": "app:1.0",
		"securityContext": {"runAsNonRoot": true},
	}]}}}})
}

# A workload that sets runAsNonRoot nowhere must be denied by this rule.
test_no_run_as_non_root_is_denied if {
	count(root_denials) > 0 with input as governed({"spec": {"template": {"spec": {"containers": [{"name": "app", "image": "app:1.0"}]}}}})
}

# An explicit runAsNonRoot=false is NOT proven non-root — denied by this rule.
test_explicit_run_as_root_is_denied if {
	count(root_denials) > 0 with input as governed({"spec": {"template": {"spec": {
		"securityContext": {"runAsNonRoot": false},
		"containers": [{"name": "app", "image": "app:1.0"}],
	}}}})
}

# A non-workload kind is out of scope — this rule never denies it.
test_non_workload_is_ignored if {
	count(root_denials) == 0 with input as {
		"apiVersion": "v1",
		"kind": "Service",
		"metadata": {"name": "svc"},
		"spec": {"selector": {"app": "x"}},
	}
}

# governed merges spec into a Deployment that already carries the governance
# labels, so the label rule never fires and the test isolates the root rule.
governed(spec) := object.union(
	{
		"apiVersion": "apps/v1",
		"kind": "Deployment",
		"metadata": {
			"name": "wl",
			"labels": {"olivares.ai/tenant": "acme", "olivares.ai/identity": "wl"},
		},
	},
	spec,
)
