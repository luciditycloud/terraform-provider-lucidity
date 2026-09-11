package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/luciditycloud/terraform-provider-lucidity/internal/client"
)

// This file drives tenantResource's actual Create/Read/Update/Delete Go
// methods directly against a stateful local mock server — not a live
// Lucidity account, and not the tfprotov6 RPC layer (lucidity_dashboard_url
// is a closed set of real deployment URLs with no override, so a mock
// server can never be reached through the normal Configure() path; this
// bypasses Configure() and wires the client directly instead). It closes
// the gap the schema/validator RPC tests above don't: the actual business
// logic in Create/Read/Update/Delete — the ACTIVE/INACTIVE pre-check,
// CONFLICT message parsing, and the three-tier destroy safety — had zero
// direct test coverage before this file existed.

// mockTenant is one record in the fake Lucidity backend below.
type mockTenant struct {
	TenantID               string
	CloudProvider          string
	CloudProviderAccountID string
	DisplayName            string
	Status                 string
	AWSIAMRoleName         string
	AWSIAMPolicyName       string
}

// mockLucidityServer is a minimal, stateful stand-in for the real Public
// Tenant API: enough of onboard/list/update/deboard to exercise every
// branch tenantResource's CRUD methods take, including CONFLICT variants.
type mockLucidityServer struct {
	mu      sync.Mutex
	tenants map[string]*mockTenant // keyed by cloudProviderAccountID
}

