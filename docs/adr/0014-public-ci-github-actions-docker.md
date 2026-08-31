# ADR-0014: Public release & CI on GitHub Actions + Docker

- **Status:** accepted
- **Date:** 2026-06-04
- **Deciders:** Fran Olivares (boot decision)
- **References:** roadmap boot decisions (Release/CI)

## Context and problem statement

Development happens in a private repository; the public, verifiable supply chain
needs a widely-trusted, transparent CI/release surface (for keyless signing identities,
SLSA provenance and public artifact distribution).

## Decision drivers

- A public, verifiable release identity (OIDC) and transparency log for signing.
- Standard, widely-trusted container distribution.
- Keep day-to-day development private until a release is curated and published.

## Considered options

- **GitHub Actions + Docker for all public artifacts; a private development repository.**
- **Self-hosted CI for public releases too.**

## Decision outcome

Chosen option: **GitHub Actions + Docker for everything public, always**; **development
happens in a private repository**. The release workflow's GitHub Actions OIDC
identity is what keyless signatures and SLSA provenance attest to, and images/charts
publish to a public OCI registry.

### Consequences

- **Good:** signatures and provenance chain to a public, verifiable identity; standard
  distribution; development stays private until intentionally published.
- **Bad / trade-offs:** the public repository is a curated export of the private
  development repository, not a live mirror.
- **Neutral:** publishing the public repository is a deliberate, gated action.

## Why the alternatives were rejected

- **Self-hosted CI for public releases** — a self-hosted signing identity is far harder
  for third parties to verify than a public GitHub Actions OIDC identity with a
  transparency log.
