// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgcontent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/olivaresai/olivares/connectors/contentsource"
)

// livePageSize is how many document refs one live List/DeltaList page returns.
const livePageSize = 100

// liveClient reads a PostgreSQL database through a pooled, read-only pgx connection.
// It is the "live" mode backend; every statement runs in a READ ONLY transaction on a
// session opened with default_transaction_read_only=on and a bounded statement_timeout
// (readonly.go layers 1-2), and Open refuses to proceed unless the session verifies as
// read-only (verifyReadOnly — the posture guarantee the incumbents leave advisory).
type liveClient struct {
	pool *pgxpool.Pool
	sc   *sourceConfig
}

// connString renders the libpq keyword connection string. A supplied dsn (secret)
// wins verbatim; otherwise the discrete fields are assembled with proper escaping so a
// password with spaces/quotes cannot break the string.
func (sc *sourceConfig) connString() string {
	if sc.dsn != "" {
		return sc.dsn
	}
	var kv []string
	add := func(k, v string) {
		if v != "" {
			kv = append(kv, k+"="+escapeKV(v))
		}
	}
	add("host", sc.host)
	add("port", sc.port)
	add("dbname", sc.dbname)
	add("user", sc.user)
	add("password", sc.password)
	add("sslmode", sc.sslmode)
	return strings.Join(kv, " ")
}

// escapeKV escapes one libpq keyword-string value.
func escapeKV(v string) string {
	if v == "" {
		return "''"
	}
	if strings.ContainsAny(v, " '\\") {
		v = strings.ReplaceAll(v, `\`, `\\`)
		v = strings.ReplaceAll(v, `'`, `\'`)
		return "'" + v + "'"
	}
	return v
}

// hasConnection reports whether any connection target is configured. With none, live
// mode opens as an empty source (never a hard failure) — the contentsource contract.
func (sc *sourceConfig) hasConnection() bool {
	return sc.dsn != "" || sc.host != ""
}

// newLiveClient opens the read-only pool and verifies the read-only posture.
func newLiveClient(ctx context.Context, sc *sourceConfig) (*liveClient, error) {
	poolCfg, err := pgxpool.ParseConfig(sc.connString())
	if err != nil {
		return nil, fmt.Errorf("pgcontent: invalid connection settings: %w", err)
	}
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	// Layer 2: the session itself is read-only and time-bounded, so PostgreSQL rejects
	// any write the connector could never build in the first place.
	poolCfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(sc.statementTimeout.Milliseconds(), 10)
	poolCfg.MaxConns = 4

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("pgcontent: connect: %w", err)
	}
	lc := &liveClient{pool: pool, sc: sc}
	if err := lc.verifyReadOnly(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return lc, nil
}

func (lc *liveClient) close() {
	if lc.pool != nil {
		lc.pool.Close()
	}
}

// inReadOnlyTx runs fn inside a READ ONLY transaction and always rolls it back (a
// read-only transaction has nothing to commit). This is the belt on top of the
// session-level default_transaction_read_only.
func (lc *liveClient) inReadOnlyTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := lc.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return fn(tx)
}

// verifyReadOnly proves, before any ingest, that the session is genuinely read-only:
// it reads transaction_read_only inside a read-only transaction and refuses to open
// unless it is "on". None of the incumbent DB content connectors verify this — they
// document read-only as advice; this connector fails closed if the posture is wrong.
func (lc *liveClient) verifyReadOnly(ctx context.Context) error {
	return lc.inReadOnlyTx(ctx, func(tx pgx.Tx) error {
		var ro string
		if err := tx.QueryRow(ctx, "SHOW transaction_read_only").Scan(&ro); err != nil {
			return fmt.Errorf("pgcontent: cannot verify read-only session: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(ro), "on") {
			return fmt.Errorf("pgcontent: refusing to open — session is not read-only (transaction_read_only=%q)", ro)
		}
		return nil
	})
}

// list returns one page of document refs by offset cursor, bounded by max_rows.
func (lc *liveClient) list(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	offset := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return nil, "", fmt.Errorf("pgcontent: invalid cursor %q", cursor)
		}
		offset = n
	}
	if offset >= lc.sc.maxRows {
		return nil, "", nil
	}
	limit := livePageSize
	if offset+limit > lc.sc.maxRows {
		limit = lc.sc.maxRows - offset
	}
	sql := lc.sc.buildListSQL(offset, limit)
	var refs []contentsource.DocRef
	err := lc.inReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRow(rows)
			if err != nil {
				return err
			}
			refs = append(refs, lc.sc.docRef(r))
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", fmt.Errorf("pgcontent: list: %w", err)
	}
	next := ""
	if len(refs) == limit && offset+limit < lc.sc.maxRows {
		next = strconv.Itoa(offset + limit)
	}
	return refs, next, nil
}

