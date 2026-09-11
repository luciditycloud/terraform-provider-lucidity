package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/luciditycloud/terraform-provider-lucidity/internal/client"
)

var (
	_ datasource.DataSource              = &tenantsDataSource{}
	_ datasource.DataSourceWithConfigure = &tenantsDataSource{}
)

func newTenantsDataSource() datasource.DataSource {
	return &tenantsDataSource{}
}

type tenantsDataSource struct {
	client *client.Client
}

type tenantsDataSourceModel struct {
	Tenants []tenantDataSourceModel `tfsdk:"tenants"`
}

type tenantDataSourceModel struct {
	TenantID               types.String `tfsdk:"tenant_id"`
	CloudProvider          types.String `tfsdk:"cloud_provider"`
	CloudProviderAccountID types.String `tfsdk:"cloud_provider_account_id"`
	CloudEntityName        types.String `tfsdk:"cloud_entity_name"`
	DisplayName            types.String `tfsdk:"display_name"`
	Status                 types.String `tfsdk:"status"`
}

func (d *tenantsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenants"
}

func (d *tenantsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	clients, ok := req.ProviderData.(*LucidityClients)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *provider.LucidityClients, got: %T. Report this issue to the provider maintainers.", req.ProviderData),
		)
		return
	}
	d.client = clients.Client
}

func (d *tenantsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists every tenant (onboarded cloud account) under the caller's Lucidity account, ACTIVE first then INACTIVE. Enables a \"desired vs actual\" comparison against your lucidity_tenant resources.",
		Attributes: map[string]schema.Attribute{
			"tenants": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every tenant under the account, ACTIVE first then INACTIVE.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"tenant_id": schema.StringAttribute{
							Computed:    true,
							Description: "Lucidity's identifier for the tenant.",
						},
						"cloud_provider": schema.StringAttribute{
							Computed:    true,
							Description: "AWS, AZURE, or GCP.",
						},
						"cloud_provider_account_id": schema.StringAttribute{
							Computed:    true,
							Description: "The cloud account id.",
						},
						"cloud_entity_name": schema.StringAttribute{
							Computed:    true,
							Description: "The provider-side account name.",
						},
						"display_name": schema.StringAttribute{
							Computed:    true,
							Description: "The display name shown in the dashboard.",
						},
						"status": schema.StringAttribute{
							Computed:    true,
							Description: "ACTIVE, INACTIVE, or DECOMMISSIONED.",
						},
					},
				},
			},
		},
	}
}

func (d *tenantsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	tenants, err := d.client.ListTenants(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list Lucidity tenants", err.Error())
		return
	}

	var state tenantsDataSourceModel
	state.Tenants = make([]tenantDataSourceModel, 0, len(tenants))
	for _, t := range tenants {
		state.Tenants = append(state.Tenants, tenantDataSourceModel{
			TenantID:               types.StringValue(t.TenantID),
			CloudProvider:          types.StringValue(t.CloudProvider),
			CloudProviderAccountID: types.StringValue(t.CloudProviderAccountID),
			CloudEntityName:        types.StringValue(t.CloudEntityName),
			DisplayName:            types.StringValue(t.DisplayName),
			Status:                 types.StringValue(t.Status),
		})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
