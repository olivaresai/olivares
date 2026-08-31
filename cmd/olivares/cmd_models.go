// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"

	"github.com/spf13/cobra"
)

// newModelsCmd wires the `models` module (modules/models/api.go) to the CLI:
// the governed model estate, the routing policies that select within it, the
// own-model registry and its supply-chain evidence, and the access governance
// that decides who may use which model.
//
// PAGINATION IS DECLARED WHERE IT WAS MEASURED, not where it was assumed. Each
// --limit/--cursor pair below corresponds to a handler that calls listQuery
// (modules/models/dto.go:173); the routes that do not read those parameters do
// not offer the flags, because a flag the engine ignores tells the operator a
// lie about their own result.
func newModelsCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Govern the model estate, routing, registry and model access",
		Long: "Govern the models this control plane knows about: the live estate and the declared\n" +
			"reference catalog, routing policies and their resolution, the own-model registry with\n" +
			"its versions, datasets and AIBOM evidence, and the access grants that decide who may\n" +
			"use what.\n\n" +
			"Connection, credential and TLS values use the same resolution order and trust controls\n" +
			"as `auth`. Every verb carries the caller's credential and the tenant header and takes NO\n" +
			"authorization decision of its own: the engine's permission checks are the only ones.\n\n" +
			"Exit codes follow the contract in `olivares --help`. Two are worth naming here: a\n" +
			"routing resolve or execute denied by an enforcing budget exits 5 (conflict), not 6 —\n" +
			"the control plane is healthy and refusing — and a rejected --data document exits 2.",
		Example: `  olivares models ls
  olivares models routing ls -o json
  olivares models --server https://plane.example.com --tenant tenant-a owned ls`,
		Args: cobra.NoArgs,
	}
	flags.addPersistent(cmd)
	c := modelstackClient{flags: flags, base: modelsAPIBase, family: "models"}
	cmd.AddCommand(newModelsEstateListCmd(c), newModelsEstateGetCmd(c))
	cmd.AddCommand(newModelsReferenceCmds(c)...)
	cmd.AddCommand(
		newModelsRoutingCmd(c),
		newModelsKeysCmd(c),
		newModelsResidencyCmd(c),
		newModelsEntitlementsCmd(c),
		newModelsOwnedCmd(c),
		newModelsVersionsCmd(c),
		newModelsDeploymentsCmd(c),
		newModelsFinetuneCmd(c),
		newModelsGPAICmd(c),
		newModelsAdmissionCmd(c),
		newModelsDatasetsCmd(c),
		newModelsAIBOMCmd(c),
		newModelsAgentArtifactsCmd(c),
		newModelsGroupsCmd(c),
		newModelsAccessCmd(c),
	)
	return cmd
}

// --- the governed estate -----------------------------------------------------

func newModelsEstateListCmd(c modelstackClient) *cobra.Command {
	return newModelstackListCmd(c, modelstackListSpec{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the governed model estate",
		Long: "List the models this tenant governs, each enriched with its provider name, context\n" +
			"window, list pricing and declared capabilities. This is the LIVE estate; `models catalog`\n" +
			"is the declared reference table it is enriched from.",
		Example: `  olivares models ls
  olivares models ls --limit 50 -o json
  olivares models ls --all -o json`,
		Target:    modelstackTarget{Collection: "/models"},
		EmptyNote: "no governed models in this tenant",
		Paginated: true,
		Columns: []modelstackColumn{
			{Header: "ID", Key: "id"},
			{Header: "NAME", Key: "name"},
			{Header: "PROVIDER", Key: "provider"},
			{Header: "FAMILY", Key: "family"},
			{Header: "STATUS", Key: "status"},
			{Header: "CONTEXT", Key: "context_window"},
		},
	})
}

func newModelsEstateGetCmd(c modelstackClient) *cobra.Command {
	return newModelstackGetCmd(c, modelstackGetSpec{
		Use:   "get <model-id>",
		Short: "Show one governed model",
		Long: "Show one governed model with its provider reference, pricing, modality and the\n" +
			"capabilities the declared catalog attributes to its family.",
		Example: `  olivares models get 018f2a10-0000-7000-8000-000000000001
  olivares models get 018f2a10-0000-7000-8000-000000000001 -o json`,
		Target: modelstackTarget{Collection: "/models", IDs: 1},
	})
}

