package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/luciditycloud/terraform-provider-lucidity/internal/client"
)

const tempBufferResourceTypeName = "lucidity_temp_buffer"

func instanceIdentifierType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"cloud_provider":            tftypes.String,
			"cloud_provider_account_id": tftypes.String,
			"instance_id":               tftypes.String,
			"resource_group":            tftypes.String,
		},
	}
}

func tempBufferType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"instance_identifier":        instanceIdentifierType(),
			"request_buffer_size_in_gib": tftypes.Number,
			"request_id":                 tftypes.String,
			"buffer_id":                  tftypes.String,
			"status":                     tftypes.String,
			"current_free_space_in_gib":  tftypes.Number,
			"failure_reason":             tftypes.String,
		},
	}
}

// tempBufferValue builds a full object value: an AWS instance and a 50 GiB
// buffer, with computed attributes null. idOverrides/overrides replace
// individual fields.
func tempBufferValue(overrides map[string]tftypes.Value, idOverrides map[string]tftypes.Value) tftypes.Value {
	id := map[string]tftypes.Value{
		"cloud_provider":            strVal("AWS"),
		"cloud_provider_account_id": strVal("123456789012"),
		"instance_id":               strVal("i-0abcd1234efgh5678"),
		"resource_group":            tftypes.NewValue(tftypes.String, nil),
	}
	for k, v := range idOverrides {
		id[k] = v
	}
	objType := tempBufferType()
	full := map[string]tftypes.Value{}
	for name, ty := range objType.AttributeTypes {
		full[name] = tftypes.NewValue(ty, nil)
	}
	full["instance_identifier"] = tftypes.NewValue(instanceIdentifierType(), id)
	full["request_buffer_size_in_gib"] = numVal(50)
	for k, v := range overrides {
		full[k] = v
	}
	return tftypes.NewValue(objType, full)
}

func tempBufferConfig(t *testing.T, overrides, idOverrides map[string]tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	dv, err := tfprotov6.NewDynamicValue(tempBufferType(), tempBufferValue(overrides, idOverrides))
	if err != nil {
		t.Fatalf("NewDynamicValue: %v", err)
	}
	return &dv
}

func validateTempBuffer(t *testing.T, overrides, idOverrides map[string]tftypes.Value) []*tfprotov6.Diagnostic {
	t.Helper()
	resp, err := newTestProviderServer(t).ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tempBufferResourceTypeName,
		Config:   tempBufferConfig(t, overrides, idOverrides),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	return resp.Diagnostics
}

func TestTempBufferRPC_GetProviderSchema_RegistersResourceAndDataSource(t *testing.T) {
	resp, err := newTestProviderServer(t).GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	if hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected no error diagnostic, got: %+v", resp.Diagnostics)
	}
	if _, ok := resp.ResourceSchemas[tempBufferResourceTypeName]; !ok {
		t.Fatalf("expected %q to be a registered resource", tempBufferResourceTypeName)
	}
	if _, ok := resp.DataSourceSchemas[tempBufferResourceTypeName]; !ok {
		t.Fatalf("expected %q to be a registered data source", tempBufferResourceTypeName)
	}
}

func TestTempBufferRPC_ValidateConfig_AcceptsAWS(t *testing.T) {
	if diags := validateTempBuffer(t, nil, nil); hasErrorDiagnostic(diags) {
		t.Fatalf("expected no error diagnostic, got: %+v", diags)
	}
}

func TestTempBufferRPC_ValidateConfig_AcceptsAzureWithResourceGroup(t *testing.T) {
	diags := validateTempBuffer(t, nil, map[string]tftypes.Value{
		"cloud_provider":            strVal("AZURE"),
		"cloud_provider_account_id": strVal("a1b2c3d4-5678-90ef-gh12-3456ijklmnop"),
		"instance_id":               strVal("my-vm-instance"),
		"resource_group":            strVal("my-resource-group"),
	})
	if hasErrorDiagnostic(diags) {
		t.Fatalf("expected no error diagnostic, got: %+v", diags)
	}
}

