// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/cmd/olivares/firstparty"
	"github.com/olivaresai/olivares/connectors/chronicle"
	"github.com/olivaresai/olivares/connectors/datadog"
	"github.com/olivaresai/olivares/connectors/elastic"
	"github.com/olivaresai/olivares/connectors/email"
	"github.com/olivaresai/olivares/connectors/filelog"
	"github.com/olivaresai/olivares/connectors/jira"
	"github.com/olivaresai/olivares/connectors/opsgenie"
	"github.com/olivaresai/olivares/connectors/otlplog"
	"github.com/olivaresai/olivares/connectors/pagerduty"
	"github.com/olivaresai/olivares/connectors/s3archive"
	"github.com/olivaresai/olivares/connectors/servicenow"
	"github.com/olivaresai/olivares/connectors/siem"
	"github.com/olivaresai/olivares/connectors/slack"
	"github.com/olivaresai/olivares/connectors/snmp"
	"github.com/olivaresai/olivares/connectors/splunkhec"
	"github.com/olivaresai/olivares/connectors/syslog"
	"github.com/olivaresai/olivares/connectors/teams"
	"github.com/olivaresai/olivares/connectors/twilio"
	"github.com/olivaresai/olivares/connectors/webhook"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/modules/notify"
	"github.com/olivaresai/olivares/sdk"
)

// This file is the XV seam adapter: it bridges the notify module's Dispatcher
// port to the real output connectors. It is the production form of "XV decides
// what/who/when, the connectors do the how" — the module never imports a
// connector; the composition root owns the glue (the deploy/orchestration
// convention). XV decides a notification goes to the destination NAME "oncall"; the
// adapter resolves that name to an opened connector and calls Notify.
//
// Secrets stay HERE, never in the module's store (docs/SECURITY-HARDENING.md): a destination's
// credential (a Slack webhook URL, a PagerDuty routing key) lives only in the
// connector configuration the operator provisions, referenced by a non-secret name.

// notifyDestinationSpec provisions one named destination: a connector kind plus its
// configuration (which carries the destination's secret, by value, from a file the
// operator controls — it is never persisted by the module).
type notifyDestinationSpec struct {
	Name   string            `json:"name"`
	Kind   string            `json:"kind"`
	Config map[string]string `json:"config"`
	// Tenants restricts which tenants may address this destination. It is a
	// TRI-STATE and the three arms are not interchangeable:
	//
	//   - ABSENT (the key omitted): every tenant may address it. This is what every
	//     deployment configured before the field existed, so it is the only
	//     upgrade-safe reading of an unwritten field.
	//   - a NON-EMPTY list: only those tenants.
	//   - an EMPTY list (`"tenants": []`): NOBODY. It is a legitimate thing for an
	//     operator to mean — "provisioned but not yet handed to anyone" — and it is
	//     honored as written rather than silently read as "everyone".
	//
	// It exists because destinations were resolved from one flat map with no tenant
	// in the lookup while routes are tenant-scoped, so a tenant's editor could name
	// any destination the operator had provisioned for anyone — and the notification
	// carried that tenant's own identity to it. An operator running one estate for
	// several customers had no way to say "this SOC belongs to that customer".
	Tenants []string `json:"tenants,omitempty"`
	// Plugin declares this destination as an EXTERNAL (third-party) output
	// binary the operator placed on the host — the output twin of sourceSpec.Plugin
	// / documentSpec.Plugin. When set, Kind is ignored and the binary runs ONLY after
	// admitExternalPlugin verifies its pinned digest + Sigstore/DSSE attestation
	// against the SINGLE operator trust root (sourcesConfig.ConnectorTrust — never a
	// second, divergent trust path). It is dispensed checksum-pinned and confined
	// exactly like an external source (runtime.DispenseOutputPluginVerified +
	// plugjail). None of its fields is secret material.
	Plugin *externalPluginSpec `json:"plugin,omitempty"`
}

