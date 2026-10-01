---
title: Установка через Homebrew
description: >-
  Координата macOS Homebrew cask для Olivares AI, что cask делает с
  Gatekeeper, и состояние публикации bump tap 26.10.0.
draft: false
---

Это путь macOS, который `INSTALL.md` называет рекомендованным. Он ставит
подписанный двоичный файл `olivares` через cask Homebrew и снимает карантин
Gatekeeper. Это не путь пакетов Linux
([Установка из пакета](/how-to/install-from-packages/)) и не Docker
([Развёртывание с Docker](/how-to/docker-deployment/)).

:::note[Бета — cask 26.10 опубликован]
`Casks/olivares.rb` в tap обновлён для 26.10 2026-10-01: он называет версию 26.10.0 и четыре
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

Безопасные значения по умолчанию: TLS включён, loopback, нет учётных данных
по умолчанию. Движок печатает URL консоли и одноразовый токен установки.
Продолжайте с [Ваш первый час](/how-to/first-hour/).

Эфемерный синтетический estate (loopback, открытый текст) только чтобы
посмотреть:

```sh
olivares serve --seed-demo --insecure --data-dir "$(mktemp -d)"
```

`--seed-demo` не экскурсия по продукту. См.
[Ваш первый час](/how-to/first-hour/).

## Связанное

- [Самостоятельно разместить плоскость управления](/how-to/self-hosting/) — другие формы установки.
- [Проверить выпуск](/how-to/verify-a-release/) — cosign, SBOM, происхождение.
- [Установка из пакета](/how-to/install-from-packages/) — Linux `.deb` / `.rpm` / `.apk`.
