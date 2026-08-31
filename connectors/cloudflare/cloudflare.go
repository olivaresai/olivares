// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
)

// Source is the Cloudflare inventory SourceConnector. It is a batch source: each
// Gather runs ONE read-only discovery pass over the configured account (and zone,
// if set) and returns nil; the engine owns re-scheduling (the connector holds no
// ticker, per the SDK contract).
type Source struct {
	cfg config
	cl  *client
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Cloudflare connector; configuration is supplied in Open.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves and validates the configuration and builds the read-only REST
// client. A configuration error (missing token/account, unparsable settings)
// surfaces here, before Gather. The api_token is held only in the client and is
// never logged or emitted.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	s.cl = newClient(c.apiBase, c.apiToken, &http.Client{Timeout: c.timeout})
	return nil
}

// Gather runs one discovery pass: it lists Workers scripts, R2 buckets and
// account Logpush jobs (and, when a zone is configured, Worker routes and zone
// Logpush jobs), sorts each set by a natural key and emits one topology edge per
// item. A target that is configured but cannot be listed yields exactly one
// health finding and the pass continues with the others. Gather honors ctx in
// every loop and returns ctx.Err() if canceled mid-pass. A single per-pass
// timestamp stamps every observation.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := time.Now().UTC()

	if err := s.gatherWorkers(ctx, sink, at); err != nil {
		return err
	}
	if err := s.gatherR2Buckets(ctx, sink, at); err != nil {
		return err
	}
	if err := s.gatherAccountLogpush(ctx, sink, at); err != nil {
		return err
	}
	if s.cfg.hasZone() {
		if err := s.gatherWorkerRoutes(ctx, sink, at); err != nil {
			return err
		}
		if err := s.gatherZoneLogpush(ctx, sink, at); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Close releases the connector's resources; it holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// emitOrFinding lists via list(), and on error emits one health finding for the
// target and returns nil (continue); on success it sorts the edges by ref and
// emits them. An Emit error (or a ctx-cancel surfaced as the list error) is fatal
// to the pass and returned. The ctx-cancel case is distinguished so a canceled
// pass returns ctx.Err() rather than swallowing it into a finding.
func (s *Source) emitOrFinding(ctx context.Context, sink sdk.Sink, subjectKind, subjectRef, failTitle string, list func() ([]edgeRow, error), at time.Time) error {
	rows, err := list()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return sink.Emit(ctx, healthFinding(subjectKind, subjectRef, failTitle, err, at))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].resRef < rows[j].resRef })
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if emitErr := sink.Emit(ctx, inventoryEdge(r.originKind, r.originRef, r.resKind, r.resRef, r.toolRef, at)); emitErr != nil {
			return emitErr
		}
	}
	return nil
}

// edgeRow is a pre-emit, sortable description of one topology edge. Sorting by
// resRef before emit gives stable, golden-testable output.
type edgeRow struct {
	originKind string
	originRef  string
	resKind    string
	resRef     string
	toolRef    string
}

// gatherWorkers discovers account Workers scripts:
// GET /accounts/{acct}/workers/scripts -> cf.account -> cf.worker.
func (s *Source) gatherWorkers(ctx context.Context, sink sdk.Sink, at time.Time) error {
	path := "/accounts/" + s.cfg.accountID + "/workers/scripts"
	return s.emitOrFinding(ctx, sink, originAccount, s.cfg.accountID, "Cloudflare Workers scripts list failed", func() ([]edgeRow, error) {
		raw, err := s.cl.get(ctx, path, nil, nil)
		if err != nil {
			return nil, err
		}
		rows := make([]edgeRow, 0, len(raw))
		for _, item := range raw {
			var sc struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(item, &sc); err != nil {
				return nil, fmt.Errorf("cloudflare: decode worker script: %w", err)
			}
			if sc.ID == "" {
				continue
			}
			rows = append(rows, edgeRow{
				originKind: originAccount,
				originRef:  s.cfg.accountID,
				resKind:    resWorker,
				resRef:     redact.Clean(sc.ID),
			})
		}
		return rows, nil
	}, at)
}

// gatherWorkerRoutes discovers zone Worker routes (zone-scoped):
// GET /zones/{zone}/workers/routes -> cf.zone -> cf.worker_route.
// The route pattern is a hostname/path glob; it is cleaned (not a URL) before it
// becomes a reference. The bound script name is the ToolRef.
func (s *Source) gatherWorkerRoutes(ctx context.Context, sink sdk.Sink, at time.Time) error {
	path := "/zones/" + s.cfg.zoneID + "/workers/routes"
	return s.emitOrFinding(ctx, sink, originZone, s.cfg.zoneID, "Cloudflare Worker routes list failed", func() ([]edgeRow, error) {
		raw, err := s.cl.get(ctx, path, nil, nil)
		if err != nil {
			return nil, err
		}
		rows := make([]edgeRow, 0, len(raw))
		for _, item := range raw {
			var rt struct {
				ID      string `json:"id"`
				Pattern string `json:"pattern"`
				Script  string `json:"script"`
			}
			if err := json.Unmarshal(item, &rt); err != nil {
				return nil, fmt.Errorf("cloudflare: decode worker route: %w", err)
			}
			if rt.Pattern == "" {
				continue
			}
			rows = append(rows, edgeRow{
				originKind: originZone,
				originRef:  s.cfg.zoneID,
				resKind:    resWorkerRoute,
				resRef:     redact.Clean(rt.Pattern),
				toolRef:    redact.Clean(rt.Script),
			})
		}
		return rows, nil
	}, at)
}

