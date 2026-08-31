// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package azureblobaudit is the Olivares AI source connector that captures
// read/write access to Azure Blob Storage from the platform's native
// StorageBlobLogs diagnostic/resource logs (ARCHITECTURE.md, docs/contracts). It
// completes the tri-cloud data-store parity alongside s3-cloudtrail (AWS) and the
// GCS connector. It reads the exported log line by line (one JSON object per
// line) and emits one model.EdgeObservation per blob access.
//
// Read-first, minimal data: the connector reads an exported audit file the
// operator ships from an Azure Storage diagnostic setting (resource logs routed
// to a storage account / event hub, exported as line-delimited JSON). It NEVER
// opens a connection to the storage account, never authenticates to Azure, never
// writes anywhere. Its only I/O is a read-only tail of the exported file.
//
// Identities, not credentials: the access is attributed to the AAD application /
// service principal the log records (identity.requester.appId), or — when no
// OAuth requester is present — to the authentication type (identity.type). The
// connector emits only that raw identifier; it never emits or logs the request
// URI's query string, a token, a token hash, a body, or any payload. The
// identity↔agent bridge is module VI's job: a requester declared shared in
// the shared_accounts config drops Confidence to approximate, but the raw
// identity is always emitted.
//
// R/RW is verbatim from the source. The mode is decided first by operationName —
// GetBlob/GetBlobProperties/ListBlobs => read; PutBlob/PutBlock/PutBlockList/
// SetBlobMetadata => write; DeleteBlob => write — and, for an operationName the
// connector does not recognize, it falls back to the log's own category:
// StorageRead => read, StorageWrite => write, StorageDelete => write. If neither
// classifies the access, Mode is model.ModeUnknown (explicit, never guessed). The
// resource is parsed from the uri ("account/container/blob" => azureblob.object,
// or "account/container" => azureblob.container). ObservedAt is the record's own
// `time`, parsed and normalized to UTC, never time.Now().
//
// It imports only the SDK and connector-internal helpers, never the engine,
// keeping the Apache-2.0 boundary clean (LICENSING.md).
package azureblobaudit
