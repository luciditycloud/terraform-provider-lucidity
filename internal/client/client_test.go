package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const protectedPath = "/external/client/api/v1/tenants"

type protectedResult struct {
	Foo string `json:"foo"`
}

func newMockServer(t *testing.T, refreshTokenCalls *int32, protectedHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(refreshEndpoint, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(refreshTokenCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(refreshResponseBody{AccessToken: fmt.Sprintf("access-token-%d", n)})
	})
	mux.HandleFunc(protectedPath, protectedHandler)
	return httptest.NewServer(mux)
}

func TestClient_401RetrySucceedsAfterForcedRefresh(t *testing.T) {
	var refreshCalls, protectedCalls int32
	srv := newMockServer(t, &refreshCalls, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&protectedCalls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(envelope{
			Success:   true,
			Data:      json.RawMessage(`{"foo":"bar"}`),
			RequestID: "req-123",
		})
	})
	defer srv.Close()

	c := NewClient(srv.URL, "refresh-secret", WithHTTPClient(srv.Client()), withRetryBaseDelay(time.Millisecond))

	var out protectedResult
	if err := c.Do(context.Background(), http.MethodGet, protectedPath, nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out.Foo != "bar" {
		t.Fatalf("got %+v, want Foo=bar", out)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 2 {
		t.Fatalf("expected 2 refresh calls (initial + forced), got %d", got)
	}
	if got := atomic.LoadInt32(&protectedCalls); got != 2 {
		t.Fatalf("expected 2 protected-endpoint calls (401 then success), got %d", got)
	}
}

func TestClient_401RetryStillFails(t *testing.T) {
	var refreshCalls int32
	srv := newMockServer(t, &refreshCalls, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	defer srv.Close()

	c := NewClient(srv.URL, "refresh-secret", WithHTTPClient(srv.Client()), withRetryBaseDelay(time.Millisecond))

	err := c.Do(context.Background(), http.MethodGet, protectedPath, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != AuthFailedMessage {
		t.Fatalf("got %q, want the exact required auth-failed message", err.Error())
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 2 {
		t.Fatalf("expected exactly 2 refresh calls (initial + one forced retry, no more), got %d", got)
	}
}

// TestClient_401CloudAccountValidationFailureSurfacesRealMessage guards
// against masking a business-logic 401 ("the cloud account could not be
// validated", per the Public Tenant API doc) behind AuthError's fixed
// expired-refresh-token message, even though both share HTTP 401 and
// error.code UNAUTHORIZED.
func TestClient_401CloudAccountValidationFailureSurfacesRealMessage(t *testing.T) {
	var refreshCalls int32
	const wantMessage = "Authentication failed: the cloud account could not be validated."
	srv := newMockServer(t, &refreshCalls, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(envelope{
			Success:   false,
			Error:     &envelopeError{Code: "UNAUTHORIZED", Message: wantMessage},
			RequestID: "req-cloud-validation",
		})
	})
	defer srv.Close()

	c := NewClient(srv.URL, "refresh-secret", WithHTTPClient(srv.Client()), withRetryBaseDelay(time.Millisecond))

	err := c.Do(context.Background(), http.MethodPost, protectedPath, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() == AuthFailedMessage {
		t.Fatalf("got the generic AuthError message, want the real API message %q", wantMessage)
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("got error of type %T, want *APIError", err)
	}
	if apiErr.Message != wantMessage {
		t.Fatalf("got message %q, want %q", apiErr.Message, wantMessage)
	}
	if apiErr.Code != "UNAUTHORIZED" {
		t.Fatalf("got code %q, want UNAUTHORIZED", apiErr.Code)
	}
	if apiErr.RequestID != "req-cloud-validation" {
		t.Fatalf("got requestId %q, want req-cloud-validation", apiErr.RequestID)
	}
}

// TestClient_401OnFinalRetryAttemptStillRetriesWithFreshToken guards against
// a bug where the 5xx-backoff budget and the one sanctioned 401-forced-refresh
// retry shared a single bounded attempt counter: 3 straight 500s followed by
// a first-time 401 on the 4th (last) attempt would force-refresh the token
// but then exit the loop before retrying with it, surfacing a generic
// "exhausted retries" error instead of actually succeeding.
func TestClient_401OnFinalRetryAttemptStillRetriesWithFreshToken(t *testing.T) {
	var refreshCalls, protectedCalls int32
	srv := newMockServer(t, &refreshCalls, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&protectedCalls, 1)
		switch {
		case n <= 3:
			w.WriteHeader(http.StatusInternalServerError)
		case n == 4:
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(envelope{Success: true, Data: json.RawMessage(`{"foo":"bar"}`)})
		}
	})
	defer srv.Close()

	c := NewClient(srv.URL, "refresh-secret", WithHTTPClient(srv.Client()), withRetryBaseDelay(time.Millisecond))

	var out protectedResult
	if err := c.Do(context.Background(), http.MethodGet, protectedPath, nil, &out); err != nil {
		t.Fatalf("Do: %v (expected the 401-forced-refresh retry to succeed, not be dropped)", err)
	}
	if out.Foo != "bar" {
		t.Fatalf("got %+v, want Foo=bar", out)
	}
	if got := atomic.LoadInt32(&protectedCalls); got != 5 {
		t.Fatalf("expected 5 protected-endpoint calls (3x 500, 1x 401, 1x success), got %d", got)
	}
}

func TestClient_RetriesOn5xxNotOn4xx(t *testing.T) {
	var refreshCalls, protectedCalls int32
	srv := newMockServer(t, &refreshCalls, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&protectedCalls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(envelope{Success: true, Data: json.RawMessage(`{"foo":"bar"}`)})
	})
	defer srv.Close()

	c := NewClient(srv.URL, "refresh-secret", WithHTTPClient(srv.Client()), withRetryBaseDelay(time.Millisecond))
	var out protectedResult
	if err := c.Do(context.Background(), http.MethodGet, protectedPath, nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out.Foo != "bar" {
		t.Fatalf("got %+v, want Foo=bar", out)
	}

	// A plain 400 must never be retried.
	protectedCalls = 0
	srv2 := newMockServer(t, &refreshCalls, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&protectedCalls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(envelope{Success: false, Error: &envelopeError{Code: "INVALID_REQUEST", Message: "bad field"}})
	})
	defer srv2.Close()
	c2 := NewClient(srv2.URL, "refresh-secret", WithHTTPClient(srv2.Client()), withRetryBaseDelay(time.Millisecond))
	err := c2.Do(context.Background(), http.MethodGet, protectedPath, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("got %T, want *APIError", err)
	}
	if apiErr.Code != "INVALID_REQUEST" {
		t.Fatalf("got code %q, want INVALID_REQUEST", apiErr.Code)
	}
	if got := atomic.LoadInt32(&protectedCalls); got != 1 {
		t.Fatalf("expected exactly 1 call for a 4xx (no retry), got %d", got)
	}
}

// TestNoSecretLeaksInDebugLog guards the "never logged, including
// TF_LOG=DEBUG" requirement: it captures everything passed to DebugLog
// across a full refresh + API-call cycle and asserts neither the refresh
// token nor the issued access token ever appears in it.
func TestNoSecretLeaksInDebugLog(t *testing.T) {
	const refreshSecret = "super-secret-refresh-token-value"
	var refreshCalls int32
	srv := newMockServer(t, &refreshCalls, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(envelope{Success: true, Data: json.RawMessage(`{"foo":"bar"}`), RequestID: "req-999"})
	})
	defer srv.Close()

	c := NewClient(srv.URL, refreshSecret, WithHTTPClient(srv.Client()), withRetryBaseDelay(time.Millisecond))

	var captured strings.Builder
	c.DebugLog = func(_ context.Context, msg string, fields map[string]any) {
		captured.WriteString(msg)
		for k, v := range fields {
			fmt.Fprintf(&captured, " %s=%v", k, v)
		}
		captured.WriteString("\n")
	}

	var out protectedResult
	if err := c.Do(context.Background(), http.MethodGet, protectedPath, nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}

	logOutput := captured.String()
	if logOutput == "" {
		t.Fatal("expected DebugLog to be called at least once")
	}
	if strings.Contains(logOutput, refreshSecret) {
		t.Fatalf("refresh token leaked into debug log: %s", logOutput)
	}
	if !strings.Contains(logOutput, "requestId=req-999") {
		t.Fatalf("expected requestId to be logged for support escalation, got: %s", logOutput)
	}

	// The issued access token must not leak either, whatever its value was.
	c.tokens.mu.Lock()
	issuedAccessToken := c.tokens.accessToken
	c.tokens.mu.Unlock()
	if issuedAccessToken == "" {
		t.Fatal("expected an access token to have been issued")
	}
	if strings.Contains(logOutput, issuedAccessToken) {
		t.Fatalf("access token leaked into debug log: %s", logOutput)
	}
}

