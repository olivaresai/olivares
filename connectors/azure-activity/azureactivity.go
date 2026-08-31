// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureactivity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// maxResponseBytes caps how much of any Azure response we read into memory. List
// and Resource Graph pages are bounded; this is a defensive bound against a
// pathological or hostile endpoint, not a functional limit.
const maxResponseBytes = 16 << 20 // 16 MiB

// Source is the Azure management-plane SourceConnector. It is a batch source:
// each Gather runs one discovery pass (Resource Graph inventory, then Activity
// Log) and returns; the engine owns re-scheduling, so the connector holds no
// ticker. It keeps no state between passes beyond its resolved config and a
// shared HTTP client.
type Source struct {
	cfg    config
	client *http.Client
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an Azure connector; configuration is supplied in Open.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves and validates configuration and builds the shared HTTP client. A
// MISSING or PARTIAL credential is offline-safe: Open succeeds and Gather is a
// silent no-op.
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

// Gather runs one discovery pass. Offline (no credential) returns immediately. It
// first resolves the subscription set (explicit config, else auto-listed), then
// runs the enabled services. A failure in one service yields exactly one health
// finding and the pass continues; ctx is honored throughout. Inventory edges
// carry the per-pass timestamp; activity edges carry the event's own timestamp.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.cfg.tokens == nil {
		return nil // offline: no credential configured.
	}
	at := time.Now().UTC()

	subs, err := s.resolveSubscriptions(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return sink.Emit(ctx, healthFinding(subjectSubscriptions, s.tenantRef(),
			"Azure subscription discovery failed", err, at))
	}
	if len(subs) == 0 {
		// A credentialed connector that can see no subscriptions is a permissions
		// signal, not silence — emit one finding and stop (nothing to read).
		return sink.Emit(ctx, healthFinding(subjectSubscriptions, s.tenantRef(),
			"Azure: no subscriptions visible to the service principal", errNoSubscriptions, at))
	}

	if s.cfg.enableInventory {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherInventory(ctx, sink, subs, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if emitErr := sink.Emit(ctx, healthFinding(subjectInventory, s.tenantRef(),
				"Azure Resource Graph inventory failed", err, at)); emitErr != nil {
				return emitErr
			}
		}
	}

	if s.cfg.enableActivity {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherActivity(ctx, sink, subs, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if emitErr := sink.Emit(ctx, healthFinding(subjectActivity, s.tenantRef(),
				"Azure Activity Log read failed", err, at)); emitErr != nil {
				return emitErr
			}
		}
	}

	if s.cfg.enableRAI {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherRAI(ctx, sink, subs, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if emitErr := sink.Emit(ctx, healthFinding(subjectRAI, s.tenantRef(),
				"Azure Responsible-AI posture read failed", err, at)); emitErr != nil {
				return emitErr
			}
		}
	}
	return nil
}

// Close releases resources; the connector holds none between passes.
func (s *Source) Close(context.Context) error { return nil }

// errNoSubscriptions is the sentinel detail for the "no subscriptions visible"
// health finding (hashed, never embedded raw).
var errNoSubscriptions = fmt.Errorf("no subscriptions visible")

// tenantRef returns a non-sensitive subject reference for a health finding.
func (s *Source) tenantRef() string {
	if s.cfg.tenantID != "" {
		return "tenants/" + s.cfg.tenantID
	}
	return "azure"
}

// httpClient returns the connector's HTTP client, falling back to a default when
// Open did not set one.
func (s *Source) httpClient() *http.Client {
	if s.client != nil {
		return s.client
	}
	return &http.Client{Timeout: defaultTimeout}
}

// getJSON issues one Bearer-authorized GET to a relative path (joined to the
// management endpoint) and decodes the JSON response into out.
func (s *Source) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	full := strings.TrimRight(s.cfg.managementEndpoint, "/") + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	return s.getURL(ctx, full, out)
}

// getURL issues one Bearer-authorized GET to an absolute URL (used to follow an
// Azure nextLink, which is a full URL) and decodes the JSON response into out.
func (s *Source) getURL(ctx context.Context, fullURL string, out any) error {
	tok, err := s.cfg.tokens.token(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	return s.do(req, out)
}

// postJSON issues one Bearer-authorized POST with a JSON body to a relative path
// and decodes the JSON response into out (used for the Resource Graph query).
func (s *Source) postJSON(ctx context.Context, path string, query url.Values, body, out any) error {
	tok, err := s.cfg.tokens.token(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	full := strings.TrimRight(s.cfg.managementEndpoint, "/") + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, full, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return s.do(req, out)
}

// do performs req, enforces the response cap, checks the status, and decodes the
// body into out. The error text carries only the status code — never the body.
func (s *Source) do(req *http.Request, out any) error {
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("azure-activity: %s %s returned status %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("azure-activity: decode %s response: %w", req.URL.Path, err)
	}
	return nil
}

// subscriptionRef is the subset of a subscription list entry we read.
type subscriptionRef struct {
	SubscriptionID string `json:"subscriptionId"`
	State          string `json:"state"`
}

// resolveSubscriptions returns the subscription ids to operate on: the explicit
// config list, or every enabled subscription the principal can see (auto-listed
// via the management API, following nextLink up to max_pages).
func (s *Source) resolveSubscriptions(ctx context.Context) ([]string, error) {
	if len(s.cfg.subscriptions) > 0 {
		return s.cfg.subscriptions, nil
	}
	var out []string
	q := url.Values{"api-version": {subscriptionsAPIVersion}}
	full := strings.TrimRight(s.cfg.managementEndpoint, "/") + "/subscriptions?" + q.Encode()
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp struct {
			Value    []subscriptionRef `json:"value"`
			NextLink string            `json:"nextLink"`
		}
		if err := s.getURL(ctx, full, &resp); err != nil {
			return nil, err
		}
		for _, sub := range resp.Value {
			if sub.SubscriptionID != "" && subscriptionEnabled(sub.State) {
				out = append(out, sub.SubscriptionID)
			}
		}
		if resp.NextLink == "" {
			break
		}
		full = resp.NextLink
	}
	return out, nil
}

// subscriptionEnabled reports whether a subscription state is usable. An empty
// state is treated as enabled (some responses omit it).
func subscriptionEnabled(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "enabled", "active", "warned":
		return true
	default:
		return false
	}
}

// emit forwards an observation, returning Emit's error so callers treat it as
// fatal to the pass.
func emit(ctx context.Context, sink sdk.Sink, obs model.Observation) error {
	return sink.Emit(ctx, obs)
}