// newModelsReferenceCmds are the read-only declared references: reference data
// the engine serves so a console, a script and this CLI all read the same table
// instead of three copies.
func newModelsReferenceCmds(c modelstackClient) []*cobra.Command {
	return []*cobra.Command{
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "catalog",
			Short: "Show the declared reference catalog (capabilities and list pricing)",
			Long: "Show the declared capability/feature matrix and list pricing per model family, with\n" +
				"the date the pricing was recorded. It is governance REFERENCE data, distinct from the\n" +
				"governed estate `models ls` reports.",
			Example: `  olivares models catalog -o json
  olivares models catalog`,
			Target: modelstackTarget{Collection: "/catalog"},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "features",
			Short: "Show which model families declare each API capability",
			Long: "Show the capability matrix: for every API feature (streaming, tool use, vision, PDF,\n" +
				"prompt caching, batch, extended thinking, …), which declared families support it.",
			Example: `  olivares models features
  olivares models features -o json`,
			Target: modelstackTarget{Collection: "/features"},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "data-governance",
			Short: "Show the context-management / memory / ZDR matrix",
			Long: "Show, per feature, whether it can clear the model's working context server-side (a\n" +
				"forensics implication), where its data persists, and whether it is ZDR-eligible (a\n" +
				"data-residency concern).",
			Example: `  olivares models data-governance
  olivares models data-governance -o json`,
			Target: modelstackTarget{Collection: "/data-governance"},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "tool-types",
			Short: "Show the dated tool-type catalog and its cost cross-walk",
			Long: "Show the declared dated tool-type identifiers, their execution surface, ZDR\n" +
				"eligibility and cost_type cross-walk. Identifiers change quarterly: the response\n" +
				"carries the as-of date it was recorded.",
			Example: `  olivares models tool-types
  olivares models tool-types -o json`,
			Target: modelstackTarget{Collection: "/tool-types"},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "rate-limits",
			Short: "Show the provider rate-limit inventory a gateway must mirror",
			Long: "Show the read-only rate-limit inventory per workspace and model group. When the\n" +
				"provider's Admin-API connector is not wired the response is empty and carries the\n" +
				"reason: an empty inventory is not the same fact as no limits.",
			Example: `  olivares models rate-limits
  olivares models rate-limits -o json`,
			Target: modelstackTarget{Collection: "/rate-limits"},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "platforms",
			Short: "Show the deployment-surface matrix and per-platform lifecycle",
			Long: "Show the declared deployment-surface matrix and per-platform model lifecycle\n" +
				"reference. It degrades to available=false with a reason when no reference provider is\n" +
				"wired, rather than reporting an empty matrix as a complete one.",
			Example: `  olivares models platforms
  olivares models platforms -o json`,
			Target: modelstackTarget{Collection: "/platforms"},
		}),
	}
}

// --- routing -----------------------------------------------------------------

func newModelsRoutingCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "routing",
		Short: "Author routing policies and resolve or execute them",
		Long: "Author the policies that select a model, resolve one against the governed estate to\n" +
			"see the decision the gateway would take, or execute it through the governed executor.\n\n" +
			"resolve is read-tier and performs no inference; execute is admin-tier and SPENDS.",
		Example: `  olivares models routing ls
  olivares models routing resolve pol-1 -o json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:     "ls",
			Aliases: []string{"list"},
			Short:   "List routing policies",
			Long: "List this tenant's routing policies with their enabled state. The policy spec (\n" +
				"strategy, candidates, fallbacks, lifecycle guards) is carried in -o json.",
			Example: `  olivares models routing ls
  olivares models routing ls --all -o json`,
			Target:    modelstackTarget{Collection: "/routing-policies"},
			EmptyNote: "no routing policies in this tenant",
			Paginated: true,
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "NAME", Key: "name"},
				{Header: "ENABLED", Key: "enabled"},
				{Header: "STRATEGY", Key: "strategy"},
			},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "get <policy-id>",
			Short:   "Show one routing policy",
			Long:    "Show one routing policy: its name, enabled state and full selection spec.",
			Example: `  olivares models routing get 018f2a10-0000-7000-8000-000000000001`,
			Target:  modelstackTarget{Collection: "/routing-policies", IDs: 1},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "create",
			Short: "Create a routing policy",
			Long: "Create a routing policy from a JSON document. The document is the policy DTO the\n" +
				"engine publishes (name, enabled, and the selection spec); it is sent unmodified.",
			Example: `  olivares models routing create --data @policy.json
  cat policy.json | olivares models routing create --data -`,
			Method: http.MethodPost,
			Target: modelstackTarget{Collection: "/routing-policies"},
			Body:   modelstackBodyRequired,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "update <policy-id>",
			Short: "Replace a routing policy in place",
			Long: "Replace one routing policy with the supplied document. This is a full replacement,\n" +
				"not a merge: fields absent from the document are absent from the stored policy.",
			Example: `  olivares models routing update 018f2a10-0000-7000-8000-000000000001 --data @policy.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/routing-policies", IDs: 1},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <policy-id>",
			Short: "Delete a routing policy",
			Long: "Delete one routing policy. Traffic selected by it stops being selected by it: this\n" +
				"changes live routing, so it requires a confirmation or --yes.",
			Example: `  olivares models routing rm 018f2a10-0000-7000-8000-000000000001 --yes`,
			Target:  modelstackTarget{Collection: "/routing-policies", IDs: 1},
			Noun:    "routing policy",
			Blast:   "live traffic selected by this policy will be routed by whatever remains",
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "resolve <policy-id>",
			Short: "Resolve a policy to the routing decision it would produce",
			Long: "Resolve one policy against the governed estate and print the decision: the primary\n" +
				"target, the fallback chain, and — when nothing is selectable — why. A governance deny\n" +
				"exits 3 and names the deny kind; an enforcing budget at its cap exits 5. It performs\n" +
				"NO inference and spends nothing.",
			Example: `  olivares models routing resolve 018f2a10-0000-7000-8000-000000000001 -o json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/routing-policies", IDs: 1, Nested: "resolve"},
			Body:    modelstackBodyNone,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "execute <policy-id>",
			Short: "Execute a routing policy through the governed executor (SPENDS)",
			Long: "Resolve the policy and act on the decision through the governed executor, emitting a\n" +
				"cost sample. This is an admin-tier spend, not a preview. When no executor is\n" +
				"provisioned the route is deny-closed and answers 503, which exits 6 and says so.",
			Example: `  olivares models routing execute 018f2a10-0000-7000-8000-000000000001 --data @request.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/routing-policies", IDs: 1, Nested: "execute"},
			Body:    modelstackBodyRequired,
		}),
	)
	return cmd
}

