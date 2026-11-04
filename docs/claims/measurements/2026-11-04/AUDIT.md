<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Current capability source review, 2026-11-04

Product source H: `85d7e17e4feec062cbb85b50077e82aefed3394f`, the development source of the 0.6 line (`RELEASE-VERSION` reads 0.6).
Website consumer W: `b627d1b01a353a63b8274486f601ef30254b90ef`.
Enterprise comparison identity E: `c662fbc086a0aa951e2e47fa33f8e10a47dad81f`.
H was read from its Git objects; W is the consumer code identity whose claims validator was run on this manifest;
E is a resolved comparison identity, and no Business or Enterprise source or capability was reviewed. This review
concerns Community source at H, not deployed website copy, a release binary or Business behavior.

The previous review (2026-10-21) read source `d7ac76cfc9951c67a99ffd2e2af9316b7d976950`. Its dated audit and index stay in
place; the manifest it described stays in Git history. H is 4,949 commits later. Method: for each of the 12 claims, every
control anchor and component-test path was compared between that source and H; each changed control file was read as a
diff; each sentence of the claim that a diff or `docs/editions.md` could touch was checked against H by reading the
code, not by trusting the earlier text. Of the 85 control anchors, 43 name the same blob as before; the other 42, in 32
files, were read. No test was executed.

## Entry review

