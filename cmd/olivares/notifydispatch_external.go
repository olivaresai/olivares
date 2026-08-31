// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/sdk"
)

// notifydispatch_external.go is the EXTERNAL (third-party) output-plugin side
// of the notify composition — the twin of the external source/content wiring in
// sources.go/secretwire.go. It finishes the output half of the advertised plugin
// model: an operator can declare a third-party output binary (notifyDestinationSpec
// .Plugin) and it runs ONLY after the SAME deny-closed admission gate the external
// source/content plugins use (admitExternalPlugin: operator-pinned digest + verified
// Sigstore/DSSE attestation against the single ConnectorTrust root), dispensed
// checksum-pinned (runtime.DispenseOutputPluginVerified) and confined by the
// plugjail that every dispense already applies. There is NO second, divergent trust
// path, NO observe mode and NO allow-unsigned escape hatch (the S142 posture).
//
// Honest scope: an external output is a NOTIFY DESTINATION (delivered by name via the
// existing notify routes), not a bus output subscribing to event types — it extends
// the exact extension point E5 wired for the first-party output plugins
// (outputPluginForKind), never the unused Runtime.LoadOutputPlugin bus path. The
// isolation claim is "signed trusted-operator output plugin confinement", never a
// "safe marketplace": the plugjail attestation records the real level per launch.

// killClient kills a go-plugin client, tolerating the nil client the test seam
// injects when it dispenses a stub connector without launching a subprocess.
func killClient(c *goplugin.Client) {
	if c != nil {
		c.Kill()
	}
}

// prepareExternal runs the full deny-closed admission + verified dispense + Open for
// one external output destination WITHOUT publishing it into conns. It is the shared
// core of the initial open (openExternal) and the live reload (reloadExternal): the
// reload prepares the NEW connector entirely with this before it touches the live
// map, so a failed reload leaves the old sink untouched (atomic). On success the
// caller owns the returned connector/client (publish + Track, or teardown). refusal
// is the single non-empty reason for the WARN; it never leaks a secret or an Open
// error string (both can embed the configured endpoint/credential).
func (d *connectorDispatcher) prepareExternal(ctx context.Context, spec notifyDestinationSpec, r *secret.Resolver) (ed *extDest, digest string, refusal string) {
	if d.rt == nil || d.dispenseVerified == nil {
		return nil, "", "external output destination has no runtime loader; skipped"
	}
	// 1) Admission is PURE and deny-closed (externalplugins.go): operator-pinned
	// digest + verified attestation against the single ConnectorTrust root, or the
	// destination is NOT wired. The refusal string is the source of truth for the WARN.
	// The trust root is read under the lock (a SIGHUP reconcile may rotate it).
	digest, refusal = admitExternalPlugin(*spec.Plugin, d.trust())
	if refusal != "" {
		return nil, "", refusal
	}
	// 2) Resolve secret references with no descriptor (the plugin self-describes
	// out-of-process, so the strict no-inline-secret check cannot run) — references
	// still resolve to live values. Never log the error: it can embed the value.
	resolved, rerr := resolveConfig(ctx, r, sdk.Descriptor{}, sdk.Config{Settings: spec.Config})
	if rerr != nil {
		return nil, "", "secret reference could not be resolved"
	}
	// 3) Dispense checksum-pinned + confined (go-plugin re-hashes the binary at exec
	// and refuses on a mismatch; plugjail scopes env/uid/cgroup). The seam defaults to
	// rt.DispenseOutputPluginVerified; tests inject a subprocess-free stub.
	conn, client, err := d.dispenseVerified(spec.Plugin.Path, digest)
	if err != nil {
		return nil, "", "failed to launch external output connector plugin (digest pin or handshake failed)"
	}
	// 4) Open in its subprocess. Never log the Open error (endpoint/credential). Kill
	// the just-launched subprocess AND release its confinement (cgroup subtree + dir)
	// so a failed Open never leaks a live process, a forked child, or a cgroup dir.
	if oerr := conn.Open(ctx, resolved); oerr != nil {
		killClient(client)
		d.rt.RunPluginCleanup(client)
		return nil, "", "failed to open (configuration error)"
	}
	return &extDest{conn: conn, client: client, spec: spec, fingerprint: fingerprintExternal(spec),
		resolvedFP: connectorResolvedDigest(spec.Kind, resolved, spec.Plugin)}, digest, ""
}

