<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Release installer trust contract

Olivares has two deliberately different installation paths. They must not be
described as if they begin at the same trust boundary.

## High-assurance path

Each release after this producer lands carries
`olivares-install-<version>.sh`. GoReleaser renders the exact release version
into that script, records its SHA-256 in `checksums.txt`, and uploads it with the
draft. The release workflow signs `checksums.txt` with cosign. An operator who
downloads the installer, `checksums.txt`, its signature and certificate can
verify the release identity and installer digest before running any installer
code. Missing cosign, a missing or duplicate checksum row, a bad signature, a
digest mismatch, or a requested version different from the embedded version is
a hard failure.

```sh
ver=YY.M.PATCH
tag=v$ver
base=https://github.com/olivaresai/olivares/releases/download/$tag
curl -fsSLO "$base/olivares-install-$ver.sh"
curl -fsSLO "$base/checksums.txt"
curl -fsSLO "$base/checksums.txt.sig"
curl -fsSLO "$base/checksums.txt.pem"
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
grep " olivares-install-$ver.sh\$" checksums.txt | sha256sum --check
sh "olivares-install-$ver.sh" --version "$tag" --dry-run
sh "olivares-install-$ver.sh" --version "$tag" --user
```

The second stage itself repeats release-identity and archive-digest verification
before installing the binary. It has no cosign bypass and never invokes sudo. A
binary-only installation remains the default; `--user` or `--system` explicitly
adds the service adapter carried inside that same verified archive.

**DIST-24-05 qualification.** The dedicated pull-request/dispatch workflow runs this
verified release path in Debian stable, Ubuntu 24.04 LTS, Fedora, openSUSE Leap and
Alpine containers and on a hosted macOS 14 runner. It checks path, mode, owner and
reported version by content. Because the public `v26.8.0` tag predates the service
adapter in this tree, each leg names and tests two subjects rather than conflating
them: the real signed public payload first, then this commit's candidate with the
opt-in service and `doctor -o json` TLS probes. No-network runs execute the dry-run
but return unmeasurable, not green. This matrix does not certify native
package-manager installation; package repositories remain a separate qualification.

**DIST-24-06 proposed repositories (phase 1 only).** Deterministic apt, rpm-md and APK
metadata producers and clean-client qualification now exist in the source tree.
**No package-repository URL is live**: there is no delegated DNS/bucket and no production
repository-signing key. The current supported surface remains the authenticated release
asset path above until those operator acts are completed.

## Convenience HTTPS bootstrap

The HTTPS bootstrap is a convenience path, not a pre-verification of its own bytes.
Its printed plan says so. It downloads the versioned second stage and verifies that
stage against the cosign-signed checksum manifest before executing it. CI, pipes and
other non-interactive callers must pass `--version`; `--dry-run` performs no download
or filesystem mutation.

The route contract is tracked in `deploy/distribution/install-endpoints.json`. Its
status is `not-live`: `/install.sh` and the `/get` redirect must not be advertised as
delegated until the public route is configured and the referenced versioned asset
exists. Delegating the domain remains a separate operator act.

## Service installation and first boot

The installer selects Linux or macOS and amd64 or arm64, then installs the binary to
an explicitly writable directory. With no service flag it stops there. `--user` and
`--system` opt into one of the adapters shipped in the signed archive:

- systemd user or system service on Linux;
- OpenRC system service on Linux;
- launchd LaunchAgent or LaunchDaemon on macOS.

`--system` requires an already privileged process; neither stage invokes `sudo`.
System mode creates a no-login service identity, a 0750 data directory, a create-only
0640 env file, and a 0644 unit (the OpenRC entry point is 0755). User mode keeps all
state below the user's XDG/home paths with a 0700 data directory and 0600 env file.
An existing data/config path with broader modes is a hard failure, and the installer
does not recursively take ownership of an existing system data directory.

`--data-dir` selects the data directory. The tuple's default (`/var/lib/olivares`,
`/Library/Application Support/Olivares`, or the XDG/home path in user mode) records
`"layout": "default"` in the manifest. Any other directory records `"layout": "custom"`
and must satisfy a shape policy rather than an allowlist: absolute and canonical, at
least two levels deep (`/srv/olivares`, never a top-level directory, because a purge
removes it as a tree), not containing the binary, config or unit, with an existing
parent that the adapter never creates under privilege, and with no symbolic link in
any component (the operator passes the resolved path; nothing is rewritten for them).
The directory is rendered into the unit, quoted when it contains a space (systemd
unquotes whole or partial words; OpenRC cannot represent it and refuses). Config and
unit paths stay inside the closed `install_layout`.

The hardened systemd unit sets `ProtectHome=true`, `PrivateTmp=true` and
`ProtectSystem=strict`. `ReadWritePaths=` re-exposes a directory under the last but
cannot undo either of the first two; `BindPaths=` can, for exactly one directory, and
that is what the adapter renders:

