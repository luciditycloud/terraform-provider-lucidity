package provider

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/luciditycloud/terraform-provider-lucidity/internal/client"
)

var (
	_ resource.Resource                   = &tempBufferResource{}
	_ resource.ResourceWithConfigure      = &tempBufferResource{}
	_ resource.ResourceWithImportState    = &tempBufferResource{}
	_ resource.ResourceWithValidateConfig = &tempBufferResource{}
)

// tempBufferPollInterval/Timeout bound the wait for a requested buffer to
// leave PROVISIONING. The docs recommend polling every 30s and contacting
// support if a buffer is stuck in PROVISIONING beyond 30 minutes. Vars
// rather than consts only so tests can shrink the wait.
//
// tempBufferRevokeTimeout bounds the wait after a revoke for the status to
// read REVOKED. Waiting matters on a replacement: Lucidity allows one buffer
// per instance, so requesting the new buffer while the old one still counts
// as active would fail with a conflict.
//
// tempBufferRequestRetries is how many extra times Create re-sends the
// request, with the same request_id, when the outcome is unknown (5xx after
// the client's own retries, timeout, dropped connection): the response
// carrying the buffer ID may have been lost, and re-sending with the same
// request_id is the documented way to get it back without a second buffer.
var (
	tempBufferPollInterval   = 30 * time.Second
	tempBufferPollTimeout    = 30 * time.Minute
	tempBufferRevokeTimeout  = 10 * time.Minute
	tempBufferRequestRetries = 2
)

func newTempBufferResource() resource.Resource {
	return &tempBufferResource{}
}

type tempBufferResource struct {
	client *client.Client
}

type instanceIdentifierModel struct {
	CloudProvider          types.String `tfsdk:"cloud_provider"`
	CloudProviderAccountID types.String `tfsdk:"cloud_provider_account_id"`
	InstanceID             types.String `tfsdk:"instance_id"`
	ResourceGroup          types.String `tfsdk:"resource_group"`
}

type tempBufferResourceModel struct {
	InstanceIdentifier     *instanceIdentifierModel `tfsdk:"instance_identifier"`
	RequestBufferSizeInGiB types.Int64              `tfsdk:"request_buffer_size_in_gib"`
	RequestID              types.String             `tfsdk:"request_id"`
	BufferID               types.String             `tfsdk:"buffer_id"`
	Status                 types.String             `tfsdk:"status"`
	CurrentFreeSpaceInGiB  types.Int64              `tfsdk:"current_free_space_in_gib"`
	FailureReason          types.String             `tfsdk:"failure_reason"`
}

func (r *tempBufferResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_temp_buffer"
}

func (r *tempBufferResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	clients, ok := req.ProviderData.(*LucidityClients)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *provider.LucidityClients, got: %T. Report this issue to the provider maintainers.", req.ProviderData),
		)
		return
	}
	r.client = clients.Client
}

// replaceIfRecorded forces replacement only when a value already recorded in
// state changes. Right after `terraform import`, these fields are null in
// state (the status API doesn't return them), so the config value is
// assumed to match and simply recorded — same approach as lucidity_tenant's
// lucidity_product_list.
const replaceIfRecordedDescription = "Forces replacement when a value already recorded in state changes; after import, the config value is assumed to match."

func stringReplaceIfRecorded() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = !req.StateValue.IsNull()
		},
		replaceIfRecordedDescription, replaceIfRecordedDescription,
	)
}

