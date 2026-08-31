<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Contract S165 — Cloud management-plane connectors (GCP/Azure)

**Status:** stable. **Module:** `/connectors` (Apache-2.0). **Go 1.26.5** module
baseline; the workspace toolchain is Go 1.26.6.
**Consumed by:** the access map module (R/RW), the governance module (identity↔agent
resolution, NHI roster), the egress/exfiltration module and the SIEM egress.
**Depends on:** the SDK [`SourceConnector`/`Sink`, open `SignalSource`](S02-sdk-runtime-eventbus.md)
(§6), the ingestion seam + re-poll scheduler and the `connectors/aws` pattern
(IAM inventory as topology + CloudTrail management feed — **replicated, not changed**).

Two `SourceConnector` connectors close the **tri-cloud parity of the MANAGEMENT plane** that the
vision promise pledged but only AWS delivered. Each one is
a **live, read-only API client** of its cloud's org/tenant control plane, and emits exactly
the `connectors/aws` contract: **topology edges** (inventory, `mode=unknown`, `attributed`)
+ **activity edges** from the native audit feed (`identity→…api`, R/W classified). They introduce their
`SignalSource` as an open string ([SDK](S02-sdk-runtime-eventbus.md) §6) without an SDK release.

| Connector | `connectors/` | SDK name | Signals |
|---|---|---|---|
| GCP management plane | `gcp-audit` | `olivares.gcp-audit` | inventory `gcp` · activity `gcp_audit` |
| Azure management plane | `azure-activity` | `olivares.azure-activity` | inventory `azure` · activity `azure_activity` |

> **Ingestion posture — live, NOT export (unlike the service-level slivers).** The org-wide
> management plane is discovered **actively** via read-only API (just like `connectors/aws`, not like
> the service-level slivers that read an exported file): GCP via Resource Manager v3 + IAM + Cloud
> Logging `entries:list`; Azure via Resource Graph + the subscription listing + Azure Monitor
> Activity Log. Authentication is **stdlib, with no cloud SDK or new dependency**: GCP service-account
> jwt-bearer (RFC 7523) or WIF/ADC `access_token`; Azure Entra client-credentials or managed-identity
> `access_token`. The token lives only in memory, is cached until shortly before
> expiry, and is **never logged or emitted**. **No credential ⇒ offline** (Open OK, Gather no-op). Only
> the control plane: a payload, secret, key or resource property is never read; the audit
> `resourceName`/body is **not parsed** (the management edge is `identity→api`, just like
> CloudTrail LookupEvents).

---

## 1. What each connector emits

### `gcp-audit`
- **Inventory (signal `gcp`)** — Resource Manager v3 (`organizations`→`folders`→`projects`, BFS walk
  with a depth cap) + IAM `serviceAccounts.list` per project. Containment edges:
  `gcp.organization ⊳ gcp.folder`, `… ⊳ gcp.project`, `gcp.project ⊳ gcp.service_account`. `mode=unknown`,
  `confidence=attributed`, `ObservedAt`=timestamp of the pass. Metadata only: resource-names, project-ids,
  SA emails, lifecycle state. **Never** an IAM policy binding or key material.
- **Activity (signal `gcp_audit`)** — Cloud Logging `entries:list` org/project-scoped over the
  `lookback` window, filter `protoPayload.@type="…AuditLog"`. One `identity → gcp.api` edge per entry:
  `OriginRef=principalEmail`, `ResourceRef=serviceName:methodName`, `ToolRef=serviceName`,
  `ObservedAt`=timestamp of the entry.

### `azure-activity`
- **Inventory (signal `azure`)** — Resource Graph (`Resources | project id, subscriptionId`) over the
  subscriptions (explicit config or auto-listed) + the tenant→subscription mapping. Edges:
  `azure.tenant ⊳ azure.subscription`, `azure.subscription ⊳ azure.resource` (ARM id lowercased to
  converge). `mode=unknown`, `confidence=attributed`.
- **Activity (signal `azure_activity`)** — Azure Monitor management events per subscription over the
  window. One `identity → azure.api` edge per **completed** operation: `OriginRef`=caller,
  `ResourceRef=operationName` (e.g. `Microsoft.Compute/virtualMachines/write`), `ToolRef`=resource
  provider, `ObservedAt`=`eventTimestamp`.

---

## 2. R/W classification — honest, never guessed

GCP Cloud Audit Logs and Azure Activity Log **do not carry a `readOnly` flag** like CloudTrail; the
classification is by the platform's own vocabulary:

