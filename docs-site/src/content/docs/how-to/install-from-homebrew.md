---
title: Install with Homebrew
description: >-
  The macOS Homebrew cask coordinate for Olivares AI, what the cask does with
  Gatekeeper, and the publication state of the 26.10.0 tap bump.
draft: false
---

This is the macOS path that `INSTALL.md` names as recommended. It installs the
signed `olivares` binary through the Homebrew cask and clears the Gatekeeper
quarantine. It is not the Linux package path
([Install from a package](/how-to/install-from-packages/)) and not Docker
([Deploy with Docker](/how-to/docker-deployment/)).

:::note[Beta — the 26.10 cask is published]
The tap's `Casks/olivares.rb` was updated for 26.10 on 2026-10-01: it names version 26.10.0 and
four platform archives whose SHA-256 values equal the release's signed `checksums.txt`. The producer is `.goreleaser.yaml` `homebrew_casks:`; the release job bumps the
tap cask. The command below is the coordinate `INSTALL.md` names
(`brew install olivaresai/tap/olivares`).
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