func TestTempBufferRPC_ValidateConfig_RejectsAzureWithoutResourceGroup(t *testing.T) {
	diags := validateTempBuffer(t, nil, map[string]tftypes.Value{
		"cloud_provider":            strVal("AZURE"),
		"cloud_provider_account_id": strVal("a1b2c3d4-5678-90ef-gh12-3456ijklmnop"),
		"instance_id":               strVal("my-vm-instance"),
	})
	if !hasErrorDiagnostic(diags) {
		t.Fatal("expected an error for AZURE without resource_group")
	}
}

func TestTempBufferRPC_ValidateConfig_RejectsResourceGroupOutsideAzure(t *testing.T) {
	diags := validateTempBuffer(t, nil, map[string]tftypes.Value{"resource_group": strVal("rg")})
	if !hasErrorDiagnostic(diags) {
		t.Fatal("expected an error for resource_group with cloud_provider AWS")
	}
}

func TestTempBufferRPC_ValidateConfig_RejectsMalformedAWSAccountID(t *testing.T) {
	diags := validateTempBuffer(t, nil, map[string]tftypes.Value{"cloud_provider_account_id": strVal("12345")})
	if !hasErrorDiagnostic(diags) {
		t.Fatal("expected an error for a non-12-digit AWS account ID")
	}
}

func TestTempBufferRPC_ValidateConfig_RejectsZeroSize(t *testing.T) {
	diags := validateTempBuffer(t, map[string]tftypes.Value{"request_buffer_size_in_gib": numVal(0)}, nil)
	if !hasErrorDiagnostic(diags) {
		t.Fatal("expected an error for a 0 GiB buffer")
	}
}

// mockBuffer is one buffer in the fake Lucidity backend below.
type mockBuffer struct {
	ID         string
	InstanceID string
	Status     string
	Reason     *string
	// statusCalls counts status reads; the buffer turns ACTIVE (or FAILED)
	// once it reaches provisionAfter.
	statusCalls int
	revokeReads int
}

// mockBufferServer is a minimal, stateful stand-in for the temporary buffer
// API, answering with the bare (unwrapped) bodies the docs show.
type mockBufferServer struct {
	mu      sync.Mutex
	buffers map[string]*mockBuffer
	seq     int

	provisionAfter int    // status reads before a buffer leaves PROVISIONING
	failWith       string // non-empty: buffers end FAILED with this reason
	requestIDs     []string
	revokeCalls    int

	// byRequestID deduplicates like Lucidity documents: the same requestId
	// gets the same buffer back.
	byRequestID map[string]string
	// dropFirstRequest creates the buffer on the first request, then closes
	// the connection without answering (a lost response).
	dropFirstRequest bool
	// revokeLag keeps a revoked buffer reading ACTIVE for this many status
	// reads before it reads REVOKED.
	revokeLag int
}

