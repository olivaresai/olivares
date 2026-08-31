# Contributor License Agreement (CLA)

Olivares AI is dual-licensed: the same code is offered to the public under
`AGPL-3.0-only` and, to those who need it, under a commercial license (see
[`LICENSING.md`](./LICENSING.md)). To be able to keep offering the commercial
exception, Olivares.AI must hold a clear, unbroken chain of copyright in all
contributed code. This CLA secures that chain.

> **Status:** Required for **external** contributors (anyone not employed by or
> contracting with Olivares.AI) **before their first contribution is merged**.
> It is **in addition to**, not a replacement for, the per-commit DCO sign-off
> (see [`DCO`](./DCO) and *Signing off* below).

This document is based on the **Harmony Agreements** individual and entity CLA
templates (HA-CLA-I and HA-CLA-E, v1.0), published at
**https://www.harmonyagreements.org** (choose *Agreements* → *Contributor
License Agreement*, individual or entity). Where this summary and the
corresponding signed Harmony agreement differ, the **signed agreement governs**.

---

## Summary of terms

By contributing, You agree to the following (full text in the Harmony PDFs):

1. **Definitions.** "You" is the individual or legal entity making the
   Contribution. "Contribution" is any original work of authorship submitted by
   You to the Olivares AI project. "We"/"Us" is Olivares.AI.

2. **Copyright license.** You grant Us a **perpetual, worldwide, non-exclusive,
   royalty-free, irrevocable** license to reproduce, prepare derivative works
   of, publicly display and perform, sublicense, and distribute Your
   Contribution and such derivative works.

3. **Outbound license / dual-licensing.** You agree that We may license Your
   Contribution to third parties under **any license terms We choose**,
   including `AGPL-3.0-only`, `Apache-2.0`, and a commercial/proprietary
   license. (This is the clause that makes the commercial exception possible.)
   You **retain** all right, title and interest in Your Contribution; this is a
   license to Us, not an assignment.

4. **Patent license.** You grant Us and recipients a perpetual, worldwide,
   non-exclusive, royalty-free, irrevocable patent license to make, use, sell,
   offer to sell, import and otherwise transfer Your Contribution, for patent
   claims You can license that are necessarily infringed by Your Contribution
   alone or in combination with the project.

5. **Your representations.** Each Contribution is Your original creation and You
   have the legal right to grant the above licenses. If Your employer has rights
   to work You create, You represent that You have permission to make the
   Contribution on the employer's behalf, or that the employer has waived such
   rights. (Entity signers: the signatory is authorized to bind the entity, and
   the agreement covers Contributions by the entity's designated employees.)

6. **No obligation / no warranty.** You are not expected to provide support, and
   except as stated above the Contribution is provided "AS IS" without
   warranties of any kind.

7. **Third-party material.** If You submit work that is not Your original
   creation, You must identify it, its source, and its license, and mark it
   conspicuously (e.g. "Submitted on behalf of a third party: [name/license]").

---

## Individual vs Entity

- **Individual CLA (HA-CLA-I):** sign if You are contributing on Your own behalf
  and own the copyright in Your Contributions.
- **Entity CLA (HA-CLA-E):** sign if a company owns the copyright in the
  Contributions (e.g. work-for-hire). An authorized signer binds the entity and
  lists the initial set of authorized employees (kept current thereafter).

---

## How to sign

We use a lightweight manual process (no automated CLA bot yet). To sign:

1. Download the matching Harmony agreement above (Individual or Entity). When
   completing it, select **outbound option five (“Any License”)** — the selection
   this project requires; a signed form with any other outbound selection is not
   accepted (project-specific pre-filled forms are a pending launch item).
2. Fill in and sign it.
3. Email the signed agreement to **enterprise@olivares.ai** with the subject
   `CLA — <your name / entity> — Olivares AI`.
4. We record Your acceptance against Your GitHub account; once recorded, Your
   pull requests can be merged.

A maintainer will confirm receipt on Your first pull request before merging.

## Signing off (DCO — every commit, everyone)

Independently of the CLA, **every** commit must be signed off under the
Developer Certificate of Origin:

```sh
git commit -s -m "feat: ..."
```

This appends a `Signed-off-by: Your Name <you@example.com>` trailer. The DCO
check on pull requests rejects any commit that lacks it (a required status
check, provisioned with the public repository). See [`DCO`](./DCO).
