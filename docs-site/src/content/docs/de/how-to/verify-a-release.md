---
title: Überprüfen, was Sie heruntergeladen haben
description: >-
  Verifizieren Sie die Signatur, SLSA-Provenance, SBOM und OpenVEX-Attestierungen
  eines Releases, bevor Sie es ausführen. Leiten Sie niemals einen Installer direkt
  in eine Shell.
---

Eine control plane ist ein Sicherheitsprodukt, daher sollten Sie als Erstes mit einem
Release **beweisen, dass es genau das ist, das das Projekt veröffentlicht hat**. Releases
von Olivares AI liefern alles mit, was Sie zur kryptografischen Verifizierung brauchen:
eine Signatur über die Prüfsummen, eine SLSA-Provenance-Attestierung, ein SBOM
(SPDX + CycloneDX) und eine OpenVEX-Attestierung — alle referenziert **per Digest,
niemals per Tag**.

:::danger[Niemals `curl | bash`]
Leiten Sie keinen Installer in eine Shell. Laden Sie die Artefakte herunter,
**verifizieren Sie sie**, und führen Sie sie erst dann aus. Die folgenden Schritte zeigen,
wie.
:::

## Was mit einem Release ausgeliefert wird

| Artefakt | Was es ist |
|---|---|
| `checksums.txt` (+ `.sig`, `.pem`) | SHA-256 jedes Artefakts, mit einer cosign-Signatur und einem Zertifikat |
| `*_<os>_<arch>.tar.gz` | das/die Release-Archiv(e) |
| `*.sbom.sigstore.json` | SBOM (SPDX) als signierte in-toto-Attestierung |
| `*.vex.sigstore.json` | OpenVEX als signierte in-toto-Attestierung |
| `*.intoto.jsonl` | SLSA Build L3 Provenance |
| Container-Image | in GHCR und Docker Hub veröffentlicht, per Digest geprüft und gepinnt |
| Helm-Chart-Quelle | aus `deploy/helm/olivares` installieren; noch kein öffentliches OCI-Chart |

## Der Ein-Befehl-Weg

Das Repository liefert `scripts/verify-release.sh`, das die vollständige Kette ausführt:
verifiziert die Signatur über `checksums.txt`, berechnet das SHA-256 jedes Artefakts neu,
und verifiziert dann die SBOM-, OpenVEX- und SLSA-Attestierungen.

```bash
# Default: keyless (Sigstore). Needs Rekor and Sigstore trusted-root material.
scripts/verify-release.sh

# Pin the SLSA provenance to a specific source tag.
scripts/verify-release.sh --source-tag v26.9.0

# Key-based: only for files signed with a private key you control.
# Releases are signed keyless and do not publish a public key.
scripts/verify-release.sh --key /path/to/your-cosign.pub
```

`--key` prüft Signaturen gegen einen öffentlichen Schlüssel statt gegen die Identität des
Release-Workflows und ignoriert das Transparenzprotokoll. Das belegt, dass die Dateien mit dem
passenden privaten Schlüssel signiert wurden, nicht dass das Projekt sie veröffentlicht hat.
Beziehen Sie diesen öffentlichen Schlüssel von seinem Inhaber über einen Kanal, der von den
geprüften Dateien getrennt ist.

`--offline` entfernt nur die Rekor-Abfrage aus den cosign-Aufrufen; die Prüfung wird dadurch
nicht netzwerkfrei. Die schlüssellose Prüfung benötigt weiterhin Sigstore-Trusted-Root-Material,
das cosign abruft, sofern es nicht bereits zwischengespeichert ist, und das Skript hat keine
Option `--trusted-root`. Die Prüfung der SBOM- und OpenVEX-Bundles benötigt auch mit `--key`
Trusted-Root-Material, und der SLSA-Schritt ruft `slsa-verifier` ohne Offline-Option auf.

## Was es prüft, Schritt für Schritt

Falls Sie die Prüfungen lieber selbst ausführen, ist dies, was das Skript tut:

1. **Signatur über die Prüfsummen** — keyless, verifiziert gegen die GitHub-Actions-Identität
   und den OIDC-Issuer des Projekts:

   ```bash
   cosign verify-blob \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

2. **Artefakt-Integrität** — jedes heruntergeladene Artefakt muss mit `checksums.txt`
   übereinstimmen:

   ```bash
   sha256sum --check checksums.txt
   ```

3. **SBOM-Attestierung (SPDX):**

   ```bash
   cosign verify-blob-attestation --type spdxjson \
     --bundle <artifact>.sbom.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

4. **OpenVEX-Attestierung** (die auf Erreichbarkeit basierende Schwachstellenaussage des Projekts):

   ```bash
   cosign verify-blob-attestation --type openvex \
     --bundle <artifact>.vex.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

5. **SLSA-Provenance:**

   ```bash
   slsa-verifier verify-artifact <artifact> \
     --provenance-path <artifact>.intoto.jsonl \
     --source-uri github.com/olivaresai/olivares
   ```

## Das Container-Image verifizieren

Für das veröffentlichte Image lösen Sie den Digest auf und verifizieren gegen die
GitHub-Actions-Identität (dieser Weg ist keyless und benötigt Netzwerk):

```bash
IMAGE=docker.io/olivaresai/olivares
DIGEST="$(crane digest "$IMAGE:<version>")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type openvex \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
slsa-verifier verify-image "$REF" \
  --source-uri github.com/olivaresai/olivares --source-tag <version>
```

Stellen Sie das Image immer **per Digest** bereit (`@sha256:…`), niemals über einen
veränderlichen Tag.

## In einer air-gapped-Umgebung

Das **Air-Gap-Bundle** erstellt ein Betreiber mit einem eigenen cosign-Schlüssel; das Bundle
enthält die passende `cosign.pub`. Seine Skripte prüfen das Helm-Chart und die gespeicherten
Images ohne Rekor gegen diesen Schlüssel; ein Image besteht nur, wenn es eine mit diesem Schlüssel
erstellte Signatur trägt. Ein Schlüssel aus dem Bundle, das mit ihm geprüft wird, authentifiziert
dieses Bundle nicht: Vergleichen Sie ihn, bevor Sie dem Bundle vertrauen, mit einer Kopie, die
Sie vom Schlüsselinhaber über einen getrennten Kanal erhalten haben. Siehe
[Installation in einer air-gapped-Umgebung](/how-to/air-gap-install/).

:::note[Ehrliche Anmerkung zur Verfügbarkeit von Attestierungen]
Die Verifizierung ist nur so vollständig wie die Attestierungen, die ein bestimmtes
Release tatsächlich veröffentlicht hat. Der Verifizierer meldet jeden Schritt, den er
ausführt; falls ein Release ein Artefakt auslässt (zum Beispiel ein Build, der kein SBOM
angehängt hat), hat der entsprechende Schritt nichts zu prüfen. Der Release-Workflow hängt
die oben benannten SBOM-, OpenVEX- und SLSA-Artefakte für den Standard-Build an.
:::
