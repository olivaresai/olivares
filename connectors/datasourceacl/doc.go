// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package datasourceacl is the OPEN (Apache-2.0) seam for the enterprise
// live-ACL-sync and Microsoft Purview sensitivity-label integration add-on
//. It carries INTERFACES and TYPES only — no implementation.
//
// The enterprise add-on (LicenseRef-Olivares-Commercial, //go:build enterprise,
// private repo) implements LiveACLSyncer and PurviewClassifier against this
// seam. The base connectors (sapodata, salesforce, snowflake, azureaisearch)
// sync ACL in batch via their LiveSource.FetchACL; this add-on upgrades to
// near-real-time webhook/polling and adds Purview label propagation.
//
// This package depends only on contentsource and the standard library, so it
// crosses the connector boundary without dragging in /core.
package datasourceacl
