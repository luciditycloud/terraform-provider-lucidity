package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTenantTestClient(t *testing.T, mux *http.ServeMux) (*Client, *httptest.Server) {
	t.Helper()
	mux.HandleFunc(refreshEndpoint, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(refreshResponseBody{AccessToken: "access-token"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "refresh-secret", WithHTTPClient(srv.Client()), withRetryBaseDelay(time.Millisecond))
	return c, srv
}

func writeEnvelope(w http.ResponseWriter, status int, success bool, data any, apiErr *envelopeError, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var raw json.RawMessage
	if data != nil {
		b, _ := json.Marshal(data)
		raw = b
	}
	_ = json.NewEncoder(w).Encode(envelope{Success: success, Data: raw, Error: apiErr, RequestID: requestID})
}

func TestOnboardTenant_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsOnboardPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("got method %s, want POST", r.Method)
		}
		var req OnboardTenantRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		if req.CloudEntityInformation.CloudProvider != "AWS" {
			t.Fatalf("got cloudProvider %q, want AWS", req.CloudEntityInformation.CloudProvider)
		}
		writeEnvelope(w, http.StatusCreated, true, OnboardedTenant{
			TenantID:               "acme_123456789012",
			CloudProvider:          "AWS",
			CloudProviderAccountID: "123456789012",
			DisplayName:            "Acme Prod",
			Status:                 TenantStatusActive,
			Products:               []string{"AUTOSCALER"},
		}, nil, "req-1")
	})
	c, _ := newTenantTestClient(t, mux)

	out, err := c.OnboardTenant(context.Background(), OnboardTenantRequest{
		CloudEntityInformation: CloudEntityInformation{
			CloudProvider:          "AWS",
			CloudProviderAccountID: "123456789012",
			ExternalID:             "ext-id",
			AWSIAMRoleName:         "LucidityRole",
			AWSIAMPolicyName:       "LucidityPolicy",
		},
		DisplayName: "Acme Prod",
		ProductList: []string{"AUTOSCALER"},
	})
	if err != nil {
		t.Fatalf("OnboardTenant: %v", err)
	}
	if out.TenantID != "acme_123456789012" || out.Status != TenantStatusActive {
		t.Fatalf("got %+v, want tenantId=acme_123456789012 status=ACTIVE", out)
	}
}

func TestOnboardTenant_ConflictActiveDuplicate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsOnboardPath, func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, http.StatusConflict, false, nil, &envelopeError{
			Code:    "CONFLICT",
			Message: "A tenant already exists for cloudProviderAccountId '123456789012'.",
		}, "req-2")
	})
	c, _ := newTenantTestClient(t, mux)

	_, err := c.OnboardTenant(context.Background(), OnboardTenantRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got error %v (%T), want *APIError", err, err)
	}
	if apiErr.Code != "CONFLICT" || apiErr.HTTPStatus != http.StatusConflict {
		t.Fatalf("got %+v, want CONFLICT/409", apiErr)
	}
}

func TestOnboardTenant_ConflictInactive(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsOnboardPath, func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, http.StatusConflict, false, nil, &envelopeError{
			Code:    "CONFLICT",
			Message: "An already deboarded (INACTIVE) tenant exists for cloudProviderAccountId '123456789012'; re-onboarding support does not exist right now.",
		}, "req-3")
	})
	c, _ := newTenantTestClient(t, mux)

	_, err := c.OnboardTenant(context.Background(), OnboardTenantRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got error %v (%T), want *APIError", err, err)
	}
	if apiErr.Code != "CONFLICT" {
		t.Fatalf("got code %q, want CONFLICT", apiErr.Code)
	}
}

func TestOnboardTenant_CloudAccountValidationFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsOnboardPath, func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, http.StatusUnauthorized, false, nil, &envelopeError{
			Code:    "UNAUTHORIZED",
			Message: "Authentication failed: the cloud account could not be validated.",
		}, "req-4")
	})
	c, _ := newTenantTestClient(t, mux)

	_, err := c.OnboardTenant(context.Background(), OnboardTenantRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got error %v (%T), want *APIError (not AuthError)", err, err)
	}
	if apiErr.RequestID != "req-4" {
		t.Fatalf("got requestId %q, want req-4 (lost the real error detail)", apiErr.RequestID)
	}
}

func TestListTenants_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("got method %s, want GET", r.Method)
		}
		writeEnvelope(w, http.StatusOK, true, tenantListResponse{
			Tenants: []TenantListItem{
				{TenantID: "t1", CloudProvider: "AWS", CloudProviderAccountID: "111", CloudEntityName: "acme-1", DisplayName: "non-prod", Status: TenantStatusActive},
				{TenantID: "t2", CloudProvider: "AWS", CloudProviderAccountID: "222", CloudEntityName: "acme-2", DisplayName: "qa", Status: TenantStatusInactive},
			},
			Meta: tenantListMeta{TotalCount: 2},
		}, nil, "req-5")
	})
	c, _ := newTenantTestClient(t, mux)

	tenants, err := c.ListTenants(context.Background())
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("got %d tenants, want 2", len(tenants))
	}
	if got, found := FindTenant(tenants, "AWS", "222"); !found || got.Status != TenantStatusInactive {
		t.Fatalf("FindTenant(AWS,222) = %+v, found=%v, want INACTIVE", got, found)
	}
	if _, found := FindTenant(tenants, "AWS", "999"); found {
		t.Fatal("FindTenant(AWS,999) unexpectedly found a match")
	}
}

