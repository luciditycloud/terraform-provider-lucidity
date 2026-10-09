package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const tenantResourceTypeName = "lucidity_tenant"

func cloudEntityInformationType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"cloud_provider":             tftypes.String,
			"cloud_provider_account_id":  tftypes.String,
			"aws_iam_external_id":        tftypes.String,
			"aws_iam_role_name":          tftypes.String,
			"aws_iam_policy_name":        tftypes.String,
			"azure_service_principal_id": tftypes.String,
			"azure_directory_id":         tftypes.String,
		},
	}
}

func tenantConfigType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"cloud_entity_information":                     cloudEntityInformationType(),
			"lucidity_dashboard_display_name":              tftypes.String,
			"lucidity_product_list":                        tftypes.List{ElementType: tftypes.String},
			"aws_org_root_id":                              tftypes.String,
			"skip_cloud_permission_check":                  tftypes.Bool,
			"lucidity_dashboard_account_delete_protection": tftypes.Bool,
			"lucidity_account_destroy_behavior":            tftypes.String,
			"tenant_id":                                    tftypes.String,
			"status":                                       tftypes.String,
			"cloud_entity_name":                            tftypes.String,
		},
	}
}

// validTenantConfig returns a fully-populated, schema-valid config value map
// (minus computed attributes, left null as Terraform would leave them in a
// real plan). Individual tests override one field to exercise a validator.
func validTenantConfig() map[string]tftypes.Value {
	cei := tftypes.NewValue(cloudEntityInformationType(), map[string]tftypes.Value{
		"cloud_provider":             strVal("AWS"),
		"cloud_provider_account_id":  strVal("123456789012"),
		"aws_iam_external_id":        strVal("8f14e45f-ceea-4331-9f5e-111111111111"),
		"aws_iam_role_name":          strVal("LucidityRole"),
		"aws_iam_policy_name":        strVal("LucidityPolicy"),
		"azure_service_principal_id": tftypes.NewValue(tftypes.String, nil),
		"azure_directory_id":         tftypes.NewValue(tftypes.String, nil),
	})
	productList := tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{strVal("AUTOSCALER")})
	return map[string]tftypes.Value{
		"cloud_entity_information":        cei,
		"lucidity_dashboard_display_name": strVal("non-prod"),
		"lucidity_product_list":           productList,
	}
}

func tenantConfigValue(t *testing.T, overrides map[string]tftypes.Value, ceiOverrides map[string]tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	objType := tenantConfigType()
	set := validTenantConfig()

	if len(ceiOverrides) > 0 {
		ceiType := cloudEntityInformationType()
		ceiSet := map[string]tftypes.Value{}
		// Start from the valid CEI and apply overrides.
		validCEI := map[string]tftypes.Value{
			"cloud_provider":             strVal("AWS"),
			"cloud_provider_account_id":  strVal("123456789012"),
			"aws_iam_external_id":        strVal("8f14e45f-ceea-4331-9f5e-111111111111"),
			"aws_iam_role_name":          strVal("LucidityRole"),
			"aws_iam_policy_name":        strVal("LucidityPolicy"),
			"azure_service_principal_id": tftypes.NewValue(tftypes.String, nil),
			"azure_directory_id":         tftypes.NewValue(tftypes.String, nil),
		}
		for k, v := range validCEI {
			ceiSet[k] = v
		}
		for k, v := range ceiOverrides {
			ceiSet[k] = v
		}
		set["cloud_entity_information"] = tftypes.NewValue(ceiType, ceiSet)
	}
	for k, v := range overrides {
		set[k] = v
	}

	full := map[string]tftypes.Value{}
	for name, ty := range objType.AttributeTypes {
		if v, ok := set[name]; ok {
			full[name] = v
		} else {
			full[name] = tftypes.NewValue(ty, nil)
		}
	}
	dv, err := tfprotov6.NewDynamicValue(objType, tftypes.NewValue(objType, full))
	if err != nil {
		t.Fatalf("NewDynamicValue: %v", err)
	}
	return &dv
}

func TestTenantResourceRPC_GetProviderSchema_RegistersResourceAndDataSource(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	if hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected no error diagnostic, got: %+v", resp.Diagnostics)
	}
	if _, ok := resp.ResourceSchemas[tenantResourceTypeName]; !ok {
		t.Fatalf("expected %q to be a registered resource, got: %+v", tenantResourceTypeName, resp.ResourceSchemas)
	}
	if _, ok := resp.DataSourceSchemas["lucidity_tenants"]; !ok {
		t.Fatalf("expected lucidity_tenants to be a registered data source, got: %+v", resp.DataSourceSchemas)
	}
}

