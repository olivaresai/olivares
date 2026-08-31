// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/firstparty"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secret"
)

// the read-only half of the source roster.
//
// `sources set` persisted directly and its group had no plan, no validate and no
// test: an operator changing WHERE A TENANT'S DATA COMES FROM had no way to see
// the change before making it, no way to check the source answers, and nothing to
// read back before reverting. In a control plane that is a design defect, not a
// missing convenience — the risk is a silent change of provenance, not a typo.
//
// The three verbs split by WHAT THEY COST, which is the only split that survives
// contact with an operator:
//
//	plan     — says WHAT WOULD CHANGE. No row is written, no source is opened.
//	validate — says the configuration is COHERENT BY ITSELF. Dials no source.
//	test     — says the source ACTUALLY ANSWERS. This one dials, and that is
//	           precisely why it is not folded into validate: a validate that needs
//	           the network is a validate people learn to skip.
//
// `set` is unchanged in what it does — it still applies, with no new mandatory
// gate and no prior-plan requirement. A missing verb is fixed by adding the verb,
// never by fencing the one that works.
//
// All three boot through rosterReadBoot: read-only AND without starting the
// runtime or reconciling the roster, so a preview does not open every connector
// a deployment has. The cost is one honest divergence — in a data directory that
// holds no installation, `set` would create one and `plan` reports there is
// nothing here — and it is named in plan's help.
//
// ⚠ WHAT THESE VERBS STILL DO, because the sol-max contrast measured it and a
// comfortable silence here would be worse than the defect: booting still OPENS
// AND MIGRATES the store, bootstraps leadership, and CREATES any missing sealer
// key. The contrast removed secret-store.key from an installation, ran
// `sources plan`, and the plan recreated it and then printed "NOTHING WAS
// WRITTEN". That belongs to boot() and is identical for every read-only verb in
// this binary (`sources ls`, `secrets ls`, `audit verify`); closing it needs a
// genuinely minimal read path, which is a unit of its own. Until then the
// commands say "no source was written or wired", which is what they can honestly
// promise, and NOT "writes nothing".

// sourceEdit is the set of flags that DESCRIBE a source. plan, test and set share
// one declaration so the verbs cannot drift apart in what they accept, and so a
// flag added for one is a flag the others already understand.
type sourceEdit struct {
	name, kind, tenant                  string
	pollSeconds                         int
	enabled                             bool
	configKV                            []string
	pluginPath, pluginSHA, pluginBundle string
	pluginPredicates                    []string
}

// sourceEditFlagNames is every flag addSourceEditFlags declares that describes
// the source itself (--name is the target, not a description). Enumerated so a
// command can ask "did the operator try to edit anything?" without listing them
// again at the call site and drifting.
var sourceEditFlagNames = []string{
	"kind", "tenant", "poll-seconds", "enabled", "config",
	"plugin-path", "plugin-sha256", "plugin-bundle", "plugin-predicate",
}

func addSourceEditFlags(cmd *cobra.Command, e *sourceEdit) {
	cmd.Flags().StringVar(&e.name, "name", "", "source name (the roster key)")
	cmd.Flags().StringVar(&e.kind, "kind", "", "first-party connector kind (e.g. vault, claude); omit for a plugin source")
	cmd.Flags().StringVar(&e.tenant, "tenant", "", "business tenant the observations belong to")
	cmd.Flags().IntVar(&e.pollSeconds, "poll-seconds", 0, "re-run a batch source every N seconds (0 = run once / streaming)")
	cmd.Flags().BoolVar(&e.enabled, "enabled", true, "whether the source is wired into the engine")
	cmd.Flags().StringArrayVar(&e.configKV, "config", nil, "connector setting key=value (repeatable); use store:<name> for secrets")
	cmd.Flags().StringVar(&e.pluginPath, "plugin-path", "", "external connector plugin binary path")
	cmd.Flags().StringVar(&e.pluginSHA, "plugin-sha256", "", "external plugin pinned sha256 digest")
	cmd.Flags().StringVar(&e.pluginBundle, "plugin-bundle", "", "external plugin Sigstore attestation bundle path")
	cmd.Flags().StringArrayVar(&e.pluginPredicates, "plugin-predicate", nil, "narrow the trust policy's predicate allow-list for this source (repeatable)")
}

