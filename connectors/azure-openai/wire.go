// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureopenai

import "encoding/json"

// This file holds the ARM / Azure-Monitor / Cost-Management JSON wire shapes the connector
// reads. Only the minimal-data fields the connector needs are mapped (verified field names
// + casing, jun-2026); the full upstream payload carries more and is ignored. The connector
// reads token COUNTS, cost amounts and deployment/model METADATA — never a prompt, a
// completion, a key value or a content-filter decision.

// --- Subscription discovery (management.azure.com/subscriptions) --------------------

type subscriptionsResponse struct {
	Value    []subscriptionRef `json:"value"`
	NextLink string            `json:"nextLink"`
}

type subscriptionRef struct {
	SubscriptionID string `json:"subscriptionId"`
	State          string `json:"state"`
}

// --- Cognitive Services accounts ----------------------------------------------------

type accountsResponse struct {
	Value    []account `json:"value"`
	NextLink string    `json:"nextLink"`
}

// account is one Cognitive Services account. ID is the full ARM resource id (used verbatim
// as the metrics resource uri and the deployments/models base path). kind is a FREE-FORM
// string (OpenAI / AIServices / Speech / …) filtered client-side.
type account struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Location   string `json:"location"`
	Properties struct {
		Endpoint string `json:"endpoint"`
	} `json:"properties"`
}

// --- Deployments (.../accounts/{a}/deployments) -------------------------------------

type deploymentsResponse struct {
	Value    []deployment `json:"value"`
	NextLink string       `json:"nextLink"`
}

// deployment is one model deployment. The deployment Name is the inference-callable id and
// the Azure Monitor ModelDeploymentName dimension value; properties.model is the underlying
// model (format Anthropic for Claude-on-Foundry, OpenAI for OpenAI). raiPolicyName is read
// as an OPAQUE reference only (RAI posture is the azure-activity connector's domain).
type deployment struct {
	Name string `json:"name"`
	Sku  struct {
		Name     string `json:"name"`
		Capacity int64  `json:"capacity"`
	} `json:"sku"`
	Properties struct {
		Model struct {
			Format  string `json:"format"`
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"model"`
		ProvisioningState    string `json:"provisioningState"`
		RaiPolicyName        string `json:"raiPolicyName"`
		VersionUpgradeOption string `json:"versionUpgradeOption"`
	} `json:"properties"`
}

// --- Account model catalog (.../accounts/{a}/models = AccountModelListResult) --------

type accountModelsResponse struct {
	Value    []accountModel `json:"value"`
	NextLink string         `json:"nextLink"`
}

// accountModel is one deployable model. Its identity fields (format/name/version) are FLAT
// at the AccountModel level (only baseModel is nested). lifecycleStatus + deprecation are
// the retirement signal mapped onto the catalog.
type accountModel struct {
	Format           string `json:"format"`
	Name             string `json:"name"`
	Version          string `json:"version"`
	IsDefaultVersion bool   `json:"isDefaultVersion"`
	LifecycleStatus  string `json:"lifecycleStatus"` // Stable|Preview|GenerallyAvailable|Deprecating|Deprecated
	Deprecation      struct {
		FineTune  string `json:"fineTune"`
		Inference string `json:"inference"` // ISO-8601 date the model retires for inference
	} `json:"deprecation"`
}

// --- Azure Monitor metrics (.../providers/Microsoft.Insights/metrics) ---------------

type metricsResponse struct {
	Value []metricResult `json:"value"`
}

type metricResult struct {
	Name struct {
		Value string `json:"value"` // the REST metric name (ProcessedPromptTokens / GeneratedTokens)
	} `json:"name"`
	Timeseries []metricTimeseries `json:"timeseries"`
}

type metricTimeseries struct {
	Metadatavalues []metricMetadataValue `json:"metadatavalues"`
	Data           []metricDatapoint     `json:"data"`
}

// metricMetadataValue is one dimension name/value pair. The dimension NAME is returned
// lowercased/normalized vs the documented PascalCase, so it is matched case-insensitively.
type metricMetadataValue struct {
	Name struct {
		Value string `json:"value"`
	} `json:"name"`
	Value string `json:"value"` // dimension value (e.g. the deployment name)
}

type metricDatapoint struct {
	TimeStamp string   `json:"timeStamp"` // capital S
	Total     *float64 `json:"total"`     // nil when the aggregation key is absent (never assume 0)
}

// --- Cost Management Query (.../providers/Microsoft.CostManagement/query) ------------

type costQueryResponse struct {
	Properties struct {
		Columns  []costColumn        `json:"columns"`
		Rows     [][]json.RawMessage `json:"rows"` // array-of-arrays, column-ordered, mixed types
		NextLink string              `json:"nextLink"`
	} `json:"properties"`
}

type costColumn struct {
	Name string `json:"name"`
	Type string `json:"type"` // "Number" | "String"
}