func newMockLucidityServer(t *testing.T) (*httptest.Server, *mockLucidityServer) {
	t.Helper()
	m := &mockLucidityServer{tenants: map[string]*mockTenant{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/external/api/v1/auth/user-token/refresh", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "mock-access-token"})
	})

	mux.HandleFunc("/external/client/api/v1/tenants/onboard", func(w http.ResponseWriter, r *http.Request) {
		var req client.OnboardTenantRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		accountID := req.CloudEntityInformation.CloudProviderAccountID

		m.mu.Lock()
		defer m.mu.Unlock()
		if existing, ok := m.tenants[accountID]; ok {
			var msg string
			if existing.Status == client.TenantStatusActive {
				msg = "A tenant already exists for cloudProviderAccountId '" + accountID + "'."
			} else {
				msg = "An already deboarded (INACTIVE) tenant exists for cloudProviderAccountId '" + accountID + "'; re-onboarding support does not exist right now."
			}
			writeMockEnvelope(w, http.StatusConflict, false, nil, "CONFLICT", msg)
			return
		}
		t := &mockTenant{
			TenantID:               "mock_" + accountID,
			CloudProvider:          req.CloudEntityInformation.CloudProvider,
			CloudProviderAccountID: accountID,
			DisplayName:            req.DisplayName,
			Status:                 client.TenantStatusActive,
			AWSIAMRoleName:         req.CloudEntityInformation.AWSIAMRoleName,
			AWSIAMPolicyName:       req.CloudEntityInformation.AWSIAMPolicyName,
		}
		m.tenants[accountID] = t
		writeMockEnvelope(w, http.StatusCreated, true, client.OnboardedTenant{
			TenantID:               t.TenantID,
			CloudProvider:          t.CloudProvider,
			CloudProviderAccountID: t.CloudProviderAccountID,
			DisplayName:            t.DisplayName,
			Status:                 t.Status,
			Products:               req.ProductList,
		}, "", "")
	})

	mux.HandleFunc("/external/client/api/v1/tenants", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			items := make([]client.TenantListItem, 0, len(m.tenants))
			for _, t := range m.tenants {
				items = append(items, client.TenantListItem{
					TenantID:               t.TenantID,
					CloudProvider:          t.CloudProvider,
					CloudProviderAccountID: t.CloudProviderAccountID,
					CloudEntityName:        "mock-entity-" + t.CloudProviderAccountID,
					DisplayName:            t.DisplayName,
					Status:                 t.Status,
				})
			}
			writeMockEnvelope(w, http.StatusOK, true, map[string]any{
				"tenants": items,
				"meta":    map[string]any{"totalCount": len(items)},
			}, "", "")
		case http.MethodPatch:
			var req client.UpdateTenantRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			accountID := req.CloudEntityInformation.CloudProviderAccountID
			existing, ok := m.tenants[accountID]
			if !ok {
				writeMockEnvelope(w, http.StatusNotFound, false, nil, "NOT_FOUND", "No tenant exists for cloudProviderAccountId '"+accountID+"'.")
				return
			}
			if existing.Status != client.TenantStatusActive {
				writeMockEnvelope(w, http.StatusConflict, false, nil, "CONFLICT", "Tenant for cloudProviderAccountId '"+accountID+"' is not ACTIVE; cannot update.")
				return
			}
			if req.DisplayName != "" {
				existing.DisplayName = req.DisplayName
			}
			if req.CloudEntityInformation.AWSIAMRoleName != "" {
				existing.AWSIAMRoleName = req.CloudEntityInformation.AWSIAMRoleName
			}
			if req.CloudEntityInformation.AWSIAMPolicyName != "" {
				existing.AWSIAMPolicyName = req.CloudEntityInformation.AWSIAMPolicyName
			}
			writeMockEnvelope(w, http.StatusOK, true, client.UpdatedTenant{
				TenantID:               existing.TenantID,
				CloudProvider:          existing.CloudProvider,
				CloudProviderAccountID: existing.CloudProviderAccountID,
				DisplayName:            existing.DisplayName,
				Status:                 existing.Status,
			}, "", "")
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/external/client/api/v1/tenants/deboard", func(w http.ResponseWriter, r *http.Request) {
		var req client.DeboardTenantRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		accountID := req.CloudEntityInformation.CloudProviderAccountID

		m.mu.Lock()
		defer m.mu.Unlock()
		existing, ok := m.tenants[accountID]
		if !ok {
			writeMockEnvelope(w, http.StatusNotFound, false, nil, "NOT_FOUND", "No tenant exists for cloudProviderAccountId '"+accountID+"'; nothing to deboard.")
			return
		}
		status, msg := client.DeboardStatusDeBoarded, "cloudProviderAccountId '"+accountID+"' has been marked INACTIVE."
		if existing.Status != client.TenantStatusActive {
			status, msg = client.DeboardStatusAlreadyDeBoarded, "cloudProviderAccountId '"+accountID+"' is already deboarded."
		}
		existing.Status = client.TenantStatusInactive
		writeMockEnvelope(w, http.StatusOK, true, client.DeboardResult{
			CloudProviderAccountID: accountID,
			Status:                 status,
			Message:                msg,
		}, "", "")
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, m
}

func writeMockEnvelope(w http.ResponseWriter, status int, success bool, data any, errCode, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	env := map[string]any{"success": success, "requestId": "mock-request-id"}
	if data != nil {
		env["data"] = data
	} else {
		env["data"] = nil
	}
	if errCode != "" {
		env["error"] = map[string]string{"code": errCode, "message": errMsg}
	} else {
		env["error"] = nil
	}
	_ = json.NewEncoder(w).Encode(env)
}

// tenantSchema returns the resource's real schema, for building tfsdk.Plan/
// State values by hand in the tests below.
func tenantSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := &tenantResource{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

// lifecycleValue builds a full object value for the resource, reusing the
// same field set as tenantFullValue (resource_tenant_test.go) so both files
// stay in sync with the schema automatically. Computed attributes are given
// concrete placeholder values rather than left unknown — Create/Update/Read
// never branch on their *incoming* value (they always overwrite them from
// the mock server's response), so this simplification doesn't change what's
// being tested, only how the fixture is built.
func lifecycleValue(overrides map[string]tftypes.Value, ceiOverrides map[string]tftypes.Value) tftypes.Value {
	return tenantFullValue(overrides, ceiOverrides)
}

// TestLifecycle_OnboardReadUpdateDestroySafety drives one AWS account
// through onboard -> no-op read -> update -> protected-destroy (blocked) ->
// forget-destroy (removed from state, stays ACTIVE) -> re-onboard (still
// ACTIVE, so CONFLICT) -> deboard -> re-onboard (INACTIVE, so CONFLICT).
func TestLifecycle_OnboardReadUpdateDestroySafety(t *testing.T) {
	srv, _ := newMockLucidityServer(t)
	c := client.NewClient(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))
	r := &tenantResource{client: c}
	sch := tenantSchema(t)
	ctx := context.Background()
	objType := tenantConfigType()

	const accountID = "111111111111"
	baseCEI := map[string]tftypes.Value{
		"cloud_provider":            strVal("AWS"),
		"cloud_provider_account_id": strVal(accountID),
	}

	// --- Create ---
	createPlan := lifecycleValue(map[string]tftypes.Value{
		"lucidity_dashboard_display_name": strVal("lifecycle-test"),
	}, baseCEI)
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: sch}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Raw: createPlan, Schema: sch}}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create: %+v", createResp.Diagnostics)
	}
	var state tenantResourceModel
	createResp.State.Get(ctx, &state)
	if state.Status.ValueString() != client.TenantStatusActive {
		t.Fatalf("got status %q after create, want ACTIVE", state.Status.ValueString())
	}
	if state.TenantID.IsNull() || state.TenantID.ValueString() == "" {
		t.Fatal("expected tenant_id to be populated after create")
	}

	// --- Read (no-op) ---
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: sch}
	r.Read(ctx, resource.ReadRequest{State: tfsdk.State{Raw: createResp.State.Raw, Schema: sch}}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read: %+v", readResp.Diagnostics)
	}
	var afterRead tenantResourceModel
	readResp.State.Get(ctx, &afterRead)
	if afterRead.Status.ValueString() != client.TenantStatusActive {
		t.Fatalf("got status %q after no-op read, want ACTIVE", afterRead.Status.ValueString())
	}

	// --- Update: display name ---
	updatePlan := lifecycleValue(map[string]tftypes.Value{
		"lucidity_dashboard_display_name": strVal("lifecycle-test-renamed"),
	}, baseCEI)
	var updateResp resource.UpdateResponse
	updateResp.State = tfsdk.State{Schema: sch}
	r.Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Raw: updatePlan, Schema: sch},
		State: tfsdk.State{Raw: createResp.State.Raw, Schema: sch},
	}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update (display name): %+v", updateResp.Diagnostics)
	}
	var afterUpdate tenantResourceModel
	updateResp.State.Get(ctx, &afterUpdate)
	if afterUpdate.DisplayName.ValueString() != "lifecycle-test-renamed" {
		t.Fatalf("got display name %q after update, want lifecycle-test-renamed", afterUpdate.DisplayName.ValueString())
	}

	// --- Destroy blocked by account_delete_protection=true (schema default) ---
	protectedState := lifecycleValue(map[string]tftypes.Value{
		"lucidity_dashboard_display_name":              strVal("lifecycle-test-renamed"),
		"lucidity_dashboard_account_delete_protection": boolVal(true),
	}, baseCEI)
	var blockedResp resource.DeleteResponse
	blockedResp.State = tfsdk.State{Schema: sch}
	r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Raw: protectedState, Schema: sch}}, &blockedResp)
	if !blockedResp.Diagnostics.HasError() {
		t.Fatal("expected Delete to be blocked by account_delete_protection, got no error")
	}
	if got, _ := client.FindTenant(mustList(t, c, ctx), "AWS", accountID); got.Status != client.TenantStatusActive {
		t.Fatalf("tenant should be untouched (still ACTIVE) after a blocked destroy, got status %q", got.Status)
	}

	// --- Destroy with protection off, forget (default behavior) ---
	forgetState := lifecycleValue(map[string]tftypes.Value{
		"lucidity_dashboard_display_name":              strVal("lifecycle-test-renamed"),
		"lucidity_dashboard_account_delete_protection": boolVal(false),
		"lucidity_account_destroy_behavior":            strVal("forget"),
	}, baseCEI)
	var forgetResp resource.DeleteResponse
	forgetResp.State = tfsdk.State{Schema: sch}
	r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Raw: forgetState, Schema: sch}}, &forgetResp)
	if forgetResp.Diagnostics.HasError() {
		t.Fatalf("Delete (forget): %+v", forgetResp.Diagnostics)
	}
	if got, ok := client.FindTenant(mustList(t, c, ctx), "AWS", accountID); !ok || got.Status != client.TenantStatusActive {
		t.Fatalf("forget-destroy should leave the tenant ACTIVE on the backend, got found=%v status=%q", ok, got.Status)
	}

	// --- Re-onboard while still ACTIVE -> CONFLICT (active variant) ---
	var reCreateResp resource.CreateResponse
	reCreateResp.State = tfsdk.State{Schema: sch}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Raw: createPlan, Schema: sch}}, &reCreateResp)
	if !reCreateResp.Diagnostics.HasError() {
		t.Fatal("expected re-onboarding a still-ACTIVE account to fail via the pre-check, got no error")
	}

	// --- Deboard for real ---
	deboardState := lifecycleValue(map[string]tftypes.Value{
		"lucidity_dashboard_display_name":              strVal("lifecycle-test-renamed"),
		"lucidity_dashboard_account_delete_protection": boolVal(false),
		"lucidity_account_destroy_behavior":            strVal("deboard"),
	}, baseCEI)
	var deboardResp resource.DeleteResponse
	deboardResp.State = tfsdk.State{Schema: sch}
	r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Raw: deboardState, Schema: sch}}, &deboardResp)
	if deboardResp.Diagnostics.HasError() {
		t.Fatalf("Delete (deboard): %+v", deboardResp.Diagnostics)
	}
	if got, ok := client.FindTenant(mustList(t, c, ctx), "AWS", accountID); !ok || got.Status != client.TenantStatusInactive {
		t.Fatalf("expected the tenant to be INACTIVE after a real deboard, got found=%v status=%q", ok, got.Status)
	}

	// --- Re-onboard after deboard -> CONFLICT (inactive variant) ---
	var reCreateResp2 resource.CreateResponse
	reCreateResp2.State = tfsdk.State{Schema: sch}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Raw: createPlan, Schema: sch}}, &reCreateResp2)
	if !reCreateResp2.Diagnostics.HasError() {
		t.Fatal("expected re-onboarding a deboarded (INACTIVE) account to fail via the pre-check, got no error")
	}

	_ = objType
}