| Source | Mode rule |
|---|---|
| GCP **Admin Activity** | `write` by the definition of the log type (Google: "actions that modify config/metadata"). |
| GCP **Data Access** | `read`/`write` by the **standard verb** of the `methodName` (AIP-136: get/list/… ⇒ read; create/update/delete/set/… ⇒ write). Unrecognized verb ⇒ `unknown`. |
| GCP **System Event / Policy Denied** | **omitted**: a Google-initiated event is not a principal's action, and a denied attempt **is not an observed access**. |
| Azure **Activity Log** | `read`/`write`/`delete`(=write) verbatim from the last RBAC segment of the `operationName`. The generic `action` suffix is ambiguous (it may read or write) ⇒ `unknown`. Only `status=Succeeded` events (a `Started` is the pair's duplicate; a `Failed` changed nothing). |

Confidence: `approximate` for a principal declared in `shared_accounts` (shared account/pool);
`attributed` for the rest. The raw identity is always emitted; only the confidence drops.

---

## 3. Honest coverage matrix (clean / lossy / opaque)

The three classes (`ARCHITECTURE.md`): **clean** = resource fidelity + verbatim mode; **lossy** =
the mode or the attribution degrades; **opaque** = the audit surface cannot tell (an absent edge
is **not** proof of no-access).

| Cloud | Surface | Tier | Why |
|---|---|---|---|
| **GCP** | Resource Manager/IAM inventory | **clean** | Topology observed directly via read-only API; `attributed`. |
| **GCP** | Cloud Audit Logs · **Admin Activity** | **clean** | Always on; `write` by definition; `principalEmail` present. |
| **GCP** | Cloud Audit Logs · **Data Access** | **lossy → opaque** | **Off by default** in GCP: where it is not enabled, the feed **sees nothing** (opaque). Where it is, `read`/`write` by verb (clean), `unknown` if the verb is not standard (lossy). |
| **Azure** | Resource Graph inventory | **clean** | Cross-subscription topology observed directly; `attributed`. |
| **Azure** | Activity Log · writes/deletes | **clean** | Verbatim mode from the RBAC action; caller `objectId`/`appId` present. |
| **Azure** | Activity Log · `action` suffix | **lossy** | Ambiguous by design ⇒ `unknown` (not guessed). |
| **Azure** | data-plane **reads** | **opaque** | The Activity Log **does not record** data reads; the service plane covers them (`azure-blob-audit`/`azurekeyvault`), not this connector. |
| **Both** | shared/pool principal | clean signal, **attribution `approximate`** | The audit attributes to a credential, not to a resolved agent (the identity↔agent bridge is the governance module). |

---

## 4. Identity convergence

`OriginKind="identity"` (never `agent`): the connector emits the raw credential; resolving it to an agent
is the governance module. The **ref converges by `external_id`** in the NHI roster:
- **GCP** — `principalEmail`. For a service account it is `name@project.iam.gserviceaccount.com`, identical
  to the ref that `google-agent` emits for a non-SPIFFE SA → the roster merges them. (GCP audit logs report
  the SA email, not the SPIFFE id; the convergence is by email.)
- **Azure** — caller resolved to `objectId` (claim `objectidentifier`) when present — the same ref
  that `entra-agent` uses as key —, otherwise `appId`, otherwise the `caller` string.

---

## 5. Boundary vs service-level slivers — no double ingestion

| Plane | Connectors | Resource namespace |
|---|---|---|
| **Data** (per-resource) | `gcs-audit`, `bigquery-audit`, `gcpkms` · `azure-blob-audit`, `azurekeyvault` | `gcs.object`, `bigquery.table`, `gcp.kms.key`, `gcp.secret` · `azureblob.object`, `azure.keyvault.*` |
| **Management** (org/tenant) | **`gcp-audit`**, **`azure-activity`** | `gcp.api`, `azure.api` (+ topology `gcp.*`/`azure.*`) |

The namespaces are disjoint: a service-level KMS event (`gcpkms`, namespace `gcp.kms.key`) and a
management operation (`gcp-audit`, namespace `gcp.api`) are never the same fact.

---

## 6. Least privilege

- **`gcp-audit`** — single OAuth scope `cloud-platform.read-only`. Read-only roles:
  `roles/resourcemanager.organizationViewer` + `roles/iam.serviceAccountViewer` + `roles/logging.viewer`;
  for **Data Access** additionally `roles/logging.privateLogViewer`. Zero write calls.
- **`azure-activity`** — scope `management.azure.com/.default`. A single **Reader** role at the tenant root
  (or per subscription): covers Resource Graph, the subscription listing and the Activity Log. Zero writes.

Every listing respects `max_pages` (pagination cap); the feed respects `max_events`. The GCP folder walk
respects a depth cap. If a Resource Graph exceeds `max_pages`, raise the cap — it is a
**documented and configurable** bound, not a silent truncation.
