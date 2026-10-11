---
title: Install in an air-gapped environment
description: >-
  Carry a signed release bundle across the gap, verify every image and the Helm
  chart fully offline, mirror them into a private registry by digest, and install
  — with no outbound calls on the disconnected side.
---

This deployment capability is distributed with Enterprise. See [editions](https://olivares.ai/pricing).

## 1. Build the bundle (online, once)

### What the bundle contains

## 2. Verify and mirror inside the gap

### Verify every image offline (no transparency log)

### Verify the Helm chart offline

# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz

# (needs the signer's GPG public key in your keyring)

### Mirror into your private registry by digest

### Install by digest, never by tag

## Nothing calls out inside the gap

## FIPS / STIG variants

## See also
