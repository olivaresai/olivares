// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package connectors is the root of the first-party and community connectors
// (Claude/OTEL, MCP, pg-audit, eBPF, cloud, model-provider, identity, output…).
// Each connector lives in its own subpackage and imports only from the SDK,
// never from core, keeping the Apache-2.0/AGPL boundary clean.
//
// Empty at bootstrap; connectors are added from session onward.
package connectors
