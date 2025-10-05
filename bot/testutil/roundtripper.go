package testutil

import "net/http"

// RoundTripperFunc is a function type that implements http.RoundTripper
// This allows inline stubbing of HTTP requests in tests
type RoundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements the http.RoundTripper interface
func (f RoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
