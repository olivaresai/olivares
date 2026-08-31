// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package auth is the engine's authentication and authorization core. It
// owns:
//
//   - Principals: the authenticated identity of a request (a human session or a
//     programmatic API token), carrying the tenant→role grants and the superadmin
//     flag that authorization reads.
//   - Credentials: opaque, revocable, server-side tokens (a public selector plus a
//     secret whose SHA-256 is compared in constant time) and argon2id password
//     hashing. No JWT for first-party auth — opaque tokens are revocable and carry
//     no secret-bearing claims (docs/SECURITY-HARDENING.md). JWT lives only behind the SSO seam.
//   - Authorization: a built-in RBAC engine (owner/admin/editor/viewer + a
//     superadmin user flag) with an ABAC PolicyEvaluator seam that can only
//     further restrict an RBAC grant (fail-closed; OPA slots in here).
//   - The Authenticator: resolves a bearer credential to a Principal against the
//     store's auth partition, performs login with per-account/per-IP throttling,
//     and flattens user-enumeration timing.
//   - The Federation (SSO) seam: the open-core single-IdP OIDC/SAML provider in
//     core/auth/federation implements it and links into the base build, so
//     go-oidc / crewjam-saml ARE in core's dependency tree; multi-IdP, enforcement
//     and managed SCIM stay enterprise (LICENSING.md). NoFederation is the default.
//
// Tenant isolation is enforced by binding the store Scope to the request's
// resolved-and-authorized tenant (RLS/triggers are the backstop); this
// package decides WHO a request is and WHAT it may do, never reaching the store
// other than through the engine's auth partition.
package auth
