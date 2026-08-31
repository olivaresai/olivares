// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package snowflake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
	"github.com/olivaresai/olivares/sdk"
)

// Compile-time assertion: *Source always satisfies LiveSource (methods are
// defined unconditionally; they return errors when not in live mode).
var _ contentsource.LiveSource = (*Source)(nil)

// defaultTimestampColumn is the column used for incremental sync when none is
// configured.
const defaultTimestampColumn = "LAST_MODIFIED"

// liveClient holds the runtime state for Snowflake SQL REST API access.
type liveClient struct {
	http          *http.Client
	account       string
	user          string
	warehouse     string
	database      string
	schemaName    string
	tables        []string
	timestampCol  string
	credentialRef string
	baseURL       string // overridden in tests
}

// newLiveClient constructs a liveClient from resolved configuration.
func newLiveClient(cfg sdk.Config) (*liveClient, error) {
	account := strings.TrimSpace(cfg.Get("account"))
	if account == "" {
		return nil, errors.New("snowflake: account is required for live mode")
	}
	user := strings.TrimSpace(cfg.Get("user"))
	if user == "" {
		return nil, errors.New("snowflake: user is required for live mode")
	}
	tablesRaw := strings.TrimSpace(cfg.Get("tables"))
	var tables []string
	for _, t := range strings.Split(tablesRaw, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tables = append(tables, t)
		}
	}
	timestampCol := strings.TrimSpace(cfg.Get("timestamp_column"))
	if timestampCol == "" {
		timestampCol = defaultTimestampColumn
	}
	baseURL := fmt.Sprintf("https://%s.snowflakecomputing.com", account)
	return &liveClient{
		http:          &http.Client{},
		account:       account,
		user:          user,
		warehouse:     strings.TrimSpace(cfg.Get("warehouse")),
		database:      strings.TrimSpace(cfg.Get("database")),
		schemaName:    strings.TrimSpace(cfg.Get("schema_name")),
		tables:        tables,
		timestampCol:  timestampCol,
		credentialRef: strings.TrimSpace(cfg.Get("credential_ref")),
		baseURL:       baseURL,
	}, nil
}

// ---- Snowflake SQL REST API response shapes -----------------------------------

// sfAPIResponse is the shape of a Snowflake SQL API response
// (POST /api/v2/statements).
type sfAPIResponse struct {
	ResultSetMetaData sfResultSetMetaData `json:"resultSetMetaData"`
	Data              [][]string          `json:"data"`
	Message           string              `json:"message"`
	Code              string              `json:"code"`
}

type sfResultSetMetaData struct {
	NumRows int         `json:"numRows"`
	RowType []sfRowType `json:"rowType"`
}

type sfRowType struct {
	Name string `json:"name"`
}

// sfStatementRequest is the request body for the Snowflake SQL API.
type sfStatementRequest struct {
	Statement string `json:"statement"`
	Warehouse string `json:"warehouse,omitempty"`
	Database  string `json:"database,omitempty"`
	Schema    string `json:"schema,omitempty"`
	Timeout   int    `json:"timeout,omitempty"`
}

// executeSQL sends a SQL statement to the Snowflake SQL REST API and returns
// the parsed response.
func (lc *liveClient) executeSQL(ctx context.Context, sql string) (*sfAPIResponse, error) {
	reqBody := sfStatementRequest{
		Statement: sql,
		Warehouse: lc.warehouse,
		Database:  lc.database,
		Schema:    lc.schemaName,
		Timeout:   60,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("snowflake: marshal request: %w", err)
	}

	reqURL := lc.baseURL + "/api/v2/statements"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("snowflake: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// The credential_ref holds the resolved JWT token for key-pair auth.
	if lc.credentialRef != "" {
		req.Header.Set("Authorization", "Bearer "+lc.credentialRef)
	}
	req.Header.Set("X-Snowflake-Authorization-Token-Type", "KEYPAIR_JWT")

	resp, err := lc.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("snowflake: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snowflake: SQL API returned HTTP %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return nil, fmt.Errorf("snowflake: read response body: %w", err)
	}

	var apiResp sfAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("snowflake: parse response: %w", err)
	}

	return &apiResp, nil
}

// columnIndex returns the index of a named column in the result set metadata,
// or -1 if not found.
func columnIndex(meta sfResultSetMetaData, name string) int {
	upper := strings.ToUpper(name)
	for i, rt := range meta.RowType {
		if strings.ToUpper(rt.Name) == upper {
			return i
		}
	}
	return -1
}