// TestClient_DoRespectsContextCancellationWhileWaitingForSemaphore proves the
// semaphore-acquisition fix: with the single concurrency slot held by
// another in-flight call, a second Do() whose context is already cancelled
// must return promptly with ctx.Err() instead of blocking until the first
// call releases the slot.
func TestClient_DoRespectsContextCancellationWhileWaitingForSemaphore(t *testing.T) {
	var refreshCalls int32
	holdFirstCall := make(chan struct{})
	firstCallStarted := make(chan struct{})
	srv := newMockServer(t, &refreshCalls, func(w http.ResponseWriter, r *http.Request) {
		close(firstCallStarted)
		<-holdFirstCall
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(envelope{Success: true, Data: json.RawMessage(`{"foo":"bar"}`)})
	})
	defer srv.Close()

	c := NewClient(srv.URL, "refresh-secret", WithHTTPClient(srv.Client()), withRetryBaseDelay(time.Millisecond), WithMaxParallelRequests(1))

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- c.Do(context.Background(), http.MethodGet, protectedPath, nil, nil)
	}()

	<-firstCallStarted // the one semaphore slot is now held

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.Do(cancelledCtx, http.MethodGet, protectedPath, nil, nil)
	close(holdFirstCall)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}

	if err := <-firstDone; err != nil {
		t.Fatalf("first Do call: %v", err)
	}
}

