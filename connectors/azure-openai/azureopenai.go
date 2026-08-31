// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureopenai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// maxResponseBytes caps how much of any Azure response we read into memory. List and
// metrics pages are bounded; this is a defensive bound against a pathological or hostile
// endpoint, not a functional limit.
const maxResponseBytes = 16 << 20 // 16 MiB

// Source is the Azure OpenAI / AI Foundry SourceConnector + CatalogProvider. It is a batch
// source: each Gather runs one read pass over the enabled surfaces (Azure Monitor usage,
// Cost Management) and returns; the engine owns re-scheduling, so the connector holds no
// ticker. Snapshot returns the deployment/model catalog. It keeps no state between passes
// beyond its resolved config and a shared HTTP client.
type Source struct {
	cfg    config
	client *http.Client
	now    func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns an Azure OpenAI / Foundry connector; configuration is supplied in Open.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves and validates configuration and builds the shared HTTP client. A MISSING
// or PARTIAL credential is offline-safe: Open succeeds, Snapshot returns the empty catalog,
// and Gather is a silent no-op.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.client = &http.Client{Timeout: defaultTimeout}
	c, err := loadConfig(cfg, s.client)
	if err != nil {
		return err
	}
	s.cfg = c
	s.client.Timeout = c.timeout
	return nil
}

// Gather runs one read pass over the enabled surfaces. Offline (no credential) returns
// immediately. An enabled source that fails yields exactly one health finding (a gap is a
// signal, not silence) and the pass continues with the next source. ctx is honored
// throughout. Every observation is stamped from a single per-pass UTC timestamp.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.cfg.tokens == nil {
		return nil // offline: no credential configured.
	}
	at := s.clock()

	subs, err := s.resolveSubscriptions(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return emit(ctx, sink, healthFinding(subjectSubscriptions, s.tenantRef(),
			"Azure OpenAI subscription discovery failed", err, at))
	}
	if len(subs) == 0 {
		return emit(ctx, sink, healthFinding(subjectSubscriptions, s.tenantRef(),
			"Azure OpenAI: no subscriptions visible to the service principal", errNoSubscriptions, at))
	}

	if s.cfg.enableUsage {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherUsage(ctx, sink, subs, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectUsage, s.tenantRef(),
				"Azure OpenAI Monitor usage read failed", err, at)); e != nil {
				return e
			}
		}
	}

	if s.cfg.enableCost {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherCost(ctx, sink, subs, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectCost, s.tenantRef(),
				"Azure OpenAI Cost Management read failed", err, at)); e != nil {
				return e
			}
		}
	}
	return nil
}

// Close releases resources; the connector holds none between passes.
func (s *Source) Close(context.Context) error { return nil }

// errNoSubscriptions is the sentinel detail for the "no subscriptions visible" finding.
var errNoSubscriptions = errors.New("no subscriptions visible")

// clock returns the connector's time source (injectable for tests), in UTC.
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// tenantRef returns a non-sensitive subject reference for a health finding.
func (s *Source) tenantRef() string {
	if s.cfg.tenantID != "" {
		return "tenants/" + s.cfg.tenantID
	}
	return "azure"
}

// httpClient returns the connector's HTTP client, falling back to a default when Open did
// not set one.
func (s *Source) httpClient() *http.Client {
	if s.client != nil {
		return s.client
	}
	return &http.Client{Timeout: defaultTimeout}
}

// getURL issues one Bearer-authorized GET to an absolute URL (an ARM path or a nextLink,
// which is a full URL) and decodes the JSON response into out.
func (s *Source) getURL(ctx context.Context, fullURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, req); err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	return s.do(req, out)
}

// postURL issues one Bearer-authorized POST with a JSON body to an absolute URL and
// decodes the JSON response into out. It is used only for the Cost Management Query action,
// which is a READ: the body is a query, the endpoint returns cost rows, and no Azure
// resource is mutated.
func (s *Source) postURL(ctx context.Context, fullURL string, body, out any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	if err := s.authorize(ctx, req); err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return s.doStatus(req, out)
}

// authorize attaches the Bearer access token. With no token source it is a no-op (offline
// Gather never reaches here).
func (s *Source) authorize(ctx context.Context, req *http.Request) error {
	if s.cfg.tokens == nil {
		return nil
	}
	tok, err := s.cfg.tokens.token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// do performs req, requires HTTP 200, and decodes the body into out.
func (s *Source) do(req *http.Request, out any) error {
	status, err := s.doStatus(req, out)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return &statusError{method: req.Method, path: req.URL.Path, status: status}
	}
	return nil
}

// doStatus performs req, enforces the response cap, and (on 2xx) decodes the body into out.
// It returns the status code so a caller can distinguish e.g. a Cost Management 204 (no
// data yet) from 200. A non-2xx is a returned statusError. The error text carries only the
// method, path and status — never the body or the credential.
func (s *Source) doStatus(req *http.Request, out any) (int, error) {
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil // 204: success with no body (e.g. Cost Management lag)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, &statusError{method: req.Method, path: req.URL.Path, status: resp.StatusCode}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("azure-openai: decode %s response: %w", req.URL.Path, err)
		}
	}
	return resp.StatusCode, nil
}

// statusError is a non-2xx response, carrying the status code so callers can route on it.
type statusError struct {
	method string
	path   string
	status int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("azure-openai: %s %s returned status %d", e.method, e.path, e.status)
}

// isStatus reports whether err is a statusError with the given HTTP status.
func isStatus(err error, status int) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.status == status
	}
	return false
}

// armURL builds a management-endpoint URL for a relative path + query.
func (s *Source) armURL(path string, q url.Values) string {
	full := strings.TrimRight(s.cfg.managementEndpoint, "/") + path
	if len(q) > 0 {
		full += "?" + q.Encode()
	}
	return full
}

// emit forwards an observation, returning Emit's error so callers treat it as fatal to the
// pass (per the SDK contract).
func emit(ctx context.Context, sink sdk.Sink, obs model.Observation) error {
	return sink.Emit(ctx, obs)
}