// cellValue safely extracts a cell value from a row by column index.
func cellValue(row []string, idx int) string {
	if idx < 0 || idx >= len(row) {
		return ""
	}
	return row[idx]
}

// DeltaList queries each configured table for rows modified since sinceToken
// (an RFC3339 high-water timestamp on the configured timestamp column). If
// sinceToken is empty, all rows are returned (full sync). ResumeToken is the
// latest changed timestamp from the results. Expired is always false
// (timestamps never expire in Snowflake).
func (s *Source) DeltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	if s.live == nil {
		return contentsource.DeltaPage{}, errors.New("snowflake: DeltaList requires live mode")
	}

	page := contentsource.DeltaPage{}
	var latestModifiedAt time.Time

	for _, table := range s.live.tables {
		fqTable := table
		if s.live.database != "" && s.live.schemaName != "" && !strings.Contains(table, ".") {
			fqTable = fmt.Sprintf("%s.%s.%s", s.live.database, s.live.schemaName, table)
		}

		sql := fmt.Sprintf(
			"SELECT * FROM %s", fqTable,
		)
		if sinceToken != "" {
			sql += fmt.Sprintf(
				" WHERE %s > '%s'",
				s.live.timestampCol, sinceToken,
			)
		}
		sql += fmt.Sprintf(
			" ORDER BY %s ASC LIMIT 1000",
			s.live.timestampCol,
		)

		apiResp, err := s.live.executeSQL(ctx, sql)
		if err != nil {
			return contentsource.DeltaPage{}, fmt.Errorf("snowflake: delta query for %s: %w", table, err)
		}

		idIdx := columnIndex(apiResp.ResultSetMetaData, "ID")
		titleIdx := columnIndex(apiResp.ResultSetMetaData, "TITLE")
		contentIdx := columnIndex(apiResp.ResultSetMetaData, "CONTENT")
		tsIdx := columnIndex(apiResp.ResultSetMetaData, s.live.timestampCol)

		for _, row := range apiResp.Data {
			rowID := strings.TrimSpace(cellValue(row, idIdx))
			if rowID == "" {
				continue
			}
			modifiedAt := parseTime(cellValue(row, tsIdx))
			if modifiedAt.After(latestModifiedAt) {
				latestModifiedAt = modifiedAt
			}

			title := cellValue(row, titleIdx)
			if title == "" {
				title = cellValue(row, contentIdx)
			}

			page.Changes = append(page.Changes, contentsource.DeltaEntry{
				DocRef: contentsource.DocRef{
					DocID:       content.Truncate(rowID, content.MaxRefLen),
					Title:       content.Truncate(title, content.MaxTitleLen),
					ContentType: "text/plain",
					ModifiedAt:  modifiedAt,
				},
				ChangeKind: contentsource.ChangeContent,
			})
		}
	}

	if !latestModifiedAt.IsZero() {
		page.ResumeToken = latestModifiedAt.UTC().Format(time.RFC3339)
	}

	return page, nil
}

func (s *Source) listLive(ctx context.Context, _ string) ([]contentsource.DocRef, string, error) {
	if s.live == nil {
		return nil, "", errors.New("snowflake: List requires live mode")
	}
	var refs []contentsource.DocRef
	for _, table := range s.live.tables {
		sql := fmt.Sprintf(
			"SELECT * FROM %s ORDER BY %s ASC LIMIT 1000",
			s.live.fqTable(table), s.live.timestampCol,
		)
		apiResp, err := s.live.executeSQL(ctx, sql)
		if err != nil {
			return nil, "", fmt.Errorf("snowflake: list query for %s: %w", table, err)
		}
		idIdx := columnIndex(apiResp.ResultSetMetaData, "ID")
		titleIdx := columnIndex(apiResp.ResultSetMetaData, "TITLE")
		contentIdx := columnIndex(apiResp.ResultSetMetaData, "CONTENT")
		tsIdx := columnIndex(apiResp.ResultSetMetaData, s.live.timestampCol)
		for _, row := range apiResp.Data {
			rowID := strings.TrimSpace(cellValue(row, idIdx))
			if rowID == "" {
				continue
			}
			title := cellValue(row, titleIdx)
			if title == "" {
				title = cellValue(row, contentIdx)
			}
			refs = append(refs, contentsource.DocRef{
				DocID:       content.Truncate(rowID, content.MaxRefLen),
				Title:       content.Truncate(title, content.MaxTitleLen),
				ContentType: "application/json",
				ModifiedAt:  parseTime(cellValue(row, tsIdx)),
			})
		}
	}
	return refs, "", nil
}