// connectorDispatcher implements notify.Dispatcher over a set of opened output
// connectors keyed by destination name. It is constructed with its provisioned
// specs and OPENED later by openAll: the secret references in a
// destination's config can only resolve once the store exists, which is after the
// module is constructed. Before openAll the conns map is empty (Deliver records
// unknown_destination — but openAll runs before rt.Start, so no live delivery sees
// the unopened state).
//
// an EXTERNAL output destination can be live-reloaded on SIGHUP after Start
// (reconcileExternal), so the maps are guarded by mu — Deliver/Destinations take a
// read lock, add/reload/remove take the write lock. A reload prepares the new
// connector fully (admit → verified dispense → Open) BEFORE the atomic map swap, so
// the destination is never absent mid-reload (readiness has no gap) and the old
// sink is never left half-open; the durable outbox's at-least-once retry
// covers the single attempt that may be in flight at the instant of the swap.
type connectorDispatcher struct {
	specs []notifyDestinationSpec
	// mu guards conns/names/ext against a post-Start live reload. Deliver
	// holds it only across the map LOOKUP, never across the connector's Notify, so a
	// slow/hung destination can never block a reload of another destination.
	mu    sync.RWMutex
	conns map[string]sdk.OutputConnector
	// ext tracks the live EXTERNAL (plugin) destinations by name — their go-plugin
	// client (for reload/teardown) and admitted fingerprint (for change detection).
	// In-process and first-party kinds are NOT in ext (they are not live-reloadable).
	ext map[string]*extDest
	// resolvedFP is the D-06 connector fingerprint per LIVE destination name:
	// a digest of the RESOLVED effective config (kind + resolved secret VALUES +
	// external-plugin identity) computed at Open and updated on every SIGHUP
	// add/reload/remove — NOT the boot spec (which SIGHUP never replaces) and NOT
	// the secret REFERENCE (whose backing value can rotate under the same ref).
	// ConnectorFingerprint reads it under the same lock as Deliver; a name absent
	// here is an unknown connector and ConnectorFingerprint returns ok=false.
	resolvedFP map[string]string
	// tenantScope is the per-destination tenant restriction, built once from the
	// specs. A destination ABSENT from this map is unscoped and addressable by every
	// tenant — the pre-existing behavior, and the only reading that does not break
	// every deployment configured before the field existed. A destination PRESENT
	// with an empty set is addressable by nobody, which is a legitimate thing for an
	// operator to write and is honored as one.
	tenantScope map[string]map[model.TenantID]struct{}
	// connectorTrust is the SINGLE operator trust root for external output binaries,
	// shared with external source/content plugins (sourcesConfig.ConnectorTrust) — a
	// nil root deny-closes every external destination (there is no second trust path).
	// trustFP is a stable hash of that root: when a SIGHUP reconcile observes it change
	// (an anchor rotated/removed), every LIVE external destination is re-admitted against
	// the new root and torn down deny-closed if it no longer verifies (revocation).
	connectorTrust *connectorTrustSpec
	trustFP        string
	// dispenseVerified is the checksum-pinned, confined dispense of an external output
	// binary; it defaults in openAll to rt.DispenseOutputPluginVerified and is an
	// injectable seam so reload/atomicity tests run without launching a subprocess.
	dispenseVerified func(path, sha256Hex string) (sdk.OutputConnector, *goplugin.Client, error)
	// rt + embedDir serve the OUT-OF-PROCESS destination kinds (outputPluginForKind):
	// the embedded plugin binary is extracted to embedDir and dispensed through rt.
	// Both are late-bound by deferredSecretWiring.openAll (boot owns them); nil/empty
	// means plugin destinations are skipped honestly (in-process kinds unaffected).
	rt       *runtime.Runtime
	embedDir string
}