func newMockBufferServer(t *testing.T) (*httptest.Server, *mockBufferServer) {
	t.Helper()
	m := &mockBufferServer{buffers: map[string]*mockBuffer{}, byRequestID: map[string]string{}, provisionAfter: 2}
	mux := http.NewServeMux()

	mux.HandleFunc("/external/api/v1/auth/user-token/refresh", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "mock-access-token"})
	})

	mux.HandleFunc("/external/api/v1/autoscaler/instance/buffer/request", func(w http.ResponseWriter, r *http.Request) {
		var req client.RequestTempBufferRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.requestIDs = append(m.requestIDs, req.RequestID)
		if id, ok := m.byRequestID[req.RequestID]; ok && req.RequestID != "" {
			writeMockJSON(w, http.StatusOK, map[string]any{"bufferId": id})
			return
		}
		for _, b := range m.buffers {
			if b.InstanceID == req.InstanceIdentifier.InstanceID && (b.Status == client.TempBufferStatusActive || b.Status == client.TempBufferStatusProvisioning) {
				writeMockJSON(w, http.StatusConflict, map[string]any{"code": "CONFLICT", "message": "Another buffer is already active on this instance."})
				return
			}
		}
		m.seq++
		b := &mockBuffer{ID: "buf-" + string(rune('a'+m.seq-1)), InstanceID: req.InstanceIdentifier.InstanceID, Status: client.TempBufferStatusProvisioning}
		m.buffers[b.ID] = b
		m.byRequestID[req.RequestID] = b.ID
		if m.dropFirstRequest {
			m.dropFirstRequest = false
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		writeMockJSON(w, http.StatusOK, map[string]any{"bufferId": b.ID})
	})

	mux.HandleFunc("/external/api/v1/autoscaler/instances/buffer/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/external/api/v1/autoscaler/instances/buffer/")
		id, action, _ := strings.Cut(rest, "/")
		m.mu.Lock()
		defer m.mu.Unlock()
		b, ok := m.buffers[id]
		if !ok {
			writeMockJSON(w, http.StatusNotFound, map[string]any{"code": "NOT_FOUND", "message": "Buffer not found."})
			return
		}
		switch action {
		case "status":
			b.statusCalls++
			if b.Status == "REVOKING" {
				if b.revokeReads >= m.revokeLag {
					b.Status = client.TempBufferStatusRevoked
				}
				b.revokeReads++
			}
			if b.Status == client.TempBufferStatusProvisioning && b.statusCalls >= m.provisionAfter {
				if m.failWith != "" {
					reason := m.failWith
					b.Status, b.Reason = client.TempBufferStatusFailed, &reason
				} else {
					b.Status = client.TempBufferStatusActive
				}
			}
			shown := b.Status
			if shown == "REVOKING" {
				shown = client.TempBufferStatusActive
			}
			writeMockJSON(w, http.StatusOK, map[string]any{
				"instanceId":            b.InstanceID,
				"status":                shown,
				"currentFreeSpaceInGiB": 75,
				"bufferId":              b.ID,
				"failureReason":         b.Reason,
			})
		case "revoke":
			m.revokeCalls++
			b.Status = client.TempBufferStatusRevoked
			if m.revokeLag > 0 {
				b.Status = "REVOKING"
			}
			writeMockJSON(w, http.StatusOK, map[string]any{"message": "Buffer revoked successfully"})
		default:
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, m
}

func writeMockJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func tempBufferSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := &tempBufferResource{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

// fastTempBufferPolling shrinks the status poll so tests don't wait 30s.
func fastTempBufferPolling(t *testing.T) {
	t.Helper()
	interval, timeout, revoke := tempBufferPollInterval, tempBufferPollTimeout, tempBufferRevokeTimeout
	tempBufferPollInterval, tempBufferPollTimeout, tempBufferRevokeTimeout = time.Millisecond, time.Second, time.Second
	t.Cleanup(func() {
		tempBufferPollInterval, tempBufferPollTimeout, tempBufferRevokeTimeout = interval, timeout, revoke
	})
}

func createTempBuffer(t *testing.T, r *tempBufferResource, sch schema.Schema, plan tftypes.Value) resource.CreateResponse {
	t.Helper()
	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: sch}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Raw: plan, Schema: sch}}, &resp)
	return resp
}