// openExternal admits, dispenses and Opens one external output destination, then
// publishes it (initial boot path, and the SIGHUP add path). It returns true when
// the destination became live. The connector is tracked for Stop teardown; the
// atomic publish makes it deliverable only after a successful Open (readiness).
func (d *connectorDispatcher) openExternal(ctx context.Context, spec notifyDestinationSpec, r *secret.Resolver, log *slog.Logger) bool {
	ed, digest, refusal := d.prepareExternal(ctx, spec, r)
	if refusal != "" {
		log.Warn("notify: external output destination refused (deny-closed) or unopenable; NOT wired", "name", spec.Name, "reason", refusal)
		return false
	}
	d.rt.TrackOutputPlugin(ed.conn, ed.client)
	d.mu.Lock()
	d.conns[spec.Name] = ed.conn
	d.ext[spec.Name] = ed
	d.resolvedFP[spec.Name] = ed.resolvedFP
	// The tenant scope is part of the destination, so it is republished with it. It
	// used to be built ONCE at construction and never touched here, which made every
	// SIGHUP path fail OPEN: a destination added by reload carried no scope entry, and
	// an absent entry means "addressable by every tenant".
	d.setTenantScope(spec, log)
	d.mu.Unlock()
	log.Info("notify: wired EXTERNAL destination (signature verified, checksum-pinned, out-of-process AutoMTLS + plugjail confinement)", "name", spec.Name, "digest", digest)
	return true
}

// reloadExternal atomically swaps a live external destination for a freshly admitted
// build of the SAME name. It prepares the new connector completely first; ONLY on
// full success does it track the new one, swap the map under the write lock, and tear
// the old subprocess down. A failed preparation returns an error with the old
// connector still live and delivering (atomic — never a half-open sink). The window
// between the swap and the old Kill is covered by the durable outbox's at-least-once
// retry: an attempt in flight on the old connector at the instant of the swap
// either completes or is retried on the new one — never lost.
func (d *connectorDispatcher) reloadExternal(ctx context.Context, spec notifyDestinationSpec, r *secret.Resolver, log *slog.Logger) error {
	ed, digest, refusal := d.prepareExternal(ctx, spec, r)
	if refusal != "" {
		// The replacement could not be installed, and the OLD connector stays live and
		// delivering — that part is deliberate and atomic. What must NOT stay is the
		// old authorization: the operator's edit may have been a revocation, and
		// leaving a revoked tenant able to send because an unrelated plugin digest
		// failed to admit couples a security change to a supply-chain outcome.
		//
		// The safe publication is the INTERSECTION of what is authorized now and what
		// the operator asked for. It can only narrow: a tenant loses access
		// immediately, and no tenant gains it until the new connector is actually
		// live. For an unscoped destination being scoped, the intersection is the new
		// list; for a move from A to B it is deny-all, which is the honest reading of
		// "neither the old authorization nor the new one is currently installable".
		d.mu.Lock()
		d.narrowTenantScope(spec, log)
		d.mu.Unlock()
		return errors.New(refusal)
	}
	d.rt.TrackOutputPlugin(ed.conn, ed.client)
	d.mu.Lock()
	old := d.ext[spec.Name]
	d.conns[spec.Name] = ed.conn
	d.ext[spec.Name] = ed
	d.resolvedFP[spec.Name] = ed.resolvedFP
	d.setTenantScope(spec, log)
	d.mu.Unlock()
	if old != nil {
		d.teardownExtDest(ctx, old)
	}
	log.Info("notify: reloaded EXTERNAL destination atomically (new build admitted + opened before swap; old torn down)", "name", spec.Name, "digest", digest)
	return nil
}

// removeExternal drops a live external destination (the SIGHUP delete path) and tears
// its subprocess down. In-flight delivery is covered as in reloadExternal.
func (d *connectorDispatcher) removeExternal(ctx context.Context, name string, log *slog.Logger) {
	d.mu.Lock()
	old := d.ext[name]
	delete(d.ext, name)
	delete(d.conns, name)
	delete(d.resolvedFP, name)
	// Drop the scope with the destination. A stale entry left behind would decide the
	// fate of a LATER destination that happens to reuse the name — and it would decide
	// it with the removed operator's intent, not the new one's.
	delete(d.tenantScope, name)
	d.mu.Unlock()
	if old != nil {
		d.teardownExtDest(ctx, old)
	}
	log.Info("notify: removed EXTERNAL destination (no longer in the operator config)", "name", name)
}

