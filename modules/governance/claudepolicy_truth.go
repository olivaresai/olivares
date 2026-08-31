// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/connectors/managedsettings"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file is the policy TRUTH LOOP: the pieces that close the gap between
// "a revision was published" and "the published policy actually governs".
//
//   - PolicyArtifactDistributor implements the ManagedDistributor seam for the
//     DECIDED v1 mechanism: the plane signs the exact
//     rendered bytes and persists them as an immutable distribution record; agents
//     PULL the signed artifact (GET /{surface}/artifact) and CHECK IN with an
//     attestation of what they verified+applied plus the config they observe
//     (POST /{surface}/checkin). Push via MDM is out of v1. DENY-CLOSED: Distribute
//     returns nil only after the signed record COMMITTED — a publish is never
//     reported "distributed" on any failure.
//   - PolicyObservedStore implements the ObservedConfigProvider seam over the
//     check-in rows: the OBSERVED side of the PERMITTED-vs-OBSERVED drift.
//   - The check-in and publish paths compute drift with the VERIFIED connector
//     logic (managedsettings.VerifyDriftJSON — never re-implemented) and record it
//     as REAL core Findings (kind "policy_drift") + finding.reported bus events, so
//     the SOC/web drift views reflect reality. Honesty: with no observation the
//     response SAYS drift was not computed — never a fabricated "no drift".

// policyConsoleName is the console's stable identifier (Descriptor name, finding
// Source and bus-event source).
const policyConsoleName = "olivares.claude-policy"

// policyDriftKind is the finding kind of every PERMITTED-vs-OBSERVED policy drift
// fact (the same kind the managedsettings connector emits — verify.go — so the web
// drift view aggregates both signals under one filter).
const policyDriftKind = "policy_drift"

// policySubjectKind is the drift findings' subject vocabulary (managedsettings
// connector verify.go — shared, never re-invented).
const policySubjectKind = "managed_policy"

// artifactPreimagePrefix is the domain-separation prefix of the signed preimage:
//
//	sha256(artifactPreimagePrefix | tenant | surface | revision | hex(sha256(rendered)))
//
// signed with a detached Ed25519 signature. The prefix pins the signature to the
// policy-artifact domain (a catalog-entry signature can never be replayed as a
// policy artifact and vice versa), and binding tenant/surface/revision into the
// preimage means a signed artifact cannot be re-served for a different target.
const artifactPreimagePrefix = "olivares.policy.artifact.v1"

// maxScopeLen bounds the check-in scope (a host id / org-distribution name).
const maxScopeLen = 128

// credentialPatterns redact secret material out of an OBSERVED document before
// it is stored (docs/SECURITY-HARDENING.md, minimal data). The OBSERVED file is evidence we did
// not author: a credential inside it is itself a HIGH drift finding, but the
// secret material must never reach the store. This is a guardrail covering the
// obvious shapes (an Anthropic/OpenAI-style key, the common provider token
// prefixes, a JWT, and the value of any secret-named JSON member), not a
// scanner — the publish path already rejects authored credentials outright.
var credentialTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{4,}`),                                          // Anthropic sk-ant-…, OpenAI sk-…
	regexp.MustCompile(`(?:ghp|gho|ghu|ghs|github_pat)_[A-Za-z0-9_]{16,}`),               // GitHub tokens
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                                               // AWS access key id
	regexp.MustCompile(`xox[a-z]-[A-Za-z0-9\-]{10,}`),                                    // Slack tokens
	regexp.MustCompile(`AIza[0-9A-Za-z_\-]{30,}`),                                        // Google API key
	regexp.MustCompile(`whsec_[A-Za-z0-9]{8,}`),                                          // webhook signing secrets
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}`), // JWT
}

// credentialMemberPattern matches the VALUE of any secret-named JSON member (the
// observed env block is the risk); the member name is kept (governance signal),
// only the value is redacted.
var credentialMemberPattern = regexp.MustCompile(`(?i)("[a-z0-9_\-]*(?:token|secret|password|passwd|api_key|apikey|access_key|client_secret|credential)[a-z0-9_\-]*"\s*:\s*)"[^"]+"`)

// redactInlineCredentials returns content with every recognized credential shape
// replaced by a fixed marker, and whether any was found.
func redactInlineCredentials(content string) (string, bool) {
	found := false
	for _, pat := range credentialTokenPatterns {
		if pat.MatchString(content) {
			found = true
			content = pat.ReplaceAllString(content, "[REDACTED]")
		}
	}
	if credentialMemberPattern.MatchString(content) {
		found = true
		content = credentialMemberPattern.ReplaceAllString(content, `$1"[REDACTED]"`)
	}
	return content, found
}

// artifactHash computes the domain-separated signing hash of a rendered artifact.
func artifactHash(tenant model.TenantID, surface string, revision int64, renderedSHA string) []byte {
	preimage := artifactPreimagePrefix + "|" + tenant.String() + "|" + surface + "|" +
		strconv.FormatInt(revision, 10) + "|" + renderedSHA
	sum := sha256.Sum256([]byte(preimage))
	return sum[:]
}

