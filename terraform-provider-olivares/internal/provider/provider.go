// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package provider implements the Terraform provider for the Olivares AI
// control plane. It is the "manage-as-code" surface: agents (and, over time,
// other control-plane objects) declared in HCL and reconciled against the
// running engine via its REST API.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

// Ensure the provider satisfies the framework interface at compile time.
var _ provider.Provider = (*olivaresProvider)(nil)

// olivaresProvider is the provider implementation.
type olivaresProvider struct {
	// version is set at build/serve time and reported by Metadata.
	version string
}

// providerModel maps the provider configuration schema to Go values.
type providerModel struct {
	Endpoint           types.String `tfsdk:"endpoint"`
	APIToken           types.String `tfsdk:"api_token"`
	Tenant             types.String `tfsdk:"tenant"`
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
}

// providerData is handed to each resource via Configure: the REST client plus
// the provider-level tenant default that resources may override per-resource.
type providerData struct {
	client *client.Client
	tenant string
}

// New returns a function that constructs the provider, as required by main.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &olivaresProvider{version: version}
	}
}

// Metadata sets the provider type name (the `olivares_` resource prefix).
func (p *olivaresProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "olivares"
	resp.Version = p.version
}

// Schema declares the provider-level configuration block.
func (p *olivaresProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage the Olivares AI control plane as code via its REST API.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Required:    true,
				Description: "Base URL of the control plane API (e.g. https://127.0.0.1:8443). May also be set via OLIVARES_ENDPOINT.",
			},
			"api_token": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "Bearer API token. May also be set via OLIVARES_API_TOKEN.",
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID sent as X-Olivares-Tenant. Omit when the token is tenant-bound. May also be set via OLIVARES_TENANT.",
			},
			"insecure_skip_verify": schema.BoolAttribute{
				Optional:    true,
				Description: "Skip TLS certificate verification (for the self-signed dev cert). Defaults to false.",
			},
		},
	}
}

// Configure validates configuration, applies environment-variable fallbacks,
// and builds the shared REST client passed to resources.
func (p *olivaresProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Reject unknown values: these come from interpolation that hasn't resolved
	// yet and we cannot build a client from them.
	if cfg.Endpoint.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Unknown endpoint",
			"The provider endpoint is not known at configure time. Set it statically or via OLIVARES_ENDPOINT.")
	}
	if cfg.APIToken.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "Unknown API token",
			"The provider api_token is not known at configure time. Set it statically or via OLIVARES_API_TOKEN.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := stringWithEnv(cfg.Endpoint, "OLIVARES_ENDPOINT")
	apiToken := stringWithEnv(cfg.APIToken, "OLIVARES_API_TOKEN")
	tenant := stringWithEnv(cfg.Tenant, "OLIVARES_TENANT")

	insecure := false
	if !cfg.InsecureSkipVerify.IsNull() && !cfg.InsecureSkipVerify.IsUnknown() {
		insecure = cfg.InsecureSkipVerify.ValueBool()
	}

	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Missing endpoint",
			"The provider requires an endpoint. Set the `endpoint` attribute or the OLIVARES_ENDPOINT environment variable.")
	}
	if apiToken == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "Missing API token",
			"The provider requires an api_token. Set the `api_token` attribute or the OLIVARES_API_TOKEN environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	data := &providerData{
		client: client.New(client.Options{
			Endpoint:           endpoint,
			APIToken:           apiToken,
			Tenant:             tenant,
			Version:            p.version,
			InsecureSkipVerify: insecure,
			// Surface RFC 9745/8594 deprecation signals from the control plane
			// as a Terraform-visible WARN. The hook fires from the HTTP
			// transport, which has no resp.Diagnostics to append to (and must
			// not — a RoundTripper outlives any single RPC), so a context-aware
			// log line is the right layer. The client dedups to exactly one
			// notice per unique method and concrete request path per run, so a
			// deprecated route warns once per distinct resource it touches
			// instead of once per call.
			OnDeprecation: func(ctx context.Context, n client.Notice) {
				tflog.Warn(ctx, "Olivares control-plane endpoint is deprecated", map[string]any{
					"method":          n.Method,
					"endpoint":        n.Path,
					"deprecation":     n.Deprecation,
					"sunset":          n.Sunset,
					"migration_guide": n.Link,
				})
			},
		}),
		tenant: tenant,
	}
	resp.ResourceData = data
	resp.DataSourceData = data
}

// Resources lists the resource types this provider serves: the manage-as-code
// surface of the control plane — agents, deployment definitions, governance
// policies and agent↔identity bindings, plus the FinOps spend budgets, MCP
// server (connector) configs, notification routes, session workspaces, RBAC
// scoped grants, model-access rules and model groups a platform team keeps
// under source control.
func (p *olivaresProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAgentResource,
		NewDeploymentResource,
		NewPolicyResource,
		NewAgentIdentityBindingResource,
		NewBudgetResource,
		NewCapabilityConfigResource,
		NewNotificationRouteResource,
		NewWorkspaceResource,
		NewRBACGrantResource,
		NewModelAccessResource,
		NewModelGroupResource,
	}
}

// DataSources lists the data source types this provider serves: read-only views of
// the governed estate (policies, identity roster, the R/RW access map + drift, a
// deployment definition, the spend budgets, the reconciled inventory, and server
// metadata) so a Terraform/OpenTofu module can reference control-plane state
// without reimplementing REST calls.
func (p *olivaresProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewPoliciesDataSource,
		NewIdentitiesDataSource,
		NewAccessEdgesDataSource,
		NewDeploymentDataSource,
		NewServerInfoDataSource,
		NewBudgetsDataSource,
		NewInventoryDataSource,
	}
}

// stringWithEnv returns the configured value, falling back to the named
// environment variable when the attribute is null/unknown/empty.
func stringWithEnv(v types.String, env string) string {
	if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		return v.ValueString()
	}
	return os.Getenv(env)
}