// TestTempBufferLifecycle drives one buffer through request -> wait for
// ACTIVE -> read -> second request on the same instance (409) -> revoke ->
// read after revoke (removed from state).
func TestTempBufferLifecycle(t *testing.T) {
	fastTempBufferPolling(t)
	srv, m := newMockBufferServer(t)
	c := client.NewClient(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))
	r := &tempBufferResource{client: c}
	sch := tempBufferSchema(t)
	ctx := context.Background()

	// --- Create: waits until ACTIVE, generates a request ID ---
	createResp := createTempBuffer(t, r, sch, tempBufferValue(nil, nil))
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create: %+v", createResp.Diagnostics)
	}
	var state tempBufferResourceModel
	createResp.State.Get(ctx, &state)
	if state.Status.ValueString() != client.TempBufferStatusActive || state.BufferID.ValueString() == "" {
		t.Fatalf("got status %q bufferId %q after create, want ACTIVE and a buffer ID", state.Status.ValueString(), state.BufferID.ValueString())
	}
	if !strings.HasPrefix(state.RequestID.ValueString(), "terraform-") || len(m.requestIDs) != 1 || m.requestIDs[0] != state.RequestID.ValueString() {
		t.Fatalf("got request_id %q, sent %v; want a generated terraform-… ID sent once", state.RequestID.ValueString(), m.requestIDs)
	}
	if state.CurrentFreeSpaceInGiB.ValueInt64() != 75 {
		t.Fatalf("got free space %d, want 75", state.CurrentFreeSpaceInGiB.ValueInt64())
	}

	// --- Read ---
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: sch}
	r.Read(ctx, resource.ReadRequest{State: tfsdk.State{Raw: createResp.State.Raw, Schema: sch}}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read: %+v", readResp.Diagnostics)
	}

	// --- Second buffer on the same instance: 409 ---
	conflict := createTempBuffer(t, r, sch, tempBufferValue(nil, nil))
	if !conflict.Diagnostics.HasError() || !strings.Contains(conflict.Diagnostics.Errors()[0].Summary(), "Another temporary buffer is active") {
		t.Fatalf("expected the one-buffer-per-instance error, got: %+v", conflict.Diagnostics)
	}

	// --- Delete: revokes ---
	var deleteResp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Raw: createResp.State.Raw, Schema: sch}}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete: %+v", deleteResp.Diagnostics)
	}
	if m.revokeCalls != 1 {
		t.Fatalf("got %d revoke calls, want 1", m.revokeCalls)
	}

	// --- Read after revoke: removed from state ---
	var afterRevoke resource.ReadResponse
	afterRevoke.State = tfsdk.State{Raw: createResp.State.Raw, Schema: sch}
	r.Read(ctx, resource.ReadRequest{State: tfsdk.State{Raw: createResp.State.Raw, Schema: sch}}, &afterRevoke)
	if afterRevoke.Diagnostics.HasError() {
		t.Fatalf("Read after revoke: %+v", afterRevoke.Diagnostics)
	}
	if !afterRevoke.State.Raw.IsNull() {
		t.Fatal("expected a revoked buffer to be removed from state")
	}

	// --- Delete again: already revoked, no second revoke call ---
	var deleteAgain resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Raw: createResp.State.Raw, Schema: sch}}, &deleteAgain)
	if deleteAgain.Diagnostics.HasError() || m.revokeCalls != 1 {
		t.Fatalf("expected a no-op delete for a revoked buffer, got %+v and %d revoke calls", deleteAgain.Diagnostics, m.revokeCalls)
	}
}

func TestTempBufferCreate_UsesConfiguredRequestID(t *testing.T) {
	fastTempBufferPolling(t)
	srv, m := newMockBufferServer(t)
	r := &tempBufferResource{client: client.NewClient(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))}

	resp := createTempBuffer(t, r, tempBufferSchema(t), tempBufferValue(map[string]tftypes.Value{"request_id": strVal("restore-orders-db")}, nil))
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %+v", resp.Diagnostics)
	}
	if len(m.requestIDs) != 1 || m.requestIDs[0] != "restore-orders-db" {
		t.Fatalf("sent request IDs %v, want [restore-orders-db]", m.requestIDs)
	}
}