func (r *tempBufferResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A temporary storage buffer on one Lucidity-managed instance, e.g. extra free space for a database restore or data migration. " +
			"Creating it requests the buffer and waits until it is ACTIVE; destroying it revokes the buffer and returns the instance to its original scaling policy. " +
			"A buffer keeps incurring cloud storage cost until it is revoked — Lucidity does not revoke it automatically. " +
			"Only one buffer can be active per instance. Lucidity's API has no call to resize or modify a buffer, so changing the size or the instance revokes the buffer and requests a new one — " +
			"don't change them while an operation depends on the buffer, and don't use create_before_destroy (the new request would conflict with the old buffer). " +
			"Keep buffer_id (e.g. as an output): Lucidity returns it only once, and it is the import ID.",
		Blocks: map[string]schema.Block{
			"instance_identifier": schema.SingleNestedBlock{
				Description: "The instance to add the buffer to. Required.",
				Validators: []validator.Object{
					objectvalidator.IsRequired(),
				},
				Attributes: map[string]schema.Attribute{
					"cloud_provider": schema.StringAttribute{
						Required:    true,
						Description: "AWS, AZURE, or GCP.",
						Validators: []validator.String{
							stringvalidator.OneOf("AWS", "AZURE", "GCP"),
						},
						PlanModifiers: []planmodifier.String{
							stringReplaceIfRecorded(),
						},
					},
					"cloud_provider_account_id": schema.StringAttribute{
						Required:    true,
						Description: "The cloud account that owns the instance: the AWS account ID (12 digits), the Azure subscription ID, or the GCP project ID.",
						PlanModifiers: []planmodifier.String{
							stringReplaceIfRecorded(),
						},
					},
					"instance_id": schema.StringAttribute{
						Required:    true,
						Description: "The instance's ID (in Azure, this may be the VM name). Must be an instance Lucidity already manages.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"resource_group": schema.StringAttribute{
						Optional:    true,
						Description: "AZURE only, and required there: the resource group the VM is in.",
						PlanModifiers: []planmodifier.String{
							stringReplaceIfRecorded(),
						},
					},
				},
			},
		},
		Attributes: map[string]schema.Attribute{
			"request_buffer_size_in_gib": schema.Int64Attribute{
				Required:    true,
				Description: "Size of the temporary buffer, in GiB. Lucidity's docs say buffers typically range from 1 GiB to 1 TiB; limits vary by cloud provider and instance type.",
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplaceIf(
						func(_ context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
							resp.RequiresReplace = !req.StateValue.IsNull()
						},
						replaceIfRecordedDescription, replaceIfRecordedDescription,
					),
				},
			},
			"request_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Client-generated ID Lucidity uses to deduplicate the buffer request, so a retried request never creates a second buffer. " +
					"Generated automatically if not set. Set it yourself to make it descriptive and stable (e.g. \"restore-orders-db-2026-10-23\"): if an apply fails after Lucidity received the request, " +
					"re-applying with the same request_id gets the same buffer back instead of a conflict. Must be unique per buffer. " +
					"Only used when the buffer is requested, so changing it later updates state only and leaves the existing buffer alone.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"buffer_id": schema.StringAttribute{
				Computed:    true,
				Description: "Lucidity's identifier for the buffer, saved to state as soon as Lucidity returns it. Lucidity returns it only once and has no way to look it up later, so keep it (e.g. as an output) if anything outside this configuration needs it. Also the import ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
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

// ValidateConfig enforces the per-cloud rules the schema alone can't
// express: resource_group is required for AZURE and meaningless elsewhere,
// and an AWS account ID must be 12 digits (same check as lucidity_tenant).
func (r *tempBufferResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg tempBufferResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A missing block is reported by its IsRequired validator.
	id := cfg.InstanceIdentifier
	if id == nil || id.CloudProvider.IsUnknown() || id.CloudProvider.IsNull() {
		return
	}

	switch id.CloudProvider.ValueString() {
	case "AZURE":
		if isMissingRequiredString(id.ResourceGroup) {
			resp.Diagnostics.AddAttributeError(
				path.Root("instance_identifier").AtName("resource_group"),
				"Missing resource_group for Azure",
				"instance_identifier.resource_group is required when cloud_provider is \"AZURE\": Lucidity needs the resource group to find the VM.",
			)
		}
	default:
		if !id.ResourceGroup.IsNull() && !id.ResourceGroup.IsUnknown() {
			resp.Diagnostics.AddAttributeError(
				path.Root("instance_identifier").AtName("resource_group"),
				"resource_group is Azure-only",
				fmt.Sprintf("instance_identifier.resource_group only applies when cloud_provider is \"AZURE\", got cloud_provider %q. Remove it.", id.CloudProvider.ValueString()),
			)
		}
	}

	if id.CloudProvider.ValueString() == "AWS" {
		if accountID := id.CloudProviderAccountID; !accountID.IsUnknown() && !awsAccountIDPattern.MatchString(accountID.ValueString()) {
			resp.Diagnostics.AddAttributeError(
				path.Root("instance_identifier").AtName("cloud_provider_account_id"),
				"Invalid AWS account ID format",
				fmt.Sprintf("instance_identifier.cloud_provider_account_id must be exactly 12 digits for cloud_provider = \"AWS\" (e.g. \"123456789012\"), got: %q.", accountID.ValueString()),
			)
		}
	}
}

func (r *tempBufferResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		resp.Diagnostics.AddError("Unexpected import ID format", "Expected the buffer ID returned by Lucidity, e.g. \"buf-abc123def456\".")
		return
	}
	// The status call is the only place the instance ID comes from; it also
	// fails fast on a wrong buffer ID instead of importing an empty shell.
	status, err := r.client.GetTempBufferStatus(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to import Lucidity temporary buffer", fmt.Sprintf("Reading buffer %s failed: %s", req.ID, err.Error()))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("buffer_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_identifier").AtName("instance_id"), status.InstanceID)...)
	resp.Diagnostics.AddWarning(
		"Some lucidity_temp_buffer fields are assumed, not read, on import",
		"Lucidity's status API returns the buffer's instance ID, status and free space, which are read on import. "+
			"It does not return the cloud provider, account ID, resource group, requested size or request ID: those are assumed to match your config and simply recorded on the next apply (never compared, never forcing a replacement).",
	)
}

func (r *tempBufferResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tempBufferResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The request ID must be fixed before the first call: it is what makes
	// RequestTempBuffer's 5xx retry safe (see client.RequestTempBuffer).
	if plan.RequestID.IsUnknown() || plan.RequestID.IsNull() || plan.RequestID.ValueString() == "" {
		id, err := newRequestID()
		if err != nil {
			resp.Diagnostics.AddError("Unable to generate a request ID", err.Error())
			return
		}
		plan.RequestID = types.StringValue(id)
	}

	id := plan.InstanceIdentifier
	request := client.RequestTempBufferRequest{
		InstanceIdentifier: client.InstanceIdentifier{
			CloudProviderAccountID: id.CloudProviderAccountID.ValueString(),
			CloudProvider:          id.CloudProvider.ValueString(),
			InstanceID:             id.InstanceID.ValueString(),
			ResourceGroup:          id.ResourceGroup.ValueString(),
		},
		RequestBufferSizeInGiB: plan.RequestBufferSizeInGiB.ValueInt64(),
		RequestID:              plan.RequestID.ValueString(),
	}
	result, err := r.client.RequestTempBuffer(ctx, request)
	// The buffer ID comes back only in this response. If the outcome is
	// unknown, Lucidity may have created the buffer and the ID is lost:
	// re-send with the same request_id to get it back (see the var block).
	for retry := 0; err != nil && client.IsOutcomeUnknown(ctx, err) && retry < tempBufferRequestRetries; retry++ {
		select {
		case <-time.After(tempBufferPollInterval):
		case <-ctx.Done():
			resp.Diagnostics.AddError("Unable to request Lucidity temporary buffer", ctx.Err().Error())
			return
		}
		result, err = r.client.RequestTempBuffer(ctx, request)
	}
	if err != nil {
		handleTempBufferRequestError(&resp.Diagnostics, err, id.InstanceID.ValueString(), plan.RequestID.ValueString())
		return
	}

	// Save the buffer ID before waiting: if the wait fails or times out,
	// Terraform keeps it in state (marked tainted), so a later destroy or
	// replace can still revoke it rather than leaving it to bill silently.
	plan.BufferID = types.StringValue(result.BufferID)
	plan.Status = types.StringValue(client.TempBufferStatusProvisioning)
	plan.CurrentFreeSpaceInGiB = types.Int64Null()
	plan.FailureReason = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	status, diags := r.waitForTempBuffer(ctx, result.BufferID)
	if status != nil {
		applyTempBufferStatus(&plan, status, false)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	resp.Diagnostics.Append(diags...)
	if status != nil && status.Status == client.TempBufferStatusFailed {
		resp.Diagnostics.AddError(
			"Lucidity temporary buffer failed",
			fmt.Sprintf(
				"Buffer %s on instance %s ended in FAILED: %s. It is kept in state as tainted, so the next apply revokes it and requests a new one. Check the reason before retrying.",
				result.BufferID, id.InstanceID.ValueString(), failureReasonText(status),
			),
		)
	}
}

// waitForTempBuffer polls the status API until the buffer leaves
// PROVISIONING (ACTIVE, FAILED or REVOKED), the timeout passes, or ctx is
// cancelled. Returns the last status seen (nil if none), with an error
// diagnostic on timeout or a failed status call.
func (r *tempBufferResource) waitForTempBuffer(ctx context.Context, bufferID string) (*client.TempBufferStatus, diag.Diagnostics) {
	var diags diag.Diagnostics
	deadline := time.Now().Add(tempBufferPollTimeout)
	var last *client.TempBufferStatus
	for {
		status, err := r.client.GetTempBufferStatus(ctx, bufferID)
		if err != nil {
			diags.AddError(
				"Unable to read Lucidity temporary buffer status",
				fmt.Sprintf("Checking buffer %s failed: %s. The buffer may still be provisioning; it is kept in state so it can be revoked.", bufferID, err.Error()),
			)
			return last, diags
		}
		last = status
		if status.Status != client.TempBufferStatusProvisioning {
			return last, diags
		}
		if time.Now().Add(tempBufferPollInterval).After(deadline) {
			diags.AddError(
				"Lucidity temporary buffer still provisioning",
				fmt.Sprintf(
					"Buffer %s was still PROVISIONING after %s. Lucidity recommends contacting support (support@lucidity.cloud) if a buffer stays in PROVISIONING beyond 30 minutes. "+
						"It is kept in state as tainted: the next apply revokes it and requests a new one, so check its status with support first if you'd rather keep it.",
					bufferID, tempBufferPollTimeout,
				),
			)
			return last, diags
		}
		select {
		case <-time.After(tempBufferPollInterval):
		case <-ctx.Done():
			diags.AddError("Unable to read Lucidity temporary buffer status", ctx.Err().Error())
			return last, diags
		}
	}
}

func (r *tempBufferResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tempBufferResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	bufferID := state.BufferID.ValueString()
	status, err := r.client.GetTempBufferStatus(ctx, bufferID)
	if err != nil {
		if isHTTPStatus(err, 404) {
			resp.Diagnostics.AddWarning(
				"Lucidity temporary buffer no longer found",
				fmt.Sprintf("Buffer %s no longer exists on Lucidity. Removing it from Terraform state; the next apply requests a new buffer if this resource is still in your configuration.", bufferID),
			)
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read Lucidity temporary buffer status", err.Error())
		return
	}

	if status.Status == client.TempBufferStatusRevoked {
		resp.Diagnostics.AddWarning(
			"Lucidity temporary buffer was revoked",
			fmt.Sprintf("Buffer %s was revoked outside Terraform. Removing it from Terraform state; the next apply requests a new buffer if this resource is still in your configuration. Remove the resource from your configuration if the buffer is no longer needed.", bufferID),
		)
		resp.State.RemoveResource(ctx)
		return
	}

	applyTempBufferStatus(&state, status, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)

	if status.Status == client.TempBufferStatusFailed {
		resp.Diagnostics.AddWarning(
			"Lucidity temporary buffer is FAILED",
			fmt.Sprintf("Buffer %s is FAILED: %s. Run `terraform apply -replace` on this resource to revoke it and request a new one.", bufferID, failureReasonText(status)),
		)
	}
}

// Update only runs when no attribute requires replacement, which in practice
// means a config value is being recorded for the first time after import.
// Nothing is sent to Lucidity — the buffer API has no update call.
func (r *tempBufferResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state tempBufferResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.BufferID = state.BufferID
	plan.Status = state.Status
	plan.CurrentFreeSpaceInGiB = state.CurrentFreeSpaceInGiB
	plan.FailureReason = state.FailureReason
	if plan.RequestID.IsUnknown() {
		plan.RequestID = state.RequestID
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tempBufferResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tempBufferResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	bufferID := state.BufferID.ValueString()

	status, err := r.client.GetTempBufferStatus(ctx, bufferID)
	if err != nil {
		if isHTTPStatus(err, 404) {
			return // already gone — nothing to revoke
		}
		resp.Diagnostics.AddError("Unable to read Lucidity temporary buffer status", err.Error())
		return
	}

	switch status.Status {
	case client.TempBufferStatusRevoked:
		return
	case client.TempBufferStatusProvisioning:
		// The docs say a request can't be cancelled: wait for it to finish
		// (or fail), then revoke.
		var diags diag.Diagnostics
		status, diags = r.waitForTempBuffer(ctx, bufferID)
		if diags.HasError() {
			resp.Diagnostics.Append(diags...)
			return
		}
		if status.Status == client.TempBufferStatusRevoked {
			return
		}
	}

	if _, err := r.client.RevokeTempBuffer(ctx, bufferID); err != nil {
		if isHTTPStatus(err, 404) {
			return
		}
		if status.Status == client.TempBufferStatusFailed {
			// Unknown whether a FAILED buffer needs (or accepts) a revoke;
			// don't block the destroy on it.
			resp.Diagnostics.AddWarning(
				"Could not revoke FAILED temporary buffer",
				fmt.Sprintf("Revoking FAILED buffer %s returned: %s. Removing it from Terraform state; check the instance in the Lucidity dashboard.", bufferID, err.Error()),
			)
			return
		}
		resp.Diagnostics.AddError("Unable to revoke Lucidity temporary buffer", err.Error())
		return
	}

	resp.Diagnostics.Append(r.waitForRevoked(ctx, bufferID)...)
}

// waitForRevoked polls until the buffer reads REVOKED (or is gone). The
// docs don't say whether revoke completes at once, so this is bounded and
// only warns on timeout: the revoke itself was accepted.
func (r *tempBufferResource) waitForRevoked(ctx context.Context, bufferID string) diag.Diagnostics {
	var diags diag.Diagnostics
	deadline := time.Now().Add(tempBufferRevokeTimeout)
	for {
		status, err := r.client.GetTempBufferStatus(ctx, bufferID)
		if err != nil {
			if !isHTTPStatus(err, 404) {
				diags.AddWarning("Unable to confirm temporary buffer revoke", fmt.Sprintf("Buffer %s was revoked, but reading its status afterwards failed: %s", bufferID, err.Error()))
			}
			return diags
		}
		if status.Status == client.TempBufferStatusRevoked {
			return diags
		}
		if time.Now().Add(tempBufferPollInterval).After(deadline) {
			diags.AddWarning(
				"Temporary buffer revoke still in progress",
				fmt.Sprintf("Lucidity accepted the revoke of buffer %s, but its status was still %s after %s. If a new buffer is requested for the same instance now, it may fail with a conflict until the revoke finishes.", bufferID, status.Status, tempBufferRevokeTimeout),
			)
			return diags
		}
		select {
		case <-time.After(tempBufferPollInterval):
		case <-ctx.Done():
			diags.AddWarning("Unable to confirm temporary buffer revoke", ctx.Err().Error())
			return diags
		}
	}
}

// applyTempBufferStatus copies the status response into model. readInstanceID
// is true only for Read, where Lucidity is the source of truth (so an
// imported buffer gets its instance ID, and a mismatch shows as drift);
// after Create the planned value is kept, or the framework would flag an
// inconsistent result if Lucidity normalizes it.
func applyTempBufferStatus(model *tempBufferResourceModel, status *client.TempBufferStatus, readInstanceID bool) {
	model.Status = types.StringValue(status.Status)
	model.CurrentFreeSpaceInGiB = types.Int64Value(status.CurrentFreeSpaceInGiB)
	model.FailureReason = types.StringPointerValue(status.FailureReason)
	if status.BufferID != "" {
		model.BufferID = types.StringValue(status.BufferID)
	}
	if readInstanceID && status.InstanceID != "" {
		if model.InstanceIdentifier == nil {
			model.InstanceIdentifier = &instanceIdentifierModel{}
		}
		model.InstanceIdentifier.InstanceID = types.StringValue(status.InstanceID)
	}
}

func failureReasonText(status *client.TempBufferStatus) string {
	if status.FailureReason == nil || *status.FailureReason == "" {
		return "no reason given"
	}
	return *status.FailureReason
}

func isHTTPStatus(err error, code int) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.HTTPStatus == code
}

// newRequestID returns "terraform-" plus a random (version 4) UUID.
func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("terraform-%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// handleTempBufferRequestError maps the request call's documented error
// statuses to what the user should do. Matches on HTTP status rather than
// error code: the docs list statuses, not code strings.
func handleTempBufferRequestError(diags *diag.Diagnostics, err error, instanceID, requestID string) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.HTTPStatus {
		case 409:
			diags.AddError(
				"Another temporary buffer is active on this instance",
				fmt.Sprintf(
					"Lucidity allows only one active buffer per instance, and instance %s already has one (%s). Either (a) two resources in your configuration target the same instance — remove the duplicate; "+
						"(b) an earlier apply created it but failed before saving it — find its buffer ID in the Lucidity dashboard and `terraform import lucidity_temp_buffer.<name> <buffer-id>`; or "+
						"(c) it was requested outside Terraform — revoke it, or wait for that operation to finish; or (d) a previous buffer on this instance was just revoked (e.g. by a replacement) and the revoke hasn't finished — wait and re-apply.",
					instanceID, apiErr.Error(),
				),
			)
			return
		case 404:
			diags.AddError(
				"Instance not found or not managed by Lucidity",
				fmt.Sprintf(
					"Lucidity could not find instance %s (%s). Check that the instance ID is correct in your cloud console, that the instance appears in the Lucidity dashboard, and that cloud_provider, cloud_provider_account_id (and resource_group for Azure) are right.",
					instanceID, apiErr.Error(),
				),
			)
			return
		case 400:
			diags.AddError(
				"Lucidity rejected the buffer request as invalid",
				fmt.Sprintf("Lucidity answered 400 to the buffer request for instance %s (request ID %s): %s", instanceID, requestID, apiErr.Error()),
			)
			return
		}
		diags.AddError("Lucidity API error requesting temporary buffer", apiErr.Error())
		return
	}
	diags.AddError("Unable to request Lucidity temporary buffer", err.Error())
}
