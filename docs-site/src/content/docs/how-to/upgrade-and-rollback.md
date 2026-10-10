---
title: Upgrade and roll back
description: >-
  How to move a self-hosted Olivares AI deployment to a newer release — preview the
  plan, take the swap, verify it, and go back if you have to. Covers the self-serve
  `olivares upgrade` command, air-gapped bundles and the platform image swap.
---

An upgrade replaces the binary; it does not migrate you onto a different product. The data
directory, the audit signing key and the TLS material stay where they are, and the engine
applies any new schema migrations itself at boot. This page is the operator's path through
that, from "should I take this release?" to "I need the previous one back".

:::caution[Back up first]
Take a DR backup before every upgrade, including routine upgrades, using the installed
release's `dr backup`. The console's **Backups** screen (`/backups`) and
[Back up and restore](/how-to/backup-and-restore/) describe the same recovery path.
**A pre-upgrade backup and its private passphrase or KEK are required to return to the
previous release after a schema advance.** `olivares upgrade` keeps a copy of the
executable; it does not take a database backup.
:::

## Which upgrade path is yours

There are two ways to move the binary forward, and they land in the same place.

| Your install | Path |
|---|---|
| A binary on a host, systemd, Docker Compose | `olivares upgrade` — this page |
| Kubernetes / Helm | Set the image and let the operator roll it. Do not run `olivares upgrade` inside a pod: the deployment is declarative and the next reconcile would undo it. |

## Before anything: read the plan

`--check` downloads and verifies the channel manifest, compares it with what is installed,
and prints what would happen. It swaps nothing.

```sh
olivares upgrade --check
```

It answers with the installed version, the available one, and a status line that is one of
`up to date`, `upgrade available`, `DOWNGRADE (blocked unless --force-rollback)` or
`UNKNOWN`. Read the status line rather than comparing the two version numbers yourself.

**`UNKNOWN` is not "probably fine".** It means the installed version could not be measured
— a cross-architecture staging directory, a `noexec` mount, a build from source — and both
the anti-rollback guard and the minimum-version gate are claims *about* the installed
version, so neither can be evaluated. The command refuses rather than guessing. Declare the
version you know is there and the guards stay armed:

<!-- release -->
```sh
olivares upgrade --check --current-version 0.1
```
<!-- /release -->

## Release channels

<!-- BEGIN GENERATED olivares-upgrade-channels — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

`olivares upgrade` follows a release **channel**. There are **3**, and they are declared in
`core/release/manifest.go` in escalating-stability order:

| `--channel` value | Declared as |
|---|---|
| `stable` | `release.ChannelStable` |
| `security` | `release.ChannelSecurity` |
| `lts` | `release.ChannelLTS` |

A value outside this table is rejected before anything is downloaded (`release.ValidChannel`).

<!-- END GENERATED olivares-upgrade-channels -->

`stable` is the general-availability line and the default. `security` carries out-of-band
fixes and nothing else, so a deployment that follows it takes security releases without
taking feature releases.

:::caution[`lts` validates, but nothing publishes it]
The table above is generated from the channel constants the code declares, so it lists every
value `--channel` accepts — and `lts` is one. **No `lts` manifest is produced or published**,
so a deployment that follows it asks an update host for an object that is not there. Security
support is term-only without general backports, and there is no frozen line: entitlements run
for the term you paid for, with no earned fallback and no perpetual right. Pick `stable` or
`security`.
:::

Pick the channel that matches how you operate, and keep it:

```sh
olivares upgrade --channel security
```

A security release is marked as such in the manifest and `--check` prints the advisories it
fixes. If you run the security channel you receive those out of band from the GA line.

## Take the upgrade

```sh
olivares upgrade
```

What the command does, in order, and why each step is there:

1. **Downloads the channel manifest and verifies its signature offline**, against the
   Ed25519 release key embedded in the build. The trust anchor is the signature, not the
   transport. A build with no embedded key requires you to supply one with `--pubkey`;
   there is no unverified path.