// A FAILED buffer errors but stays in state with its ID, so Terraform marks
// it tainted and a later apply can revoke and replace it.
func TestTempBufferCreate_FailedKeepsBufferInState(t *testing.T) {
	fastTempBufferPolling(t)
	srv, m := newMockBufferServer(t)
	m.failWith = "insufficient capacity"
	r := &tempBufferResource{client: client.NewClient(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))}
	ctx := context.Background()

	resp := createTempBuffer(t, r, tempBufferSchema(t), tempBufferValue(nil, nil))
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "insufficient capacity") {
		t.Fatalf("expected a FAILED error naming the reason, got: %+v", resp.Diagnostics)
	}
	var state tempBufferResourceModel
	resp.State.Get(ctx, &state)
	if state.BufferID.ValueString() == "" || state.Status.ValueString() != client.TempBufferStatusFailed {
		t.Fatalf("got bufferId %q status %q, want the FAILED buffer kept in state", state.BufferID.ValueString(), state.Status.ValueString())
	}
}

// Timing out while PROVISIONING still leaves the buffer ID in state.
func TestTempBufferCreate_TimeoutKeepsBufferInState(t *testing.T) {
	fastTempBufferPolling(t)
	tempBufferPollTimeout = 5 * time.Millisecond
	srv, m := newMockBufferServer(t)
	m.provisionAfter = 1 << 30 // never leaves PROVISIONING
	r := &tempBufferResource{client: client.NewClient(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))}
	ctx := context.Background()

	resp := createTempBuffer(t, r, tempBufferSchema(t), tempBufferValue(nil, nil))
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "still provisioning") {
		t.Fatalf("expected a still-provisioning error, got: %+v", resp.Diagnostics)
	}
	var state tempBufferResourceModel
	resp.State.Get(ctx, &state)
	if state.BufferID.ValueString() == "" || state.Status.ValueString() != client.TempBufferStatusProvisioning {
		t.Fatalf("got bufferId %q status %q, want the PROVISIONING buffer kept in state", state.BufferID.ValueString(), state.Status.ValueString())
	}
}

func TestTempBufferCreate_InstanceNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/external/api/v1/auth/user-token/refresh", func(w http.ResponseWriter, r *http.Request) {
		writeMockJSON(w, http.StatusOK, map[string]any{"accessToken": "mock-access-token"})
	})
	mux.HandleFunc("/external/api/v1/autoscaler/instance/buffer/request", func(w http.ResponseWriter, r *http.Request) {
		writeMockJSON(w, http.StatusNotFound, map[string]any{"code": "NOT_FOUND", "message": "Instance not found or not managed by Lucidity."})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r := &tempBufferResource{client: client.NewClient(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))}

	resp := createTempBuffer(t, r, tempBufferSchema(t), tempBufferValue(nil, nil))
	if !resp.Diagnostics.HasError() || resp.Diagnostics.Errors()[0].Summary() != "Instance not found or not managed by Lucidity" {
		t.Fatalf("expected the instance-not-found error, got: %+v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Fatal("expected nothing saved to state when the request itself fails")
	}
}

// A lost response (connection dropped after Lucidity created the buffer) is
// recovered by re-sending the same request_id: the same buffer comes back,
// and no second buffer is created.
func TestTempBufferCreate_LostResponseRecoveredByRequestID(t *testing.T) {
	fastTempBufferPolling(t)
	srv, m := newMockBufferServer(t)
	m.dropFirstRequest = true
	r := &tempBufferResource{client: client.NewClient(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))}
	ctx := context.Background()

	resp := createTempBuffer(t, r, tempBufferSchema(t), tempBufferValue(nil, nil))
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %+v", resp.Diagnostics)
	}
	var state tempBufferResourceModel
	resp.State.Get(ctx, &state)
	if len(m.buffers) != 1 || len(m.requestIDs) != 2 || m.requestIDs[0] != m.requestIDs[1] {
		t.Fatalf("got %d buffers and request IDs %v; want 1 buffer and the same request ID sent twice", len(m.buffers), m.requestIDs)
	}
	if state.Status.ValueString() != client.TempBufferStatusActive || m.buffers[state.BufferID.ValueString()] == nil {
		t.Fatalf("got bufferId %q status %q; want the recovered buffer, ACTIVE", state.BufferID.ValueString(), state.Status.ValueString())
	}
}

