package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Tenant API endpoints. All rooted at /external/client/api/v1/tenants per
// the Public Tenant API doc; the dashboard account is always resolved from
// the caller's access token, never sent explicitly.
const (
	tenantsPath        = "/external/client/api/v1/tenants"
	tenantsOnboardPath = "/external/client/api/v1/tenants/onboard"
	tenantsDeboardPath = "/external/client/api/v1/tenants/deboard"
)

// Tenant status values, per the List/Update response "status" field.
const (
	TenantStatusActive         = "ACTIVE"
	TenantStatusInactive       = "INACTIVE"
	TenantStatusDecommissioned = "DECOMMISSIONED"
)

// Deboard result status values, per the Deboard response "status" field.
const (
	DeboardStatusDeBoarded        = "DE_BOARDED"
	DeboardStatusAlreadyDeBoarded = "ALREADY_DE_BOARDED"
)

// CloudEntityInformation identifies a tenant's cloud account and, on
// onboard/update, carries the provider-specific auth fields. Field
// applicability (required/optional/ignored) differs per call — see the
// doc comment on each method below.
type CloudEntityInformation struct {
	CloudProvider           string `json:"cloudProvider"`
	CloudProviderAccountID  string `json:"cloudProviderAccountId"`
	ExternalID              string `json:"externalId,omitempty"`
	AWSIAMRoleName          string `json:"awsIAMRoleName,omitempty"`
	AWSIAMPolicyName        string `json:"awsIAMPolicyName,omitempty"`
	AzureServicePrincipalID string `json:"azureServicePrincipalId,omitempty"`
	AzureDirectoryID        string `json:"azureDirectoryId,omitempty"`

	// AWSRootID is NOT part of the documented Public Tenant API request
	// schema (confirmed absent from both the onboard and update field
	// tables, live-tested 2026-09-06). Sent anyway on the maintainer's
	// explicit instruction so it lands in Lucidity's records for accounts
	// where a shared IAM role/policy is assumed across an AWS Organization
	// — but since it's undocumented, Lucidity may silently ignore it or,
	// if it validates request bodies strictly, reject the call outright.
	// omitempty so a config that never sets aws_root_id sends exactly the
	// documented payload shape.
	AWSRootID string `json:"awsRootId,omitempty"`
}

// OnboardTenantRequest is the POST .../tenants/onboard request body.
type OnboardTenantRequest struct {
	CloudEntityInformation   CloudEntityInformation `json:"cloudEntityInformation"`
	DisplayName              string                 `json:"displayName"`
	SkipCloudPermissionCheck bool                   `json:"skipCloudPermissionCheck,omitempty"`
	ProductList              []string               `json:"productList"`
}

// OnboardedTenant is the OnboardTenant response's data field.
type OnboardedTenant struct {
	TenantID               string   `json:"tenantId"`
	CloudProvider          string   `json:"cloudProvider"`
	CloudProviderAccountID string   `json:"cloudProviderAccountId"`
	DisplayName            string   `json:"displayName"`
	Status                 string   `json:"status"`
	Products               []string `json:"products"`
}

