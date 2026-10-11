---
title: Установка в изолированной (air-gapped) среде
description: >-
  Перенесите подписанный комплект релиза через разрыв сети, проверьте каждый
  образ и Helm-чарт полностью офлайн, зеркалируйте их в приватный реестр по
  digest и установите — без исходящих вызовов на отключённой стороне.
---

This deployment capability is distributed with Enterprise. See [editions](https://olivares.ai/pricing).

## 1. Сборка комплекта (онлайн, однократно)

### Что содержит комплект

## 2. Проверка и зеркалирование внутри изоляции

### Проверьте каждый образ офлайн (без журнала прозрачности)

### Проверьте Helm-чарт офлайн

# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz

# (needs the signer's GPG public key in your keyring)

### Зеркалируйте в свой приватный реестр по digest

### Устанавливайте по digest, никогда по тегу

## Внутри изоляции ничто не обращается наружу

## Варианты FIPS / STIG

## См. также