// extDest is one live external (plugin) output destination: the opened connector,
// its go-plugin client (nil in tests that inject a subprocess-free dispense), the
// admitted spec (kept so a rotated trust root can RE-ADMIT the running binary and
// revoke it if it no longer verifies), and the fingerprint of the admitted spec so
// reconcileExternal can tell an unchanged destination from one whose binary/digest/
// config the operator edited.
type extDest struct {
	conn        sdk.OutputConnector
	client      *goplugin.Client
	spec        notifyDestinationSpec
	fingerprint string
	// resolvedFP is the connector fingerprint of the RESOLVED effective config
	// this live external destination opened (published into connectorDispatcher.
	// resolvedFP under the write lock on every add/reload).
	resolvedFP string
}

// outputPluginForKind maps a notify-destination kind to the embedded plugin binary
// that serves it OUT-OF-PROCESS — the output twin of pluginBinaryForKind, and the
// production caller of the `task build:connectors` *-output binaries (E5:
// before this map they were compiled and embedded but unreachable). Each is a
// CloudEvents egress whose broker dependency tree (franz-go, go-amqp, SigV4) must
// never link into the engine; the subprocess boundary is the same deps/SBOM
// isolation the source plugins use (ARCHITECTURE.md).
var outputPluginForKind = map[string]string{
	"kafka":      "kafka-output",
	"amqp":       "amqp-output",
	"cloudqueue": "cloudqueue-output",
}

var _ notify.Dispatcher = (*connectorDispatcher)(nil)

// newConnectorDispatcher records the provisioned destinations; it does NOT open
// them (openAll does, once the secret resolver and store exist). With no specs the
// dispatcher is empty (the honest "transport wired, nothing to send to yet" state),
// and the module warns once at Start. trust is the SINGLE operator trust root for
// external output binaries (sourcesConfig.ConnectorTrust), shared with external
// source/content plugins — nil deny-closes every external destination.
func newConnectorDispatcher(specs []notifyDestinationSpec, trust *connectorTrustSpec, log *slog.Logger) *connectorDispatcher {
	return &connectorDispatcher{
		specs:          specs,
		conns:          make(map[string]sdk.OutputConnector, len(specs)),
		ext:            make(map[string]*extDest),
		resolvedFP:     make(map[string]string, len(specs)),
		tenantScope:    buildTenantScope(specs, log),
		connectorTrust: trust,
		trustFP:        hashTrust(trust),
	}
}

// setTenantScope republishes one destination's tenant restriction. The caller holds
// d.mu. An UNDECLARED tenants list removes any entry, which is what makes widening a
// destination on SIGHUP work as well as narrowing it.
func (d *connectorDispatcher) setTenantScope(spec notifyDestinationSpec, log *slog.Logger) {
	if d.tenantScope == nil {
		d.tenantScope = map[string]map[model.TenantID]struct{}{}
	}
	scoped := buildTenantScope([]notifyDestinationSpec{spec}, log)
	if set, ok := scoped[spec.Name]; ok {
		d.tenantScope[spec.Name] = set
		return
	}
	delete(d.tenantScope, spec.Name)
}

// narrowTenantScope publishes the INTERSECTION of the currently authorized tenants
// and the ones a desired spec names. The caller holds d.mu.
//
// It exists for the reload that could not be installed: authorization must be able to
// move in the SAFE direction without waiting for a connector replacement it does not
// depend on. Intersection is what makes that sound — the result never authorizes a
// tenant that neither the live scope nor the desired scope authorized, so a failed
// reload can revoke and can never grant.
func (d *connectorDispatcher) narrowTenantScope(spec notifyDestinationSpec, log *slog.Logger) {
	if spec.Tenants == nil {
		return // the desired state constrains nothing, so there is nothing to narrow to
	}
	desired := buildTenantScope([]notifyDestinationSpec{spec}, log)[spec.Name]
	current, scoped := d.tenantScope[spec.Name]
	if !scoped {
		// Currently unscoped: every tenant is authorized, so the intersection is
		// exactly the desired set.
		if d.tenantScope == nil {
			d.tenantScope = map[string]map[model.TenantID]struct{}{}
		}
		d.tenantScope[spec.Name] = desired
		return
	}
	out := make(map[model.TenantID]struct{}, len(desired))
	for t := range desired {
		if _, ok := current[t]; ok {
			out[t] = struct{}{}
		}
	}
	d.tenantScope[spec.Name] = out
}

