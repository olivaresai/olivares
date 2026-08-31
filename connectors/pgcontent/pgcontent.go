// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgcontent

import (
	"context"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.pg-content"

// Source is the PostgreSQL knowledge content source. The zero value is not usable;
// call New. It embeds content.Store to serve the export (snapshot) mode, and holds a
// read-only liveClient for the live mode.
type Source struct {
	content.Store
	sc   *sourceConfig
	live *liveClient
}

var (
	_ contentsource.Source     = (*Source)(nil)
	_ contentsource.LiveSource = (*Source)(nil)
)

// New returns a PostgreSQL content source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema. Every
// credential field is Secret (resolved from the secret store by reference, never
// inline): `dsn` and `password_ref` carry connection secrets, `credential_ref` is a
// validated reference belt.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "PostgreSQL (governed content)",
		Surfaces:    []string{"knowledge.document"},
		Description: "Ingests rows from a PostgreSQL database as governed knowledge documents (read-only by construction, with declared per-row ACL and per-column classification). Distinct from the pgaudit R/RW access-audit connector, and NOT NL-to-SQL — it materializes rows as content.",
		ConfigFields: []sdk.ConfigField{
			{Key: fMode, Type: sdk.FieldString, Default: "export", Description: "\"export\" (default; a static row snapshot) or \"live\" (a read-only database connection)"},
			{Key: fDSN, Type: sdk.FieldString, Secret: true, Description: "full libpq/URL connection string incl. password (live mode); resolved from the secret store, never inline. Alternative to the discrete host/port/... fields"},
			{Key: fHost, Type: sdk.FieldString, Description: "database host (live mode, when not using dsn)"},
			{Key: fPort, Type: sdk.FieldString, Default: "5432", Description: "database port"},
			{Key: fDBName, Type: sdk.FieldString, Description: "database name"},
			{Key: fUser, Type: sdk.FieldString, Description: "least-privilege read-only role (documented: GRANT SELECT only)"},
			{Key: fPasswordRef, Type: sdk.FieldString, Secret: true, Description: "password for the read-only role; resolved from the secret store, never inline"},
			{Key: fSSLMode, Type: sdk.FieldString, Default: "require", Description: "libpq sslmode (disable|require|verify-ca|verify-full)"},
			{Key: fCredentialRef, Type: sdk.FieldString, Secret: true, Description: "optional secret-store reference (e.g. vault:secret/pg#password); a cleartext secret is rejected at Open"},
			{Key: fSchema, Type: sdk.FieldString, Default: "public", Description: "the schema of the table (or the schema label for a query)"},
			{Key: fTable, Type: sdk.FieldString, Description: "the table to materialize as documents (mutually exclusive with query)"},
			{Key: fQuery, Type: sdk.FieldString, Description: "a read-only SELECT to materialize as documents (validated SELECT-only; mutually exclusive with table)"},
			{Key: fKeyColumns, Type: sdk.FieldString, Description: "comma-separated key column(s) forming the row's stable document id (required)"},
			{Key: fBodyColumns, Type: sdk.FieldString, Description: "comma-separated column(s) concatenated into the document body (required)"},
			{Key: fTitleColumn, Type: sdk.FieldString, Description: "column for the document title (defaults to the id)"},
			{Key: fContentType, Type: sdk.FieldString, Default: "text/plain", Description: "MIME-ish content type of the body"},
			{Key: fUpdatedAtColumn, Type: sdk.FieldString, Description: "timestamp column driving incremental (delta) sync; absent ⇒ full reconciliation each pass"},
			{Key: fACLColumns, Type: sdk.FieldString, Description: "comma-separated column(s) whose values become the row's ACL references (honest mapping; empty ⇒ inherit the KB default ACL)"},
			{Key: fACLPrefix, Type: sdk.FieldString, Default: "group:", Description: "prefix applied to each ACL column value (e.g. group:)"},
			{Key: fClassColumn, Type: sdk.FieldString, Description: "column holding the row's sensitivity classification label"},
			{Key: fSensitiveColumns, Type: sdk.FieldString, Description: "comma-separated column(s) that are sensitive; a value in one adds an external label \"<sensitive_label>:<column>\" (per-column classification → retrieval DLP)"},
			{Key: fSensitiveLabel, Type: sdk.FieldString, Default: "pii", Description: "the external-label prefix for sensitive columns"},
			{Key: fMetadataColumns, Type: sdk.FieldString, Description: "comma-separated column(s) surfaced as non-sensitive provenance attributes"},
			{Key: fStatementTimeout, Type: sdk.FieldDuration, Default: "30s", Description: "per-statement timeout enforced on the read-only session"},
			{Key: fMaxRows, Type: sdk.FieldString, Default: "100000", Description: "maximum rows the connector ingests per source (bounds a full scan)"},
			{Key: fSpaceRef, Type: sdk.FieldString, Description: "logical container label for provenance (defaults to postgres:<schema>.<table>)"},
			{Key: fExportPath, Type: sdk.FieldString, Description: "path to a row-snapshot JSON file or a directory of *.json files (export mode)"},
		},
	}
}

