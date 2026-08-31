// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package s3cloudtrail is the Olivares AI source connector that captures
// read/write access to Amazon S3 from AWS CloudTrail (ARCHITECTURE.md,
// docs/contracts). It reads CloudTrail log files — the classic
// {"Records":[…]} object, gzipped or plain, or newline-delimited records — and
// emits one model.EdgeObservation per S3 event.
//
// The read/write mode is taken verbatim from CloudTrail's own readOnly field,
// never inferred. The identity is the IAM principal CloudTrail attributes the
// call to (an IAM user, an assumed-role session, an AWS service); an assumed
// role shared across many callers is marked approximate so the identity↔agent
// resolution is left to module VI. Only S3 events (eventSource
// s3.amazonaws.com) are processed; other CloudTrail feeds are cloud discovery
//.
//
// The connector is read-only over the log files and never calls AWS. It imports
// only the SDK and connector-internal helpers, never the engine (LICENSING.md).
package s3cloudtrail
