<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Upgrading, rolling back, and reconfiguring

The operator runbook for moving an Olivares AI deployment between versions safely, and
for knowing which configuration changes take effect live versus needing a restart. It
covers all three self-hosted paths (Docker / Compose, native systemd packages, and the
Helm chart) and the single static binary they all share.

It assumes the secure-by-default posture from [`SECURITY-HARDENING.md`](SECURITY-HARDENING.md)
and the disaster-recovery procedure in [`DR-RUNBOOK.md`](DR-RUNBOOK.md); back up before any
upgrade.

---

## 0. The model in one paragraph

The engine is a single binary; an upgrade is a **new image/binary over the same data
directory** (the SQLite store or your Postgres, the audit signing key and the TLS
material persist). On boot the engine applies any new schema migrations itself,
idempotently, using the **expand-contract** model. An older binary refuses a core schema
version newer than it supports, including additive migrations. Take a DR backup before
upgrading; returning to an older release after a schema advance requires restoring that
pre-upgrade point (§5).

There are **two ways to move the binary forward**, both landing on that same
image/binary-over-the-same-data model: **(a)** your platform's package/image swap (Docker,
Compose, systemd package, Helm — §4), and **(b)** the self-serve **`olivares upgrade`**
command, which downloads the next signed release for your **channel**, verifies it
**offline** against the embedded OTA key, and swaps the binary atomically. Automatic
rollback covers a failed executable version probe, not database recovery (§7). The
Kubernetes/Helm path is **declarative** — you set the image and the
Business operator source rolls it — so you do not run `olivares upgrade` inside a pod; the
command is for binary/systemd/compose installs.

> **No hot-patching — ever.** A Go binary is **not** live-patched in place. An upgrade
> always installs a new binary and restarts the process. "Zero downtime" is a graceful
> **drain + handover** (§9), not an in-process patch. What *does* apply live is **data and
> configuration** — sources, connectors, secrets, policy and the license all hot-reload
> without a restart (§6); that is data/config reload, not code patching.

---

## 1. Versioning and the image coordinate