func (s *Source) fetchLive(ctx context.Context, docID string) (contentsource.Document, error) {
	if s.live == nil {
		return contentsource.Document{}, errors.New("snowflake: Fetch requires live mode")
	}
	for _, table := range s.live.tables {
		sql := fmt.Sprintf(
			"SELECT * FROM %s WHERE ID = '%s' LIMIT 1",
			s.live.fqTable(table), sqlString(docID),
		)
		apiResp, err := s.live.executeSQL(ctx, sql)
		if err != nil {
			return contentsource.Document{}, fmt.Errorf("snowflake: fetch query for %s: %w", table, err)
		}
		if len(apiResp.Data) == 0 {
			continue
		}
		raw := rowMap(apiResp.ResultSetMetaData, apiResp.Data[0])
		body, err := json.Marshal(raw)
		if err != nil {
			return contentsource.Document{}, fmt.Errorf("snowflake: marshal row: %w", err)
		}
		title := stringMapValue(raw, "TITLE")
		if title == "" {
			title = stringMapValue(raw, "CONTENT")
		}
		return contentsource.Document{
			Source:      contentsource.SourceSnowflake,
			DocID:       content.Truncate(docID, content.MaxRefLen),
			Title:       content.Truncate(title, content.MaxTitleLen),
			Body:        string(body),
			ContentType: "application/json",
			SpaceRef:    "table:" + table,
			ModifiedAt:  parseTime(stringMapValue(raw, s.live.timestampCol)),
			Attributes:  map[string]string{"table": table},
		}, nil
	}
	return contentsource.Document{}, fmt.Errorf("snowflake: row %s not found in configured tables", docID)
}

func (lc *liveClient) fqTable(table string) string {
	if lc.database != "" && lc.schemaName != "" && !strings.Contains(table, ".") {
		return fmt.Sprintf("%s.%s.%s", lc.database, lc.schemaName, table)
	}
	return table
}

func rowMap(meta sfResultSetMetaData, row []string) map[string]string {
	out := make(map[string]string, len(meta.RowType))
	for i, rt := range meta.RowType {
		out[rt.Name] = cellValue(row, i)
	}
	return out
}

func stringMapValue(raw map[string]string, name string) string {
	upper := strings.ToUpper(name)
	for k, v := range raw {
		if strings.ToUpper(k) == upper {
			return v
		}
	}
	return ""
}

func sqlString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// FetchACL queries SHOW GRANTS ON TABLE for each configured table and returns
// the Snowflake roles that have SELECT privilege as ACL entries.
func (s *Source) FetchACL(ctx context.Context, docID string) (contentsource.ACLResult, error) {
	if s.live == nil {
		return contentsource.ACLResult{}, errors.New("snowflake: FetchACL requires live mode")
	}

	// FetchACL operates at the table level. The docID encodes the table the row
	// belongs to; we query grants on each configured table and return the union
	// of roles with SELECT privilege.
	var acl []string
	for _, table := range s.live.tables {
		fqTable := table
		if s.live.database != "" && s.live.schemaName != "" && !strings.Contains(table, ".") {
			fqTable = fmt.Sprintf("%s.%s.%s", s.live.database, s.live.schemaName, table)
		}

		sql := fmt.Sprintf("SHOW GRANTS ON TABLE %s", fqTable)
		apiResp, err := s.live.executeSQL(ctx, sql)
		if err != nil {
			return contentsource.ACLResult{}, fmt.Errorf("snowflake: grants query for %s: %w", table, err)
		}

		privIdx := columnIndex(apiResp.ResultSetMetaData, "privilege")
		roleIdx := columnIndex(apiResp.ResultSetMetaData, "grantee_name")

		for _, row := range apiResp.Data {
			priv := strings.ToUpper(strings.TrimSpace(cellValue(row, privIdx)))
			role := strings.TrimSpace(cellValue(row, roleIdx))
			if role == "" {
				continue
			}
			if priv == "SELECT" || priv == "ALL" || priv == "ALL PRIVILEGES" || priv == "OWNERSHIP" {
				acl = append(acl, "role:"+role)
			}
		}
	}

	return contentsource.ACLResult{
		ACL: content.CleanACL(acl),
	}, nil
}
