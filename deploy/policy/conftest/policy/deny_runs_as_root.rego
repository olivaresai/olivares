# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

# deny_runs_as_root enforces the least-privilege half of the Olivares posture:
# a Kubernetes workload must declare runAsNonRoot=true, either on the pod
# securityContext or on every container's securityContext. A workload that may
# run as UID 0 is a privilege-escalation surface; deny-by-default means a
# manifest that does not PROVE it drops root is rejected, never assumed safe.
#
# This mirrors the control plane's deny-only ABAC stance on the customer's IaC;
# it is not the PDP. runAsNonRoot is the same gate the upstream Pod Security
# "restricted" profile applies (kubernetes.io/docs/concepts/security/pod-security-standards/);
# we encode it here so the customer catches it before merge, not at admission.
package main

import rego.v1

# deny fires when a workload does not establish runAsNonRoot at the pod level
# AND at least one container also fails to establish it. A pod-level
# runAsNonRoot=true covers every container, so it alone satisfies the rule; a
# container-level runAsNonRoot=true covers that container. Both must be absent
# for a container for the workload to be denied. Anything other than the literal
# `true` (absent, or an explicit false) is treated as NOT proven non-root —
# never guessed safe.
deny contains msg if {
	is_workload
	not pod_runs_as_non_root
	some container in all_containers
	not container_runs_as_non_root(container)
	msg := sprintf(
		"%s/%s: container %q does not set runAsNonRoot=true (pod nor container securityContext) — workloads must drop root (least privilege)",
		[input.kind, name, object.get(container, "name", "<unnamed>")],
	)
}

# pod_runs_as_non_root reports whether the pod securityContext pins
# runAsNonRoot to exactly true.
pod_runs_as_non_root if {
	object.get(pod_spec, ["securityContext", "runAsNonRoot"], false) == true
}

# container_runs_as_non_root reports whether a container's own securityContext
# pins runAsNonRoot to exactly true.
container_runs_as_non_root(container) if {
	object.get(container, ["securityContext", "runAsNonRoot"], false) == true
}

# is_workload restricts the rule to the pod-bearing kinds plus a bare Pod, so a
# Service / ConfigMap / CRD sharing the bundle is never spuriously denied.
is_workload if input.kind in workload_kinds

workload_kinds := {"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Pod"}

# all_containers is every regular + init container of the resolved PodSpec.
all_containers contains c if {
	some c in object.get(pod_spec, "containers", [])
}

all_containers contains c if {
	some c in object.get(pod_spec, "initContainers", [])
}

# pod_spec resolves the PodSpec across the workload shapes:
#   - CronJob nests it under spec.jobTemplate.spec.template.spec
#   - other workloads under spec.template.spec
#   - a bare Pod under spec
pod_spec := input.spec.jobTemplate.spec.template.spec

pod_spec := input.spec.template.spec if not input.spec.jobTemplate

pod_spec := input.spec if {
	not input.spec.jobTemplate
	not input.spec.template
}

name := object.get(input, ["metadata", "name"], "<unnamed>")
