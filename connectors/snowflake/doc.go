// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package snowflake is the Snowflake data warehouse knowledge DATA connector: it
// ingests content from Snowflake tables and views for governed knowledge,
// read-only, with RBAC grant-based ACL as provenance permissions.
//
// It implements contentsource.Source and contentsource.LiveSource. In live mode
// it connects via the Snowflake SQL REST API
// (https://{account}.snowflakecomputing.com/api/v2/statements) using key-pair
// authentication (JWT). DeltaList uses a configurable timestamp column for
// incremental sync. FetchACL queries SHOW GRANTS to extract the Snowflake RBAC
// hierarchy. Credentials are by secret-store reference only (docs/SECURITY-HARDENING.md section 3).
// Imports only the SDK + the shared content helper, never /core.
package snowflake
