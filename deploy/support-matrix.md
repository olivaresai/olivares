<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# v26.10 deployment support matrix

**Record date: 2026-09-27. Release candidate qualification is pending.**
This is the canonical deployment qualification and support-status record for
v26.10. It supersedes support classifications in the
[SLO guide](../docs/17-PRODUCTION-READINESS-SLO.md) and
[sizing guide](../docs/SIZING-AND-CAPACITY.md), while retaining their dated workload
observations, metric definitions and operating procedures. It makes no new SLA.

## Status and claim types

- **Supported:** the exact release/deployment tuple has an accepted qualification
  record covering its declared operations, failures and recovery, with stated
  support limits. It does not imply support for an unrecorded tuple or an SLA.
- **Preview:** the exact tuple has measured evidence and explicitly bounded
  operations and limits. Missing qualification evidence alone is not preview.
- **Unsupported:** the named combination is outside the declared support boundary;
  its configuration or apparent ability to start does not create support.
- **Not-yet-qualified:** intended coverage whose complete release qualification
  has not been recorded here. It is work due, not permission to omit required scope.

Keep three fields separate: **target** (intended behavior or objective),
**observation** (what an identified artifact/workload actually demonstrated), and
**customer commitment** (an explicitly agreed supported scope or service obligation).
A benchmark, a passing unit test, an installation recipe or a numerical SLO/RPO/RTO
target does not become a customer commitment by being copied into a release document.

## Profile summary

Profile names describe deployment views; they do not rename installed configuration
keys or installer choices. Single-node production remains an intended first-class
topology when qualified; HA is not required for every production installation.
Community and Enterprise compositions, servers, desktops, clusters, and local and
provider AI retain their required qualification work. Hosted Cloud activation
remains separately deferred; this record does not change that decision.

| Profile | Intended composition and qualification focus | Status | Complete v26.10 qualification record |
| --- | --- | --- | --- |
| P-local | One local engine, embedded console, SQLite and local official tools; first use, filesystem rights, bounded resources and restore | Not-yet-qualified | Not recorded |
| P-team | Shared engine, authenticated remote clients, qualified SQLite or PostgreSQL placement; identity/scope isolation, TLS/proxy and concurrency | Not-yet-qualified | Not recorded |
| P-enterprise | PostgreSQL, enterprise identity/operations and the entitled Module set; split duties, secrets, migration, retention and paid journeys | Not-yet-qualified | Not recorded |
| P-HA | Multi-instance placement and database/coordination services; write/effect ownership, fences, failover, split-brain refusal and sessions after node loss | Not-yet-qualified | Not recorded |
| P-isolated | Restricted or disconnected self-hosted environment; offline artifacts/trust, dependency closure, backup/key rebuild and synchronization limits | Not-yet-qualified | Not recorded |
| P-hybrid | Authorized sources and actuators across local/cloud networks; enrollment, instance identity, egress and reconciliation after partitions | Not-yet-qualified | Not recorded |
| P-cloud | Customer portal, Cloud control plane, scoped tenants and commercial services; customer isolation, privileges and provisioning recovery | Not-yet-qualified | Not recorded; hosted activation deferred |

These statuses describe the complete records on this page, not an assertion that
all earlier tests failed or that no partial implementation exists. Each supported
or preview row must cite an accepted qualification record below. A profile cannot
be promoted by changing its status cell while its evidence remains unrecorded.

## Installation targets are not qualified deployments

The [installation guide](../INSTALL.md) and release configuration describe source
and artifact targets. They do not supply all fields of a deployment qualification.

| Surface | Recorded source boundary | Qualification still needed for the exact candidate |
| --- | --- | --- |
| Linux server/desktop | Release configuration targets `linux/amd64` and `linux/arm64`; installation paths include binary, deb/rpm/apk and services | Distribution/version, artifact, package lifecycle, init/service privileges, workload, upgrade and recovery |
| macOS | Release configuration targets `darwin/amd64` and `darwin/arm64`; the installation guide states that binaries are not Apple-notarized | Actual OS/artifact execution, install/update and CLI/service behavior; do not infer notarization |
| Windows | Native Windows release artifacts are unsupported by the current build configuration; documented routes use Linux containers or WSL2 | The actual Windows/WSL2 or container host and Linux guest tuple; these routes do not qualify a native Windows service |
| Containers and Kubernetes | Linux container and Helm/operator configuration exist; SQLite placement is single-node, PostgreSQL permits active-passive configuration | Image/chart digests, architecture, runtime/cluster/storage versions, persistence, identities, proxy, upgrade, faults and recovery |
| Appliance images | Debian 13 is the intended appliance base; the server amd64 image is a separate qualification target | Actual guest package/boot inventory, hardware, install/update/rollback and recovery; this does not qualify workstation, arm64 or another distribution's appliance |
| Local/provider AI | Installing a runtime or CLI and choosing an OS does not qualify a model or provider operation | Runtime/tool and Adapter versions, model identity, protocol/authentication, operation, streaming/cancellation/resume limits, CPU/GPU/driver/memory tuple and observed effects |

Choosing Debian for an appliance does not restrict the product's Linux installation
coverage to Debian. A package test in a container is partial evidence; it does not
establish a real host's service-manager behavior. A host or cluster test is also
partial unless it covers the recorded profile, workload and recovery contract.

## Qualification record format

Create one record per qualified tuple under the heading
`### Qualification record: <id>`.
Use **not recorded** for any unobserved field; do not inherit it from another record.
A preview may name explicit measured restrictions, but must still bind its tuple
and evidence. A supported record must cover all operations it declares supported.

