package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Temporary buffer API endpoints, per the Temporary Buffer guide and API
// reference. Note the request path uses singular "instance" while status
// and revoke use plural "instances" — that is how the docs spell them.
const (
	tempBufferRequestPath = "/external/api/v1/autoscaler/instance/buffer/request"
	tempBufferBasePath    = "/external/api/v1/autoscaler/instances/buffer/"
)

// Temporary buffer status values, per the status response "status" field.
const (
	TempBufferStatusProvisioning = "PROVISIONING"
	TempBufferStatusActive       = "ACTIVE"
	TempBufferStatusRevoked      = "REVOKED"
	TempBufferStatusFailed       = "FAILED"
)

// InstanceIdentifier identifies the cloud instance a buffer is requested
// for. ResourceGroup is Azure-only; InstanceID may be the VM name in Azure.
type InstanceIdentifier struct {
	CloudProviderAccountID string `json:"cloudProviderAccountId"`
	CloudProvider          string `json:"cloudProvider"`
	InstanceID             string `json:"instanceId"`
	ResourceGroup          string `json:"resourceGroup,omitempty"`
}

// RequestTempBufferRequest is the PATCH .../instance/buffer/request body.
// RequestID is client-generated: Lucidity uses it to deduplicate, so
// resending the same request with the same RequestID must not create a
// second buffer. If absent, the request is treated as new.
type RequestTempBufferRequest struct {
	InstanceIdentifier     InstanceIdentifier `json:"instanceIdentifier"`
	RequestBufferSizeInGiB int64              `json:"requestBufferSizeInGiB"`
	RequestID              string             `json:"requestId,omitempty"`
}

// TempBufferRequestResult is the request call's success response.
type TempBufferRequestResult struct {
	BufferID string `json:"bufferId"`
}

// TempBufferStatus is the status call's success response.
type TempBufferStatus struct {
	InstanceID            string  `json:"instanceId"`
	Status                string  `json:"status"`
	CurrentFreeSpaceInGiB int64   `json:"currentFreeSpaceInGiB"`
	BufferID              string  `json:"bufferId"`
	FailureReason         *string `json:"failureReason"`
}

// RevokeTempBufferRequest is the POST .../{bufferId}/revoke body. The docs
// require bufferId in both the path and the body.
type RevokeTempBufferRequest struct {
	BufferID string `json:"bufferId"`
}

// RevokeTempBufferResult is the revoke call's success response.
type RevokeTempBufferResult struct {
	Message string `json:"message"`
}

// RequestTempBuffer asks Lucidity to add a temporary buffer to an instance.
// The buffer starts in PROVISIONING; poll GetTempBufferStatus until it is
// ACTIVE or FAILED. Only one buffer can be active per instance (409
// otherwise).
//
// Uses Do, not DoNoRetry5xx (unlike OnboardTenant): the request carries a
// RequestID that Lucidity documents as its deduplication key, so resending
// it after a 5xx is the documented safe recovery. There is also no list
// endpoint to find a buffer created by a lost response, so a retry with the
// same RequestID is the only way to recover its bufferId. Callers must
// always set RequestID for this to hold.
func (c *Client) RequestTempBuffer(ctx context.Context, req RequestTempBufferRequest) (*TempBufferRequestResult, error) {
	var out TempBufferRequestResult
	if err := c.Do(ctx, http.MethodPatch, tempBufferRequestPath, req, &out); err != nil {
		return nil, err
	}
	if out.BufferID == "" {
		return nil, fmt.Errorf("lucidity: buffer request succeeded but returned no bufferId")
	}
	return &out, nil
}

// GetTempBufferStatus returns a buffer's current status and free space.
func (c *Client) GetTempBufferStatus(ctx context.Context, bufferID string) (*TempBufferStatus, error) {
	var out TempBufferStatus
	if err := c.Do(ctx, http.MethodGet, tempBufferBasePath+url.PathEscape(bufferID)+"/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeTempBuffer removes a buffer and returns the instance to its original
// scaling policy. The buffer keeps incurring cloud storage cost until this
// is called.
func (c *Client) RevokeTempBuffer(ctx context.Context, bufferID string) (*RevokeTempBufferResult, error) {
	var out RevokeTempBufferResult
	if err := c.Do(ctx, http.MethodPost, tempBufferBasePath+url.PathEscape(bufferID)+"/revoke", RevokeTempBufferRequest{BufferID: bufferID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
