<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Argo CD interop

Two artifacts make the Olivares AI control plane a first-class citizen of an Argo CD
GitOps estate. Both are **read-first / declarative** — they observe or describe
health; they never trigger a sync (acting on a deployment is module VII,
governed by the human-in-the-loop gate).

## 1. Observation — the `argocd` source connector

`connectors/argocd` parses **exported** Argo CD `Application` manifests
(`argoproj.io/v1alpha1`) and emits the GitOps reconciliation posture
(`sync` / `health` / last-operation) as findings onto the control plane's event bus
through the ingestion seam. Wire it as an observation source:

```jsonc
// OLIVARES_SOURCES_CONFIG
{
  "sources": [
    {
      "name": "argo-prod",
      "kind": "argocd",
      "tenant": "<tenant-uuid>",
      "poll_seconds": 300,
      "config": { "path": "/var/lib/olivares/exports/argocd-applications.yaml" }
    }
  ]
}
```

Export the manifests the connector reads with:

```sh
kubectl get applications -A -o yaml > /var/lib/olivares/exports/argocd-applications.yaml
```

The connector reads **only** `status.{sync,health,operationState}`; it never reads
`spec` (Helm values / plugin env can carry secrets) and never opens an API
connection. `OutOfSync` is surfaced as drift, `Degraded`/`Failed` as higher-severity
findings.

## 2. Health — the `ControlPlane` Lua health check

`resource_customizations/ops.olivares.ai/ControlPlane/health.lua` lets **Argo CD**
report the health of an Olivares control plane it manages (the
`ops.olivares.ai/v1alpha1` `ControlPlane` CRD reconciled by the Operator), instead
of leaving a custom resource stuck on `Progressing`. It maps the CRD's `Available`,
`Progressing`, and `Degraded` conditions and its `Pending`, `Progressing`, `Ready`,
and `Invalid` phases.

Install either way (both verified against
<https://argo-cd.readthedocs.io/en/latest/operator-manual/health/>):

- **Directory form** — copy the `resource_customizations/` tree into Argo CD's
  configured customizations directory.
- **ConfigMap form** — inline the script under
  `resource.customizations.health.ops.olivares.ai_ControlPlane` in `argocd-cm`.

Validate and unit-test it:

```sh
argocd admin settings resource-overrides health <control-plane.yaml>
# unit tests: copy health.lua + health_test.yaml + testdata/ into an Argo CD checkout
# and run `go test ./util/lua/`
```
