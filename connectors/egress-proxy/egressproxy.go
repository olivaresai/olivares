// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package egressproxy

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/meshobs"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.egress-proxy"

// SignalEgressProxy is the connector's provenance value (an open-string
// model.SignalSource, declared package-local like snowflake-audit's): an allow/deny
// verdict an egress proxy WROTE, distinct from a kernel "ebpf" backstop edge or an
// "envoy_als" mesh edge so the operator never silently collapses the two planes
// (ARCHITECTURE.md). The SDK does not seed it; a connector introduces its own.
const SignalEgressProxy model.SignalSource = "egress_proxy"

// SignalAIEndpoint is an allowed observation of an operator-configured AI host.
// It certifies neither the log's completeness nor the endpoint's governance.
const SignalAIEndpoint model.SignalSource = "egress_proxy_ai"

// toolEgressVerdict is the ToolRef/Tool stamped on every observation — the surface
// that rendered the verdict.
const toolEgressVerdict = "egress_proxy.verdict"

// maxLine is the scanner's max token size: an egress verdict line is small, but a
// proxy that inlines a policy blob can be large; 4 MiB tolerates that without OOM.
const maxLine = 4 * 1024 * 1024

// logExtensions are the file extensions a directory contributes. A verdict log is
// JSON-lines, so .log/.json/.jsonl/.ndjson are all accepted.
var logExtensions = map[string]bool{".log": true, ".json": true, ".jsonl": true, ".ndjson": true}

// Source is the egress-proxy verdict-log connector. It is a PURE FILE PARSER: it
// reads the verdict log the operator's egress proxy already writes and emits one
// edge (allow) or finding (deny) per line. It opens no listener and makes no outbound
// connection (see the package doc and the no-listener test). The zero value is not
// usable; call New.
type Source struct {
	path    string
	aiHosts map[string]bool
	now     func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies the source-connector contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an egress-proxy source with default configuration (batch mode: the
// engine re-polls the verdict export; de-dup is on the observation natural key).
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Egress proxy verdicts (allow/deny)",
		Description: "Observes an agent egress proxy's allow/deny verdict log (FQDN allowlist) as read-first egress edges + permitted-path-violation findings. Pure file parser; opens no listener..",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "egress-proxy verdict log file, or a directory of *.log / *.json / *.jsonl / *.ndjson files (one JSON decision per line)."},
			{Key: "ai_hosts", Type: sdk.FieldString, Description: "Optional comma-separated exact AI host names. No URLs, ports or wildcards. Classification covers only these hosts in this log; absence of observations proves no estate completeness."},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration error
// reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if s.now == nil {
		s.now = time.Now
	}
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("egress-proxy: path is required")
	}
	s.aiHosts = nil
	if configured := strings.TrimSpace(cfg.Get("ai_hosts")); configured != "" {
		s.aiHosts = make(map[string]bool)
		for _, value := range strings.Split(configured, ",") {
			host, ok := aiHost(value)
			if !ok {
				return errors.New("egress-proxy: ai_hosts requires exact host names without URLs, ports or wildcards")
			}
			s.aiHosts[host] = true
		}
	}
	return nil
}

func aiHost(value string) (string, bool) {
	value = strings.TrimSpace(value)
	for _, c := range value {
		if c > 127 {
			return "", false
		}
	}
	host := strings.TrimSuffix(strings.ToLower(value), ".")
	if host == "" || len(host) > 253 {
		return "", false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, c := range label {
			if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return "", false
			}
		}
	}
	return host, true
}