// Delete waits until the revoke shows as REVOKED, so a replacement's new
// request doesn't hit the one-buffer-per-instance conflict.
func TestTempBufferDelete_WaitsForRevoked(t *testing.T) {
	fastTempBufferPolling(t)
	srv, m := newMockBufferServer(t)
	m.revokeLag = 3
	r := &tempBufferResource{client: client.NewClient(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))}
	sch := tempBufferSchema(t)
	ctx := context.Background()

	created := createTempBuffer(t, r, sch, tempBufferValue(nil, nil))
	if created.Diagnostics.HasError() {
		t.Fatalf("Create: %+v", created.Diagnostics)
	}
	var deleteResp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Raw: created.State.Raw, Schema: sch}}, &deleteResp)
	if deleteResp.Diagnostics.HasError() || len(deleteResp.Diagnostics) != 0 {
		t.Fatalf("Delete: want no diagnostics, got %+v", deleteResp.Diagnostics)
	}

	// The instance is free again: a new buffer can be requested at once.
	again := createTempBuffer(t, r, sch, tempBufferValue(nil, nil))
	if again.Diagnostics.HasError() {
		t.Fatalf("Create after delete: %+v", again.Diagnostics)
	}
}

func planTempBufferUpdate(t *testing.T, prior, cfg tftypes.Value) *tfprotov6.PlanResourceChangeResponse {
	t.Helper()
	objType := tempBufferType()
	priorDV, err := tfprotov6.NewDynamicValue(objType, prior)
	if err != nil {
		t.Fatalf("NewDynamicValue(prior): %v", err)
	}
	cfgDV, err := tfprotov6.NewDynamicValue(objType, cfg)
	if err != nil {
		t.Fatalf("NewDynamicValue(config): %v", err)
	}
	resp, err := newTestProviderServer(t).PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         tempBufferResourceTypeName,
		PriorState:       &priorDV,
		ProposedNewState: &cfgDV,
		Config:           &cfgDV,
	})
	if err != nil {
		t.Fatalf("PlanResourceChange: %v", err)
	}
	return resp
}

// tempBufferState is a prior state for an existing ACTIVE buffer.
func tempBufferState(overrides map[string]tftypes.Value) tftypes.Value {
	set := map[string]tftypes.Value{
		"request_id":                strVal("restore-1"),
		"buffer_id":                 strVal("buf-a"),
		"status":                    strVal(client.TempBufferStatusActive),
		"current_free_space_in_gib": numVal(75),
		"failure_reason":            tftypes.NewValue(tftypes.String, nil),
	}
	for k, v := range overrides {
		set[k] = v
	}
	return tempBufferValue(set, nil)
}

func TestTempBufferRPC_PlanResourceChange_RequestIDChangeIsInPlace(t *testing.T) {
	resp := planTempBufferUpdate(t, tempBufferState(nil), tempBufferState(map[string]tftypes.Value{"request_id": strVal("restore-2")}))
	if hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("unexpected error: %+v", resp.Diagnostics)
	}
	if len(resp.RequiresReplace) != 0 {
		t.Fatalf("changing request_id must not replace the buffer, got RequiresReplace=%v", resp.RequiresReplace)
	}
}

func TestTempBufferRPC_PlanResourceChange_SizeChangeReplaces(t *testing.T) {
	resp := planTempBufferUpdate(t, tempBufferState(nil), tempBufferState(map[string]tftypes.Value{"request_buffer_size_in_gib": numVal(100)}))
	if hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("unexpected error: %+v", resp.Diagnostics)
	}
	found := false
	for _, p := range resp.RequiresReplace {
		if p.String() == `AttributeName("request_buffer_size_in_gib")` {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a size change to require replacement, got RequiresReplace=%v", resp.RequiresReplace)
	}
}
