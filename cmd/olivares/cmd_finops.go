// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"

	"github.com/spf13/cobra"
)

// finopsWindowFilters is the since/until window the spend and value reports read
// (modules/finops/dto.go timeWindow). It is declared ONCE because the same two
// parameters appear on nine routes and three near-copies of a flag pair is how
// two of them end up spelled differently.
//
// The engine REFUSES an unparseable timestamp rather than widening the window to
// all time (timeParam, dto.go:305-317), so a typo here comes back as a 400 and
// exits 2 — never as a silently larger number.
func finopsWindowFilters() []modelstackFilterSpec {
	return []modelstackFilterSpec{
		{Flag: "since", Query: "since", Usage: "start of the window, RFC3339 (e.g. 2026-08-01T00:00:00Z)"},
		{Flag: "until", Query: "until", Usage: "end of the window, RFC3339"},
	}
}

func finopsWindowAndDimension() []modelstackFilterSpec {
	return append(finopsWindowFilters(),
		modelstackFilterSpec{Flag: "dimension", Query: "dimension", Usage: "group by this dimension (e.g. provider, model, workspace)"})
}

// newFinOpsCmd wires the `finops` module (modules/finops/api.go) to the CLI:
// what the AI estate costs, what it is worth, the budgets that cap it and the
// cost centers it is charged to.
//
// The three write paths that INGEST (cost, seats, outcomes) answer 202 Accepted,
// not 200, and the CLI reports that status rather than flattening it to success:
// "accepted for processing" and "processed" are different facts to a script that
// polls for the result.
func newFinOpsCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{
		Use: "finops",
		// Deliberately no "cost" alias: `finops cost ingest` is a real path under
		// this group, and a top-level alias of the same word makes two different
		// invocations read the same.
		Short: "Report AI spend and value, and govern budgets, rates and cost centers",
		Long: "Report what the AI estate costs and what it returns, and govern the budgets, model\n" +
			"rates and cost centers that shape it.\n\n" +
			"Connection, credential and TLS values use the same resolution order and trust controls\n" +
			"as `auth`. Time windows are RFC3339 and the control plane refuses an unparseable one\n" +
			"rather than widening it, so a mistyped --since exits 2 instead of returning a bigger\n" +
			"number than you asked for.",
		Example: `  olivares finops spend summary --since 2026-08-01T00:00:00Z
  olivares finops budgets ls -o json
  olivares finops spend export --format focus > spend.csv`,
		Args: cobra.NoArgs,
	}
	flags.addPersistent(cmd)
	c := modelstackClient{flags: flags, base: finopsAPIBase, family: "finops"}
	cmd.AddCommand(
		newFinOpsSpendCmd(c),
		newFinOpsValueCmd(c),
		newFinOpsOutcomesCmd(c),
		newFinOpsSeatsCmd(c),
		newFinOpsCostIngestCmd(c),
		newFinOpsBudgetsCmd(c),
		newFinOpsAlertsCmd(c),
		newFinOpsCostCentersCmd(c),
		newFinOpsRatesCmd(c),
		newFinOpsStatementsCmd(c),
		newFinOpsForecastCmd(c),
		newFinOpsRecommendationsCmd(c),
		newFinOpsTeamSummaryCmd(c),
		newFinOpsComparisonCmd(c),
	)
	return cmd
}

// --- spend -------------------------------------------------------------------

func newFinOpsSpendCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spend",
		Short: "Report observed AI spend over a window",
		Long: "Report the spend the control plane observed over a time window: the raw series, a\n" +
			"summary, a trend, the reconciliation against provider invoices, the allocation to cost\n" +
			"centers, the unified cross-source view, and the FOCUS export.",
		Example: `  olivares finops spend summary --since 2026-08-01T00:00:00Z
  olivares finops spend ls --dimension provider -o json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "ls",
			Short:   "Show the spend series for a window",
			Long:    "Show observed spend over the window, optionally grouped by a dimension.",
			Example: `  olivares finops spend ls --dimension provider --since 2026-08-01T00:00:00Z`,
			Target:  modelstackTarget{Collection: "/spend"},
			Filters: finopsWindowAndDimension(),
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "summary",
			Short:   "Show the spend summary for a window",
			Long:    "Show the aggregate spend summary over the window: totals and their breakdown.",
			Example: `  olivares finops spend summary --since 2026-08-01T00:00:00Z -o json`,
			Target:  modelstackTarget{Collection: "/spend", Nested: "summary"},
			Filters: finopsWindowFilters(),
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "trend",
			Short:   "Show the spend trend over a window",
			Long:    "Show spend as a time series over the window, for a chart or a threshold check.",
			Example: `  olivares finops spend trend --since 2026-07-01T00:00:00Z -o json`,
			Target:  modelstackTarget{Collection: "/spend", Nested: "trend"},
			Filters: finopsWindowFilters(),
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "reconciliation",
			Short: "Compare observed spend against provider-reported cost",
			Long: "Compare what this control plane observed against what the provider reported, so a\n" +
				"gap is a measured number rather than a suspicion.",
			Example: `  olivares finops spend reconciliation --since 2026-08-01T00:00:00Z -o json`,
			Target:  modelstackTarget{Collection: "/spend", Nested: "reconciliation"},
			Filters: finopsWindowFilters(),
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "allocation",
			Short:   "Show how spend allocates to cost centers",
			Long:    "Show the allocation of observed spend to cost centers through their mapping rules.",
			Example: `  olivares finops spend allocation --since 2026-08-01T00:00:00Z -o json`,
			Target:  modelstackTarget{Collection: "/spend", Nested: "allocation"},
			Filters: finopsWindowFilters(),
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "unified",
			Short:   "Show the unified cross-source spend view",
			Long:    "Show spend unified across every ingested source, so one estate reads as one number.",
			Example: `  olivares finops spend unified --since 2026-08-01T00:00:00Z -o json`,
			Target:  modelstackTarget{Collection: "/spend", Nested: "unified"},
			Filters: finopsWindowFilters(),
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "export",
			Short: "Export spend in the FOCUS interchange format",
			Long: "Export the window's spend for an external FinOps tool. The control plane may answer\n" +
				"a media type that is not JSON; when it does, stdout carries the export VERBATIM so it\n" +
				"can be redirected to a file, and the media type is named on stderr.",
			Example: `  olivares finops spend export --since 2026-08-01T00:00:00Z --format focus
  olivares finops spend export --provenance measured -o json`,
			Target: modelstackTarget{Collection: "/spend", Nested: "export"},
			Filters: append(finopsWindowFilters(),
				modelstackFilterSpec{Flag: "format", Query: "format", Usage: "export format the module publishes (e.g. focus)"},
				modelstackFilterSpec{Flag: "provenance", Query: "provenance", Usage: "restrict to rows of this provenance"}),
			Raw: true,
		}),
	)
	return cmd
}

// --- value and outcomes ------------------------------------------------------

func newFinOpsValueCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "value",
		Short: "Report the value side of the unit economics",
		Long: "Report the value recorded against spend: the series and its summary, so cost per\n" +
			"outcome is a measurement rather than an estimate.",
		Example: `  olivares finops value summary --since 2026-08-01T00:00:00Z
  olivares finops value ls --dimension agent -o json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "ls",
			Short:   "Show the value series for a window",
			Long:    "Show recorded value over the window, optionally grouped by a dimension.",
			Example: `  olivares finops value ls --since 2026-08-01T00:00:00Z --dimension agent`,
			Target:  modelstackTarget{Collection: "/value"},
			Filters: finopsWindowAndDimension(),
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "summary",
			Short: "Show the value summary and cost-per-outcome",
			Long: "Show the aggregate value summary over the window, including cost per outcome and\n" +
				"cost per satisfied outcome where enough has been recorded to compute them.",
			Example: `  olivares finops value summary --since 2026-08-01T00:00:00Z -o json`,
			Target:  modelstackTarget{Collection: "/value", Nested: "summary"},
			Filters: finopsWindowAndDimension(),
		}),
	)
	return cmd
}

func newFinOpsOutcomesCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "outcomes",
		Short: "Record and read business outcomes attributed to AI work",
		Long: "Record the outcomes that give spend a denominator, and read back what has been\n" +
			"recorded, per subject.",
		Example: `  olivares finops outcomes ls
  olivares finops outcomes ingest --data @outcome.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List recorded outcomes",
			Long:      "List the recorded outcomes with their subject, verdict, value and source.",
			Example:   `  olivares finops outcomes ls --subject-kind agent -o json`,
			Target:    modelstackTarget{Collection: "/outcomes"},
			EmptyNote: "no outcomes recorded",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "subject-kind", Query: "subject_kind", Usage: "only outcomes whose subject is of this kind"},
				{Flag: "subject-ref", Query: "subject_ref", Usage: "only outcomes for this subject reference"},
			},
			Columns: []modelstackColumn{
				{Header: "SUBJECT", Key: "subject_ref"},
				{Header: "KIND", Key: "subject_kind"},
				{Header: "OUTCOME", Key: "outcome_ref"},
				{Header: "VERDICT", Key: "verdict"},
				{Header: "VALUE µUSD", Key: "value_micro_usd"},
				{Header: "AT", Key: "occurred_at"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "ingest",
			Short: "Record one business outcome",
			Long: "Record one outcome from a JSON document. The control plane answers 202 Accepted:\n" +
				"the record is taken, not yet aggregated into the value reports.",
			Example: `  olivares finops outcomes ingest --data @outcome.json
  cat outcome.json | olivares finops outcomes ingest --data -`,
			Method: http.MethodPost,
			Target: modelstackTarget{Collection: "/outcomes"},
			Body:   modelstackBodyRequired,
		}),
	)
	return cmd
}

func newFinOpsSeatsCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "seats",
		Short: "Record seat counts and read seat utilization",
		Long: "Record the seats a provider has assigned, and read the utilization that comes from\n" +
			"comparing them with observed activity — the number that finds seats nobody uses.",
		Example: `  olivares finops seats utilization
  olivares finops seats ingest --data @seats.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "ingest",
			Short: "Record a provider's seat counts for a day",
			Long: "Record assigned, premium and pending-invite seat counts for one provider and day,\n" +
				"from a JSON document. The control plane answers 202 Accepted.",
			Example: `  olivares finops seats ingest --data @seats.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/seats"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "utilization",
			Short:   "Show seat utilization",
			Long:    "Show how the recorded seats compare with observed activity, per provider.",
			Example: `  olivares finops seats utilization -o json`,
			Target:  modelstackTarget{Collection: "/seats", Nested: "utilization"},
		}),
	)
	return cmd
}

func newFinOpsCostIngestCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cost",
		Short: "Record an observed cost sample",
		Long: "Record one observed cost sample — tokens, cost, provider, model, and the workspace,\n" +
			"key, actor and provenance it is attributed to — from a JSON document.\n\n" +
			"This is the ingest path every spend report is built from. The control plane answers\n" +
			"202 Accepted: the sample is taken, not yet aggregated.",
		Example: `  olivares finops cost ingest --data @sample.json
  cat sample.json | olivares finops cost ingest --data -`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newModelstackWriteCmd(c, modelstackWriteSpec{
		Use:   "ingest",
		Short: "Record one observed cost sample",
		Long: "Record one cost sample from a JSON document. Fields the control plane does not\n" +
			"recognize are rejected with a 400 (exit 2) rather than silently dropped.",
		Example: `  olivares finops cost ingest --data @sample.json`,
		Method:  http.MethodPost,
		Target:  modelstackTarget{Collection: "/cost"},
		Body:    modelstackBodyRequired,
	}))
	return cmd
}

// --- budgets and alerts ------------------------------------------------------

func newFinOpsBudgetsCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "budgets",
		Short: "Govern spend budgets and read their status",
		Long: "Govern the budgets that cap spend, and read each one's live status against its cap.\n\n" +
			"An ENFORCING budget at its cap is what denies a routing resolve or execute: `models\n" +
			"routing resolve` then exits 5 with the action (block or throttle) named.",
		Example: `  olivares finops budgets ls
  olivares finops budgets status 018f2a10-0000-7000-8000-000000000009`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List budgets",
			Long:      "List this tenant's budgets with their enabled state; the full spec is in -o json.",
			Example:   `  olivares finops budgets ls --all -o json`,
			Target:    modelstackTarget{Collection: "/budgets"},
			EmptyNote: "no budgets defined",
			Paginated: true,
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "NAME", Key: "name"},
				{Header: "ENABLED", Key: "enabled"},
				{Header: "SCOPE", Key: "dimension"},
				{Header: "ACTION", Key: "action"},
			},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "get <budget-id>",
			Short:   "Show one budget",
			Long:    "Show one budget: its scope, period, cap and whether it enforces or only alerts.",
			Example: `  olivares finops budgets get 018f2a10-0000-7000-8000-000000000009`,
			Target:  modelstackTarget{Collection: "/budgets", IDs: 1},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "create",
			Short:   "Create a budget",
			Long:    "Create one budget from a JSON document. An enforcing budget can deny live traffic.",
			Example: `  olivares finops budgets create --data @budget.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/budgets"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "update <budget-id>",
			Short: "Replace a budget",
			Long: "Replace one budget with the supplied document. Full replacement, not a merge: a cap\n" +
				"omitted from the document is omitted from the budget.",
			Example: `  olivares finops budgets update 018f2a10-0000-7000-8000-000000000009 --data @budget.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/budgets", IDs: 1},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <budget-id>",
			Short: "Delete a budget",
			Long: "Delete one budget. If it was ENFORCING, the cap it applied to live routing is gone\n" +
				"from the moment it is deleted.",
			Example: `  olivares finops budgets rm 018f2a10-0000-7000-8000-000000000009 --yes`,
			Target:  modelstackTarget{Collection: "/budgets", IDs: 1},
			Noun:    "budget",
			Blast:   "an enforcing cap on live routing disappears with it",
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "status <budget-id>",
			Short: "Show one budget's live status against its cap",
			Long: "Show how much of one budget's cap the current period has consumed, and whether it\n" +
				"is at a threshold or at the cap.",
			Example: `  olivares finops budgets status 018f2a10-0000-7000-8000-000000000009 -o json`,
			Target:  modelstackTarget{Collection: "/budgets", IDs: 1, Nested: "status"},
		}),
	)
	return cmd
}

