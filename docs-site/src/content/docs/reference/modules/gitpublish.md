---
title: "Governed Git publication"
description: "Push commits, open pull requests and merge through approved Git-host bindings, with current caller authority and retained intent outcomes."
---

The gitpublish module provides governed publication to GitHub, GitLab and plain git remotes (push only). It is part of Community.

Each effect requires an approved target, repository and credential binding, plus current authorization. Without the required custody, Git executable or authority, publication is refused. A registered module does not mean a repository is ready to publish.

The API namespace is `/v1/m/gitpublish`. It exposes publication targets, push, pull-request and merge intents, reconciliation, and observations.

Target reads (`GET /targets` and `GET /targets/{id}`) include a read-only `host` (`github`, `gitlab` or `git`) resolved from the approved credential binding. The console offers pull requests and merges only for GitHub and GitLab, including the draft pull request offered after a session push. If a binding is no longer approved, or an older engine omits the host, the console offers only push; the engine still checks custody and authorization for every effect. A binding lookup failure returns an unavailable response.

A push can name a session run (`session_run`). Olivares then fetches the commit from that run's folder into the server repository before it publishes, over the local file protocol only. The run must belong to the target's workspace; a request never names a path.

An uncertain remote outcome is not permission to retry. The module records requests, observations and acknowledgments separately. A remote operation already dispatched can race a later local revocation.

[Module API reference](/reference/api-beta/).
