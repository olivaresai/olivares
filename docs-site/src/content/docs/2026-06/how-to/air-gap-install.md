---
title: Install in an air-gapped environment
description: Carry a signed release bundle across the gap, verify every image
  and the Helm chart fully offline, mirror them into a private registry by
  digest, and install — with no outbound calls on the disconnected side.
slug: 2026-06/how-to/air-gap-install
---

This versioned guide describes the pre-1.0 edition placement. From 1.0, offline installation requires Enterprise; its complete procedure is supplied through that distribution. Community can verify a carried bundle without installing it:

```sh
olivares upgrade --bundle ./release-bundle --check
```
