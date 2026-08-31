// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package bedrockkb is the read-only Amazon Bedrock Knowledge Bases governance
// connector. It OBSERVES Bedrock KB retrieval — it does NOT store, index or embed
// documents; Bedrock KB manages its own vector store (OpenSearch Serverless, Aurora
// pgvector, Pinecone, Redis, etc.). The connector is the governance bridge that lets
// an operator who already uses Bedrock KB incorporate their retrieval into Olivares's
// governance chain without migrating data.
//
// # What it does
//
// On each Gather pass it calls the Bedrock Agent Runtime Retrieve API on the
// configured Knowledge Base(s), using a health-check query, to:
//
//  1. Verify connectivity and authentication to each KB.
//  2. Observe the retrieval configuration (number of results, source URIs, scores).
//  3. Emit a FindingReport per KB with the observed retrieval health/posture.
//  4. Emit an EdgeObservation linking the KB to the discovered data sources.
//
// # What it does NOT do
//
//   - It does NOT intercept real-time retrieval calls (that is an inference proxy
//     concern, not a batch source connector).
//   - It does NOT apply DLP or content filtering on retrieved chunks (that is the
//     knowledge module's RetrievalGuard seam, applied at query time).
//   - It does NOT call RetrieveAndGenerate (which invokes a model — the observation
//     should never trigger billable inference).
//   - It never reads the full document content, only chunk excerpts returned by
//     Retrieve (and never logs them — minimal data).
//
// # License boundary
//
// This connector is Apache-2.0 and imports only the SDK and standard library
// (plus connectors/internal/awssig for SigV4). It never imports /core or /modules.
// The governance integration (DLP, audit, policy) lives in the composition root
// (cmd/olivares, AGPL).
package bedrockkb
