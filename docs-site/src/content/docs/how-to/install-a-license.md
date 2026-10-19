---
title: Install a license and move to Business
description: >-
  Where a purchased license goes, how to install it without restarting the engine,
  how to check what is installed, and the in-place Community → Business swap.
  Verification is offline Ed25519 — no network call establishes entitlement.
---

You bought a plan and received a license. This page is what to do with it: where the file
goes, how to apply it to a running engine, how to read what is installed, and — if you
bought a Business plan — how to swap the Community binary for the commercial one
without reinstalling anything.

:::note[A license is an attestation, not a runtime switch]
**It gates no feature in the software you are running.** An expired or absent license does
not turn functionality off, and no license caps user accounts — self-hosted users are
unlimited in every tier. It is a signed statement of what you are entitled to, not a key
that unlocks code already on your disk.

**A live license grants access to commercial artifacts.** Offline bundle installation (`olivares upgrade --bundle`) requires the Enterprise binary. Enterprise installs a signed Community bundle without a license; a commercial bundle requires a live license, verified offline. Community and Business support `--bundle --check` without reading a license or installing the bundle.
:::

## What you received

| You bought | What arrives | What you do with it |
|---|---|---|
| Community | nothing to install | already running — nothing on this page applies |
| Business / Enterprise, self-hosted | a **license file** and a **download token** | install the license, then swap to the commercial binary |
| Cloud | credentials for a hosted tenant | nothing to install on a host of yours |

The license is a single signed blob. Save it as a file — `customer.license`, any name — and
keep the download token from the same email: they are used at different steps and only the
license is installed.

## 1 · Install the license

```sh
olivares license install ./customer.license --data-dir /var/lib/olivares
```

The command **verifies the blob before it writes anything**, against the Ed25519 public key
embedded in your build, so a truncated copy-paste fails here rather than at the next boot.
On success it writes `<data-dir>/license.key` with mode `0600` — the canonical at-rest
license the engine reads by default.

Pass `-` instead of a path to read the blob from standard input:

```sh
pbpaste | olivares license install - --data-dir /var/lib/olivares
```

Installing over an existing license **replaces** it, atomically, and prints which one it
replaced.

### Apply it to a running engine — no restart

A running engine picks the new license up in place. Any one of these does it:

```sh
kill -HUP "$(pidof olivares)"                 # signal the running process
curl -X POST .../v1/console/runtime/reload    # the API half
```

…or the console's own reload control. Restarting also works; it is just not necessary.

### Where the engine looks, in order

If you already inject the license some other way, know that the data-dir file is the
**lowest** of four sources. The engine resolves, highest first:

1. `--license <path>` (or `LicenseFile` in the config file)
2. `OLIVARES_LICENSE_PATH=<path>`
3. `OLIVARES_LICENSE=<blob>` — the license inline in the environment
4. `<data-dir>/license.key` — what `license install` writes

`license install` **refuses** when it can see that an override outranks the file it is about
to write: installing under one would leave a file the engine never reads, and you would see
exit 0 and no change. It says which override it found, and `--force` stages the file anyway —
the legitimate case being an override you are about to remove.

:::caution[What that refusal can and cannot see]
It reads `OLIVARES_LICENSE_PATH` and `OLIVARES_LICENSE` **from its own environment**. It
cannot see a `--license` flag (or a `LicenseFile` config entry) given to an engine that is
already running as a separate process — `install` and `uninstall` do not take a `--license`
flag at all. So on a host where the service was started with an explicit path, both commands
can succeed while changing nothing the engine reads.

Run `olivares license status` after either one. It resolves by the same precedence the engine
uses and tells you which source is actually in effect, which is the question that matters.
:::

## 2 · Check what is installed

```sh
olivares license status --data-dir /var/lib/olivares
```

`status` is offline and resolves the license by the same precedence the engine uses, so it
answers the question that matters — *which license is actually in effect* — rather than
"is there a file". It reports the source it resolved, the holder, the plan and the
expiry.

Run it after every install, and after removing an override.

## 3 · Community → Business, in place

