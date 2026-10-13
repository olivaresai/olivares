---
title: Identity
description: Read-only identity posture and workload identity federation, selectable separately from governance.
---

`identity` is the read-only identity posture module. It is **selected by default**
on a fresh installation and requires [governance](/reference/modules/vi-governance/).
Its descriptor is `olivares.identity-console`; its selectable name and API
namespace are `identity`. Sharing the governance package does not make it the
same module as governance.

Community serves local SSO posture at `/v1/m/identity/sso`. Workload identity
federation (WIF), managed identity lifecycle, external-key posture
and workspace residency require the Business/Enterprise identity add-on. Their
published routes remain available as HTTP `501` seams in Community, and the
console hides their views. Selecting `identity` does not install an add-on.

In Business/Enterprise, the WIF graph
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
olivares modules off identity
```

Managed identity lifecycle and directory authorization are Business governance
capabilities. See the
[CLI reference](/reference/cli/#command-olivares-identity) for the posture commands.

With the Business identity add-on, `olivares identity wif` reads the federation graph.
