// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package github is the Olivares AI source connector that observes GitHub
// repositories as data sources for coding agents, emitting
// model.EdgeObservation records to the access-map module (ARCHITECTURE.md). It
// derives observed R/RW access edges from webhook events (push,
// pull_request) and reconciles with periodic API polling; ACL-derived
// permitted edges ride the existing SignalPolicy path.
//
// The connector is webhook-first: a GitHub App or organization webhook
// delivers push and pull-request events in real time, and a background
// poller runs at a configurable interval for reconciliation (catching
// events the webhook missed during downtime). A separate ACL sync loop
// polls collaborator and team permissions to populate the permitted side
// of the permitted-vs-observed diff.
//
// Three correlation layers resolve AI-agent attribution from GitHub's
// identity model:
//
//  1. Commit markers: Co-Authored-By trailers naming known AI tools (Claude,
//     Copilot, Cursor, etc.) mark the push as agent-assisted with
//     approximate confidence.
//  2. Bot accounts: pushes from declared bot usernames (e.g. a GitHub App
//     bot) are attributed to the agent with attributed confidence.
//  3. Human identity: all other pushes are attributed to the pusher's login
//     as a human identity.
//
// The connector is read-only: it reads webhook payloads and the GitHub
// REST API, and never creates, modifies or deletes any GitHub resource.
//
// Authentication supports GitHub App installation tokens (preferred) with
// a personal access token fallback. For GitHub Enterprise Server the API
// base URL is configurable.
//
// It imports only the SDK (and connector-internal helpers), never the
// engine, keeping the Apache-2.0 boundary clean (LICENSING.md).
package github
