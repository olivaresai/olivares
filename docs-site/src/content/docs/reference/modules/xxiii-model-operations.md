---
title: "Module XXIII — model operations"
description: >-
  The governed registry of models you OWN — hosted, fine-tuned or imported — with
  signed-model admission and local inference deployments. It governs the supply chain of
  your own models; it does not train, serve or benchmark them.
---

Module XXIII is the **own-model** side of the model stack. Where module X (models &
providers) governs the *reference catalog* and *routing* of the models you consume, this
module governs the models you **own and operate**: a versioned registry, the signed-model
**admission** gate that decides which versions may be deployed, and the local **inference
deployments** that serve them. It tracks and governs; it never trains a model, runs a
fine-tune job, or executes inference itself.

The console surface for this module is **Model Operations** (Intelligence group), with tabs
for owned models, datasets, fine-tune jobs, admission, deployments and the AIBOM seal
ledger. Supplier GPAI posture (per-provider) lives in **Models & providers**, and the agent
supply chain is its own view — both are per-provider / per-estate concerns, not per-owned
model.

## What it is

Three cooperating surfaces, all deny-closed and audited:

- **Owned models & versions.** A registry of the models you own (`hosted`, `fine_tuned`,
  `imported`), each with immutable **versions** that name an artifact. A version is
  recorded, then its signed artifact is admitted — the version row itself never changes.
- **Admission.** A per-tenant **trust policy** and the recorded **verdict** history. The
  policy names the trust anchors — CA roots and/or public keys, plus optional Sigstore
  identities and issuers — and the signing **method is derived** from what you configure
  (`sigstore-keyless`, `certificate-pki` or `bare-key`); an empty policy admits nothing.
  Admitting a version verifies a signature **bundle** against the policy and records the
  verdict. A verdict that fails to verify is recorded honestly, not hidden.
- **Deployments.** Local inference deployments (vLLM, Ollama, llama.cpp, other). When the
  tenant **enforces** signed models, creating or updating a deployment that references a
  version re-checks admission: if the version has no verified verdict, or the trust root
  that admitted it is no longer in the policy, the deployment is refused.

## Lineage & evidence

- **Datasets.** Minimal-data lineage components — a name, an optional content reference and
  a hash, a classification and a governance label — **never the dataset contents**. A
  dataset is tenant-wide; its optional model reference is a lineage pointer, validated
  deny-closed. `verified` is an **operator claim** of provenance, never a cryptographic
  result, and the console labels it as such.
- **Fine-tune jobs.** Records of externally run fine-tuning work and the model **version**
  each produced. The plane never starts, cancels or executes training and stores no weights
  or dataset contents — these are inventory records, not a training launcher.
- **AIBOM & model card.** From an owned model you can **generate** a live CycloneDX AIBOM
  (or an SPDX 3.0.1 serialization) and a model card (JSON or Markdown), all read-only. A
  generated document is not evidence until you **seal** it: sealing anchors a canonical
  content-hash commitment to the audit ledger (always CycloneDX — SPDX can never be sealed).
  The ledger stores only the hash, so the seal receipt is the one chance to save the sealed
  document. The cross-model **AIBOM seals** tab is the durable, append-only ledger of those
  commitments.

## What it enforces

When `require_signed` is on, a deployment referencing a model version is admitted **only
if** that version has a verified admission verdict whose anchoring trust root is still
configured. Rotating a root out of the policy retroactively denies future deployment
creates/updates of versions that only that root admitted — they must be **re-admitted**
under the current anchors first. This is the same anchor pin the engine records on every
verdict (`signer_roots`), surfaced so an operator can see exactly which root vouched for a
version.

## What it is not

- It does **not** run training or fine-tune jobs — it records their status for lineage.
- It does **not** serve inference — it governs the deployment records that do.
- It does **not** decide "currently deployable" from a stored verdict — only the engine's
  re-check at deploy time is authoritative, so the console never labels a version trusted
  or deployable from history alone.

## Agent supply chain

The separate **Agent Artifacts** console view registers four tenant-estate artifact
classes: Agent Skills, `.mcpb` extensions, MCP App `ui://` templates, and `AGENTS.md`
instruction files. The registry stores identity, provenance, content fingerprints and
posture metadata — never skill bodies, manifests or instruction text. A posture grade is
a **recorded scan result** from a connector scanner or operator, not a scan run by the
console; an absent grade is shown neutrally as not scanned.

Its CycloneDX 1.6 agent-supply-chain BOM is distinct from a per-model lineage AIBOM.
Seals append a canonical content-hash commitment to the separate
`models.agent_aibom` ledger, while the returned receipt remains the only copy of the
sealed document. Coverage is registered-only: an artifact never registered is not
represented.
