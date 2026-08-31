# ADR-0008: Opaque server-side tokens, not JWT, for first-party auth

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Olivares AI (confirmed by adversarial review)
- **References:** API/authz/audit contract (§2, decision §13.2)

## Context and problem statement

The first-party authentication mechanism had to be chosen. The initial scope mentioned
"sessions/JWT". For a security product, the failure modes of bearer credentials —
revocation, secret-bearing claims, parsing-library risk — matter a great deal.

## Decision drivers

- Immediate revocation.
- No secrets carried inside the token.
- Minimal cryptographic parsing attack surface; secure by default.

## Considered options

- **Opaque server-side tokens** (a random secret, stored hashed, looked up server-side).
- **JWT** for first-party sessions.

## Decision outcome

Chosen option: **opaque server-side tokens** for first-party auth. Tokens are prefixed
by purpose (`olvs_` session, `olvk_` API key); the server stores only a public selector
and a SHA-256 of the secret, comparing in constant time. JWT is confined to the
SSO/federation seam, not first-party sessions.

### Consequences

- **Good:** tokens are revocable, carry no secrets, and need no crypto-parsing of
  attacker-supplied claims; secure by default.
- **Bad / trade-offs:** validation requires a server-side lookup (acceptable for a
  control plane).
- **Neutral:** federation still uses JWT where the protocol requires it.

## Why the alternatives were rejected

- **JWT for first-party sessions** — hard to revoke before expiry, tends to carry
  claims, and adds parsing/validation attack surface for no benefit here.
