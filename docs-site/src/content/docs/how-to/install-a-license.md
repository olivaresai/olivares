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

**What it does gate is ACCESS TO ARTIFACTS**, and that distinction is the whole model: a
live license is required to download the commercial build and to install from a local
bundle (`olivares upgrade --bundle`), checked offline against the key embedded in your
binary. That is why the commercial build is a different binary you fetch with a token
rather than a flag flipped in the one you have — and why "it gates nothing" would be the
wrong thing to tell you.
:::

## What you received

| You bought | What arrives | What you do with it |
|---|---|---|
| Community | nothing to install | already running — nothing on this page applies |
| Business / Business Max, self-hosted | a **license file** and a **download token** | install the license, then swap to the enterprise binary |
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

With a license installed, the commercial binary is a download away. Nothing is
reinstalled and no data moves:

```sh
olivares upgrade --enterprise --token <TOKEN>
```

:::note[Why the flag says `--enterprise` when the edition is Business]
The flag names the **artifact channel** — the gated download — not the edition you bought.
It is `--enterprise` in the binary you already have (`cmd/olivares/cmd_upgrade.go`), so this
page prints it exactly as you must type it. The editions themselves are Community and
Business, with the four business add-ons named in [`LICENSING.md`](https://olivares.ai/pricing):
Regulated Operations, AI Runtime Security, Compliance Packs, and Identity & Scale.
:::

It fetches the signed commercial build for your platform, **verifies the signature
offline** — a tampered artifact aborts the upgrade with the running binary untouched — and
swaps it in atomically, keeping a backup of the previous one. `--check` first if you want to
see the plan without taking it:

```sh
olivares upgrade --enterprise --token <TOKEN> --check
```

Restart the service, and then turn the add-ons on:

```sh
olivares enterprise enable <preset>     # starter | regulated | full
```

Activation is governed and audited: it shows you a diff first, and stages any add-on that
needs a secret or a review rather than half-enabling it. `olivares enterprise status` reports
what is active. These commands exist **only in the commercial binary** — if
`olivares enterprise` is not a command, you are still running the Community build and the
swap above has not happened yet.

:::caution[Back up before the swap]
The swap replaces a binary, not your data — but take the backup anyway, the same one
[Upgrade and roll back](/how-to/upgrade-and-rollback/) asks for. That page also covers
going back to the previous binary.
:::

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
