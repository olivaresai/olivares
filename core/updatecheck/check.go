// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package updatecheck is the engine-side "is an update available?" probe behind the
// console's update indicator. It fetches the configured channel's signed
// manifest, verifies it OFFLINE against the embedded OTA key, and reports
// whether a newer release exists — WITHOUT ever changing the binary (that is the
// operator's explicit `olivares upgrade`). It is opt-in and air-gap-honest: with no
// endpoint configured it reports Enabled=false (silence, never an error), and a
// fetch/verify failure is captured in Error (the console shows "check failed", it
// does not crash). It shares the exact manifest verifier the CLI upgrade uses, so
// "an update is available" here rests on the same signature check.
//
// It is NOT the CLI's decision, and this comment used to claim it was
// ("anti-rollback-aware"). It is a NOTIFICATION: it reports whether the channel holds a
// version with higher precedence, and it does not apply the min-version gate, the
// rollout cohort, or the license checks that `olivares upgrade` applies before it will
// act. Treating the indicator as the verdict is what let an unstamped build be told an
// OLDER release was "available" — the zero version made every release look higher.
package updatecheck

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/release"
)

// Status is the console-facing result of an update check (JSON-rendered as-is).
type Status struct {
	Enabled        bool      `json:"enabled"`                  // update checking is configured (an endpoint + key)
	Available      bool      `json:"available"`                // a newer release exists on the channel
	UpToDate       bool      `json:"up_to_date"`               // running the latest (or newer) on the channel
	Channel        string    `json:"channel"`                  // the channel checked
	CurrentVersion string    `json:"current_version"`          // the running version
	LatestVersion  string    `json:"latest_version,omitempty"` // the channel's current version
	Security       bool      `json:"security,omitempty"`       // the available release carries a security fix
	Advisories     []string  `json:"advisories,omitempty"`     // CVE/OSV ids the available release fixes
	CheckedAt      time.Time `json:"checked_at,omitempty"`     // when the last check ran
	Error          string    `json:"error,omitempty"`          // last check failure (transient; not fatal)
}

// Config parameterises a check. Endpoint empty (or a nil PubKey) means "disabled".
//
// Channel: `lts` is a THIRD value release.ValidChannel accepts, and no lts line is produced
// or published (an internal design note (not shipped):98-116,144, and
// an internal design note (not shipped):665-670 — general_backports: false). This comment enumerated it as
// if it were an offer, which makes it the THIRD place the channel set is stated to a reader —
// after `olivares upgrade --channel` and `olivares release manifest --channel`, both
// corrected in C03-22. It is named rather than omitted because the validator does accept it,
// and hiding an accepted value documents a narrower validator than the one that runs; what it
// may not do is read as a product line, which on an exported field of a public package is the
// same false promise the two flag strings were.
type Config struct {
	// Endpoint is the update channel: a GitHub repository (or one of its releases), or a
	// static mirror base. Which layout it means is resolved by release.ResolveChannel, so
	// this indicator reads exactly the URL `olivares upgrade` reads.
	Endpoint       string
	Channel        string            // stable | security (default stable; lts validates, nothing publishes it)
	CurrentVersion string            // the running binary version
	InstallID      string            // stable rollout-bucket identity (not a secret)
	PubKey         ed25519.PublicKey // the embedded OTA key
	Client         *http.Client      // optional (default 15s timeout)
	Now            func() time.Time  // optional (test seam)
}

