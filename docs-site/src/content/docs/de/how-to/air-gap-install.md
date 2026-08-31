---
title: Installation in einer Air-Gap-Umgebung
description: >-
  Ein signiertes Release-Bundle über den Gap tragen, jedes Image und das
  Helm-Chart vollständig offline verifizieren, sie per Digest in eine private
  Registry spiegeln und installieren — ohne ausgehende Aufrufe auf der getrennten Seite.
---

Olivares AI ist **self-host-first und air-gap-ready**. Diese Anleitung bringt ein
signiertes Release über einen Air Gap **ohne Netzwerk auf der getrennten Seite**:
Sie verifizieren jedes Image und das Helm-Chart offline gegen einen Public Key,
spiegeln sie **per Digest** in Ihre private Registry und installieren. Das Produkt
führt **keine obligatorischen ausgehenden Aufrufe beim Booten** aus, sodass nichts
innerhalb des Gaps das Internet erreicht. Der einzige Befehl, der einen
Hersteller-Endpunkt erreichen würde, ist `olivares upgrade`; `--endpoint` oder
`--bundle` richtet ihn auf Ihren eigenen Mirror.

Der Ablauf ist zweiseitig:

1. **Online, einmalig** — ein Maintainer baut ein eigenständiges Bundle.
2. **Innerhalb des Gaps** — Sie verifizieren es offline und spiegeln es in Ihre
   Registry.

Diese Seite dokumentiert, wie das Bundle und die mitgelieferten Skripte
**verwendet** werden; sie baut die Release-Pipeline nicht neu auf.

## 1. Das Bundle bauen (online, einmalig)

Auf einer verbundenen Maschine zieht `scripts/airgap-bundle.sh` jedes Image
**per Digest fixiert**, paketiert und signiert das Helm-Chart, sammelt
SBOM/OpenVEX/Provenance und gibt einen einzelnen Tarball mit einer `VERIFY.md`
aus:

```bash
scripts/airgap-bundle.sh \
  --version v26.8.0 \
  --image docker.io/olivaresai/olivares:26.8.0-amd64 \
  --chart deploy/helm/olivares \
  --cosign-key cosign.key \
  [--collector-image <ref>] [--out dist/airgap] [--gpg-key <id>]
```

Das Image wird von Docker Hub über seine offizielle Koordinate
(`docker.io/olivaresai/olivares`) gezogen; derselbe Inhalt liegt, per Digest identisch,
auch unter `ghcr.io/olivaresai/olivares`, falls Sie von dort spiegeln möchten. Docker Hub
begrenzt die Rate **anonymer** Pulls; ghcr.io tut das bei öffentlichen Images nicht, was auf
einem nicht authentifizierten Build-Host hilft.

:::caution[SBOM/VEX/Provenance werden bereitgestellt, nicht generiert]
Der Bundler kopiert SBOM, OpenVEX und Provenance **nach bestem Bemühen aus
Umgebungsvariablen** (`OLIVARES_SBOM_FILES`, `OLIVARES_VEX_FILES`,
`OLIVARES_PROV_FILES`) in das Bundle. Sind diese nicht gesetzt, sind die
Verzeichnisse `sbom/`, `vex/` und `prov/` im Bundle leer — setzen Sie sie, damit
Ihr getrennter Standort die Attestierungen erhält.
:::

### Was das Bundle enthält

```text
images/<name>/   cosign-saved image + its signatures/attestations (offline)
chart/<chart>.tgz   packaged Helm chart  (+ .tgz.sig cosign, + .prov if gpg)
sbom/  vex/  prov/   SBOM, OpenVEX and SLSA provenance for the release
cosign.pub          the public key to verify everything offline (key mode)
digests.txt         the pinned digest of every image (the manifest of record)
VERIFY.md           the exact offline verification + mirror walkthrough
```

Das Bundle trägt außerdem Kopien von `airgap-mirror.sh` und `verify-release.sh`,
sodass die getrennte Seite nichts aus dem Netzwerk benötigt.

## 2. Verifizieren und spiegeln innerhalb des Gaps

Auf der getrennten Seite benötigen Sie nur `cosign`, `crane`, `helm` und `tar` —
sowie eine erreichbare **private Registry**. Kein Internet.

### Jedes Image offline verifizieren (kein Transparency Log)

```bash
for d in images/*/; do
  cosign verify --local-image "$d" --insecure-ignore-tlog --key cosign.pub
done
```