func TestUpdateTenant_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("got method %s, want PATCH", r.Method)
		}
		writeEnvelope(w, http.StatusNotFound, false, nil, &envelopeError{
			Code:    "NOT_FOUND",
			Message: "No tenant exists for cloudProviderAccountId '111'.",
		}, "req-6")
	})
	c, _ := newTenantTestClient(t, mux)

	_, err := c.UpdateTenant(context.Background(), UpdateTenantRequest{
		CloudEntityInformation: CloudEntityInformation{CloudProvider: "AWS", CloudProviderAccountID: "111"},
		DisplayName:            "renamed",
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "NOT_FOUND" {
		t.Fatalf("got %v, want *APIError with code NOT_FOUND", err)
	}
}

func TestUpdateTenant_ConflictNotActive(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsPath, func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, http.StatusConflict, false, nil, &envelopeError{
			Code:    "CONFLICT",
			Message: "Tenant for cloudProviderAccountId '111' is not ACTIVE; cannot update.",
		}, "req-7")
	})
	c, _ := newTenantTestClient(t, mux)

	_, err := c.UpdateTenant(context.Background(), UpdateTenantRequest{
		CloudEntityInformation: CloudEntityInformation{CloudProvider: "AWS", CloudProviderAccountID: "111"},
		DisplayName:            "renamed",
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "CONFLICT" {
		t.Fatalf("got %v, want *APIError with code CONFLICT", err)
	}
}

func TestDeboardTenant_DeBoardedAndIdempotent(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsDeboardPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("got method %s, want PUT", r.Method)
		}
		calls++
		status, message := DeboardStatusDeBoarded, "cloudProviderAccountId '111' has been marked INACTIVE."
		if calls > 1 {
			status, message = DeboardStatusAlreadyDeBoarded, "cloudProviderAccountId '111' is already deboarded."
		}
		writeEnvelope(w, http.StatusOK, true, DeboardResult{
			CloudProviderAccountID: "111",
			Status:                 status,
			Message:                message,
		}, nil, "req-8")
	})
	c, _ := newTenantTestClient(t, mux)

	first, err := c.DeboardTenant(context.Background(), "AWS", "111")
	if err != nil {
		t.Fatalf("DeboardTenant (first): %v", err)
	}
	if first.Status != DeboardStatusDeBoarded {
		t.Fatalf("got status %q, want DE_BOARDED", first.Status)
	}

	second, err := c.DeboardTenant(context.Background(), "AWS", "111")
	if err != nil {
		t.Fatalf("DeboardTenant (second, idempotent): %v", err)
	}
	if second.Status != DeboardStatusAlreadyDeBoarded {
		t.Fatalf("got status %q, want ALREADY_DE_BOARDED", second.Status)
	}
}

func TestDeboardTenant_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsDeboardPath, func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, http.StatusNotFound, false, nil, &envelopeError{
			Code:    "NOT_FOUND",
			Message: "No tenant exists for cloudProviderAccountId '999'; nothing to deboard.",
		}, "req-9")
	})
	c, _ := newTenantTestClient(t, mux)

	_, err := c.DeboardTenant(context.Background(), "AWS", "999")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "NOT_FOUND" {
		t.Fatalf("got %v, want *APIError with code NOT_FOUND", err)
	}
}

func TestOnboardTenant_DoesNotRetry5xx(t *testing.T) {
	var posts int32
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsOnboardPath, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posts, 1)
		writeEnvelope(w, http.StatusInternalServerError, false, nil, &envelopeError{Code: "INTERNAL_ERROR", Message: "boom"}, "req-500")
	})
	c, _ := newTenantTestClient(t, mux)

	_, err := c.OnboardTenant(context.Background(), OnboardTenantRequest{})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got := atomic.LoadInt32(&posts); got != 1 {
		t.Fatalf("expected exactly 1 onboard POST on a 5xx (no auto-retry), got %d", got)
	}
	if !IsOutcomeUnknown(context.Background(), err) {
		t.Fatalf("a 5xx must be classified outcome-unknown, got: %v", err)
	}
}

func TestOnboardTenant_StillRetries401AfterRefresh(t *testing.T) {
	var posts int32
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsOnboardPath, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&posts, 1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeEnvelope(w, http.StatusCreated, true, OnboardedTenant{TenantID: "t"}, nil, "req-ok")
	})
	c, _ := newTenantTestClient(t, mux)

	if _, err := c.OnboardTenant(context.Background(), OnboardTenantRequest{}); err != nil {
		t.Fatalf("OnboardTenant: %v", err)
	}
	if got := atomic.LoadInt32(&posts); got != 2 {
		t.Fatalf("expected 2 POSTs (401 then success after forced refresh), got %d", got)
	}
}

