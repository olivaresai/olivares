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

An active commercial license displays a small Business or Enterprise badge in the navigation,
Search and commands, Settings > Edition, and Edition & license. Turn off **Show edition badge** in
Settings > Edition to hide it; the choice follows your account within the organization. Community
and installations without a valid commercial license show no badge.

## How a capability is placed

- Community has everything one person or one team needs to run AI on its own: install, providers, running
  the official tools, governance and enforcement, the audit ledger, records, identity with one identity
  provider, and operations. The safety boundary is never paid: built-in roles, each session bound to its
  provider, native deny-overlay rules, the kill switch and its two-person re-enable, and the deny-closed
  checks.
- Business includes everything in Community and adds what businesses and professional, regulated or large
  environments need (the console quotes the two marked summaries, one item per line):
  <!-- console-business-summary:start -->
  - Session cockpit and fleet deployment.
  - Live session sharing with named, expiring guest access and owner-approved message proposals.
  - Directories and several identity providers, departments and delegated administration.
  - Compliance and reporting, regulated records.
  - Deeper runtime inspection and key management.
  - Integrations with enterprise systems, commercial license and support.
  <!-- console-business-summary:end -->

  One subscription includes the Business base line and four capability families (Regulated Operations, AI Runtime
  Security, Compliance Packs, Identity & Scale); an administrator turns each family on or off.
- <!-- console-enterprise-summary:start -->Enterprise includes everything in Business, plus terms agreed in a contract and the capabilities that depend on them.<!-- console-enterprise-summary:end -->

From 0.1, no edition:

- moves a Community feature to a paid edition, or caps or time-limits it;
- counts users: every edition has unlimited users;
- puts the implementation of a paid capability in the public repository, as source, a disabled flag or a hidden
  page; the public tree holds only seams that answer 501 or do nothing. The one exception is what you stored
  before 1.0: a Community binary keeps enforcing stored custom Cedar permits and forbids, custom roles, permission groups and scoped grants,
  the review and quorum restrictions of stored approval policies, budgets and group-to-role mappings,
  and the department tree, department membership, group nesting, and agent groups with their members that
  forbid rules match. A stored approval policy that raises the tier or the quorum keeps applying in Community; lowering is inert there. A legal hold a Business engine stored keeps vetoing every purge, knowledge delete and account retirement in Community; Community cannot list or release it, and Business can.
  It lists custom authorization data read-only and can delete roles, permission groups and grants or disable the
  authored Cedar surface (`DELETE /v1/m/governance/pdp/active?engine=cedar`) without deleting revision history.
  Business makes the same stored data editable, with no migration. It can remove the stored budgets and other restricted policies, and clear a stored group-to-role mapping
  (`PUT /v1/groups/{id}/role` with an empty role), which only narrows access; it cannot create, widen or
  otherwise change any of these. The inbound SCIM server that applied what your identity provider pushed is
  Business: a Community binary answers it with 501, so a group the provider pushed before stays stored and listed
  and nothing more arrives from the provider;
- makes the Community binary read a license to turn a capability on or off;
- silently drops a protection that was in force: apart from the stored items above, a Community binary that finds
  a Business protection that was in force on that deployment (a CMEK key source, a login requirement or network
  allow-list, durable delivery, a runtime check or records control, a CAEP receiver or transmitter) refuses to start
  and names the edition.

The behaviors that do not follow these rules yet are listed under "Where the product differs from this page
today".

A Business or Enterprise capability runs only while a valid license whose grants include it is installed;
the Business build alone does not turn it on. With no license, an expired one or a revoked one, the build refuses
that capability's new work with `addon_requires_license` and keeps reading, verifying and exporting your data.
The same holds for the Business choices made inside routes and commands Community also serves: steering a
colleague's session, a managed session credential, a SIEM sink, an approval policy beyond the Community line,
assigning provider profiles, AI tools and department standards, and `olivares ddil import`. The engine's own
in-process calls (activating a catalog instance, the deployment drift loop) pass the same license check as a
request from outside ([LICENSING.md](../LICENSING.md) states how the license is checked).

Commercial rights run to the end of the paid term. After it, Community features, your data and your export
keep working. A CMEK install needs the Business build to start; with no license or an expired one, that
build still opens the key and configuration envelopes for `olivares dr backup`, and `olivares keys unseal`
still prints a sealed configuration file.

## Release cadence

