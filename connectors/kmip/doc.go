// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package kmip is the Olivares AI connector for an on-prem OASIS KMIP v2.1
// key-management server (Thales, Entrust, Fortanix, PyKMIP, …).
// It INVENTORIES the server's key objects read-only and never touches key material.
//
// It is a PURE-GO TTLV/TLS client (no cgo): it speaks the KMIP binary
// Tag-Type-Length-Value encoding over a mutually-authenticated TLS connection
// (port 5696) and issues ONLY two operations — Locate (discover the object ids) and
// GetAttributes (read each object's non-secret attributes: Object Type,
// Cryptographic Algorithm, Cryptographic Length, State, Name). It NEVER issues Get
// (which returns key material), Create, Register, Destroy, Revoke or any mutating
// operation — those op codes are not even compiled in. The minimal-data rule
// (docs/SECURITY-HARDENING.md-3) holds by construction: GetAttributes returns metadata, not the key.
//
// What it produces:
//   - Snapshot (inventory): the KMIP server as a secret_store NHI
//     (Ref "kmip:<endpoint>") — a governed custodian in the unified inventory.
//   - Gather: one custody EdgeObservation per discovered key object — the server
//     (identity) "holds" a kmip.key resource ("kmip:<endpoint>/<uid>"), Mode
//     ModeUnknown (custody is existence, not an R/RW access — the map can filter
//     Source "kmip" out of the least-privilege access diff), ToolRef carrying the
//     object's type/algorithm/state descriptor.
//
// Honest seams: the byte layout of the KMIP 2.1 Attributes structure
// (each attribute as an independently-tagged item) and the Name sub-tags were not
// confirmable verbatim from the spec body, so the parser reads the attribute tags
// it DID confirm (Object Type, Cryptographic Algorithm/Length, State, Name) and
// ignores the rest rather than fabricate; a key object whose attributes are all
// unknown still appears in the inventory by its unique identifier. Richer per-object
// attribute inventory beyond type/algorithm/state would need a structured resource
// model (the EdgeObservation contract carries no attribute map) — a foundation
// decision, noted not faked. Port 5696 is the IANA assignment (KMIP Profiles).
//
// It imports only the SDK, the Apache identitysource/tlsx helpers and the standard
// library — never the engine (/core).
package kmip