func TestIsOutcomeUnknown(t *testing.T) {
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	conn := &TransportError{Err: errors.New("connection reset by peer")}
	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"nil", ctx, nil, false},
		{"5xx", ctx, &APIError{HTTPStatus: 503, Code: "X"}, true},
		{"wrapped 5xx", ctx, fmt.Errorf("wrap: %w", &APIError{HTTPStatus: 500}), true},
		{"4xx conflict", ctx, &APIError{HTTPStatus: 409, Code: "CONFLICT"}, false},
		{"4xx validation", ctx, &APIError{HTTPStatus: 400, Code: "INVALID_REQUEST"}, false},
		{"auth error", ctx, AuthError{}, false},
		{"transport error", ctx, conn, true},
		{"transport error but caller cancelled", cancelled, conn, false},
		{"plain error", ctx, errors.New("encoding body"), false},
	}
	for _, tc := range cases {
		if got := IsOutcomeUnknown(tc.ctx, tc.err); got != tc.want {
			t.Errorf("%s: IsOutcomeUnknown = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestOnboardTenant_ConnectionDropIsOutcomeUnknown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsOnboardPath, func(w http.ResponseWriter, r *http.Request) {
		// Hijack and close without writing a response: the request was
		// received but the client never learns the outcome.
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	})
	c, _ := newTenantTestClient(t, mux)

	_, err := c.OnboardTenant(context.Background(), OnboardTenantRequest{})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !IsOutcomeUnknown(context.Background(), err) {
		t.Fatalf("a dropped connection must be classified outcome-unknown, got: %v", err)
	}
}

func TestListTenants_ExternalIDPresence(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"tenants":[
			{"tenantId":"a","cloudProvider":"AWS","cloudProviderAccountId":"1","externalId":"8f14e45f-ceea-4331-9f5e-111111111111"},
			{"tenantId":"b","cloudProvider":"AWS","cloudProviderAccountId":"2","externalId":null},
			{"tenantId":"c","cloudProvider":"AWS","cloudProviderAccountId":"3"}
		],"meta":{"totalCount":3}}}`))
	})
	c, _ := newTenantTestClient(t, mux)

	got, err := c.ListTenants(context.Background())
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d tenants, want 3", len(got))
	}
	if !got[0].ExternalIDReported || got[0].ExternalID == nil || *got[0].ExternalID != "8f14e45f-ceea-4331-9f5e-111111111111" {
		t.Errorf("value case: got %+v", got[0])
	}
	if !got[1].ExternalIDReported || got[1].ExternalID != nil {
		t.Errorf("explicit null case: want reported-but-nil, got %+v", got[1])
	}
	if got[2].ExternalIDReported || got[2].ExternalID != nil {
		t.Errorf("absent key case (older backend): want not reported, got %+v", got[2])
	}
	if got[0].TenantID != "a" || got[2].CloudProviderAccountID != "3" {
		t.Errorf("other fields must still decode, got %+v / %+v", got[0], got[2])
	}
}

func TestUpdateTenant_SendsExternalID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tenantsPath, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if got := body["cloudEntityInformation"]["externalId"]; got != "new-ext" {
			t.Errorf("expected externalId=new-ext in PATCH body, got %v", got)
		}
		writeEnvelope(w, http.StatusOK, true, UpdatedTenant{TenantID: "t"}, nil, "req-u")
	})
	c, _ := newTenantTestClient(t, mux)

	_, err := c.UpdateTenant(context.Background(), UpdateTenantRequest{CloudEntityInformation: CloudEntityInformation{
		CloudProvider: "AWS", CloudProviderAccountID: "1", ExternalID: "new-ext",
	}})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
}

func TestListTenants_TotalCountGuard(t *testing.T) {
	cases := []struct {
		name      string
		total     int
		wantTrunc bool
	}{
		{"matches returned", 2, false},
		{"larger than returned: truncated", 5, true},
		{"missing/zero (older backend)", 0, false},
		{"smaller than returned (stale count)", 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc(tenantsPath, func(w http.ResponseWriter, r *http.Request) {
				writeEnvelope(w, http.StatusOK, true, map[string]any{
					"tenants": []map[string]any{
						{"tenantId": "a", "cloudProvider": "AWS", "cloudProviderAccountId": "1", "status": "ACTIVE"},
						{"tenantId": "b", "cloudProvider": "AWS", "cloudProviderAccountId": "2", "status": "INACTIVE"},
					},
					"meta": map[string]any{"totalCount": tc.total},
				}, nil, "req-l")
			})
			c, _ := newTenantTestClient(t, mux)

			got, err := c.ListTenants(context.Background())
			var te *TruncatedListError
			if isTrunc := errors.As(err, &te); isTrunc != tc.wantTrunc {
				t.Fatalf("truncated error = %v, want %v (err=%v)", isTrunc, tc.wantTrunc, err)
			}
			if tc.wantTrunc {
				if te.Returned != 2 || te.Total != 5 || got != nil {
					t.Fatalf("unexpected truncation result: %+v got=%v", te, got)
				}
			} else if err != nil || len(got) != 2 {
				t.Fatalf("expected both tenants and no error, got %v / %v", got, err)
			}
		})
	}
}
