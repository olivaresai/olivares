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
Take a backup before every upgrade, including the ones that look routine. The console's
**Backups** screen (`/backups`) and [Back up and restore](/how-to/backup-and-restore/)
both do it. Nothing on this page depends on your having a backup — and you will want one
anyway the one time something surprises you.
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

```sh
olivares upgrade --check --current-version 26.8.0
```

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

:::note[There is no hot patching]
A Go binary is not patched in place. "Zero downtime" here means a graceful drain and
handover, or a rolling restart — never an in-process patch. What does apply live, without a
restart, is data and configuration: sources, connectors, secrets, policy and the license.
:::

## Air-gapped installs

An air-gapped deployment never reaches an update host. Move the bundle in by whatever means
you already trust, then install from the local file — the verification is identical, because
it was never the network that was being trusted.

**Installing from a bundle needs a live license on the box.** It is checked offline, against
the license key embedded in your binary: no call is made, so this works behind the air gap.
If you have not put your license on the box yet,
[Install a license](/how-to/install-a-license/) is the page that does it.
`--check` is not gated, so you can verify a bundle before staging anything:

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --check   # verify only; no license read
olivares upgrade --bundle ./olivares-release.tar.gz --yes     # install; needs a live license
```

If your build carries no embedded release key, or you mirror releases under your own
signing key, point the command at the key you verify against:

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --pubkey @/etc/olivares/release.pub
```

See [Install air-gapped](/how-to/air-gap-install/) for how the bundle is produced and
carried across.

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

The previous binary is kept next to the one that replaced it, and the command prints the
path when it swaps. Rolling back is restoring that file and restarting the service.

Rollback is safe by design rather than by luck: every schema change ships as an additive
expand first, and its destructive contract only in a later release, so the previous
release's binary keeps working against the upgraded schema. That is what makes a rollback
"put the old binary back", not "reverse the database".

If you need to install an older release rather than restore the kept backup, the
anti-rollback guard blocks it until you say so explicitly:

```sh
olivares upgrade --force-rollback --yes
```

The override is recorded in the audit log. The minimum-version gate is **not** overridable
by it: if a manifest declares a floor your installed version is below, step through an
intermediate release rather than trying to jump.

## When it goes wrong

| Symptom | What it means | What to do |
|---|---|---|
| `--check` prints `UNKNOWN` | The installed version could not be measured, so no ordering claim is possible | Pass `--current-version` with the version you know is installed |
| `min_ver` says you are too old | The release refuses to install directly over yours | Upgrade to the named intermediate release first |
| The new binary does not start | The post-swap probe failed | It has already reverted to the backup; check the logs and report the release |
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
