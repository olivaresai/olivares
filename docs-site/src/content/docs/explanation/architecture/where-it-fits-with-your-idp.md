---
title: Where Olivares AI fits with your IdP
description: >-
  Olivares AI is not an identity provider. It federates agent identity from the
  registries you already run — Entra Agent ID, AWS AgentCore Identity, Google
  Agent Identity — read-only, and uses it to attribute the access map. How it
  composes with your IdP, SSO/SCIM, SPIFFE/WIF, and the ID-JAG / XAA standards.
sidebar:
  order: 3
---

A frequent first question from a security architect is: *"Is this another
identity system I have to run?"* No. **Olivares AI is not an identity provider and
does not own identities.** It **consumes** the identities you already issue — for
humans, from your IdP via SSO/SCIM; for agents, from the agent-identity registries
the hyperscalers have made generally available — and uses them to attribute *who
or what* is behind each edge in the [access map](/explanation/). This note
explains exactly where the seam sits.

## The layering

```
   Your IdP (Entra ID / Okta / Google)         ← humans: SSO + SCIM (unchanged)
   Agent-identity registries                    ← agents: Entra Agent ID,
     (Entra Agent ID / AgentCore / Google)        AgentCore Identity, Google Agent Identity
            │  read-only roster sync
            ▼
   Olivares AI  ── SPIFFE/WIF roster ──► R/RW access map (attributed edges)
            │                            └─ Permitted-vs-Observed drift
            └─ deny-closed gates (approvals, hooks PEP, MCP gating) — never an IdP
```

- **Humans** authenticate through **your** IdP. Olivares AI integrates with
  standard **SSO and SCIM** for operator accounts and group-to-role mapping; it
  does not store credentials or become a second directory.
  → [SSO & SCIM identity](/how-to/connectors/sso-scim-identity/)
- **Agents** get their identity from the registries you already adopted. Olivares
  AI federates those rosters **read-only** onto an internal **SPIFFE/WIF** roster,
  so every observed access can be tied to a governed, named identity rather than an
  anonymous process.

## What the agent-identity federation actually does

The control plane ships read-only roster connectors for the GA agent-identity
registries, each verified against its primary source and **deny-closed** (no
credential → empty roster, never a phantom error):

- **Microsoft Entra Agent ID** — imports agent identities, blueprints, and
  owner/sponsor relationships via Microsoft Graph; surfaces registry-asserted
  orphans. Blueprints carrying long-lived password credentials raise a
  **long-lived-credential drift** finding.
- **AWS AgentCore Identity** — imports the agent roster; agents with a service
  identity map to a service-account identity kind.
- **Google Agent Identity** — imports reasoning-engine identities; the reference
  is a full **SPIFFE ID**, so it converges with the SPIFFE roster by external id.

These mappings feed the [access map's attribution](/reference/glossary/#attribution-confidence)
axis (`firm` / `approximate` / `unknown`) — they do not reimplement it. Federation
is strictly read-only: Olivares AI **never** mutates a remote registry. Ownership
and orphan signals are forwarded to the non-human-identity lifecycle so a
registry-asserted orphan shows up through the existing governance machinery.

:::note[Experimental and design-toward, labelled as such]
Cross-ecosystem descriptors (**OASF**) and **AGNTCY Agent Badges** are treated as
**experimental** until they meet verifiable-credential conformance. Rosters that
are still in preview (e.g. Google's Gemini Enterprise Agent Platform) are wired as
**seams**, not claimed as live. We mark what is GA, what is preview, and what is
design-toward — we do not blur them.
:::

## ID-JAG, XAA and SPIFFE-based client auth

The enterprise standards for *delegated, attributable* agent access are
converging, and the control plane is built to ride them rather than invent its
own:

- **ID-JAG** (Identity Assertion JWT Authorization Grant) and **XAA** (Cross-App
  Access) are the emerging pattern for an IdP to issue **scoped, attributable**
  authorization for an agent acting across applications — the enterprise-managed
  authorization extension in the MCP authorization work. As these land, the
  attributable token becomes another high-fidelity signal the access map can bind
  to a governed identity.
- **SPIFFE-based OAuth client authentication**
  (`draft-ietf-oauth-spiffe-client-auth`) lets the plane's own OAuth flows
  authenticate with a **SVID** the moment an authorization server publishes
  support — over the existing deny-by-default mTLS. This is **design-toward**, with
  no conformance claim, until the draft and server support stabilise.
- **Short-lived by default.** Long-lived static credentials discovered in the
  estate are flagged as a drift class, in line with **Five Eyes** guidance (2026)
  that agent credentials should be short-lived.

## What this means for you

- You keep your IdP, your SSO, your SCIM, and whichever agent-identity registry you
  standardised on. Nothing migrates.
- Olivares AI becomes the place where **all** of those identities meet the
  **observed behaviour** of your estate — the only layer that can say "this agent,
  from this registry, owned by this human, is using access that policy never
  granted."
- Because federation is read-only and self-hosted, that correlation needs no
  identity data to cross your boundary: there is no mandatory telemetry and no
  control-plane egress by default, and what crosses your perimeter is what **you**
  configure to cross it — calls to your model APIs, the SIEM/webhook outputs you
  wire, an external embedding provider if you provision one.

## Related

- [Agent / Identity / NHI](/reference/glossary/#identity--nhi) — the glossary
  definitions.
- [vs AI control towers](/explanation/positioning/vs-control-towers/) — the
  bidirectional integration with ecosystem admin planes.
