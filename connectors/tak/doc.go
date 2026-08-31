// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package tak governs a TAK (Team Awareness Kit) deployment as one more surface:
// it inventories a TAK Server's posture and ingests Cursor-on-Target (CoT) events
// as a governed signal, with the same scoping granularity as any other source.
//
// # Clean-room provenance (read this before touching cot.go)
//
// The CoT wire format implemented here was written from the PUBLIC-RELEASE MITRE
// SPECIFICATION only. No TAK or ATAK source code was read, copied, translated or
// derived from, at any point, by any contributor to this package.
//
// Specification sources used (both "Approved for Public Release; Distribution
// Unlimited", re-verified against the living documents on 2026-07-09):
//
//   - "The Developer's Guide to Cursor on Target", Mike Butler, MITRE Technical
//     Report, August 2005. DTIC accession ADA637348, MITRE Case #06-0249.
//   - The CoT base-event schema `Event-PUBLIC.xsd` ("Schema for Cursor-On-Target
//     (CoT) Event data model (Version 2.0), 13-June-2003"), Copyright (c) 2005 The
//     MITRE Corporation, MITRE Case #11-3895.
//   - "TAK Server Configuration Guide" v5.2 (July 2024), for the input/listener
//     port and protocol conventions asserted in config.go.
//
// Deliberately NOT used, and off-limits to this package:
//
//   - github.com/deptofdefense/AndroidTacticalAssaultKit-CIV (ATAK-CIV) — GPLv3
//     (LICENSE.md is the GPLv3 text; archived 2025-05-02).
//   - github.com/TAK-Product-Center/Server (TAK Server) — GPLv3.
//
// Both projects carry a U.S. Federal "Distribution A" release marking, which is a
// government release statement and NOT a software license: the license on both
// code trees is GPLv3. Olivares connectors are Apache-2.0 and never link against
// copyleft code (LICENSING.md, scripts/check-boundary.sh). The CoT *format* is
// separable from the TAK *implementations*: the schema and the developer's guide
// sit in MITRE's public-release regime, which is precisely what makes a clean-room
// implementation legitimate. MITRE retains copyright in the schema text itself, so
// this package implements the format and cites the source; it does not reproduce
// the XSD or the guide's prose.
//
// # What this connector does
//
//   - Posture: reads a TAK Server's CoreConfig.xml (offline, from disk) and reports
//     its configured inputs (protocol, port, anonymous-group exposure), TLS/keystore
//     configuration (legacy TLS version, default keystore password, missing CRL) and
//     certificate-signing backend as minimal-data findings. An optional live probe of
//     the server's version endpoint adds a version/reachability finding — the ONLY
//     thing this connector reads over the network. Credentials for that probe are
//     supplied by reference (mTLS client certificate), never inline. With neither a
//     config path nor a probe configured it is an honest no-op: it emits nothing
//     rather than fabricate a clean posture.
//
//   - CoT ingest: listens for CoT events over UDP (one message per datagram) and
//     TCP ("open-squirt-close": one message per connection), parses the base event
//     schema strictly, and lifts each event into a governed access edge plus, where
//     warranted, a finding. Payload detail never leaves the connector: positions and
//     free-form <detail> are reduced to a digest, and the emitting UID is hashed.
//
// # Honest limits
//
// This connector does NOT: speak mesh/radio bearers; implement or host ATAK/WinTAK
// plugins; read or score TAK federation (the <federation> CoreConfig element is
// deliberately not modeled — see wire.go); call the Marti management API for
// anything beyond the optional version probe; implement Link-16 or any MIL-STD
// certification-gated protocol; or carry an Iron Bank / DoD accreditation. Those are
// separate, optional paths for the customer.
// The CoT <detail> sub-schema is intentionally not modeled: only the base event is
// parsed, and detail is treated as opaque, size-capped bytes.
package tak