// desiredSourceDef computes the definition a `set` with these flags would
// persist: the row as it stands today with ONLY the flags the operator actually
// passed applied on top, or a fresh enabled default when there is no row.
//
// plan, test and set all go through this one function, and that is the whole
// point of the unit. `plan` exists to answer "what will `set` do"; a plan that
// recomputed the answer its own way could be wrong in exactly the case that
// matters — the edit nobody double-checked — so the preview is the apply's own
// arithmetic rather than a second opinion about it.
func desiredSourceDef(f *pflag.FlagSet, e *sourceEdit, existing model.SourceDef, found bool) (model.SourceDef, error) {
	def := existing
	if !found {
		def = model.SourceDef{Name: e.name, Scope: auth.GlobalSourceScope, Enabled: true, Config: map[string]string{}}
	}
	if f.Changed("kind") {
		def.Kind = e.kind
	}
	if f.Changed("tenant") {
		def.Tenant = e.tenant
	}
	if f.Changed("poll-seconds") {
		def.PollSeconds = e.pollSeconds
	}
	if f.Changed("enabled") {
		def.Enabled = e.enabled
	}
	if f.Changed("config") {
		merged, perr := parseConfigKV(e.configKV, def.Config)
		if perr != nil {
			return model.SourceDef{}, perr
		}
		def.Config = merged
	}
	if f.Changed("plugin-path") || f.Changed("plugin-sha256") || f.Changed("plugin-bundle") || f.Changed("plugin-predicate") {
		if def.Plugin == nil {
			def.Plugin = &model.SourcePluginRef{}
		}
		if f.Changed("plugin-path") {
			def.Plugin.Path = e.pluginPath
		}
		if f.Changed("plugin-sha256") {
			def.Plugin.SHA256 = e.pluginSHA
		}
		if f.Changed("plugin-bundle") {
			def.Plugin.Bundle = e.pluginBundle
		}
		if f.Changed("plugin-predicate") {
			def.Plugin.PredicateTypes = e.pluginPredicates
		}
	}
	return def, nil
}

// --- the diff ----------------------------------------------------------------

// sourceChange is one field a `set` would move. From is empty for a field the
// row does not have today (including every field of a source being created).
type sourceChange struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// orderedSourceFields is the reading order of the fixed fields. Config and plugin
// keys follow, sorted, so two runs over the same state print the same plan —
// a plan whose line order moves cannot be diffed by the operator or by a script.
var orderedSourceFields = []string{"kind", "tenant", "poll_seconds", "enabled"}

// sourceFields flattens a definition to the comparable field set. Only the
// operator-authored fields appear: the row's identity and version (BaseFields)
// are not something a `set` decides.
func sourceFields(def model.SourceDef) map[string]string {
	out := map[string]string{
		"kind":         def.Kind,
		"tenant":       def.Tenant,
		"poll_seconds": strconv.Itoa(def.PollSeconds),
		"enabled":      strconv.FormatBool(def.Enabled),
	}
	for k, v := range def.Config {
		out["config."+k] = v
	}
	if def.Plugin != nil {
		out["plugin.path"] = def.Plugin.Path
		out["plugin.sha256"] = def.Plugin.SHA256
		out["plugin.bundle"] = def.Plugin.Bundle
		if len(def.Plugin.PredicateTypes) > 0 {
			out["plugin.predicate_types"] = strings.Join(def.Plugin.PredicateTypes, ",")
		}
	}
	return out
}

