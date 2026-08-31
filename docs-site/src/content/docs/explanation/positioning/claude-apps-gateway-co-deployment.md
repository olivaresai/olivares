---
title: Claude apps gateway + Olivares AI co-deployment
description: >-
  How to run Anthropic's self-hosted Claude apps gateway and let Olivares AI
  govern it as another enterprise surface: inventory, posture, audit ingest,
  OTLP correlation, and the phase-1 gateway-protocol endpoint.
sidebar:
  order: 9
---

## What the Claude apps gateway is

Anthropic's
[Claude apps gateway](https://code.claude.com/docs/en/claude-apps-gateway) is a
self-hosted service shipped inside the `claude` binary in v2.1.195+; run it with
`claude gateway --config gateway.yaml` and back it with PostgreSQL. It puts OIDC
sign-in in front of Amazon Bedrock, Claude Platform on AWS, Google Cloud Agent
Platform, Microsoft Foundry, or the Anthropic API, so developers use corporate
IdP sessions rather than local provider credentials. Its `gateway.yaml` maps IdP
groups to model allowlists and managed settings, and its spend-limits Admin API
can cap per-user, per-group, or organization spend. It fans out telemetry over
OTLP and emits single-line JSON audit events. Anthropic's June 29, 2026
[announcement](https://claude.com/blog/introducing-the-claude-apps-gateway)
frames it as first-party gateway infrastructure for Claude Code.

## Run it. Olivares governs it.

If you already run the Anthropic gateway, or plan to, keep it. The doctrine is
**and, not or**: Anthropic's gateway owns the Claude Code gateway session,
model-access and upstream-routing path; Olivares AI makes that deployment a
governed surface inside the broader control plane.

The `claude-apps-gateway` connector inventories `gateway.yaml`: issuer,
IdP-group -> model allowlists, spend-admin posture, OTLP destinations and
upstreams. It raises posture findings for the config states that matter to a
governance operator, and it ingests the gateway's JSON audit events so denials,
session mints and inference records enter the tamper-evident audit ledger. Point the
gateway's OTLP fan-out at the Olivares OTLP receiver and the `session.id` signal
can correlate with governed session runtime records; Olivares still retains
structural data, not prompt payloads.

## Documented limits

Anthropic's documented scope decisions, quoted from their docs as of
2026-07-03. These are scope statements, not defects; they define where a
co-deployment boundary belongs.

| Feature | Status | Notes |
|---|---|---|
| SAML, LDAP, and other non-OIDC auth | Not supported. | OIDC only. Front with an OIDC bridge if needed |
| Multi-tenant (multiple OIDC issuers) | Not supported. | One issuer per gateway. Run separate instances |
| Admin UI | Not available. | Configuration is the YAML file; redeploy to change it |
| Helm chart | Not available. | The gateway runs as a standard stateless Deployment |
| CI pipelines | There is no service-token flow for unattended pipelines |  |
| OTLP/gRPC | Not supported. | OTLP over HTTP only |
| Windows server | Not supported. | Deploy on Linux |
| Model catalog | Claude models only | the gateway translates Claude IDs per upstream |

## What Olivares adds alongside

Olivares does not remove those limits from the Anthropic gateway. It adds the
missing governance plane beside it.

| Anthropic gateway limit | Olivares capability alongside it |
|---|---|
| SAML, LDAP, and other non-OIDC auth | For the Olivares console and governance plane, [SSO/SCIM identity](/how-to/connectors/sso-scim-identity/) documents OIDC/SAML federation and [the IdP architecture](/explanation/architecture/where-it-fits-with-your-idp/) maps humans and agents onto SSO/SCIM and SPIFFE/WIF rosters. That does not retrofit SAML into the Anthropic gateway; keep the gateway OIDC-only or front it with an OIDC bridge. |
| Multi-tenant (multiple OIDC issuers) | Olivares' [multi-tenant control plane](/reference/modules/xx-multi-tenancy/) scopes entities, findings, sessions and the audit ledger by tenant, with PostgreSQL RLS for multi-tenant deployments. Run separate gateway instances per issuer and govern each as its own surface; do not treat one Anthropic gateway as multi-issuer. |
| Admin UI | The Olivares web console is a presentation layer over the same API described by [module XIX](/reference/modules/xix-api-manage-as-code/), and the identity docs show the live **Identity & NHI -> SSO & SCIM** UI. It is an admin console for the control plane, not a UI editor for Anthropic's `gateway.yaml`. |
| Helm chart | Olivares ships its own [Kubernetes Helm deployment](/tutorials/getting-started/kubernetes/) and a separate Kubernetes operator. This deploys the Olivares control plane; it does not claim to package Anthropic's gateway. |
| CI pipelines | Olivares automation can use opaque, revocable, tenant-bound API tokens through [manage-as-code](/how-to/manage-as-code/). For governed runtime and deployment credentials, the WIF/SPIFFE broker mints short-lived credentials; that is separate from the Anthropic gateway, whose own CI guidance remains provider-direct unless you intentionally use the Olivares proxy endpoint below. |
| OTLP/gRPC | The Olivares `claude` receiver accepts the normal OTLP receiver paths used by [OpenTelemetry GenAI](/how-to/connectors/otel-genai/), including HTTP and gRPC. The Anthropic gateway still sends OTLP/HTTP; other governed agents can use gRPC directly, and the resulting events can feed the cryptographic audit ledger and [compliance evidence packs](/reference/modules/xiii-compliance/). |
| Windows server | No Windows-server capability is claimed here. Run server-side components on Linux, containers or Kubernetes, and govern developer endpoints through telemetry, hooks and connector evidence. |
| Model catalog | [Module X](/reference/modules/x-models/) governs a cross-vendor model/provider estate: Claude, OpenAI, Gemini and local inference; the Bedrock connector adds Bedrock usage/cost and Guardrails observability. The Anthropic gateway remains Claude-only while Olivares governs the wider estate, including Codex posture through [subscription-auth governance](/explanation/positioning/governing-subscription-authed-agents/). |

## Protocol superset, phase 1

Anthropic publishes the gateway protocol and invites third-party implementations.
The Olivares inference proxy implements a phase-1 superset described by the
engineering contract for the apps-gateway protocol: OAuth
discovery, RFC 8628 device authorization, token polling through the sessions
credential seam after authenticated approval, single-document managed-settings
delivery with ETag, the read-only spend-limits list shape, and `GET /protocol`.

The descriptor documents the divergences itself: managed settings are
single-document mode, the version header is `x-olivares-version`, spend-limit
write/effective/audit routes return conformant `501` responses, and Olivares keeps
its richer budget-deny mapping while adding `x-should-retry: false`. Phase 1 does
not ship Anthropic's OIDC callback/browser `/device` page, per-group managed
settings merge rules, spend-limit write paths, `count_tokens`, or
`x-claude-code-session-id` header attribution.

## Choosing a topology

- **Gateway alone.** Enough for a single-issuer OIDC organization that is
  Claude-only, comfortable managing YAML and redeploys, and satisfied with the
  gateway's own spend limits, OTLP fan-out and JSON audit output.
- **Gateway + Olivares.** The recommended co-deployment when Claude Code goes
  into a regulated estate: keep the Anthropic gateway, add the
  `claude-apps-gateway` connector, point OTLP at Olivares, and retain the
  resulting posture, runtime and evidence picture in the control plane.
- **Olivares proxy as the gateway-protocol endpoint.** Use this when you
  intentionally want the Olivares inference proxy to serve the phase-1
  gateway-protocol surface. It is useful when the shipped subset is enough; it is
  not a full replacement for the Anthropic gateway's browser OIDC flow or
  write-path spend administration.