| Claim | Source observation at H | Decision and limits |
| --- | --- | --- |
| `sessions-live-operations` (C1) | `schema.go`, `api.go` and `communication_handoff_apply.go` changed; `live.go`, `work_api.go`, `communication_api.go` and `communication_handoff_service.go` did not. The new session routes (a person's own pin, Web Push devices, governed connections, folder sharing) and the `?since` filter on the timeline add behavior and leave the claim's sentences true. The `goal` column still has no writer: its only reader is `dto.go`. Handoff cancellation is a method with no route, and the deadline service constructor is private and unwired. | Keep the claim as measured. No sentence changed. Connections and sharing are Business and are not claimed. |
| `agent-coordination` (C2) | Orchestration routes, the command and A2A delegation are the same blobs as before and still refuse in Community. `mcpgateway.go` still tries to build the A2A push receiver and inbound server, and `a2agateway_community.go` still returns a Business error for both, so neither is mounted. New at H: the orchestration cadence and workflow settings (`OLIVARES_ORCH_*`) are Business protection keys, which a Community server refuses at boot even at `0`. | Add that refusal to the condition about the missing pump. The gateway-refusal test gap stays: no test covers inbound A2A and push. |
| `simulation-sandbox` (C3) | Every sandbox and `sandboxrt` source is the same blob as before; `wire.go` changed elsewhere. The mock runner, backend preflight and first-passing-backend rule read as before. | Keep the claim as measured. Preflight is still a presence check, not an isolation test. |
| `model-routing-providers` (C4) | `models-view.tsx` changed by a rewrite of the surrounding view; the execute panel still shows only for `models:routing:admin` and maps 503, 402 and 429 as before. `modelsguardedtext.go` and `wire.go` now read the content firewall through an edition live source that Community leaves empty, so the Chat content stage is still the core data-loss check. A model availability read was added to `api.ts`. | Keep the claim as measured. |
| `managed-settings-delivery` (C5) | `claudepolicy.go` now serves validate and dry-run for the managed files of Claude Code, Codex, Grok Build, OpenCode and Gemini CLI, and for a department's instructions file. `claudepolicy_fleet_noenterprise.go` still answers 501 naming Business for publish, versions, artifact, check-in, distribution and inventory. `appsgateway.go` still serves one configured document to an authenticated bearer, and now answers 503 when the credential store cannot be read. Editions place assigning instructions to departments in Business. | Name the five tools in the validate and preview sentence. Add the 503 answer to the gateway condition. Do not claim department instruction files for Community. Per-host application and fleet rollout still need executed evidence. |
| `roadmap-access-map-inventory` (C6) | `module.go`, `query.go` and `reactor.go` changed: a session launch is now a permitted edge, an expired reach removes it, and the drift diff reports whether any grant was declared. `evidence.go` and `api.go` are unchanged: evidence reads still publish no route. | Keep the claim as measured. Observed and permitted stay separate inputs. |
| `roadmap-claude-code-governance` (C6) | `claudehookpep.go` and `hookpep/claudehookpep.go` changed only to read the content firewall through the live source. The decider still reads stop state first and denies when it is unreadable. `killswitch.go` and `approval_service.go` changed as described under the kill switch. `breakglass_noenterprise.go` is unchanged. | Keep the claim as measured. |
| `roadmap-mcp-governance` (C6) | `mcpadmission.go`, `entries.go`, `sign.go`, `catalogdeploy.go` and the deployment refusal are the same blobs as before. | Keep the claim as measured. |
| `roadmap-finops-cost-controls` (C6) | `budgetgate.go` and the admission files are unchanged and still reserve with the deny-on-unreachable posture. `budgets.go` now counts a group budget's spend over its members where a directory reader exists; Community has none, so a group budget there still reads zero and truncated. `edition_noenterprise.go` still answers 501 to budget creation and the new change-cost read. `CheckBudget` still has no caller outside tests. `sessiongov.go` adds fields to the launch plan hash and the `auto` permission mode; availability stays fail-closed unless set to fail-open. | Keep the claim as measured. |
| `roadmap-compliance-evidence` (C6) | `views_noenterprise.go` still empties the framework catalog and answers 501 for framework views, sealing and OSCAL export. `evidence.go` adds a ledger supplement to the JSON export only when an edition supplies one; Community supplies none. | Keep the claim as measured. |
| `roadmap-kill-switch` (C6) | `killswitch.go`: an estate stop now also cancels every pending approval a session filed, except `nhi.`, `security.` and `compliance.` actions; an agent stop is unchanged. Re-enable now spends the bound approval through the shared consumption path in the same transaction as the flip, which re-counts the two distinct approvers and keeps the unreviewed-incident block; a refusal rolls the spend back. `cmd_ui_security.go` (Security, Kill switch screen of `olivares ui`) engages an estate-wide stop behind a reason and a typed word, and `cmd_governance.go` still has no engage or re-enable command. The circuit breaker key is still refused at boot. | State the session-approval cancellation, the transaction that spends the approval, and the terminal screen that can engage a stop. The command surface is read-only only for `olivares governance killswitch`. Do not claim an executed stop or re-enable journey. |
| `roadmap-self-hosted` (C6) | `boot.go` refuses a Business-only setting at serve time and, new at H, refuses to serve when the activation manifest exists and cannot be read. `INSTALL.md` still says the Helm chart and Kubernetes operator are distributed with Business, and no operator or chart is in the source tree. `license_service.go` only adds a change notification. | Add the boot refusals to the condition. Do not qualify the deployment, upgrade or recovery matrix from code alone. |

## Reproduction and acceptance limits

`INDEX.json` lists the exact control and component-test blobs at H. Every `controlRoutes` anchor in the current manifest
names that blob. The source check reads committed objects, resolves H and the selected producer, requires ancestry, and
verifies anchors and this audit's digest. It makes no network request.

Run `python3 scripts/check-public-claims.py --producer <exact-commit>` and
`PYTHONDONTWRITEBYTECODE=1 python3 scripts/test-public-claims.py` from the product checkout. Producer P is the commit
containing this measurement, not H and not a self-referential field in this index. Source verification does not admit P
for publication; the website's independently reviewed admission is separate.

H is development source. This repository carries no release tag, so no published release artifact was measured. Every
claim remains `implemented_unaccepted`; every named acceptance job remains `implemented: false`. No claim-acceptance
test or backend, host or provider journey was executed for this measurement. Test files are source anchors, and no
null acceptance evidence or mutation record is replaced by this documentation review.
