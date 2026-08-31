// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcpaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// maxResponseBytes caps how much of any GCP response we read into memory. List
// and entries pages are bounded; this is a defensive bound against a pathological
// or hostile endpoint, not a functional limit.
const maxResponseBytes = 16 << 20 // 16 MiB

// Source is the GCP management-plane SourceConnector. It is a batch source: each
// Gather runs one discovery pass over the enabled services (Resource Manager/IAM
// inventory, then Cloud Audit Logs) and returns; the engine owns re-scheduling,
// so the connector holds no ticker (per the SDK contract). It keeps no state
// between passes beyond its resolved config and a shared HTTP client.
type Source struct {
	cfg    config
	client *http.Client
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a GCP connector; configuration is supplied in Open.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves and validates configuration and builds the shared HTTP client. A
// configuration error (malformed credentials, unreadable key file, a service
// enabled with no org/project scope) surfaces here, before Gather, per the SDK
// contract. A MISSING credential is offline-safe: Open succeeds and Gather is a
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

// Gather runs one discovery pass over the enabled services. Offline (no
// credential) returns immediately. A disabled service is skipped silently. An
// enabled service that fails yields exactly one health finding (a gap is a
// signal, not silence) and the pass continues. ctx is honored: it is checked
// before each service and inside every page loop, and a cancellation returns
// ctx.Err() promptly. Inventory edges are stamped with a single per-pass UTC
// timestamp; audit edges carry the entry's own timestamp.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.cfg.tokens == nil {
		return nil // offline: no credential configured.
	}
	at := time.Now().UTC()

	if s.cfg.enableInventory {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherInventory(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if emitErr := sink.Emit(ctx, healthFinding(subjectInventory, s.cfg.scopeRef(),
				"GCP Resource Manager / IAM inventory failed", err, at)); emitErr != nil {
				return emitErr
			}
		}
	}

	if s.cfg.enableAudit {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherAudit(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if emitErr := sink.Emit(ctx, healthFinding(subjectAudit, s.cfg.scopeRef(),
				"GCP Cloud Audit Logs read failed", err, at)); emitErr != nil {
				return emitErr
			}
		}
	}
	return nil
}

// Close releases the connector's resources. It holds no long-lived resources
// between passes; it is safe to call even if Open failed.
func (s *Source) Close(context.Context) error { return nil }

// scopeRef returns a non-sensitive subject reference for a health finding: the
// org id, else the first project, else the literal "gcp".
func (c config) scopeRef() string {
	if c.orgID != "" {
		return "organizations/" + c.orgID
	}
	if len(c.projects) > 0 {
		return "projects/" + c.projects[0]
	}
	return "gcp"
}

// httpClient returns the connector's HTTP client, falling back to a default when
// Open did not set one (defensive; Open always sets it on success).
func (s *Source) httpClient() *http.Client {
	if s.client != nil {
		return s.client
	}
	return &http.Client{Timeout: defaultTimeout}
}

// getJSON issues one Bearer-authorized GET and decodes the JSON response into
// out. The access token is fetched (and cached) per call; it is set only on the
// Authorization header and never logged.
func (s *Source) getJSON(ctx context.Context, fullURL string, out any) error {
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

// postJSON issues one Bearer-authorized POST with a JSON body and decodes the
// JSON response into out. It is used for the Cloud Logging entries:list call,
// which takes a request body (resourceNames + filter + pagination) by API design
// — a read despite the POST verb, exactly like AWS CloudTrail LookupEvents.
func (s *Source) postJSON(ctx context.Context, fullURL string, body, out any) error {
	tok, err := s.cfg.tokens.token(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return s.do(req, out)
}

// do performs req, enforces the response cap, checks the status, and decodes the
// body into out. The error text carries only the status code — never the body,
// which can echo a resource name or token.
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
		return fmt.Errorf("gcp-audit: %s %s returned status %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("gcp-audit: decode %s response: %w", req.URL.Path, err)
	}
	return nil
}

// emit forwards an observation, returning Emit's error so callers can treat it as
// fatal to the pass (per the SDK contract).
func emit(ctx context.Context, sink sdk.Sink, obs model.Observation) error {
	return sink.Emit(ctx, obs)
}