func TestTenantResourceRPC_ValidateConfig_AcceptsValidConfig(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config:   tenantConfigValue(t, nil, nil),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected no error diagnostic, got: %+v", resp.Diagnostics)
	}
}

func TestTenantResourceRPC_ValidateConfig_RejectsUnsupportedCloudProvider(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config:   tenantConfigValue(t, nil, map[string]tftypes.Value{"cloud_provider": strVal("OCI")}),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if !hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected an error diagnostic for an unsupported cloud_provider (must be AWS, AZURE, or GCP), got: %+v", resp.Diagnostics)
	}
}

// AZURE/GCP resources only ever enter Terraform via `terraform import` (onboarding
// is AWS-only), so a valid AZURE config must NOT require any of the AWS-only
// fields — they're enforced conditionally in ValidateConfig, not via schema
// Required, precisely so this case validates cleanly.
func TestTenantResourceRPC_ValidateConfig_AcceptsAzureProviderWithoutAWSFields(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config: tenantConfigValue(t, map[string]tftypes.Value{
			"lucidity_product_list": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		}, map[string]tftypes.Value{
			"cloud_provider":            strVal("AZURE"),
			"cloud_provider_account_id": strVal("00000000-0000-0000-0000-000000000000"),
			"aws_iam_external_id":       tftypes.NewValue(tftypes.String, nil),
			"aws_iam_role_name":         tftypes.NewValue(tftypes.String, nil),
			"aws_iam_policy_name":       tftypes.NewValue(tftypes.String, nil),
		}),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected no error diagnostic for a valid AZURE config with no AWS-only fields set, got: %+v", resp.Diagnostics)
	}
}

func TestTenantResourceRPC_ValidateConfig_RejectsAWSMissingRoleName(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config: tenantConfigValue(t, nil, map[string]tftypes.Value{
			"aws_iam_role_name": tftypes.NewValue(tftypes.String, nil),
		}),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if !hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected an error diagnostic for cloud_provider=AWS with aws_iam_role_name unset, got: %+v", resp.Diagnostics)
	}
}

func TestTenantResourceRPC_ValidateConfig_RejectsEmptyProductList(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config: tenantConfigValue(t, map[string]tftypes.Value{
			"lucidity_product_list": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{}),
		}, nil),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if !hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected an error diagnostic for an empty lucidity_product_list, got: %+v", resp.Diagnostics)
	}
}

func TestTenantResourceRPC_ValidateConfig_RejectsUnsupportedProduct(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config: tenantConfigValue(t, map[string]tftypes.Value{
			"lucidity_product_list": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{strVal("NOT_A_PRODUCT")}),
		}, nil),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if !hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected an error diagnostic for an unsupported product, got: %+v", resp.Diagnostics)
	}
}

func TestTenantResourceRPC_ValidateConfig_RejectsNonUUIDExternalID(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config: tenantConfigValue(t, nil, map[string]tftypes.Value{
			"aws_iam_external_id": strVal("not-a-uuid"),
		}),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if !hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected an error diagnostic for a non-UUID aws_iam_external_id, got: %+v", resp.Diagnostics)
	}
}

func TestTenantResourceRPC_ValidateConfig_AcceptsUUIDExternalID(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config: tenantConfigValue(t, nil, map[string]tftypes.Value{
			"aws_iam_external_id": strVal("8f14e45f-ceea-4331-9f5e-111111111111"),
		}),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected no error diagnostic for a valid UUID aws_iam_external_id, got: %+v", resp.Diagnostics)
	}
}

func TestTenantResourceRPC_ValidateConfig_RejectsMalformedAWSAccountID(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config: tenantConfigValue(t, nil, map[string]tftypes.Value{
			"cloud_provider_account_id": strVal("not-an-account-id"),
		}),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if !hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected an error diagnostic for a malformed AWS cloud_provider_account_id, got: %+v", resp.Diagnostics)
	}
}

func TestTenantResourceRPC_ValidateConfig_AcceptsWellFormedAWSAccountID(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config: tenantConfigValue(t, nil, map[string]tftypes.Value{
			"cloud_provider_account_id": strVal("123456789012"),
		}),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected no error diagnostic for a well-formed AWS cloud_provider_account_id, got: %+v", resp.Diagnostics)
	}
}

