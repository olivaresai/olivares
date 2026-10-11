<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Editions

Olivares AI is one product in three editions. Community is the complete product for one person or one team, free
under AGPL-3.0.
Business adds what larger and regulated organizations need, as separate commercial code. Enterprise adds
terms agreed in a contract and the few capabilities that depend on them. Prices and license scope are on the
[offer page](https://olivares.ai/pricing).

## How a capability is placed

- Community has everything one person or one team needs to run AI on its own: install, providers, running
  the official tools, governance and enforcement, the audit ledger, records, identity with one identity
  provider, and operations. The safety boundary is never paid: built-in roles, each session bound to its
  provider, native deny-overlay rules, the kill switch and its two-person re-enable, and the deny-closed
  checks.
- Business includes everything in Community and adds what businesses and professional, regulated or large
  environments need: the session cockpit, directories and several identity providers, departments and
  delegated administration, compliance and reporting, regulated records, deeper runtime inspection, key
  management, fleet deployment, integrations with enterprise systems, the commercial license and support. One
  subscription includes the Business base line and four capability families (Regulated Operations, AI Runtime
  Security, Compliance Packs, Identity & Scale); an administrator turns each family on or off.
- Enterprise includes everything in Business, plus terms agreed in a contract and the capabilities that
  depend on them.

From 0.1, no edition:

- moves a Community feature to a paid edition, or caps or time-limits it;
- counts users: every edition has unlimited users;
- puts the implementation of a paid capability in the public repository, as source, a disabled flag or a hidden
  page; the public tree holds only seams that answer 501 or do nothing. The one exception is what you stored
  before 1.0: a Community binary keeps enforcing stored custom Cedar permits and forbids, custom roles, permission groups and scoped grants,
  the review and quorum restrictions of stored approval policies, budgets and group-to-role mappings,
  and the department tree, department membership, group nesting, and agent groups with their members that
  forbid rules match. Approval policies may still require review or raise tier/quorum in Community; lowering is inert there.
  It lists custom authorization data read-only and can delete roles, permission groups and grants or disable the
  authored Cedar surface (`DELETE /v1/m/governance/pdp/active?engine=cedar`) without deleting revision history.
  Business makes the same stored data editable, with no migration. It can remove the stored budgets and other restricted policies, and clear a stored group-to-role mapping
  (`PUT /v1/groups/{id}/role` with an empty role), which only narrows access; it cannot create, widen or
  otherwise change any of these. The inbound SCIM server keeps applying what your identity provider pushes:
  deleting a group it provisioned removes the group, its members and its role mapping, which cuts the groups
  nested under it off from their ancestors; SCIM never adds a nesting edge or a role mapping;
- makes the Community binary read a license to turn a capability on or off;
- silently drops a protection that was in force: apart from the stored items above, a Community binary that finds
  a Business protection that was in force on that deployment (a CMEK key source, a login requirement or network
  allow-list, durable delivery, a runtime check or records control, a CAEP receiver or transmitter, required
  consent for privileged-action recording) refuses to start and names the edition.

The behaviors that do not follow these rules yet are listed under "Where the product differs from this page
today".

Commercial rights run to the end of the paid term. After it, Community features, your data and your export
keep working. A CMEK install needs the Business build to start; with no license or an expired one, that
build still opens the key and configuration envelopes for `olivares dr backup`, and `olivares keys unseal`
still prints a sealed configuration file.

## Placement

| Area | Capability | Edition | Why |
|---|---|---|---|
| Install | Static binary with embedded console, deb and rpm packages, container; SQLite or PostgreSQL; backup and restore | Community | Every user starts here. |
| Install | Helm chart, Kubernetes operator, Terraform provider, appliance images, FIPS and STIG images | Business: base line | Fleet and regulated deployment. |
| Install | Air-gapped install and offline update bundles | Enterprise | Disconnected sites are set up per contract. Verifying a bundle without installing it (`olivares upgrade --bundle --check`) stays in Community. |
| Providers | Provider tools sign in with their own login, or with your own provider keys | Community | Every session starts with a provider. |
| Sessions | Launch, list, show the status of, stop and remove official CLIs (Claude Code, Codex, Grok Build) from the CLI and the console: `olivares session start`, `ls`, `show`, `stop`, `rm`; `olivares agent session create`, `ls`, `get`, `stop`, `cleanup`, `rm` | Community | Running the tools is the first job of the product. |
| Sessions | Follow a session and stop it doing harm from the CLI: `olivares session follow`, `events` and `interrupt`; `olivares agent session events` and `interrupt` | Community | One person watching their own session, and interrupting it, is part of running the tools safely. |
| Sessions | Prepare local Git work in the session console: status, stage and unstage files, commit with the signed-in user's profile, and create or switch local branches inside the session's OS boundary | Community | Preparing one's own session changes is part of running the tools. Existing governed publication prepares pushes and pull requests. |
| Sessions | Preview in the session console the app a session serves on a local port: the engine proxies it under a short-lived link, sandboxed away from the console, with reload and open in a new tab | Community | Seeing what one's own session builds is part of running the tools. |
| Sessions | Session cockpit: the workspace sidebar, live terminals in the console (attach, send input, interrupt, resume), several panes at once, the agent dashboard, the agent listener over mutual TLS, and steering from the CLI (`olivares session send`, `resume`, `peers`; `olivares agent session attach`, `input`, `resume`) | Business: Identity & Scale | The cockpit is a working environment for steering live sessions; launching, watching and stopping them, and interrupting them from the CLI, stay in Community. |
| Visibility | Inventory of agents, sessions, models, MCP servers and tools; live timelines; health | Community | Governance starts with seeing what runs. |
| Visibility | Access map, identity inventory | Business: Identity & Scale | Analysis of who reaches what across an organization. |
| Work | Work items | Community | Part of running AI at any size. |
| Work | Orchestration, A2A delegation to authorized peers | Business: Identity & Scale | Coordinating agents across systems. |
| Knowledge | Content sources, governed retrieval, model catalog, inference proxy | Community | Connecting your own data is the core job. |
| Authorization | Built-in roles, native deny overlay, kill switch | Community | Who reaches what is the safety boundary, and it is never paid. Stored custom permits, forbids, roles and scoped grants keep their existing effect. Read and revoke stay available; create/update returns 501. |
| Authorization | Custom Cedar policies (permits and forbids), custom roles, permission groups and scoped grants | Business: base line | Policy as code for organizations with their own rules. |
| Authorization | Approval engine with its fixed floors (re-enabling the kill switch and every critical action need two distinct people) | Community | The floors are fixed in code and guard Community's own safety boundary. |
| Authorization | Tier-lowering approval policies and break-glass | Business: base line | Community keeps the approval engine, the two-human CRITICAL floor, kill-switch dual control, and authoring/enforcement of review, tier-raising and quorum-raising policies. Stored lowering policies remain readable/exportable but cannot lower Community defaults or floors; stored emergency grants are inert there. Business retains lowering and the audited, recorded emergency lifecycle. |
| Enforcement | Four deny-closed enforcement points: Claude Code hook, inline inference proxy, MCP `tools/call` gate, A2A delegation gate | Community | The deny-closed checks are part of the safety boundary. Without the Business A2A delegation, its gate refuses every delegation. |
| Data protection | Inline guardrails (PII, prompt injection, jailbreak), DLP on egress, hybrid post-quantum TLS (the Go default key exchange on the product's TLS listeners) | Community | Data that reaches a provider is part of the safety boundary. |
| Data protection | Signing keys you provide (`OLIVARES_AUDIT_SIGNING_KEY[_FILE]`, and the catalog and policy keys), the off-box checkpoint signer (`OLIVARES_LEDGER_SIGNER`), the custody assertions `OLIVARES_KEY_CUSTODY=byok` (the audit key must be one you provide, never a minted one) and `OLIVARES_LEDGER_CUSTODY=hyok` (checkpoints must be signed off-box), `olivares keys status` | Community | An HA pair needs one shared ledger key, or the ledger forks at failover; `olivares audit key-transition` needs the off-box signer. |
| Data protection | CMEK: signing keys and operator configuration files sealed in envelopes that only your key management service opens (`OLIVARES_KEY_WRAP`; `olivares keys wrap`, `rotate`, `rewrap`, `seal`, `unseal`) | Business: base line | Key management for organizations that hold their keys in a key service. A CMEK source is any of `OLIVARES_AUDIT_SIGNING_KEY_WRAPPED_FILE`, `OLIVARES_CATALOG_SIGNING_KEY_WRAPPED_FILE`, `OLIVARES_POLICY_SIGNING_KEY_WRAPPED_FILE`, `OLIVARES_KEY_CUSTODY=cmek`, or a sealed configuration file. From 0.1, a Community binary that finds one refuses to start the server and every command that opens the store (`olivares dr backup` and `olivares audit verify` among them), and never mints a replacement key. The data is kept: a Business build opens the envelopes and backs it up, with no license or an expired one. |
| Testing and spend | Evals, sandboxes, spend shown per session | Community | Every user tests AI and sees what it costs. |
| Testing and spend | Red team, budgets that alert, throttle or block, FinOps spend analysis across teams and projects | Business: base line | Assurance and cost control for organizations. A budget configured before 1.0 stays enforced in every edition: a Community binary keeps enforcing it while the FinOps module is on, can remove it, and cannot create or change one. |
| AI runtime security | Content firewall, hook firewall, server-tool egress, elicitation mediator, computer-use gate, render inspector, retrieval scan, circuit breaker, CAEP transmitter, eBPF runtime sensor | Business: AI Runtime Security | Deeper inspection and automatic response on top of the Community checks. |
| AI runtime security | MCP tool-definition pinning, threat-intelligence catalog | Business: base line | Detection depth beyond the Community deny-closed checks. |
| Accounts | Unlimited local users, passkeys (WebAuthn/FIDO2), step-up authentication | Community | No edition counts users. |
| Accounts | PIV/CAC smart cards | Business: Identity & Scale | Government and enterprise credentials. |
| Single sign-on | One active OIDC or SAML identity provider, SAML SP metadata | Community | One organization signs in with its own provider. |
| Provisioning | Inbound SCIM 2.0 server: your identity provider creates, updates and deprovisions users, and pushes its groups, in Olivares | Community | The SCIM server is open-core and stays that way. |
| Provisioning | Managed SCIM (outbound provisioning): the client that pushes create, update and deprovision to the identity provider | Business: Identity & Scale | Available in the Business Identity & Scale artifact: tenant users and groups reconcile automatically to a configured HTTPS SCIM 2.0 target, with native audit entries for each effect. Supports complete and paginated SCIM inventories and equivalent DNS, port and IPv6 endpoint spellings. The Community port remains nil and links no outbound client; its inbound SCIM server stays available. |
| Identity sources | Directories and rosters (AD/LDAP, Okta, Entra, Keycloak, Vault and others), several active identity providers, login-time group mapping, require-SSO and network allow-list, LDAP/AD login and directory sync, agent identity federation, CAEP receiver, CyberArk Conjur | Business: Identity & Scale | Organizations with directories and more than one identity provider. |
| Identity sources | The operator's group list and group-to-role mapping set in Olivares (`GET /v1/groups`, `PUT /v1/groups/{id}/role`), agent groups and their members (`/v1/agent-groups`) | Business: Identity & Scale | Groups structure an organization. The inbound SCIM server still accepts groups your identity provider pushes (`/v1/scim/v2/Groups`). Role mappings and agent groups with their members stored before 1.0 follow the one exception above: they keep applying in Community, and a stored role mapping can be cleared there. |
| Identity sources | Group nesting (`PUT /v1/groups/{id}/parent`), departments (`PUT /v1/workspaces/{id}/parent`), filing a group in a workspace (`PUT /v1/groups/{id}/workspace`), delegated department administrators, department budgets | Business: Identity & Scale | Structuring an organization. The stored department tree, department membership and group nesting follow the one exception above: they keep counting for forbid rules in Community. Group nesting has been public API since v26.8.0<!-- release-fixed -->, so it stays in Community until its move follows the deprecation path. |
| Identity sources | Scoped upstream credentials minted by OAuth 2.0 token exchange (RFC 8693) | Enterprise | Depends on the customer's own token service; scoped per contract. |
| Audit | Signed hash-chained audit ledger, viewed in the console and the CLI (`olivares audit ls`), checkpointed, verified and recovered (`olivares audit verify`, `checkpoint`, `tree`, `key-transition`, `recover`) | Community | Seeing what AI did, and proving the ledger is intact, is part of governing it. |
| Audit | `olivares audit export` (CEF, LEEF, syslog, OTLP, OCSF), `olivares audit archive export` and `verify` to a directory, `olivares audit observe-report`, the examiner-grade evidence bundle (`olivares audit archive bundle`) | Business: base line | Evidence handed to auditors and to other systems. Your data export keeps working in every edition: `olivares dr backup` (a CMEK install backs up with a Business build, which needs no live license for it). |
| Observability | Local trace and metrics views, W3C trace-context propagation | Community | Seeing local activity and carrying request context are part of governing AI. |
| Audit | SIEM and ITSM push connectors, OTLP trace downloads, trace and metric delivery to an external collector | Business: base line | Integration with enterprise operations. Local observability stays in Community. |
| Records | Retention, legal hold, right to erasure with key shred | Community | Every organization has these obligations. |
| Records | Recording of privileged actions with notice and consent, WORM archive (S3 Object Lock, Azure, GCS), regulatory retention floors with a compliance lock, legal-hold reconciliation on archives, erasure depth, incident close-loop with PagerDuty and Opsgenie | Business: Regulated Operations | What regulated industries need on top of the Community records. |
| Compliance | Framework catalogs, OSCAL export, DORA ICT-risk view, on-demand reports | Business: Compliance Packs | Compliance work is for organizations that answer to auditors. |
| Compliance | DORA register of information, ISO/IEC 42001 pack, compliance depth (US state and sector packs, FedRAMP 20x), OSCAL ingest and POA&M | Business: Compliance Packs | Drafts for auditors when audits recur. They certify nothing. |
| Compliance | Scheduled reports, report branding and templates, posture and risk summaries, posture export, evidence bundles (signed once you configure a signing key), NIS 2 incident classification, post-quantum posture check, deployment readiness | Business: base line | Reporting for organizations that answer to auditors and boards. Schedule choices and bundle-signing readiness remain available with base Business, even when Compliance Packs is disabled; generating an on-demand report still requires Compliance Packs. |
| Platform | One organization with tenant isolation in the store (row-level security on PostgreSQL), API, client SDKs, the open connectors, in-process event bus | Community | Olivares composes with what you already run. |
| Platform | Core NATS bridge; at-least-once delivery of enforcement events, with deduplication, over your NATS JetStream | Business: Identity & Scale | Event delivery across systems and nodes. |
| Platform | Several organizations served from one deployment | Enterprise | Hosting several organizations is agreed per contract. |
| License and support | AGPL-3.0, signed public releases with SBOM and provenance, public security fixes, community support in the public repository | Community | Anyone can read, build and verify it. |
| License and support | Commercial license (an AGPL exception), signed release channel where you approve every upgrade, business-hours email support (best effort, no response target; details in [SUPPORT.md](../SUPPORT.md)) | Business: base line | For organizations whose policy rules out AGPL, or that need someone to write to. |
| License and support | More legal entities, deployments and identity providers; LTS; OTA update mirrors; OEM, MSP and redistribution rights; support terms agreed in the contract, with non-binding first-response targets unless the contract says otherwise | Enterprise | Negotiated for each organization. |

## Where the product differs from this page today

- Red-team targets, runs and scoring, FinOps budget authoring, and team and project spend analysis are Business base capabilities. Community keeps their published routes with edition refusals, retains their stored data for backup, and keeps enforcing existing budgets while FinOps is on. It can read and remove those budgets. Evals, sandbox execution, and spend shown per session remain Community capabilities.

- Releases before 1.0 shipped some capabilities that this page places in Business or Enterprise in the public
  build: live terminals in the console, the access map, directory rosters, departments and nested groups, the
  group list and group-to-role mapping, agent groups, compliance views and reports, the CAEP
  receiver, privileged-action recording, and several organizations, among others. 0.1<!-- release-fixed --> moves them to the
  Business build.
  Their stored data is kept: Business reads it, and Community keeps exporting it with `olivares dr backup`
  (a CMEK install, with a Business build, which needs no live license for it).
- Releases before 1.0 install an offline update bundle built with
  the historical offline bundle producer without a license, and check a license for any other
  bundle in the public build. From 0.1 the Community binary installs no offline bundle and reads no license for a bundle:
  `olivares upgrade --bundle` refuses and names the edition, and `olivares upgrade --bundle --check` still verifies
  a bundle without installing it. Installing offline bundles is Enterprise.
- Releases before 0.1 open a configured CMEK envelope (`OLIVARES_KEY_WRAP`) in the public build. 0.1<!-- release-fixed --> moves CMEK to
  Business; a Community binary with a CMEK source configured then refuses to start the server and every command
  that opens the store, and mints no new key. Signing keys you provide (`OLIVARES_AUDIT_SIGNING_KEY[_FILE]`) keep
  working in Community.
- Login enforcement already refuses to start a Community binary on a deployment where it was enforced before; a
  login requirement or network allow-list stored on a Community install that never enforced it stays inert. The
  durable event bus already refuses to start a Community binary when configured.
  Every other seam in `cmd/olivares/wire_noenterprise.go` that is passed the configuration ignores it today
  instead: server-tool egress, the content firewall, the hook firewall, threat intelligence, the incident
  close-loop, regulatory retention floors, legal-hold reconciliation on archives, erasure depth, the computer-use
  gate, MCP tool-definition pinning, the render inspector, the elicitation mediator, the CAEP transmitter and the
  circuit breaker. NIS 2 incident classification and the posture, risk and evidence-bundle reports ignore theirs
  too, and their routes answer 501. A configured CAEP receiver still runs in the Community binary.

Placement is not maturity. [Honesty and limits](../docs-site/src/content/docs/start/honesty-and-limits.md)
states what each capability does today. The licenses by directory are in [LICENSING.md](../LICENSING.md).
