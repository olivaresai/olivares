// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgcontent

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/content"
	"github.com/olivaresai/olivares/sdk"
)

// Config field keys the Descriptor declares.
const (
	fMode             = "mode"
	fDSN              = "dsn"
	fHost             = "host"
	fPort             = "port"
	fDBName           = "dbname"
	fUser             = "user"
	fSSLMode          = "sslmode"
	fPasswordRef      = "password_ref"
	fCredentialRef    = "credential_ref"
	fSchema           = "schema"
	fTable            = "table"
	fQuery            = "query"
	fKeyColumns       = "key_columns"
	fBodyColumns      = "body_columns"
	fTitleColumn      = "title_column"
	fContentType      = "content_type"
	fUpdatedAtColumn  = "updated_at_column"
	fACLColumns       = "acl_columns"
	fACLPrefix        = "acl_prefix"
	fClassColumn      = "classification_column"
	fSensitiveColumns = "sensitive_columns"
	fSensitiveLabel   = "sensitive_label"
	fMetadataColumns  = "metadata_columns"
	fStatementTimeout = "statement_timeout"
	fMaxRows          = "max_rows"
	fSpaceRef         = "space_ref"
	fExportPath       = "export_path"
)

// Defaults.
const (
	defaultSchema           = "public"
	defaultContentType      = "text/plain"
	defaultACLPrefix        = "group:"
	defaultSensitiveLabel   = "pii"
	defaultStatementTimeout = 30 * time.Second
	defaultMaxRows          = 100_000
	// maxRowsCeiling caps max_rows so a misconfiguration cannot ask the connector to
	// buffer an unbounded result set.
	maxRowsCeiling = 5_000_000
)

// sourceConfig is the parsed, validated declarative document definition: how a row
// of the configured table (or SELECT) becomes a knowledge Document, plus the
// connection and the read-only safety bounds. Every identifier it holds has passed
// validIdent, so building SQL from it cannot inject.
type sourceConfig struct {
	mode string // "export" | "live"

	// Connection (live mode). dsn (secret) wins; else host/port/dbname/user + password.
	dsn        string
	host       string
	port       string
	dbname     string
	user       string
	sslmode    string
	password   string // resolved from password_ref by the engine's secret resolver
	credential string // credential_ref, validated as a reference (never inline)

	schema string
	table  string // validated identifier; mutually exclusive with query
	query  string // validated SELECT-only; mutually exclusive with table

	keyColumns   []string
	bodyColumns  []string
	titleColumn  string
	contentType  string
	updatedAtCol string

	aclColumns   []string
	aclPrefix    string
	classColumn  string
	sensitiveCol []string
	sensitiveLbl string
	metadataCol  []string

	statementTimeout time.Duration
	maxRows          int
	spaceRef         string

	exportPath string
}

// parseConfig reads and validates the operator's settings into a sourceConfig. It is
// deny-closed: a missing required field or an unsafe identifier is an error at Open,
// never a silent empty source (a source that legitimately has no credential opens as
// an empty source — that is handled by the caller, not treated as invalid config).
func parseConfig(cfg sdk.Config) (sourceConfig, error) {
	get := func(k string) string { return strings.TrimSpace(cfg.Get(k)) }

	sc := sourceConfig{
		mode:         normalizeMode(get(fMode)),
		dsn:          get(fDSN),
		host:         get(fHost),
		port:         get(fPort),
		dbname:       get(fDBName),
		user:         get(fUser),
		sslmode:      get(fSSLMode),
		password:     cfg.Get(fPasswordRef), // resolved secret value; not trimmed
		credential:   get(fCredentialRef),
		schema:       orDefault(get(fSchema), defaultSchema),
		table:        get(fTable),
		query:        strings.TrimSpace(cfg.Get(fQuery)),
		titleColumn:  get(fTitleColumn),
		contentType:  orDefault(get(fContentType), defaultContentType),
		updatedAtCol: get(fUpdatedAtColumn),
		aclPrefix:    orDefault(get(fACLPrefix), defaultACLPrefix),
		classColumn:  get(fClassColumn),
		sensitiveLbl: orDefault(get(fSensitiveLabel), defaultSensitiveLabel),
		spaceRef:     get(fSpaceRef),
		exportPath:   get(fExportPath),
	}
	sc.keyColumns = splitList(get(fKeyColumns))
	sc.bodyColumns = splitList(get(fBodyColumns))
	sc.aclColumns = splitList(get(fACLColumns))
	sc.sensitiveCol = splitList(get(fSensitiveColumns))
	sc.metadataCol = splitList(get(fMetadataColumns))

	// credential_ref must be a reference, never an inline secret (belt; the engine's
	// resolver is the authority). password_ref is resolved to a value by the engine,
	// so it is NOT run through the reference validator here.
	if msg := content.ValidateCredentialRef(sc.credential); msg != "" {
		return sourceConfig{}, errors.New("pgcontent: " + msg)
	}

	// statement_timeout / max_rows bounds.
	to, err := parseTimeout(get(fStatementTimeout))
	if err != nil {
		return sourceConfig{}, err
	}
	sc.statementTimeout = to
	rows, err := parseMaxRows(get(fMaxRows))
	if err != nil {
		return sourceConfig{}, err
	}
	sc.maxRows = rows

	if err := sc.validate(); err != nil {
		return sourceConfig{}, err
	}
	return sc, nil
}

