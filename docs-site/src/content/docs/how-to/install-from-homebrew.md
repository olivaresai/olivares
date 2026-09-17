---
title: Install with Homebrew
description: >-
  The macOS Homebrew cask coordinate for Olivares AI, what the cask does with
  Gatekeeper, and the publication state of the v26.9.0 tap bump.
draft: false
---

This is the macOS path that `INSTALL.md` names as recommended. It installs the
signed `olivares` binary through the Homebrew cask and clears the Gatekeeper
quarantine. It is not the Linux package path
([Install from a package](/how-to/install-from-packages/)) and not Docker
([Deploy with Docker](/how-to/docker-deployment/)).

:::note[Beta — the v26.9.0 cask is not published yet]
The install-surface witness records Homebrew as **not-published**
(`docs/releases/v26.9.0-install-surfaces.json`, measured 2026-09-15T20:29:52Z).
The producer is `.goreleaser.yaml` `homebrew_casks:`. The tap cask is bumped by
the release job, which that tag has not run. The command below is the
coordinate `INSTALL.md` names (`brew install olivaresai/tap/olivares`). Treat
this as the install shape, not a live tap, until that witness flips.
:::

## 1. Install the cask

```sh
brew install olivaresai/tap/olivares
```

Homebrew checks each cask download against its recorded SHA-256 (`INSTALL.md`).
The cask installs the signed binary and **clears the Gatekeeper quarantine**.

Darwin binaries are signed by cosign (supply-chain trust) and are **not yet
Apple-notarized**. A manual archive download is quarantined; the cask handles
that, or you clear it with `xattr -d com.apple.quarantine olivares` as
`INSTALL.md` shows for the manual path.

## 2. First run

```sh
olivares quickstart
```

Secure defaults: TLS on, loopback, no default credentials. The engine prints
the console URL and the one-time setup token. Continue with
[Your first hour](/how-to/first-hour/).

An ephemeral synthetic estate (loopback, plaintext) is for looking around only:

```sh
olivares serve --seed-demo --insecure --data-dir "$(mktemp -d)"
```

`--seed-demo` is not a product tour. See [Your first hour](/how-to/first-hour/).

## Related

- [Self-host the control plane](/how-to/self-hosting/) — other install shapes.
- [Verify a release](/how-to/verify-a-release/) — cosign, SBOM, provenance.
- [Install from a package](/how-to/install-from-packages/) — Linux `.deb` / `.rpm` / `.apk`.