// OnboardTenant connects a cloud account to Lucidity as a managed tenant.
// AWS-only today: a non-AWS cloudProvider returns 400 INVALID_REQUEST.
func (c *Client) OnboardTenant(ctx context.Context, req OnboardTenantRequest) (*OnboardedTenant, error) {
	var out OnboardedTenant
	// DoNoRetry5xx, not Do: a 5xx on the onboard POST doesn't prove the
	// tenant wasn't created, and blindly re-sending it turns a
	// successful-but-5xx create into a masking 409. The caller re-lists to
	// find out what actually happened instead (see IsOutcomeUnknown).
	if err := c.DoNoRetry5xx(ctx, http.MethodPost, tenantsOnboardPath, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TenantListItem is one entry in the ListTenants response.
type TenantListItem struct {
	TenantID               string `json:"tenantId"`
	CloudProvider          string `json:"cloudProvider"`
	CloudProviderAccountID string `json:"cloudProviderAccountId"`
	CloudEntityName        string `json:"cloudEntityName"`
	DisplayName            string `json:"displayName"`
	Status                 string `json:"status"`

	// ExternalID is the AWS IAM external ID Lucidity holds for the tenant
	// (nil when the API reports null). ExternalIDReported distinguishes a
	// backend that returns the key at all (true, even if null) from an older
	// one that omits it entirely (false) — only the former means "remote has
	// no external id"; the latter means "can't tell", and callers must not
	// treat it as a mismatch.
	ExternalID         *string `json:"externalId"`
	ExternalIDReported bool    `json:"-"`
}

// UnmarshalJSON records whether the externalId key was present at all.
func (t *TenantListItem) UnmarshalJSON(b []byte) error {
	type plain TenantListItem
	aux := struct {
		*plain
		ExternalID json.RawMessage `json:"externalId"`
	}{plain: (*plain)(t)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	t.ExternalID, t.ExternalIDReported = nil, len(aux.ExternalID) > 0
	if t.ExternalIDReported && string(aux.ExternalID) != "null" {
		var v string
		if err := json.Unmarshal(aux.ExternalID, &v); err != nil {
			return err
		}
		t.ExternalID = &v
	}
	return nil
}

type tenantListMeta struct {
	TotalCount int `json:"totalCount"`
}

type tenantListResponse struct {
	Tenants []TenantListItem `json:"tenants"`
	Meta    tenantListMeta   `json:"meta"`
}

// ListTenants returns every tenant under the caller's account (resolved from
// the access token), ACTIVE first then INACTIVE, per the doc.
func (c *Client) ListTenants(ctx context.Context) ([]TenantListItem, error) {
	var out tenantListResponse
	if err := c.Do(ctx, http.MethodGet, tenantsPath, nil, &out); err != nil {
		return nil, err
	}
	// totalCount counts every tenant under the account (INACTIVE included),
	// so it is only meaningful against this same response, never against
	// anything local. A larger count than returned means the list was cut
	// short (pagination), and any "not found" derived from it — Read removing
	// state, Create's duplicate pre-check, the 5xx recovery — would be
	// unreliable, so fail loudly instead. A missing/zero or smaller count
	// (older backend, stale count) is not evidence of truncation.
	if out.Meta.TotalCount > len(out.Tenants) {
		return nil, &TruncatedListError{Returned: len(out.Tenants), Total: out.Meta.TotalCount}
	}
	return out.Tenants, nil
}

// TruncatedListError means ListTenants returned fewer tenants than the API
// says exist. No page parameter is documented yet, so the provider cannot
// fetch the rest.
type TruncatedListError struct {
	Returned, Total int
}

func (e *TruncatedListError) Error() string {
	return fmt.Sprintf(
		"lucidity returned %d of %d tenants (the list is truncated, likely paginated); the provider cannot safely tell whether a tenant exists, so it stops rather than guess. Report this to the provider maintainers or Lucidity support — pagination support is needed",
		e.Returned, e.Total,
	)
}

// FindTenant returns the list entry matching provider+accountID, and whether
// one was found. Used by Read/Create for the list-and-match pattern the API
// requires (there is no GET-by-ID).
func FindTenant(tenants []TenantListItem, cloudProvider, cloudProviderAccountID string) (TenantListItem, bool) {
	for _, t := range tenants {
		if t.CloudProvider == cloudProvider && t.CloudProviderAccountID == cloudProviderAccountID {
			return t, true
		}
	}
	return TenantListItem{}, false
}

// UpdateTenantRequest is the PATCH .../tenants request body. Only
// CloudProvider and CloudProviderAccountID (to identify the tenant) plus
// whichever fields are changing should be set; the rest must stay zero so
// they're omitted and existing values are preserved (authInfo is merged
// server-side). DisplayName and/or at least one provider auth field must be
// set, or the request is rejected. ExternalID (AWS) can be rotated via
// update; rotate the AWS trust policy first, since Lucidity re-validates by
// AssumeRole.
type UpdateTenantRequest struct {
	CloudEntityInformation CloudEntityInformation `json:"cloudEntityInformation"`
	DisplayName            string                 `json:"displayName,omitempty"`
}

// UpdatedTenant is the UpdateTenant response's data field.
type UpdatedTenant struct {
	TenantID               string `json:"tenantId"`
	CloudProvider          string `json:"cloudProvider"`
	CloudProviderAccountID string `json:"cloudProviderAccountId"`
	DisplayName            string `json:"displayName"`
	Status                 string `json:"status"`
}

// UpdateTenant applies a partial update to an existing, ACTIVE tenant.
func (c *Client) UpdateTenant(ctx context.Context, req UpdateTenantRequest) (*UpdatedTenant, error) {
	var out UpdatedTenant
	if err := c.Do(ctx, http.MethodPatch, tenantsPath, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeboardTenantRequest is the PUT .../tenants/deboard request body. Only
// CloudProvider and CloudProviderAccountID are read; any other field is
// ignored per the doc.
type DeboardTenantRequest struct {
	CloudEntityInformation CloudEntityInformation `json:"cloudEntityInformation"`
}

// DeboardResult is the DeboardTenant response's data field.
type DeboardResult struct {
	CloudProviderAccountID string `json:"cloudProviderAccountId"`
	Status                 string `json:"status"` // DE_BOARDED or ALREADY_DE_BOARDED
	Message                string `json:"message"`
}

// DeboardTenant marks a tenant INACTIVE. Idempotent: deboarding an
// already-inactive tenant succeeds (Status == DeboardStatusAlreadyDeBoarded)
// rather than erroring. IRREVERSIBLE via API — an INACTIVE tenant can only
// be reactivated by Lucidity support.
func (c *Client) DeboardTenant(ctx context.Context, cloudProvider, cloudProviderAccountID string) (*DeboardResult, error) {
	req := DeboardTenantRequest{CloudEntityInformation: CloudEntityInformation{
		CloudProvider:          cloudProvider,
		CloudProviderAccountID: cloudProviderAccountID,
	}}
	var out DeboardResult
	if err := c.Do(ctx, http.MethodPut, tenantsDeboardPath, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