// The refresh endpoint (and the temporary buffer endpoints) document a flat
// error body; its code must be recognized instead of falling back to UNKNOWN.
func TestParseErrorBody_FlatShape(t *testing.T) {
	err := parseErrorBody(http.StatusBadRequest, []byte(`{"code":"INVALID_REQUEST","message":"refreshToken is required","details":null}`))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "INVALID_REQUEST" || apiErr.Message != "refreshToken is required" || apiErr.Details != "" {
		t.Fatalf("got %+v, want INVALID_REQUEST with no details", err)
	}

	err = parseErrorBody(http.StatusConflict, []byte(`{"code":"CONFLICT","message":"busy","details":{"id":"x-1"}}`))
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.Error(), "x-1") {
		t.Fatalf("got %v, want details shown in the error", err)
	}
}

// A body without the envelope's "success" key is decoded directly; an
// enveloped body still decodes its "data".
func TestDecodeEnvelope_BareAndWrapped(t *testing.T) {
	var out struct {
		ID string `json:"id"`
	}
	if err := decodeEnvelope([]byte(`{"id":"bare"}`), &out); err != nil || out.ID != "bare" {
		t.Fatalf("bare: got %+v, %v", out, err)
	}
	out.ID = ""
	if err := decodeEnvelope([]byte(`{"success":true,"data":{"id":"wrapped"},"error":null,"requestId":"r"}`), &out); err != nil || out.ID != "wrapped" {
		t.Fatalf("wrapped: got %+v, %v", out, err)
	}
}
