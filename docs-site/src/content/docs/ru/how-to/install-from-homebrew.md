---
title: Установка через Homebrew
description: >-
  Координата macOS Homebrew cask для Olivares AI, что cask делает с
  Gatekeeper, и состояние публикации его bump tap.
draft: false
---

Следующий выпуск — <!-- release -->`0.1`<!-- /release -->; его релиз на GitHub ещё не опубликован. Команды ниже описывают планируемые артефакты. До публикации собирайте из исходников, а после публикации проверяйте каждый артефакт перед использованием. Наблюдаемый статус записан в <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release -->.

Это путь macOS, который `INSTALL.md` называет рекомендованным. Он ставит
подписанный двоичный файл `olivares` через cask Homebrew и снимает карантин
Gatekeeper. Это не путь пакетов Linux
([Установка из пакета](/how-to/install-from-packages/)) и не Docker
([Развёртывание с Docker](/how-to/docker-deployment/)).

:::note[Бета — cask 26.10 опубликован]
`Casks/olivares.rb` в tap обновлён для 26.10 2026-10-01: он называет версию 26.10.1<!-- release-fixed --> и четыре
архива платформ, чьи SHA-256 совпадают с подписанным `checksums.txt` релиза. Производитель — `.goreleaser.yaml`
`homebrew_casks:`; cask tap поднимает задание выпуска. Команда ниже — координата,
которую называет `INSTALL.md` (`brew install olivaresai/tap/olivares`).
:::

## 1. Установить cask

```sh
brew install olivaresai/tap/olivares
```

Homebrew сверяет каждую загрузку cask с записанным SHA-256 (`INSTALL.md`).
Cask ставит подписанный двоичный файл и **снимает карантин Gatekeeper**.

Двоичные файлы Darwin подписаны cosign (доверие цепочки поставки) и **ещё не
нотаризованы Apple**. Ручная загрузка архива попадает в карантин; cask это
обрабатывает, или снимите его через
`xattr -d com.apple.quarantine olivares`, как показывает `INSTALL.md` для
ручного пути.

## 2. Первый запуск

```sh
olivares quickstart
```

Безопасные значения по умолчанию: TLS включён, слушатели на всех интерфейсах, нет учётных данных
по умолчанию. Движок печатает URL консоли и одноразовый токен установки.
Продолжайте с [Ваш первый час](/how-to/first-hour/).

Эфемерный синтетический estate (loopback, открытый текст) только чтобы
посмотреть:

```sh
olivares serve --seed-demo --insecure --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --data-dir "$(mktemp -d)"
```

`--seed-demo` не экскурсия по продукту. См.
[Ваш первый час](/how-to/first-hour/).

## Связанное

- [Самостоятельно разместить плоскость управления](/how-to/self-hosting/) — другие формы установки.
- [Проверить выпуск](/how-to/verify-a-release/) — cosign, SBOM, происхождение.
- [Установка из пакета](/how-to/install-from-packages/) — Linux `.deb` / `.rpm` / `.apk`.
