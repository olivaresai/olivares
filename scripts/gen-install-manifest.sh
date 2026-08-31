#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Generate the flat, Helm-free install manifest (deploy/manifests/install.yaml) by
# rendering the signed Helm chart (deploy/helm/olivares) with its safe
# single-node defaults. This is the fourth install path (C5): an operator with
# only `kubectl` — the common air-gapped / locked-down case — can
# `kubectl apply -n olivares-system -f deploy/manifests/install.yaml` without Helm or
# the Kustomize inflation generator on PATH. The file is GENERATED, never hand-edited;
# `task manifests:gen` runs this, and CI fails if the committed file drifts from the
# chart. The render is deterministic (no randAlphaNum/uuid/genSelfSigned/now in the
# templates), so the freshness diff is stable.
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
CHART="$ROOT/deploy/helm/olivares"
OUT_DIR="$ROOT/deploy/manifests"
OUT="$OUT_DIR/install.yaml"

# --kube-version is REQUIRED: Chart.yaml pins kubeVersion ">=1.25.0-0" and helm's
# default capability is older, so a bare `helm template` errors. Pin a current LTS.
KUBE_VERSION="${KUBE_VERSION:-1.29.0}"
NAMESPACE="${OLIVARES_NAMESPACE:-olivares-system}"

# ⛔ TERCERA RESPUESTA: sin `helm` no se generó nada. Salir 1 se lee como «el manifiesto
#    está mal»; lo cierto es que no se pudo producir. El manifiesto commiteado sigue siendo el que
#    era y esto no dice nada sobre él.
command -v helm >/dev/null 2>&1 || { echo "gen-install-manifest: ⛔ NO HE PODIDO MIRAR — helm no está en este host, así que no se generó ningún manifiesto. Esto no dice nada sobre el que hay commiteado." >&2; exit 2; }

mkdir -p "$OUT_DIR"

{
	echo "# SPDX-FileCopyrightText: 2026 Olivares.AI"
	echo "# SPDX-License-Identifier: AGPL-3.0-only"
	echo "#"
	echo "# GENERATED — DO NOT EDIT. Flat install manifest for the Olivares AI control"
	echo "# plane, rendered from deploy/helm/olivares with safe single-node"
	echo "# defaults (sqlite, 1 replica, collectors/PDB/NetworkPolicy/ServiceMonitor off)."
	echo "# Regenerate with 'task manifests:gen'; CI fails if this drifts from the chart."
	echo "#"
	echo "#   kubectl create namespace $NAMESPACE   # once"
	echo "#   kubectl apply -n $NAMESPACE -f deploy/manifests/install.yaml"
	echo "#"
	echo "# Rolling out for real: pin the image to a digest (deploy/gitops/README.md)."
	helm template olivares "$CHART" --kube-version "$KUBE_VERSION" --namespace "$NAMESPACE"
} >"$OUT"

echo "gen-install-manifest: wrote $OUT (helm $(helm version --short 2>/dev/null), --kube-version $KUBE_VERSION)"
