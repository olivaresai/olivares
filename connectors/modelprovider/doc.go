// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package modelprovider defines provider catalogs, cost samples and HTTP clients.
// CatalogProvider exposes reference data separately from SDK CostSample observations.
// Key and workspace inventory contains metadata, never usable credentials.
//
// Client performs read-only API requests. InferenceClient and ChatTextTransport send
// caller-supplied content; callers govern its persistence and permitted destination.
// Credentials stay in memory and are never logged or persisted by these clients.
//
// The package stays within the Apache-2.0 connector/SDK boundary and never imports core.
package modelprovider