Release and container tags use bare `MAJOR.MINOR`: release <!-- release -->`0.1`<!-- /release -->, image tags <!-- release -->`:0.1`<!-- /release --> and `:latest`;
FIPS/STIG variants are Business artifacts. See [versioning](../INSTALL.md#versioning).

The official registry is **Docker Hub**:

```
docker.io/olivaresai/olivares
```

`ghcr.io/olivaresai/olivares` is the fallback and carries identical content **by digest**:
GoReleaser builds and signs on ghcr.io, then the release's `mirror-dockerhub` job copies that
exact digest to Docker Hub with `cosign copy`, signatures and attestations included.
Reach for it when Docker Hub is unreachable or its **anonymous-pull rate limit** bites —
ghcr.io does not rate-limit anonymous pulls of public images. **In production, pin by
digest** — a tag is mutable, a digest is exactly what you verified:

<!-- release -->
```sh
cosign verify docker.io/olivaresai/olivares:0.1 \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
# then resolve and use the digest you verified (same value on either registry):
crane digest docker.io/olivaresai/olivares:0.1   # -> sha256:<…>
```
<!-- /release -->

---

## 2. How schema migrations work (so you can trust the upgrade)

- **Applied automatically at boot**, idempotently: a version already recorded is skipped,
  so re-running the same image is a no-op. A transactional migration commits with its
  tracking row in one transaction; the one exception is an online `CREATE INDEX CONCURRENTLY`
  migration, which runs outside a transaction and, on mid-way failure, leaves a droppable
  `INVALID` index (and no tracking row) that a re-run retries. Either way the bookkeeping is
  never left inconsistent.
- **Expand-contract (parallel change).** An `expand` is additive and online-safe (a new
  nullable column, a new table/index, a backfill); a `contract` is the destructive cleanup
  (drop a column, `SET NOT NULL`, drop a table) and ships **a release after** the expand it
  completes. A CI linter enforces that expands are additive-only.
- **HA-safe, with a deadline you should know about.** On Postgres, schema changes run under a
  cluster-wide advisory lock, so when several replicas boot at once exactly one migrates and
  the others wait. On SQLite there is a single writer, so there is nothing to serialize.

  **A waiting replica gives up after 5 minutes and exits non-zero.** That budget is a
  constant, and it is *smaller* than what the migrating replica is allowed to take: the
  migrating one gets **10 minutes per guard unit** (not for all of them together), plus 3+2
  minutes to close the guard rollout, plus a `migrate.Apply` leg with no ceiling of its own.
  So on a large or contended database it is entirely possible — with nothing broken — for the
  leader to be inside its own limits while every other replica fails to start.

  **What you see, and what to do.** Each waiting replica logs
  `waiting for the migration coordination lock; another node is migrating`, naming the
  holder's pid, every attempt and then every tenth. If it gives up, the pod exits with an
  error naming the holder and Kubernetes restarts it — **with a fresh 5-minute budget each
  time**. That is a crash-loop that resolves itself the moment the leader finishes; it is not
  a stuck deployment, and it needs no intervention beyond patience proportional to the
  migration. Watch the leader's log to know how long that is.

  **The one case that does not resolve itself** is a leader that dies *without closing its
  TCP connection* — host loss, a power cut, a destroyed VM, or a network partition — rather
  than being killed (a kill sends FIN, PostgreSQL reaps the backend and the lock is released
  immediately). The server-side backend then survives holding the advisory lock until the
  server's own TCP keepalives reap it, which with default settings is **hours**. Every
  replica that boots meanwhile burns its 5 minutes and exits, and the log line will tell you
  `another node is migrating` — which in this case is **false**: nobody is migrating, the
  holder is a corpse. Confirm it (the named pid is not a live node of yours), then release it
  deliberately:

  ```sh
  psql "$DSN" -c "SELECT pg_terminate_backend(<pid-from-the-error>)"
  ```

  Only ever do that against a pid you have confirmed is not a running instance: terminating a
  live migrator is how two nodes end up running DDL at once, which is the exact thing this
  lock exists to prevent.

You can see exactly what a database carries — without a SQL client — with the read-only:

```sh
olivares migrate status --data-dir /var/lib/olivares          # sqlite
olivares migrate status --engine postgres --dsn "$DSN"        # postgres (file:/env: refs ok)
```

It lists every applied migration, its **phase** (`expand`/`contract`) and apply time, and
flags any that were reverted. It opens a transient read-only connection and is safe to run
against a live engine. For a deployment under systemd, run it as the service user, e.g.
`sudo -u olivares olivares migrate status --data-dir /var/lib/olivares`; in a container,
`docker exec olivares /usr/local/bin/olivares migrate status --data-dir /var/lib/olivares`.

---

## 3. Before you upgrade

> [!IMPORTANT]
> **Configuration compatibility note.** A configured-but-unreadable file or malformed JSON now aborts startup, instead of warning and silently omitting the requested control, for `OLIVARES_AGENTCORE_EXPORT_CONFIG`, `OLIVARES_AGENT_GATEWAY_CONFIG`, `OLIVARES_APPROVAL_BRIDGE_CONFIG`, `OLIVARES_AUDIT_ARCHIVE_CONFIG`, `OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG`, `OLIVARES_CLAUDE_ERASER_CONFIG`, `OLIVARES_CLAUDE_FILES_CONFIG`, `OLIVARES_DEPLOY_EXECUTOR_CONFIG`, `OLIVARES_HITL_CONFIG`, `OLIVARES_HOOK_PEP_CONFIG`, `OLIVARES_INFERENCE_PROXY_CONFIG`, `OLIVARES_NHI_ACTUATORS_CONFIG`, `OLIVARES_NOTIFY_CONFIG`, `OLIVARES_ORCH_DISPATCH_CONFIG`, `OLIVARES_PIV_CONFIG`, `OLIVARES_RATELIMIT_CONFIG`, `OLIVARES_SANDBOX_RUNTIME_CONFIG`, `OLIVARES_SOURCES_CONFIG`, `OLIVARES_VOICE_CALL_CONFIG`, and `OLIVARES_VOICE_DISPATCH_CONFIG`; invalid `OLIVARES_AUDIT_SPOOL_MAX_BYTES` likewise aborts. Unset values remain optional. The new `OLIVARES_SESSION_BUDGET_AVAILABILITY` and `OLIVARES_SESSION_CONTEXT_AVAILABILITY` controls accept `fail-open` or `fail-closed`; when unset, both are `fail-closed` in every edition (until 26.10.1<!-- release-fixed --> the community edition defaulted to `fail-open`; set it explicitly to keep that behavior), and an invalid posture resolves fail-closed.

1. **Record the current digest** so you have an exact rollback target:
   ```sh
   docker inspect --format '{{index .RepoDigests 0}}' olivares   # Docker
   # Compose: note the OLIVARES_IMAGE digest in your .env; Helm: the live image.digest value
   ```
2. **Back up.** Before upgrading, use the currently installed release to take a DR bundle
   (store snapshot + signing and sealer keys + chain tips) — see [`DR-RUNBOOK.md`](DR-RUNBOOK.md).
   Keep its passphrase separately, along with the original TLS material, any sealer key
   supplied by an environment variable, and configuration; the bundle does not replace those. `olivares upgrade` backs up the
   executable only, not the database.
   For the PostgreSQL recovery route in §5, use native `pg_dump` and `pg_restore` tools
   whose major version matches both the source and restore servers. Confirm all four
   versions before taking the backup. PostgreSQL does not guarantee that output from a
   newer `pg_dump` will load into an older server, even when the dump came from that server
   ([PostgreSQL notes](https://www.postgresql.org/docs/17/app-pgdump.html#APP-PGDUMP-NOTES)).
   Restores across PostgreSQL major versions need separate validation.
3. **Note the current migration state:** `olivares migrate status …` (above). After the
   upgrade you compare. Migration phases alone do not establish that an older binary can
   open the upgraded store (§5).

### 3.1 PostgreSQL owner/app split: the effective-privilege preflight

**This only concerns PostgreSQL deployments that run a separate owner role
(`--owner-dsn`).** Single-role PostgreSQL and SQLite are unaffected: there the application
role owns the schema it creates, so there is nothing for a later grant to miss.

In the split the OWNER creates the tables and the APP role only holds DML on them. An
upgrade that adds a relation therefore depends on the app role acquiring privileges on an
object that did not exist when the grants were written. If it does not, the old behaviour
was the worst possible one: the migration **committed** — the tracking table advanced and
the relations were created — and the deployment then failed with `SQLSTATE 42501`. Rolling
the binary back does not undo a tracking row, so the estate was left in a state neither
release could serve.

Since this release, boot measures that **before its first durable change**, inside the
migration advisory lock:

- for relations that **already exist**, it measures the app role's *effective* privileges
  on their OIDs;
- for relations this upgrade **still has to create**, the owner session creates one
  ordinary probe table inside a transaction that is **always rolled back**, and measures
  the app role's effective privileges on that fresh OID.

*Effective* is the operative word. A privilege held **directly, through a group role, or
through `PUBLIC`** all count, because all three work at runtime. `pg_default_acl` is
neither consulted nor required: it is a convenient way to provision, not the contract, so
an estate whose grants were applied by hand passes exactly as it should.

If the check fails, boot stops with `ErrPostgresUpgradePrivilegePreflight` and **nothing
has been migrated** — the trackers, relations and receipts are exactly as they were. The
message names the relations, the privileges and the OID it measured. There is deliberately
**no `--force` and no `--skip-preflight`**: a flag on `serve` would mix "I authorize schema
commits" with "I authorize serving" and leave you without an observable boundary.

> [!NOTE]
> **Why `DELETE` on `audit_spool_gaps` is asked for even with the audit spool disabled.**
> That table holds *degrade episodes*: durable records that some evidence was dropped. A
> node only ever **creates** one while `OLIVARES_AUDIT_SPOOL_MAX_BYTES` is positive and
> `on_full` is `degrade` — but the record **outlives the process**. Whichever node starts
> next has to seal it into the signed ledger as a gap marker and remove it, and that
> happens on the first ordinary write regardless of the budget, including after you have
> turned the option off. So the engine asks for `SELECT` and `DELETE` there
> unconditionally, and for `INSERT`/`UPDATE` — the ones that *record* new losses — only
> when the option is actually on.
>
> It asks rather than checks on purpose: deciding whether an episode is pending is a
> cross-tenant read of a `FORCE ROW LEVEL SECURITY` table, which the least-privilege owner
> cannot perform, and the engine will not acquire an admin credential or relax a guard in
> order to look. The standard grants in
> [`deploy/postgres/01-app-role.sql`](../deploy/postgres/01-app-role.sql) already cover
> this; you only meet it on an estate whose grants were narrowed by hand.

**Two supported ways forward.**

**A — let the grants reach future tables** (the convenient route, and what
`olivares db init --owner-role …` sets up for you):

```sh
psql "$OWNER_DSN" -v ON_ERROR_STOP=1 -c \
  'ALTER DEFAULT PRIVILEGES IN SCHEMA public
     GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO olivares_app'
# then start the service again; nothing else is needed
```

**B — separate the phases deliberately** (`migrate` → `grant` → `serve`), which is the
route for estates whose grants are provisioned by hand or by a change-controlled script:

```sh
# 1. Apply the schema and STOP. No service is opened, no leader election, no listeners.
olivares migrate apply --dsn env:DATABASE_URL --owner-dsn env:OWNER_DSN

# 2. Grant on the relations that NOW EXIST — which is the whole point of the phase.
psql "$OWNER_DSN" -v ON_ERROR_STOP=1 -c \
  'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO olivares_app'

# 3. Only now start the service.
olivares serve --engine postgres --dsn env:DATABASE_URL --owner-dsn env:OWNER_DSN
```

> [!IMPORTANT]
> **`migrate apply` exiting 0 means the schema was applied. It does NOT mean the node is
> ready.** It never reports `serving_ready`, and it deliberately does not accept
> `--admin-dsn`: the phase performs no cross-tenant read, so an admin credential is not
> merely unused — it is never opened. Step 2 is yours, and step 3 re-runs the full
> preflight against the relations that now exist.

`migrate apply` is idempotent and safe to re-run. It applies the same schema `serve` would
— including the fencing-epoch relation used by leader election, which is materialized in
this phase precisely so your step-2 grant can cover it — and it stops before every runtime
step: no epoch backfill, no audit-spool recompute, no metadata-blinding actuation, no
elector, no listeners. Every connection it opened is closed before it returns.

---

## 4. Upgrade

Pull and **verify** the new image/package first (§1), then:

**Docker (`docker run`)**
```sh
docker stop olivares && docker rm olivares      # the named data volume is NOT removed
docker run -d --name olivares -p 127.0.0.1:8443:8443 -p 127.0.0.1:8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares@sha256:<new-digest> \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```
(`--listen :8443` listens dual-stack inside the container; the `-p 127.0.0.1:…` host mapping
keeps it loopback-only on the host.)

**Docker Compose**

Keep every Compose override used for installation, including
`-f deploy/compose/docker-compose.postgres.yml` for PostgreSQL. The commands below
are for the base SQLite installation; dropping the PostgreSQL override changes
the engine's database selection.

```sh
# set OLIVARES_IMAGE to the new digest in deploy/compose/.env, then:
docker compose -f deploy/compose/docker-compose.yml pull
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
# recreates with the data volume reused and returns only after /readyz is healthy
docker compose -f deploy/compose/docker-compose.yml exec olivares olivares version
```

**Native DEB/RPM packages (systemd)**

```sh
sudo dpkg -i olivares_<new>_linux_amd64.deb      # or: sudo rpm -Uvh olivares_<new>_linux_amd64.rpm
sudo systemctl status olivares
sudo journalctl -u olivares --since '5 minutes ago'
```

Upgrading from 0.1<!-- release-fixed --> requests a restart of an active, unmasked service without
changing its enablement. A stopped service stays stopped; its next start performs
the snapshot. The appliance package-phase owner still owns its restart. Configuration,
data, and operator-edited systemd drop-ins persist. Fresh installation does not start
the service. Older packages whose removal script stops/disables the service still
print their existing recovery instruction.

If the restart fails, inspect `journalctl -u olivares` and repair the reported cause.
Retry package configuration, then run `sudo systemctl start olivares`. Configuration
retry alone does not start a service left failed or inactive by the unsuccessful
restart. The explicit start preserves the service's enablement.

Before the installed `/usr/bin/olivares serve` opens the store or applies migrations,
it consumes the package's durable request using the service's resolved database
configuration. It writes one completed snapshot per package transaction and database
under `<data-dir>/backups/pre-upgrade/<request-digest>/`: `olivares.db` for SQLite
(`VACUUM INTO`, including committed WAL data), or `dump.pgcustom` for PostgreSQL.
Directories are private and snapshot files are mode `0600`. A service restart reuses
that transaction's completed snapshot; another package upgrade retains it and creates
a new one. An installation that has never created its SQLite store has nothing to copy.

For PostgreSQL, install the matching PostgreSQL client (`pg_dump`) and configure the
service's `--admin-dsn` with a cross-tenant backup role on the same live database as
`--dsn`/`--owner-dsn` (see [DR-RUNBOOK.md](DR-RUNBOOK.md)). Secret `file:`/`env:` references
are resolved by boot, and dump credentials never enter process arguments. An explicit
`--allow-privileged-db-role` remains supported: a proven privileged application
connection may supply the dump when no admin DSN is configured. An ordinary
RLS-limited application connection is never a backup fallback.

A snapshot error, including a failure to sync the snapshot or its directory entries
to storage, blocks that boot before migration; correct the reported cause and restart
the service. A retry syncs the completed snapshot's directories again without replacing
the original database copy. Package-manager success only means the restart was requested:
verify `/readyz` and the new running version afterward. Keep sufficient disk space;
snapshots are retained until the operator removes them. They protect the local package
startup path, not concurrent migrations initiated by another binary or another host.
They contain the database only, so retain a full DR backup with the installation's
signing keys for disaster recovery. No automatic database rollback or additional
package downgrade guard is introduced; the store's existing downgrade refusal still
applies (§5). APK/OpenRC keeps its existing upgrade procedure.

**Helm (Business)**

Use the chart supplied with the Business distribution.
```sh
# from the source chart of a checkout at the release you upgrade to: the OCI chart's
# publication is unverified, so no remote chart is offered here
helm upgrade olivares ./business-chart \
  --set image.digest=<new-sha256> --reuse-values
```
On a Postgres HA release (`replicaCount>1`) this is a rolling update: the StatefulSet
replaces pods one at a time, the advisory lock serializes any migration, and the leader
election keeps a single active writer throughout.

**Then verify:**
```sh
curl -fsSk https://127.0.0.1:8443/readyz && echo OK
olivares migrate status --data-dir /var/lib/olivares    # confirm the new versions applied
```

---

## 5. Rollback — roll back the binary, not the schema

> **Login enforcement.** Once a deployment has run an artifact that carries the login
> enforcement component, rolling back to one that does not — while require-SSO or the
> login-surface IP allow-list is still configured — refuses at startup, before any listener
> is acquired. See [`LOGIN-ENFORCEMENT-OPERATIONS.md`](LOGIN-ENFORCEMENT-OPERATIONS.md) §6
> and §7 before planning that downgrade.

An older binary refuses to open a store whose core schema version exceeds its supported
version. This applies even to adjacent releases and additive migrations. Reinstalling the
old binary or image does not undo a schema advance; do not edit migration history or
bypass the refusal.

To recover after a schema advance, stop every engine using the store and preserve the
upgraded data. Use the previous release's binary to restore the DR bundle taken before
the upgrade, following [`DR-RUNBOOK.md`](DR-RUNBOOK.md):

- SQLite: use `dr restore --in-place` with `--operator` and `--reason` when restoring over
  the existing data directory. Keep the automatically preserved pre-restore files until
  recovery is confirmed.
- PostgreSQL: provision an empty restore target with `olivares db init`. In the owner/app
  split, supply the target's `--dsn`, `--owner-dsn` and `--admin-dsn` to `dr restore`,
  together with a fresh data directory for the restored signing key.

Use your private passphrase file for the restore. Require successful ledger and audit-key
verification before starting the old release.

Before restarting any PostgreSQL engine, point every runtime DSN reference (app, owner
and any admin) at the verified restored target. Update service configuration, environment files and
Compose/Helm secrets or values. Point the data-dir, volume and signing-key mappings at
the matching restored signing custody, or deliberately promote it into the original path.
Keep the original configuration backup, TLS material and sealer keys.

Once the store is compatible with the old release, redeploy the digest recorded in §3:

- Docker / Compose: recreate with the *previous* `@sha256:` digest (§4 with the old digest).
- systemd: install the previous package using the package manager's downgrade procedure,
  then restart the service.
- Helm: use `helm rollback olivares <previous-revision>` only if that revision's DSNs and
  volume/key mappings already select the restored target and signing custody. Otherwise
  use `helm upgrade … --set image.digest=<old>` with the corrected restore-target values.

After restarting, sign in and check the recovered data.

> [!IMPORTANT]
> **Recovery returns to the saved point.** Writes made after the pre-upgrade backup are
> absent from the recovered store. Preserve the upgraded store for reconciliation. A
> missing pre-upgrade bundle or passphrase cannot be replaced by the executable backup.
> A `contract` migration can remove data an older release needs, and an additive migration
> can still advance the core schema beyond that release's supported version.

### 5.1 Rolling back past the egress writer fence

The writer fence refuses any mutation that introduces or moves an event destination unless the
writer proves, in the same transaction, that it consults the egress destination control. Once it
is **armed**, a binary that predates it cannot author or re-point a subscription: its write fails
with `olivares: eventing egress writer fence: this write carries no capability attestation …`.

That is the fence working, not a fault — and it has three consequences worth knowing before you
roll back rather than after:

- **Installing the fence is never breaking.** On a deployment whose fleet predates it, the fence
  is classified DORMANT and every existing writer keeps working. Only `olivares eventing fence
  arm` closes it, and that is a deliberate act.
- **There is no disarm.** The arming ceremony has no `--mode` flag and compatibility is not a
  target: the fence exists because an un-upgraded writer can author a destination nothing
  governed, and a lever that reopens that would be shipping the hole with a switch on it.
- **So a rollback across an armed fence is a rollback of *authoring*, not of delivery.** The
  delivery rail is deliberately not fenced — an un-replaced node keeps delivering, and its
  events, deliveries and cursors keep being written — so a rolled-back node continues to serve.
  What it cannot do is create or re-point a subscription. If you need that from the older binary,
  the honest answer is to restore from a DR backup taken before the arming
  ([`DR-RUNBOOK.md`](DR-RUNBOOK.md) §9.5), not to reopen the fence.

**Order, when both controls are pending.** Arm the fence **before** actuating the destination
control. `olivares eventing egress actuate --mode enforced` refuses while the fence is dormant and
says so, because `--assert-writers-upgraded` would otherwise be recorded with nothing enforcing
it. The order is not a preference: interrupted between the two steps, "fence armed, destinations
still in compatibility" is more restrictive and authorizes nothing silently, whereas the reverse
leaves exactly the gap the fence exists to close. A fleet that genuinely cannot converge yet
proceeds with `--accept-unfenced`, and the gap is recorded with the decision.

```sh
olivares eventing fence status                     # posture, and what the database really does
olivares eventing fence arm --reason "CHG-…" --assert-writers-upgraded
olivares eventing egress actuate --mode enforced --reason "CHG-…" --assert-writers-upgraded
```

For a genuinely reversible expand during an incident, the engine's migration runner has a
`Revert` path (it runs a migration's down-statements and marks it reverted, keeping the
history). It is intentionally **not** exposed as a destructive CLI subcommand: most
migrations are forward-only by design (you cannot un-`FORCE` row-level security without a
tenant-leak window, and you do not silently drop data), and the honest rollback for those
is a binary rollback or a restore, not a schema reversal.

---

## 6. Reconfiguration: what applies live, and what requires a restart

Changing configuration is **not** the same as upgrading. Much of the runtime is
**hot-reconfigurable** — no restart, no downtime:

| Configuration | Applies live? | How |
|---|---|---|
| Observation **sources** (add / remove / edit / rotate credentials) | **Yes** | Console **Sources** tab, the runtime-reload API, or `SIGHUP` to the process |
| **Connectors** + their sealed credentials | **Yes** | Console connector onboarding (test → seal → reference → apply) |
| **Secrets** in the sealed store (`store:` references) | **Yes** | Console **Secrets**, resolved on next use |
| Active policy / PDP | **Yes** | Reloaded in place (per-tenant) |
| **Commercial license / edition entitlements** (renew, install, remove) | **Yes** | Console **Edition & license** tab, `olivares license install` / `olivares license uninstall` + reload/`SIGHUP`, or `OLIVARES_LICENSE*` + reload — see §7 |

Trigger a roster reconcile out-of-band with **`sudo systemctl reload olivares`** (the unit's
`ExecReload` sends `SIGHUP`); for a container, **`docker kill --signal=HUP olivares`**, or the authenticated
runtime-reload API. The engine logs what it added, removed, rotated and rejected, and each
reload report also states, every time, the domains it does **not** cover.

Everything below is **read once at boot** and changes only with a **process restart**. This
list mirrors the engine's own `requires_restart` report (`cmd/olivares/reconcile.go`), so it
cannot silently drift from the code:

| Configuration | Why a restart | Change it via |
|---|---|---|
| **Identity / roster providers** (`OLIVARES_SOURCES_CONFIG.identity`) | Identity wiring is built at boot | edit the sources config, restart |
| **Knowledge document sources** (`…sources.documents`) | Document ingest is wired at boot | edit the sources config, restart |
| **External connector trust policy** (`…connector_trust`) | Trust anchors are loaded at boot | edit the sources config, restart |
| **HTTP/gRPC listeners and TLS** (`--listen`, `--grpc-listen`, `tls.crt`/`tls.key`) | Sockets and TLS material are bound at boot | env file / serve flags, restart |
| **Database DSN, the event bus, the sealer (KEK) config** (`--engine`, `--dsn`, `OLIVARES_KEY_WRAP`, sealed configs) | The store, bus and key custody are opened at boot | env file, restart |
| **Data directory** (`--data-dir`) | Holds the store, keys and TLS — fixed for a process | env file, restart |

### What a restart costs (HA / SQLite)

- **SQLite single-node (default).** The process *is* the single writer (the store is pinned
  to one connection). A restart is a brief, **full availability gap** for its duration
  (seconds) — there is no second node to take over. No data is lost: the data directory
  persists across the restart.
- **Postgres active-passive HA** (`replicaCount>1` + a shared audit signing key). Restarting
  the **leader** is a fast handoff: it resigns, `/readyz` drains it, and a hot standby
  acquires leadership via the Postgres advisory-lock elector and serves. The design is
  CP-over-AP — *at most one* active writer ever, even at the cost of a few seconds of
  unavailability on a hard crash — so the signed audit hash-chain can never fork. Restart
  standbys freely; restart the leader to trigger an intentional failover.

> Live reconfiguration of sources/connectors/secrets (the table above) and live
> license/edition hot-apply (§7) are implemented. Broader hot-reload (e.g. listeners,
> identity) is not yet implemented; this table is the honest boundary until it is.

---

## 7. Editions and the in-place upgrade (community ↔ commercial build)

The edition is a function of **(a) which binary runs** and **(b) a valid commercial
license for the additive add-ons** — never a re-install or a data migration. This is the Grafana/GitLab/Elastic
model: one installation, the data directory and config untouched, the edition swapped under
it.

- **The community (default, AGPL) binary** is the complete open product. It **never reads a
  license to change behavior** — it does not gate a feature, degrade a request, or block a
  boot on a license check, and it runs air-gapped (see
  [`LICENSING.md`](../LICENSING.md)). It *does*
  install, display and hot-apply the license **artifact** (so you can stage it before the
  swap), but the only consumer of an attested claim is the closed commercial build.
- **The commercial binary** (`-tags enterprise`, Business and Enterprise) is a strict **superset** that reads the
  **same** store and config. Without a valid license it runs **identically** to community —
  the add-ons stay inactive, and since the licensing decision of 2026-07-27 there are no
  "community caps" left for it to fall
  back to (user accounts are unlimited in every edition) — so it is a safe drop-in *first*,
  license *after*.

**The upgrade community → commercial build is therefore: stop, swap the binary (same version),
start — one restart.** The license itself needs no restart (below).

### Self-serve upgrades: `olivares upgrade`

`olivares upgrade` is the self-serve OTA path for **both editions**. It moves the running
binary to the next signed release of the **same edition** on a **channel** (§8), verified
offline and swapped atomically with a kept backup. On the **public channel** the community
edition needs **no license and no token**; `--enterprise` adds the license gate and the
gated download. Installing from `--bundle` requires the Enterprise binary (§10); Community refuses before reading a license or bundle.
`--bundle --check` is never gated because it installs nothing.

```bash
olivares upgrade --check                      # community: show the plan (current -> available, CVEs), no swap
olivares upgrade                              # community: install the latest stable release
olivares upgrade --channel security           # take only security releases (§8)
olivares upgrade --enterprise --token <TOKEN> # licensed Business edition (needs a live license)
olivares upgrade --enterprise --connect --data-dir /var/lib/olivares  # connected enterprise: no pasted token
# → after any swap, restart the service to run the new binary (§9 for zero downtime)
```

- **Signed per-channel manifest (TUF-lite).** The command fetches the channel's
  `manifest.json` + a detached Ed25519 signature, verifies the signature against the
  **OTA key embedded in this binary**, then picks the artifact for this OS/arch and
  confirms its SHA-256 matches the signed manifest before executing anything. A tampered
  manifest, a tampered artifact, a wrong key, or a build with no embedded OTA key
  **aborts with the running binary untouched** — there is no "skip verification" path. For an
  air-gapped or self-signed mirror, pass `--pubkey <base64|@file>`.
- **Anti-rollback.** The updater refuses to install a version **older** than the one running
  unless you pass **`--force-rollback`**, which records an audit entry
  (`<data-dir>/upgrade-audit.log`) before proceeding. A manifest's `min_version` can also
  require you to step through an intermediate release rather than jump directly.
- **Both of those are claims about the version you are ON, so the updater refuses to guess
  it.** It learns the installed version by running `<target> version`. When that cannot
  answer — a `noexec` mount, a binary staged for another platform with `--os/--arch`, an
  install that left the file non-executable, or a **build from source**, which carries no
  version stamp — the upgrade **fails closed** and says which of those it hit. It does not
  fall back to the version of the binary running the command: that is a different binary,
  and feeding it to anti-rollback made every older release look like a step forward.
  Declare it instead with **`--current-version <version>`**, which keeps both guards armed
  (and the audit record truthful) rather than bypassing them:

  <!-- release -->
  ```bash
  olivares upgrade --target /opt/olivares/olivares --current-version 0.1
  ```
  <!-- /release -->

  Released binaries are unaffected — every published artifact is stamped at build time, so
  this only ever applies to a binary you compiled yourself or staged for another platform.
- **The swap is atomic with a kept backup.** The new binary is written beside the current
  one, **exec-probed** (`<new> version` must run) BEFORE anything is replaced, then renamed
  into place; the previous binary is kept at `<path>.bak-<ts>-<unique>`. If the installed
  binary fails its post-swap `version` probe it is **rolled back automatically**. This
  does not test engine startup or restore the database. The running process is untouched
  until you restart it. Before a later rollback, follow the data recovery procedure in §5.
- **One upgrade agent per binary, and the guards are re-checked against the file that is
  still there.** The command takes an exclusive lock on the target across the whole
  prepare → download → swap sequence, so a second concurrent run **exits `5` (Conflict)**
  and installs nothing. Run **one** timer and change `--channel` on it; do not run a timer
  per channel. This is not tidiness: the backup path was derived from the clock in whole
  seconds, so two installs finishing in the same second wrote the **same** backup file, and
  the loser's automatic rollback then restored the winner's binary and reported success.
  The lock is a `flock`, so the kernel releases it if the process dies — there is no stale
  lock to clear by hand. And because a package manager or an image rollout does **not** take
  that lock, the command re-reads the target's bytes immediately before swapping and refuses
  if they are not the ones it planned against: anti-rollback and `min_version` are claims
  about one specific installed file, and a verdict about a file that has since been replaced
  is not a verdict about anything.
- **The security boundary is the signature, not the transport** — a hostile or plain-HTTP
  endpoint cannot substitute a binary it did not sign, so the offline check is the trust
  anchor (TLS is defence in depth). Air-gapped installs use `--bundle` (§10).
- **Staged rollout.** A manifest may roll a release out to a percentage of the fleet; a node
  self-selects deterministically. A manual `olivares upgrade` proceeds regardless (explicit
  intent); the opt-in timer below respects the cohort with `--if-eligible`.
- **`--token`** (enterprise) comes from your license/fulfilment email; it authorises the
  gated download from `licenses.olivares.ai` (override with `--endpoint`). It travels in the
  `Authorization` header, **never the query string, argv or a redirect** — set it with `--token`
  or the `OLIVARES_UPGRADE_TOKEN` environment variable (the systemd timer reads the latter from an
  `EnvironmentFile`, so no download token ever appears in a unit's command line). After an
  enterprise upgrade, restart and run `olivares enterprise enable <preset>`.
- **`--download-protocol`** (enterprise) selects the gated download protocol: `release-v1` (the
  default first-party flow) resolves ONE consistent `{version, set, manifest, signature}` tuple and
  corroborates it on every request, so the manifest, its signature and the artifact are one
  release even if the channel advances mid-download (a mismatch is a named conflict, never mixed
  bytes). `legacy` keeps the existing per-request `/download` route for a custom or older gateway;
  it gains no permission to bypass the token version, the manifest signature or the artifact SHA. A
  `404` from the new route is a compatibility diagnostic, not an automatic downgrade.
- **`--connect`** (enterprise) replaces a pasted token with the deployment's own identity. Once
  `olivares license connect start` has bound the data directory (the purchase owner approves its
  key once in the customer portal, from any browser), `upgrade --enterprise --connect` first
  refreshes the credential and download token by proof of possession, then runs the unchanged
  gated download. It works when the installed credential has expired, as long as the purchase is
  current. The returned credential replaces `license.key` only after it verifies against the data
  directory's license trust and confers a current right; a refusal, a timeout or an unverifiable
  answer leaves the license, the token and the binary unchanged. `--connect` is refused together
  with `--bundle` (which stays offline) and with `--token`.

**Opt-in automatic checks (systemd timer).** Auto-update is **never on by default** — a
control plane does not change under an operator without a maintenance window. `olivares
upgrade --install-timer` emits an opt-in `systemd` service + timer that runs a rollout-aware
`upgrade --if-eligible` in a window you choose:

```bash
olivares upgrade --install-timer --channel security --timer-dir /etc/systemd/system
sudo systemctl daemon-reload && sudo systemctl enable --now olivares-upgrade.timer
```

The enterprise token, if any, is read from an `EnvironmentFile` — never inlined into the
unit. A connected unit (`--enterprise --connect`) needs neither a token nor an
`EnvironmentFile`: it pins `--data-dir`, `--connect` and any `--license` or `--pubkey` you gave,
and each run proves possession of the key stored under that data directory. It refreshes the
connected credential only when the timer fires on its `OnCalendar` schedule. It does not schedule
itself from the credential's refresh planning boundary (`olivares license connect status`,
`last.effective_until`): the earliest boundary among the credential's active signed lines, such as
an add-on's provisional lease, and not the end of every line's right. When `--timer-schedule` is
omitted, a connected unit runs daily (`*-*-* 03:00:00`, with the same random delay of up to 30 minutes
and `Persistent=true`); the Community timer and any explicit `--timer-schedule` are unchanged. A
provisional lease lasts 72 h, and its refresh is meant to happen 12 h before it ends. A daily run leaves
room for that only while the host and the service can actually run: it is a fixed cadence, not a
deadline-driven scheduler, and it promises nothing through a long outage. With a slower explicit
schedule, run `olivares license connect refresh` on its own shorter schedule. Upgrading the binary does
not rewrite a unit that is already installed; regenerate it with
`olivares upgrade --install-timer --enterprise --connect --data-dir <dir> --timer-dir /etc/systemd/system`,
then `sudo systemctl daemon-reload`. The service runs
`--if-eligible`, so a node upgrades only when it is in the manifest's rollout cohort and in the
window.

### Activation: `olivares enterprise enable <preset>` (buying turns something ON)

A commercial binary starts **byte-identical to community** — its add-ons are opt-in and
fail-inert, each gated by its own `OLIVARES_*_CONFIG`. So a fresh upgrade adds capability but
no behaviour change until you turn something on. The activation pack does that in one step,
governed and auditable:

```bash
olivares enterprise enable regulated       # shows a diff, then activates/stages the add-ons
olivares enterprise status                 # per-add-on state (active / pending / available)
olivares enterprise promote <add-on>       # activate a staged add-on after filling its config
olivares enterprise disable <preset>       # symmetric; keeps the staged config files
```

Presets are cumulative — **`starter`** (reporting, PQC posture, onboarding, threat-intel),
**`regulated`** (+ RTBF depth, retention floors, WORM archive, legal-hold, incident loop),
**`full`** (+ the content/hook firewalls, the egress / computer-use / render / elicitation
gates, credential minter, login enforcement). `enable` writes a governed **activation
manifest** (`<data-dir>/enterprise-activation.json`) and materialises each add-on's config
under `<data-dir>/enterprise-activation.d/`. **Honesty is enforced:** an add-on activates only
when its default is safe without operator input; controls that need a **secret** (WORM
archive, incident routing, credential minter) or a **policy review** (computer-use, render
inspection) are **staged** — a template you fill and `promote`, never silently
pretended-active. The full add-on→config table is generated from the catalog
(`olivares enterprise catalog`).

Activation is applied at the **next engine restart** (add-ons are wired at boot — unlike a
license, which hot-applies for term and entitlement changes). The console surfaces the same table plus a
preview-diff enable under the **Edition & license** tab (superadmin + AAL3).

### Installing / changing a license — multi-surface, hot-applied

| Surface | How |
|---|---|
| **CLI** | `olivares license install <file\|->` writes `<data-dir>/license.key` (0600, atomically) after verifying it, and names the licence it replaced; `olivares license uninstall --yes` removes it and reports what the engine resolves afterwards; `olivares license status` prints the at-rest status as JSON. Both REFUSE while a `--license`/`OLIVARES_LICENSE*` override outranks the data-dir file (`install --force` stages it anyway) |
| **Console** | **Edition & license** tab → paste or upload the blob (superadmin + AAL3 step-up) |
| **File / env** | The engine reads, in precedence order, `--license <path>` > `OLIVARES_LICENSE_PATH` > `OLIVARES_LICENSE` (inline) > `<data-dir>/license.key` |
| **Connected** | `olivares license connect start` creates the deployment's Ed25519 key under `<data-dir>/connect/` (0700; files 0600), requests the owner's approval and prints the approval URL and key fingerprint; running it again after approval completes the binding. `refresh`, `rotate-key`, `recover`, `reactivate`, `deactivate`, `status` and `abandon` follow. Every step is recorded before it is sent and repeated with the same operation id after a crash or timeout. A verified `rotate-key`, `recover` or `reactivate` is recorded before the new key replaces the old one; after an interruption, running the same command again completes it without contacting the service, and `abandon` refuses to discard it. A NEW `recover`, `reactivate` or `deactivate --owner-approval` request needs the current purchase credential with `--evidence <file>` (or `--evidence -` for stdin); the installed license is not used as that evidence, and finishing a recorded request repeats it without reading evidence again. No key, credential, token or approval reference is printed or placed in a URL or argument |
| **Trust** | `olivares license trust status\|set\|fence` manages `<data-dir>/license-trust.json`: keys added to the embedded anchor, retired to verify-only, revoked, or pinned to a `key_epoch`, plus a minimum-epoch fence. Boot, reload, both install surfaces, the enterprise upgrade and `--bundle` gates, doctor and the connected client verify through this one keyring; it reloads with the license, also when a configured license source cannot be read: the live license is then kept and verified under the current trust |

A **renewal or a fresh install applies live — zero downtime.** The console install
hot-applies immediately; a file install applies on the next `SIGHUP` /
`systemctl reload` / runtime-reload API (the same triggers that reconcile sources), or on
the next start. User accounts are never part of the entitlement: they are unlimited in every
edition (licensing decision of 2026-07-27), so no install, renewal, expiry or removal can cap them. *The license file is managed in the data dir; if a `--license`/`OLIVARES_LICENSE*`
override is set, it OUTRANKS the data-dir file and the console/CLI install is refused (the
license is managed out-of-band) — never a silently shadowed file.*

### Expiry and downgrade — graceful, never destructive

- **Expiry / invalidation.** The engine **does not crash or lose data**. It reverts to
  community behavior (commercial add-ons go read-only/off), logs a `WARN`, and the console
  shows a renewal banner. Install a renewed license to restore it — live. **Your user
  accounts are untouched**: they are unlimited in every edition, so a lapse never caps,
  disables or deletes one.
- **Downgrade-acknowledge (inert).** The `acknowledge=true` round-trip is still accepted by
  the API and the console, but nothing triggers it any more: since the 2026-07-27
  licensing decision no license entitles
  fewer user accounts, so there is no seat downgrade to confirm.

### Schema parity makes the swap safe

Both binaries register the **identical** schema — same tables, columns, indexes and
migrations, through the **same** module chain (no tag-gated fork). It is enforced in CI, and you
can check it yourself the same way CI does — diff `olivares migrate manifest` between the two
builds and expect no difference — so a binary swap in **either** direction can never land in a
partial-upgrade state.

### Rollback commercial build → community is symmetric

Roll back the **binary, not the schema** (§5). Swapping the commercial binary back to the
community one on the **same data directory** works: any rows written by the commercial build stay
**dormant, not deleted**. The community binary simply stops serving the commercial
surfaces; user accounts are unaffected in either direction (they are never capped).

---

## 8. Release channels

`olivares upgrade` follows a **channel**. Each published channel publishes a signed
`manifest.json` describing its current release; you pick one with `--channel` (default
`stable`).

| Channel | What it carries | Who it is for |
|---|---|---|
| **`stable`** (default) | Every general-availability release on the current line. | The default: current features + fixes. |
| **`security`** | Only security releases (may be published out-of-band, ahead of a feature release). | Operators who want the smallest change surface but must take security fixes fast. |

**There is no `lts` line, and this section used to say there was.** `lts` is still a value
`release.ValidChannel` accepts — the constant is declared in `core/release/manifest.go` and
`--channel lts` therefore passes validation — but no `lts` manifest is produced or published,
so following it asks an update host for an object that is not there. Security support is
**term-only** with `general_backports: false`; a
frozen-line arrangement exists only as a per-contract item on an enterprise order form, with
its anchor version and its window written into that contract, and never as a global policy
this page could state on its behalf.

What this page promised until 2026-08-16, and what was actually true:

| It said | Measured |
|---|---|
| A **12-month** support window from LTS designation | No window is implemented anywhere. It was a live delegation that the later canon withdraws. |
| `eol_at` **surfaced in the console** | `grep -rniE '\blts\b\|eol_at\|eolAt' web/src` → **0 matches**. The console shows no such date. |
| Enterprise LTS builds delivered through **"the private repo granted by your subscription"** | No such repo is granted by any subscription. This was the costly one: a buyer could cite it to demand backport builds that are not produced. |
| `eol_at` fencing the window | `core/release/manifest.go:638-640` passes a past `eol_at` through `warn(...)`, never `refuse(...)`. It is a note on a manifest, not a bound. |

`eol_at` is still carried and still printed by `--check` when a manifest sets it. Read it as
what the code makes it: a declaration, with nothing enforcing it.

**Anti-rollback across channels.** Version comparison is by semantic version, not by
channel, so switching `--channel` never downgrades silently: a lower target still requires
`--force-rollback` (audited). The `security` channel is a subset of what is also on `stable`
except during an embargoed out-of-band push, when it may lead briefly.

---

## 9. Zero-downtime restarts and upgrades

An upgrade installs a new binary; running it requires a **restart**. How much (if any)
downtime that restart costs depends on the topology:

- **HA (recommended for zero downtime).** Behind a load balancer with `replicaCount>1`
  (Postgres + a shared audit signing key), do a **rolling restart**: upgrade and restart one
  node at a time. `/readyz` drains the node being replaced and the load balancer routes
  around it, so the fleet never has an accept gap. This is the same mechanism as the Helm
  rolling update (§4) and the durable-bus HA design.

  > **On Kubernetes, mind the readiness layout.** With the leader-only readiness layout
  > (the Helm chart today, and the operator's `spec.haRouting: Legacy`) a StatefulSet
  > *rolling update* cannot finish on its own: the replaced pod comes back as a standby,
  > never becomes Ready, and never satisfies the update barrier — so drive that restart pod
  > by pod (highest ordinal first, leader last). The operator's
  > `spec.haRouting: LeaderRouting` removes the constraint: readiness becomes
  > `/pod-readyz` (pod health) and client traffic follows a leader-selecting Service, so
  > `kubectl rollout` completes unattended. See `docs/HA-LEADER-ROUTING.md`.

- **Single node, overlapping handover (`--reuse-port`).** Start the engine with
  `olivares serve --reuse-port` (Linux/BSD: it binds listeners with `SO_REUSEPORT`). To hand
  over: install the new binary (`olivares upgrade`), start a **second** process with the same
  flags — it binds the **same** ports alongside the first — confirm it is healthy, then send
  the old process `SIGTERM` (it stops accepting, finishes in-flight requests, and exits).
  Because both processes accept during the overlap, the measured **listener-wide accept gap is
  ~0** (bounded automated test: 0 refused connections across a handover, both the old and new
  server serving, far under the <5 s single-node target). This does **not** guarantee that every
  fresh TCP attempt survives listener retirement: on Linux, connections already assigned to the
  old accept queue may be reset when it closes because `tcp_migrate_req` defaults to disabled
  ([kernel documentation](https://docs.kernel.org/networking/ip-sysctl.html#tcp-migrate-req-boolean)).
  Retrying clients recover; workloads requiring zero request loss should use the HA topology
  above rather than claiming that property from `SO_REUSEPORT` alone. Without `--reuse-port` (or
  on a platform without
  `SO_REUSEPORT`) a single-node restart is a brief full-availability gap (seconds) — no data
  is lost; the data directory persists.

- **Graceful drain always.** On `SIGTERM`/`SIGINT` the engine stops accepting, finishes
  in-flight requests within the shutdown deadline, writes a final signed audit checkpoint,
  and closes the store cleanly — so an ungraceful kill is never required to restart.

Kubernetes gets this for free from the rolling StatefulSet update; `--reuse-port` is for
bare-metal/VM single-node and compose deployments that want a near-zero-gap restart.

### The rolling-upgrade window: indexed IdP home-realm routing

Home-realm discovery routes an email domain through the derived
`federation_domain_claims` index: one row per claimed domain points to a configuration, and
each domain is unique. The configuration's JSON `claimed_domains` column remains the
operator-facing, authoritative list. The table is only a routing index. It is maintained
transactionally on every configuration write and reconciled at every startup.
`ReconcileDomainClaims` runs once during boot; it reports problems but does not fail the
boot.

During a rolling upgrade, the fleet temporarily contains pre-index and post-index nodes. If
an old node writes or claims a domain, it cannot create the index row because its binary does
not know the table. Until an updated node next starts, home-realm discovery for that domain
falls back to the global login. This is deny-closed: the user is not routed to the wrong IdP;
automatic domain routing is simply unavailable. It is a safe, temporary degradation, not a
defect.

At the next boot of an updated node, reconciliation converges the index from
`claimed_domains`: it backfills missing domains, prunes orphaned or stale rows, and
places any domain claimed by more than one configuration in deny-closed quarantine until the
operator resolves the conflict. Reconciliation is idempotent and tolerates concurrent
multi-node boots.

During this window:

- Avoid creating or editing domain claims from old nodes. Do so after the rollout completes
  or from an already-updated node.
- If a domain loses automatic routing mid-rollout, this window is the cause. Restart an
  updated node, or wait for one to boot; reconciliation restores the routing.
- The fallback is always the deny-closed global login: never the wrong IdP and never a hard
  login failure.

As described in §5, the index is derived: rolling back the binary does not corrupt it, and
reconciliation at the next startup converges it again.

---

## 10. Air-gapped updates

Offline bundle installation is an Enterprise capability. Community verifies a bundle without
reading a license or installing anything:

```sh
olivares upgrade --bundle olivares-update.tar.gz --pubkey <release.pub> --check
```

Verification checks the signature, channel, freshness and version ordering and prints the
upgrade plan. An install request from a Community binary refuses even when the signed manifest
says `community` or a live license is installed. The bundle producer and installation journey
are distributed with Enterprise; see [editions](editions.md).

### Which bundles need a license

Community reads no license for a bundle. Enterprise retains offline verification of its
installed license for commercial bundles. Checking a bundle requires no license in either
edition. The online Community update channel remains available without a license.

---

## 11. The update indicator (console)

When an update endpoint is configured (`OLIVARES_UPDATE_ENDPOINT`, optionally
`OLIVARES_UPDATE_CHANNEL`), the engine runs a periodic, offline-verified check of the
configured channel's manifest and the console's **System health** tab shows whether a newer
release is available — with a **security** badge when the available release carries a
security fix. The check is read-only: it never changes the binary (that stays the operator's
explicit `olivares upgrade`).

It is **air-gap-honest**: with no endpoint configured the engine makes **no outbound calls**.
The console explains why **Check now** is unavailable and offers **Update instructions**,
which can be read without a network request. Use an official release with an embedded OTA
verification key, set `OLIVARES_UPDATE_ENDPOINT` to an approved signed channel in the service
configuration, and restart to enable checks. Leave it unset to keep checks off. A development
build without that key cannot check a signed channel. For a manual package or image upgrade,
back up, verify the replacement, and keep the same data directory (§4).

The unconfigured check-now API continues to return **501**, never an "up to date" result.
A transient configured check failure remains visible and retryable. The check verifies the manifest against
the embedded OTA key exactly as `olivares upgrade` does, so "an update is available"
identifies a verified newer release. Installation still requires the upgrade CLI's
eligibility and artifact integrity checks.

---

## 12. Uninstall

Removing the package or container **never** deletes the data directory (`/var/lib/olivares`
or the `olivares-data` volume) — it holds the append-only audit ledger and the signing key.
Remove it by hand only if you really mean to. See [`../INSTALL.md`](../INSTALL.md#upgrading--uninstalling).

---

## 13. Security updates — CRA statement

Security updates are distributed as signed releases, free of charge, and without undue
delay according to the remediation targets in [`SECURITY.md`](../SECURITY.md). They are
verifiable with [`scripts/verify-release.sh`](../scripts/verify-release.sh), shipped as
patch releases when the security fix is separable from feature upgrades, and can be
rolled back using the binary/image rollback procedure above. The CRA reporting and
support-period readiness pack is [`CRA-READINESS.md`](CRA-READINESS.md).

---

## See also

- [`DR-RUNBOOK.md`](DR-RUNBOOK.md) — backup/restore, RPO/RTO, key custody, the DR drill.
- [`SECURITY-HARDENING.md`](SECURITY-HARDENING.md) — the secure-by-default posture.
- [`../INSTALL.md`](../INSTALL.md) — the per-OS install matrix and the image coordinate.
- [`RELEASE-VERIFICATION.md`](RELEASE-VERIFICATION.md) — verifying a release (cosign / SBOM / SLSA).

## Core v10 directory User authority

[Directory User authority](DIRECTORY-USER-AUTHORITY.md) describes the required
stopped-store ceremony for staged and already enforced legacy installations,
the PostgreSQL noAdmin inventory role, exact retry and commit reconciliation.
Core v10 preserves v1–v9 history; its activation advances protocol, H coverage and
business G atomically. This foundation does not claim whole F2 readiness.

The local `./business-chart` examples refer to an extracted, verified Business chart package supplied through the Business channel. Publication is unverified here; obtain and authenticate the package using that channel’s instructions.
