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
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

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
		st.Error = err.Error()
		return st
	}
	mb, err := get(ctx, client, layout.ManifestURL())
	if err != nil {
		st.Error = err.Error()
		return st
	}
	sig, err := get(ctx, client, layout.SignatureURL())
	if err != nil {
		st.Error = err.Error()
		return st
	}
	m, err := release.VerifyManifest(mb, sig, cfg.PubKey)
	if err != nil {
		st.Error = err.Error()
		return st
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
		st.Error = fmt.Sprintf("asked for channel %s and the endpoint served a manifest signed for channel %s — the signature is valid, so this is a wrong-channel answer (stale mirror or misrouted endpoint), not a forgery", channel, got)
		return st
	}
	if m.Stale(now) {
		// Anti-freeze: a stale (but validly signed) manifest is not a trustworthy
		// "up to date" signal — surface it as a failed check, not silence.
		st.Error = "channel manifest is expired (stale or frozen mirror?)"
		return st
	}
	plan, err := m.PlanUpgrade(cfg.CurrentVersion, "", "", cfg.InstallID, now)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.LatestVersion = m.Version
	// An unstamped build has no position in the ordering, so neither answer is available
	//. Reading plan.Direction anyway told a source build that an OLDER release was
	// "available", because the zero Version sits below every release. Report the reason
	// instead of a fabricated verdict: Available and UpToDate both stay false, which is
	// what the console already renders when a check could not conclude.
	if !plan.CurrentKnown {
		st.Error = fmt.Sprintf("this build carries no version stamp (%q), so it cannot be compared against channel %s", cfg.CurrentVersion, channel)
		return st
	}
	st.Available = plan.Direction > 0 // a strictly newer release
	st.UpToDate = plan.Direction <= 0 // running the latest or newer
	if st.Available {
		st.Security = m.Security
		st.Advisories = m.Advisories
	}
	return st
}

func get(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "olivares-updatecheck")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update check: %s returned %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // manifests are small
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
