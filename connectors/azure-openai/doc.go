// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package azureopenai is the read-only Olivares AI connector for Azure OpenAI / Azure AI
// Foundry — the Azure generative-AI estate the control plane is otherwise blind to. The
// existing claude-api surface models only Claude-on-Foundry inference (Messages); the
// openai connector's "azure-openai mode" relabels the provider but still calls
// OpenAI-organization paths (/v1/organization/usage, /v1/models, admin_api_keys) that DO
// NOT EXIST on Azure. This connector reads the REAL Azure surfaces: the ARM Cognitive
// Services deployment + model catalog, per-deployment token usage from Azure Monitor, and
// billed cost from Azure Cost Management.
//
// It satisfies the two shared model/provider contracts (README.md): sdk.SourceConnector
// (usage/cost as model.CostSample observations) and modelprovider.CatalogProvider (the
// catalog). ProviderRef is "azure-openai" (modelprovider.ProviderAzureOpenAI); every cost
// sample carries Gateway=foundry so FinOps/governance attributes Azure spend distinctly.
//
// # What it emits (three surfaces, all read-only over the ARM control plane)
//
//   - CATALOG (modelprovider.Catalog): the Cognitive Services accounts (kind OpenAI /
//     AIServices) are the Workspaces; each account's DEPLOYMENTS are the catalog Models
//     (Ref = the deployment name — the inference-callable id and the Azure Monitor
//     ModelDeploymentName dimension), enriched with the underlying model format/name/
//     version, declared family pricing + capabilities, and the account-model lifecycle
//     (deprecation / retirement). Claude-on-Foundry is an ordinary Anthropic-format
//     deployment, enumerated by the SAME call as the OpenAI deployments.
//
//   - TOKEN USAGE → model.CostSample (Azure Monitor metrics on the account resource:
//     ProcessedPromptTokens = input, GeneratedTokens = output, split per deployment by the
//     ModelDeploymentName dimension): one sample per (deployment, bucket), cost DERIVED
//     from declared list pricing (Provenance=estimated — the metrics carry counts, not
//     money). MINIMAL DATA: only token COUNTS and the deployment/account refs.
//
//   - BILLED COST → model.CostSample (opt-in, Azure Cost Management Query — money, never
//     tokens): one sample per (account ResourceId, meter, day), cost mapped by COLUMN NAME
//     (the response cost column may come back as PreTaxCost even when Cost was requested),
//     UsageDate is an integer yyyymmdd, money parsed as a decimal string (never a float).
//     Open-month / trailing days are Provenance=estimated (Azure rerate-until-invoiced;
//     there is no isFinal flag), finalized days are billed. No row ⇒ nothing emitted
//     (absence ≠ zero); a 204 (no data yet, normal lag) is not an error.
//
// The usage and cost streams are SEPARATE, honest lenses that do not double-count: usage
// carries real tokens with derived cost, billed cost carries real money with no tokens.
//
// # Scope boundary (no duplication)
//
// Responsible-AI / content-filter posture is DEFERRED to connectors/azure-activity (enable_rai), which already owns it: this connector reads Deployment.raiPolicyName only
// as an OPAQUE reference and never infers "no content filtering" from an absent policy
// name (Azure applies the platform default). Enable RAI posture on the azure-activity
// connector, not here.
//
// # Security posture (docs/SECURITY-HARDENING.md-3)
//
// Read-only: every call is a GET against the ARM control plane (Cost Management's Query is
// a POST action that is a READ — the body is a query, never a mutation). It never reads
// an account key (listKeys returns the secret — out of scope, minimal-data) and never
// calls the data-plane inference endpoint. It mints Azure AD tokens with the standard-
// library client-credentials flow (auth.go), holds the credential only in memory, and
// never logs or emits it. It imports only the SDK and the Apache modelprovider contract.
package azureopenai