// Gather reads the configured verdict log(s) line by line and emits an edge per
// allow and a finding per deny. It is a BATCH POLLER: it returns nil when the files
// are exhausted so the engine re-runs it; re-reading a file each run is safe because
// the engine de-dups on the observation natural key. A blank/garbage line is
// tolerated and skipped — it never fails the run.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) (err error) {
	if len(s.aiHosts) != 0 {
		defer func() {
			if ctx.Err() != nil {
				return
			}
			report := model.InventoryCollectionReport{State: "unknown", Reason: "scope_unproven", ObservedUntil: s.clock()}
			if err != nil {
				var pathError *os.PathError
				if !errors.As(err, &pathError) {
					return // preserve scanner, sink and cancellation errors
				}
				report.State, report.Reason = "unavailable", "gather_error"
			}
			// The native structured result carries the failure without file paths or
			// raw log text. Successful parsing still proves no complete estate scope.
			err = sink.Emit(ctx, report)
		}()
	}
	files, err := s.listFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.scanFile(ctx, f, sink); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; this connector holds no handles between Gather runs
// (each file is opened, scanned and closed within scanFile).
func (s *Source) Close(context.Context) error { return nil }

// scanFile reads one verdict-log file, emitting an observation per parseable line. It
// opens the file for READ ONLY (os.Open), never a listener; the file handle is closed
// before the function returns.
func (s *Source) scanFile(ctx context.Context, path string, sink sdk.Sink) error {
	f, err := os.Open(path) //nolint:gosec // path is operator-configured (read-first artifact)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		rec, ok := parseLine(sc.Bytes())
		if !ok {
			continue // blank/garbage/no-destination line — tolerated, never fatal
		}
		if err := s.emit(ctx, sink, rec); err != nil {
			return err
		}
	}
	return sc.Err()
}

// emit builds the meshobs record for one verdict and emits its observation(s). A
// record whose decision is neither allow nor deny is skipped (the verdict is taken
// verbatim, never guessed).
func (s *Source) emit(ctx context.Context, sink sdk.Sink, rec record) error {
	mr, ok := s.toMeshRecord(rec)
	if !ok {
		return nil
	}
	return mr.Emit(ctx, sink)
}

// toMeshRecord maps a verdict record onto the shared L7 builder. OriginVerified is
// always false — a log line is not a cryptographically verified peer identity, so the
// edge is Approximate (the same honesty as the eBPF backstop). A deny carries its
// (later scrubbed + hashed) reason. ok=false when the decision is unclassifiable.
func (s *Source) toMeshRecord(rec record) (meshobs.Record, bool) {
	verdict, ok := classifyVerdict(rec.decision)
	if !ok {
		return meshobs.Record{}, false
	}
	ts, timeKnown := parseTime(rec.timestamp)
	if !timeKnown {
		ts = s.clock()
	}
	mr := meshobs.Record{
		OriginRef:      rec.identity, // empty becomes "unknown" inside meshobs
		OriginVerified: false,        // a log line is not a cryptographic identity
		FQDN:           rec.host,
		Port:           rec.port,
		Method:         rec.method,
		Verdict:        verdict,
		Source:         SignalEgressProxy,
		Tool:           toolEgressVerdict,
		ObservedAt:     ts,
	}
	if host, valid := aiHost(rec.host); valid && s.aiHosts[host] && verdict == meshobs.VerdictAllowed {
		mr.FQDN, mr.Source = host, SignalAIEndpoint
		if !timeKnown {
			// Reception time remains available natively; an absent log occurrence
			// must not be presented as a source-declared timestamp.
			mr.ObservedAt = time.Time{}
		}
	}
	if verdict == meshobs.VerdictDenied {
		// The deny reason rides the finding's (scrubbed, hashed) detail. No taxonomy is
		// asserted: a denied egress has no single honest OWASP/ATLAS mapping.
		mr.DenyReason = rec.reason
	}
	return mr, true
}

// listFiles resolves the configured path to a sorted list of files (a directory
// contributes its verdict-log entries; a file contributes itself).
func (s *Source) listFiles() ([]string, error) {
	fi, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{s.path}, nil
	}
	entries, err := os.ReadDir(s.path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if logExtensions[strings.ToLower(filepath.Ext(e.Name()))] {
			files = append(files, filepath.Join(s.path, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

// clock returns the connector's time source (injectable in tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