| Field | Required contents |
| --- | --- |
| Identity and verdict | Record ID, release/artifact digests, composition revision, profile, supported/preview status, acceptance date and accountable support function |
| Platform | OS/distribution and version, architecture, kernel/init where relevant, hardware or VM identity; container/cluster runtime and versions when used |
| Database and state | Database version/mode, roles, storage/filesystem and durability settings, replication/coordination, schema and reader/writer compatibility |
| Network and authority | Reverse proxy/version and full TLS/forwarding chain; service identities, filesystem/egress rights, authentication and tenant/context isolation |
| Composition | Core and exact entitled Module versions; no inference from another edition's test |
| Provider capability tuple | Provider, model/version or artifact digest, Adapter/tool/runtime version, protocol, authentication mode and authorized operation; observed streaming, cancellation and resume/reconciliation limits |
| Resource envelope | Measured limits/usage for CPU, memory, GPU/driver where used, disk/headroom, connections, goroutines, subprocesses, queues, request/row/upload bytes and retention |
| Tested workload | Dataset/data classes, tenants, concurrency, durations, operation mix and external dependencies; identify fixtures and real provider/tool operations separately |
| Operation and recovery | Install/update/uninstall and compatible rollback procedures; readiness and liveness; faults, ownership/fences, backup/key custody, restored authority and post-restore effect reconciliation |
| Target | Intended capacity/latency/availability and RPO/RTO, with units, time window, numerator/denominator and specified usable recovery state |
| Observation | Timestamp/clock and method, actual workload and injected fault, observed capacity/loss/recovery/effects, failures and remaining uncertainty |
| Customer commitment and evidence | Explicit agreed scope/limits, or not recorded; reproducible commands, retained artifact/results and independent acceptance bound to this tuple. Publish only evidence appropriate for the public record, without credentials or private payloads |

No complete v26.10 tuple is entered in this record yet. Candidate evidence must be
bound and reviewed before promotion; this statement does not discard the partial
observations below.

## Retained observations and their limits

| Observation | Provenance and measured scope | What it does not establish |
| --- | --- | --- |
| SQLite write paths and store-free bus, 2026-06-09 | [Sizing §2](../docs/SIZING-AND-CAPACITY.md): Ryzen 7 5800U, 32 GiB, Go 1.26.4, GOMAXPROCS 12, SQLite WAL on tmpfs; tables retain the measured rates and latencies | Durable-storage performance, complete ingest workload or current-release per-node capacity |
| Durable-disk comparison, 2026-06-12 | [Sizing §2-bis](../docs/SIZING-AND-CAPACITY.md): same reported hardware, ext4/NVMe and PostgreSQL 17.10 as recorded; write concurrency sampled at 1, 4 and 16 | Every PostgreSQL version/storage class, an unsampled two-writer crossover or HA recovery |
| Decision/retrieval/storage workloads, 2026-07-15 | [Sizing §2-ter](../docs/SIZING-AND-CAPACITY.md): recorded reference hardware; decision/retrieval store paths use SQLite `:memory:`; authorization uses declared deterministic fixtures | Complete provider dispatch, streaming, settlement or arbitrary tenant/model workloads; storage growth is its separate recorded on-disk measurement |
| SQLite restore drill, 2026-07-15 | [DR runbook](../docs/DR-RUNBOOK.md) and [day-2 log](../docs/DAY2-DRILL-LOG.md): reference build container, Linux 7.0.14-arch1-1, Go 1.26.4, 16 vCPU; 500–20,000 events, 123 ms–1.257 s for restore + boot + verify | Complete isolated rebuild, operational key custody, RPO for declared data classes or reconciliation of later external effects |
| PostgreSQL restore coverage described by the runbook | [DR limits](../docs/DR-RUNBOOK.md): PostgreSQL 16 logical backup/restore and role postures are reported separately from skipped legs and untested PITR/offsite/HA paths | A current release tuple, other PostgreSQL versions, successful PITR replay or a multi-node recovery result |

Historical provenance is preserved as reported, not remeasured for v26.10 here.
Unrecorded artifact revisions, exact versions or resource fields stay unrecorded.
The SLO and RPO/RTO numbers in the companion guides remain targets unless a separate
qualified record and explicit customer commitment state otherwise.

## Operating and recovery boundaries

SQLite files are not a shared multi-node writer protocol. PostgreSQL connectivity,
leader election and routing do not by themselves qualify HA. The
[HA runbook](../docs/HA-LEADER-ROUTING.md) describes routing and test scope; the
qualification must separately establish the actual write/effect fences and refusal
of stale owners under node, database and network faults. Source-level fencing on
one path must not be generalized to every mutation or external effect.

A lost OS process or PTY is not transparently migrated. Logical run recovery,
provider-supported resume and reconciliation of uncertain effects are separate
operations. Do not blindly redispatch work whose external outcome is unknown.

Readiness differs from liveness. The engine's documented `/readyz` checks include
store, leadership and setup prerequisites; it is not evidence that every Module,
secret resolver, bus or provider dependency is ready. Qualify required dependencies
for the actual composition. TLS and trusted forwarding must match the declared
proxy chain; network placement never replaces authorization.

A restore qualification starts in a new environment with compatible artifacts,
configuration/schema and Module versions, consistent state, required evidence and
recoverable keys under their custody procedure. It must restore authority without
broadening grants, demonstrate a usable operation, and reconcile effects after the
restore point. Record data classes, observed loss, workload, fault, clock, RPO/RTO
and uncertainty. Backup creation, failover and restoration are separate tests;
a checksum or successful database dump alone does not qualify recovery. Follow the
[DR procedures](../docs/DR-RUNBOOK.md) without copying secret values into a general
manifest. A failed post-upgrade readiness check must use the declared compatible
recovery path; exit zero alone is not an upgrade qualification.
