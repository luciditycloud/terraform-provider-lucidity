package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func TestRequestTempBuffer_BareResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tempBufferRequestPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("got method %s, want PATCH", r.Method)
		}
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		id := raw["instanceIdentifier"].(map[string]any)
		if id["cloudProvider"] != "AWS" || id["instanceId"] != "i-0abcd1234efgh5678" {
			t.Fatalf("unexpected instanceIdentifier: %+v", id)
		}
		if _, ok := id["resourceGroup"]; ok {
			t.Fatalf("resourceGroup must be omitted for AWS, got: %+v", id)
		}
		if raw["requestBufferSizeInGiB"] != float64(50) || raw["requestId"] != "maintenance-2024-001" {
			t.Fatalf("unexpected body: %+v", raw)
		}
		writeJSON(w, http.StatusOK, map[string]any{"bufferId": "buf-abc123def456"})
	})
	c, _ := newTenantTestClient(t, mux)

	out, err := c.RequestTempBuffer(context.Background(), RequestTempBufferRequest{
		InstanceIdentifier: InstanceIdentifier{
			CloudProviderAccountID: "123456789012",
			CloudProvider:          "AWS",
			InstanceID:             "i-0abcd1234efgh5678",
		},
		RequestBufferSizeInGiB: 50,
		RequestID:              "maintenance-2024-001",
	})
	if err != nil {
		t.Fatalf("RequestTempBuffer: %v", err)
	}
	if out.BufferID != "buf-abc123def456" {
		t.Fatalf("got bufferId %q, want buf-abc123def456", out.BufferID)
	}
}

// The docs show bare responses, but every other endpoint wraps them in the
// envelope; both must decode.
func TestRequestTempBuffer_EnvelopeResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tempBufferRequestPath, func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, http.StatusOK, true, map[string]any{"bufferId": "buf-wrapped"}, nil, "req-1")
	})
	c, _ := newTenantTestClient(t, mux)

	out, err := c.RequestTempBuffer(context.Background(), RequestTempBufferRequest{RequestID: "r"})
	if err != nil {
		t.Fatalf("RequestTempBuffer: %v", err)
	}
	if out.BufferID != "buf-wrapped" {
		t.Fatalf("got bufferId %q, want buf-wrapped", out.BufferID)
	}
}

func TestRequestTempBuffer_ConflictFlatError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tempBufferRequestPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":    "BUFFER_ALREADY_ACTIVE",
			"message": "Another buffer is already active on this instance.",
			"details": map[string]any{"bufferId": "buf-existing"},
		})
	})
	c, _ := newTenantTestClient(t, mux)

	_, err := c.RequestTempBuffer(context.Background(), RequestTempBufferRequest{RequestID: "r"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %v, want *APIError", err)
	}
	if apiErr.HTTPStatus != http.StatusConflict || apiErr.Code != "BUFFER_ALREADY_ACTIVE" {
		t.Fatalf("got %+v, want 409 BUFFER_ALREADY_ACTIVE", apiErr)
	}
	if !strings.Contains(apiErr.Details, "buf-existing") || !strings.Contains(apiErr.Error(), "buf-existing") {
		t.Fatalf("expected details to be kept and shown, got %+v / %q", apiErr, apiErr.Error())
	}
}

// A 5xx is retried with the same requestId — the documented dedup key.
func TestRequestTempBuffer_RetriesWithSameRequestID(t *testing.T) {
	var calls int32
	var seen []string
	mux := http.NewServeMux()
	mux.HandleFunc(tempBufferRequestPath, func(w http.ResponseWriter, r *http.Request) {
		var req RequestTempBufferRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		seen = append(seen, req.RequestID)
		if atomic.AddInt32(&calls, 1) == 1 {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "INTERNAL", "message": "boom"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"bufferId": "buf-1"})
	})
	c, _ := newTenantTestClient(t, mux)

	out, err := c.RequestTempBuffer(context.Background(), RequestTempBufferRequest{RequestID: "req-fixed"})
	if err != nil {
		t.Fatalf("RequestTempBuffer: %v", err)
	}
	if out.BufferID != "buf-1" || len(seen) != 2 || seen[0] != "req-fixed" || seen[1] != "req-fixed" {
		t.Fatalf("got bufferId %q, requestIds %v; want buf-1 and the same requestId twice", out.BufferID, seen)
	}
}

func TestRequestTempBuffer_ErrorsOnMissingBufferID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tempBufferRequestPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	c, _ := newTenantTestClient(t, mux)

	if _, err := c.RequestTempBuffer(context.Background(), RequestTempBufferRequest{RequestID: "r"}); err == nil {
		t.Fatal("expected an error for a success response without bufferId")
	}
}

func TestGetTempBufferStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tempBufferBasePath+"buf-abc123def456/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("got method %s, want GET", r.Method)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"instanceId":            "i-0abcd1234efgh5678",
			"status":                "ACTIVE",
			"currentFreeSpaceInGiB": 75,
			"bufferId":              "buf-abc123def456",
			"failureReason":         nil,
		})
	})
	c, _ := newTenantTestClient(t, mux)

	out, err := c.GetTempBufferStatus(context.Background(), "buf-abc123def456")
	if err != nil {
		t.Fatalf("GetTempBufferStatus: %v", err)
	}
	if out.Status != TempBufferStatusActive || out.CurrentFreeSpaceInGiB != 75 || out.FailureReason != nil || out.InstanceID != "i-0abcd1234efgh5678" {
		t.Fatalf("unexpected status: %+v", out)
	}
}

func TestGetTempBufferStatus_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tempBufferBasePath+"buf-gone/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "NOT_FOUND", "message": "Buffer not found."})
	})
	c, _ := newTenantTestClient(t, mux)

	_, err := c.GetTempBufferStatus(context.Background(), "buf-gone")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != http.StatusNotFound {
		t.Fatalf("got %v, want a 404 *APIError", err)
	}
}

func TestRevokeTempBuffer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tempBufferBasePath+"buf-abc123def456/revoke", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("got method %s, want POST", r.Method)
		}
		var req RevokeTempBufferRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.BufferID != "buf-abc123def456" {
			t.Fatalf("got body bufferId %q, want buf-abc123def456", req.BufferID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"message": "Buffer revoked successfully"})
	})
	c, _ := newTenantTestClient(t, mux)

	out, err := c.RevokeTempBuffer(context.Background(), "buf-abc123def456")
	if err != nil {
		t.Fatalf("RevokeTempBuffer: %v", err)
	}
	if out.Message != "Buffer revoked successfully" {
		t.Fatalf("got message %q", out.Message)
	}
}

// The refresh endpoint documents the same flat error shape; its code must
// now be recognized instead of falling back to UNKNOWN.
func TestParseErrorBody_FlatShape(t *testing.T) {
	err := parseErrorBody(http.StatusBadRequest, []byte(`{"code":"INVALID_REQUEST","message":"refreshToken is required","details":null}`))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "INVALID_REQUEST" || apiErr.Message != "refreshToken is required" || apiErr.Details != "" {
		t.Fatalf("got %+v, want INVALID_REQUEST with no details", err)
	}
}
