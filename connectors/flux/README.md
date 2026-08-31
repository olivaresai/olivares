# Flux GitOps posture — the `flux` source connector

`connectors/flux` makes the Olivares AI control plane a first-class observer of a
**Flux** GitOps estate. It is **read-first / declarative**: it parses *exported* Flux
CRD manifests and reports the reconciliation posture as findings; it never calls the
Flux controllers, never triggers a `flux reconcile`, and reads no payloads (acting on
a deployment is module VII, governed by the human-in-the-loop gate). It is the
sibling of the `argocd` connector for the other major GitOps engine; Flux
graduated CNCF on **2022-11-30**.

## What it reads

Flux spreads its API across **three** controller groups, so the connector accepts all
three kinds rather than hardcoding one group/version:

| Kind | API group (current version) | Posture read |
| --- | --- | --- |
| `GitRepository` | `source.toolkit.fluxcd.io/v1` | `status.conditions[Ready]`, `status.observedGeneration`, `status.artifact.revision` |
| `Kustomization` | `kustomize.toolkit.fluxcd.io/v1` | `status.conditions[Ready]`, `status.lastAppliedRevision` vs `status.lastAttemptedRevision`, `status.observedGeneration` |
| `HelmRelease` | `helm.toolkit.fluxcd.io/v2` | `status.conditions[Ready]`, `status.history[0].chartVersion` vs `status.lastAttemptedRevision`, `status.observedGeneration` |

> **Caveat (HelmRelease v2):** the *applied* revision lives in
> `status.history[0].chartVersion`, **not** in a `status.lastAppliedRevision` field
> (that field is a Kustomization concept). The connector reads `history[0]` for the
> HelmRelease drift comparison.

It reads `metadata.{name,namespace,generation}` and **only** the `status` fields above.
It never reads `spec` (a `HelmRelease` `spec.values` can embed secrets; a
`GitRepository` `spec.url`/`secretRef` name an endpoint and a secret — payload-adjacent),
and it never reads a condition `message` (which can carry a registry URL, a chart path,
or a YAML build error). Revision strings are compared **in memory** to decide drift and
then discarded — no commit SHA, chart version, or artifact URL is ever placed into a
finding.

## Classification

Taken **verbatim** from the object's own
`status.conditions[?(@.type=="Ready")].status` — never guessed:

| Ready.status | Severity | Meaning |
| --- | --- | --- |
| `"True"` | **Info** | reconciled |
| `"False"` | **High** | reconciliation failing — the condition **reason** token (e.g. `ArtifactFailed`) is put in the Title, never the message |
| absent / empty | **Medium (Unknown)** | no reconciliation status reported yet — classified honestly as Unknown, never silently Healthy |

A reconciled (`Ready==True`) object that is **drifted** also gets a **Medium** drift
finding — applied != attempted revision, or `observedGeneration` lags
`metadata.generation`.

## Wiring

```jsonc
// OLIVARES_SOURCES_CONFIG
{
  "sources": [
    {
      "name": "flux-prod",
      "kind": "flux",
      "tenant": "<tenant-uuid>",
      "poll_seconds": 300,
      "config": { "path": "/var/lib/olivares/exports/flux.yaml" }
    }
  ]
}
```

Export the manifests the connector reads with:

```sh
kubectl get gitrepositories,kustomizations,helmreleases -A -o yaml \
  > /var/lib/olivares/exports/flux.yaml
```

`path` may be a single file or a directory of `*.yaml` / `*.yml` / `*.json` manifests
(multi-document YAML supported). Non-Flux documents in the same file/directory are
skipped.

## OpenGitOps 1.0 → Flux evidence mapping

[OpenGitOps](https://opengitops.dev) (CNCF-hosted) defines what GitOps **is**, in four
principles (v1.0.0). This connector reports the Flux evidence for each, so an operator
can show their estate conforms:

| OpenGitOps 1.0 principle (verbatim) | Flux evidence this connector observes |
| --- | --- |
| **1. Declarative** — "A system managed by GitOps must have its desired state expressed declaratively." | The presence of `GitRepository` / `Kustomization` / `HelmRelease` declarative objects (the kinds this connector parses). |
| **2. Versioned and Immutable** — "Desired state is stored in a way that enforces immutability, versioning and retains a complete version history." | `GitRepository` `status.artifact.revision` (the commit SHA the source controller pinned). Read to decide drift; never emitted. |
| **3. Pulled Automatically** — "Software agents automatically pull the desired state declarations from the source." | Controller-populated `status` on the declarative Flux objects; this connector does not infer an external push. |
| **4. Continuously Reconciled** — "Software agents continuously observe actual system state and attempt to apply the desired state." | `status.conditions[Ready]` + `status.observedGeneration` vs `metadata.generation` + applied-vs-attempted revision drift. |

## License boundary

Apache-2.0. The connector imports only the SDK (`sdk`, `sdk/model`) and
connector-internal helpers (`connectors/internal/redact`) plus `gopkg.in/yaml.v3` — it
never imports the AGPL engine (`/core`), enforced by `scripts/check-boundary.sh`.
