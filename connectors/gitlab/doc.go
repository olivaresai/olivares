// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package gitlab is the Olivares AI source connector that observes GitLab
// projects as data sources for coding agents, emitting
// model.EdgeObservation records to the access-map module (ARCHITECTURE.md). It
// derives observed R/RW access edges from webhook events (push,
// merge_request, tag_push) and reconciles with periodic API polling;
// ACL-derived permitted edges ride the existing SignalPolicy path.
//
// The connector is webhook-first: a GitLab project or group webhook
// delivers push and merge-request events in real time, and a background
// poller runs at a configurable interval for reconciliation (catching
// events the webhook missed during downtime). A separate ACL sync loop
// polls project and group membership to populate the permitted side of
// the permitted-vs-observed diff.
//
// Three correlation layers resolve AI-agent attribution from GitLab's
// identity model:
//
//  1. Commit markers: Co-Authored-By trailers naming known AI tools (Claude,
//     Copilot, Cursor, etc.) mark the push as agent-assisted with
//     approximate confidence.
//  2. Bot accounts: pushes from declared bot usernames are attributed to the
//     agent with attributed confidence.
//  3. Human identity: all other pushes are attributed to the pusher's login
//     as a human identity.
//
// The connector is read-only: it reads webhook payloads and the GitLab
// REST API v4, and never creates, modifies or deletes any GitLab resource.
//
// Authentication uses a Personal, Group or Project access token with the
// api scope. For self-managed GitLab instances the API base URL is
// configurable.
//
// It imports only the SDK (and connector-internal helpers), never the
// engine, keeping the Apache-2.0 boundary clean (LICENSING.md).
package gitlab