// teardownExtDest untracks the old destination from the runtime's Stop teardown (so
// it is not Closed/Killed a second time), Closes the connector gracefully, Kills the
// subprocess, then RELEASES its confinement (cgroup.kill over the whole subtree +
// RemoveAll of the cgroup dir) — the same order Stop uses (Close, Kill, then reclaim),
// so a live reload/remove never orphans a forked child or leaks a cgroup dir.
func (d *connectorDispatcher) teardownExtDest(ctx context.Context, ed *extDest) {
	if d.rt != nil {
		d.rt.UntrackOutputPlugin(ed.conn, ed.client)
	}
	if ed.conn != nil {
		_ = ed.conn.Close(ctx)
	}
	killClient(ed.client)
	if d.rt != nil {
		d.rt.RunPluginCleanup(ed.client)
	}
}

// trust returns the current single external-connector trust root (read-locked, since
// a SIGHUP reconcile may swap it for a freshly re-read one).
func (d *connectorDispatcher) trust() *connectorTrustSpec {
	d.mu.RLock()
	t := d.connectorTrust
	d.mu.RUnlock()
	return t
}

// liveExternalNames returns the names of the live external destinations (read-locked).
func (d *connectorDispatcher) liveExternalNames() []string {
	d.mu.RLock()
	out := make([]string, 0, len(d.ext))
	for name := range d.ext {
		out = append(out, name)
	}
	d.mu.RUnlock()
	sort.Strings(out)
	return out
}

// currentFingerprint returns the fingerprint of a live external destination.
func (d *connectorDispatcher) currentFingerprint(name string) (string, bool) {
	d.mu.RLock()
	ed, ok := d.ext[name]
	d.mu.RUnlock()
	if !ok {
		return "", false
	}
	return ed.fingerprint, true
}

// currentSpec returns the admitted spec of a live external destination (for
// re-admission against a rotated trust root).
func (d *connectorDispatcher) currentSpec(name string) (notifyDestinationSpec, bool) {
	d.mu.RLock()
	ed, ok := d.ext[name]
	d.mu.RUnlock()
	if !ok {
		return notifyDestinationSpec{}, false
	}
	return ed.spec, true
}

// externalReloadReport is the count summary of a SIGHUP reconcile pass (logged, never
// per-destination content). Revoked counts live destinations torn down deny-closed
// because they no longer admit under a rotated trust root.
type externalReloadReport struct {
	Added, Reloaded, Removed, Revoked, Refused, Unchanged int
}

// reconcileExternal reconciles ONLY the EXTERNAL output destinations against a freshly
// re-read operator config on SIGHUP: it REVOKES live destinations that no longer admit
// under a rotated trust root, opens new external destinations, atomically reloads those
// whose binary/digest/config changed, and removes those the operator deleted. In-process
// and first-party destinations are deliberately out of scope — they have no subprocess
// to hot-swap and (like the non-roster half of the source reconciler) a change to them
// applies on restart. trust is the freshly re-read single ConnectorTrust root.
func (d *connectorDispatcher) reconcileExternal(ctx context.Context, specs []notifyDestinationSpec, trust *connectorTrustSpec, r *secret.Resolver, log *slog.Logger) externalReloadReport {
	var rep externalReloadReport
	if d.dispenseVerified == nil && d.rt != nil {
		d.dispenseVerified = d.rt.DispenseOutputPluginVerified
	}
	// Adopt the freshly re-read trust root and note whether it changed since the last
	// pass (an operator may have rotated or REMOVED an anchor).
	newFP := hashTrust(trust)
	d.mu.Lock()
	trustChanged := newFP != d.trustFP
	d.connectorTrust = trust
	d.trustFP = newFP
	d.mu.Unlock()

	// REVOCATION PASS (only when the trust root changed): re-admit every live external
	// destination's ADMITTED spec against the new root; tear down deny-closed any that
	// no longer verify — so removing a compromised trusted_key and sending SIGHUP kills
	// the already-running binary it signed, not only future launches.
	if trustChanged {
		for _, name := range d.liveExternalNames() {
			spec, ok := d.currentSpec(name)
			if !ok || spec.Plugin == nil {
				continue
			}
			if _, refusal := admitExternalPlugin(*spec.Plugin, trust); refusal != "" {
				log.Warn("notify: SIGHUP external destination REVOKED (no longer admits under the rotated trust root); torn down deny-closed", "name", name, "reason", refusal)
				d.removeExternal(ctx, name, log)
				rep.Revoked++
			}
		}
	}

	desired := make(map[string]notifyDestinationSpec, len(specs))
	for _, s := range specs {
		if s.Plugin == nil {
			continue // only external destinations are hot-reconciled
		}
		if _, dup := desired[s.Name]; dup {
			log.Warn("notify: SIGHUP: duplicate external destination name; later definition ignored", "name", s.Name)
			continue
		}
		desired[s.Name] = s
	}

	// Removals: a live external destination no longer in the operator config.
	for _, name := range d.liveExternalNames() {
		if _, want := desired[name]; !want {
			d.removeExternal(ctx, name, log)
			rep.Removed++
		}
	}

	// Adds and reloads (sorted for a deterministic, testable order).
	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := desired[name]
		fp, live := d.currentFingerprint(name)
		if !live {
			// A name already used by a NON-external destination (in-process /
			// first-party) must not be shadowed by a live external reload.
			if d.has(name) {
				log.Warn("notify: SIGHUP: external destination name already used by a non-external destination; NOT wired", "name", name)
				rep.Refused++
				continue
			}
			if d.openExternal(ctx, s, r, log) {
				rep.Added++
			} else {
				rep.Refused++
			}
			continue
		}
		if fp == fingerprintExternal(s) {
			// The CONNECTOR is unchanged, and the authorization may not be. A previous
			// reload that failed published the intersection of the old and desired
			// scopes — correctly, since it must never grant on failure — and if the
			// operator then reverts the file, the connector's fingerprint matches again
			// and nothing republishes the scope. The narrowed set would stick, denying
			// a tenant the operator has since restored.
			//
			// Republishing here is safe precisely because the fingerprint matched: the
			// spec in hand IS the live connector's spec, so this is the operator's
			// current intent and not a stale one.
			d.mu.Lock()
			d.setTenantScope(s, log)
			d.mu.Unlock()
			rep.Unchanged++
			continue
		}
		if err := d.reloadExternal(ctx, s, r, log); err != nil {
			// Deny-closed: the previous connector stays live (atomic). WARN the reason.
			log.Warn("notify: SIGHUP external reload refused; keeping the previously admitted connector live", "name", name, "reason", err.Error())
			rep.Refused++
			continue
		}
		rep.Reloaded++
	}
	return rep
}