// gatherR2Buckets discovers account R2 buckets:
// GET /accounts/{acct}/r2/buckets -> cf.account -> r2.bucket.
// The result array is nested under result.buckets, so an unwrap extracts it.
func (s *Source) gatherR2Buckets(ctx context.Context, sink sdk.Sink, at time.Time) error {
	path := "/accounts/" + s.cfg.accountID + "/r2/buckets"
	return s.emitOrFinding(ctx, sink, originAccount, s.cfg.accountID, "Cloudflare R2 buckets list failed", func() ([]edgeRow, error) {
		raw, err := s.cl.get(ctx, path, nil, unwrapBuckets)
		if err != nil {
			return nil, err
		}
		rows := make([]edgeRow, 0, len(raw))
		for _, item := range raw {
			var b struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(item, &b); err != nil {
				return nil, fmt.Errorf("cloudflare: decode r2 bucket: %w", err)
			}
			if b.Name == "" {
				continue
			}
			rows = append(rows, edgeRow{
				originKind: originAccount,
				originRef:  s.cfg.accountID,
				resKind:    resR2Bucket,
				resRef:     redact.Clean(b.Name),
			})
		}
		return rows, nil
	}, at)
}

// gatherAccountLogpush discovers account-scoped Logpush jobs:
// GET /accounts/{acct}/logpush/jobs -> cf.account -> cf.logpush_job.
func (s *Source) gatherAccountLogpush(ctx context.Context, sink sdk.Sink, at time.Time) error {
	path := "/accounts/" + s.cfg.accountID + "/logpush/jobs"
	return s.emitOrFinding(ctx, sink, originAccount, s.cfg.accountID, "Cloudflare account Logpush jobs list failed", func() ([]edgeRow, error) {
		return s.logpushRows(ctx, path, originAccount, s.cfg.accountID)
	}, at)
}

// gatherZoneLogpush discovers zone-scoped Logpush jobs (zone-scoped):
// GET /zones/{zone}/logpush/jobs -> cf.zone -> cf.logpush_job.
func (s *Source) gatherZoneLogpush(ctx context.Context, sink sdk.Sink, at time.Time) error {
	path := "/zones/" + s.cfg.zoneID + "/logpush/jobs"
	return s.emitOrFinding(ctx, sink, originZone, s.cfg.zoneID, "Cloudflare zone Logpush jobs list failed", func() ([]edgeRow, error) {
		return s.logpushRows(ctx, path, originZone, s.cfg.zoneID)
	}, at)
}

// logpushRows fetches and shapes Logpush jobs into edge rows. The resource ref is
// "<dataset>#<job id>" (a stable natural key); the destination is passed through
// redact.SanitizeURL because destination_conf can embed credentials (e.g.
// s3://...?...&secret=...). ownership_challenge, logpull_options and any token
// field are never read or emitted (minimal-data, docs/SECURITY-HARDENING.md).
func (s *Source) logpushRows(ctx context.Context, path, originKind, originRef string) ([]edgeRow, error) {
	raw, err := s.cl.get(ctx, path, nil, nil)
	if err != nil {
		return nil, err
	}
	rows := make([]edgeRow, 0, len(raw))
	for _, item := range raw {
		var j struct {
			ID              int64  `json:"id"`
			Dataset         string `json:"dataset"`
			DestinationConf string `json:"destination_conf"`
		}
		if err := json.Unmarshal(item, &j); err != nil {
			return nil, fmt.Errorf("cloudflare: decode logpush job: %w", err)
		}
		rows = append(rows, edgeRow{
			originKind: originKind,
			originRef:  originRef,
			resKind:    resLogpushJob,
			resRef:     redact.Clean(j.Dataset) + "#" + strconv.FormatInt(j.ID, 10),
			toolRef:    redact.SanitizeURL(j.DestinationConf),
		})
	}
	return rows, nil
}

// unwrapBuckets extracts the buckets array from an R2 list result, which wraps the
// array under {"buckets":[...]}. A result that is already an array (or null) is
// returned unchanged so the shape is tolerated either way.
func unwrapBuckets(result json.RawMessage) (json.RawMessage, error) {
	trimmed := result
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return result, nil // already an array, null, or empty
	}
	var obj struct {
		Buckets json.RawMessage `json:"buckets"`
	}
	if err := json.Unmarshal(result, &obj); err != nil {
		return nil, fmt.Errorf("cloudflare: decode r2 buckets envelope: %w", err)
	}
	return obj.Buckets, nil
}