func newFinOpsAlertsCmd(c modelstackClient) *cobra.Command {
	return newModelstackListCmd(c, modelstackListSpec{
		Use:   "alerts",
		Short: "List budget threshold alerts",
		Long: "List the alerts budgets have raised: which budget, which dimension and key, which\n" +
			"threshold, and the spend against the limit when it fired.",
		Example: `  olivares finops alerts
  olivares finops alerts --budget-id 018f2a10-0000-7000-8000-000000000009 -o json`,
		Target:    modelstackTarget{Collection: "/alerts"},
		EmptyNote: "no budget alerts raised",
		Paginated: true,
		Filters: []modelstackFilterSpec{
			{Flag: "budget-id", Query: "budget_id", Usage: "only alerts raised by this budget"},
		},
		Columns: []modelstackColumn{
			{Header: "BUDGET", Key: "budget_id"},
			{Header: "DIMENSION", Key: "dimension"},
			{Header: "KEY", Key: "key"},
			{Header: "PERIOD", Key: "period"},
			{Header: "THRESHOLD %", Key: "threshold_pct"},
			{Header: "SPEND µUSD", Key: "spend_micro_usd"},
			{Header: "LIMIT µUSD", Key: "limit_micro_usd"},
			{Header: "SEVERITY", Key: "severity"},
		},
	})
}

// --- cost centers ------------------------------------------------------------

func newFinOpsCostCentersCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use: "cost-centers",
		// The alias below is the BRITISH spelling on purpose: the command name is US
		// (`.golangci.yml` sets `locale: US` and the API collection is `/cost-centers`),
		// and the alias is what lets an operator or a runbook typing the British form
		// still resolve. The misspell pass of 9e077afb5 rewrote the alias to the US
		// spelling too, which deleted the only thing it did and left it a duplicate of
		// its own name — silently: an alias equal to the command name registers nothing
		// new and nothing measured aliases. See TestNoAliasDuplicatesItsOwnCommandName.
		// misspell flags the literal itself, and it has to: the linter cannot tell an
		// accepted invocation from prose. Suppressing it belongs in `.golangci.yml`
		// beside `mitre` and `mosquitto`, which is the integration lane's call, not this lane's.
		Aliases: []string{"cost-centres"},
		Short:   "Govern cost centers and the rules that map spend to them",
		Long: "Govern the cost centers spend is charged to, and the mapping rules that decide which\n" +
			"observed spend lands on which center.",
		Example: `  olivares finops cost-centers ls
  olivares finops cost-centers mappings ls 018f2a10-0000-7000-8000-00000000000a`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List cost centers",
			Long:      "List the cost centers with their code, owner and status.",
			Example:   `  olivares finops cost-centers ls --status active -o json`,
			Target:    modelstackTarget{Collection: "/cost-centers"},
			EmptyNote: "no cost centers defined",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "status", Query: "status", Usage: "only cost centers in this status"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "CODE", Key: "code"},
				{Header: "NAME", Key: "name"},
				{Header: "OWNER", Key: "owner"},
				{Header: "STATUS", Key: "status"},
			},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "get <cost-center-id>",
			Short:   "Show one cost center",
			Long:    "Show one cost center with its metadata and timestamps.",
			Example: `  olivares finops cost-centers get 018f2a10-0000-7000-8000-00000000000a`,
			Target:  modelstackTarget{Collection: "/cost-centers", IDs: 1},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "create",
			Short:   "Create a cost center",
			Long:    "Create one cost center from a JSON document.",
			Example: `  olivares finops cost-centers create --data @cost-center.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/cost-centers"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "update <cost-center-id>",
			Short:   "Replace a cost center",
			Long:    "Replace one cost center with the supplied document. Full replacement, not a merge.",
			Example: `  olivares finops cost-centers update 018f2a10-0000-7000-8000-00000000000a --data @cost-center.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/cost-centers", IDs: 1},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <cost-center-id>",
			Short: "Delete a cost center",
			Long: "Delete one cost center. Spend that mapped to it stops being allocated, and past\n" +
				"statements keep the center they were generated against.",
			Example: `  olivares finops cost-centers rm 018f2a10-0000-7000-8000-00000000000a --yes`,
			Target:  modelstackTarget{Collection: "/cost-centers", IDs: 1},
			Noun:    "cost center",
			Blast:   "spend that mapped to it stops being allocated",
		}),
		newFinOpsCostCenterMappingsCmd(c),
	)
	return cmd
}

func newFinOpsCostCenterMappingsCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mappings",
		Short: "Govern the rules that map spend onto one cost center",
		Long: "Govern the mapping rules of one cost center: which source dimension and key it\n" +
			"claims, and in what priority order.",
		Example: `  olivares finops cost-centers mappings ls 018f2a10-0000-7000-8000-00000000000a
  olivares finops cost-centers mappings add 018f2a10-0000-7000-8000-00000000000a --data @mapping.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:     "ls <cost-center-id>",
			Aliases: []string{"list"},
			Short:   "List one cost centre's mapping rules",
			Long: "List the mapping rules of one cost center, in priority order.\n\n" +
				"This route takes NO cursor or limit: the control plane serves it under a fixed\n" +
				"server-side cap (modules/finops/costcenter.go:306-309). The flags are therefore not\n" +
				"offered, and a capped page says so rather than looking complete.",
			Example:   `  olivares finops cost-centers mappings ls 018f2a10-0000-7000-8000-00000000000a -o json`,
			Target:    modelstackTarget{Collection: "/cost-centers", Nested: "mappings", IDs: 1},
			EmptyNote: "no mapping rules on this cost center",
			Paginated: false,
			CapNote:   "the control plane caps this list server-side and does not page it; narrow it by cost center",
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "DIMENSION", Key: "source_dimension"},
				{Header: "KEY", Key: "source_key"},
				{Header: "PRIORITY", Key: "priority"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "add <cost-center-id>",
			Short:   "Add a mapping rule to a cost center",
			Long:    "Add one mapping rule to a cost center from a JSON document.",
			Example: `  olivares finops cost-centers mappings add 018f2a10-0000-7000-8000-00000000000a --data @mapping.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/cost-centers", Nested: "mappings", IDs: 1},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <cost-center-id> <mapping-id>",
			Short: "Remove a mapping rule from a cost center",
			Long: "Remove one mapping rule. It takes TWO identifiers — the cost center and the rule —\n" +
				"because the route addresses the rule within its center; both are escaped as single\n" +
				"path segments.",
			Example: `  olivares finops cost-centers mappings rm 018f2a10-0000-7000-8000-00000000000a 018f2a10-0000-7000-8000-00000000000b --yes`,
			Target:  modelstackTarget{Collection: "/cost-centers", Nested: "mappings", IDs: 2},
			Noun:    "cost-center mapping",
			Blast:   "spend matching this rule stops being charged to that center",
		}),
	)
	return cmd
}

