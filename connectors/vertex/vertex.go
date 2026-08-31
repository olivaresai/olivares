// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vertex

import (
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

// maxResponseBytes caps how much of any response we read into memory. It is a defensive
// bound against a pathological or hostile endpoint, not a functional limit.
const maxResponseBytes = 16 << 20 // 16 MiB

// Source is the platform SourceConnector + CatalogProvider (Gemini Enterprise Agent
// Platform, formerly Vertex AI). It is a batch source: each
// Gather runs one read pass over the enabled surfaces (Cloud Monitoring usage, the
// operator cost export, Model Armor posture, Model Armor sanitization logs) and returns;
// the engine owns re-scheduling, so the connector holds no ticker. Snapshot returns the
// model catalog. It keeps no state between passes beyond its resolved config and a shared
// HTTP client.
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

// New returns a platform connector; configuration is supplied in Open.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves and validates configuration and builds the shared HTTP client. A MISSING
// credential is offline-safe: Open succeeds, Snapshot returns the declared catalog, and
// Gather emits only what needs no Google credential (a no-auth cost export). A malformed
// inline service-account key surfaces here, before Gather (per the SDK contract).
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

// Gather runs one read pass over the enabled surfaces. A disabled or unconfigured source
// is skipped silently. An enabled source that fails yields exactly one health finding (a
// gap is a signal, not silence) and the pass continues with the next source. ctx is
// honored: it is checked before each source and inside every page loop. Every observation
// is stamped from a single per-pass UTC timestamp.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := s.clock()

	// 1. Token usage from Cloud Monitoring (needs a credential + project).
	if s.cfg.enableUsage && s.cfg.tokens != nil && s.cfg.project != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherUsage(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectUsage, s.projectRef(),
				"Vertex Cloud Monitoring usage read failed", err, at)); e != nil {
				return e
			}
		}
	}

	// 2. Billed cost from the operator-wired billing-export result (no Google creds).
	if s.cfg.costExportURL != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherCost(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectCost, s.projectRef(),
				"Vertex billing-export read failed", err, at)); e != nil {
				return e
			}
		}
	}

	// 3. Model Armor safety posture (needs a credential + project).
	if s.cfg.enableModelArmor && s.cfg.tokens != nil && s.cfg.project != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherModelArmor(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectModelArmor, s.projectRef(),
				"Vertex Model Armor read failed", err, at)); e != nil {
				return e
			}
		}
	}

	// 4. Model Armor sanitization results from Cloud Logging (needs a credential + project).
	if s.cfg.enableSanitizationIngest && s.cfg.tokens != nil && s.cfg.project != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherSanitization(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectArmorSanitization, s.projectRef(),
				"Vertex Model Armor sanitization-log read failed", err, at)); e != nil {
				return e
			}
		}
	}
	return nil
}

// Close releases the connector's resources; it holds none between passes.
func (s *Source) Close(context.Context) error { return nil }

// clock returns the connector's time source (injectable for tests), in UTC.
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// projectRef is the non-sensitive subject reference for project-scoped health/posture
// findings.
func (s *Source) projectRef() string {
	if s.cfg.project != "" {
		return "projects/" + s.cfg.project
	}
	return "vertex"
}

// httpClient returns the connector's HTTP client, falling back to a default when Open did
// not set one.
func (s *Source) httpClient() *http.Client {
	if s.client != nil {
		return s.client
	}
	return &http.Client{Timeout: defaultTimeout}
}

// getURL issues one Bearer-authorized GET to an absolute URL and decodes the JSON
// response into out. With no token source (offline) it sends no Authorization header, so
// a no-auth operator export endpoint still works.
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

// authorize attaches the Bearer access token when a token source is configured. Offline
// (nil token source) it is a no-op so no empty Authorization header is sent.
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

// do performs req, enforces the response cap, checks the status, and decodes the body
// into out. The error text carries only the method, the path (never the query string,
// which can hold a page cursor) and the status code — never the body or the credential.
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
		return &statusError{method: req.Method, path: req.URL.Path, status: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("vertex: decode %s response: %w", req.URL.Path, err)
	}
	return nil
}

// statusError is a non-2xx response, carrying the status code so callers can route on it
// (e.g. tolerate a per-model 404 during catalog enrichment) without substring-matching.
type statusError struct {
	method string
	path   string
	status int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("vertex: %s %s returned status %d", e.method, e.path, e.status)
}

// isStatus reports whether err is a statusError with the given HTTP status.
func isStatus(err error, status int) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.status == status
	}
	return false
}

// joinURL joins an endpoint base and a path, trimming any duplicate slash, and appends an
// encoded query when present.
func joinURL(base, path string, q url.Values) string {
	full := strings.TrimRight(base, "/") + path
	if len(q) > 0 {
		full += "?" + q.Encode()
	}
	return full
}

// emit forwards an observation, returning Emit's error so callers treat it as fatal to
// the pass (per the SDK contract).
func emit(ctx context.Context, sink sdk.Sink, obs model.Observation) error {
	return sink.Emit(ctx, obs)
}