// --- provider keys and workspace governance ----------------------------------

func newModelsKeysCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Govern provider API-key and workspace references",
		Long: "Govern the references to provider API keys and workspaces. These are minimal-data\n" +
			"REFERENCES — an external id, a name, an owner, a status and a hint. No secret value is\n" +
			"stored here or printed by these verbs.",
		Example: `  olivares models keys ls
  olivares models keys ls --status active -o json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:     "ls",
			Aliases: []string{"list"},
			Short:   "List provider key and workspace references",
			Long: "List the governed provider key/workspace references. Values are never included:\n" +
				"the hint is what the provider publishes, not the credential.",
			Example: `  olivares models keys ls --provider-ref anthropic
  olivares models keys ls --ref-kind workspace -o json`,
			Target:    modelstackTarget{Collection: "/keys"},
			EmptyNote: "no provider key or workspace references registered",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "provider-ref", Query: "provider_ref", Usage: "only references for this provider"},
				{Flag: "ref-kind", Query: "ref_kind", Usage: "only references of this kind (e.g. api_key, workspace)"},
				{Flag: "status", Query: "status", Usage: "only references in this status"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "KIND", Key: "ref_kind"},
				{Header: "PROVIDER", Key: "provider_ref"},
				{Header: "NAME", Key: "name"},
				{Header: "WORKSPACE", Key: "workspace_ref"},
				{Header: "STATUS", Key: "status"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "create",
			Short:   "Register a provider key or workspace reference",
			Long:    "Register one reference from a JSON document. The document carries no secret value.",
			Example: `  olivares models keys create --data @key-ref.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/keys"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "update <ref-id>",
			Short:   "Replace a key or workspace reference",
			Long:    "Replace one reference with the supplied document. Full replacement, not a merge.",
			Example: `  olivares models keys update 018f2a10-0000-7000-8000-000000000001 --data @key-ref.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/keys", IDs: 1},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <ref-id>",
			Short: "Remove a key or workspace reference",
			Long: "Remove one governed reference. The provider-side credential is not touched: this\n" +
				"removes the control plane's knowledge of it, and with it the governance that used it.",
			Example: `  olivares models keys rm 018f2a10-0000-7000-8000-000000000001 --yes`,
			Target:  modelstackTarget{Collection: "/keys", IDs: 1},
			Noun:    "provider key reference",
			Blast:   "governance that scopes this reference stops applying",
		}),
	)
	return cmd
}

func newModelsResidencyCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "residency",
		Short: "Govern per-workspace inference-geo residency",
		Long: "Read and set the permitted inference geographies per workspace — the PERMITTED side\n" +
			"of the compliance geo-drift scan.",
		Example: `  olivares models residency ls
  olivares models residency set --data @residency.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:     "ls",
			Aliases: []string{"list"},
			Short:   "List per-workspace residency records",
			Long:    "List the declared inference-geo residency per workspace, with the date it was recorded.",
			Example: `  olivares models residency ls
  olivares models residency ls --workspace-ref ws-1 -o json`,
			Target:    modelstackTarget{Collection: "/workspace-residency"},
			EmptyNote: "no workspace residency declared",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "workspace-ref", Query: "workspace_ref", Usage: "only this workspace"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "WORKSPACE", Key: "workspace_ref"},
				{Header: "ALLOWED", Key: "allowed_geos"},
				{Header: "DEFAULT", Key: "default_geo"},
				{Header: "OBSERVED", Key: "workspace_geo"},
				{Header: "AS OF", Key: "as_of"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "set",
			Short: "Declare a workspace's permitted inference geographies",
			Long: "Upsert the residency record for one workspace from a JSON document. This declares\n" +
				"what is PERMITTED; the drift scan compares it against what is observed.",
			Example: `  olivares models residency set --data @residency.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/workspace-residency"},
			Body:    modelstackBodyRequired,
		}),
	)
	return cmd
}

func newModelsEntitlementsCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "entitlements",
		Short: "Attest provider entitlement state for restricted access tiers",
		Long: "Read and attest the operator-declared entitlement state for restricted provider\n" +
			"access tiers. A suspended tier is what makes the routing chain drop its candidates.",
		Example: `  olivares models entitlements ls
  olivares models entitlements set --data @tier.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List access-tier entitlement attestations",
			Long:      "List the attested entitlement state per restricted access tier, with who attested it.",
			Example:   `  olivares models entitlements ls --state suspended`,
			Target:    modelstackTarget{Collection: "/access-tier-entitlements"},
			EmptyNote: "no access-tier entitlements attested",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "tier", Query: "tier", Usage: "only this access tier"},
				{Flag: "state", Query: "state", Usage: "only entitlements in this state"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "TIER", Key: "tier"},
				{Header: "STATE", Key: "state"},
				{Header: "AS OF", Key: "as_of"},
				{Header: "BY", Key: "updated_by"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "set",
			Short:   "Attest the entitlement state of one access tier",
			Long:    "Upsert one access-tier entitlement attestation from a JSON document.",
			Example: `  olivares models entitlements set --data @tier.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/access-tier-entitlements"},
			Body:    modelstackBodyRequired,
		}),
	)
	return cmd
}

// --- own-model registry ------------------------------------------------------

func newModelsOwnedCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "owned",
		Short: "Govern the own-model registry",
		Long: "Govern the models this organization owns or fine-tunes: the registry entry, its\n" +
			"visibility and status. Versions, deployments and evidence hang off these entries.",
		Example: `  olivares models owned ls
  olivares models owned get 018f2a10-0000-7000-8000-000000000001`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List owned models",
			Long:      "List the registered own-models with their kind, base reference, visibility and status.",
			Example:   `  olivares models owned ls --status active -o json`,
			Target:    modelstackTarget{Collection: "/owned-models"},
			EmptyNote: "no owned models registered",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "kind", Query: "kind", Usage: "only models of this kind"},
				{Flag: "status", Query: "status", Usage: "only models in this status"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "NAME", Key: "name"},
				{Header: "KIND", Key: "kind"},
				{Header: "BASE", Key: "base_ref"},
				{Header: "VISIBILITY", Key: "visibility"},
				{Header: "STATUS", Key: "status"},
			},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "get <owned-id>",
			Short:   "Show one owned model",
			Long:    "Show one own-model registry entry with its provider, owner and governance note.",
			Example: `  olivares models owned get 018f2a10-0000-7000-8000-000000000001`,
			Target:  modelstackTarget{Collection: "/owned-models", IDs: 1},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "create",
			Short:   "Register an owned model",
			Long:    "Register one own-model from a JSON document. Registering governs it; it trains nothing.",
			Example: `  olivares models owned create --data @owned.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/owned-models"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "update <owned-id>",
			Short:   "Replace an owned-model entry",
			Long:    "Replace one own-model entry with the supplied document. Full replacement, not a merge.",
			Example: `  olivares models owned update 018f2a10-0000-7000-8000-000000000001 --data @owned.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/owned-models", IDs: 1},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <owned-id>",
			Short: "Remove an owned model from the registry",
			Long: "Remove one own-model entry. Its versions, deployments and sealed evidence reference\n" +
				"it, so this is the widest-blast delete in the registry.",
			Example: `  olivares models owned rm 018f2a10-0000-7000-8000-000000000001 --yes`,
			Target:  modelstackTarget{Collection: "/owned-models", IDs: 1},
			Noun:    "owned model",
			Blast:   "versions, deployments and AIBOM evidence reference this entry",
		}),
	)
	return cmd
}

func newModelsVersionsCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "versions",
		Short: "Govern owned-model versions and their signed admission",
		Long: "Register and retire the versions of an owned model, and run the deny-closed\n" +
			"verify-before-admit ceremony that records a signature verdict against one.",
		Example: `  olivares models versions ls --owned-ref 018f2a10-0000-7000-8000-000000000001
  olivares models versions admit 018f2a10-0000-7000-8000-000000000002 --data @admit.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List owned-model versions",
			Long:      "List registered versions with their artifact reference, status and lineage parent.",
			Example:   `  olivares models versions ls --status published -o json`,
			Target:    modelstackTarget{Collection: "/model-versions"},
			EmptyNote: "no model versions registered",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "owned-ref", Query: "owned_ref", Usage: "only versions of this owned model"},
				{Flag: "status", Query: "status", Usage: "only versions in this status"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "OWNED", Key: "owned_ref"},
				{Header: "VERSION", Key: "version"},
				{Header: "ARTIFACT", Key: "artifact_ref"},
				{Header: "STATUS", Key: "status"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "create",
			Short:   "Register an owned-model version",
			Long:    "Register one version of an owned model from a JSON document.",
			Example: `  olivares models versions create --data @version.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/model-versions"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "admit <version-id>",
			Short: "Run the signed-model admission ceremony against a version",
			Long: "Verify a version's signature against this tenant's admission trust root and record\n" +
				"the verdict. The gate is deny-closed: a document that does not verify produces a\n" +
				"recorded refusal, not an admission.",
			Example: `  olivares models versions admit 018f2a10-0000-7000-8000-000000000002 --data @admit.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/model-versions", IDs: 1, Nested: "admit"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <version-id>",
			Short: "Remove an owned-model version",
			Long: "Remove one registered version. A deployment or a routing target that names this\n" +
				"version stops resolving to it, so this can change live traffic.",
			Example: `  olivares models versions rm 018f2a10-0000-7000-8000-000000000002 --yes`,
			Target:  modelstackTarget{Collection: "/model-versions", IDs: 1},
			Noun:    "model version",
			Blast:   "deployments and routing targets naming this version stop resolving",
		}),
	)
	return cmd
}

func newModelsDeploymentsCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deployments",
		Short: "Govern local inference deployments",
		Long: "Govern the inference deployments that serve owned models: their runtime, endpoint\n" +
			"reference, status and whether they are governed by this control plane.",
		Example: `  olivares models deployments ls
  olivares models deployments ls --runtime vllm -o json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List inference deployments",
			Long:      "List the registered inference deployments with runtime, endpoint and status.",
			Example:   `  olivares models deployments ls --status running`,
			Target:    modelstackTarget{Collection: "/inference-deployments"},
			EmptyNote: "no inference deployments registered",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "runtime", Query: "runtime", Usage: "only deployments on this runtime"},
				{Flag: "status", Query: "status", Usage: "only deployments in this status"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "NAME", Key: "name"},
				{Header: "RUNTIME", Key: "runtime"},
				{Header: "TYPE", Key: "deployment_type"},
				{Header: "VERSION", Key: "version_ref"},
				{Header: "STATUS", Key: "status"},
				{Header: "GOVERNED", Key: "governed"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "create",
			Short:   "Register an inference deployment",
			Long:    "Register one inference deployment from a JSON document.",
			Example: `  olivares models deployments create --data @deployment.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/inference-deployments"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "update <deployment-id>",
			Short:   "Replace an inference deployment",
			Long:    "Replace one deployment with the supplied document. Full replacement, not a merge.",
			Example: `  olivares models deployments update 018f2a10-0000-7000-8000-000000000003 --data @deployment.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/inference-deployments", IDs: 1},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:     "rm <deployment-id>",
			Short:   "Remove an inference deployment",
			Long:    "Remove one deployment record. Traffic pinned to its endpoint stops being governed.",
			Example: `  olivares models deployments rm 018f2a10-0000-7000-8000-000000000003 --yes`,
			Target:  modelstackTarget{Collection: "/inference-deployments", IDs: 1},
			Noun:    "inference deployment",
			Blast:   "traffic pinned to this endpoint stops being governed",
		}),
	)
	return cmd
}

func newModelsFinetuneCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "finetune",
		Short: "Record fine-tune jobs and their outcome",
		Long: "Record the fine-tune jobs this organization runs: base model, dataset, runtime,\n" +
			"status and the version they produced. The control plane INVENTORIES these jobs; it\n" +
			"does not run them.",
		Example: `  olivares models finetune ls
  olivares models finetune get 018f2a10-0000-7000-8000-000000000004`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List fine-tune job records",
			Long:      "List recorded fine-tune jobs with their base model, dataset, runtime and status.",
			Example:   `  olivares models finetune ls --status running -o json`,
			Target:    modelstackTarget{Collection: "/finetune-jobs"},
			EmptyNote: "no fine-tune jobs recorded",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "status", Query: "status", Usage: "only jobs in this status"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "NAME", Key: "name"},
				{Header: "BASE", Key: "base_ref"},
				{Header: "DATASET", Key: "dataset_ref"},
				{Header: "RUNTIME", Key: "runtime"},
				{Header: "STATUS", Key: "status"},
				{Header: "RESULT", Key: "result_version_ref"},
			},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "get <job-id>",
			Short:   "Show one fine-tune job record",
			Long:    "Show one recorded fine-tune job with its timestamps and resulting version reference.",
			Example: `  olivares models finetune get 018f2a10-0000-7000-8000-000000000004`,
			Target:  modelstackTarget{Collection: "/finetune-jobs", IDs: 1},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "create",
			Short:   "Record a fine-tune job",
			Long:    "Record one fine-tune job from a JSON document.",
			Example: `  olivares models finetune create --data @job.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/finetune-jobs"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "update <job-id>",
			Short:   "Replace a fine-tune job record",
			Long:    "Replace one job record with the supplied document — typically to record its outcome.",
			Example: `  olivares models finetune update 018f2a10-0000-7000-8000-000000000004 --data @job.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/finetune-jobs", IDs: 1},
			Body:    modelstackBodyRequired,
		}),
	)
	return cmd
}

// --- compliance posture and admission ----------------------------------------

func newModelsGPAICmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gpai",
		Short: "Attest per-provider GPAI compliance posture",
		Long: "Read and attest the per-provider GPAI posture (code-of-practice signature, technical\n" +
			"documentation, training-data summary, copyright policy, systemic-risk and safety\n" +
			"reporting). It is an OPERATOR ATTESTATION and records claim vs verified separately.",
		Example: `  olivares models gpai ls
  olivares models gpai attest --data @posture.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List attested GPAI posture per provider",
			Long:      "List the attested GPAI posture per provider, with who attested it and whether it was verified.",
			Example:   `  olivares models gpai ls --provider-ref anthropic -o json`,
			Target:    modelstackTarget{Collection: "/gpai-posture"},
			EmptyNote: "no GPAI posture attested",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "provider-ref", Query: "provider_ref", Usage: "only this provider"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "PROVIDER", Key: "provider_ref"},
				{Header: "COP", Key: "cop_signatory"},
				{Header: "TECH DOCS", Key: "technical_docs"},
				{Header: "VERIFIED", Key: "verified"},
				{Header: "BY", Key: "attested_by"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "attest",
			Short:   "Attest one provider's GPAI posture",
			Long:    "Upsert the GPAI posture attestation for one provider from a JSON document.",
			Example: `  olivares models gpai attest --data @posture.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/gpai-posture"},
			Body:    modelstackBodyRequired,
		}),
	)
	return cmd
}

func newModelsAdmissionCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admission",
		Short: "Govern the signed-model admission trust root and read its verdicts",
		Long: "Read and set the per-tenant admission policy — the trust root the verify-before-admit\n" +
			"ceremony evaluates against — and read the verdicts it has recorded.\n\n" +
			"Setting the policy is admin-tier: it decides what this tenant will ever admit.",
		Example: `  olivares models admission policy
  olivares models admission ls --verified true`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "policy",
			Short:   "Show the admission trust root",
			Long:    "Show this tenant's signed-model admission policy: the roots, issuers and requirements a version must satisfy.",
			Example: `  olivares models admission policy -o json`,
			Target:  modelstackTarget{Collection: "/admission-policy"},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "set-policy",
			Short: "Replace the admission trust root",
			Long: "Replace this tenant's admission policy with the supplied document. It governs every\n" +
				"future admission, so it is a full replacement and admin-tier.",
			Example: `  olivares models admission set-policy --data @admission-policy.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/admission-policy"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List recorded admission verdicts",
			Long:      "List the admission verdicts recorded per version: what verified, what did not, and why.",
			Example:   `  olivares models admission ls --verified false -o json`,
			Target:    modelstackTarget{Collection: "/model-admissions"},
			EmptyNote: "no admission verdicts recorded",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "version-ref", Query: "version_ref", Usage: "only verdicts for this version"},
				{Flag: "verified", Query: "verified", Usage: "only verdicts with this verification outcome (true or false)"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "VERSION", Key: "version_ref"},
				{Header: "SIGNER", Key: "signer_identity"},
				{Header: "SIGNATURE", Key: "signature_verified"},
				{Header: "ARTIFACT", Key: "artifact_verified"},
				{Header: "TLOG", Key: "tlog_verified"},
				{Header: "REASON", Key: "reason"},
			},
		}),
	)
	return cmd
}