// --- rate catalog ----------------------------------------------------------

func newFinOpsRatesCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rates",
		Aliases: []string{"model-rates"},
		Short:   "Govern the model rate catalog used to price usage",
		Long: "Govern the per-model rates the control plane prices observed usage with: input,\n" +
			"output, cache-read and cache-creation rates, and the window each rate is effective in.\n\n" +
			"These identifiers are the SAME model references `models ls` reports.",
		Example: `  olivares finops rates ls --provider anthropic
  olivares finops rates create --data @rate.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List model rates",
			Long:      "List the rate catalog with each rate's effective window.",
			Example:   `  olivares finops rates ls --model claude-opus-4 -o json`,
			Target:    modelstackTarget{Collection: "/model-rates"},
			EmptyNote: "no model rates defined",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "provider", Query: "provider", Usage: "only rates for this provider"},
				{Flag: "model", Query: "model", Usage: "only rates for this model reference"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "PROVIDER", Key: "provider"},
				{Header: "MODEL", Key: "model"},
				{Header: "IN µUSD", Key: "input_rate_micro_usd"},
				{Header: "OUT µUSD", Key: "output_rate_micro_usd"},
				{Header: "FROM", Key: "effective_from"},
				{Header: "UNTIL", Key: "effective_until"},
			},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "get <rate-id>",
			Short:   "Show one model rate",
			Long:    "Show one rate with all four price components and its effective window.",
			Example: `  olivares finops rates get 018f2a10-0000-7000-8000-00000000000c`,
			Target:  modelstackTarget{Collection: "/model-rates", IDs: 1},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "create",
			Short: "Add a model rate",
			Long: "Add one rate from a JSON document. An overlapping effective window for the same\n" +
				"provider and model is refused with 409 (exit 5), so pricing never becomes ambiguous.",
			Example: `  olivares finops rates create --data @rate.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/model-rates"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "update <rate-id>",
			Short: "Replace a model rate",
			Long: "Replace one rate with the supplied document. Repricing changes what future reports\n" +
				"compute; it does not rewrite samples already priced.",
			Example: `  olivares finops rates update 018f2a10-0000-7000-8000-00000000000c --data @rate.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/model-rates", IDs: 1},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <rate-id>",
			Short: "Delete a model rate",
			Long: "Delete one rate. Usage in its window loses the price it was computed with, so a\n" +
				"later report over that window can come back with a different number.",
			Example: `  olivares finops rates rm 018f2a10-0000-7000-8000-00000000000c --yes`,
			Target:  modelstackTarget{Collection: "/model-rates", IDs: 1},
			Noun:    "model rate",
			Blast:   "usage in its window loses the price it was computed with",
		}),
	)
	return cmd
}

// --- statements --------------------------------------------------------------

func newFinOpsStatementsCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "statements",
		Short: "Generate, read and export per-cost-center statements",
		Long: "Generate the periodic statements charged to each cost center, list and read them, and\n" +
			"export one for an accounting system.",
		Example: `  olivares finops statements ls --period monthly
  olivares finops statements generate --data @period.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "generate",
			Short: "Generate statements for a period",
			Long: "Generate the statements for one period from a JSON document naming the period\n" +
				"(monthly or weekly) and its RFC3339 start. The control plane refuses any other\n" +
				"period or an unparseable start with a 400, which exits 2.",
			Example: `  olivares finops statements generate --data '{"period":"monthly","period_start":"2026-08-01T00:00:00Z"}'`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/statements", Nested: "generate"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List generated statements",
			Long:      "List the generated statements with their cost center, period, total and status.",
			Example:   `  olivares finops statements ls --status final -o json`,
			Target:    modelstackTarget{Collection: "/statements"},
			EmptyNote: "no statements generated",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "cost-center-id", Query: "cost_center_id", Usage: "only statements for this cost center"},
				{Flag: "period", Query: "period", Usage: "only statements of this period kind (monthly or weekly)"},
				{Flag: "status", Query: "status", Usage: "only statements in this status"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "CENTER", Key: "cost_center_code"},
				{Header: "PERIOD", Key: "period"},
				{Header: "START", Key: "period_start"},
				{Header: "TOTAL µUSD", Key: "total_micro_usd"},
				{Header: "LINES", Key: "line_count"},
				{Header: "STATUS", Key: "status"},
			},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "get <statement-id>",
			Short:   "Show one statement with its lines",
			Long:    "Show one statement: its period, totals, delta against the prior period and its lines.",
			Example: `  olivares finops statements get 018f2a10-0000-7000-8000-00000000000d -o json`,
			Target:  modelstackTarget{Collection: "/statements", IDs: 1},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "export <statement-id>",
			Short: "Export one statement",
			Long: "Export one statement for an accounting system. When the control plane answers a\n" +
				"media type that is not JSON, stdout carries it VERBATIM so it can be redirected, and\n" +
				"the media type is named on stderr.",
			Example: `  olivares finops statements export 018f2a10-0000-7000-8000-00000000000d > statement.csv`,
			Target:  modelstackTarget{Collection: "/statements", IDs: 1, Nested: "export"},
			Raw:     true,
		}),
	)
	return cmd
}

