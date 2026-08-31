// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package mongoaudit is the mongo-audit source connector. It captures per-collection
// R/RW access from the MongoDB native audit log and emits one EdgeObservation per
// authorized data access. It is part of the clean-tier R/RW map (docs/contracts):
// the raw access classification comes verbatim from MongoDB, never guessed.
//
// # Security posture
//
//   - Read-first: the connector reads MongoDB's own audit log FILE. The operator
//     configures the server with the audit log destination set to a file in JSON
//     format (auditLog.destination=file, auditLog.format=JSON) and ships that file
//     to the path this connector tails. The connector NEVER opens a connection to
//     mongod/mongos, never runs a command, and never writes anywhere — the only I/O
//     is a read-only tail of the exported audit file (docs/SECURITY-HARDENING.md).
//   - Minimal data: the record struct declares ONLY the fields needed to build the
//     edge — atype, ts, users, and the param.command + param.ns. It deliberately
//     does NOT declare param.args (the command body: filters, documents, query
//     arguments). The query/command payload is never read, never retained, never
//     emitted (docs/SECURITY-HARDENING.md). The connector emits only the edge: origin identity,
//     resource, mode, source, confidence, tool, timestamp.
//   - Identities, not credentials: OriginKind is always "identity" (the audit
//     attributes an access to a MongoDB user account, not to a resolved agent; the
//     identity↔agent bridge is module VI /). The raw identity (users[0] as
//     "user@db") is emitted; a credential value never is.
//
// # The platform
//
// MongoDB Community/Enterprise (and Percona Server for MongoDB) emit a system-event
// audit log. The audited action this connector consumes is authCheck: an
// authorization check MongoDB performs for an operation. With the server parameter
// auditAuthorizationSuccess enabled, the audit log records authorized accesses
// (result==0) as well as denied ones — that authorized stream is the per-collection
// R/RW signal. Each authCheck line carries the operation (param.command), the target
// namespace (param.ns = "db.collection"), the acting user(s) (users[]), and the
// MongoDB error code (result: 0 = authorized; non-zero, e.g. 13 Unauthorized = denied).
//
// # Re-classification: degraded -> clean-tier (the differentiator)
//
// MongoDB was originally written off as a degraded/lossy store (docs/contracts
// listed Mongo among "lossy/impossible-passive" stores). That assessment was wrong for
// the authCheck stream. An authCheck record gives, WITHOUT any payload:
//
//   - the operation performed (param.command: find/insert/update/…),
//   - the exact namespace touched (param.ns: db.collection), and
//   - the acting identity (users[]).
//
// That is precisely operation + resource + identity — everything needed for an honest
// per-collection R/RW edge, classified VERBATIM by MongoDB's own command vocabulary,
// with no statement/regex guessing and no payload. So mongo-audit is RE-CLASSIFIED to
// clean-tier, per-collection, on par with pgAudit and MariaDB TABLE events. The only
// honest caveats are operational, not classificatory: the operator must enable
// auditAuthorizationSuccess (else only denials are logged, and a denial is not an
// access), and an authCheck without a namespace degrades the resource to the database.
//
// # R/RW mapping (verbatim from param.command)
//
//	find / aggregate / count / distinct / getMore / listCollections / listIndexes  -> read
//	insert / update / delete / findAndModify / create / createIndexes /
//	  drop / dropDatabase / renameCollection                                       -> write
//	any other command                                                              -> unknown (explicit, never guessed)
//
// Resource kind is "mongo.collection" (ResourceRef = the full namespace
// "db.collection"); if param.ns carries no collection part, it degrades to
// "mongo.database". Only authCheck records with result==0 (authorized) are emitted: a
// denied authorization check (non-zero result) means the operation did NOT touch the
// resource, so it is not an access edge and is dropped.
//
// # Identity & the shared-service-account problem
//
// OriginRef is the first acting user as "user@db" (users[0]). A record with no user is
// skipped (no attributable identity). When the operator declares "user" or "user@db"
// as a shared account (the shared_accounts CSV config), the raw identity is still
// emitted but Confidence drops to approximate — the attribution to an agent is
// ambiguous, which resolves (docs/contracts).
package mongoaudit
