// Package provider implements the Lucidity Terraform provider: Phase 1's
// authentication layer, plus Phase 2's lucidity_tenant resource and
// lucidity_tenants data source.
package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/providervalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/luciditycloud/terraform-provider-lucidity/internal/client"
)

const defaultMaxParallelRequests = 10

var (
	_ provider.Provider                     = &LucidityProvider{}
	_ provider.ProviderWithConfigValidators = &LucidityProvider{}
)

// LucidityProvider is the top-level Terraform provider implementation.
type LucidityProvider struct {
	// version is injected by main.go, set at build time by GoReleaser.
	version string
}

type lucidityProviderModel struct {
	RefreshToken                 types.String `tfsdk:"refresh_token"`
	RefreshTokenFile             types.String `tfsdk:"refresh_token_file"`
	RefreshTokenCommand          types.String `tfsdk:"refresh_token_command"`
	LucidityDashboardURL         types.String `tfsdk:"lucidity_dashboard_url"`
	MaxParallelRequests          types.Int64  `tfsdk:"max_parallel_requests"`
	ProactiveRefreshBufferMins   types.Int64  `tfsdk:"proactive_refresh_buffer_minutes"`
	LucidityDashboardAccountName types.String `tfsdk:"lucidity_dashboard_account_name"`
}

// LucidityClients bundles the API client made available to resources and
// data sources via their Configure methods. Only Client exists in Phase 1.
type LucidityClients struct {
	Client *client.Client
}

// New returns a provider.Provider factory, suitable for
// providerserver.ServeOpts. version should be injected by GoReleaser at
// build time; main.go defaults it to "dev" for local builds.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &LucidityProvider{version: version}
	}
}

func (p *LucidityProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "lucidity"
	resp.Version = p.version
}

func (p *LucidityProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Interact with the Lucidity cloud storage optimization platform.",
		Attributes: map[string]schema.Attribute{
			"refresh_token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Long-lived Lucidity refresh token, supplied directly. Discouraged: the value ends up in your .tf config or, if fed from a data source, in Terraform state. Prefer refresh_token_file, refresh_token_command, or the LUCIDITY_REFRESH_TOKEN environment variable. Exactly one of refresh_token, refresh_token_file, or refresh_token_command may be set.",
			},
			"refresh_token_file": schema.StringAttribute{
				Optional:    true,
				Description: "Path to a local file containing only the Lucidity refresh token; its contents are trimmed of surrounding whitespace. Never sent anywhere but read into memory. Exactly one of refresh_token, refresh_token_file, or refresh_token_command may be set.",
			},
			"refresh_token_command": schema.StringAttribute{
				Optional:    true,
				Description: "Shell command whose trimmed stdout is used as the Lucidity refresh token, e.g. `vault kv get -field=token secret/lucidity`. Runs via the platform shell with a 30s timeout; stdout is never logged, including at TF_LOG=DEBUG. Exactly one of refresh_token, refresh_token_file, or refresh_token_command may be set.",
			},
			"lucidity_dashboard_url": schema.StringAttribute{
				Required:    true,
				Description: fmt.Sprintf("The URL you use to log in to the Lucidity dashboard. Determines the API base URL the provider talks to — there is no separate base-URL setting. Must be exactly one of: %s.", strings.Join(validLucidityDashboardURLs(), ", ")),
				Validators: []validator.String{
					stringvalidator.OneOf(validLucidityDashboardURLs()...),
				},
			},
			"max_parallel_requests": schema.Int64Attribute{
				Optional:    true,
				Description: fmt.Sprintf("Maximum number of concurrent Lucidity API requests. Defaults to %d.", defaultMaxParallelRequests),
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
			},
			"proactive_refresh_buffer_minutes": schema.Int64Attribute{
				Optional: true,
				Description: fmt.Sprintf(
					"How many minutes before the %d-minute access-token expiry to proactively renew it. Defaults to %d. Must be between 1 and %d.",
					int(client.AccessTokenTTL.Minutes()),
					int((client.AccessTokenTTL - client.DefaultProactiveRefreshAge).Minutes()),
					int(client.AccessTokenTTL.Minutes())-1,
				),
				Validators: []validator.Int64{
					int64validator.Between(1, int64(client.AccessTokenTTL.Minutes())-1),
				},
			},
			"lucidity_dashboard_account_name": schema.StringAttribute{
				Required:    true,
				Description: "The Lucidity dashboard account this refresh token is expected to belong to. Intended to catch a wrong-account refresh token before any tenant operation runs; the provider cannot yet cross-validate this against the token itself (no Lucidity endpoint currently exposes which account a token belongs to), so today this is recorded but not enforced.",
			},
		},
	}
}

// ConfigValidators enforces the locked token-source precedence: zero or one
// of refresh_token / refresh_token_file / refresh_token_command may be set
// (zero is valid — it means "use LUCIDITY_REFRESH_TOKEN"); more than one is
// a hard validate-time error naming every attribute that was set.
func (p *LucidityProvider) ConfigValidators(_ context.Context) []provider.ConfigValidator {
	return []provider.ConfigValidator{
		providervalidator.Conflicting(
			path.MatchRoot("refresh_token"),
			path.MatchRoot("refresh_token_file"),
			path.MatchRoot("refresh_token_command"),
		),
	}
}

func (p *LucidityProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data lucidityProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	refreshToken, err := resolveRefreshToken(ctx, data.RefreshToken.ValueString(), data.RefreshTokenFile.ValueString(), data.RefreshTokenCommand.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to resolve Lucidity refresh token", err.Error())
		return
	}

	baseURL, ok := apiBaseURLFor(data.LucidityDashboardURL.ValueString())
	if !ok {
		// Unreachable in practice: the OneOf validator already rejects any
		// value not in knownDeployments before Configure runs. Kept as a
		// defensive check in case the validator and that table ever drift.
		resp.Diagnostics.AddAttributeError(
			path.Root("lucidity_dashboard_url"),
			"Unknown lucidity_dashboard_url",
			fmt.Sprintf("%q is not a recognized Lucidity dashboard login URL.", data.LucidityDashboardURL.ValueString()),
		)
		return
	}

	maxParallel := defaultMaxParallelRequests
	if !data.MaxParallelRequests.IsNull() {
		maxParallel = int(data.MaxParallelRequests.ValueInt64())
	}

	proactiveRefreshAge := client.DefaultProactiveRefreshAge
	if !data.ProactiveRefreshBufferMins.IsNull() {
		bufferMinutes := data.ProactiveRefreshBufferMins.ValueInt64()
		proactiveRefreshAge = client.AccessTokenTTL - time.Duration(bufferMinutes)*time.Minute
	}

	apiClient := client.NewClient(baseURL, refreshToken,
		client.WithMaxParallelRequests(maxParallel),
		client.WithProactiveRefreshAge(proactiveRefreshAge),
	)
	apiClient.DebugLog = func(ctx context.Context, msg string, fields map[string]any) {
		tflog.Debug(ctx, msg, fields)
	}

	clients := &LucidityClients{Client: apiClient}
	resp.DataSourceData = clients
	resp.ResourceData = clients
}

func (p *LucidityProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newTenantResource,
	}
}

func (p *LucidityProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newTenantsDataSource,
	}
}