// --- analysis ----------------------------------------------------------------

func newFinOpsForecastCmd(c modelstackClient) *cobra.Command {
	return newModelstackGetCmd(c, modelstackGetSpec{
		Use:   "forecast",
		Short: "Forecast spend from the observed history",
		Long: "Project spend forward from what has been observed, over a window of history and for\n" +
			"a period, optionally per dimension.",
		Example: `  olivares finops forecast --period monthly --window-days 90
  olivares finops forecast --dimension provider -o json`,
		Target: modelstackTarget{Collection: "/forecast"},
		Filters: []modelstackFilterSpec{
			{Flag: "dimension", Query: "dimension", Usage: "forecast per this dimension"},
			{Flag: "period", Query: "period", Usage: "forecast period (e.g. monthly)"},
			{Flag: "window-days", Query: "window_days", Usage: "days of history the projection is built from"},
		},
	})
}

func newFinOpsRecommendationsCmd(c modelstackClient) *cobra.Command {
	return newModelstackGetCmd(c, modelstackGetSpec{
		Use:   "recommendations",
		Short: "Show cost-reduction recommendations",
		Long: "Show the recommendations the control plane derives from observed spend: the changes\n" +
			"that would reduce it, each with the evidence it was derived from.",
		Example: `  olivares finops recommendations
  olivares finops recommendations -o json`,
		Target: modelstackTarget{Collection: "/recommendations"},
	})
}

func newFinOpsTeamSummaryCmd(c modelstackClient) *cobra.Command {
	return newModelstackGetCmd(c, modelstackGetSpec{
		Use:     "team-summary",
		Short:   "Show the per-team spend summary",
		Long:    "Show spend summarized per team for a period, for a chargeback conversation.",
		Example: `  olivares finops team-summary --period monthly -o json`,
		Target:  modelstackTarget{Collection: "/analytics", Nested: "team-summary"},
		Filters: []modelstackFilterSpec{
			{Flag: "period", Query: "period", Usage: "summary period (e.g. monthly)"},
		},
	})
}

func newFinOpsComparisonCmd(c modelstackClient) *cobra.Command {
	return newModelstackGetCmd(c, modelstackGetSpec{
		Use:   "comparison",
		Short: "Compare what a workload would cost on other models",
		Long: "Compare the observed cost of a source model against what the same workload would have\n" +
			"cost on candidate models, over a window.",
		Example: `  olivares finops comparison --source-model claude-opus-4 --target-models claude-sonnet-4
  olivares finops comparison --dimension provider --window-days 30 -o json`,
		Target: modelstackTarget{Collection: "/comparison"},
		Filters: append(finopsWindowFilters(),
			modelstackFilterSpec{Flag: "source-model", Query: "source_model", Usage: "the model the observed workload ran on"},
			modelstackFilterSpec{Flag: "target-models", Query: "target_models", Usage: "candidate models to price the same workload against"},
			modelstackFilterSpec{Flag: "dimension", Query: "dimension", Usage: "restrict the comparison to this dimension"},
			modelstackFilterSpec{Flag: "dim-key", Query: "dim_key", Usage: "restrict the comparison to this key within the dimension"},
			modelstackFilterSpec{Flag: "window-days", Query: "window_days", Usage: "days of history to compare over"},
			modelstackFilterSpec{Flag: "forecast-period", Query: "forecast_period", Usage: "period to project the saving over"}),
	})
}