`--insecure-ignore-tlog` überspringt Sigstores Online-Transparency-Log; das
Vertrauen stammt aus dem mitgelieferten `cosign.pub`. (Das ist *nicht* dasselbe
wie das keyless `--offline`-Flag — im Key-Modus ist die Offline-Vertrauenswurzel
der Public Key.)

### Das Helm-Chart offline verifizieren

```bash
cosign verify-blob --key cosign.pub --insecure-ignore-tlog \
  --signature chart/*.tgz.sig chart/*.tgz
# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz
# (needs the signer's GPG public key in your keyring)
```

### Per Digest in Ihre private Registry spiegeln

`scripts/airgap-mirror.sh` verifiziert jedes Image offline, lädt es in Ihre
Registry und **fixiert es per Digest neu**, um zu bestätigen, dass der Digest die
Spiegelung überstanden hat (es verwendet `crane` und `cosign load` — **nicht**
`oras`):

```bash
scripts/airgap-mirror.sh \
  --bundle olivares-airgap-v26.8.0.tar.gz \
  --registry registry.internal:5000 [--insecure]
```

### Per Digest installieren, niemals per Tag

```bash
helm install olivares \
  oci://registry.internal:5000/charts/olivares \
  --version <chart-version> \
  --set image.repository=registry.internal:5000/olivares \
  --set image.digest=<digest-from-digests.txt>
```

Installieren Sie immer aus dem **Digest** in `digests.txt`, niemals aus einem
veränderbaren Tag — ein Digest ist unveränderlich und ist das, was Sie
verifiziert haben.

## Innerhalb des Gaps ruft nichts nach außen

> Die Engine führt **keine obligatorischen ausgehenden Aufrufe beim Booten** aus
> (sie bindet standardmäßig an Loopback), sodass innerhalb des Gaps nichts das
> Internet erreicht. `olivares upgrade` ist der einzige Befehl, der einen
> Hersteller-Endpunkt erreichen würde; `--endpoint` oder `--bundle` richtet ihn
> auf Ihren eigenen Mirror.

Die Lizenz wird **offline** validiert (eine Ed25519-Signatur, kein Lizenzserver),
und keiner der oben genannten Verifizierungs- oder Installationsschritte berührt
das Internet, sobald das Bundle über den Gap gelangt ist. Es gibt keinen
Telemetrie-Home-Standard zum Deaktivieren.

Erreicht wird der Anbieter auf der **Online**-Seite, und das ist so gewollt: Beim Bauen
des Bundles wird das Release heruntergeladen, und in einer kommerziellen Umgebung ist
das Abonnement der Zugangsnachweis, mit dem die Add-ons, ihre Updates und ihre Patches
bezogen werden. Das ist das SUSE/Novell-Modell — eine Air-Gapped-Umgebung wird aus
einem lokalen Mirror bedient, der dasselbe Recht weiterhin trägt. Siehe
[Self-Hosting](/de/how-to/self-hosting/).

:::note[Listen-Standardwerte: Container vs. Binary]
Direkt ausgeführt, bindet das Binary standardmäßig an **Loopback**. Der
Standardbefehl des Release-**Container-Images** bindet `0.0.0.0` innerhalb des
Containers, damit Sie es mit Ihrem Ingress/Service vorlagern können — das ist ein
Bind innerhalb des Containers, kein ausgehender Aufruf. Setzen Sie Ihre
Listen-Adressen explizit für Ihre Bereitstellung.
:::

## FIPS-/STIG-Varianten

Es existieren gehärtete Build-Varianten (ein FIPS-Modus-Build, der das
CMVP-validierte kryptografische Go-Modul einbindet, und ein STIG-orientiertes
Image). Diese sind **post-v1** und tragen ihr eigenes Ehrlichkeits-Ledger —
insbesondere wird **keine FedRAMP/DoD-ATO behauptet**, und nur die spezifisch
validierte Modulversion sollte als validiert dargestellt werden. Behandeln Sie
sie als verfügbar-aber-noch-nicht-v1 statt als zertifiziertes Angebot.

## Siehe auch

- [Verifizieren, was Sie heruntergeladen haben](/how-to/verify-a-release/) — die
  nicht-air-gapped Verifizierungskette (Signatur, SBOM, OpenVEX, SLSA).
- [Die Control Plane selbst hosten](/how-to/self-hosting/) — die Pfade Single-
  Binary, Compose und Kubernetes und ihre sicheren Standardeinstellungen.