// diffSourceDefs lists what changes between the persisted row and the desired
// one. When there is no row (found=false) every field of the desired definition
// is reported as new — a creation shows the whole row it would write, not just
// the fields that happen to differ from a zero value.
func diffSourceDefs(before model.SourceDef, found bool, after model.SourceDef) []sourceChange {
	beforeF := map[string]string{}
	if found {
		beforeF = sourceFields(before)
	}
	afterF := sourceFields(after)
	// The masking rule is derived from BOTH definitions: a `set` that changes the
	// kind moves a value between two descriptors, and a field either declares
	// secret is a field this plan must not print.
	secretKeys := secretConfigKeys(before, after)

	seen := map[string]bool{}
	keys := make([]string, 0, len(beforeF)+len(afterF))
	for _, k := range orderedSourceFields {
		seen[k] = true
		keys = append(keys, k)
	}
	extra := make([]string, 0, len(beforeF)+len(afterF))
	for k := range beforeF {
		if !seen[k] {
			seen[k] = true
			extra = append(extra, k)
		}
	}
	for k := range afterF {
		if !seen[k] {
			seen[k] = true
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)

	out := make([]sourceChange, 0, len(keys))
	for _, k := range keys {
		from, to := beforeF[k], afterF[k]
		if from == to {
			continue
		}
		out = append(out, sourceChange{Field: k, From: planValue(k, from, secretKeys), To: planValue(k, to, secretKeys)})
	}
	return out
}

// redactedPlanValue stands in for a value the plan refuses to echo.
const redactedPlanValue = "<redacted>"

// planValue renders a field value as the plan may PRINT it. A plan is pasted into
// tickets, PRs and CI logs, so the two shapes the store refuses outright — a value
// with credential material embedded in it, and a literal under a
// credential-bearing key — are shown as <redacted>. Those values are refused
// anyway (the problems list names the field, exactly as the store does), so
// printing them buys the operator nothing and can publish a secret they typed by
// mistake. A `store:<name>` REFERENCE is printed in full: it is not a secret, and
// seeing which reference a source will use is most of the value of a plan.
func planValue(field, value string, secretKeys map[string]bool) string {
	key, isConfig := strings.CutPrefix(field, "config.")
	if !isConfig || value == "" {
		return value
	}
	// The connector's OWN declaration first. This is the rule `set` refuses by
	// (checkInlineSecrets -> secret.CheckNoInlineSecrets over the descriptor), and
	// the sol-max contrast found the plan not using it: the github connector
	// declares `pat` secret, no heuristic below recognizes that name, and a literal
	// `ghp_...` was printed in full to stdout AND to the JSON — by the same report
	// that then explained set would refuse it.
	if secretKeys[key] && !secret.IsReference(value) {
		return redactedPlanValue
	}
	if secret.ContainsInlineCredential(value) {
		return redactedPlanValue
	}
	if secret.IsCredentialBearingConfigKey(key) && !secret.IsReference(value) {
		return redactedPlanValue
	}
	return value
}

// secretConfigKeys is the set of config keys the connectors behind these
// definitions declare SECRET, so the plan can mask exactly what the write would
// refuse rather than a guess at it.
//
// For an external plugin or an unknown kind it contributes nothing, and that is
// not an oversight: the host does not hold those descriptors, which is the same
// limitation checkInlineSecrets declares for the write itself. Those sources fall
// back to the store-level rules below, which are the only rules that apply to
// them anyway — and the plan says so in NotChecked rather than implying cover it
// does not have.
func secretConfigKeys(defs ...model.SourceDef) map[string]bool {
	out := map[string]bool{}
	for _, def := range defs {
		if def.Plugin != nil {
			continue
		}
		conn, ok := buildInProcSource(def.Kind)
		if !ok {
			continue
		}
		for _, f := range conn.Descriptor().ConfigFields {
			if f.Secret {
				out[f.Key] = true
			}
		}
	}
	return out
}

// --- the offline check -------------------------------------------------------

// The two places a source definition can be refused. They are NOT the same
// event, and an operator who cannot tell them apart cannot act on either:
//
//	problemAtWrite — the STORE refuses. `set` fails and nothing is persisted.
//	problemAtApply — the store accepts and the RECONCILER refuses. `set` reports
//	                 success, the row is durable, and the running engine declines
//	                 to wire it: "persisted, but the live apply was rejected".
//
// The second is the surprise this whole unit exists to remove, so it is named
// rather than folded into a single "invalid".
const (
	problemAtWrite = "write"
	problemAtApply = "apply"
)

// sourceProblem is one reason this definition would not work, and where.
type sourceProblem struct {
	At      string `json:"at"`
	Message string `json:"message"`
}

// sourceCheck is the verdict of everything that can be decided WITHOUT the
// network, in the three answers this repository insists on: it is fine
// (Problems empty), it is broken (Problems), or it could not be looked at
// (NotChecked). Collapsing the third into the first is the defect that has cost
// most here, so it has its own list and its own line in the output.
type sourceCheck struct {
	Valid      bool            `json:"valid"`
	Problems   []sourceProblem `json:"problems,omitempty"`
	Warnings   []string        `json:"warnings,omitempty"`
	NotChecked []string        `json:"not_checked,omitempty"`
}

// refusedAt reports whether any problem would bite at the given stage.
func (c sourceCheck) refusedAt(stage string) bool {
	for _, p := range c.Problems {
		if p.At == stage {
			return true
		}
	}
	return false
}

// checkSourceOffline runs every rule that can refuse this definition without
// touching the network, in the order the operator would meet them:
//
//  1. what the STORE would refuse (auth.ValidateSourceDef — the write's own rule,
//     called rather than reimplemented);
//  2. what the descriptor-aware inline-secret guard would refuse (the same
//     checkInlineSecrets `set` calls);
//  3. what the RECONCILER would refuse later, and this is the part that only
//     existed after the write: an unknown kind, a connector that is not embedded
//     in this build, an external plugin that fails admission, and a connector
//     identity another enabled source already owns.
//
// roster is the rest of the persisted roster (the identity check needs it); trust
// is the deployment's connector-trust policy (nil is itself a refusal for an
// external plugin, deny-closed).
func checkSourceOffline(def model.SourceDef, roster []model.SourceDef, trust *connectorTrustSpec) sourceCheck {
	var c sourceCheck
	// What the STORE refuses: `set` never gets past these, so nothing is persisted.
	if err := auth.ValidateSourceDef(def); err != nil {
		c.Problems = append(c.Problems, sourceProblem{At: problemAtWrite, Message: err.Error()})
	}
	if err := checkInlineSecrets(def); err != nil {
		c.Problems = append(c.Problems, sourceProblem{At: problemAtWrite, Message: err.Error()})
	}

	// What the RECONCILER refuses: the write succeeds and the engine still will not
	// wire it. Before this command there was no way to learn any of these without
	// performing the write first.
	switch {
	case def.Plugin != nil:
		spec := externalPluginSpec{Path: def.Plugin.Path, SHA256: def.Plugin.SHA256, Bundle: def.Plugin.Bundle, PredicateTypes: def.Plugin.PredicateTypes}
		if _, refusal := admitExternalPlugin(spec, trust); refusal != "" {
			c.Problems = append(c.Problems, sourceProblem{At: problemAtApply, Message: "external connector plugin refused (deny-closed): " + refusal})
		}
		c.NotChecked = append(c.NotChecked,
			"the connector identity of an external plugin is only known once the binary is launched, so a collision with another source cannot be ruled out here (`olivares sources test` launches it)",
			"this host does not hold an external plugin's descriptor, so it cannot know which of its config fields are secret: the plan masks only what the STORE would refuse, and any other value is printed as written")
	case strings.TrimSpace(def.Kind) == "":
		// Already reported by ValidateSourceDef ("either a kind OR a plugin"); saying
		// it twice in different words would read as two problems.
	default:
		for _, msg := range checkSourceKind(def, roster) {
			c.Problems = append(c.Problems, sourceProblem{At: problemAtApply, Message: msg})
		}
		if _, isPlugin := pluginBinaryForKind[def.Kind]; isPlugin {
			c.NotChecked = append(c.NotChecked, fmt.Sprintf("the %q connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)", def.Kind))
		}
	}

	if !def.Enabled {
		c.Warnings = append(c.Warnings, "the source is DISABLED: it is persisted and listed, but the engine ingests nothing from it until it is enabled")
	}
	if refs := configReferences(def.Config); len(refs) > 0 {
		c.Warnings = append(c.Warnings, fmt.Sprintf("%s: secret references are resolved when the source is APPLIED, not here — `olivares sources test` proves they resolve", strings.Join(refs, ", ")))
	}
	c.Valid = len(c.Problems) == 0
	return c
}

// checkSourceKind answers the two questions the store never asks: can this build
// run this kind at all, and is another enabled source already using the connector
// identity this one would claim.
func checkSourceKind(def model.SourceDef, roster []model.SourceDef) []string {
	var problems []string
	if bin, isPlugin := pluginBinaryForKind[def.Kind]; isPlugin {
		// Same sentence the reconciler uses when it refuses the apply
		// (reconcile.go defaultPrepare), so the preview and the apply speak once.
		if !slices.Contains(firstparty.Available(), bin) {
			problems = append(problems, fmt.Sprintf("the %q connector is not embedded in this build (build it with `task build:connectors`, or run it from a collector)", def.Kind))
		}
		return problems
	}
	conn, ok := buildInProcSource(def.Kind)
	if !ok {
		return append(problems, fmt.Sprintf("unknown or unsupported source kind %q", def.Kind))
	}
	if !def.Enabled {
		// A disabled source is never wired, so it can collide with nothing.
		return problems
	}
	identity := conn.Descriptor().Name
	for _, other := range roster {
		if other.Name == def.Name || !other.Enabled || other.Plugin != nil {
			continue
		}
		oconn, ook := buildInProcSource(other.Kind)
		if !ook || oconn.Descriptor().Name != identity {
			continue
		}
		problems = append(problems, fmt.Sprintf("connector identity %q is already used by source %q (only one instance per connector identity)", identity, other.Name))
	}
	return problems
}

// configReferences names the config keys holding a secret reference, sorted.
func configReferences(cfg map[string]string) []string {
	out := make([]string, 0, len(cfg))
	for k, v := range cfg {
		if secret.IsReference(v) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// missingSourceError reports a named source that is not in the roster and that
// the operator did not describe with any flag.
//
// Without it, `validate --name vualt-prod` (a typo) answers "a source must name
// the business tenant" and "either a kind OR a plugin": every rule failing at
// once on a definition the operator never wrote. The row simply is not there,
// and that is the answer. `plan` is the exception on purpose — absence there
// means CREATE, which is exactly what it is for.
func missingSourceError(f *pflag.FlagSet, name, verb string) error {
	for _, n := range sourceEditFlagNames {
		if f.Changed(n) {
			return nil // the operator is describing a candidate, not addressing a row
		}
	}
	return exitcode.New(exitcode.NotFound, fmt.Errorf(
		"no source named %q in the roster — `olivares sources ls` lists them, and `olivares sources plan --name %s ...` %s the one you would create",
		name, name, verb))
}

// --- what a running engine would do ------------------------------------------

// planLiveEffect names what a RUNNING engine would do with this change at its
// next reload (POST /v1/console/runtime/reload or SIGHUP).
//
// It is derived from the roster and the reconciler's OWN rules — the enabled gate
// and the identity fingerprint — never from any engine's live state: that map
// lives in the serve process and this command is offline. So it says what the
// rules imply, and says it in those words. Claiming to know what a particular
// engine currently has wired would be the kind of confident sentence this repo
// pays for later.
func planLiveEffect(before model.SourceDef, found bool, after model.SourceDef, check sourceCheck) string {
	switch {
	case check.refusedAt(problemAtWrite):
		return "nothing — the write itself would be refused, so there would be no new row to reload"
	case check.refusedAt(problemAtApply):
		return "REFUSE to wire it (deny-closed) — the row WOULD be persisted and the engine would decline it, leaving whatever runs today running"
	}
	switch {
	case !after.Enabled && (!found || !before.Enabled):
		return "nothing — the source is disabled, so it is stored and not run"
	case !after.Enabled:
		return "STOP ingesting from this source (it stays in the roster, disabled)"
	case !found:
		return "wire the new source and start ingesting from it"
	case !before.Enabled:
		return "wire the source and start ingesting from it (it was disabled)"
	case fingerprintDef(before) == fingerprintDef(after):
		return "nothing — no field the reconciler fingerprints changed, so the running connector is left alone"
	default:
		return "re-open the connector in place with the new configuration (a rotate: the new instance opens BEFORE the old one is dropped)"
	}
}

// --- plan --------------------------------------------------------------------

// sourcePlanReport is the whole answer, and the same value renders the text and
// the JSON so the two can never disagree. Persisted is always false and is
// serialized anyway: a machine reading a plan should be able to ASSERT that
// nothing was written rather than infer it from the verb's name.
type sourcePlanReport struct {
	Name       string         `json:"name"`
	Action     string         `json:"action"`
	Exists     bool           `json:"exists"`
	Changes    []sourceChange `json:"changes"`
	LiveEffect string         `json:"live_effect"`
	Check      sourceCheck    `json:"check"`
	Persisted  bool           `json:"persisted"`
}

func sourcesPlanCmd() *cobra.Command {
	var dataDir, engine, dsn string
	var e sourceEdit
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what a `sources set` with these flags WOULD change — no source is written or opened",
		Long: "plan is `sources set` with the writing taken out. It reads the roster, applies exactly the\n" +
			"flags you passed the way set would, and prints the field-by-field difference, whether the\n" +
			"result would be accepted, and what a running engine would do with it at its next reload.\n" +
			"NO source row is written and NO connector is opened.\n\n" +
			"It is not a no-side-effect command, and saying otherwise would be a lie you only discover\n" +
			"later: opening an installation migrates its store and creates any missing sealer key, the\n" +
			"same as every other read-only verb in this binary. What it will not do is touch the roster\n" +
			"or dial a source.\n\n" +
			"It boots the store READ-ONLY, so unlike `set` it will not create an installation that is not\n" +
			"there: in an empty --data-dir it reports that there is no installation instead of planning\n" +
			"against one it would have to make first.\n\n" +
			"Exit code 5 (conflict) when the planned definition would be REFUSED — the report is still\n" +
			"printed, so plan works as a CI gate in front of set.",
		Example: `  # What would this change, exactly?
  olivares sources plan --name vault-prod --tenant t_xyz789 --data-dir /var/lib/olivares

  # The same question, for a machine
  olivares sources plan --name vault-prod --config addr=https://vault.internal:8200 -o json`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := rosterReadBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			existing, found, err := eng.sourceStore.Get(cmd.Context(), auth.GlobalSourceScope, e.name)
			if err != nil {
				return err
			}
			def, err := desiredSourceDef(cmd.Flags(), &e, existing, found)
			if err != nil {
				return err
			}
			roster, err := eng.sourceStore.List(cmd.Context(), auth.GlobalSourceScope)
			if err != nil {
				return err
			}

			rep := sourcePlanReport{
				Name:    e.name,
				Exists:  found,
				Changes: diffSourceDefs(existing, found, def),
				Check:   checkSourceOffline(def, roster, eng.sourceReconciler.trust),
			}
			rep.LiveEffect = planLiveEffect(existing, found, def, rep.Check)
			switch {
			case !found:
				rep.Action = "create"
			case len(rep.Changes) == 0:
				rep.Action = "no-op"
			default:
				rep.Action = "update"
			}
			if rerr := renderOut(cmd, func(out io.Writer) error { return writeSourcePlan(out, rep) }, rep); rerr != nil {
				return rerr
			}
			if !rep.Check.Valid {
				// The report names every problem; a second message would only repeat it.
				return exitcode.New(exitcode.Conflict, nil)
			}
			return nil
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	addSourceEditFlags(cmd, &e)
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func writeSourcePlan(out io.Writer, rep sourcePlanReport) error {
	switch rep.Action {
	case "create":
		fmt.Fprintf(out, "plan for source %q: CREATE — it is not in the roster\n", rep.Name)
	case "no-op":
		fmt.Fprintf(out, "plan for source %q: NO-OP — the roster already says exactly this\n", rep.Name)
	default:
		fmt.Fprintf(out, "plan for source %q: UPDATE — %d field(s) would change\n", rep.Name, len(rep.Changes))
	}
	if len(rep.Changes) > 0 {
		fmt.Fprintln(out)
		// render-exempt: this IS the text branch of renderOut. sourcesPlanCmd hands
		// the whole sourcePlanReport to renderOut, which calls this function only
		// when the selected output is text and marshals the very same value for
		// -o json. The E2 gate reads per occurrence and cannot see across the
		// function boundary, so the reason is stated here where the table is built.
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "  FIELD\tFROM\tTO")
		for _, ch := range rep.Changes {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", ch.Field, orDash(ch.From), orDash(ch.To))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	fmt.Fprintln(out)
	writeSourceCheck(out, rep.Check)
	fmt.Fprintf(out, "on the next reload/SIGHUP a running engine would: %s\n", rep.LiveEffect)
	fmt.Fprintln(out, "NO SOURCE WAS WRITTEN OR OPENED. Apply the same change with `olivares sources set` and these flags (set also requires --actor and --reason).")
	return nil
}

func writeSourceCheck(out io.Writer, c sourceCheck) {
	switch {
	case c.Valid:
		fmt.Fprintln(out, "configuration: VALID (everything that can be decided without the network)")
	case c.refusedAt(problemAtWrite):
		fmt.Fprintln(out, "configuration: `sources set` WOULD REFUSE THIS — nothing would be persisted")
	default:
		fmt.Fprintln(out, "configuration: `sources set` would PERSIST it and a running engine would NOT WIRE it")
	}
	for _, p := range c.Problems {
		fmt.Fprintf(out, "  ✗ [%s] %s\n", p.At, p.Message)
	}
	for _, w := range c.Warnings {
		fmt.Fprintf(out, "  ! %s\n", w)
	}
	for _, u := range c.NotChecked {
		fmt.Fprintf(out, "  ? not checked here: %s\n", u)
	}
}

// --- validate ----------------------------------------------------------------

// sourceValidateReport is one row's offline verdict. `validate` with no --name
// reports one of these per roster row.
type sourceValidateReport struct {
	Name      string      `json:"name"`
	Check     sourceCheck `json:"check"`
	Persisted bool        `json:"persisted"`
}

func sourcesValidateCmd() *cobra.Command {
	var dataDir, engine, dsn string
	var e sourceEdit
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check a source definition is coherent by itself — offline, no network, no writes",
		Long: "validate answers one question: is this configuration internally consistent and admissible?\n" +
			"It runs the store's own rules (name, tenant, a kind XOR a plugin, credential fields that\n" +
			"must hold references), the descriptor-aware inline-secret guard, and the checks that used to\n" +
			"speak only AFTER a write — is the kind known, is it embedded in this build, does the external\n" +
			"plugin pass admission, does another enabled source already own that connector identity.\n\n" +
			"It never dials the source. Whether it actually answers is `sources test`, and the\n" +
			"split is deliberate: a validate that needs the network is a validate people skip.\n\n" +
			"With --name it validates that row as it stands, or as your flags would leave it. With no\n" +
			"--name it validates EVERY row in the roster, which is the pre-flight for a reload.\n" +
			"Exit code 5 (conflict) if anything would be refused.",
		Example: `  # One source, as the roster holds it
  olivares sources validate --name vault-prod --data-dir /var/lib/olivares

  # One source, as these flags would leave it
  olivares sources validate --name vault-prod --kind vault --config addr=https://vault.internal:8200

  # The whole roster, before a reload
  olivares sources validate --data-dir /var/lib/olivares`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := cmd.Flags()
			if e.name == "" {
				for _, n := range sourceEditFlagNames {
					if f.Changed(n) {
						return exitcode.New(exitcode.Usage, fmt.Errorf(
							"--%s describes ONE source, so it needs --name; without --name this command validates every row in the roster as it stands", n))
					}
				}
			}
			eng, err := rosterReadBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			roster, err := eng.sourceStore.List(cmd.Context(), auth.GlobalSourceScope)
			if err != nil {
				return err
			}
			trust := eng.sourceReconciler.trust

			var reports []sourceValidateReport
			if e.name != "" {
				existing, found, gerr := eng.sourceStore.Get(cmd.Context(), auth.GlobalSourceScope, e.name)
				if gerr != nil {
					return gerr
				}
				if !found {
					if merr := missingSourceError(f, e.name, "describes"); merr != nil {
						return merr
					}
				}
				def, derr := desiredSourceDef(f, &e, existing, found)
				if derr != nil {
					return derr
				}
				reports = append(reports, sourceValidateReport{Name: e.name, Check: checkSourceOffline(def, roster, trust)})
			} else {
				for _, row := range roster {
					reports = append(reports, sourceValidateReport{Name: row.Name, Check: checkSourceOffline(row, roster, trust)})
				}
			}

			if rerr := renderOut(cmd, func(out io.Writer) error {
				if len(reports) == 0 {
					_, err := fmt.Fprintln(out, "no sources in the roster — nothing to validate")
					return err
				}
				for _, r := range reports {
					fmt.Fprintf(out, "source %q\n", r.Name)
					writeSourceCheck(out, r.Check)
				}
				return nil
			}, reports); rerr != nil {
				return rerr
			}
			for _, r := range reports {
				if !r.Check.Valid {
					return exitcode.New(exitcode.Conflict, nil)
				}
			}
			return nil
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	addSourceEditFlags(cmd, &e)
	return cmd
}

// --- test --------------------------------------------------------------------

// sourceTestReport is the answer to "does this source actually answer?".
// Persisted is serialized for the same reason as in the plan: a probe that also
// wrote would be a very expensive surprise, so the report states it.
type sourceTestReport struct {
	Name      string      `json:"name"`
	Kind      string      `json:"kind"`
	Answered  bool        `json:"answered"`
	Reason    string      `json:"reason,omitempty"`
	Detail    string      `json:"detail,omitempty"`
	Check     sourceCheck `json:"check"`
	Persisted bool        `json:"persisted"`
}

func sourcesTestCmd() *cobra.Command {
	var dataDir, engine, dsn string
	var e sourceEdit
	var timeout time.Duration
	var showConnectorError bool
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Open the source for real to prove it answers, then close it — nothing is wired or written",
		Long: "test is the verb that goes to the network. It resolves the source's secret references,\n" +
			"builds the connector exactly as the engine would, and calls Open — which the SDK defines as\n" +
			"the configuration-validation point, so a missing setting, a bad credential and an\n" +
			"unreachable target all surface here. Then it closes it again. Nothing is wired into a\n" +
			"running engine and nothing is persisted.\n\n" +
			"The offline checks run FIRST: a definition the store would refuse is never dialed.\n\n" +
			"By default the connector's own error is REPLACED by a generic reason, because that message\n" +
			"was produced against the RESOLVED configuration and can carry credential material into a\n" +
			"terminal, a ticket or a CI log. Pass --show-connector-error when you need it and accept that.\n\n" +
			"Exit code 5 (conflict) if the definition would be refused before dialing; 6 (server) if the\n" +
			"source did not answer.",
		Example: `  # Does the persisted source still answer?
  olivares sources test --name vault-prod --data-dir /var/lib/olivares

  # Would it answer with the address I am about to set? (nothing is written)
  olivares sources test --name vault-prod --config addr=https://vault.internal:8200`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := rosterReadBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			existing, found, err := eng.sourceStore.Get(cmd.Context(), auth.GlobalSourceScope, e.name)
			if err != nil {
				return err
			}
			if !found {
				if merr := missingSourceError(cmd.Flags(), e.name, "describes"); merr != nil {
					return merr
				}
			}
			def, err := desiredSourceDef(cmd.Flags(), &e, existing, found)
			if err != nil {
				return err
			}
			roster, err := eng.sourceStore.List(cmd.Context(), auth.GlobalSourceScope)
			if err != nil {
				return err
			}

			rep := sourceTestReport{Name: e.name, Kind: sourceKindLabel(def), Check: checkSourceOffline(def, roster, eng.sourceReconciler.trust)}
			if !rep.Check.Valid {
				rep.Reason = "the definition would be refused before anything is dialed — see the problems above"
				if rerr := renderOut(cmd, func(out io.Writer) error { return writeSourceTest(out, rep) }, rep); rerr != nil {
					return rerr
				}
				return exitcode.New(exitcode.Conflict, nil)
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			rep.Answered, rep.Reason, rep.Detail = probeSource(ctx, eng.sourceReconciler, def, showConnectorError)
			if rerr := renderOut(cmd, func(out io.Writer) error { return writeSourceTest(out, rep) }, rep); rerr != nil {
				return rerr
			}
			if !rep.Answered {
				return exitcode.New(exitcode.Server, nil)
			}
			return nil
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	addSourceEditFlags(cmd, &e)
	_ = cmd.MarkFlagRequired("name")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second,
		"give up on the connector after this long (a source that never answers must not hang the command forever)")
	cmd.Flags().BoolVar(&showConnectorError, "show-connector-error", false,
		"print the connector's own failure message. It was produced against the RESOLVED configuration and can embed credential material, so it is off by default")
	return cmd
}

// probeSource builds the connector the way the engine would and opens it. The
// prepared source is ALWAYS discarded: for a plugin kind, preparing it launched a
// subprocess, and a probe that leaked one per invocation would be worse than the
// missing verb it replaces.
func probeSource(ctx context.Context, sr *sourceReconciler, def model.SourceDef, showDetail bool) (answered bool, reason, detail string) {
	ps, cfg, rejection := sr.prepare(ctx, def)
	if rejection != "" {
		return false, rejection, ""
	}
	if ps == nil {
		// Every production branch of defaultPrepare returns either a prepared source
		// or a reason, never neither — but prepare is a field a test can replace, and
		// a nil dereference here would report a crash where the answer is "no answer".
		return false, "the connector could not be prepared, and no reason was given", ""
	}

	// The probe runs on its own goroutine and the deadline is enforced HERE, not
	// inside the connector.
	//
	// The straight-line version did not enforce anything: it handed ctx to Open and
	// trusted the connector to honor it, and Close never received a deadline at
	// all. A connector that ignores its context — or a Close that blocks — hung the
	// command for ever, under a flag whose help promised it "must not hang the
	// command forever". The sol-max contrast measured that, and also that a hang
	// meant `ps.Discard()` was never reached, so a launched plugin subprocess
	// outlived the command that started it.
	//
	// On a timeout the caller stops waiting, discards the prepared source (which
	// kills the subprocess and releases its confinement) and reports honestly. The
	// probe goroutine may still be parked inside a connector that will not return;
	// it holds nothing this command needs and the process is about to exit. That is
	// the residue, and it is named rather than papered over: a CLI can bound its own
	// waiting, it cannot bound somebody else's Open.
	done := make(chan error, 1)
	go func() { done <- ps.Probe(ctx, cfg) }()

	var err error
	select {
	case err = <-done:
		defer ps.Discard()
	case <-ctx.Done():
		ps.Discard()
		return false, "the connector did not answer within --timeout (it was not wired, and any subprocess it started was killed)", ""
	}
	if err != nil {
		// Same genericization the live apply path uses (applyErrReason): the message
		// ran against the resolved config and may embed a secret value.
		if showDetail {
			detail = err.Error()
		}
		if errors.Is(err, runtime.ErrSourceOpenFailed) {
			return false, "the connector could not be opened with the supplied configuration", detail
		}
		// NOT the same fact as "it did not answer": the source DID open. Reported
		// separately so an operator does not go hunting a connectivity problem.
		return false, "the connector opened but did not shut down cleanly (the source answered)", detail
	}
	return true, "", ""
}

func writeSourceTest(out io.Writer, rep sourceTestReport) error {
	writeSourceCheck(out, rep.Check)
	if rep.Answered {
		fmt.Fprintf(out, "source %q (%s): ANSWERED — the connector opened with this configuration and was closed again\n", rep.Name, rep.Kind)
	} else {
		fmt.Fprintf(out, "source %q (%s): DID NOT ANSWER — %s\n", rep.Name, rep.Kind, rep.Reason)
	}
	if rep.Detail != "" {
		fmt.Fprintf(out, "  connector detail (--show-connector-error): %s\n", rep.Detail)
	}
	fmt.Fprintln(out, "NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.")
	return nil
}

// sourceKindLabel names a definition's connector the way `sources ls` does, so
// one source reads the same in both commands.
func sourceKindLabel(def model.SourceDef) string {
	if def.Plugin != nil {
		return "plugin:" + def.Plugin.Path
	}
	if def.Kind == "" {
		return "no kind"
	}
	return def.Kind
}