Installing a valid Business or Enterprise license on a Community build keeps that
build running. The session cockpit and the other commercial pages appear only after
you switch this installation to Business. The console's **Edition & license** view
names the installed license separately from the build. **Expires** is the paid term;
**Lease until**, when shown, is the separate provisional instance lease. Running in
Docker? Go to [Docker and Compose](#docker-and-compose) below.

With a license installed, the commercial binary is a download away. Nothing is
reinstalled and no data moves:

```sh
olivares upgrade --enterprise --token <TOKEN>
```

:::note[Business and the commercial artifact channel]
The `--enterprise` flag names the commercial artifact channel, not the edition.
The editions are Community, Business and Enterprise. Business includes Regulated Operations,
AI Runtime Security, Compliance Packs, and Identity & Scale in one subscription.
Each family can be enabled or disabled; none is sold separately.
:::

It fetches the signed commercial build for your platform, **verifies the signature
offline** — a tampered artifact aborts the upgrade with the running binary untouched — and
swaps it in atomically, keeping a backup of the previous one. Business has the same version
number as Community, so this also works when you already run that version. `--check` first if
you want to see the plan without taking it; it names the move as `edition: community -> enterprise`:

```sh
olivares upgrade --enterprise --token <TOKEN> --check
```

Restart the service, and then turn the modules on:

```sh
olivares enterprise enable <preset>     # starter | regulated | full
```

The command and the preset names are what the commercial binary accepts. The presets are
not editions and do not map one to one to the four Business capability families.
Activation is governed and audited: it shows you a diff first, and stages any module that
needs a secret or a review rather than half-enabling it. `olivares enterprise status` reports
what is active. These commands exist **only in the commercial binary** — if
`olivares enterprise` is not a command, you are still running the Community build and the
swap above has not happened yet.

:::caution[Back up before the swap]
The swap replaces a binary, not your data — but take the backup anyway, the same one
[Upgrade and roll back](/how-to/upgrade-and-rollback/) asks for. That page also covers
going back to the previous binary.
:::

### Docker and Compose

The public `docker.io/olivaresai/olivares` image is Community. In a container,
`olivares upgrade` installs the release into the data volume, not into the image, so
recreating the container starts it again. This applies the Business release when it is newer
than the image or has the same version (`--check` names the move as `edition: community -> enterprise`);
building the image yourself, as described below, is the alternative. With the license installed (section 2),
run the upgrade in the running container and restart it. The token comes from your fulfilment
email; pass it by name so it stays out of the process list:

```sh
export OLIVARES_UPGRADE_TOKEN=<TOKEN>
docker compose exec -e OLIVARES_UPGRADE_TOKEN olivares olivares upgrade --enterprise --yes
docker compose up -d --force-recreate olivares
```

The release is kept in `ota/` under the data directory (`/var/lib/olivares/ota` in the
shipped image) together with the signed manifest and artifact it was verified from. The
image binary starts that release when it is newer than the image, or the same version
built differently (a Community image of the version you applied as Business). Nothing in
the volume is trusted on its own. A start checks the manifest's signature against the OTA
key embedded in the image, checks the artifact against the digest the manifest signs,
unpacks the binary from it and rewrites the applied binary if it differs. Unpacking takes
about a second, so the proof is kept in the container's temporary directory (the shipped
Compose file mounts a `tmpfs` there), not in the volume: later commands, the Compose
health probe included, check the signature and the version and reuse the proof while the
manifest and the applied binary are exactly as proved. A recreate empties it and the next
start proves again. Without a writable temporary directory every command proves in full.
If the image is newer than the applied release, for example after you pull a new image,
the image runs and the start log says so; run the upgrade again to apply a newer release.
Evidence that does not verify is ignored with a message and the image runs.
`docker compose exec olivares olivares ...` runs the applied release too. The volume keeps
no backup of the previous binary: a start rebuilds the applied binary from its evidence, so
a backup would not roll anything back. To return to the image's binary, remove `ota/`.
This needs a build with an embedded OTA key, which every published image has, and a data
volume that allows executing files (a named volume does; on a `noexec` mount
`olivares serve` stops and says why instead of silently running the image's binary).

You can also build the image yourself instead. Download the **same version and Linux
architecture** of the Business binary through the
[licensed download portal](https://licenses.olivares.ai/portal), and verify its signature
and checksum using the download's instructions. Then layer that verified binary onto the
matching Community runtime image on your own host. In a directory containing the verified
binary named `olivares`, use this `Dockerfile`:

```dockerfile
ARG COMMUNITY_IMAGE
FROM ${COMMUNITY_IMAGE}
COPY --chmod=0755 olivares /usr/local/bin/olivares
```

Build with the digest of the Community image for that version, verified as in
[Verify a release](/how-to/verify-a-release/). The result keeps that image's non-root
user, entrypoint and ports, so the `Dockerfile` needs no `USER` line:

```sh
docker build --build-arg COMMUNITY_IMAGE=docker.io/olivaresai/olivares@sha256:<digest> \
  --tag olivares-business-local:X.Y .
```

Keep this image private and retain the commercial license and notices supplied with
the download. Do not put your license file or download token into the build context.
Set your Compose service's `image` to `olivares-business-local:X.Y`, then recreate
that service with `docker compose up -d --no-deps --force-recreate olivares`. For
standalone Docker, recreate the container with this image and the same volume and
arguments. Keep the data volume: do not use `down --volumes`.

Check **Edition & license** after the restart: the edition should read **Business**, and
the installed license remains in the data volume. Enterprise contracts use this
commercial build too; any contract-specific artifact follows its download instructions.

## Removing a license

```sh
olivares license uninstall --data-dir /var/lib/olivares --yes
```

It deletes `<data-dir>/license.key` and reports what it removed. Like `install`, it refuses
while it can see an `OLIVARES_LICENSE*` override — the file is not what is in effect, so
removing it would change nothing — and with the same blind spot: a flag passed to a
separately running engine is invisible to it. This is the offline half of the console's own
`DELETE /v1/console/license`.

Removing the license does **not** disable anything you were running. It withdraws the
attestation; the commercial binary keeps behaving as the commercial binary until you swap it
back.

## What is *not* on this page

- **Issuing licenses** (`license keygen` / `sign`) is the vendor side of the same command.
  You do not need it as a customer.
- **What each plan contains** lives in the pricing pages, not here.
- **How the model works** — why a subscription is access to artifacts rather than a switch —
  is [Open core and licensing](/explanation/open-core-and-licensing/).
- **The purchase → refund rehearsal** (sandbox fixtures, not a live card) is
  [Rehearse the commercial cycle](/how-to/rehearse-commercial-cycle/).