2. **Refuses to go backwards.** Installing an older version than the running one is
   blocked unless you pass `--force-rollback`, which records an audit entry.
3. **Binds the artifact to the manifest's signed SHA-256** before the bytes are ever
   executed.
4. **Probes the candidate**, then swaps atomically, keeping a timestamped backup of the
   binary it replaced. If the newly installed binary does not run, it reverts to that
   backup on its own.
5. **Leaves the running process alone.** The swap changes the file on disk. The new code
   takes over when you restart the service.

Add `--yes` when you are driving it from a script and there is nobody to answer the
confirmation prompt.

The automatic rollback in step 4 tests `version`, not service startup or database
compatibility. It does not recover a store migrated at the next restart.

:::note[There is no hot patching]
A Go binary is not patched in place. "Zero downtime" here means a graceful drain and
handover, or a rolling restart — never an in-process patch. What does apply live, without a
restart, is data and configuration: sources, connectors, secrets, policy and the license.
:::

## Air-gapped installs

An air-gapped deployment never reaches an update host. Move the bundle in by whatever means
you already trust, then install from the local file — the verification is identical, because
it was never the network that was being trusted.

Offline installation requires Enterprise. Community verifies a bundle with `--bundle --check` without reading a license or installing it.

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --check
```


## Staged rollout and unattended checks

A manifest can name a staged-rollout cohort, so a release reaches a fraction of the estate
first. `--if-eligible` makes a node act only when it is in that cohort, and does nothing
otherwise:

```sh
olivares upgrade --if-eligible --yes
```

That is the form the built-in timer runs. To emit a systemd timer and service that call it
inside a maintenance window:

```sh
olivares upgrade --install-timer --timer-schedule 'Sun *-*-* 03:00:00'
```

It prints the units by default; `--timer-dir` writes them where you tell it. This is
opt-in — nothing schedules itself.

The console has the read-only half of the same information: **Settings → update status**
calls `POST /v1/console/update-check`, which runs a check against the configured channel on
demand. A deployment that is air-gapped or has no channel configured answers `501` and says
so, rather than reporting that there is no update.

## Verify the upgrade

```sh
olivares version
olivares upgrade --check
```

`--check` should now report `up to date`. Then confirm the service itself is healthy: the
console's **Health** screen (`/health`), or the engine's readiness endpoint from
[Monitor with Prometheus](/how-to/monitor-with-prometheus/).

## Rolling back

The previous executable is kept next to its replacement, and the command prints its
path. That file is an executable backup, not a recovery point for your data.

**An older binary refuses a core schema version newer than it supports**, including
additive migrations. Reinstalling the old binary or image cannot reverse a schema advance.
Do not edit migration history or bypass the refusal.

1. Stop every engine using the store and preserve the upgraded data, service configuration,
   TLS material and external sealer keys.
2. Use the **previous release's binary** to restore the DR bundle taken **before** the upgrade,
   following [Back up and restore](/how-to/backup-and-restore/). For SQLite, restore into a
   fresh data directory, or use `dr restore --in-place` with `--operator` and `--reason`
   when replacing the original directory; keep its preserved pre-restore files until recovery
   is confirmed. For PostgreSQL, provision an empty target with `olivares db init` and supply
   the target's `--dsn`, `--owner-dsn` and `--admin-dsn`, plus a fresh signing-key directory.
3. Require successful ledger and audit-key verification. Point the service's data directory,
   volumes and PostgreSQL DSN references at the restored store and matching signing custody
   before starting the previous release.
4. Sign in and check the recovered data and service health.

**Recovery returns to the saved point.** Writes after the pre-upgrade backup are absent
from the restored store; retain the upgraded store for reconciliation. Without that bundle
and its passphrase or KEK, replacing the executable cannot provide this recovery.

`--force-rollback` allows an older executable to be installed and records the override in
the audit log. It does **not** override the core schema check, restore data, or override a
manifest's minimum-version gate. If the installed version is below that floor, use an
intermediate release.

### Test the recovery path before upgrading production

Use a disposable SQLite data directory and the verified previous and candidate binaries.
Start the previous release, complete setup, sign in, and stop it. Set `PREVIOUS` and
`CANDIDATE` to those executable paths; set `DATA`, `RESTORED`, `BUNDLE` and `PASSPHRASE`
to scratch paths, with `RESTORED` initially absent and the private passphrase file outside
both data directories. Take and verify the backup **with the previous release**:

```sh
"$PREVIOUS" dr backup --engine sqlite --data-dir "$DATA" --out "$BUNDLE" --passphrase-file "$PASSPHRASE"
"$PREVIOUS" dr verify --in "$BUNDLE" --passphrase-file "$PASSPHRASE"
```

Start the candidate on `DATA`, sign in, then stop it. Start the previous release on that
same directory: if the core schema advanced beyond its ceiling, it must exit non-zero with
`core schema version newer than this binary supports: database=… binary=…`.
Then restore the saved bundle with the previous release:

```sh
"$PREVIOUS" dr restore --engine sqlite --data-dir "$RESTORED" --in "$BUNDLE" --passphrase-file "$PASSPHRASE"
```

Require exit zero and successful ledger verification; start the previous release on
`RESTORED`, sign in with the original account, and check the original audit public key and
saved data. A failed restore or sign-in is a failed recovery test. This tests recovery
from a pre-upgrade bundle, rather than only proving that a binary can execute `version`.

Measured SQLite recovery check (2026-10-08): official 26.10.1<!-- release-fixed --> created core schema 18;
a newer candidate advanced it to 27. The 26.10.1<!-- release-fixed --> binary refused the upgraded store with
exit 1 (`database=27 binary=18`). Its `dr backup`, `dr verify` and `dr restore` all exited
zero; after restoring the pre-upgrade bundle into a fresh directory, 26.10.1<!-- release-fixed --> accepted the
original account and returned the original audit public key.

## When it goes wrong

| Symptom | What it means | What to do |
|---|---|---|
| `--check` prints `UNKNOWN` | The installed version could not be measured, so no ordering claim is possible | Pass `--current-version` with the version you know is installed |
| `min_ver` says you are too old | The release refuses to install directly over yours | Upgrade to the named intermediate release first |
| The installed executable fails its post-swap `version` probe | The executable check failed | The command restores the saved executable; check the logs |
| Service startup fails after the restart, or the old binary reports a newer core schema | A service or store failure is outside the executable probe | Stop the service and follow Rolling back to restore the pre-upgrade DR bundle |
| `--install-timer` fires but nothing happens | The node is not in the staged-rollout cohort | Expected with `--if-eligible`; the cohort widens as the rollout proceeds |
| "another olivares upgrade is already installing", exit **5** | One upgrade at a time per binary. The lock is held for the whole download-and-swap sequence | Wait for the running one and re-run. If nothing is running the kernel has already released the lock, so re-run now |
| "it CHANGED while this upgrade was downloading" | Something else replaced the binary after the plan was made — a package manager, an image rollout, a config-management run | Re-run: the guards are re-evaluated against what is actually installed. If it keeps happening, two things are managing the same binary |

**One upgrade agent per binary.** `olivares upgrade` takes an exclusive lock on the target
for the whole prepare-download-swap sequence, so a second run exits `5` instead of
installing. Install **one** timer and change `--channel` on it rather than running a timer
per channel: two installs finishing in the same second used to overwrite each other's
rollback backup, and the loser's automatic rollback would then restore the *other* binary and
report success. Immediately before it swaps, the command also re-reads the target's bytes and
refuses if they are not the ones it planned against, because the anti-rollback and
minimum-version verdicts are claims about a specific installed file.

For anything else, [Troubleshooting](/how-to/troubleshooting/) is the general path, and the
console's **Logs** screen (`/logs`) streams the engine's own log.