// fetch returns one document by its DocID, rebuilding the WHERE from the decoded key.
func (lc *liveClient) fetch(ctx context.Context, docID string) (contentsource.Document, error) {
	keys, err := lc.sc.decodeKeys(docID)
	if err != nil {
		return contentsource.Document{}, err
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	sql := lc.sc.buildFetchSQL()
	var doc contentsource.Document
	found := false
	err = lc.inReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			r, err := scanRow(rows)
			if err != nil {
				return err
			}
			doc = lc.sc.toDocument(r)
			found = true
		}
		return rows.Err()
	})
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("pgcontent: fetch: %w", err)
	}
	if !found {
		return contentsource.Document{}, ErrDocumentNotFound
	}
	return doc, nil
}

// deltaList returns rows changed since the persisted cursor (an updated-at timestamp),
// for contentsource.LiveSource incremental sync. Without an updated-at column the
// connector does not implement LiveSource, so this is only reached when one is set.
func (lc *liveClient) deltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	since := parseTimestamp(sinceToken) // zero when empty ⇒ first full pass
	sql := lc.sc.buildDeltaSQL(livePageSize)
	var page contentsource.DeltaPage
	var maxSeen time.Time
	err := lc.inReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRow(rows)
			if err != nil {
				return err
			}
			ref := lc.sc.docRef(r)
			page.Changes = append(page.Changes, contentsource.DeltaEntry{DocRef: ref, ChangeKind: contentsource.ChangeContent})
			if ref.ModifiedAt.After(maxSeen) {
				maxSeen = ref.ModifiedAt
			}
		}
		return rows.Err()
	})
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("pgcontent: delta list: %w", err)
	}
	// A full page means there may be more rows at the same/greater cursor; ask for
	// another page immediately (intra-pass) by advancing the resume point. The engine
	// persists ResumeToken only when the pass drains (a short page).
	if len(page.Changes) == livePageSize && maxSeen.After(since) {
		page.NextToken = maxSeen.UTC().Format(time.RFC3339Nano)
	} else if maxSeen.After(since) {
		page.ResumeToken = maxSeen.UTC().Format(time.RFC3339Nano)
	}
	return page, nil
}

// fetchACL re-reads only the ACL/classification of a document (LiveSource ACL-refresh).
func (lc *liveClient) fetchACL(ctx context.Context, docID string) (contentsource.ACLResult, error) {
	doc, err := lc.fetch(ctx, docID)
	if err != nil {
		return contentsource.ACLResult{}, err
	}
	return contentsource.ACLResult{
		ACL:            doc.ACL,
		ExternalLabels: doc.ExternalLabels,
		Classification: doc.Classification,
	}, nil
}

// scanRow converts one pgx result row into the string-valued row the document mapping
// consumes (identical shape to the export path).
func scanRow(rows pgx.Rows) (row, error) {
	vals, err := rows.Values()
	if err != nil {
		return nil, err
	}
	fds := rows.FieldDescriptions()
	r := make(row, len(fds))
	for i, fd := range fds {
		if i < len(vals) {
			r[fd.Name] = stringifyDBValue(vals[i])
		}
	}
	return r, nil
}

// stringifyDBValue renders one column value from pgx as a string (NULL → "").
func stringifyDBValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// ErrDocumentNotFound is returned by the live Fetch when no row matches the DocID.
var ErrDocumentNotFound = errors.New("pgcontent: document not found")

// --- discovery ---------------------------------------------------------------

// Discovery is the read-only introspection of a schema: its tables and their columns,
// so an operator can pick what to materialize before authoring a document definition.
type Discovery struct {
	Schema string
	Tables []TableInfo
}

// TableInfo is one table and its columns.
type TableInfo struct {
	Name    string
	Columns []ColumnInfo
}

// ColumnInfo is one column's name, SQL data type and nullability.
type ColumnInfo struct {
	Name     string
	DataType string
	Nullable bool
}

// discoverColumnsSQL reads column metadata for a schema from information_schema, in a
// deterministic order. The schema is a BOUND parameter ($1), never interpolated.
const discoverColumnsSQL = `SELECT table_name, column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_schema = $1
ORDER BY table_name, ordinal_position`

// discover introspects one schema's tables and columns (read-only).
func (lc *liveClient) discover(ctx context.Context, schema string) (Discovery, error) {
	out := Discovery{Schema: schema}
	byTable := map[string]int{}
	err := lc.inReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, discoverColumnsSQL, schema)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var table, col, dtype, nullable string
			if err := rows.Scan(&table, &col, &dtype, &nullable); err != nil {
				return err
			}
			idx, ok := byTable[table]
			if !ok {
				idx = len(out.Tables)
				byTable[table] = idx
				out.Tables = append(out.Tables, TableInfo{Name: table})
			}
			out.Tables[idx].Columns = append(out.Tables[idx].Columns, ColumnInfo{
				Name:     col,
				DataType: dtype,
				Nullable: strings.EqualFold(nullable, "YES"),
			})
		}
		return rows.Err()
	})
	if err != nil {
		return Discovery{}, fmt.Errorf("pgcontent: discover: %w", err)
	}
	return out, nil
}
