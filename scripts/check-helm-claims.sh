#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Assert that every rendered persistentVolumeClaim.claimName has a producer in
# the same manifest: either an explicit PersistentVolumeClaim or a concrete PVC
# name derived from a StatefulSet volumeClaimTemplate + ordinal. This catches a
# workload that names a PVC which the selected Helm topology never creates.
set -eu

[ "$#" -eq 1 ] || { echo "usage: $0 <rendered-manifest.yaml>" >&2; exit 2; }
MANIFEST=$1
[ -f "$MANIFEST" ] || { echo "check-helm-claims: missing manifest: $MANIFEST" >&2; exit 2; }

awk '
function clean(value) {
	gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
	gsub(/^"|"$/, "", value)
	return value
}
function reset_doc() {
	kind = ""
	object_name = ""
	replicas = 1
	in_metadata = 0
	in_volume_claim_templates = 0
	volume_claim_templates = ""
}
function finish_doc(    names, count, i, ordinal, claim) {
	if (kind == "PersistentVolumeClaim" && object_name != "") {
		produced[object_name] = kind "/" object_name
	}
	if (kind == "StatefulSet" && object_name != "" && volume_claim_templates != "") {
		count = split(volume_claim_templates, names, " ")
		for (i = 1; i <= count; i++) {
			if (names[i] == "") continue
			for (ordinal = 0; ordinal < replicas; ordinal++) {
				claim = names[i] "-" object_name "-" ordinal
				produced[claim] = kind "/" object_name " volumeClaimTemplates[" names[i] "]"
			}
		}
	}
}
BEGIN { reset_doc() }
/^---[[:space:]]*$/ { finish_doc(); reset_doc(); next }
/^kind:[[:space:]]+/ {
	kind = clean(substr($0, index($0, ":") + 1))
	next
}
/^metadata:[[:space:]]*$/ { in_metadata = 1; next }
in_metadata && /^  name:[[:space:]]+/ {
	object_name = clean(substr($0, index($0, ":") + 1))
	in_metadata = 0
	next
}
/^  replicas:[[:space:]]+/ {
	replicas = clean(substr($0, index($0, ":") + 1)) + 0
	next
}
/^  volumeClaimTemplates:[[:space:]]*$/ {
	in_volume_claim_templates = 1
	next
}
in_volume_claim_templates && /^        name:[[:space:]]+/ {
	name = clean(substr($0, index($0, ":") + 1))
	volume_claim_templates = volume_claim_templates " " name
	next
}
/^[[:space:]]+claimName:[[:space:]]+/ {
	claim = clean(substr($0, index($0, ":") + 1))
	referenced[claim] = referenced[claim] " " kind "/" object_name
}
END {
	finish_doc()
	failures = 0
	reference_count = 0
	for (claim in referenced) {
		reference_count++
		if (!(claim in produced)) {
			print "check-helm-claims: dangling claimName " claim " referenced by" referenced[claim] > "/dev/stderr"
			failures++
		}
	}
	if (failures > 0) exit 1
	print "check-helm-claims: OK (" reference_count " unique claimName reference(s), all produced)"
}
' "$MANIFEST"