// buildTenantScope indexes the per-destination tenant restriction.
//
// A malformed tenant id is dropped WITH A WARNING rather than ignored or fatal: the
// destination stays scoped, so the typo narrows access instead of widening it, and
// the operator is told. Silently treating the whole list as absent would be the
// dangerous reading — a typo would turn a scoped destination into a global one.
// checkDuplicateDestinationNames refuses an operator file that names a destination
// twice, BEFORE anything is opened.
//
// Neither "first wins" nor "last wins" described the old behavior, which is what
// made it dangerous rather than merely untidy: openAll let the first successfully
// opened connector win while buildTenantScope let the last SCOPED definition win, so
// tenant B's authorization could be attached to tenant A's connector. And a later
// duplicate with no tenants key was skipped rather than clearing the earlier scope,
// so a third rule applied again.
//
// Silently choosing either definition is not acceptable for a secret-bearing routing
// file. The operator wrote two things and gets told so.
func checkDuplicateDestinationNames(specs []notifyDestinationSpec) error {
	seen := make(map[string]struct{}, len(specs))
	for _, s := range specs {
		name := strings.TrimSpace(s.Name)
		if _, dup := seen[name]; dup {
			return fmt.Errorf("notify: destination %q is declared more than once; "+
				"refusing to guess which definition owns the name (connector identity and "+
				"tenant authorization would come from different entries)", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func buildTenantScope(specs []notifyDestinationSpec, log *slog.Logger) map[string]map[model.TenantID]struct{} {
	out := map[string]map[model.TenantID]struct{}{}
	for _, s := range specs {
		if s.Tenants == nil {
			continue // unscoped: every tenant may address it
		}
		set := make(map[model.TenantID]struct{}, len(s.Tenants))
		for _, raw := range s.Tenants {
			t, present, err := parseBusinessTenant("notify destination: tenant scope entry", raw)
			if err != nil || !present {
				if log != nil {
					log.Warn("notify: destination tenant scope entry is not a tenant id and is IGNORED; the destination stays scoped and this tenant will NOT be able to address it",
						"destination", s.Name, "entry", raw)
				}
				continue
			}
			set[t] = struct{}{}
		}
		out[s.Name] = set
	}
	return out
}

// connectorResolvedDigest is the one-way, canonical (length-prefixed) digest of a
// connector's RESOLVED effective config — kind + the resolved settings (secret
// VALUES, one-way hashed, never exposed) + external-plugin identity. A rotation
// of a store:-backed value (e.g. a webhook URL) under an unchanged secret ref
// therefore changes the digest. Never returns or logs a secret value.
func connectorResolvedDigest(kind string, resolved sdk.Config, plugin *externalPluginSpec) string {
	var b strings.Builder
	b.WriteString("olivares.notify.connector.resolved.v1")
	lp := func(s string) { b.WriteString(strconv.Itoa(len(s))); b.WriteByte(':'); b.WriteString(s) }
	lp(kind)
	keys := make([]string, 0, len(resolved.Settings))
	for k := range resolved.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lp(k)
		lp(resolved.Settings[k])
	}
	if plugin != nil {
		pb, _ := json.Marshal(plugin)
		lp(string(pb))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// openAll resolves each destination's secret references to live values and opens
// its connector. A spec with an unknown kind, an unresolvable secret reference or a
// configuration error is logged and SKIPPED (it does not abort boot) — that
// destination then resolves to unknown_destination in the ledger, a visible
// misconfiguration rather than a silent failure. It runs before rt.Start.
func (d *connectorDispatcher) openAll(ctx context.Context, r *secret.Resolver, log *slog.Logger) {
	// Late-bind the verified-dispense seam to the real runtime (nil in a boot without
	// a loader → external destinations skip honestly; tests inject their own).
	if d.dispenseVerified == nil && d.rt != nil {
		d.dispenseVerified = d.rt.DispenseOutputPluginVerified
	}
	for _, spec := range d.specs {
		// The duplicate check runs BEFORE any work so a shadowed later definition
		// never opens a connection (or launches a subprocess) that is then dropped.
		if d.has(spec.Name) {
			log.Warn("notify: duplicate destination name; later definition ignored", "name", spec.Name)
			continue
		}
		// an EXTERNAL (third-party) output binary takes precedence over Kind —
		// deny-closed admission against the single ConnectorTrust root, then a
		// checksum-pinned, confined dispense (the openExternal path).
		if spec.Plugin != nil {
			d.openExternal(ctx, spec, r, log)
			continue
		}
		if bin, isPlugin := outputPluginForKind[spec.Kind]; isPlugin {
			d.openPlugin(ctx, spec, bin, r, log)
			continue
		}
		conn, err := buildOutputConnector(spec.Kind)
		if err != nil {
			log.Warn("notify: destination has unknown connector kind; skipped", "name", spec.Name, "kind", spec.Kind)
			continue
		}
		// Resolve the destination's secret references (Slack webhook URL, PagerDuty
		// routing key, …) against the secret store / backends, enforcing the strict
		// no-inline-secret rule on the connector's declared secret fields.
		resolved, rerr := resolveConfig(ctx, r, conn.Descriptor(), sdk.Config{Settings: spec.Config})
		if rerr != nil {
			log.Warn("notify: destination secret reference could not be resolved; skipped", "name", spec.Name, "kind", spec.Kind)
			continue
		}
		// Never log err here — a connector's Open error can embed the configured
		// endpoint/credential; only the non-sensitive name/kind is logged.
		if oerr := conn.Open(ctx, resolved); oerr != nil {
			log.Warn("notify: destination failed to open (configuration error); skipped", "name", spec.Name, "kind", spec.Kind)
			continue
		}
		d.put(spec.Name, conn, connectorResolvedDigest(spec.Kind, resolved, nil))
	}
}

// has reports whether a destination name is already opened (read-locked).
func (d *connectorDispatcher) has(name string) bool {
	d.mu.RLock()
	_, ok := d.conns[name]
	d.mu.RUnlock()
	return ok
}

// put records an opened in-process or first-party destination (write-locked). These
// kinds are not live-reloadable, so they are not tracked in ext; resolvedFP carries
// their connector fingerprint (the digest of the RESOLVED config actually opened).
func (d *connectorDispatcher) put(name string, conn sdk.OutputConnector, resolvedFP string) {
	d.mu.Lock()
	d.conns[name] = conn
	d.resolvedFP[name] = resolvedFP
	d.mu.Unlock()
}

// openPlugin wires one OUT-OF-PROCESS destination (outputPluginForKind): extract
// the embedded binary, dispense the connector over gRPC (AutoMTLS), resolve the
// config references and Open it in its subprocess. Every failure is logged and
// SKIPPED (the openAll contract) — notably a plain dev build without `task
// build:connectors` warns honestly instead of pretending the destination exists.
func (d *connectorDispatcher) openPlugin(ctx context.Context, spec notifyDestinationSpec, bin string, r *secret.Resolver, log *slog.Logger) {
	if d.rt == nil || d.embedDir == "" {
		log.Warn("notify: plugin destination has no runtime loader; skipped", "name", spec.Name, "kind", spec.Kind)
		return
	}
	path, err := firstparty.Extract(d.embedDir, bin)
	if err != nil {
		log.Warn("notify: first-party output connector not embedded in this build; destination skipped (build it with `task build:connectors`). It will not deliver.",
			"name", spec.Name, "kind", spec.Kind, "binary", bin)
		return
	}
	// An out-of-process plugin has no in-process descriptor, so the strict
	// no-inline-secret check is skipped — references still resolve (the
	// wireSources plugin-path rule).
	resolved, rerr := resolveConfig(ctx, r, sdk.Descriptor{}, sdk.Config{Settings: spec.Config})
	if rerr != nil {
		log.Warn("notify: destination secret reference could not be resolved; skipped", "name", spec.Name, "kind", spec.Kind)
		return
	}
	conn, client, err := d.rt.DispenseOutputPlugin(path)
	if err != nil {
		log.Warn("notify: failed to launch output connector plugin; destination skipped", "name", spec.Name, "kind", spec.Kind, "error", err)
		return
	}
	// Never log oerr — the Open error can embed the configured endpoint/credential.
	if oerr := conn.Open(ctx, resolved); oerr != nil {
		client.Kill()
		log.Warn("notify: destination failed to open (configuration error); skipped", "name", spec.Name, "kind", spec.Kind)
		return
	}
	d.rt.TrackOutputPlugin(conn, client)
	d.put(spec.Name, conn, connectorResolvedDigest(spec.Kind, resolved, nil))
	log.Info("notify: wired destination (out-of-process plugin, AutoMTLS)", "name", spec.Name, "kind", spec.Kind)
}

// Destinations returns the opened destination names (never a credential), sorted for
// a deterministic order. Read-locked so it is safe against a live reload.
func (d *connectorDispatcher) Destinations() []string {
	d.mu.RLock()
	out := make([]string, 0, len(d.conns))
	for name := range d.conns {
		out = append(out, name)
	}
	d.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Deliver resolves the destination name to its opened connector and delivers the
// notification, or returns notify.ErrUnknownDestination so the module records the
// misconfiguration. The read lock is held ONLY across the map lookup, never across
// Notify: a slow or hung destination must never block a concurrent reload.
// A destination swapped by a reload the instant after this lookup delivers on the
// old connector; if that attempt loses the race with the old subprocess's Kill it
// errors and the durable outbox retries on the new connector — at-least-once,
// never lost.
func (d *connectorDispatcher) Deliver(ctx context.Context, tenant model.TenantID, destination string, n sdk.Notification) error {
	d.mu.RLock()
	conn, ok := d.conns[destination]
	allowed := d.addressableBy(tenant, destination)
	d.mu.RUnlock()
	// A destination this tenant may not address is reported as UNKNOWN, not as
	// forbidden. The two are deliberately the same answer: distinguishing them would
	// let a route author enumerate another tenant's destination names by watching
	// which error came back, and the name is the thing worth protecting here.
	if !ok || !allowed {
		return notify.ErrUnknownDestination
	}
	return conn.Notify(ctx, n)
}

// addressableBy reports whether a tenant may name this destination. The caller
// holds d.mu.
func (d *connectorDispatcher) addressableBy(tenant model.TenantID, destination string) bool {
	scope, scoped := d.tenantScope[destination]
	if !scoped {
		return true // unscoped: addressable by every tenant
	}
	_, ok := scope[tenant]
	return ok
}

// DestinationsFor returns the destinations a tenant may address, which is what a
// tenant-facing surface may show. Destinations() stays the operator's whole-estate
// view.
func (d *connectorDispatcher) DestinationsFor(tenant model.TenantID) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]string, 0, len(d.conns))
	for name := range d.conns {
		if d.addressableBy(tenant, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ConnectorFingerprint returns the digest of the LIVE connector actually behind a
// destination NAME (the RESOLVED config opened, updated on every SIGHUP reload),
// so a workflow that froze it at approval BLOCKS if the operator swaps or
// reconfigures the connector — or rotates a resolved secret VALUE — under an
// unchanged route destination (Flag B). ok=false when the name has no LIVE
// connector (never opened, or SIGHUP-removed); the caller then DENIES. Read under
// the same lock as Deliver; the digest never exposes a secret value.
func (d *connectorDispatcher) ConnectorFingerprint(destination string) (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	fp, ok := d.resolvedFP[destination]
	return fp, ok
}

// buildOutputConnector constructs an unopened output connector by kind. The first
// six are the connectors (siem is the generic HTTP SIEM sink); the SIEM/log/
// telemetry egress kinds added by give a SOC the standard transport it already
// runs: syslog (UDP/TCP/TLS 6514, also CEF/LEEF), splunkhec (full HEC envelope +
// indexer ack), otlplog (OTLP/HTTP logs), chronicle (Google SecOps UDM), datadog
// (Logs Intake v2), elastic (ECS + _bulk), snmp (SNMPv3 authPriv traps) and filelog
// (the tailable file/stream for a Universal-Forwarder posture).
func buildOutputConnector(kind string) (sdk.OutputConnector, error) {
	switch kind {
	case "slack":
		return slack.New(), nil
	case "teams":
		return teams.New(), nil
	case "pagerduty":
		return pagerduty.New(), nil
	case "opsgenie":
		return opsgenie.New(), nil
	case "webhook":
		return webhook.New(), nil
	case "siem":
		return siem.New(), nil
	// ITSM/ChatOps destinations: ServiceNow (Table/SIR/em_event), Jira/JSM, the
	// reworked Teams (Workflows + Adaptive Cards, above), email (SMTP+DKIM) and the
	// out-of-band Twilio SMS (nice-to-have, probable post-v1).
	case "servicenow":
		return servicenow.New(), nil
	case "jira":
		return jira.New(), nil
	case "email":
		return email.New(), nil
	case "twilio":
		return twilio.New(), nil
	case "syslog":
		return syslog.New(), nil
	case "splunkhec":
		return splunkhec.New(), nil
	case "otlplog":
		return otlplog.New(), nil
	case "chronicle":
		return chronicle.New(), nil
	case "datadog":
		return datadog.New(), nil
	case "elastic":
		return elastic.New(), nil
	case "snmp":
		return snmp.New(), nil
	case "filelog":
		return filelog.New(), nil
	// Records-management: the S3 Object Lock (WORM) sink — each notification
	// becomes one immutable, lock-verified object (it doubles as the audit-archive
	// sink via its exported Put face).
	case "s3archive":
		return s3archive.New(), nil
	default:
		// build-tag-gated enterprise destinations (e.g. "teamsbot", the
		// registered-bot Action.Execute Teams connector). Returns (nil,false) in the
		// default build, so an unknown kind still errors as before (no rug-pull).
		if c, ok := enterpriseOutputConnector(kind); ok {
			return c, nil
		}
		return nil, fmt.Errorf("unknown output connector kind %q", kind)
	}
}

// loadNotifyDestinations reads the optional notification-destination provisioning
// file named by OLIVARES_NOTIFY_CONFIG (a JSON array of notifyDestinationSpec). It
// is the operator's secret-bearing config, kept out of the store. A missing path yields
// no destinations; a supplied path must be readable and contain valid JSON.
func loadNotifyDestinations(_ *slog.Logger) ([]notifyDestinationSpec, error) {
	path := os.Getenv("OLIVARES_NOTIFY_CONFIG")
	if path == "" {
		return nil, nil
	}
	var specs []notifyDestinationSpec
	if err := loadOperatorJSONConfig("OLIVARES_NOTIFY_CONFIG", path, &specs); err != nil {
		return nil, err
	}
	// A duplicate name is fatal HERE, before any connector is opened or any
	// subprocess launched — see checkDuplicateDestinationNames for why choosing one
	// silently is not an option.
	if err := checkDuplicateDestinationNames(specs); err != nil {
		return nil, fmt.Errorf("OLIVARES_NOTIFY_CONFIG is set to %q: %w", path, err)
	}
	return specs, nil
}