// shortFingerprint is the catalog-convention display fingerprint of a public key
// (first 16 hex chars of its SHA-256).
func shortFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

// --- PolicyArtifactDistributor (the seam, made real) -----------------------

// PolicyArtifactDistributor signs a published revision's rendered bytes with the
// plane's policy signing key and persists the immutable distribution record the
// agent pull serves. Constructed in the composition root (which owns the key);
// its store handle is late-bound by boot() (the knowledgeGuard pattern).
//
// DENY-CLOSED: an unbound store, a missing key, or any persist failure returns an
// error — publish then reports "enqueue-failed", never "distributed".
type PolicyArtifactDistributor struct {
	mu    sync.RWMutex
	data  api.ModuleData
	priv  ed25519.PrivateKey
	clock model.Clock
}

var _ ManagedDistributor = (*PolicyArtifactDistributor)(nil)

// NewPolicyArtifactDistributor builds the distributor around the plane's policy
// signing key. A nil/short key returns nil — the caller then leaves the seam
// unwired (the honest "seam-pending" posture), exactly like the catalog's
// unsigned fallback.
func NewPolicyArtifactDistributor(priv ed25519.PrivateKey) *PolicyArtifactDistributor {
	if len(priv) != ed25519.PrivateKeySize {
		return nil
	}
	return &PolicyArtifactDistributor{priv: priv, clock: model.SystemClock{}}
}

// UseData late-binds the store handle (boot() calls it after the store opens).
func (d *PolicyArtifactDistributor) UseData(data api.ModuleData) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.data = data
}

// WithDistributorClock overrides the clock (tests inject a deterministic clock).
func (d *PolicyArtifactDistributor) WithDistributorClock(clk model.Clock) *PolicyArtifactDistributor {
	d.clock = clk
	return d
}

// Distribute signs the rendered bytes and persists the distribution record. It
// returns nil ONLY after the record committed: the record IS the distribution
// evidence the pull endpoint serves.
func (d *PolicyArtifactDistributor) Distribute(ctx context.Context, tenant model.TenantID, surface, _ string, revision int64, rendered []byte) error {
	d.mu.RLock()
	data := d.data
	d.mu.RUnlock()
	if data == nil {
		return errStoreUnbound
	}
	if revision <= 0 || len(rendered) == 0 {
		return errBadArtifact
	}
	sum := sha256.Sum256(rendered)
	renderedSHA := hex.EncodeToString(sum[:])
	sig := ed25519.Sign(d.priv, artifactHash(tenant, surface, revision, renderedSHA))
	pub := d.priv.Public().(ed25519.PublicKey)
	now := d.clock.Now()
	return data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(distributionKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			colDistSurface:  surface,
			colDistRevision: revision,
			colDistRendered: string(rendered),
			colDistSHA:      renderedSHA,
			colDistSig:      base64.StdEncoding.EncodeToString(sig),
			colDistPubKey:   base64.StdEncoding.EncodeToString(pub),
			colDistKeyFP:    shortFingerprint(pub),
			colDistSignedAt: now.String(),
		})
		return err
	})
}

// Sentinel errors of the deny-closed Distribute path (also asserted by tests).
var (
	errStoreUnbound = distributorError("policy distributor: store handle not bound yet (boot in progress) — deny-closed")
	errBadArtifact  = distributorError("policy distributor: refusing to sign an empty artifact or a non-positive revision")
)

type distributorError string

func (e distributorError) Error() string { return string(e) }

// --- PolicyObservedStore (the ObservedConfigProvider seam, made real) ----------

// PolicyObservedStore serves the OBSERVED host configurations recorded by the
// attested agent check-ins (POST /{surface}/checkin). It is the store-backed
// implementation of the ObservedConfigProvider seam: one ObservedConfig per
// (surface, scope) row, newest state. Its store handle is late-bound by boot().
type PolicyObservedStore struct {
	mu   sync.RWMutex
	data api.ModuleData
}

var _ ObservedConfigProvider = (*PolicyObservedStore)(nil)

// NewPolicyObservedStore returns the provider (store handle late-bound by boot()).
func NewPolicyObservedStore() *PolicyObservedStore { return &PolicyObservedStore{} }

// UseData late-binds the store handle.
func (s *PolicyObservedStore) UseData(data api.ModuleData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = data
}