// Kind declares this source ingests knowledge documents.
func (s *Source) Kind() contentsource.ContentClass { return contentsource.ClassDocument }

// Open validates configuration and wires either the read-only live client (live mode)
// or the parsed snapshot (export mode). A live source with no connection configured,
// or an export source with no path, opens successfully as an EMPTY source (declared
// offline, never a hard failure) — the contentsource contract.
func (s *Source) Open(ctx context.Context, cfg sdk.Config) error {
	sc, err := parseConfig(cfg)
	if err != nil {
		return err
	}
	s.sc = &sc
	if sc.mode == modeLive {
		if !sc.hasConnection() {
			s.SetDocs(nil)
			return nil
		}
		lc, err := newLiveClient(ctx, &sc)
		if err != nil {
			return err
		}
		s.live = lc
		s.SetDocs(nil)
		return nil
	}
	if sc.exportPath == "" {
		s.SetDocs(nil)
		return nil
	}
	docs, err := sc.parseExport()
	if err != nil {
		return err
	}
	s.SetDocs(docs)
	return nil
}

// List returns one page of document references (honoring ctx). Read-only.
func (s *Source) List(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if s.live != nil {
		return s.live.list(ctx, cursor)
	}
	return s.Store.List(cursor)
}

// Fetch returns one document by id (honoring ctx). The body is raw; the module
// redacts it. Read-only.
func (s *Source) Fetch(ctx context.Context, docID string) (contentsource.Document, error) {
	if err := ctx.Err(); err != nil {
		return contentsource.Document{}, err
	}
	if s.live != nil {
		return s.fetchLive(ctx, docID)
	}
	return s.Store.Fetch(docID)
}

// fetchLive re-reads the document from the live database. It is the named LiveSource
// dispatch path the connector-inventory guard (scripts/check-connectors.sh) requires a
// LiveSource to route through before any export-store fallback, so a "live" source can
// never silently serve only the stale materialized copy.
func (s *Source) fetchLive(ctx context.Context, docID string) (contentsource.Document, error) {
	return s.live.fetch(ctx, docID)
}

// Close releases the connection pool (live mode); the export store holds none.
func (s *Source) Close(context.Context) error {
	if s.live != nil {
		s.live.close()
	}
	return nil
}

// DeltaList reports rows changed since sinceToken for incremental sync
// (contentsource.LiveSource). It requires live mode and a configured updated-at
// column; otherwise it returns an error and the module falls back to full-list
// reconciliation (mirrors the s3content precedent).
func (s *Source) DeltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	if s.live == nil {
		return contentsource.DeltaPage{}, errors.New("pgcontent: DeltaList requires live mode")
	}
	if s.sc.updatedAtCol == "" {
		return contentsource.DeltaPage{}, errors.New("pgcontent: DeltaList requires updated_at_column")
	}
	return s.live.deltaList(ctx, sinceToken)
}

// FetchACL re-reads only a document's ACL/classification (contentsource.LiveSource).
func (s *Source) FetchACL(ctx context.Context, docID string) (contentsource.ACLResult, error) {
	if s.live == nil {
		return contentsource.ACLResult{}, errors.New("pgcontent: FetchACL requires live mode")
	}
	return s.live.fetchACL(ctx, docID)
}

// Discover introspects a schema's tables and columns (live, read-only) so an operator
// can choose what to materialize. It requires live mode.
func (s *Source) Discover(ctx context.Context, schema string) (Discovery, error) {
	if s.live == nil {
		return Discovery{}, errors.New("pgcontent: Discover requires live mode")
	}
	if schema == "" {
		schema = s.sc.schema
	}
	if !validIdent(schema) {
		return Discovery{}, fmt.Errorf("pgcontent: invalid schema %q", schema)
	}
	return s.live.discover(ctx, schema)
}