func mustList(t *testing.T, c *client.Client, ctx context.Context) []client.TenantListItem {
	t.Helper()
	tenants, err := c.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	return tenants
}

// TestLifecycle_ReadDetectsOutOfBandDeboard confirms Read() — not just
// Delete() — correctly surfaces a tenant that went INACTIVE some other way
// (e.g. deboarded directly via the API, outside Terraform entirely): kept
// in state with its real status, plus an error diagnostic, per the
// resource's INACTIVE handling design.
func TestLifecycle_ReadDetectsOutOfBandDeboard(t *testing.T) {
	srv, mock := newMockLucidityServer(t)
	c := client.NewClient(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))
	r := &tenantResource{client: c}
	sch := tenantSchema(t)
	ctx := context.Background()

	const accountID = "222222222222"
	baseCEI := map[string]tftypes.Value{
		"cloud_provider":            strVal("AWS"),
		"cloud_provider_account_id": strVal(accountID),
	}
	plan := lifecycleValue(map[string]tftypes.Value{"lucidity_dashboard_display_name": strVal("drift-test")}, baseCEI)

	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: sch}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Raw: plan, Schema: sch}}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create: %+v", createResp.Diagnostics)
	}

	// Simulate an out-of-band deboard: mutate the backend directly, bypassing
	// this provider entirely (no Delete() call happens here).
	mock.mu.Lock()
	mock.tenants[accountID].Status = client.TenantStatusInactive
	mock.mu.Unlock()

	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: sch}
	r.Read(ctx, resource.ReadRequest{State: tfsdk.State{Raw: createResp.State.Raw, Schema: sch}}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Fatal("expected an error diagnostic for a tenant that went INACTIVE out-of-band, got none")
	}
	if readResp.State.Raw.IsNull() {
		t.Fatal("expected the resource to stay in state (not removed) for an INACTIVE tenant")
	}
	var afterRead tenantResourceModel
	readResp.State.Get(ctx, &afterRead)
	if afterRead.Status.ValueString() != client.TenantStatusInactive {
		t.Fatalf("got status %q, want INACTIVE", afterRead.Status.ValueString())
	}
}

