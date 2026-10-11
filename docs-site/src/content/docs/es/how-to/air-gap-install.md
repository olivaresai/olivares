---
title: Instalar en un entorno air-gapped
description: >-
  Lleva un bundle de release firmado al otro lado de la brecha, verifica cada
  imagen y el chart de Helm por completo offline, refléjalos en un registro
  privado por digest e instala — sin llamadas salientes en el lado desconectado.
---

This deployment capability is distributed with Enterprise. See [editions](https://olivares.ai/pricing).

## 1. Construir el bundle (online, una vez)

### Qué contiene el bundle

## 2. Verificar y reflejar dentro de la brecha

### Verificar cada imagen offline (sin transparency log)

### Verificar el chart de Helm offline

# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz

# (needs the signer's GPG public key in your keyring)

### Reflejar en tu registro privado por digest

### Instalar por digest, nunca por tag

## Dentro de la brecha nada llama al exterior

## Variantes FIPS / STIG

## Véase también