// --- lineage: datasets, AIBOMs, agent artifacts ------------------------------

func newModelsDatasetsCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "datasets",
		Short: "Govern dataset lineage components",
		Long: "Govern the datasets that appear as lineage components in an AIBOM: their\n" +
			"classification, governance status, content hash and attestation.",
		Example: `  olivares models datasets ls
  olivares models datasets create --data @dataset.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List governed datasets",
			Long:      "List datasets with their classification, governance status and content hash.",
			Example:   `  olivares models datasets ls --owned-ref 018f2a10-0000-7000-8000-000000000001`,
			Target:    modelstackTarget{Collection: "/datasets"},
			EmptyNote: "no datasets registered",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "owned-ref", Query: "owned_ref", Usage: "only datasets of this owned model"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "NAME", Key: "name"},
				{Header: "OWNED", Key: "owned_ref"},
				{Header: "CLASSIFICATION", Key: "classification"},
				{Header: "GOVERNANCE", Key: "governance"},
				{Header: "VERIFIED", Key: "verified"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "create",
			Short:   "Register a dataset",
			Long:    "Register one dataset lineage component from a JSON document.",
			Example: `  olivares models datasets create --data @dataset.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/datasets"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <dataset-id>",
			Short: "Remove a dataset",
			Long: "Remove one dataset record. Already-sealed AIBOMs keep the component they sealed —\n" +
				"the seal is evidence — but new AIBOMs will not carry it.",
			Example: `  olivares models datasets rm 018f2a10-0000-7000-8000-000000000005 --yes`,
			Target:  modelstackTarget{Collection: "/datasets", IDs: 1},
			Noun:    "dataset",
			Blast:   "new AIBOMs stop carrying this lineage component",
		}),
	)
	return cmd
}

func newModelsAIBOMCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aibom",
		Short: "Generate, seal and list AI bills of materials",
		Long: "Generate the CycloneDX AIBOM for an owned model (or its SPDX 3.0.1 AI-Profile form),\n" +
			"seal a content hash of it to the append-only ledger as evidence, list the seals, and\n" +
			"render the model card built from the same governed inventory.\n\n" +
			"Generation is read-only. SEALING anchors evidence and cannot be withdrawn.",
		Example: `  olivares models aibom get 018f2a10-0000-7000-8000-000000000001 --format spdx
  olivares models aibom card 018f2a10-0000-7000-8000-000000000001 --format md`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "get <owned-id>",
			Short: "Generate the AIBOM for one owned model",
			Long: "Generate the AIBOM for one owned model. The default is CycloneDX; --format spdx\n" +
				"renders the SPDX 3.0.1 AI Profile JSON-LD form of the same inventory. Read-only:\n" +
				"generating seals nothing.",
			Example: `  olivares models aibom get 018f2a10-0000-7000-8000-000000000001
  olivares models aibom get 018f2a10-0000-7000-8000-000000000001 --format spdx`,
			Target: modelstackTarget{Collection: "/owned-models", IDs: 1, Nested: "aibom"},
			Filters: []modelstackFilterSpec{
				{Flag: "format", Query: "format", Usage: "document format: the module's default (CycloneDX) or spdx"},
			},
			Raw: true,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "seal <owned-id>",
			Short: "Seal the current AIBOM to the ledger as evidence",
			Long: "Compute the AIBOM for one owned model and anchor its content hash to the append-only\n" +
				"ledger. The seal is evidence: it records what the inventory was at this moment and\n" +
				"cannot be withdrawn.",
			Example: `  olivares models aibom seal 018f2a10-0000-7000-8000-000000000001`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/owned-models", IDs: 1, Nested: "aibom"},
			Body:    modelstackBodyNone,
		}),
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List AIBOM seals",
			Long:      "List the sealed AIBOMs with their serial number, content hash and ledger position.",
			Example:   `  olivares models aibom ls --owned-ref 018f2a10-0000-7000-8000-000000000001 -o json`,
			Target:    modelstackTarget{Collection: "/aiboms"},
			EmptyNote: "no AIBOM seals recorded",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "owned-ref", Query: "owned_ref", Usage: "only seals for this owned model"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "OWNED", Key: "owned_ref"},
				{Header: "SERIAL", Key: "serial_number"},
				{Header: "HASH", Key: "content_hash"},
				{Header: "COMPONENTS", Key: "component_count"},
				{Header: "LEDGER SEQ", Key: "ledger_seq"},
			},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "card <owned-id>",
			Short: "Render the model card for one owned model",
			Long: "Render the model card generated from the same governed inventory as the AIBOM.\n" +
				"--format md asks for Markdown, which the control plane serves as text: stdout then\n" +
				"carries it verbatim so it can be redirected to a file.",
			Example: `  olivares models aibom card 018f2a10-0000-7000-8000-000000000001 --format md
  olivares models aibom card 018f2a10-0000-7000-8000-000000000001 -o json`,
			Target: modelstackTarget{Collection: "/owned-models", IDs: 1, Nested: "model-card"},
			Filters: []modelstackFilterSpec{
				{Flag: "format", Query: "format", Usage: "card format: the module's default (JSON) or md"},
			},
			Raw: true,
		}),
	)
	return cmd
}

func newModelsAgentArtifactsCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "agent-artifacts",
		Aliases: []string{"artifacts"},
		Short:   "Govern the agent-artifact supply chain",
		Long: "Govern the four agent-artifact classes (skill, mcpb_extension, mcp_app_template,\n" +
			"agents_md) as registry entries with provenance and a posture verdict, and the dedicated\n" +
			"agent-supply-chain CycloneDX BOM built from them.\n\n" +
			"Its seals are their OWN evidence class, separate from the model-lineage AIBOM seals.",
		Example: `  olivares models agent-artifacts ls
  olivares models agent-artifacts aibom -o json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List governed agent artifacts",
			Long:      "List the registered agent artifacts with their class, provenance, content hash and posture grade.",
			Example:   `  olivares models agent-artifacts ls --artifact-class skill -o json`,
			Target:    modelstackTarget{Collection: "/agent-artifacts"},
			EmptyNote: "no agent artifacts registered",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "artifact-class", Query: "artifact_class", Usage: "only artifacts of this class (skill, mcpb_extension, mcp_app_template, agents_md)"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "CLASS", Key: "artifact_class"},
				{Header: "NAME", Key: "name"},
				{Header: "VERSION", Key: "version"},
				{Header: "PROVENANCE", Key: "provenance"},
				{Header: "POSTURE", Key: "posture_grade"},
				{Header: "VERIFIED", Key: "verified"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "create",
			Short:   "Register an agent artifact",
			Long:    "Register one agent artifact with its provenance and content hash, from a JSON document.",
			Example: `  olivares models agent-artifacts create --data @artifact.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/agent-artifacts"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <artifact-id>",
			Short: "Remove an agent artifact",
			Long: "Remove one agent artifact from the registry. Sealed agent BOMs keep what they\n" +
				"sealed; future BOMs will not carry this component.",
			Example: `  olivares models agent-artifacts rm 018f2a10-0000-7000-8000-000000000006 --yes`,
			Target:  modelstackTarget{Collection: "/agent-artifacts", IDs: 1},
			Noun:    "agent artifact",
			Blast:   "future agent BOMs stop carrying this component",
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "aibom",
			Short:   "Generate the agent-supply-chain BOM",
			Long:    "Generate the CycloneDX BOM covering this tenant's governed agent artifacts. Read-only.",
			Example: `  olivares models agent-artifacts aibom -o json`,
			Target:  modelstackTarget{Collection: "/agent-artifacts", Nested: "aibom"},
			Raw:     true,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "seal",
			Short: "Seal the agent-supply-chain BOM to the ledger",
			Long: "Anchor the content hash of the current agent-supply-chain BOM to the append-only\n" +
				"ledger as its own evidence kind. It cannot be withdrawn.",
			Example: `  olivares models agent-artifacts seal`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/agent-artifacts", Nested: "aibom"},
			Body:    modelstackBodyNone,
		}),
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "seals",
			Short:     "List agent-supply-chain BOM seals",
			Long:      "List the sealed agent BOMs with their serial number, content hash and ledger position.",
			Example:   `  olivares models agent-artifacts seals -o json`,
			Target:    modelstackTarget{Collection: "/agent-artifacts", Nested: "aiboms"},
			EmptyNote: "no agent BOM seals recorded",
			Paginated: true,
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "SERIAL", Key: "serial_number"},
				{Header: "HASH", Key: "content_hash"},
				{Header: "COMPONENTS", Key: "component_count"},
				{Header: "LEDGER SEQ", Key: "ledger_seq"},
				{Header: "AT", Key: "generated_at"},
			},
		}),
	)
	return cmd
}

