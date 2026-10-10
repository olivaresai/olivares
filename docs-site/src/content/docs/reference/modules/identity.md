---
title: Identity
description: Read-only identity posture and workload identity federation, selectable separately from governance.
---

`identity` is the read-only identity posture module. It is **selected by default**
on a fresh installation and requires [governance](/reference/modules/vi-governance/).
Its descriptor is `olivares.identity-console`; its selectable name and API
namespace are `identity`. Sharing the governance package does not make it the
same module as governance.

The module serves the workload identity federation (WIF) graph, SSO posture,
external-key posture and workspace residency at `/v1/m/identity/`. The WIF graph
distinguishes declared configuration from observed configuration when a live
source is available. Certificate presence is a boolean; the response does not
expose certificate bodies, provider keys or identity tokens.

These are read-only views. They do not create or edit provider federation
objects. Unconfigured posture providers report their availability and reason;
an empty declared graph is not proof that live federation is configured.
The routes require `governance:idposture:read`.

Use **Edition & modules** in the console, or the CLI:

```sh
olivares modules ls
olivares modules on identity
olivares identity wif
olivares modules off identity
```

Identity lifecycle and authorization remain governance capabilities. See the
[CLI reference](/reference/cli/#command-olivares-identity) for the posture commands.