// validate enforces the document-definition and identifier-safety rules. Every
// schema/table/column identifier MUST be a safe SQL identifier (validIdent) because
// the connector interpolates them into the SELECT it builds — a WHERE/ORDER-BY value
// is always a bound parameter, but an identifier cannot be a bind, so it is validated
// and double-quoted instead. The read-only guard is a separate, additional layer.
func (sc *sourceConfig) validate() error {
	if sc.table != "" && sc.query != "" {
		return errors.New("pgcontent: set either table or query, not both")
	}
	// Export mode needs no query/table shape validation for the connection, but the
	// document-definition columns still matter for mapping; require the minimum set.
	if sc.table == "" && sc.query == "" && sc.mode == modeLive {
		return errors.New("pgcontent: live mode requires a table or a query")
	}
	if sc.table != "" && !validIdent(sc.table) {
		return fmt.Errorf("pgcontent: invalid table identifier %q", sc.table)
	}
	if !validIdent(sc.schema) {
		return fmt.Errorf("pgcontent: invalid schema identifier %q", sc.schema)
	}
	if sc.query != "" {
		if err := ValidateReadOnlyQuery(sc.query); err != nil {
			return fmt.Errorf("pgcontent: %w", err)
		}
	}
	if len(sc.keyColumns) == 0 {
		return errors.New("pgcontent: key_columns is required (the row's stable identifier)")
	}
	if len(sc.bodyColumns) == 0 {
		return errors.New("pgcontent: body_columns is required (the row's document body)")
	}
	// Validate every referenced identifier.
	idents := append([]string{}, sc.keyColumns...)
	idents = append(idents, sc.bodyColumns...)
	idents = append(idents, sc.aclColumns...)
	idents = append(idents, sc.sensitiveCol...)
	idents = append(idents, sc.metadataCol...)
	if sc.titleColumn != "" {
		idents = append(idents, sc.titleColumn)
	}
	if sc.updatedAtCol != "" {
		idents = append(idents, sc.updatedAtCol)
	}
	if sc.classColumn != "" {
		idents = append(idents, sc.classColumn)
	}
	for _, id := range idents {
		if !validIdent(id) {
			return fmt.Errorf("pgcontent: invalid column identifier %q", id)
		}
	}
	return nil
}

// selectColumns is the de-duplicated set of columns the connector must SELECT to
// build a Document: key + body + title + updated-at + acl + class + sensitive +
// metadata columns, in a stable order (used for the projected column list).
func (sc *sourceConfig) selectColumns() []string {
	seen := map[string]bool{}
	var out []string
	add := func(cols ...string) {
		for _, c := range cols {
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	add(sc.keyColumns...)
	add(sc.bodyColumns...)
	add(sc.titleColumn)
	add(sc.updatedAtCol)
	add(sc.aclColumns...)
	add(sc.classColumn)
	add(sc.sensitiveCol...)
	add(sc.metadataCol...)
	return out
}

const (
	modeExport = "export"
	modeLive   = "live"
)

func normalizeMode(m string) string {
	if strings.EqualFold(strings.TrimSpace(m), modeLive) {
		return modeLive
	}
	return modeExport
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// splitList splits a comma-separated setting into trimmed, non-empty entries.
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseTimeout(s string) (time.Duration, error) {
	if s == "" {
		return defaultStatementTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("pgcontent: invalid statement_timeout %q: %w", s, err)
	}
	if d <= 0 {
		return 0, errors.New("pgcontent: statement_timeout must be positive")
	}
	return d, nil
}

func parseMaxRows(s string) (int, error) {
	if s == "" {
		return defaultMaxRows, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("pgcontent: invalid max_rows %q: %w", s, err)
	}
	if n <= 0 {
		return 0, errors.New("pgcontent: max_rows must be positive")
	}
	if n > maxRowsCeiling {
		n = maxRowsCeiling
	}
	return n, nil
}