Business and Community share one release numbering (`MAJOR.MINOR`, see
[versioning](../INSTALL.md#versioning)). A number that exists in both editions is built from one core source
commit, so a fix in Community's core is in the Business release of the same number.

From the release after 0.1<!-- release-fixed -->:

- Business X.Y is published within 24 hours of Community X.Y, from the same core commit. Community X.Y is tagged
  on its own gates; the only Business step before either tag is pinning the candidate commit.
- Business-only features keep their own pace. They never hold a core fix back, and no Community release waits
  for them.
- A hotfix is the next release number, built from the newest release's core and the fix alone. It carries no
  schema migration, so returning to the release before it needs no data step. It is signed like every release and
  delivered on the same licensed channel: `stable`, and `security` when it declares an advisory. A core fix ships
  in both editions under that number; a number that only Business has is a Business hotfix. Every licensee of a
  set receives it: there is no per-customer build.
- Business data stays intact across editions: a Community build exports it offline (`olivares dr backup`,
  `olivares dr verify`) without changing it, unless a protection listed above needs the Business build, and the
  Business build of a later number reads it again with its stored evidence unchanged. How a Community server
  behaves on that directory is the last item under "Where the product differs from this page today".

How a hotfix is delivered and rolled back is in
[Upgrading, rolling back, and reconfiguring](UPGRADE-AND-ROLLBACK.md#hotfix-releases).

## Placement

| Area | Capability | Edition | Why |
|---|---|---|---|
| Install | Static binary with embedded console, deb and rpm packages, container; SQLite or PostgreSQL; backup and restore | Community | Every user starts here. |
| Install | Helm chart, Kubernetes operator, Terraform provider, appliance images, FIPS and STIG images | Business: base line | Fleet and regulated deployment. |
| Install | Deployment and integration: declare, plan, apply, verify, roll back and retire agents and MCP servers on company infrastructure, with the saved executor setup and the ledger of declared wirings (`olivares deploy`, `/v1/m/deploy`) | Business: base line | Acting on company infrastructure is fleet deployment. Community keeps the routes, which answer 501 and name Business, and the stored tables. `olivares agent deploy` (finding an official CLI on this host and registering its profile) stays in Community. |
| Install | Air-gapped install and offline update bundles | Enterprise | Disconnected sites are set up per contract. Verifying a bundle without installing it (`olivares upgrade --bundle --check`) stays in Community. |
| Install | Air-gap DDIL transfer of governance state between disconnected sites: `olivares ddil keygen`, `export`, `verify` and `import` | Business: base line | Air-gapped and disconnected operations. Community answers exit code 9 to every `ddil` command. |
| Providers | Provider tools sign in with their own login, or with your own provider keys | Community | Every session starts with a provider. |
| Sessions | Launch, list, show the status of, stop and remove official CLIs (Claude Code, Codex, Grok Build) from the CLI and the console: `olivares session start`, `ls`, `show`, `stop`, `rm`; `olivares agent session create`, `ls`, `get`, `stop`, `cleanup`, `rm` | Community | Running the tools is the first job of the product. |
| Sessions | Optional instance-wide idle auto-stop for finished sessions, off by default, with audited policy stops and resume in the same folder and conversation | Community | A person or team can reclaim memory on a shared host without losing its work. |
| Sessions | Follow a session and stop it doing harm from the CLI: `olivares session follow`, `events` and `interrupt`; `olivares agent session events` and `interrupt` | Community | One person watching their own session, and interrupting it, is part of running the tools safely. |
| Sessions | Prepare local Git work in the session console: status, stage and unstage files, commit with the signed-in user's profile, and create or switch local branches inside the session's OS boundary | Community | Preparing one's own session changes is part of running the tools. Existing governed publication prepares pushes and pull requests. |
| Sessions | Preview in the session console the app a session serves on a local port: the engine proxies it under a short-lived link, sandboxed away from the console, with reload and open in a new tab | Community | Seeing what one's own session builds is part of running the tools. |
| Sessions | Steer a session you started, from the CLI and the console: `olivares session send`, `resume` and `peers`; `olivares agent session attach`, `input` and `resume`; the message box, Resume and live view of one session | Community | Directing the session you started is part of running the tools. |
| Sessions | Steer a session another person started: the same commands and console actions on a colleague's session | Business: base line | Directing a colleague's session is team operation. Community answers `501 business_required` to these calls; the `sessions:run:write` role check is the same in both editions. |
| Sessions | Managed credentials for sessions: an SSH key or short-lived certificate, a database login or an API account that the engine holds and a helper uses for the session (`credentials` on a launch; the document sealed as `credential/<name>` in the tenant's secrets) | Business: base line | A session reaches a company's servers, databases and APIs without ever holding the key or the password: the helper is an SSH agent socket, a PostgreSQL proxy or a local API proxy, and it stops when the session does. Community answers `501 business_required` to a launch that names a credential. |
| Sessions | Session cockpit: the workspace sidebar, several live terminals in panes at once, the agent dashboard, and the agent listener over mutual TLS | Business: Identity & Scale | The cockpit is a working environment for a team's live sessions; launching, watching, steering, interrupting and stopping the sessions you started stay in Community. |
| Sessions | Governed connections: a database, an SSH host or a service on the network, named once by an administrator with its credential by reference (a `connection/` entry of the secret store) and granted to departments; a session in a granted department, or one below it, reaches it through the engine, which holds the credential, and every use is audited (`olivares connection`, `/v1/m/sessions/connections`) | Business: Identity & Scale | Reaching company systems on behalf of a department is organization-wide governance, and departments are Business. A Community server answers the six routes with 501 `business_required` and offers a session no connection tool; connections a Business build stored stay inert there. MCP servers a session uses stay in Community. |
| Visibility | Inventory of agents, sessions, models, MCP servers and tools; live timelines; health | Community | Governance starts with seeing what runs. |
| Visibility | Access map (graph, neighbors, permitted-versus-observed drift and attack paths) and identity inventory (identities, the groups mirrored with them, and the agent-to-identity bindings) | Community | Seeing who reaches what is the first job of governance. Filling the inventory from a directory is Business (see Identity sources). |
| Work | Work items | Community | Part of running AI at any size. |
| Work | Orchestration, A2A delegation to authorized peers | Business: Identity & Scale | Coordinating agents across systems. |
| Knowledge | Content sources for files, Google Drive, Confluence, Notion, S3, Snowflake, Azure AI Search and PostgreSQL; governed retrieval, model catalog, inference proxy | Community | Connecting your own data is the core job. |
| Knowledge | Enterprise-system connectors: the SharePoint, SAP OData and Salesforce content sources, and the ServiceNow CMDB, Oracle audit, KMIP and Claude Compliance API sources | Business: base line | Integration with enterprise systems. Community does not construct them: a configured one stops startup and names Business, and the API answers 501 `connector_unavailable`. |
| Knowledge | Workspace wiki: built-in olivares-wiki skill, folder initialization, ingest, cited query, and lint through tool sessions | Community | A person or team needs to maintain its own sources and linked pages. |
| Authorization | Built-in roles, native deny overlay, kill switch | Community | Who reaches what is the safety boundary, and it is never paid. Stored custom permits, forbids, roles and scoped grants keep their existing effect. Read and revoke stay available; create/update returns 501. |
| Authorization | Custom Cedar policies (permits and forbids), custom roles, permission groups and scoped grants | Business: base line | Policy as code for organizations with their own rules. |
| Authorization | Time-boxed scoped grants and source-scope bindings, self-service access requests approved by the department admin or owner, expiry reminders, and access-review groups for expiring and never-expiring grants | Business: base line | Temporary access with an auditable request, approval and expiry lifecycle. Existing grants without an expiry keep working unchanged. |
| Authorization | External policy engine: a Cedar policy file or Open Policy Agent, selected with `OLIVARES_PDP_ENGINE` | Business: base line | Policy as code for organizations with their own rules. Community refuses to serve with an engine selected (exit code 9); unset, blank or `none` is the native engine alone. |
| Authorization | Approval engine with its fixed floors (re-enabling the kill switch and every critical action need two distinct people), and any number of approval policies that require a review of an action | Community | The floors are fixed in code and guard Community's own safety boundary. A tenant keeps as many such policies as it needs. When several enabled policies match an action, the earliest created one applies. A policy that sets a tier or an approval count different from the action's default is Business (next row). |
| Authorization | Approval by what a publication touches: a session's push or pull request is classified `docs_only`, `code` or `sensitive` from the paths its exact commit changes (a rename counts both paths; a push that names a lease counts every path that differs from the old tip), by the tenant's sensitive globs, which are the defaults plus the `sensitive_paths` of its approval policies (the defaults cover CI and release configuration, `CODEOWNERS`, dependency manifests and lockfiles, build recipes, migrations, and the files that steer other agents); an approval policy may match on the class to raise the tier or the quorum, or require someone other than the launcher to approve; the approval card and the ledger entry show the class and the matched paths, never diff content | Business: base line | Community publication approval is unchanged: one approval of the proposal as a whole, and authoring a policy that names a class, lists sensitive paths or excludes the launcher is refused with 501 there (a stored one, written before a downgrade, keeps raising scrutiny and cannot lower a tier). In Business a policy that names a class is preferred, for a change of that class, over one that names none, except that excluding the launcher is the sum of the policies that apply. |
| Authorization | Approval policies that change the risk tier or the approval count (raising or lowering) and break-glass | Business: base line | Community keeps the approval engine, the two-human CRITICAL floor, kill-switch dual control, and authoring/enforcement of review policies; authoring a policy that sets a tier or an approval count different from the action's default is refused with 501 `business_required`. Stored raising policies keep applying and can be read, switched off and deleted, but saving a change to one is Business; stored lowering policies remain readable/exportable but cannot lower Community defaults or floors; stored emergency grants are inert there. Business retains authoring tier and quorum policies, lowering, and the audited, recorded emergency lifecycle. |
| Authorization | Approval bound to a change record: an approval policy names a ServiceNow change request or a Jira issue (`change_record`: system, key, optional destination and allowed states); an approval under it is decided, and spent or relied on by a gate that reads its status, only while that record is in an allowed state and inside its window, the engine reads the record through the tenant's ServiceNow or Jira notification destination (the read-only credential stays behind it), a record canceled after the approval revokes the unspent grant and stops a spent one from authorizing anything new, an unreachable system refuses (deny closed), and the ledger keeps the key, the state and the time observed | Business: base line | Change management is an enterprise process. Community refuses authoring a policy that names a record with 501 `business_required`; a stored one approves nothing there, because Community wires no reader. |
| Governance | Fleet-wide managed settings for Claude Code, Codex, Grok Build, OpenCode and Gemini CLI: publishing a revision of a tool's managed file (`managed-settings`, `hooks`, `managed-mcp` and `sandbox` for Claude Code, and `managed-instructions@<department>` for the instructions file `/etc/claude-code/CLAUDE.md` of a department's workstations; `codex-requirements`, `codex-managed-config`, `grok-requirements`, `opencode-config` and `gemini-cli-settings` for the others) and its revision history, the signed artifact that workstations pull and verify, the attested check-in, the drift view (`olivares claude-policy publish`, `versions`, `artifact`, `pull`, `checkin`, `distribution`), and the inventory of the AI tools each workstation reports on that check-in: the driver, its version and its sign-in state (`/v1/m/claude-policy/inventory`) | Business: base line | Managing the policy and the tools of a fleet of workstations is fleet management. A Community server answers these routes with 501 and names Business, and keeps the stored table. Validating and previewing a document (`olivares claude-policy validate`, `dry-run`) stays in Community, and `olivares agent managed-settings`, `olivares codex managed-config` and `olivares grok managed-config` still render the files for the hosts you manage yourself. |
| Enforcement | Four deny-closed enforcement points: Claude Code hook, inline inference proxy, MCP `tools/call` gate, A2A delegation gate | Community | The deny-closed checks are part of the safety boundary. Without the Business A2A delegation, its gate refuses every delegation. |
| Data protection | Inline guardrails (PII, prompt injection, jailbreak), DLP on egress, hybrid post-quantum TLS (the Go default key exchange on the product's TLS listeners) | Community | Data that reaches a provider is part of the safety boundary. The inference proxy is a Community module that a new install leaves off until an administrator turns it on (`olivares modules on inferenceproxy`). Once it is on, and while its request DLP gate is on (the default), it denies a prompt in which its classifier finds a secret (the `secret.credential` class). An administrator edits the DLP rules in the console under Inference proxy, in every edition. |
| Data protection | Signing keys you provide (`OLIVARES_AUDIT_SIGNING_KEY[_FILE]`, and the catalog and policy keys) and `olivares keys status` | Community | The audit ledger needs a key you can back up and restore on another host, and an operator reads the custody of an installation to recover it. |
| Data protection | Custody assertions `OLIVARES_KEY_CUSTODY=byok` (the audit key must be one you provide, never a minted one) and `OLIVARES_LEDGER_CUSTODY=hyok` (checkpoints must be signed off-box), the off-box checkpoint signer (`OLIVARES_LEDGER_SIGNER`) and the signing-key transition (`olivares audit key-transition`) | Business: base line | Regulated key management. A Community binary refuses to start, or to run a command that opens the engine, when `OLIVARES_KEY_CUSTODY=byok`, `OLIVARES_LEDGER_CUSTODY=hyok` or an off-box `OLIVARES_LEDGER_SIGNER` is set, names Business and exits with code 9; `audit key-transition` answers the same way. The data is kept: unset the variable, or run the Business build. |
| Data protection | CMEK: signing keys and operator configuration files sealed in envelopes that only your key management service opens (`OLIVARES_KEY_WRAP`; `olivares keys wrap`, `rotate`, `rewrap`, `seal`, `unseal`) | Business: base line | Key management for organizations that hold their keys in a key service. A CMEK source is any of `OLIVARES_AUDIT_SIGNING_KEY_WRAPPED_FILE`, `OLIVARES_CATALOG_SIGNING_KEY_WRAPPED_FILE`, `OLIVARES_POLICY_SIGNING_KEY_WRAPPED_FILE`, `OLIVARES_KEY_CUSTODY=cmek`, or a sealed configuration file. From 0.1, a Community binary that finds one refuses to start the server and every command that opens the store (`olivares dr backup` and `olivares audit verify`, `olivares audit tree verify`, `olivares audit tree prove` among them), and never mints a replacement key. The data is kept: a Business build opens the envelopes and backs it up, with no license or an expired one. |
| Testing and spend | Evals, sandboxes, spend shown per session | Community | Every user tests AI and sees what it costs. |
| Testing and spend | Red team, budgets that alert, throttle or block, FinOps spend analysis across teams and projects | Business: base line | Assurance and cost control for organizations. A budget configured before 1.0 stays enforced in every edition: a Community binary keeps enforcing it while the FinOps module is on, can remove it, and cannot create or change one. |
| AI runtime security | Content firewall, hook firewall, server-tool egress, elicitation mediator, computer-use gate, render inspector, retrieval scan, circuit breaker, CAEP transmitter, eBPF runtime sensor, reversible PII tokens in session MCP tool results | Business: AI Runtime Security | Deeper inspection and automatic response on top of the Community checks. |
| AI runtime security | MCP tool-definition pinning, threat-intelligence catalog | Business: base line | Detection depth beyond the Community deny-closed checks. |
| Accounts | Unlimited local users, passkeys (WebAuthn/FIDO2), step-up authentication | Community | No edition counts users. |
| Accounts | PIV/CAC smart cards | Business: Identity & Scale | Government and enterprise credentials. |
| Single sign-on | One active OIDC or SAML identity provider, SAML SP metadata | Community | One organization signs in with its own provider. |
| Provisioning | Inbound SCIM 2.0 server: your identity provider creates, updates and deprovisions users, pushes its groups, and sends security events (the SET receiver), in Olivares (`/v1/scim/v2`) | Business: Identity & Scale | Managed provisioning from a company identity provider. A Community server mounts the same URLs, authenticates the token as before and answers 501 in the SCIM error shape, naming Business; nothing is read or written. Removing a member from the organization stays in Community: the console's People tab and `DELETE /v1/members/{id}` do it with no SCIM server. Accounts and groups an identity provider provisioned before the move stay stored. |
| Provisioning | Managed SCIM (outbound provisioning): the client that pushes create, update and deprovision to the identity provider | Business: Identity & Scale | Available in the Business Identity & Scale artifact: tenant users and groups reconcile automatically to a configured HTTPS SCIM 2.0 target, with native audit entries for each effect. Supports complete and paginated SCIM inventories and equivalent DNS, port and IPv6 endpoint spellings. The Community port remains nil and links no outbound client. |
| Identity sources | Directories and rosters (AD/LDAP, Okta, Entra, Keycloak, Vault and others), several active identity providers, login-time group mapping, require-SSO and network allow-list, LDAP/AD login and directory sync, agent identity federation, CAEP receiver, CyberArk Conjur | Business: Identity & Scale | Organizations with directories and more than one identity provider. |
| Identity sources | The operator's group list and group-to-role mapping set in Olivares (`GET /v1/groups`, `PUT /v1/groups/{id}/role`), agent groups and their members (`/v1/agent-groups`) | Business: Identity & Scale | Groups structure an organization. Groups an identity provider pushed stay listed; the inbound SCIM server that receives them is Business (the Provisioning row). Role mappings and agent groups with their members stored before 1.0 follow the one exception above: they keep applying in Community, and a stored role mapping can be cleared there. |
| Identity sources | Group nesting (`PUT /v1/groups/{id}/parent`), departments (`PUT /v1/workspaces/{id}/parent`), filing a group in a workspace (`PUT /v1/groups/{id}/workspace`), delegated department administrators, department budgets | Business: Identity & Scale | Structuring an organization. The stored department tree, department membership and group nesting follow the one exception above: they keep counting for forbid rules in Community, which can neither change nor clear them (clearing a nesting could lift a forbid rule that matches through the parent group). A Community server answers the three `PUT` routes with 501 `departments_unavailable` and names Business. |
| Identity sources | Assigning provider profiles and AI tools to departments, groups and people, so each sees and launches only the tools, accounts and model paths assigned to it (`olivares sourcescope bindings create --source-type profile` or `tool`; a department assignment reaches its sub-departments, a forbid is absolute and an inheritance filter on the `provider` class cuts what reaches a node from above) | Business: Identity & Scale | A department with its own tools and accounts is how an organization is structured. A profile or tool nobody was assigned stays open to everyone, and a Community server keeps applying the assignments already stored, lists and revokes them, and answers a create or an update with 501 `business_required`. |
| Identity sources | Assigning instructions and a default session template to departments and directory groups, so each Claude Code launch starts from its department's standards: a session template's custom instructions are given to every launch of the people the assignment reaches, parent department first and ahead of the launch's own, and the template assigned as default governs a launch that names none (`olivares sourcescope bindings create --source-type instructions` or `template`, with `--scope-tree workspace` or `user_group`; a department assignment reaches its sub-departments, a forbid is absolute, and a department or group has one default template, which a launch that names another keeps). A person belongs to the departments their directory groups are filed in (`PUT /v1/groups/{id}/workspace`), and an acting agent or a department-confined member to its own; a session keeps the standards it was launched with when it is resumed, whoever resumes it. The same text is the department's instructions file, which an administrator publishes (`olivares claude-policy publish managed-instructions@<department>`) and workstations pull (`olivares claude-policy pull managed-instructions@<department> --out /etc/claude-code/CLAUDE.md`): a snapshot that is world-readable on the workstation, so it holds nothing secret, while sessions read the department live (`dry-run` says whether the file is current). Instructions assigned to a directory group reach its members' sessions and are not in a department's file | Business: Identity & Scale | An organization states its standards once, per department, instead of once per tool and workstation. Taking an assignment off a department or group, or lifting it with a forbid, waits for a second administrator, as every relaxation does; assigning one does not. Changing a template that is assigned, or the skills pinned to it, needs template administration; that administrator alone can still empty an assigned instructions template or edit a default template so that it no longer governs, because the second administrator guards the assignment and not the template's text. An assigned template that is archived or gone refuses the launches it would have shaped, naming it; a stopped session launched with a template since taken off its department and archived continues without it. Only Claude Code is given them. A launch that carries none of this is what it always was, and a Community server keeps applying the assignments already stored, lists and revokes them, and answers a create or an update with 501 `business_required`. |
| Identity sources | Scoped upstream credentials minted by OAuth 2.0 token exchange (RFC 8693) | Enterprise | Depends on the customer's own token service; scoped per contract. |
| Audit | Signed ledger and its console/API/CLI reads, checkpoints and signing-key state | Community | Seeing what AI did stays available. Verification and evidence export run in Business; stored ledgers, checkpoints and keys are preserved in every edition. |
| Audit | `olivares audit verify`, `olivares audit tree verify` and `prove`, `olivares audit export` (CEF, LEEF, syslog, OTLP, OCSF), `olivares audit archive export` and `verify` to a directory, `olivares audit observe-report`, the examiner-grade evidence bundle (`olivares audit archive bundle`) | Business: base line | Evidence handed to auditors and to other systems. Your data export keeps working in every edition: `olivares dr backup` (a CMEK install, or one that sets a Business custody or off-box signer setting, backs up with a Business build, which needs no live license for it, or in Community after the setting is unset). |
| Observability | Local trace and metrics views, W3C trace-context propagation | Community | Seeing local activity and carrying request context are part of governing AI. |
| Audit | SIEM and ITSM push connectors, OTLP trace downloads, trace and metric delivery to an external collector | Business: base line | Integration with enterprise operations. Local observability stays in Community. In Community, a create or update of an event subscription that carries a SIEM sink (`sink_kind` set) answers `501 business_required`. Generic webhook subscriptions stay in Community. A sink stored before delivers nothing: an update that omits it disables or clears the subscription's sink, and `DELETE` removes the subscription. |
| Records | Retention schedules and the hold-checked sweep | Community | Every organization keeps its own data as long as it needs to. |
| Records | Legal hold, right to erasure with key shred | Business: Regulated Operations | Litigation and regulated records. Community keeps the `/v1/m/compliance` hold and erasure routes (`/holds`, `/erasure`, `/data-subjects`, `/claude-files`), which answer `501 business_required`; `olivares compliance holds`, `erasure` and `subject` are hidden and exit 9. Deleting your own data (sessions, memory, knowledge) is not an erasure request and stays in Community. |
| Records | Recording of privileged actions with notice and consent, WORM archive (S3 Object Lock, Azure, GCS), regulatory retention floors with a compliance lock, legal-hold reconciliation on archives, erasure depth, incident close-loop with PagerDuty and Opsgenie | Business: Regulated Operations | What regulated industries need on top of the Community records. Community keeps the `/v1/m/recording/` routes, which answer `501 business_required`, and records nothing; sessions already stored stay in the store. |
| Compliance | Framework catalogs, OSCAL export, DORA ICT-risk view, on-demand reports | Business: Compliance Packs | Compliance work is for organizations that answer to auditors. |
| Compliance | DORA register of information, ISO/IEC 42001 pack, compliance depth (US state and sector packs, FedRAMP 20x), OSCAL ingest and POA&M | Business: Compliance Packs | Drafts for auditors when audits recur. They certify nothing. |
| Compliance | Scheduled reports, report branding and templates, posture and risk summaries, posture export, evidence bundles (signed once you configure a signing key), NIS 2 incident classification, post-quantum posture check, deployment readiness | Business: base line | Reporting for organizations that answer to auditors and boards. Schedule choices and bundle-signing readiness remain available with base Business, even when Compliance Packs is disabled; generating an on-demand report still requires Compliance Packs. |
| Platform | One organization with tenant isolation in the store (row-level security on PostgreSQL), API, client SDKs, the open connectors, in-process event bus | Community | Olivares composes with what you already run. |
| Platform | Core NATS bridge; at-least-once delivery of enforcement events, with deduplication, over your NATS JetStream | Business: Identity & Scale | Event delivery across systems and nodes. |
| Platform | Active-passive HA leader routing (`OLIVARES_HA_LEADER_GATE`, `OLIVARES_HA_LEADER_LABEL`: the leader gate and the pod-label publisher) and the edge collector (`olivares collector`) | Business: base line | Availability and scale across nodes and sites. A Community server with a leader-routing switch set refuses to start and names Business; `olivares collector` is hidden from its help and exits 9. |
| Platform | Several organizations served from one deployment | Enterprise | Organization management uses the private Enterprise implementation. In Community, `/v1/system/orgs` routes answer 501 and `olivares tenants` is hidden from help and exits 9 when invoked. Stored organizations and tenant isolation are retained; Community still exports their data. Hosting several organizations is agreed per contract. |
| License and support | AGPL-3.0, signed public releases with SBOM and provenance, public security fixes, community support in the public repository | Community | Anyone can read, build and verify it. |
| License and support | Commercial license (an AGPL exception), signed release channel where you approve every upgrade, business-hours email support (best effort, no response target; details in [SUPPORT.md](../SUPPORT.md)) | Business: base line | For organizations whose policy rules out AGPL, or that need someone to write to. |
| License and support | More legal entities, deployments and identity providers; LTS; OTA update mirrors; OEM, MSP and redistribution rights; support terms agreed in the contract, with non-binding first-response targets unless the contract says otherwise | Enterprise | Negotiated for each organization. |

## Console pages

The Community console registers no page for a Business capability: its navigation, area
directories and command palette list only Community pages, and none of its pages asks for a
Business route (which answers 501). A bookmark of a Business page opens one sentence that says
the page is part of Business, with a link to it and no action. Retention schedules, a Community
capability, have their own Community page, `/retention`; the Compliance page that also holds that
tab is Business. These are the Business pages;
`web/src/features/route-census.json` records the same paths as `business_paths`, and a test
keeps the two lists equal.

<!-- console-business-pages:start -->
- `/automations`
- `/compliance`
- `/deploy`
- `/finops`
- `/orchestration`
- `/organization`
- `/recordings`
- `/red-team`
- `/residency`
- `/session-viewer/$id`
- `/team-costs`
- `/tenants`
<!-- console-business-pages:end -->

## Identity & Scale extraction

Directory snapshot/reconciliation, agent registration and managed lifecycle,
agent identity federation and the CAEP receiver are private Business source. Community keeps
their authenticated route contracts with explicit 501 refusal and hides their CLI commands
and console views. The access map and the identity inventory (identities, mirrored groups, agent bindings)
stay in Community with the routes, fields and status codes of 0.1. Local login, passkeys, TOTP, one OIDC/SAML
provider and their account standing protections remain in Community.

The shared schema and backup preserve existing identities, bindings, access edges and lifecycle
records. Business reads those records without a migration. Community continues enforcing stored
NHI revocation, sponsorship and offboarding decisions. An enabled stored CAEP receiver, a known NHI rotation timestamp,
a configured rotation window or a pending escalation deadline prevents Community server startup;
offline backup can still open and export the estate with its existing directory configuration.
Active directory ingestion, managed lifecycle configuration and WIF opt-in refuse explicitly.

## Where the product differs from this page today

- Red-team targets, runs and scoring, FinOps budget authoring, and team and project spend analysis are Business base capabilities. Community keeps their published routes with edition refusals, retains their stored data for backup, and keeps enforcing existing budgets while FinOps is on. It can read and remove those budgets. Evals, sandbox execution, and spend shown per session remain Community capabilities.

- Community 0.2 and earlier let any caller with `sessions:run:write` steer a session another person
  started (input, resume and peers). That is a Business capability, and from the next release a Community
  engine answers `501 business_required` to those calls on a colleague's session. Steering the session you
  started stays in Community.

- Releases before 1.0 shipped some capabilities that this page places in Business or Enterprise in the public
  build: several live terminals in panes at once, directory rosters, departments and nested groups, the
  group list and group-to-role mapping, agent groups, compliance views and reports, the CAEP
  receiver, privileged-action recording, and several organizations, among others. 0.1<!-- release-fixed --> moves them to the
  Business build.
  Their stored data is kept: Business reads it, and Community keeps exporting it with `olivares dr backup`
  (a CMEK install, with a Business build, which needs no live license for it).
- A Community engine whose stored recording consent is `required` enforces nothing and logs one warning at start;
  it does not refuse to start.
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
  Every other Business protection that is configured through an environment key refuses the same way: a
  Community `olivares serve` or `olivares quickstart` exits with the Edition exit code (9), names the key and
  Business, and opens nothing, when one of these is set, in the environment or by an active entry of the data
  directory's activation manifest: server-tool egress
  (`OLIVARES_SERVERTOOL_EGRESS_CONFIG`), the content firewall (`OLIVARES_CONTENT_FIREWALL_CONFIG`), the hook
  firewall (`OLIVARES_HOOK_FIREWALL_CONFIG`), threat intelligence (`OLIVARES_THREATINTEL_CONFIG`), the incident
  close-loop (`OLIVARES_INCIDENTLOOP_CONFIG`), regulatory retention floors (`OLIVARES_RETENTION_GOVERNOR_CONFIG`),
  legal-hold reconciliation on archives (`OLIVARES_AUDIT_LEGALHOLD_RECONCILE=true`), erasure depth
  (`OLIVARES_RTBF_DEPTH_CONFIG`), the computer-use gate (`OLIVARES_COMPUTER_USE_CONFIG`), MCP tool-definition pinning
  (`OLIVARES_TOOL_PIN_CONFIG`), the render inspector (`OLIVARES_RENDER_INSPECTOR_CONFIG`), the elicitation mediator
  (`OLIVARES_ELICITATION_MEDIATOR_CONFIG`), the CAEP transmitter (`OLIVARES_CAEP_TRANSMITTER_CONFIG`), the circuit
  breaker (`OLIVARES_CIRCUIT_BREAKER_CONFIG=true`), NIS 2 incident classification (`OLIVARES_NIS2INCIDENT_CONFIG`), the NIS 2
  posture, risk and evidence-bundle reports (`OLIVARES_REPORTING_CONFIG`) and reversible PII tokens for session tool data
  (`OLIVARES_PII_TOKENS_CONFIG`), the per-server upstream credential minter (`OLIVARES_CREDENTIAL_MINTER_CONFIG`), the
  onboarding readiness assessor (`OLIVARES_ONBOARDING_CONFIG`) and the post-quantum posture assessor (`OLIVARES_PQC_POSTURE_CONFIG`).
  Those two are read only when the Business `enterprise readiness` and `pqc-posture` commands run, not at serve time.
  The agent-link and agent-enrollment listeners (`OLIVARES_SESSIONS_AGENT_LINK_LISTEN`,
  `OLIVARES_SESSIONS_AGENT_BOOTSTRAP_LISTEN`), the SIEM forwarder
  interval (`OLIVARES_SIEM_FORWARD_INTERVAL`) and the orchestration cadence and workflow pumps
  (`OLIVARES_ORCH_CADENCE_INTERVAL`, `OLIVARES_ORCH_WORKFLOW_INTERVAL`, `OLIVARES_ORCH_WORKFLOW_MAX`,
  `OLIVARES_ORCH_WORKFLOW_STEPS_MAX`) are refused the same way. A set value refuses even when it is `0`, which turns the loop off in Business.
  A Community binary also refuses to serve when the data directory's activation manifest exists and cannot be read or
  parsed, because it cannot tell which Business capabilities that manifest activates.
  Two Business seams are not refused by key: a notify destination of a Business kind is skipped with a log line that
  names Business, and a `conjur` source kind refuses startup naming Business, as the other Business source kinds do.
  A blank key, or a switch that is not true, protects nothing and is not refused; commands that do not serve (`db`,
  `dr`, `audit`) are not refused. Tool-definition pinning runs in Business with or without its key, so a Community binary
  cannot tell that pins were in force from the key alone. The CAEP receiver is private Business source. Community refuses its HTTP/configuration operations and refuses server startup while an enabled receiver remains stored; offline backup stays available.

- A same-version Business build starts on a Community-created directory after Community has started successfully
  and been stopped cleanly. Your accounts, settings and audit history stay in place; no license is needed to start.

- A Community server starts on a data directory that a Business build has written and keeps every stored row;
  what Community does not offer answers 501, and the Business build opens the directory again. Its store admission
  compares the stored guard history with the histories it was built with, a Business build's among them. A directory
  with any other history is refused before login, naming the mismatch, and is not changed; `olivares dr backup` and
  `olivares dr verify` open and export it. The cross-edition check distinguishes safe refusal from a successful
  operational crossing: a refused startup preserves data but does not qualify Community operation on that directory.

Placement is not maturity. [Honesty and limits](../docs-site/src/content/docs/start/honesty-and-limits.md)
states what each capability does today. The licenses by directory are in [LICENSING.md](../LICENSING.md).