// fingerprintExternal is the change key for an external destination: a stable hash of
// the admission inputs (path, digest pin, attestation bundle, narrowed predicates)
// and the resolved-reference config. Config carries secret REFERENCES (store:<name>),
// never values, so hashing it leaks nothing. A change in any of these triggers an
// atomic reload; an identical spec is left untouched (no needless subprocess churn).
func fingerprintExternal(spec notifyDestinationSpec) string {
	h := sha256.New()
	if spec.Plugin != nil {
		fmt.Fprintf(h, "path=%s\x00sha256=%s\x00bundle=%s\x00", spec.Plugin.Path, spec.Plugin.SHA256, spec.Plugin.Bundle)
		preds := append([]string(nil), spec.Plugin.PredicateTypes...)
		sort.Strings(preds)
		for _, p := range preds {
			fmt.Fprintf(h, "pred=%s\x00", p)
		}
	}
	// The tenant scope is part of the spec's identity. Omitting it made NARROWING a
	// destination's tenants on SIGHUP a silent no-op: the fingerprint matched, the
	// reconcile reported "unchanged", and the revoked tenant kept delivering.
	tenants := append([]string(nil), spec.Tenants...)
	sort.Strings(tenants)
	fmt.Fprintf(h, "tenants_declared=%t\x00", spec.Tenants != nil)
	for _, t := range tenants {
		fmt.Fprintf(h, "tenant=%s\x00", t)
	}
	keys := make([]string, 0, len(spec.Config))
	for k := range spec.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "cfg=%s=%s\x00", k, spec.Config[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashTrust is a stable hash of the effective ConnectorTrust root (all anchoring
// inputs: trusted roots/keys, keyless identities/issuers, predicate allow-list). A nil
// root hashes to a fixed sentinel distinct from every non-nil root, so going from
// unconfigured to configured (or back) is detected as a change. reconcileExternal
// compares it across SIGHUPs to decide whether a revocation re-admission pass is owed.
func hashTrust(trust *connectorTrustSpec) string {
	h := sha256.New()
	if trust == nil {
		return "nil"
	}
	writeSorted := func(label string, vs []string) {
		cp := append([]string(nil), vs...)
		sort.Strings(cp)
		for _, v := range cp {
			fmt.Fprintf(h, "%s=%s\x00", label, v)
		}
	}
	writeSorted("root", trust.TrustedRoots)
	writeSorted("key", trust.TrustedKeys)
	writeSorted("id", trust.AllowedIdentities)
	writeSorted("iss", trust.AllowedIssuers)
	writeSorted("pred", trust.AllowedPredicates)
	return hex.EncodeToString(h.Sum(nil))
}
