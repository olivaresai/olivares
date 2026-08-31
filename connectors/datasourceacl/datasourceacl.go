// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package datasourceacl

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
)

// LiveACLSyncer is the interface the enterprise add-on implements to provide
// near-real-time ACL synchronization from a data source. The base connectors
// sync ACL in batch (LiveSource.FetchACL); a LiveACLSyncer upgrades to
// webhook-driven or high-frequency polling with exponential backoff.
//
// The implementation is in the private enterprise repo behind
// //go:build enterprise. This interface is the open seam.
type LiveACLSyncer interface {
	// StartWatch begins watching a source for ACL changes. The watcher calls
	// back via the provided Callback when it detects a change. It honors ctx
	// for cancellation.
	StartWatch(ctx context.Context, source contentsource.Source, cfg WatchConfig, cb Callback) error
	// Stop halts the watcher. Safe to call multiple times.
	Stop() error
}

// Callback is invoked by a LiveACLSyncer when it detects an ACL change on a
// document. The implementation must be safe for concurrent calls.
type Callback func(ctx context.Context, change ACLChange)

// ACLChange describes one ACL mutation detected by the live watcher.
type ACLChange struct {
	// SourceKind identifies the data source that changed.
	SourceKind contentsource.SourceKind
	// DocID is the source's stable document identifier.
	DocID string
	// ACL is the new ACL for the document (replaces the previous one).
	ACL []string
	// ExternalLabels is the new set of external sensitivity labels.
	ExternalLabels []string
	// Classification is the new classification value.
	Classification string
	// DetectedAt is when the watcher observed the change (UTC).
	DetectedAt time.Time
}

// WatchConfig configures a LiveACLSyncer.
type WatchConfig struct {
	// PollInterval is the base interval between polling cycles. The watcher
	// applies exponential backoff on transient errors and resets on success.
	PollInterval time.Duration
	// MaxBackoff caps the exponential backoff duration.
	MaxBackoff time.Duration
	// CredentialRef is the secret-store reference for the watcher's own
	// credential (never an inline secret).
	CredentialRef string
}

// PurviewLabel represents a Microsoft Purview sensitivity label.
type PurviewLabel struct {
	// ID is the Purview label GUID.
	ID string
	// Name is the display name (e.g. "Highly Confidential").
	Name string
	// Tooltip is the operator-facing description.
	Tooltip string
	// Priority determines precedence when multiple labels apply (lower wins).
	Priority int
}

// PurviewClassifier reads Microsoft Purview sensitivity labels and maps them
// to contentsource.Document.ExternalLabels entries ("purview:<label>"). The
// enterprise add-on implements this against the Purview REST API.
type PurviewClassifier interface {
	// Classify returns the external labels for a document identified by docID.
	// The labels are in the "purview:<lowercase_name>" format.
	Classify(ctx context.Context, docID string) ([]string, error)
	// ListLabels returns all sensitivity labels available in the tenant.
	ListLabels(ctx context.Context) ([]PurviewLabel, error)
}

// PurviewConfig configures a PurviewClassifier.
type PurviewConfig struct {
	// Endpoint is the Purview API endpoint (e.g.
	// "https://api.informationprotection.azure.com").
	Endpoint string
	// TenantID is the Azure AD tenant.
	TenantID string
	// CredentialRef is the secret-store reference for the Purview API
	// credential (never an inline secret).
	CredentialRef string
}