// Observed returns every OBSERVED config for the surface (one per scope). An
// unbound store is an error — the caller reports "could not read", never "none".
func (s *PolicyObservedStore) Observed(ctx context.Context, tenant model.TenantID, surface string) ([]ObservedConfig, error) {
	s.mu.RLock()
	data := s.data
	s.mu.RUnlock()
	if data == nil {
		return nil, errStoreUnbound
	}
	var out []ObservedConfig
	err := data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(observedKind)
		if err != nil {
			return err
		}
		recs, err := listAll(ctx, repo, eq(colObsSurface, surface))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			// A scope that has only checked in attestations (no observed_content)
			// is NOT an observation of the host config — skipping it keeps the
			// drift path from reading "no content reported" as "file absent on
			// host" (which VerifyDriftJSON flags HIGH "host is ungoverned").
			if rec.String(colObsContent) == "" {
				continue
			}
			out = append(out, ObservedConfig{
				Scope:      rec.String(colObsScope),
				Content:    []byte(rec.String(colObsContent)),
				ObservedAt: rec.String(colObsCheckedInAt),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- drift recording (shared by publish and check-in) --------------------------

// drainOpenPolicyDrift reads EVERY open policy_drift finding of the tenant (the
// de-dup set / truth-view counts). It drains all pages: a single default page is
// the oldest 1000 rows tenant-wide, and a truncated de-dup set silently
// re-creates (and re-emits) every finding that fell off it — the exact trap the
// HITL ingester documents (modules/security/anomaly.go).
func drainOpenPolicyDrift(ctx context.Context, sc store.Scope) ([]model.Finding, error) {
	var out []model.Finding
	q := model.Query{Filters: []model.Filter{
		eq("kind", policyDriftKind), eq("status", string(model.FindingOpen)),
	}, Limit: listCap}
	for {
		finds, page, err := sc.Findings().List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, finds...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// recordDriftFindings persists drift reports as REAL core Findings (the SOC/web
// views read them) with an open-finding de-dup on (kind, detail hash, scope). It
// returns the DTOs (finding ids filled) and the reports that were newly created
// (the ones to emit on the bus AFTER commit — house rule, sweep.go). It does NOT
// touch the scope's drift counters: stamping is the CALLER's explicit act,
// performed only when a content-drift computation actually ran (a counter
// stamped off absence would fabricate compliance).
func (c *PolicyConsole) recordDriftFindings(ctx context.Context, mc api.ModuleContext, surface string, revision int64, scope string, reports []sdkmodel.FindingReport) ([]policyDriftDTO, []sdkmodel.FindingReport, error) {
	dtos := make([]policyDriftDTO, 0, len(reports))
	var fresh []sdkmodel.FindingReport
	if len(reports) == 0 {
		return dtos, nil, nil
	}
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		// Every open policy_drift finding, drained: the de-dup set.
		open, err := drainOpenPolicyDrift(ctx, sc)
		if err != nil {
			return err
		}
		seen := make(map[string]model.ID, len(open))
		for _, f := range open {
			ref, _ := f.Metadata["subject_ref"].(string)
			seen[hex.EncodeToString(f.DetailHash)+"|"+ref] = f.ID
		}
		for _, rep := range reports {
			hashBytes, derr := hex.DecodeString(rep.DetailHash)
			if derr != nil {
				// A non-hex detail hash is hashed again rather than dropped: the
				// finding must never be lost to an encoding surprise.
				sum := sha256.Sum256([]byte(rep.DetailHash))
				hashBytes = sum[:]
			}
			dto := driftToDTO(rep)
			if id, dup := seen[hex.EncodeToString(hashBytes)+"|"+scope]; dup {
				dto.ID = id.String()
				dtos = append(dtos, dto)
				continue // already open — never duplicate the same standing drift fact
			}
			created, ferr := sc.Findings().Create(ctx, model.Finding{
				Kind:        rep.Kind,
				Severity:    model.Severity(rep.Severity),
				Status:      model.FindingOpen,
				Source:      policyConsoleName,
				SubjectKind: rep.SubjectKind,
				Title:       rep.Title,
				DetailHash:  hashBytes,
				OccurredAt:  c.clock.Now(),
				Metadata: map[string]any{
					"subject_ref": scope,
					"surface":     surface,
					"revision":    revision,
				},
			})
			if ferr != nil {
				return ferr
			}
			dto.ID = created.ID.String()
			dtos = append(dtos, dto)
			fresh = append(fresh, rep)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return dtos, fresh, nil
}

// stampObservedDrift records the result of a CONTENT-drift computation on the
// scope's observed row (count = content findings of that computation, the same
// semantics at check-in and at publish). Callers invoke it ONLY when
// VerifyDriftJSON actually ran — never off an absence. A missing row (e.g. a
// test provider not backed by check-ins) is a no-op: the counters describe
// check-in rows, nothing else.
func (c *PolicyConsole) stampObservedDrift(ctx context.Context, mc api.ModuleContext, surface, scope string, count int) error {
	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(observedKind)
		if err != nil {
			return err
		}
		rec, found, err := findOne(ctx, repo, eq(colObsSurface, surface), eq(colObsScope, scope))
		if err != nil || !found {
			return err
		}
		rec[colObsDriftCount] = int64(count)
		rec[colObsDriftAt] = c.clock.Now().String()
		_, err = repo.Update(ctx, rec)
		return err
	})
}

// publishFindings emits drift reports on the bus (finding.reported — notify/SIEM/
// correlation) AFTER the store commit. Nil-safe on an absent host.
func (c *PolicyConsole) publishFindings(ctx context.Context, tenant model.TenantID, reports []sdkmodel.FindingReport) {
	if c.host == nil {
		return
	}
	for _, rep := range reports {
		if err := c.host.Publish(ctx, event.FromObservation(tenant.String(), policyConsoleName, rep)); err != nil && c.log != nil {
			c.log.Warn("claude-policy: drift finding bus publish failed (the finding IS persisted)", "err", err)
		}
	}
}

// runDrift computes PERMITTED-vs-OBSERVED drift for a surface against every
// observed scope, records the findings, and returns (DTOs, notes). It is HONEST
// about every absence: no provider, a failing provider, no observation, or a
// non-managed-settings surface each say so — never a fabricated "no drift".
// The second return (computed) distinguishes a REAL empty drift list from an
// honest unknown — the caller/UI must never render the latter as "no drift".
func (c *PolicyConsole) runDrift(ctx context.Context, mc api.ModuleContext, surface, authored string, revision int64, notes []string) ([]policyDriftDTO, bool, []string) {
	out := []policyDriftDTO{}
	if c.observed == nil {
		return out, false, append(notes, "drift not computed: no observed-config source is wired (PERMITTED-vs-OBSERVED verification pending live ingest)")
	}
	observations, err := c.observed.Observed(ctx, mc.Tenant, surface)
	if err != nil {
		return out, false, append(notes, "drift not computed: the observed-config source could not be read — absence of signal, not absence of drift")
	}
	if len(observations) == 0 {
		return out, false, append(notes, "drift not computed: no observed host config available for this surface (no agent check-in yet)")
	}
	if surface != surfaceManagedSettings {
		return out, false, append(notes, "drift verification for "+surface+" reflects the connector findings stream; publish-time diff is managed-settings-only today")
	}
	at := c.clock.Now().Time()
	var clean, diffed int
	for _, obs := range observations {
		reports, derr := managedsettings.VerifyDriftJSON(obs.Scope, []byte(authored), obs.Content, at)
		if derr != nil {
			// VerifyDriftJSON errors only on a malformed AUTHORED doc — unreachable
			// after validation, but reported honestly if it ever happens.
			notes = append(notes, "drift not computed for scope "+obs.Scope+": "+derr.Error())
			continue
		}
		diffed++
		dtos, fresh, rerr := c.recordDriftFindings(ctx, mc, surface, revision, obs.Scope, reports)
		if rerr != nil {
			// The computation stands; persistence failed. Report the drift AND the
			// persistence failure — never swallow either.
			for _, rep := range reports {
				out = append(out, driftToDTO(rep))
			}
			notes = append(notes, "drift findings for scope "+obs.Scope+" could not be persisted (they are reported here only)")
			continue
		}
		out = append(out, dtos...)
		c.publishFindings(ctx, mc.Tenant, fresh)
		// Stamp the scope with THIS computation's result (only after it ran).
		if serr := c.stampObservedDrift(ctx, mc, surface, obs.Scope, len(reports)); serr != nil && c.log != nil {
			c.log.Warn("claude-policy: could not stamp drift counters", "scope", obs.Scope, "err", serr)
		}
		if len(reports) == 0 {
			clean++
		}
	}
	notes = append(notes, "drift computed against "+strconv.Itoa(diffed)+" observed scope(s)")
	if diffed > 0 && clean == diffed && len(out) == 0 {
		notes = append(notes, "PERMITTED policy matches the OBSERVED host config on every reporting scope (no drift)")
	}
	return out, diffed > 0, notes
}

// --- the pull / check-in / truth-view routes ------------------------------------

// artifactMeta is the non-content summary of a signed distribution record.
type artifactMeta struct {
	Revision       int64  `json:"revision"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	KeyFingerprint string `json:"key_fingerprint"`
	SignedAt       string `json:"signed_at,omitempty"`
}

// artifactResponse is the signed artifact an agent pulls. It is self-contained
// for verification: the rendered bytes, the detached signature, the public key
// and the preimage recipe. The agent should PIN key_fingerprint out-of-band (the
// publish response shows it to the operator).
type artifactResponse struct {
	Surface        string   `json:"surface"`
	Revision       int64    `json:"revision"`
	Content        string   `json:"content"`
	ArtifactSHA256 string   `json:"artifact_sha256"`
	Signature      string   `json:"signature"`
	PublicKey      string   `json:"pubkey"`
	KeyFingerprint string   `json:"key_fingerprint"`
	SignedAt       string   `json:"signed_at,omitempty"`
	Algorithm      string   `json:"algorithm"`
	Preimage       string   `json:"preimage"`
	Notes          []string `json:"notes,omitempty"`
}

// handleArtifact serves the latest (or ?revision=N) SIGNED artifact for a surface —
// the agent-pull half of the decided distribution mechanism. Read tier (the puller
// is an authenticated machine identity). DENY-CLOSED: a revision without a signed
// distribution record is 404 — there is never an unsigned artifact to apply.
func (c *PolicyConsole) handleArtifact(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	surface, ok := surfaceOf(w, r)
	if !ok {
		return
	}
	var wantRev int64
	if raw := r.URL.Query().Get("revision"); raw != "" {
		n, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid revision"))
			return
		}
		wantRev = n
	}
	var (
		rec        model.Record
		found      bool
		latestRev  int64 // newest PUBLISHED revision
		maxDistRev int64 // newest SIGNED artifact revision
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, e := sc.Ext(distributionKind)
		if e != nil {
			return e
		}
		recs, e := listAll(r.Context(), repo, eq(colDistSurface, surface))
		if e != nil {
			return e
		}
		for _, cand := range recs {
			n := cand.Int(colDistRevision)
			if n > maxDistRev {
				maxDistRev = n
			}
			switch {
			case wantRev > 0 && n == wantRev:
				rec, found = cand, true
			case wantRev == 0 && (!found || n > rec.Int(colDistRevision)):
				rec, found = cand, true
			}
		}
		// The newest PUBLISHED revision, to say honestly when it outruns the
		// newest SIGNED artifact (an enqueue failure left it undistributed).
		revs, e := listRevisions(r.Context(), sc, surface)
		if e != nil {
			return e
		}
		if len(revs) > 0 {
			latestRev = revs[0].Revision
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		msg := "no signed artifact for this surface (nothing published, or distribution is unwired) — deny-closed: nothing to pull"
		if wantRev > 0 {
			msg = "revision " + strconv.FormatInt(wantRev, 10) + " has no signed distribution artifact — deny-closed: nothing to pull"
		}
		writeJSON(w, http.StatusNotFound, errorBody(msg))
		return
	}
	resp := artifactResponse{
		Surface:        surface,
		Revision:       rec.Int(colDistRevision),
		Content:        rec.String(colDistRendered),
		ArtifactSHA256: rec.String(colDistSHA),
		Signature:      rec.String(colDistSig),
		PublicKey:      rec.String(colDistPubKey),
		KeyFingerprint: rec.String(colDistKeyFP),
		SignedAt:       rec.String(colDistSignedAt),
		Algorithm:      "ed25519",
		Preimage:       artifactPreimagePrefix + "|" + mc.Tenant.String() + "|" + surface + "|<revision>|<artifact_sha256>, hashed with SHA-256; the signature is detached over that hash",
		Notes:          []string{"verify the signature and PIN key_fingerprint out-of-band before applying; report back via POST /" + surface + "/checkin"},
	}
	// Both notes are computed against the NEWEST signed artifact — an explicit
	// pull of an old revision must never be told the newer one failed to sign.
	if latestRev > maxDistRev {
		resp.Notes = append(resp.Notes, "revision "+strconv.FormatInt(latestRev, 10)+" is published but has NO signed artifact (distribution enqueue failed) — revision "+strconv.FormatInt(maxDistRev, 10)+" is the newest distributable revision")
	}
	if resp.Revision < maxDistRev {
		resp.Notes = append(resp.Notes, "a newer signed artifact exists (revision "+strconv.FormatInt(maxDistRev, 10)+") — this response serves the explicitly requested older revision")
	}
	writeJSON(w, http.StatusOK, resp)
}

// checkinInput is the distribution agent's attested report: which artifact it
// verified+applied (revision + hash echo + key fingerprint) and the managed
// config it OBSERVES on the host after applying.
type checkinInput struct {
	Scope           string `json:"scope"`
	Revision        int64  `json:"revision,omitempty"`
	ArtifactSHA256  string `json:"artifact_sha256,omitempty"`
	KeyFingerprint  string `json:"key_fingerprint,omitempty"`
	ObservedContent string `json:"observed_content,omitempty"`
}

// checkinResult reports the attestation verdict and the drift computed against
// the latest published revision.
type checkinResult struct {
	Surface        string           `json:"surface"`
	Scope          string           `json:"scope"`
	Verified       bool             `json:"verified"`
	LatestRevision int64            `json:"latest_revision,omitempty"`
	Drift          []policyDriftDTO `json:"drift"`
	Notes          []string         `json:"notes,omitempty"`
}

// handleCheckin records a distribution agent's attested check-in: it verifies the
// echoed artifact hash/fingerprint against the distribution record (a mismatch is
// a HIGH drift finding — tampered or stale artifact), upserts the scope's OBSERVED
// config (credential-redacted), computes drift against the latest published
// revision, and audits the whole thing. Write tier — an editor-scoped machine
// token can check in without holding publish rights. TRUST MODEL: the scope is
// the agent's self-asserted host identity; any claude-policy:write principal may
// report any scope, so every check-in records (and the truth view shows) WHO
// reported it — forgery is attributable, never silent. DENY-CLOSED: a
// validation or persist error records nothing and returns an explicit failure.
func (c *PolicyConsole) handleCheckin(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	surface, ok := surfaceOf(w, r)
	if !ok {
		return
	}
	var in checkinInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Scope = strings.TrimSpace(in.Scope)
	if in.Scope == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("scope is required (the host id / distribution name this check-in reports for)"))
		return
	}
	if len(in.Scope) > maxScopeLen || strings.ContainsFunc(in.Scope, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid scope"))
		return
	}
	if in.Revision < 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid revision"))
		return
	}
	if len(in.ObservedContent) > maxPolicyContentBytes {
		writeJSON(w, http.StatusBadRequest, errorBody("observed_content too large"))
		return
	}
	observed, hadCred := redactInlineCredentials(in.ObservedContent)
	now := c.clock.Now()

	var (
		verified     bool
		attestNotes  []string
		latestRev    int64
		latestRevDoc string
		extraReports []sdkmodel.FindingReport
	)
	checkin := func(sc store.Scope) error {
		// Re-entrant under the conflict retry below: reset per-attempt state.
		verified, attestNotes, latestRev, latestRevDoc, extraReports = false, nil, 0, "", nil
		// 1. Attestation: the echoed artifact must match the distribution record.
		switch {
		case in.Revision == 0:
			attestNotes = append(attestNotes, "check-in carries no artifact attestation (revision not reported) — recorded unverified")
		case in.ArtifactSHA256 == "":
			attestNotes = append(attestNotes, "check-in did not echo the artifact hash — recorded unverified")
		default:
			repo, e := sc.Ext(distributionKind)
			if e != nil {
				return e
			}
			rec, found, e := findOne(r.Context(), repo, eq(colDistSurface, surface), eq(colDistRevision, in.Revision))
			if e != nil {
				return e
			}
			switch {
			case !found:
				attestNotes = append(attestNotes, "no distribution record exists for the attested revision — recorded unverified")
				extraReports = append(extraReports, attestationMismatchReport(in.Scope, surface,
					"check-in attests revision "+strconv.FormatInt(in.Revision, 10)+" which has no signed distribution record", now))
			case rec.String(colDistSHA) != in.ArtifactSHA256:
				attestNotes = append(attestNotes, "the attested artifact hash does NOT match the signed distribution record — recorded unverified")
				extraReports = append(extraReports, attestationMismatchReport(in.Scope, surface,
					"check-in attests an artifact hash that does not match the signed distribution record for revision "+strconv.FormatInt(in.Revision, 10)+" (tampered or stale artifact)", now))
			case in.KeyFingerprint != "" && rec.String(colDistKeyFP) != in.KeyFingerprint:
				attestNotes = append(attestNotes, "the attested signer fingerprint does NOT match the distribution record — recorded unverified")
				extraReports = append(extraReports, attestationMismatchReport(in.Scope, surface,
					"check-in attests a signer key fingerprint that does not match the distribution record for revision "+strconv.FormatInt(in.Revision, 10), now))
			default:
				verified = true
			}
		}
		if hadCred {
			extraReports = append(extraReports, sdkmodel.FindingReport{
				Kind: policyDriftKind, Severity: sdkmodel.SeverityHigh,
				SubjectKind: policySubjectKind, SubjectRef: in.Scope,
				Title:      "observed " + surface + " carries an inline credential (redacted before storage)",
				DetailHash: hashHexOf(in.Scope + "|" + surface + "|inline-credential"),
				OccurredAt: now.Time(),
			})
		}

		// 2. Upsert the scope's observed row (latest state; history = audit trail).
		// An attestation-only check-in (no observed_content) NEVER overwrites a
		// previously reported observation: wiping it would later read as "file
		// absent on host" — a fabricated HIGH "host is ungoverned" finding for a
		// host that merely sent an attestation ping.
		repo, e := sc.Ext(observedKind)
		if e != nil {
			return e
		}
		rec, found, e := findOne(r.Context(), repo, eq(colObsSurface, surface), eq(colObsScope, in.Scope))
		if e != nil {
			return e
		}
		sum := sha256.Sum256([]byte(observed))
		fields := model.Record{
			colObsSurface:     surface,
			colObsScope:       in.Scope,
			colObsReportedRev: in.Revision,
			colObsReportedSHA: in.ArtifactSHA256,
			colObsVerified:    verified,
			colObsReporter:    mc.Principal.Actor(),
			colObsCheckedInAt: now.String(),
		}
		if observed != "" {
			fields[colObsContent] = observed
			fields[colObsContentSHA] = hex.EncodeToString(sum[:])
		}
		if found {
			for k, v := range fields {
				rec[k] = v
			}
			if _, e = repo.Update(r.Context(), rec); e != nil {
				return e
			}
		} else {
			// First sight of this scope: an explicit no-content row (empty sha —
			// distinguishable from sha256("")) with untouched drift counters.
			if observed == "" {
				fields[colObsContent] = ""
				fields[colObsContentSHA] = ""
			}
			fields[colObsDriftCount] = int64(0)
			created, ce := repo.Create(r.Context(), fields)
			if ce != nil {
				return ce
			}
			rec = created
		}

		// 3. The latest published revision (the PERMITTED side of the drift below).
		revs, e := listRevisions(r.Context(), sc, surface)
		if e != nil {
			return e
		}
		if len(revs) > 0 {
			latestRev = revs[0].Revision
			dto, okRev, ge := getRevision(r.Context(), sc, surface, latestRev)
			if ge != nil {
				return ge
			}
			if okRev {
				latestRevDoc = dto.Content
			}
		}

		// 4. Self-audit the check-in in the SAME transaction (never the content —
		// the hash is the minimal-data reference; docs/SECURITY-HARDENING.md,§4).
		return auditEvent(r.Context(), sc, mc, "governance.claude_policy.checkin", observedKind, model.ID(rec.String(model.ColID)), map[string]any{
			"surface": surface, "scope": in.Scope, "revision": in.Revision,
			"verified": verified, "content_sha256": hex.EncodeToString(sum[:]),
			"inline_credential_redacted": hadCred,
		})
	}
	// Two first check-ins for one (surface, scope) can race the unique index —
	// the loser retries (the publish handler's stance on its revision counter).
	var err error
	for attempt := 0; attempt < maxDecisionRetries; attempt++ {
		if err = mc.Data.Mutate(r.Context(), checkin); err == nil || !isConflict(err) {
			break
		}
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	res := checkinResult{Surface: surface, Scope: in.Scope, Verified: verified, LatestRevision: latestRev, Drift: []policyDriftDTO{}, Notes: attestNotes}

	// PERMITTED-vs-OBSERVED content drift against the latest published revision —
	// managed-settings only (the verified connector logic); other surfaces say so.
	// Content drift and the attestation/credential findings are persisted in ONE
	// pass; the scope's drift counters are stamped ONLY when the content diff
	// actually ran (a counter stamped off absence would fabricate compliance).
	reports := extraReports
	contentComputed := false
	var contentFindings int
	switch {
	case observed == "":
		res.Notes = append(res.Notes, "no observed_content reported — content drift not computed for this check-in")
	case latestRevDoc == "":
		res.Notes = append(res.Notes, "no published revision exists for this surface — observed config recorded, drift not computed")
	case surface != surfaceManagedSettings:
		res.Notes = append(res.Notes, "observed config recorded; the publish-time content diff is managed-settings-only today ("+surface+" drift reflects the connector findings stream)")
	default:
		content, derr := managedsettings.VerifyDriftJSON(in.Scope, []byte(latestRevDoc), []byte(observed), now.Time())
		if derr != nil {
			res.Notes = append(res.Notes, "drift not computed: "+derr.Error())
			break
		}
		reports = append(reports, content...)
		contentComputed, contentFindings = true, len(content)
	}
	dtos, fresh, rerr := c.recordDriftFindings(r.Context(), mc, surface, latestRev, in.Scope, reports)
	if rerr != nil {
		// The computation stands; persistence failed. Report BOTH — never swallow.
		for _, rep := range reports {
			res.Drift = append(res.Drift, driftToDTO(rep))
		}
		res.Notes = append(res.Notes, "drift findings could not be persisted (they are reported here only)")
	} else {
		res.Drift = append(res.Drift, dtos...)
		c.publishFindings(r.Context(), mc.Tenant, fresh)
	}
	if contentComputed {
		if serr := c.stampObservedDrift(r.Context(), mc, surface, in.Scope, contentFindings); serr != nil && c.log != nil {
			c.log.Warn("claude-policy: could not stamp drift counters", "scope", in.Scope, "err", serr)
		}
	}
	// "no drift" is claimed only when this check-in computed the diff AND nothing
	// at all was found — never alongside attestation/credential findings.
	if contentComputed && contentFindings == 0 && len(res.Drift) == 0 {
		res.Notes = append(res.Notes, "OBSERVED config matches the published revision "+strconv.FormatInt(latestRev, 10)+" (no drift on this scope)")
	}
	writeJSON(w, http.StatusOK, res)
}

// attestationMismatchReport is the HIGH drift finding a failed pull attestation
// raises: the host claims an artifact the plane never signed for that revision.
func attestationMismatchReport(scope, surface, title string, now model.Timestamp) sdkmodel.FindingReport {
	return sdkmodel.FindingReport{
		Kind: policyDriftKind, Severity: sdkmodel.SeverityHigh,
		SubjectKind: policySubjectKind, SubjectRef: scope,
		Title:      title,
		DetailHash: hashHexOf(scope + "|" + surface + "|" + title),
		OccurredAt: now.Time(),
	}
}

// hashHexOf is the module-local detail hash (the module cannot import
// connectors/internal/redact — it hashes directly, like toolFingerprint).
func hashHexOf(detail string) string {
	sum := sha256.Sum256([]byte(detail))
	return hex.EncodeToString(sum[:])
}

// scopeStatus is one scope's truth-loop state in the distribution view.
type scopeStatus struct {
	Scope       string `json:"scope"`
	CheckedInAt string `json:"checked_in_at,omitempty"`
	// Reporter is the audit actor of the last check-in: the scope is the agent's
	// SELF-ASSERTED host identity, so the view always shows who asserted it.
	Reporter         string `json:"reporter,omitempty"`
	ReportedRevision int64  `json:"reported_revision,omitempty"`
	Verified         bool   `json:"verified"`
	Current          bool   `json:"current"` // verified AND on the newest signed artifact
	// ContentReported distinguishes a scope that reported its observed config
	// from one that only ever attested — drift facts exist only for the former.
	ContentReported bool   `json:"content_reported"`
	ObservedSHA256  string `json:"observed_sha256,omitempty"`
	// DriftCount/LastDriftAt = the LAST content-drift computation for this scope
	// (stamped only when the diff actually ran); OpenFindings = the LIVE count of
	// open policy_drift findings attributed to this scope (content drift AND
	// attestation/credential facts) — the number an operator must trust.
	DriftCount   int64  `json:"drift_count"`
	LastDriftAt  string `json:"last_drift_at,omitempty"`
	OpenFindings int64  `json:"open_findings"`
}

// distributionView is the truth-loop summary the console renders: what was
// published, what was signed for distribution, and what every scope reports.
type distributionView struct {
	Surface        string        `json:"surface"`
	LatestRevision int64         `json:"latest_revision,omitempty"`
	Artifact       *artifactMeta `json:"artifact,omitempty"`
	Scopes         []scopeStatus `json:"scopes"`
	Notes          []string      `json:"notes,omitempty"`
}

// handleDistribution serves the per-surface truth view (read tier): published vs
// signed vs observed, scope by scope — real state only, every absence named.
func (c *PolicyConsole) handleDistribution(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	surface, ok := surfaceOf(w, r)
	if !ok {
		return
	}
	out := distributionView{Surface: surface, Scopes: []scopeStatus{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		revs, e := listRevisions(r.Context(), sc, surface)
		if e != nil {
			return e
		}
		if len(revs) > 0 {
			out.LatestRevision = revs[0].Revision
		}
		distRepo, e := sc.Ext(distributionKind)
		if e != nil {
			return e
		}
		dists, e := listAll(r.Context(), distRepo, eq(colDistSurface, surface))
		if e != nil {
			return e
		}
		var newest model.Record
		for _, d := range dists {
			if newest == nil || d.Int(colDistRevision) > newest.Int(colDistRevision) {
				newest = d
			}
		}
		if newest != nil {
			out.Artifact = &artifactMeta{
				Revision:       newest.Int(colDistRevision),
				ArtifactSHA256: newest.String(colDistSHA),
				KeyFingerprint: newest.String(colDistKeyFP),
				SignedAt:       newest.String(colDistSignedAt),
			}
		}
		obsRepo, e := sc.Ext(observedKind)
		if e != nil {
			return e
		}
		obs, e := listAll(r.Context(), obsRepo, eq(colObsSurface, surface))
		if e != nil {
			return e
		}
		// The LIVE per-scope open-finding counts — the stored counter only covers
		// the last computation; standing findings are the trustworthy number.
		open, e := drainOpenPolicyDrift(r.Context(), sc)
		if e != nil {
			return e
		}
		openByScope := map[string]int64{}
		for _, f := range open {
			if ref, ok := f.Metadata["subject_ref"].(string); ok {
				openByScope[ref]++
			}
		}
		for _, rec := range obs {
			st := scopeStatus{
				Scope:            rec.String(colObsScope),
				CheckedInAt:      rec.String(colObsCheckedInAt),
				Reporter:         rec.String(colObsReporter),
				ReportedRevision: rec.Int(colObsReportedRev),
				Verified:         rec.Bool(colObsVerified),
				ContentReported:  rec.String(colObsContentSHA) != "",
				ObservedSHA256:   rec.String(colObsContentSHA),
				DriftCount:       rec.Int(colObsDriftCount),
				LastDriftAt:      rec.String(colObsDriftAt),
				OpenFindings:     openByScope[rec.String(colObsScope)],
			}
			st.Current = st.Verified && out.Artifact != nil && st.ReportedRevision == out.Artifact.Revision
			out.Scopes = append(out.Scopes, st)
			if !st.ContentReported {
				out.Notes = append(out.Notes, "scope "+st.Scope+" has never reported its observed config — content drift for it is UNKNOWN (attestation only)")
			}
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if out.LatestRevision == 0 {
		out.Notes = append(out.Notes, "nothing published for this surface yet")
	}
	if out.Artifact == nil {
		out.Notes = append(out.Notes, "no signed distribution artifact exists (distribution seam unwired, or every enqueue failed) — agents have nothing to pull")
	} else if out.Artifact.Revision < out.LatestRevision {
		out.Notes = append(out.Notes, "the newest published revision has NO signed artifact (enqueue failed) — agents still pull revision "+strconv.FormatInt(out.Artifact.Revision, 10))
	}
	if len(out.Scopes) == 0 {
		out.Notes = append(out.Notes, "no agent has checked in for this surface — distribution state on hosts is UNKNOWN (honest unknown, not compliant)")
	}
	writeJSON(w, http.StatusOK, out)
}