| data directory | rendered | effect |
| --- | --- | --- |
| anywhere else | `ReadWritePaths=<data-dir>` | writable under `ProtectSystem=strict` |
| `/home`, `/root`, `/run/user` | `ProtectHome=tmpfs` + `BindPaths=<data-dir>` | that directory is visible, every home stays hidden |
| `/tmp`, `/var/tmp` | `PrivateTmp=true` kept + `BindPaths=<data-dir>` | that directory is visible inside the private `/tmp`, the rest of the host's temporary tree stays hidden |
| `/dev`, `/proc`, `/sys` | refused | kernel and device interfaces, not durable state; the hardening replaces or read-only-mounts them |

> **Corrected on 2026-09-05.** This section previously said that a data directory under
> `/tmp` or `/var/tmp` "is refused … because no directive can reach the host's tree
> there", and both installers refused it on that ground. **The claim was false.** systemd
> commit [`a227a4be`](https://github.com/systemd/systemd/commit/a227a4be489333b1b149df124ab284d82397ff2d)
> ("namespace: if we can create the destination of bind and PrivateTmp= mounts",
> 2017-09-28) states the opposite in its own message — *"we can use namespace bind mounts
> on dirs in /tmp or /var/tmp even in conjunction with PrivateTmp="* — and the maintainer
> closed [systemd#7272](https://github.com/systemd/systemd/issues/7272#issuecomment-344038459)
> by pointing at it. An untested assumption published as an impossibility is worse than an
> unsupported option: it turned an adapter's debt into a product veto.

`BindPaths=` under `/tmp` or `/var/tmp` needs **systemd 235 or later**: that commit is an
ancestor of `v235` and is not contained in `v234` (measured against the official
repository on 2026-09-05). Both installers read `systemctl --version` on a live host and
refuse the location, naming the running version, when it is older; a staging root
(`--root`) has no manager to ask and is rendered without that check. A path that has to
be re-exposed with `BindPaths=` may not contain `:`, which that directive reads as the
separator between source and destination.

`/tmp` and `/var/tmp` are shared, world-writable directories that many distributions
clear on boot or on a timer (`systemd-tmpfiles`), which would delete the estate under a
running service. The installers say so and still render it: the location is the
operator's decision, not the adapter's veto.

**What is and is not verified.** The rendering is covered by the repository's own
hermetic batteries, which run without a service manager. A disposable Debian 13.6 /
systemd 257 runtime (`toolchains/systemd-test-runtime`) has since started a real unit
with `PrivateTmp=true`, `ProtectHome=tmpfs` and two `BindPaths=`, and observed the bound
directories readable and the host's other `/home` and `/tmp` content hidden — but with
the bind *destination* under `/tmp`, not with a bind *source* under the host's `/tmp`,
which is the direction this mapping uses. Effective access, UID/GID permissions and the
invisibility of neighbouring paths for that direction are pending a run of the reviewed
installer inside that runtime; the render does not certify them.

A reinstall (an engine upgrade through the same adapter) rewrites the manifest and
carries the AgentOps records a previous run added (`workspace_dir`, `dropin`,
`runtime-env`) the way it carries the account flags, after validating each against the
layout it renders: the workspace must be a canonical absolute path, the drop-in must be
this unit's `agentops.conf`, the runtime env must sit beside this config. Any other
previous record is refused with the manifest path named. An uninstall witness is not
carried: the rendered unit is the witness again.

The service is not started unless `--start` is present. Immediately before touching
the init manager, the adapter validates every `OLIVARES_*` key without printing its
value. First boot stays on loopback with TLS; a started service must answer both
`/livez` and `/readyz` inside a wait bounded twice — at most 60 polling attempts, and a
60-second budget of whole-second wall clock. The remaining budget is read before every
request and every sleep, and each request's 2-second connect and 5-second total limits are
clipped to what is left, so no request starts or runs past the deadline. A response that
arrives after it is refused as a late answer rather than reported healthy. That budget
comes from `date +%s` and POSIX shell has no monotonic clock, so it is subject to clock
adjustment: a backward step stalls the elapsed reading until the clock catches up, and the
real wait can then run past 60 seconds by about the size of that step. The attempt limit
and each request's own limits do not depend on the clock, and the total can exceed the
budget by curl's own shutdown granularity. When the
wait ends without both endpoints healthy, the adapter runs one further probe — bounded
separately at 5 seconds — asking the installed binary for its own `olivares readyz` verdict,
and prints that fixed local diagnosis and remedy. No HTTP response body or header is ever
printed; a probe that could not be measured instead reports the input, TLS or transport
failure that stopped it, in that error's own unfiltered words, which on loopback describe
this host's own engine. The failure leaves the service, its configuration and its data
exactly as installed. A binary without that subcommand is reported as such instead of
being guessed at. The final output names the init-specific log
that holds the one-time token and emits a JSON result. The adapter also records
`<data-dir>/install-manifest.json`, so later lifecycle work can distinguish files it
owns from operator data.

Examples:

```sh
# Per-user systemd/launchd service, installed but not started.
sh "olivares-install-$ver.sh" --version "$tag" --user

# System service: privilege is an explicit act outside the installer.
sudo sh "olivares-install-$ver.sh" --version "$tag" --system --start
```

The installer does not create an administrator, widen listeners, enable insecure TLS,
activate demo data, invent a database DSN, or remove pre-existing data.

## Removal and estate migration

Every service installation records a strict v2 ownership manifest whose paths must belong
to the `install_layout` in the signed release index. Both the installed command and the
verified shell installer expose the same three actions:

```sh
olivares uninstall --plan --data-dir /var/lib/olivares
olivares uninstall --preserve --data-dir /var/lib/olivares
olivares uninstall --purge --data-dir /var/lib/olivares --yes
sh "olivares-install-$ver.sh" --uninstall --plan --data-dir /var/lib/olivares
```

Plan lists the service, binary, unit, identity, configuration, data, logs and keys without
mutation. Preserve stops the service and removes installer-managed software while retaining
configuration, data, logs, keys and their service identity. Purge requires explicit
confirmation. The complete manifest is validated before the init manager or filesystem is
touched; any path outside the index returns 2. Package removal applies the preserve policy
while the package manager remains responsible for its own binary and unit. Its systemd hook
stops/disables the service; Alpine validates the manifest without inventing a systemd action.

A manifest with `"layout": "custom"` names a data directory outside the fixed tuples. The
manifest alone never authorizes its removal: the data directory must pass the same shape
policy the adapter applied, and a second, owner-checked witness at a fixed indexed path
must name exactly that directory before even a plan is printed. The first witness is the
unit: only its execution directive counts (`ExecStart=` with continuation lines and
systemd quoting, OpenRC `command_args=`, the launchd `ProgramArguments` program), read
by the same parser `doctor` uses; a mention in a comment, `Environment=`, `ExecStartPre=`
or any other directive never corroborates, and a unit that names several data
directories is refused as ambiguous. When preserve (or an interrupted purge) removes that
unit, it first records an uninstall witness beside the service configuration
(`olivares-uninstall-witness-<digest>.json`, root-owned, never read through a link) that
names the same data directory and unit; that record is the second witness for the later
plan or purge and is removed last by the purge. A manifest with neither witness is refused
with the two options named: re-run the signed installer to render the unit, or remove the
estate by hand. A forged manifest in an arbitrary directory therefore has no witness.
Manifests written before the field existed keep validating as default layouts.

Preserve then purge on one host works in that order. When the witness rather than a
live unit corroborates the estate, the plan keeps every closed-path software file (this
product already removed its own), and it removes the shared configuration files only
when no service definition is live at the unit path, so purging a preserved estate never
removes the unit, binary or configuration of a later installation. A purge removes the
data tree before the manifest and the manifest before the directory, so a purge that
fails inside the tree leaves the manifest and the witness for the retry.

The native AgentOps installer records two more roles and the selected workspace in the
same manifest: `dropin` (`<unit>.d/agentops.conf`, managed, removed by preserve and purge
together with its directory when empty) and `runtime-env` (`agentops.env` beside the
service config, operator content after creation: kept by preserve, removed by purge).
`workspace_dir` is disclosed and never removed by any operation; when it lies inside the
data directory, a purge reports `remove-tree` because the data tree removal covers it,
and a link there is unlinked without following it. `olivares doctor` measures the same
record as `agentops-layout`: drop-in mode and references to the recorded claude `HOME`,
token dir and workspace, runtime env mode (values never read) and the workspace directory.

Migration reuses the existing `olivares dr backup` and `dr restore` contract rather than
adding a second export format. New bundles carry a per-file SHA-256 inventory authenticated
under the operator KEK with `hmac-sha256-kek-v1`. Restore verifies it before writing and
refuses a bundle produced by a newer engine with the required minimum version in the error.
Unsigned older bundles require the explicit `--allow-legacy-unsigned` exception and must be
authenticated separately. Continuity is still proved after import; see the DR runbook.

## Local diagnostic

`olivares doctor` is the host-install diagnostic; `olivares health` remains the
remote subject-health namespace. Doctor measures the build and independent anchor
fingerprints, file modes, manifest, service account, init state, TLS live/readiness,
store status and installed licence. Strict audit verification and a signed-channel
check are opt-in with `--audit-tenant` and `--check-updates`; the latter reports the
measured installed version, available version and signed ordering verdict.

Both text and `-o json` contain key names and paths but never configuration values,
license blobs, tokens or subprocess reports. Its exit contract is: **0 healthy, 1 a
measured defect, and 2 unmeasurable** because at least one required check could not
run. Optional, unrequested checks are `not_applicable`, never silently green.

```sh
olivares doctor --mode user
olivares doctor --mode system --data-dir /var/lib/olivares -o json
```
