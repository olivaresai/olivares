---
title: Mit Homebrew installieren
description: >-
  Die macOS-Homebrew-Cask-Koordinate für Olivares AI, was das Cask mit
  Gatekeeper tut, und der Veröffentlichungsstand seines Tap-Bumps.
draft: false
---

Das nächste Release ist <!-- release -->`0.1`<!-- /release -->; es ist noch nicht auf GitHub veröffentlicht. Die folgenden Befehle beschreiben die geplanten Artefakte. Bauen Sie bis zur Veröffentlichung aus dem Quellcode und prüfen Sie danach jedes Artefakt vor der Verwendung. Der beobachtete Veröffentlichungsstand steht in <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release -->.

Das ist der macOS-Pfad, den `INSTALL.md` als empfohlen nennt. Er installiert
die signierte `olivares`-Binärdatei über das Homebrew-Cask und hebt die
Gatekeeper-Quarantäne auf. Es ist nicht der Linux-Paketpfad
([Aus einem Paket installieren](/how-to/install-from-packages/)) und nicht
Docker ([Mit Docker bereitstellen](/how-to/docker-deployment/)).

:::note[Beta — das 26.10-Cask ist veröffentlicht]
`Casks/olivares.rb` im Tap wurde am 2026-10-01 für 26.10 aktualisiert: Es nennt Version
26.10.1<!-- release-fixed --> und vier Plattform-Archive, deren SHA-256-Werte der signierten `checksums.txt` des
Releases entsprechen. Der Producer ist
`.goreleaser.yaml` `homebrew_casks:`; der Release-Job hebt das Tap-Cask an. Der Befehl
unten ist die Koordinate, die `INSTALL.md` nennt (`brew install olivaresai/tap/olivares`).
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

Sichere Vorgaben: TLS an, Listener auf allen Schnittstellen, keine Standardanmeldedaten. Die Engine
druckt die Konsolen-URL und das einmalige Setup-Token. Weiter mit
[Ihre erste Stunde](/how-to/first-hour/).

Ein ephemeres synthetisches Estate (Loopback, Klartext) dient nur zum
Ansehen:

```sh
olivares serve --seed-demo --insecure --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --data-dir "$(mktemp -d)"
```

`--seed-demo` ist keine Produkttour. Siehe
[Ihre erste Stunde](/how-to/first-hour/).

## Verwandt

- [Die Control Plane selbst hosten](/how-to/self-hosting/) — andere Installationsformen.
- [Ein Release verifizieren](/how-to/verify-a-release/) — cosign, SBOM, Herkunft.
- [Aus einem Paket installieren](/how-to/install-from-packages/) — Linux `.deb` / `.rpm` / `.apk`.
