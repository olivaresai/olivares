---
title: Проверка того, что вы скачали
description: >-
  Проверьте подпись релиза, происхождение SLSA, SBOM и аттестации OpenVEX, прежде
  чем запускать его — онлайн (без ключа) или полностью офлайн (на основе ключа).
  Никогда не направляйте установщик напрямую в shell.
---

Control plane — это продукт безопасности, поэтому первое, что вам следует сделать с
релизом, — это **доказать, что это именно тот релиз, который опубликовал проект**.
Релизы Olivares AI поставляются со всем необходимым для криптографической проверки:
подпись над контрольными суммами, аттестация происхождения SLSA, SBOM (SPDX +
CycloneDX) и аттестация OpenVEX — все ссылаются **по дайджесту, никогда по тегу**.

:::danger[Никогда не используйте `curl | bash`]
Не направляйте установщик в shell. Скачайте артефакты, **проверьте их** и только
затем запускайте. Шаги ниже описывают, как это сделать.
:::

## Что поставляется с релизом

| Артефакт | Что это |
|---|---|
| `checksums.txt` (+ `.sig`, `.pem`) | SHA-256 каждого артефакта, с подписью и сертификатом cosign |
| `*_<os>_<arch>.tar.gz` | архив(ы) релиза |
| `*.sbom.sigstore.json` | SBOM (SPDX) как подписанная аттестация in-toto |
| `*.vex.sigstore.json` | OpenVEX как подписанная аттестация in-toto |
| `*.intoto.jsonl` | происхождение SLSA Build L3 |
| образ контейнера + Helm-чарт | публикуются в реестр при релизе, закрепляются по дайджесту |

## Путь в одну команду

Репозиторий поставляется со скриптом `scripts/verify-release.sh`, который выполняет
всю цепочку: проверяет подпись над `checksums.txt`, заново вычисляет SHA-256 каждого
артефакта, затем проверяет аттестации SBOM, OpenVEX и SLSA.

```bash
# Default: keyless (Sigstore). Needs network access to the transparency log (Rekor).
scripts/verify-release.sh

# Key-based (air-gap friendly): verify against the project's public key.
scripts/verify-release.sh --key cosign.pub

# Fully offline: no Rekor / no transparency-log network at all.
scripts/verify-release.sh --key cosign.pub --offline

# Pin the SLSA provenance to a specific source tag.
scripts/verify-release.sh --source-tag v26.8.0
```

С `--offline` (или всякий раз, когда предоставлен ключ) скрипт добавляет
`--insecure-ignore-tlog` к каждому вызову cosign, так что сеть Sigstore/Rekor не
используется — это путь для отключённых от сети сред.

## Что он проверяет, шаг за шагом

Если вы предпочитаете выполнять проверки самостоятельно, вот что делает скрипт:

1. **Подпись над контрольными суммами** — без ключа, проверяется против
   идентичности GitHub Actions проекта и эмитента OIDC:

   ```bash
   cosign verify-blob \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

2. **Целостность артефактов** — каждый скачанный артефакт должен совпадать с
   `checksums.txt`:

   ```bash
   sha256sum --check checksums.txt
   ```

3. **Аттестация SBOM (SPDX):**

   ```bash
   cosign verify-blob-attestation --type spdxjson \
     --bundle <artifact>.sbom.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

4. **Аттестация OpenVEX** (заявление проекта об уязвимостях на основе достижимости):

   ```bash
   cosign verify-blob-attestation --type openvex \
     --bundle <artifact>.vex.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

5. **Происхождение SLSA:**

   ```bash
   slsa-verifier verify-artifact <artifact> \
     --provenance-path <artifact>.intoto.jsonl \
     --source-uri github.com/olivaresai/olivares
   ```

## Проверка образа контейнера

Для опубликованного образа разрешите дайджест и проверьте против идентичности
GitHub Actions (этот путь без ключа и требует сети):

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

Всегда развёртывайте образ **по дайджесту** (`@sha256:…`), никогда по изменяемому тегу.

## В изолированной среде (air-gapped)

Если вы вообще не можете достучаться до сети, используйте **air-gap бандл**, который
несёт публичный ключ и проверяет всё офлайн (без Rekor). См.
[Установка в изолированной среде (air-gapped)](/how-to/air-gap-install/).

:::note[Честное замечание о доступности аттестаций]
Проверка настолько полна, насколько полны аттестации, которые данный релиз
фактически опубликовал. Верификатор сообщает о каждом выполняемом им шаге; если
релиз пропускает артефакт (например, сборка, которая не приложила SBOM),
соответствующему шагу нечего проверять. Рабочий процесс релиза прикладывает
артефакты SBOM, OpenVEX и SLSA, названные выше, для стандартной сборки.
:::
