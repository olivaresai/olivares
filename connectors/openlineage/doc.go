// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package openlineage is a read-only SourceConnector that enriches the R/RW
// access map (module III) with DATA-FLOW lineage: which job read which
// dataset and wrote which other dataset, observed from the run itself.
//
// # The platform
//
// OpenLineage (https://openlineage.io, a Linux Foundation graduate project) is
// an open standard for collecting lineage metadata from data pipelines. A run of
// a job emits RunEvents (START / RUNNING / COMPLETE / FAIL / ABORT / OTHER), and
// a terminal COMPLETE event carries the datasets the run actually read (inputs)
// and wrote (outputs). These are OBSERVED edges — a run truly touched the
// dataset — so they belong on the observed side of the PERMITTED↔OBSERVED diff,
// exactly like pgAudit/CloudTrail edges (docs/contracts).
//
// # Ingest path (honest)
//
// v1 is a FILE reader: the operator configures the OpenLineage file transport to
// append RunEvents to a file as newline-delimited JSON (one event per line), and
// this connector tails that file. An HTTP receiver that accepts events on the
// OpenLineage transport endpoint directly is a documented follow-up — this
// connector does NOT open a network listener (no port is bound), it reads the
// events file the operator ships.
//
// # Security posture (docs/SECURITY-HARDENING.md, docs/contracts)
//
//   - Read-first / read-only: the connector only reads (tails) the events file.
//     It never connects to a metadata server, a warehouse, or any data store, and
//     never writes anywhere. The I/O is read-only over the events file.
//   - Minimal data: it emits only the access edge (origin, resource, mode,
//     source, confidence, toolref, ts). The record structs declare ONLY the
//     fields read — the run's facets, schema, column-level lineage, SQL and any
//     job payload are NOT parsed and never traverse a wire field.
//   - Identities, not credentials: OriginKind is always "identity" — the pipeline
//     job (namespace/name) is a non-human identity, never a resolved agent. The
//     identity↔agent bridge is module VI's job; this connector never invents
//     an attribution the lineage does not give.
//
// # R/RW mapping (verbatim from the run's direction)
//
// Only a terminal COMPLETE event (or an empty eventType, treated as complete) is
// emitted, so a START+COMPLETE pair is not double-counted. For such an event:
//
//	each entry in inputs  => edge  job -> input dataset   Mode = read
//	each entry in outputs => edge  job -> output dataset  Mode = write
//
// The direction IS the classification — an input is read, an output is written —
// taken verbatim from the run's own inputs/outputs lists, never guessed. (Modes
// other than read/write do not arise from this shape, so no ModeUnknown edge is
// emitted here; non-terminal events are skipped entirely, not emitted as unknown.)
//
//	OriginKind   = "identity"
//	OriginRef    = job.namespace + "/" + job.name      (the pipeline NHI)
//	ResourceKind = "openlineage.dataset"
//	ResourceRef  = dataset.namespace + "/" + dataset.name
//	ToolRef      = eventType                            (e.g. "COMPLETE")
//	ObservedAt   = eventTime (parsed, normalized to UTC)
//	Source       = "openlineage"
//	Confidence   = approximate if the job (OriginRef) is a declared shared
//	               account, attributed otherwise (docs/contracts)
package openlineage
