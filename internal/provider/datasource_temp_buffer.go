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
	_ datasource.DataSource              = &tempBufferDataSource{}
	_ datasource.DataSourceWithConfigure = &tempBufferDataSource{}
)

func newTempBufferDataSource() datasource.DataSource {
	return &tempBufferDataSource{}
}

type tempBufferDataSource struct {
	client *client.Client
}

type tempBufferDataSourceModel struct {
	BufferID              types.String `tfsdk:"buffer_id"`
	InstanceID            types.String `tfsdk:"instance_id"`
	Status                types.String `tfsdk:"status"`
	CurrentFreeSpaceInGiB types.Int64  `tfsdk:"current_free_space_in_gib"`
	FailureReason         types.String `tfsdk:"failure_reason"`
}

func (d *tempBufferDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_temp_buffer"
}

func (d *tempBufferDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *tempBufferDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the current status of a temporary buffer by its buffer ID, including one requested outside Terraform. Useful to check a buffer is ACTIVE before starting a restore, e.g. in a `check` block.",
		Attributes: map[string]schema.Attribute{
			"buffer_id": schema.StringAttribute{
				Required:    true,
				Description: "Lucidity's identifier for the buffer.",
			},
			"instance_id": schema.StringAttribute{
				Computed:    true,
				Description: "The instance the buffer belongs to.",
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "PROVISIONING, ACTIVE, REVOKED, or FAILED.",
			},
			"current_free_space_in_gib": schema.Int64Attribute{
				Computed:    true,
				Description: "Current free space reported for the buffer, in GiB.",
			},
			"failure_reason": schema.StringAttribute{
				Computed:    true,
				Description: "Why the buffer failed, when status is FAILED.",
			},
		},
	}
}

func (d *tempBufferDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg tempBufferDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	status, err := d.client.GetTempBufferStatus(ctx, cfg.BufferID.ValueString())
	if err != nil {
		if isHTTPStatus(err, 404) {
			resp.Diagnostics.AddError("Lucidity temporary buffer not found", fmt.Sprintf("No buffer exists with ID %s.", cfg.BufferID.ValueString()))
			return
		}
		resp.Diagnostics.AddError("Unable to read Lucidity temporary buffer status", err.Error())
		return
	}

	state := tempBufferDataSourceModel{
		BufferID:              cfg.BufferID,
		InstanceID:            types.StringValue(status.InstanceID),
		Status:                types.StringValue(status.Status),
		CurrentFreeSpaceInGiB: types.Int64Value(status.CurrentFreeSpaceInGiB),
		FailureReason:         types.StringPointerValue(status.FailureReason),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
