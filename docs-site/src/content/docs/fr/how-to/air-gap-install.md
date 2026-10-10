---
title: Installer dans un environnement air-gapped
description: >-
  Transportez un bundle de release signé de l'autre côté de la coupure, vérifiez
  chaque image et le chart Helm entièrement hors ligne, mettez-les en miroir
  dans un registre privé par digest, et installez — sans appel sortant du côté déconnecté.
---

This deployment capability is distributed with Enterprise. See [editions](https://olivares.ai/pricing).

## 1. Construire le bundle (en ligne, une fois)

### Ce que contient le bundle

## 2. Vérifier et mettre en miroir à l'intérieur de la coupure

### Vérifier chaque image hors ligne (sans journal de transparence)

### Vérifier le chart Helm hors ligne

# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz

# (needs the signer's GPG public key in your keyring)

### Mettre en miroir dans votre registre privé par digest

### Installer par digest, jamais par tag

## À l'intérieur de la coupure, rien n'appelle vers l'extérieur

## Variantes FIPS / STIG

## Voir aussi
