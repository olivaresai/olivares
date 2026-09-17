---
title: Mit Homebrew installieren
description: >-
  Die macOS-Homebrew-Cask-Koordinate für Olivares AI, was das Cask mit
  Gatekeeper tut, und der Veröffentlichungsstand des v26.9.0-Tap-Bumps.
draft: false
---

Das ist der macOS-Pfad, den `INSTALL.md` als empfohlen nennt. Er installiert
die signierte `olivares`-Binärdatei über das Homebrew-Cask und hebt die
Gatekeeper-Quarantäne auf. Es ist nicht der Linux-Paketpfad
([Aus einem Paket installieren](/how-to/install-from-packages/)) und nicht
Docker ([Mit Docker bereitstellen](/how-to/docker-deployment/)).

:::note[Beta — das v26.9.0-Cask ist noch nicht veröffentlicht]
Der Install-Surface-Zeuge verzeichnet Homebrew als **not-published**
(`docs/releases/v26.9.0-install-surfaces.json`, gemessen
2026-09-15T20:29:52Z). Der Producer ist `.goreleaser.yaml`
`homebrew_casks:`. Das Tap-Cask wird vom Release-Job angehoben, den dieser
Tag nicht ausgeführt hat. Der Befehl unten ist die Koordinate, die
`INSTALL.md` nennt (`brew install olivaresai/tap/olivares`). Behandeln Sie
das als Installationsform, nicht als lebendigen Tap, bis dieser Zeuge
wechselt.
:::

## 1. Das Cask installieren

```sh
brew install olivaresai/tap/olivares
```

Homebrew prüft jeden Cask-Download gegen die aufgezeichnete SHA-256
(`INSTALL.md`). Das Cask installiert die signierte Binärdatei und **hebt die
Gatekeeper-Quarantäne auf**.

Darwin-Binärdateien sind mit cosign signiert (Supply-Chain-Vertrauen) und
**noch nicht von Apple notarisiert**. Ein manueller Archiv-Download wird
unter Quarantäne gestellt; das Cask erledigt das, oder Sie heben sie mit
`xattr -d com.apple.quarantine olivares` auf, wie `INSTALL.md` für den
manuellen Pfad zeigt.

## 2. Erster Start

```sh
olivares quickstart
```

Sichere Vorgaben: TLS an, Loopback, keine Standardanmeldedaten. Die Engine
druckt die Konsolen-URL und das einmalige Setup-Token. Weiter mit
[Ihre erste Stunde](/how-to/first-hour/).

Ein ephemeres synthetisches Estate (Loopback, Klartext) dient nur zum
Ansehen:

```sh
olivares serve --seed-demo --insecure --data-dir "$(mktemp -d)"
```

`--seed-demo` ist keine Produkttour. Siehe
[Ihre erste Stunde](/how-to/first-hour/).

## Verwandt

- [Die Control Plane selbst hosten](/how-to/self-hosting/) — andere Installationsformen.
- [Ein Release verifizieren](/how-to/verify-a-release/) — cosign, SBOM, Herkunft.
- [Aus einem Paket installieren](/how-to/install-from-packages/) — Linux `.deb` / `.rpm` / `.apk`.
