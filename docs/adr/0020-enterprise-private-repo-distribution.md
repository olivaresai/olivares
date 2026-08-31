# ADR-0020: Enterprise edition distributed from a separate private repository

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** ADR-0010 (license is attestation-only), ADR-0011 (license boundary),
  `LICENSING.md`

## Context and problem statement

The licensing model is open core: the AGPL core/modules/web is the complete community
edition, the SDK/connectors are Apache-2.0, and the `enterprise/` line is additive
commercial code built only with `-tags enterprise` (ADR-0011). But until now the
`enterprise/` source **shipped in the public repository**. Because the activation gate is
the build tag (not the license, which is attestation-only by ADR-0010) and the license
never gates runtime, anyone could `git clone && go build -tags enterprise` and obtain the
full commercial binary for free. The commercial moat rested entirely on the legal license
(honor system over visible source).

## Decision drivers

- Make the build-tag gating **real**, not cosmetic: the commercial binary should not be
  compilable by anyone from public source.
- Keep the AGPL community binary **bit-for-bit unchanged** — no rug-pull, no feature
  removed from what already shipped open.
- Preserve the per-directory license boundary (ADR-0011) and the attestation-only license
  (ADR-0010), both unchanged.

## Considered options

- **Keep `enterprise/` in the public repo** (the GitLab `ee/`-in-one-repo model,
  source-available). Honest, but the moat is honor-system over visible, freely-compilable
  source.
- **Move `enterprise/` to a separate private repository** (the Grafana model: public OSS
  source + downloadable enterprise binary built from private source).

## Decision outcome

Chosen option: **move `enterprise/` to a separate private repository**. The public repo no
longer contains the `enterprise/` tree, the `//go:build enterprise` `cmd/olivares` wiring,
or any tooling that builds `-tags enterprise`. The commercial binary is built in the
private repo by overlaying the commercial tree and the wiring onto a pinned checkout of
the public tree (the public tree is a submodule; the wiring overlays into `cmd/olivares`'
`package main`, which `go.work` cannot do by module selection alone).

This changes only **distribution**, not licensing:

- **ADR-0011 (license boundary) stands unchanged:** `enterprise/` is still
  `LicenseRef-Olivares-Commercial`; the AGPL/Apache frontier is intact.
- **ADR-0010 (attestation-only license) stands unchanged:** the open binary still never
  reads a license to enable/disable anything. The license becomes *materially* meaningful
  only because the source that reads an attested claim (the add-on entitlements) is no longer
  public — not because the license started gating runtime.

### Consequences

- `git clone` of the public repo + `go build -tags enterprise` no longer produces the
  commercial binary: the source it needs is private. Gating is now real.
- The default AGPL binary is unchanged — it never linked `enterprise/`.
- The open≡enterprise schema-parity gate (it needs both editions) moves to the private
  repo, the only tree that can build both.
- Two repos and a small overlay-assembly step are the cost; the public release artifact is
  unaffected (it was already built `-tags release`, never `-tags enterprise`).

## Amendment (2026-07-28) — the seat entitlement named above is gone

The distribution decision stands: `enterprise/` lives in a private repository and the
build-tag gating is real. What no longer holds is the *example* used in Consequences —
"the source that reads an attested claim (the seat entitlement)". Decision B10
(2026-07-27) removed the user cap, so there is no seat entitlement and no build reads a
license to cap users; the attested claims that remain are read only to entitle the
additive add-ons. The original sentence is left as written because it records what was
true when this ADR was taken. Current decision: the commercial pricing canon (maintained privately)
(`self_hosted.users: unlimited`) and `LICENSING.md`
