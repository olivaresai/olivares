---
title: "Governed Git publication"
description: "Push commits, open pull requests and merge through approved Git-host bindings, with current caller authority and retained intent outcomes."
---

The gitpublish module provides governed publication to GitHub and GitLab. It is part of Community.

Each effect requires an approved target, repository and credential binding, plus current authorization. Without the required custody, Git executable or authority, publication is refused. A registered module does not mean a repository is ready to publish.

The API namespace is `/v1/m/gitpublish`. It exposes publication targets, push, pull-request and merge intents, reconciliation, and observations.

An uncertain remote outcome is not permission to retry. The module records requests, observations and acknowledgments separately. A remote operation already dispatched can race a later local revocation.

[Module API reference](/reference/api-beta/).