func TestTenantResourceRPC_ValidateConfig_RejectsBlankDisplayName(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config: tenantConfigValue(t, map[string]tftypes.Value{
			"lucidity_dashboard_display_name": strVal(""),
		}, nil),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if !hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected an error diagnostic for a blank lucidity_dashboard_display_name, got: %+v", resp.Diagnostics)
	}
}

func TestTenantResourceRPC_ValidateConfig_RejectsInvalidDestroyBehavior(t *testing.T) {
	srv := newTestProviderServer(t)
	resp, err := srv.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: tenantResourceTypeName,
		Config: tenantConfigValue(t, map[string]tftypes.Value{
			"lucidity_account_destroy_behavior": strVal("delete"),
		}, nil),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	if !hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected an error diagnostic for lucidity_account_destroy_behavior=delete (must be forget|deboard), got: %+v", resp.Diagnostics)
	}
}

func boolVal(b bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, b) }

// tenantFullValue builds a complete, schema-shaped object value (every
// attribute concretely set, standing in for an existing resource's prior
// state or a fully-known plan) for driving PlanResourceChange directly.
func tenantFullValue(overrides map[string]tftypes.Value, ceiOverrides map[string]tftypes.Value) tftypes.Value {
	validCEI := map[string]tftypes.Value{
		"cloud_provider":             strVal("AWS"),
		"cloud_provider_account_id":  strVal("123456789012"),
		"aws_iam_external_id":        strVal("8f14e45f-ceea-4331-9f5e-111111111111"),
		"aws_iam_role_name":          strVal("LucidityRole"),
		"aws_iam_policy_name":        strVal("LucidityPolicy"),
		"azure_service_principal_id": tftypes.NewValue(tftypes.String, nil),
		"azure_directory_id":         tftypes.NewValue(tftypes.String, nil),
	}
	for k, v := range ceiOverrides {
		validCEI[k] = v
	}
	cei := tftypes.NewValue(cloudEntityInformationType(), validCEI)

	full := map[string]tftypes.Value{
		"cloud_entity_information":                     cei,
		"lucidity_dashboard_display_name":              strVal("non-prod"),
		"lucidity_product_list":                        tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{strVal("AUTOSCALER")}),
		"aws_org_root_id":                              strVal("111111111111"),
		"skip_cloud_permission_check":                  boolVal(false),
		"lucidity_dashboard_account_delete_protection": boolVal(true),
		"lucidity_account_destroy_behavior":            strVal("forget"),
		"tenant_id":                                    strVal("acme_123456789012"),
		"status":                                       strVal("ACTIVE"),
		"cloud_entity_name":                            strVal("acme-123456789012"),
	}
	for k, v := range overrides {
		full[k] = v
	}
	return tftypes.NewValue(tenantConfigType(), full)
}

func TestTenantResourceRPC_PlanResourceChange_RejectsAWSOrgRootIDChange(t *testing.T) {
	srv := newTestProviderServer(t)
	objType := tenantConfigType()

	priorDV, err := tfprotov6.NewDynamicValue(objType, tenantFullValue(nil, nil))
	if err != nil {
		t.Fatalf("NewDynamicValue(prior): %v", err)
	}
	changed := tenantFullValue(map[string]tftypes.Value{"aws_org_root_id": strVal("222222222222")}, nil)
	configDV, err := tfprotov6.NewDynamicValue(objType, changed)
	if err != nil {
		t.Fatalf("NewDynamicValue(config): %v", err)
	}
	proposedDV, err := tfprotov6.NewDynamicValue(objType, changed)
	if err != nil {
		t.Fatalf("NewDynamicValue(proposed): %v", err)
	}

	resp, err := srv.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         tenantResourceTypeName,
		PriorState:       &priorDV,
		ProposedNewState: &proposedDV,
		Config:           &configDV,
	})
	if err != nil {
		t.Fatalf("PlanResourceChange: %v", err)
	}
	if !hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected a plan-time error diagnostic for a changed aws_org_root_id, got: %+v", resp.Diagnostics)
	}
}