// Check performs ONE offline-verified update check. It never returns an error:
// air-gap / not-configured => Enabled=false (silent); a network or signature
// failure => Enabled=true with Error set. It performs no writes and no swap.
func Check(ctx context.Context, cfg Config) Status {
	now := time.Now().UTC()
	if cfg.Now != nil {
		now = cfg.Now()
	}
	channel := strings.TrimSpace(cfg.Channel)
	if channel == "" {
		channel = release.ChannelStable
	}
	if strings.TrimSpace(cfg.Endpoint) == "" || cfg.PubKey == nil {
		// Air-gap / unconfigured: silent, not an error.
		return Status{Enabled: false, Channel: channel, CurrentVersion: cfg.CurrentVersion}
	}
	st := Status{Enabled: true, Channel: channel, CurrentVersion: cfg.CurrentVersion, CheckedAt: now}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	// WHERE the manifest lives is NOT spelled out here. It used to be — this line read
	// `Endpoint + "/" + channel + "/manifest.json"`, the same string `olivares upgrade`
	// carried in its own package — and while there was one layout the duplication was
	// invisible. FIRMA B (2026-08-21) put the community channel on GitHub Releases,
	// whose assets are FLAT, so there are now two layouts and two readers of the same
	// channel. A fact in two places drifts: rewiring only the CLI would leave this badge
	// 404-ing against the very carrier the product ships with, reporting a transport error
	// the operator cannot act on. One resolver, both readers (core/release/channelurl.go).
	layout, err := release.ResolveChannel(cfg.Endpoint, channel)
	if err != nil {
		return fail(st, &checkFailure{
			stage:  stageResolve,
			where:  displayEndpoint(cfg.Endpoint),
			reason: resolveReason(cfg.Endpoint, channel),
			err:    err,
		})
	}
	manifestURL, signatureURL := layout.ManifestURL(), layout.SignatureURL()
	where := displayEndpoint(manifestURL)
	mb, err := get(ctx, client, manifestURL, stageManifest)
	if err != nil {
		return fail(st, err)
	}
	sig, err := get(ctx, client, signatureURL, stageSignature)
	if err != nil {
		return fail(st, err)
	}
	m, err := release.VerifyManifest(mb, sig, cfg.PubKey)
	if err != nil {
		return fail(st, &checkFailure{stage: stageVerify, where: where, reason: verifyReason(err), err: err})
	}
	// SAME BINDING AS THE CLI, and the console needs it more, not less: this runs
	// unattended and its whole output is a badge. The signature proves the manifest is ours;
	// it does not prove it is the CHANNEL we asked for, because all three are signed by the
	// same key. A stable manifest answering a `security` check renders a calm "up to date"
	// for an estate that is not — and the operator never typed a command to inspect.
	//
	// It is an ERROR and never a silent downgrade to stable: this struct already has three
	// states, and "I could not check" is the honest one when the answer is about a different
	// channel than the question.
	if got := strings.TrimSpace(m.Channel); got != channel {
		return fail(st, &checkFailure{stage: stageBinding, where: where, reason: wrongChannelReason(channel, got)})
	}
	if m.Stale(now) {
		// Anti-freeze: a stale (but validly signed) manifest is not a trustworthy
		// "up to date" signal — surface it as a failed check, not silence.
		return fail(st, &checkFailure{stage: stageFreshness, where: where, reason: reasonExpired})
	}
	plan, err := m.PlanUpgrade(cfg.CurrentVersion, "", "", cfg.InstallID, now)
	if err != nil {
		return fail(st, &checkFailure{stage: stageCompare, reason: reasonVersionUnparsable, err: err})
	}
	st.LatestVersion = m.Version
	// An unstamped build has no position in the ordering, so neither answer is available
	//. Reading plan.Direction anyway told a source build that an OLDER release was
	// "available", because the zero Version sits below every release. Report the reason
	// instead of a fabricated verdict: Available and UpToDate both stay false, which is
	// what the console already renders when a check could not conclude.
	if !plan.CurrentKnown {
		return fail(st, &checkFailure{stage: stageCompare, reason: reasonUnstamped + channel})
	}
	st.Available = plan.Direction > 0 // a strictly newer release
	st.UpToDate = plan.Direction <= 0 // running the latest or newer
	if st.Available {
		st.Security = m.Security
		st.Advisories = m.Advisories
	}
	return st
}

