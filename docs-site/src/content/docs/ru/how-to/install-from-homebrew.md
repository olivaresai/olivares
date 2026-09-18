---
title: Установка через Homebrew
description: >-
  Координата macOS Homebrew cask для Olivares AI, что cask делает с
  Gatekeeper, и состояние публикации bump tap v26.9.1.
draft: false
---

Это путь macOS, который `INSTALL.md` называет рекомендованным. Он ставит
подписанный двоичный файл `olivares` через cask Homebrew и снимает карантин
Gatekeeper. Это не путь пакетов Linux
([Установка из пакета](/how-to/install-from-packages/)) и не Docker
([Развёртывание с Docker](/how-to/docker-deployment/)).

:::note[Бета — cask v26.9.1 ещё не опубликован]
Свидетель поверхностей установки записывает Homebrew как **not-published**
(`docs/releases/v26.9.1-install-surfaces.json`, измерено
2026-09-15T20:29:52Z). Производитель — `.goreleaser.yaml`
`homebrew_casks:`. Cask tap поднимает задание выпуска, которое этот тег не
запускал. Команда ниже — координата, которую называет `INSTALL.md`
(`brew install olivaresai/tap/olivares`). Считайте это формой установки, а
не живым tap, пока этот свидетель не сменится.
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