// TestLifecycle_ReadRemovesTenantThatNoLongerExists confirms Read() removes
// the resource from state (rather than erroring) when the backend has no
// matching tenant at all.
func TestLifecycle_ReadRemovesTenantThatNoLongerExists(t *testing.T) {
	srv, mock := newMockLucidityServer(t)
	c := client.NewClient(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))
	r := &tenantResource{client: c}
	sch := tenantSchema(t)
	ctx := context.Background()

	const accountID = "333333333333"
	baseCEI := map[string]tftypes.Value{
		"cloud_provider":            strVal("AWS"),
		"cloud_provider_account_id": strVal(accountID),
	}
	plan := lifecycleValue(map[string]tftypes.Value{"lucidity_dashboard_display_name": strVal("vanished-test")}, baseCEI)

	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: sch}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Raw: plan, Schema: sch}}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create: %+v", createResp.Diagnostics)
	}

	mock.mu.Lock()
	delete(mock.tenants, accountID)
	mock.mu.Unlock()

	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: sch}
	r.Read(ctx, resource.ReadRequest{State: tfsdk.State{Raw: createResp.State.Raw, Schema: sch}}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected only a warning for a vanished tenant, got error diagnostics: %+v", readResp.Diagnostics)
	}
	if !readResp.State.Raw.IsNull() {
		t.Fatal("expected the resource to be removed from state when the backend has no matching tenant")
	}
}

