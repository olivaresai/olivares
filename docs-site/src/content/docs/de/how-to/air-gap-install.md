---
title: Installation in einer Air-Gap-Umgebung
description: >-
  Ein signiertes Release-Bundle über den Gap tragen, jedes Image und das
  Helm-Chart vollständig offline verifizieren, sie per Digest in eine private
  Registry spiegeln und installieren — ohne ausgehende Aufrufe auf der getrennten Seite.
---

This deployment capability is distributed with Enterprise. See [editions](https://olivares.ai/pricing).

## 1. Das Bundle bauen (online, einmalig)

### Was das Bundle enthält

## 2. Verifizieren und spiegeln innerhalb des Gaps

### Jedes Image offline verifizieren (kein Transparency Log)

### Das Helm-Chart offline verifizieren

# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz

# (needs the signer's GPG public key in your keyring)

### Per Digest in Ihre private Registry spiegeln

### Per Digest installieren, niemals per Tag

## Innerhalb des Gaps ruft nichts nach außen

## FIPS-/STIG-Varianten

## Siehe auch
