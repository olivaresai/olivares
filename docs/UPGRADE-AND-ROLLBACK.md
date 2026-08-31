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
idempotently, using the online **expand-contract** model — so a routine upgrade needs no
maintenance window and no manual SQL. Because every schema change ships as an *additive*
expand first and its *destructive* contract only in a **later** release, the previous
release's binary keeps working against the upgraded schema — which is what makes rollback
"redeploy the previous image", not "reverse the database".

There are **two ways to move the binary forward**, both landing on that same
image/binary-over-the-same-data model: **(a)** your platform's package/image swap (Docker,
Compose, systemd package, Helm — §4), and **(b)** the self-serve **`olivares upgrade`**
command, which downloads the next signed release for your **channel**, verifies it
**offline** against the embedded OTA key, and swaps the binary atomically with automatic
rollback (§7). The Kubernetes/Helm path is **declarative** — you set the image and the
operator/StatefulSet rolls it — so you do not run `olivares upgrade` inside a pod; the
command is for binary/systemd/compose installs.

> **No hot-patching — ever.** A Go binary is **not** live-patched in place. An upgrade
> always installs a new binary and restarts the process. "Zero downtime" is a graceful
> **drain + handover** (§9), not an in-process patch. What *does* apply live is **data and
> configuration** — sources, connectors, secrets, policy and the license all hot-reload
> without a restart (§6); that is data/config reload, not code patching.

---

## 1. Versioning and the image coordinate