// TestLifecycle_OnboardCloudAccountValidationFailure confirms the 401
// distinguishing logic (client.go's cloudAccountValidationError) surfaces
// correctly all the way up through Create()'s own error handling, rather
// than being masked behind AuthError's generic message.
func TestLifecycle_OnboardCloudAccountValidationFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/external/api/v1/auth/user-token/refresh", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "mock-access-token"})
	})
	mux.HandleFunc("/external/client/api/v1/tenants/onboard", func(w http.ResponseWriter, r *http.Request) {
		writeMockEnvelope(w, http.StatusUnauthorized, false, nil, "UNAUTHORIZED", "Authentication failed: the cloud account could not be validated.")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := client.NewClient(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))
	r := &tenantResource{client: c}
	sch := tenantSchema(t)
	ctx := context.Background()

	// This account has no prior state, and ListTenants (hit by Create's
	// pre-check) isn't stubbed here — point Create straight at the failing
	// onboard call by using a mux that 404s list, which surfaces as a
	// generic list error instead. To isolate the onboard-error path
	// specifically, stub list to return an empty result.
	mux.HandleFunc("/external/client/api/v1/tenants", func(w http.ResponseWriter, r *http.Request) {
		writeMockEnvelope(w, http.StatusOK, true, map[string]any{"tenants": []client.TenantListItem{}, "meta": map[string]any{"totalCount": 0}}, "", "")
	})

	plan := lifecycleValue(map[string]tftypes.Value{"lucidity_dashboard_display_name": strVal("validation-fail-test")}, map[string]tftypes.Value{
		"cloud_provider":            strVal("AWS"),
		"cloud_provider_account_id": strVal("444444444444"),
	})
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: sch}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Raw: plan, Schema: sch}}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected an error diagnostic for a cloud-account-validation failure, got none")
	}
	found := false
	for _, d := range createResp.Diagnostics {
		if d.Summary() == "Cloud account could not be validated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the distinct 'Cloud account could not be validated' diagnostic, got: %+v", createResp.Diagnostics)
	}
}

// TestLifecycle_ImportState confirms the import-ID parsing logic directly:
// well-formed IDs populate cloud_provider/cloud_provider_account_id and
// force account_delete_protection=true; malformed ones error clearly.
func TestLifecycle_ImportState(t *testing.T) {
	sch := tenantSchema(t)
	ctx := context.Background()
	r := &tenantResource{}

	t.Run("well-formed", func(t *testing.T) {
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}}
		r.ImportState(ctx, resource.ImportStateRequest{ID: "AWS/123456789012"}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected error diagnostics: %+v", resp.Diagnostics)
		}
		var model tenantResourceModel
		resp.State.Get(ctx, &model)
		if model.CloudEntityInformation.CloudProvider.ValueString() != "AWS" {
			t.Fatalf("got cloud_provider %q, want AWS", model.CloudEntityInformation.CloudProvider.ValueString())
		}
		if model.CloudEntityInformation.CloudProviderAccountID.ValueString() != "123456789012" {
			t.Fatalf("got cloud_provider_account_id %q, want 123456789012", model.CloudEntityInformation.CloudProviderAccountID.ValueString())
		}
		if !model.AccountDeleteProtection.ValueBool() {
			t.Fatal("expected account_delete_protection to be forced true on import")
		}
	})

	t.Run("malformed", func(t *testing.T) {
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}}
		r.ImportState(ctx, resource.ImportStateRequest{ID: "not-a-valid-id"}, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected an error diagnostic for a malformed import ID, got none")
		}
	})
}
