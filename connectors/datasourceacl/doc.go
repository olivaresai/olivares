// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package datasourceacl is the OPEN (Apache-2.0) seam for the enterprise
// live-ACL-sync and Microsoft Purview sensitivity-label integration add-on
//. It carries INTERFACES and TYPES only — no implementation.
//
// The enterprise add-on (LicenseRef-Olivares-Commercial, //go:build enterprise,
// private repo) was to implement LiveACLSyncer and PurviewClassifier against
// this seam; it was never built (see the deprecation note below). The base
// connectors (sapodata, salesforce, snowflake, azureaisearch) sync ACL in
// batch via their LiveSource.FetchACL; the add-on would have upgraded that to
// near-real-time webhook/polling and added Purview label propagation.
//
// This package depends only on contentsource and the standard library, so it
// crosses the connector boundary without dragging in /core.
//
// Deprecated: nothing implements or imports this seam. The enterprise add-on it
// was carved out for (live ACL sync, Purview labels) was never built: no
// importer in either distribution, and no
// open issue plans one (measured 2026-10-06). Batch ACL sync through
// contentsource.LiveSource.FetchACL is the supported mechanism. The package is
// kept only because every public tag since v26.8.0 shipped it.
package datasourceacl
