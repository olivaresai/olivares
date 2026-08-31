<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# NIH / NSF research-data policy pack

**Purpose.** Operationalize the U.S. federal research-funding mandates on generative AI with the
control plane's *existing* enforcement — so a university or research institution can show a funder
that controlled-access data cannot reach a public or non-retention-guaranteeing AI surface.

> **Honesty.** This is a technical operator guide, not legal advice and not a certification. It maps
> each mandate to a control you configure and to a test that proves it. Labeling which datasets are
> *controlled-access* is your institutional decision; the plane enforces the deny once the label is
> applied.

---

## 1. NIH NOT-OD-25-081 — controlled-access data must not enter public generative AI

**The mandate.** NIH prohibits submitting NIH controlled-access data (e.g. dbGaP) into generative-AI
tools, and requires that such data not be retained by, or used to train, external models after the
project. Primary source:
<https://grants.nih.gov/grants/guide/notice-files/NOT-OD-25-081.html>.

**How the plane enforces it (two independent, deny-closed gates):**

1. **Classification clearance ladder** — `modules/knowledge/vector.go`
   (`classificationAllowed`). Classify the controlled-access dataset's knowledge base at a
   restricted clearance (`confidential`, `secret`, or the `restricted` alias — **never** `public`).
   A chunk is admitted to retrieval only when its classification rank ≤ the requesting identity's
   clearance rank, and an unknown label on either side **fails closed**. A public/low-clearance
   agent surface therefore cannot retrieve a restricted dataset.

2. **DLP egress gate** — `modules/knowledge/dlp.go` (`dlpPolicy.decide`, `unscannedDenied`). Apply
   the shipped preset [`presets/nih-controlled-access.dlp.json`](presets/nih-controlled-access.dlp.json)
   via `PUT /v1/m/knowledge/dlp/rules`. It denies the `controlled-access` sensitivity class and
   `unscanned` content from egress — both the retrieval response text and the ingest-time embed call
   to an external (`model_backed`) embedder. Because the gate is deny-closed, adding this preset also
   denies any other *unlisted* labeled class from egress; add explicit `{"class":"…","action":"allow"}`
   rules for the classes your institution permits.

**Non-retention evidence.** Set the KB embed policy to a local/no-egress embedder so ingest never
transmits content out; the append-only `knowledge_dlp_event` rows (class + counts, never content)
plus the residency↔egress gate (`modules/knowledge/kb.go`, `residencyEgressForbidden`) evidence that
controlled-access content did not leave the perimeter.

**Proof.** `modules/knowledge/s336_test.go` (`TestNIHControlledAccessPreset`) loads the shipped
preset and asserts the `controlled-access` class is denied egress and that a restricted-classified
chunk is not visible to a public surface — so this guide and the enforced policy cannot drift.

---

## 2. NIH NOT-OD-25-132 — notice / acknowledgement policy

NIH guidance on the responsible use of AI in applications. Primary source:
<https://grants.nih.gov/grants/guide/notice-files/NOT-OD-25-132.html>. Operationalize as an
acknowledgement policy: require investigators to attest, before an agent may operate over
grant-related sources, that AI use complies with the funder's terms. Carry the attestation as an
approval/HITL gate on the relevant workspace and record it in the audit ledger.

## 3. NSF — generative AI in the merit-review and proposal process

NSF's notice to the research community restricting generative-AI use in the merit-review process and
directing disclosure in proposal preparation (December 2023). Confirm the current canonical notice at
<https://www.nsf.gov> (Proposal & Award Policies, generative-AI guidance). Operationalize the same
way as NOT-OD-25-132: an acknowledgement/disclosure gate plus audit evidence. NSF proposal content is
typically classified `confidential`; the same clearance ladder keeps it off public surfaces.

---

## Apply the pack

1. Classify each controlled-access / proposal dataset's KB at `confidential` or `secret`
   (`PUT /v1/m/knowledge/kbs/{id}` with `"classification"`).
2. Apply the DLP preset: `PUT /v1/m/knowledge/dlp/rules` with each rule from
   [`presets/nih-controlled-access.dlp.json`](presets/nih-controlled-access.dlp.json).
3. Wire a local/no-egress embedder for those KBs (embed policy).
4. Attach an acknowledgement/approval gate for NOT-OD-25-132 / NSF disclosure.
5. Export `knowledge_dlp_event` and the audit ledger as your non-retention evidence.
