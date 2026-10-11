---
title: Claude agent confirmations
description: Managed-agent thread events and human approval or rejection of pending tool confirmations.
---

`claude-agents` provides the managed-agent tool-confirmation backend. It is
**off by default** on a fresh installation and requires
[governance](/reference/modules/vi-governance/). Its descriptor is
`olivares.claude-agents`, with API routes under `/v1/m/claude-agents/`.

The module reads a session's thread events and lets a human approve or reject
a pending tool confirmation. It uses governance's approval state machine;
enabling this module does not start an agent or approve its tools automatically.
Decisions are audited against a redacted fingerprint of the session and tool
use, rather than storing the raw tool payload in the decision record.

Event reads require `governance:approval:read`. Confirmations require
`governance:approval:admin` and a stable human user identity; a system token
cannot confirm a tool. With no thread-event source, the event list is empty;
an error from a wired source is reported as an upstream failure.

Use **Edition & modules** in the console, or the CLI:

```sh
olivares modules ls
olivares modules on claude-agents
olivares modules off claude-agents
```

See [governance](/reference/modules/vi-governance/) for the approval model and
[live operation & sessions](/reference/modules/ii-sessions/) for session runtime.
