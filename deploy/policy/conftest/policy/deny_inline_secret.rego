# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

# deny_inline_secret enforces the "no inline secrets" half of the Olivares
# governance posture (docs/SECURITY-HARDENING.md, minimal-data): a credential must live in a
# Kubernetes Secret and be referenced (secretKeyRef / secretRef / a mounted
# Secret volume), NEVER pasted as a literal value into a manifest the customer
# commits to Git. A merged plaintext credential is a standing exfiltration path
# and survives in Git history forever; the gate stops it before merge.
#
# This is the CLIENT-SIDE binding of the deny-by-default stance, not the policy
# decision point. The control plane's PDP (Cedar+OPA) is the authority
# at runtime; here we only mirror its "deny unless proven safe" shape on the
# customer's IaC. We never call the PDP and never reimplement it.
package main

import rego.v1

# sensitiveKeyPattern matches the NAME of a field that conventionally holds a
# credential. Case-insensitive; covers password / secret / token and the
# api_key / api-key / apikey spellings. It intentionally over-matches on the key
# name — the value test below is what decides whether a finding fires, so a
# benign key that happens to match (e.g. a Secret's own `data`) is not penalised
# unless it actually carries an inline literal.
sensitive_key_pattern := `(?i)(password|secret|token|api[_-]?key)`

# deny fires for every container env var that sets a sensitive value INLINE
# (spec...env[].value) rather than via valueFrom.secretKeyRef. An env var that
# sources its value from a Secret carries no `value`, so it never matches.
deny contains msg if {
	some container in workload_containers
	some env in container.env
	regex.match(sensitive_key_pattern, env.name)
	env.value != ""
	msg := sprintf(
		"%s/%s: container %q sets sensitive env %q to an inline literal — use valueFrom.secretKeyRef into a Kubernetes Secret (no inline credentials)",
		[input.kind, name, object.get(container, "name", "<unnamed>"), env.name],
	)
}

# deny fires for any other manifest field whose KEY is sensitive and whose VALUE
# is a plain literal string. This catches inline credentials outside the
# container env shape — e.g. a Helm-rendered ConfigMap, a CRD spec, an
# annotation — anywhere a literal password/token/apiKey is embedded. A value
# that is itself a reference object (secretKeyRef / secretRef / valueFrom) is a
# map, not a string, so it is correctly exempt.
deny contains msg if {
	[path, value] := walk(input)
	some key in path
	is_string(key)
	regex.match(sensitive_key_pattern, key)
	is_string(value)
	value != ""
	not under_container_env(path) # the env case is reported above; avoid a duplicate
	msg := sprintf(
		"%s/%s: field %q at %v holds an inline literal credential — reference a Kubernetes Secret instead (no inline secrets)",
		[input.kind, name, key, path],
	)
}

# name is the manifest's metadata.name, defaulted so a message is always legible
# even on a nameless fragment.
name := object.get(input, ["metadata", "name"], "<unnamed>")

# workload_containers is every container (regular + init + ephemeral) of a
# pod-bearing workload, OR the containers of a bare Pod. Empty for a non-workload
# manifest, so the env rule simply does not fire there.
workload_containers contains c if {
	some c in object.get(pod_spec, "containers", [])
}

workload_containers contains c if {
	some c in object.get(pod_spec, "initContainers", [])
}

workload_containers contains c if {
	some c in object.get(pod_spec, "ephemeralContainers", [])
}

# pod_spec resolves the PodSpec for both the workload shape
# (spec.template.spec — Deployment/StatefulSet/DaemonSet/Job/ReplicaSet) and the
# bare-Pod shape (spec).
pod_spec := input.spec.template.spec

pod_spec := input.spec if not input.spec.template.spec

# under_container_env reports whether a walk path sits inside a container env
# entry, so the generic rule does not double-report what the env rule covers.
under_container_env(path) if {
	some i
	path[i] == "env"
}
