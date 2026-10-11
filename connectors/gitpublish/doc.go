// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package gitpublish is the stateless Git-host WRITE adapter behind the
// gitpublish module (J10-S3): GitHub App installation tokens, the GitHub and
// GitLab pull/merge REST calls, and a leased `git push` run in a closed
// environment. It holds no authority and no state. Deciding WHO may publish,
// and recording what was requested, observed and acknowledged, belongs to the
// module; this package only performs one bounded host call at a time and
// classifies its result.
//
// Target kinds interpret approved publication rows and expose the existing
// read-only observers (connectors/github, connectors/gitlab) for content diffs.
//
// Custody rules this package enforces by construction:
//   - a credential is a Secret, which never renders through fmt, slog, JSON or
//     an error; only the one call that must send it calls Reveal;
//   - an installation token is narrowed to one repository and the effect's
//     permissions, and a token whose repository list is wider is released and
//     refused; the Workflows permission is never requested;
//   - host failures surface only bounded fields (status, a closed code and the
//     request id), never a host body;
//   - the HTTP client refuses every redirect, follows pagination only on the
//     same origin, uses no proxy from the environment and refuses loopback and
//     link-local peers.
//
// It imports the standard library, SDK and first-party Apache-2.0 observers,
// never the engine.
package gitpublish