func TestTenantResourceRPC_PlanResourceChange_AllowsUnchangedAWSOrgRootID(t *testing.T) {
	srv := newTestProviderServer(t)
	objType := tenantConfigType()

	same := tenantFullValue(nil, nil)
	priorDV, err := tfprotov6.NewDynamicValue(objType, same)
	if err != nil {
		t.Fatalf("NewDynamicValue(prior): %v", err)
	}
	configDV, err := tfprotov6.NewDynamicValue(objType, same)
	if err != nil {
		t.Fatalf("NewDynamicValue(config): %v", err)
	}
	proposedDV, err := tfprotov6.NewDynamicValue(objType, same)
	if err != nil {
		t.Fatalf("NewDynamicValue(proposed): %v", err)
	}

	resp, err := srv.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         tenantResourceTypeName,
		PriorState:       &priorDV,
		ProposedNewState: &proposedDV,
		Config:           &configDV,
	})
	if err != nil {
		t.Fatalf("PlanResourceChange: %v", err)
	}
	if hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected no error diagnostic when aws_org_root_id is unchanged, got: %+v", resp.Diagnostics)
	}
}

// planTenantUpdate drives PlanResourceChange for an existing resource whose
// prior state is prior and whose (fully-known) config/proposed state is cfg.
func planTenantUpdate(t *testing.T, prior, cfg tftypes.Value) *tfprotov6.PlanResourceChangeResponse {
	t.Helper()
	srv := newTestProviderServer(t)
	objType := tenantConfigType()
	priorDV, err := tfprotov6.NewDynamicValue(objType, prior)
	if err != nil {
		t.Fatalf("NewDynamicValue(prior): %v", err)
	}
	cfgDV, err := tfprotov6.NewDynamicValue(objType, cfg)
	if err != nil {
		t.Fatalf("NewDynamicValue(config): %v", err)
	}
	resp, err := srv.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         tenantResourceTypeName,
		PriorState:       &priorDV,
		ProposedNewState: &cfgDV,
		Config:           &cfgDV,
	})
	if err != nil {
		t.Fatalf("PlanResourceChange: %v", err)
	}
	return resp
}

func requiresReplaceOf(resp *tfprotov6.PlanResourceChangeResponse, attr string) bool {
	for _, p := range resp.RequiresReplace {
		for _, step := range p.Steps() {
			if n, ok := step.(tftypes.AttributeName); ok && string(n) == attr {
				return true
			}
		}
	}
	return false
}

func nullList() tftypes.Value {
	return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil)
}

func TestTenantResourceRPC_PlanResourceChange_ExternalIDChangeIsInPlace(t *testing.T) {
	prior := tenantFullValue(nil, nil)
	cfg := tenantFullValue(nil, map[string]tftypes.Value{"aws_iam_external_id": strVal("00000000-0000-4000-8000-000000000000")})

	resp := planTenantUpdate(t, prior, cfg)
	if hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected no error, got: %+v", resp.Diagnostics)
	}
	if len(resp.RequiresReplace) != 0 {
		t.Fatalf("external ID is updatable in place now; got forced replace on: %+v", resp.RequiresReplace)
	}
}

func TestTenantResourceRPC_PlanResourceChange_ProductListChangeStillReplaces(t *testing.T) {
	prior := tenantFullValue(nil, nil)
	cfg := tenantFullValue(map[string]tftypes.Value{
		"lucidity_product_list": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{strVal("AUTOSCALER"), strVal("AUTOSCALER")}),
	}, nil)

	resp := planTenantUpdate(t, prior, cfg)
	if !requiresReplaceOf(resp, "lucidity_product_list") {
		t.Fatalf("changing a product list already recorded in state must force replace, got RequiresReplace=%+v diags=%+v", resp.RequiresReplace, resp.Diagnostics)
	}
}

func TestTenantResourceRPC_PlanResourceChange_NullStateProductListAndOrgRootAreAssumedNotCompared(t *testing.T) {
	// State after `terraform import`: List never returns either field.
	prior := tenantFullValue(map[string]tftypes.Value{
		"lucidity_product_list": nullList(),
		"aws_org_root_id":       tftypes.NewValue(tftypes.String, nil),
	}, nil)
	cfg := tenantFullValue(nil, nil) // config supplies both

	resp := planTenantUpdate(t, prior, cfg)
	if hasErrorDiagnostic(resp.Diagnostics) {
		t.Fatalf("expected no error when recording previously-unreadable fields, got: %+v", resp.Diagnostics)
	}
	if len(resp.RequiresReplace) != 0 {
		t.Fatalf("expected no forced replace after import, got: %+v", resp.RequiresReplace)
	}
}
