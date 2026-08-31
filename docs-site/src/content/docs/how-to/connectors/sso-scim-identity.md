---
title: "SSO, SCIM & identity sources (firm attribution)"
description: >-
  Wire enterprise identity end to end: federated console login (OIDC/SAML
  through the federation seam), SCIM provisioning into the control plane, and
  the LDAP / Okta / Entra roster sources that upgrade access-map attribution
  from approximate to firm.
sidebar:
  order: 8
---

Identity is the hard dependency under the whole access map: native audit
attributes an access to a **credential**, and only an identity roster can
bind that credential to an **agent or person**. This page wires the three
identity surfaces: console **SSO login**, **SCIM provisioning** into the
control plane, and the **roster sources** (LDAP, Okta, Entra ID) that make
attribution `attributed` instead of `approximate`.

## 1. Console SSO (OIDC / SAML)

Federated login is served through the engine's **federation seam**. The
posture is honest by construction:

- The login flow endpoints exist in every build, and the engine keeps every
  secret-bearing flow value server-side — the CSRF state, the OIDC nonce,
  the PKCE verifier (only the S256 *challenge* goes to the provider).
  Authorization Code + **PKCE is always on**.
- The default build ships the `NoFederation` provider: both endpoints return
  `501 sso_not_configured` — the surface is advertised honestly with no IdP
  wired. The federation provider that completes the protocol is part of the
  enterprise build and is **configured by environment at boot**
  (`OLIVARES_SSO_PROTOCOL`, the `OLIVARES_OIDC_*` set for OIDC, the
  `OLIVARES_SAML_*` set for SAML).
- The redirect/ACS URI your IdP must carry is **exact**
  (`…/v1/auth/federation/callback` on your console origin — RFC 9700 exact
  matching, no prefix tricks).

The console's **Identity & NHI → SSO & SCIM** tab documents the live
configuration, checks your IdP's redirect URI against the exact expected
value, and shows the connection state — and where a panel's backend is a
declared contract not yet live, it says "backend pending" rather than
rendering fabricated data:

<img class="light:sl-hidden" src="/console/identity-dark.png" alt="The Identity & NHI view: SSO configuration with exact redirect-URI checking, the NHI roster, and key posture tabs." />
<img class="dark:sl-hidden" src="/console/identity-light.png" alt="The Identity & NHI view: SSO configuration with exact redirect-URI checking, the NHI roster, and key posture tabs." />

## 2. SCIM provisioning (inbound)

The control plane is a standard SCIM 2.0 (RFC 7644) service provider at:

```
/v1/scim/v2/Users
/v1/scim/v2/Groups
```

- **Auth:** a tenant-bound **admin/owner API token** on the SCIM
  integration — the same opaque-token model as the rest of the API, no
  separate SCIM secret type. The endpoint is always present (not
  feature-gated).
- **Users** provisions and deprovisions principals; deprovisioning by your
  IdP revokes access the moment HR says so.
- **Groups** carries identity-to-group reference data. Each group can map to
  a control-plane role via `mapped_role` — and that mapping is
  **operator-owned**: it is set on the control-plane side and audited
  (`scim.group.role.map`); an IdP push never silently escalates a role.
  Unknown members in a pushed group are skipped **and audited**, not
  invented.

## 3. Roster sources: LDAP, Okta, Entra ID

Roster sources feed module VI's identity inventory and — this is the point —
give module III the bindings that upgrade attribution:

```json
{
  "sources": [
    {
      "name": "corp-ldap",
      "kind": "ldap",
      "tenant": "<tenant-id>",
      "config": {
        "url": "ldaps://ldap.corp.example:636",
        "bind_dn": "cn=olivares-ro,ou=svc,dc=corp,dc=example",
        "bind_password": "<reference>",
        "base_dn": "dc=corp,dc=example"
      }
    },
    {
      "name": "okta",
      "kind": "idp",
      "tenant": "<tenant-id>",
      "config": { "provider": "okta", "base_url": "https://corp.okta.com", "api_token": "<reference>" }
    }
  ]
}
```

Key LDAP options (from the shipped descriptor): `user_filter` /
`group_filter`, `privileged_group_dns` (groups whose membership is itself a
privileged-access signal), `nhi_dn_suffix` (which subtree holds non-human
identities), `start_tls`, `page_size`. The `idp` kind takes
`provider: okta` (with `api_token`) or `provider: entra` (with `tenant_id` /
`client_id` / `client_secret`); `okta` and `entra` also work directly as the
`kind`.

### How this upgrades attribution — precisely

A roster source registers identities (by external id) and, where the
directory declares them, **permitted grants**. When an observed edge's origin
matches a **non-shared** roster identity, module III ties the access to that
identity and the edge's confidence is upgraded to `attributed`. Identities
that several workloads share stay honestly `approximate` — the roster cannot
un-share a credential; only issuing per-agent identity can
([the bridge to governance](/how-to/govern-and-approve/)).

Dedicated **agent-identity and workload-identity kinds** (the agent
federation sources — Entra Agent ID, AgentCore, SPIFFE and peers) are the
firm per-agent signal; group/directory rosters sharpen people and service
accounts.

## Honest limits

- **SSO completes in the enterprise build.** The seam, flow security and the
  501 posture are in every build; the protocol provider is not.
- **A roster cannot fix a shared credential.** It can only tell you,
  honestly, that the credential is shared.
- **SCIM is inbound provisioning** — the control plane does not push
  identities back to your IdP, and the Security-Event-Token receiver is an
  inbound surface, not an outbound webhook.

## Related

- [Connect a source](/how-to/connect-a-source/#the-hard-dependency-per-agent-identity)
  — why identity is the hard dependency.
- [Govern and approve](/how-to/govern-and-approve/) — roles, RBAC and what a
  `mapped_role` grants.
- [Connectors & coverage tiers](/reference/connectors/) — the full identity
  source list (Vault, Infisical, Keycloak, SPIFFE, the agent-identity
  federation kinds).