Releases use CalVer (`vYY.M.PATCH`, e.g. `v26.8.0`); container tags drop the leading `v`
(`:26.8.0`, `:latest`, `:26.8.0-fips`, `:26.8.0-stig`). See [`../INSTALL.md`](../INSTALL.md#versioning).

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

```sh
cosign verify docker.io/olivaresai/olivares:26.8.0 \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
# then resolve and use the digest you verified (same value on either registry):
crane digest docker.io/olivaresai/olivares:26.8.0   # -> sha256:<…>
```

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
> **Configuration compatibility note.** A configured-but-unreadable file or malformed JSON now aborts startup, instead of warning and silently omitting the requested control, for `OLIVARES_AGENTCORE_EXPORT_CONFIG`, `OLIVARES_AGENT_GATEWAY_CONFIG`, `OLIVARES_APPROVAL_BRIDGE_CONFIG`, `OLIVARES_AUDIT_ARCHIVE_CONFIG`, `OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG`, `OLIVARES_CLAUDE_ERASER_CONFIG`, `OLIVARES_CLAUDE_FILES_CONFIG`, `OLIVARES_DEPLOY_EXECUTOR_CONFIG`, `OLIVARES_HITL_CONFIG`, `OLIVARES_HOOK_PEP_CONFIG`, `OLIVARES_INFERENCE_PROXY_CONFIG`, `OLIVARES_NHI_ACTUATORS_CONFIG`, `OLIVARES_NOTIFY_CONFIG`, `OLIVARES_ORCH_DISPATCH_CONFIG`, `OLIVARES_PIV_CONFIG`, `OLIVARES_RATELIMIT_CONFIG`, `OLIVARES_SANDBOX_RUNTIME_CONFIG`, `OLIVARES_SOURCES_CONFIG`, `OLIVARES_VOICE_CALL_CONFIG`, and `OLIVARES_VOICE_DISPATCH_CONFIG`; invalid `OLIVARES_AUDIT_SPOOL_MAX_BYTES` likewise aborts. Unset values remain optional. The new `OLIVARES_SESSION_BUDGET_AVAILABILITY` and `OLIVARES_SESSION_CONTEXT_AVAILABILITY` controls accept `fail-open` or `fail-closed`; when unset, both default to `fail-open` in the community edition and `fail-closed` in the enterprise edition (an invalid posture resolves fail-closed).

1. **Record the current digest** so you have an exact rollback target:
   ```sh
   docker inspect --format '{{index .RepoDigests 0}}' olivares   # Docker
   # Compose: note the OLIVARES_IMAGE digest in your .env; Helm: the live image.digest value
   ```
2. **Back up.** Take a DR bundle (store snapshot + signing keys + chain tips) — see
   [`DR-RUNBOOK.md`](DR-RUNBOOK.md). A backup is the only thing that makes a *data* problem
   recoverable; rollback alone is not.
3. **Note the current migration state:** `olivares migrate status …` (above). After the
   upgrade you compare, and the new rows' phases tell you whether a later rollback is safe
   (§5).

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
```sh
# set OLIVARES_IMAGE to the new digest in deploy/compose/.env, then:
docker compose -f deploy/compose/docker-compose.yml up -d   # recreates with the data volume reused
```

**Native packages (systemd)**
```sh
sudo dpkg -i olivares_<new>_linux_amd64.deb      # or: rpm -U … / apk add …
sudo systemctl restart olivares                  # /etc/olivares/olivares.env and /var/lib/olivares persist
```

**Helm**
```sh
helm upgrade olivares oci://ghcr.io/olivaresai/charts/olivares \
  --version <chart-version> --verify \
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

The expand-contract discipline means **the previous release's binary runs against the
upgraded schema** (every destructive change ships a release *after* the additive one it
finishes). So the rollback is simply: **redeploy the digest you recorded in §3.** The data
volume and the (forward-only) schema stay exactly as they are; you do not reverse the
database.

- Docker / Compose: recreate with the *previous* `@sha256:` digest (§4 with the old digest).
- systemd: `dpkg -i` / `rpm -U` / `apk add` the *previous* package, then `systemctl restart`.
- Helm: `helm rollback olivares <previous-revision>` (or `helm upgrade … --set image.digest=<old>`).

> [!IMPORTANT]
> **The one unsafe case: rolling back *across* a `contract` migration.** A contract removes
> something (a column, a table, a constraint), so a binary from *before* that contract
> shipped may depend on what it removed. Before rolling back more than one release, run
> `olivares migrate status` and check the **phase** of the migrations applied by the
> version you are leaving: if any are `contract`, do **not** roll the binary back past them
> — restore from a DR backup taken before the upgrade instead ([`DR-RUNBOOK.md`](DR-RUNBOOK.md)).
> Routine same-release and adjacent-release rollbacks (expands only) are safe.

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
`ExecReload` sends `SIGHUP`); for a container, **`docker kill --signal=HUP olivares`** (the
distroless image has no shell, so `kill` inside it is not an option), or the authenticated
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

## 7. Editions and the in-place upgrade (community ↔ enterprise)

The edition is a function of **(a) which binary runs** and **(b) a valid commercial
license for the additive add-ons** — never a re-install or a data migration. This is the Grafana/GitLab/Elastic
model: one installation, the data directory and config untouched, the edition swapped under
it.

- **The community (default, AGPL) binary** is the complete open product. It **never reads a
  license to change behavior** — it does not gate a feature, degrade a request, or block a
  boot on a license check, and it runs air-gapped (ADR-0010,
  `docs/adr/0010-license-attestation-only.md`; `LICENSING.md`). It *does*
  install, display and hot-apply the license **artifact** (so you can stage it before the
  swap), but the only consumer of an attested claim is the closed enterprise build.
- **The enterprise binary** (`-tags enterprise`) is a strict **superset** that reads the
  **same** store and config. Without a valid license it runs **identically** to community —
  the add-ons stay inactive, and since the licensing decision of 2026-07-27 there are no
  "community caps" left for it to fall
  back to (user accounts are unlimited in every edition) — so it is a safe drop-in *first*,
  license *after*.

**The upgrade community → enterprise is therefore: stop, swap the binary (same version),
start — one restart.** The license itself needs no restart (below).

### Self-serve upgrades: `olivares upgrade`

`olivares upgrade` is the self-serve OTA path for **both editions**. It moves the running
binary to the next signed release of the **same edition** on a **channel** (§8), verified
offline and swapped atomically with a kept backup. On the **public channel** the community
edition needs **no license and no token**; `--enterprise` adds the license gate and the
gated download, and **installing from `--bundle` is license-gated too** (§10 — it is a route
by which the same bytes arrive, and a signed bundle does not say which edition it carries;
`--bundle --check` stays ungated because it installs nothing).

```bash
olivares upgrade --check                      # community: show the plan (current -> available, CVEs), no swap
olivares upgrade                              # community: install the latest stable release
olivares upgrade --channel security           # take only security releases (§8)
olivares upgrade --enterprise --token <TOKEN> # licensed enterprise superset (needs a live license)
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

  ```bash
  olivares upgrade --target /opt/olivares/olivares --current-version 26.8.0
  ```

  Released binaries are unaffected — every published artifact is stamped at build time, so
  this only ever applies to a binary you compiled yourself or staged for another platform.
- **The swap is atomic with a kept backup.** The new binary is written beside the current
  one, **exec-probed** (`<new> version` must run) BEFORE anything is replaced, then renamed
  into place; the previous binary is kept at `<path>.bak-<ts>-<unique>`. If the installed
  binary fails its post-swap probe it is **rolled back automatically**. The running process is
  untouched until you restart it; manual rollback is `mv <path>.bak-* <path>` (symmetric, §5).
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
  gated download from `licenses.olivares.ai` (override with `--endpoint`). After an
  enterprise upgrade, restart and run `olivares enterprise enable <preset>`.

**Opt-in automatic checks (systemd timer).** Auto-update is **never on by default** — a
control plane does not change under an operator without a maintenance window. `olivares
upgrade --install-timer` emits an opt-in `systemd` service + timer that runs a rollout-aware
`upgrade --if-eligible` in a window you choose:

```bash
olivares upgrade --install-timer --channel security --timer-dir /etc/systemd/system
sudo systemctl daemon-reload && sudo systemctl enable --now olivares-upgrade.timer
```

The enterprise token, if any, is read from an `EnvironmentFile` — never inlined into the
unit. The service runs `--if-eligible`, so a node upgrades only when it is in the manifest's
rollout cohort and in the window.

### Activation: `olivares enterprise enable <preset>` (buying turns something ON)

An enterprise binary starts **byte-identical to community** — its add-ons are opt-in and
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

A **renewal or a fresh install applies live — zero downtime.** The console install
hot-applies immediately; a file install applies on the next `SIGHUP` /
`systemctl reload` / runtime-reload API (the same triggers that reconcile sources), or on
the next start. User accounts are never part of the entitlement: they are unlimited in every
edition (licensing decision of 2026-07-27), so no install, renewal, expiry or removal can cap them. *The license file is managed in the data dir; if a `--license`/`OLIVARES_LICENSE*`
override is set, it OUTRANKS the data-dir file and the console/CLI install is refused (the
license is managed out-of-band) — never a silently shadowed file.*

### Expiry and downgrade — graceful, never destructive

- **Expiry / invalidation.** The engine **does not crash or lose data**. It reverts to
  community behavior (enterprise add-ons go read-only/off), logs a `WARN`, and the console
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

### Rollback enterprise → community is symmetric

Roll back the **binary, not the schema** (§5). Swapping the enterprise binary back to the
community one on the **same data directory** works: any enterprise-written rows stay
**dormant, not deleted**. The community binary simply stops serving the enterprise
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

An air-gapped deployment upgrades from a **local bundle**, verified **offline** — no
network, no cosign, no Rekor. On a connected host, build the signed bundle for a release:

```sh
scripts/export-update-bundle.sh --dir <release-dir> --channel stable --version 26.8.0 \
  --sign-key <dedicated-ed25519-ota-key> --out olivares-update-26.8.0.tar.gz
```

The bundle is a tarball of the signed `manifest.json`, its signature, and the platform
archives it lists. Move it across the air gap. **Installing needs a live license present on
the box** — `--check` does not — so stage the license first (it is a file; it crosses the air
gap the way the bundle does):

```sh
olivares upgrade --bundle olivares-update-26.8.0.tar.gz --pubkey <release.pub> --check
olivares license install ./license.key   # once. Verified OFFLINE, against the license key
                                         # embedded in this binary — no call is made
olivares upgrade --bundle olivares-update-26.8.0.tar.gz --pubkey <release.pub> --yes
```

`--bundle` runs the identical verify → anti-rollback → SHA-bind → atomic-swap path as the
online upgrade, but reads only from the bundle — so an air-gapped install verifies
byte-identically to a connected one. This is coherent with
[`RELEASE-VERIFICATION.md`](RELEASE-VERIFICATION.md) and the DDIL store-and-forward posture.

### Why installing from a bundle is license-gated, and what that costs

A bundle is a tarball, and tarballs travel. Until 2026-08-17 this route returned its source
**before** the license check ran, so holding a release tarball was enough to install from it
— no credential, no token, no network — while `--help` advertised exactly that ("100%
offline"). The gate was not weak here; it was not here.

The check is **offline and local**: the installed license is read at rest and verified
against the license key embedded in the running binary. There is **no registry call and no
entitlement lookup**, because requiring one would contradict the air gap this route exists
for — "air-gapped" and "gated" have to be able to hold at the same time.

It applies to **every** bundle, not only an enterprise one, and that edge is deliberately
blunt: nothing authenticated inside a bundle says which edition its artifact is (the signed
manifest carries no edition field, and the artifact's shape is a heuristic rather than a
signed claim), so the route cannot tell a community bundle from an enterprise one and
refuses closed for both. **What that costs today:** an operator with no license has no
offline route for a community **install** — the public channel (§8) needs a reachable
endpoint. When the signed manifest carries the edition, this gate can narrow to what a
bundle declares; it cannot narrow on a guess.

**`--check` is not gated**, because what is closed is an unlicensed *install* and `--check`
installs nothing — it verifies the signature, the channel, the freshness and the version
ordering, prints the plan and stops. It also hands the holder of a leaked tarball nothing
they do not already have: they hold the bytes. So checking a bundle stays free, on purpose:
a gate that refused `--check` would push you to run `--yes` blind, which is worse than
whatever it protected.

---

## 11. The update indicator (console)

When an update endpoint is configured (`OLIVARES_UPDATE_ENDPOINT`, optionally
`OLIVARES_UPDATE_CHANNEL`), the engine runs a periodic, offline-verified check of the
configured channel's manifest and the console's **System health** tab shows whether a newer
release is available — with a **security** badge when the available release carries a
security fix. The check is read-only: it never changes the binary (that stays the operator's
explicit `olivares upgrade`).

It is **air-gap-honest**: with no endpoint configured the engine makes **no outbound calls**
and the console shows **no indicator** — silence, never an error. A transient check failure
is captured and surfaced quietly, never as a crash. The check verifies the manifest against
the embedded OTA key exactly as `olivares upgrade` does, so "an update is available"
means the same signed, anti-rollback-aware decision.

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
