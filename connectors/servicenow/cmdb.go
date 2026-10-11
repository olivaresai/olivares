// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package servicenow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

var cmdbIdentifier = regexp.MustCompile(`^[0-9a-f]{32}$`)
var cmdbSourceRef = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
var cmdbClass = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}$`)

// CMDBResource is the allowlisted metadata projection released to a permitted
// caller. Unrequested Table API fields never become tool output or observations.
type CMDBResource struct {
	SysID       string    `json:"sys_id"`
	Name        string    `json:"name"`
	Class       string    `json:"sys_class_name"`
	UpdatedAt   time.Time `json:"updated_at"`
	CollectedAt time.Time `json:"collected_at"`
}

type cmdbClient struct {
	base, auth, sourceRef, sysID string
	pageSize, maxPages           int
	http                         *http.Client
}

func newCMDBClient(cfg sdk.Config) (*cmdbClient, error) {
	u, err := url.Parse(cfg.Get(cfgInstanceURL))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("servicenow cmdb: invalid instance URL")
	}
	// Plain HTTP is confined to local protocol fixtures. Real instances use TLS.
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("servicenow cmdb: TLS is required")
		}
	}
	c := &cmdbClient{base: strings.TrimRight(u.String(), "/"), sourceRef: cfg.Get("source_ref"), sysID: cfg.Get("sys_id"), pageSize: 100, maxPages: 10}
	if c.sourceRef == "" {
		// Keep the existing bounded identifier format without asking the operator
		// to name an instance that its validated URL already identifies.
		digest := sha256.Sum256([]byte(strings.ToLower(u.Scheme + "://" + u.Host)))
		c.sourceRef = "servicenow-cmdb." + hex.EncodeToString(digest[:])
	}
	if !cmdbSourceRef.MatchString(c.sourceRef) || (c.sysID != "" && !cmdbIdentifier.MatchString(c.sysID)) {
		return nil, errors.New("servicenow cmdb: invalid source or resource identifier")
	}
	mode := cfg.Get(cfgAuthMode)
	if mode == "" {
		mode = authBasic
	}
	switch mode {
	case authBasic:
		user, password := cfg.Get(cfgUsername), cfg.Get(cfgPassword)
		if user == "" || password == "" || strings.ContainsAny(user, ":\r\n") {
			return nil, errors.New("servicenow cmdb: basic credentials are required")
		}
		c.auth = "Basic " + basicAuth(user, password)
	case authBearer:
		token := cfg.Get(cfgToken)
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return nil, errors.New("servicenow cmdb: bearer credential is required")
		}
		c.auth = "Bearer " + token
	default:
		return nil, errors.New("servicenow cmdb: invalid authentication mode")
	}
	for _, setting := range []struct {
		key    string
		target *int
		max    int
	}{{"page_size", &c.pageSize, 1000}, {"max_pages", &c.maxPages, 100}} {
		if raw := cfg.Get(setting.key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > setting.max {
				return nil, errors.New("servicenow cmdb: invalid collection bounds")
			}
			*setting.target = value
		}
	}
	c.http = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return c, nil
}

func (c *cmdbClient) get(ctx context.Context, suffix string, query url.Values) (json.RawMessage, http.Header, error) {
	query.Set("sysparm_fields", "sys_id,name,sys_class_name,sys_updated_on")
	query.Set("sysparm_display_value", "false")
	query.Set("sysparm_exclude_reference_link", "true")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/now/table/cmdb_ci"+suffix+"?"+query.Encode(), nil)
	if err != nil {
		return nil, nil, errors.New("servicenow cmdb: invalid read request")
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return nil, nil, errors.New("servicenow cmdb: instance unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, nil, errors.New("servicenow cmdb: read refused or unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, nil, errors.New("servicenow cmdb: invalid response size")
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil, nil, errors.New("servicenow cmdb: invalid response")
	}
	return envelope.Result, response.Header, nil
}

func decodeCMDBResource(raw json.RawMessage) (CMDBResource, error) {
	var item struct {
		ID      string `json:"sys_id"`
		Name    string `json:"name"`
		Class   string `json:"sys_class_name"`
		Updated string `json:"sys_updated_on"`
	}
	if json.Unmarshal(raw, &item) != nil || !cmdbIdentifier.MatchString(item.ID) || item.Name == "" || len(item.Name) > 512 || !cmdbClass.MatchString(item.Class) {
		return CMDBResource{}, errors.New("servicenow cmdb: invalid resource metadata")
	}
	// ServiceNow's internal date/time display is UTC when display values are false.
	updated, err := time.Parse("2006-01-02 15:04:05", item.Updated)
	if err != nil {
		return CMDBResource{}, errors.New("servicenow cmdb: invalid resource update time")
	}
	return CMDBResource{SysID: item.ID, Name: item.Name, Class: item.Class, UpdatedAt: updated, CollectedAt: time.Now().UTC()}, nil
}

// CMDBSource discovers resource identifiers through the existing observation
// pipeline. Discovery is unknown access, never an observed agent read or write.
type CMDBSource struct{ client *cmdbClient }

var _ sdk.SourceConnector = (*CMDBSource)(nil)

func NewCMDBSource() *CMDBSource { return &CMDBSource{} }

func (*CMDBSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "olivares.servicenow.cmdb", Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeSource,
		Title: "ServiceNow CMDB", Description: "Read-only bounded CMDB resource discovery; emits identifiers and source provenance.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgInstanceURL, Type: sdk.FieldString, Required: true, Description: "ServiceNow HTTPS instance URL."},
			{Key: cfgAuthMode, Type: sdk.FieldString, Default: authBasic, Description: "basic or bearer authentication."},
			{Key: cfgUsername, Type: sdk.FieldString, Secret: true, Description: "Integration username secret reference."},
			{Key: cfgPassword, Type: sdk.FieldString, Secret: true, Description: "Integration password secret reference."},
			{Key: cfgToken, Type: sdk.FieldString, Secret: true, Description: "Bearer token secret reference."},
			{Key: "source_ref", Type: sdk.FieldString, Description: "Optional source identifier override; defaults to a stable identifier derived from the instance URL."},
			{Key: "page_size", Type: sdk.FieldInt, Default: "100", Description: "Records per request, 1–1000."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: "10", Description: "Collection page bound, 1–100. Whole collection deadline: 30s."},
		}}
}

func (s *CMDBSource) Open(_ context.Context, cfg sdk.Config) error {
	c, err := newCMDBClient(cfg)
	if err != nil {
		return err
	}
	s.client = c
	return nil
}

func (s *CMDBSource) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.client == nil {
		return errors.New("servicenow cmdb: source is not open")
	}
	c := s.client
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var ids []string
	seen := map[string]bool{}
	total := -1
	for page := 0; page < c.maxPages; page++ {
		offset := page * c.pageSize
		raw, headers, err := c.get(ctx, "", url.Values{"sysparm_limit": {strconv.Itoa(c.pageSize)}, "sysparm_offset": {strconv.Itoa(offset)}, "sysparm_query": {"ORDERBYsys_id"}, "sysparm_no_count": {"false"}})
		if err != nil {
			return err
		}
		count, err := strconv.Atoi(headers.Get("X-Total-Count"))
		if err != nil || count < 0 || count > c.pageSize*c.maxPages || (total >= 0 && total != count) {
			return errors.New("servicenow cmdb: incomplete or changing collection")
		}
		total = count
		var rows []json.RawMessage
		if json.Unmarshal(raw, &rows) != nil || string(raw) == "null" || len(rows) > c.pageSize || offset+len(rows) > total {
			return errors.New("servicenow cmdb: invalid collection page")
		}
		for _, row := range rows {
			item, err := decodeCMDBResource(row)
			if err != nil {
				return err
			}
			if seen[item.SysID] {
				return errors.New("servicenow cmdb: duplicate resource in collection")
			}
			seen[item.SysID] = true
			ids = append(ids, item.SysID)
		}
		// The Table API filters ACLs after applying limit. Empty/short pages are
		// therefore not completion; advance across the server's stable total count.
		if offset+c.pageSize >= total {
			observed := time.Now().UTC()
			for _, id := range ids {
				if err := sink.Emit(ctx, model.EdgeObservation{OriginKind: "source", OriginRef: c.sourceRef,
					ResourceKind: "servicenow.cmdb", ResourceRef: c.base + "/api/now/table/cmdb_ci/" + id,
					Mode: model.ModeUnknown, Source: "servicenow_cmdb", Confidence: model.ConfidenceApproximate, ObservedAt: observed}); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return errors.New("servicenow cmdb: collection page budget exhausted")
}

func (s *CMDBSource) Close(context.Context) error {
	if s.client != nil {
		s.client.http.CloseIdleConnections()
		s.client = nil
	}
	return nil
}

// CMDBReader reads one operator-configured resource, with no caller-selected URL,
// table or identifier. Managed MCP admission remains the gateway's authority.
type CMDBReader struct{ client *cmdbClient }

func NewCMDBReader(cfg sdk.Config) (*CMDBReader, error) {
	c, err := newCMDBClient(cfg)
	if err != nil {
		return nil, err
	}
	if c.sysID == "" {
		return nil, errors.New("servicenow cmdb: configure one resource identifier")
	}
	return &CMDBReader{client: c}, nil
}

func (r *CMDBReader) Read(ctx context.Context) (CMDBResource, error) {
	raw, _, err := r.client.get(ctx, "/"+r.client.sysID, url.Values{})
	if err != nil {
		return CMDBResource{}, err
	}
	item, err := decodeCMDBResource(raw)
	if err != nil {
		return CMDBResource{}, err
	}
	if item.SysID != r.client.sysID {
		return CMDBResource{}, errors.New("servicenow cmdb: unexpected resource identifier")
	}
	return item, nil
}