// --- model-access governance -------------------------------------------------

func newModelsGroupsCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "groups",
		Aliases: []string{"model-groups"},
		Short:   "Author named model groups",
		Long: "Author the named sets that model-access grants point at: explicit model references\n" +
			"plus family and tier selectors. A group is a reference set; it grants nothing on its own.",
		Example: `  olivares models groups ls
  olivares models groups create --data @group.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List model groups",
			Long:      "List the named model groups with their explicit members and selectors.",
			Example:   `  olivares models groups ls -o json`,
			Target:    modelstackTarget{Collection: "/model-groups"},
			EmptyNote: "no model groups defined",
			Paginated: true,
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "NAME", Key: "name"},
				{Header: "MEMBERS", Key: "member_refs"},
				{Header: "FAMILIES", Key: "family_selectors"},
				{Header: "TIERS", Key: "tier_selectors"},
			},
		}),
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:     "get <group-id>",
			Short:   "Show one model group",
			Long:    "Show one model group with its members, selectors and description.",
			Example: `  olivares models groups get 018f2a10-0000-7000-8000-000000000007`,
			Target:  modelstackTarget{Collection: "/model-groups", IDs: 1},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "create",
			Short:   "Create a model group",
			Long:    "Create one model group from a JSON document.",
			Example: `  olivares models groups create --data @group.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/model-groups"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "update <group-id>",
			Short: "Replace a model group",
			Long: "Replace one model group with the supplied document. Grants that point at this group\n" +
				"immediately cover the new membership.",
			Example: `  olivares models groups update 018f2a10-0000-7000-8000-000000000007 --data @group.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/model-groups", IDs: 1},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <group-id>",
			Short: "Delete a model group",
			Long: "Delete one model group. The engine refuses with 409 (exit 5) while a grant still\n" +
				"points at it, so a grant is never left aiming at nothing.",
			Example: `  olivares models groups rm 018f2a10-0000-7000-8000-000000000007 --yes`,
			Target:  modelstackTarget{Collection: "/model-groups", IDs: 1},
			Noun:    "model group",
			Blast:   "grants pointing at this group must be removed first",
		}),
	)
	return cmd
}

func newModelsAccessCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "access",
		Aliases: []string{"model-access"},
		Short:   "Author model-access grants (who may use which model)",
		Long: "Author the grants that decide which subject (user, role or agent group) may use which\n" +
			"model or model group, in which workspace and on which surface. They are enforced\n" +
			"deny-closed in the routing select/execute chain.\n\n" +
			"Authoring a grant is ADMIN-tier: it widens who may spend against a model.",
		Example: `  olivares models access ls
  olivares models access ls --subject-kind role --subject-ref admin`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List model-access grants",
			Long:      "List the grants with their subject, target, workspace, surfaces and effect (allow or forbid).",
			Example:   `  olivares models access ls --target-kind model_group -o json`,
			Target:    modelstackTarget{Collection: "/model-access"},
			EmptyNote: "no model-access grants defined",
			Paginated: true,
			Filters: []modelstackFilterSpec{
				{Flag: "subject-kind", Query: "subject_kind", Usage: "only grants whose subject is of this kind (user, role, agent_group)"},
				{Flag: "subject-ref", Query: "subject_ref", Usage: "only grants for this subject reference"},
				{Flag: "target-kind", Query: "target_kind", Usage: "only grants whose target is of this kind (model, model_group)"},
				{Flag: "target-ref", Query: "target_ref", Usage: "only grants for this target reference"},
			},
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "SUBJECT", Key: "subject_ref"},
				{Header: "S-KIND", Key: "subject_kind"},
				{Header: "TARGET", Key: "target_ref"},
				{Header: "T-KIND", Key: "target_kind"},
				{Header: "WORKSPACE", Key: "workspace_ref"},
				{Header: "EFFECT", Key: "effect"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "create",
			Short: "Create a model-access grant",
			Long: "Create one grant from a JSON document. A forbid grant narrows; an allow grant widens\n" +
				"who may spend against a model — which is why this is admin-tier in the engine.",
			Example: `  olivares models access create --data @grant.json`,
			Method:  http.MethodPost,
			Target:  modelstackTarget{Collection: "/model-access"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:     "update <grant-id>",
			Short:   "Replace a model-access grant",
			Long:    "Replace one grant with the supplied document. Full replacement, not a merge.",
			Example: `  olivares models access update 018f2a10-0000-7000-8000-000000000008 --data @grant.json`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/model-access", IDs: 1},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <grant-id>",
			Short: "Delete a model-access grant",
			Long: "Delete one grant. Removing a FORBID widens access and removing an ALLOW narrows it:\n" +
				"the confirmation names the grant so the direction is checked before it is applied.",
			Example: `  olivares models access rm 018f2a10-0000-7000-8000-000000000008 --yes`,
			Target:  modelstackTarget{Collection: "/model-access", IDs: 1},
			Noun:    "model-access grant",
			Blast:   "removing a forbid WIDENS access; removing an allow narrows it",
		}),
	)
	return cmd
}