func get(ctx context.Context, client *http.Client, rawURL, stage string) ([]byte, error) {
	where := displayEndpoint(rawURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &checkFailure{stage: stage, where: where, reason: reasonBadRequestURL, err: err}
	}
	req.Header.Set("User-Agent", "olivares-updatecheck")
	resp, err := client.Do(req)
	if err != nil {
		return nil, &checkFailure{stage: stage, where: where, reason: transportReason(ctx, err), err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, &checkFailure{stage: stage, where: where, reason: fmt.Sprintf(reasonHTTPStatus, resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes+1))
	if err != nil {
		return nil, &checkFailure{stage: stage, where: where, reason: reasonReadFailed, err: err}
	}
	if len(body) > maxMetadataBytes {
		return nil, &checkFailure{stage: stage, where: where, reason: reasonOversize}
	}
	return body, nil
}

const maxMetadataBytes = 1 << 20

const maxStatusError = 400

// checkFailure exposes owned stage/reason text and a display-only endpoint.
// The underlying cause is available through Unwrap and must not reach Status.Error.
type checkFailure struct {
	stage  string
	where  string
	reason string
	err    error
}

func (e *checkFailure) Error() string {
	msg := "update check: " + e.stage
	if e.where != "" {
		msg += " (" + e.where + ")"
	}
	return msg + ": " + e.reason
}

func (e *checkFailure) Unwrap() error { return e.err }

func fail(st Status, err error) Status {
	st.Error = statusMessage(err)
	st.Available = false
	st.UpToDate = false
	st.Security = false
	st.Advisories = nil
	return st
}

func statusMessage(err error) string {
	var f *checkFailure
	if !errors.As(err, &f) {
		return "update check: failed for an unrecognized reason"
	}
	return clampStatusError(f.Error())
}

func clampStatusError(s string) string {
	if len(s) <= maxStatusError {
		return s
	}
	cut := maxStatusError
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

const (
	stageResolve   = "resolving the update endpoint"
	stageManifest  = "fetching the channel manifest"
	stageSignature = "fetching the manifest signature"
	stageVerify    = "verifying the channel manifest"
	stageBinding   = "checking the channel binding"
	stageFreshness = "checking manifest freshness"
	stageCompare   = "comparing versions"
)

const (
	reasonChannelUnknown  = "the configured channel is not one of the published channels"
	reasonEndpointEmpty   = "the configured endpoint is empty"
	reasonEndpointNotURL  = "the configured endpoint is not an absolute URL"
	reasonEndpointQueryFr = "an update endpoint is a base path, so it carries no query string and no fragment"
	reasonEndpointLayout  = "the endpoint is not a usable public channel layout"

	reasonBadRequestURL = "the resolved address is not a usable request URL"
	reasonNetwork       = "network error"
	reasonCanceled      = "the check was canceled"
	reasonTimeout       = "the request timed out"
	reasonReadFailed    = "the response could not be read"
	reasonOversize      = "the response is larger than the 1 MiB limit for channel metadata"
	reasonHTTPStatus    = "HTTP %d"

	reasonNoKey        = "this build has no usable OTA verification key"
	reasonBadSignature = "the signature does not verify against the OTA key"
	reasonBadSigFormat = "the detached signature is not in a readable form"
	reasonBadManifest  = "the signed manifest is not one this build accepts"

	reasonExpired           = "the channel manifest is expired (stale or frozen mirror)"
	reasonVersionUnparsable = "this build's version stamp is not a parsable version"
	reasonUnstamped         = "this build carries no version stamp, so it cannot be compared against channel "
)

func resolveReason(endpoint, channel string) string {
	if !release.ValidChannel(strings.TrimSpace(channel)) {
		return reasonChannelUnknown
	}
	raw := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if raw == "" {
		return reasonEndpointEmpty
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return reasonEndpointNotURL
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return reasonEndpointQueryFr
	}
	return reasonEndpointLayout
}

func verifyReason(err error) string {
	switch {
	case errors.Is(err, release.ErrNoKey):
		return reasonNoKey
	case errors.Is(err, release.ErrBadSignature):
		return reasonBadSignature
	}
	var me *release.ManifestError
	if errors.As(err, &me) {
		return reasonBadManifest
	}
	return reasonBadSigFormat
}

func wrongChannelReason(asked, served string) string {
	what := "a channel this build does not publish"
	if release.ValidChannel(served) {
		what = "channel " + served
	}
	return "asked for channel " + asked + ", and the endpoint served a manifest signed for " + what +
		"; the signature is valid, so this is a wrong-channel answer (stale or misrouted mirror), not a forgery"
}

func transportReason(ctx context.Context, err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return reasonCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return reasonTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return reasonTimeout
	}
	return reasonNetwork
}

const (
	displayEndpointMaxLen      = 96
	displayEndpointUnavailable = ""
)

func displayEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return displayEndpointUnavailable
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return displayEndpointUnavailable
	}
	if u.Host == "" {
		return displayEndpointUnavailable
	}
	out := scheme + "://" + u.Host
	if len(out) > displayEndpointMaxLen || !printableASCII(out) {
		return displayEndpointUnavailable
	}
	return out
}

func printableASCII(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// Checker caches the latest Status and refreshes it on an interval, so the console
// health endpoint reads a cached value rather than hitting the network per request.
type Checker struct {
	cfg      Config
	interval time.Duration
	mu       sync.RWMutex
	latest   Status
}

// NewChecker builds a Checker. interval <= 0 defaults to 6h.
func NewChecker(cfg Config, interval time.Duration) *Checker {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	return &Checker{cfg: cfg, interval: interval, latest: Status{Enabled: cfg.Endpoint != "" && cfg.PubKey != nil, Channel: cfg.Channel, CurrentVersion: cfg.CurrentVersion}}
}

// Latest returns the most recent cached Status (safe before the first refresh).
func (c *Checker) Latest() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest
}

// Refresh runs one check now and caches it.
func (c *Checker) Refresh(ctx context.Context) Status {
	s := Check(ctx, c.cfg)
	c.mu.Lock()
	c.latest = s
	c.mu.Unlock()
	return s
}

// Run refreshes immediately, then on the interval, until ctx is canceled. It is
// intended to run in its own goroutine for the engine's lifetime.
func (c *Checker) Run(ctx context.Context) {
	c.Refresh(ctx)
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Refresh(ctx)
		}
	}
}
