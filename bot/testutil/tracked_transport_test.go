package testutil_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deichbewohner/rocketbot/bot/testutil"
)

func TestTrackedTransport_DetectsLeaks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response"))
	}))
	defer server.Close()

	tracked := testutil.NewTrackedTransport(nil)
	client := &http.Client{Transport: tracked}

	// Make request but don't close body - should leak
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp // intentionally not closing

	opened, closed := tracked.Stats()
	if opened != 1 {
		t.Errorf("opened = %d, want 1", opened)
	}
	if closed != 0 {
		t.Errorf("closed = %d, want 0 (leak test)", closed)
	}
}

func TestTrackedTransport_NoLeakWhenClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response"))
	}))
	defer server.Close()

	tracked := testutil.NewTrackedTransport(nil)
	defer tracked.AssertAllClosed(t)

	client := &http.Client{Transport: tracked}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// Read the body
	_, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	// Close explicitly before checking stats
	resp.Body.Close()

	opened, closed := tracked.Stats()
	if opened != 1 {
		t.Errorf("opened = %d, want 1", opened)
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1", closed)
	}
}

func TestTrackedTransport_MultipleRequests(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response"))
	}))
	defer server.Close()

	tracked := testutil.NewTrackedTransport(nil)
	defer tracked.AssertAllClosed(t)

	client := &http.Client{Transport: tracked}

	// Make 3 requests, close all properly
	for i := 0; i < 3; i++ {
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	if callCount != 3 {
		t.Errorf("server called %d times, want 3", callCount)
	}

	opened, closed := tracked.Stats()
	if opened != 3 {
		t.Errorf("opened = %d, want 3", opened)
	}
	if closed != 3 {
		t.Errorf("closed = %d, want 3", closed)
	}
}

func TestTrackedTransport_ErrorResponse(t *testing.T) {
	// When request fails, no body is returned, so counts stay at 0
	tracked := testutil.NewTrackedTransport(nil)
	defer tracked.AssertAllClosed(t)

	client := &http.Client{Transport: tracked}

	_, err := client.Get("http://invalid.test.local.invalid")
	if err == nil {
		t.Fatal("expected request to fail")
	}

	opened, closed := tracked.Stats()
	if opened != 0 {
		t.Errorf("opened = %d, want 0 (request failed)", opened)
	}
	if closed != 0 {
		t.Errorf("closed = %d, want 0 (request failed)", closed)
	}
}

func TestTrackedTransport_WithCustomTransport(t *testing.T) {
	// Test that we can wrap a custom transport
	customTransport := &http.Transport{}
	tracked := testutil.NewTrackedTransport(customTransport)

	if tracked.Transport != customTransport {
		t.Error("TrackedTransport should wrap the provided transport")
	}
}

func TestTrackedTransport_CloseMultipleTimes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tracked := testutil.NewTrackedTransport(nil)
	defer tracked.AssertAllClosed(t)

	client := &http.Client{Transport: tracked}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// Close multiple times - should only count once
	resp.Body.Close()
	resp.Body.Close()
	resp.Body.Close()

	opened, closed := tracked.Stats()
	if opened != 1 {
		t.Errorf("opened = %d, want 1", opened)
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1 (should count only once)", closed)
	}
}

func TestTrackedTransport_SimulateResolveChannelBug(t *testing.T) {
	// This simulates the bug we had in ResolveChannel where resp variable
	// was reused without properly closing the first response
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":false}`))
	}))
	defer server.Close()

	tracked := testutil.NewTrackedTransport(nil)
	client := &http.Client{Transport: tracked}

	// Simulate the buggy code pattern
	var resp *http.Response
	var err error

	// First request
	req1, _ := http.NewRequest("GET", server.URL, nil)
	resp, err = client.Do(req1)
	if err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}
	// BUG: Using closure-based defer that captures resp by reference
	defer func() { resp.Body.Close() }()

	// Second request - reuses resp variable
	req2, _ := http.NewRequest("GET", server.URL, nil)
	resp, err = client.Do(req2)
	if err != nil {
		t.Fatalf("request 2 failed: %v", err)
	}
	// BUG: This also captures resp by reference, which now points to response 2
	defer func() { resp.Body.Close() }()

	// At defer time, both closures close response 2, response 1 leaks
	opened, closed := tracked.Stats()
	if opened != 2 {
		t.Errorf("opened = %d, want 2", opened)
	}

	// Before fix: closed would be 1 (leak!)
	// After fix in ResolveChannel: each body captured separately, closed = 2
	if closed != 0 {
		t.Errorf("closed = %d, want 0 (bug still active before defer runs)", closed)
	}

	// Now let defers run (implicit at end of function)
	// The bug would show: only response 2 gets closed twice, response 1 leaks
}

func TestTrackedTransport_PropagatesReadErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test"))
	}))
	defer server.Close()

	tracked := testutil.NewTrackedTransport(nil)
	defer tracked.AssertAllClosed(t)

	client := &http.Client{Transport: tracked}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Read should work normally through the wrapper
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(body) != "test" {
		t.Errorf("body = %q, want %q", body, "test")
	}
}

func TestTrackedTransport_WorksWithRoundTripperFunc(t *testing.T) {
	// Test that we can wrap a custom RoundTripperFunc
	mockTransport := testutil.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("mock")),
			Header:     make(http.Header),
		}, nil
	})

	tracked := testutil.NewTrackedTransport(mockTransport)
	defer tracked.AssertAllClosed(t)

	client := &http.Client{Transport: tracked}
	resp, err := client.Get("http://example.com")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "mock" {
		t.Errorf("body = %q, want %q", body, "mock")
	}
}
