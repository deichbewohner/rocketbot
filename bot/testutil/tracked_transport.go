package testutil

import (
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

// TrackedTransport wraps an http.RoundTripper and tracks response body lifecycle.
// Use this to detect resource leaks where response bodies aren't closed.
type TrackedTransport struct {
	Transport http.RoundTripper
	opened    atomic.Int32
	closed    atomic.Int32
}

// NewTrackedTransport creates a transport wrapper that tracks body closes.
// Typical usage:
//
//	tracked := testutil.NewTrackedTransport(http.DefaultTransport)
//	client := &http.Client{Transport: tracked}
//	defer tracked.AssertAllClosed(t)
func NewTrackedTransport(base http.RoundTripper) *TrackedTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &TrackedTransport{Transport: base}
}

// RoundTrip implements http.RoundTripper by wrapping response bodies.
func (t *TrackedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.Transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	t.opened.Add(1)

	// Wrap the body to track closes
	resp.Body = &trackedBody{
		ReadCloser: resp.Body,
		onClose: func() {
			t.closed.Add(1)
		},
	}

	return resp, nil
}

// AssertAllClosed fails the test if any response bodies weren't closed.
// Call this with defer after creating the tracked transport.
func (t *TrackedTransport) AssertAllClosed(tb testing.TB) {
	tb.Helper()
	opened := t.opened.Load()
	closed := t.closed.Load()
	if opened != closed {
		tb.Errorf("response body leak: opened %d, closed %d", opened, closed)
	}
}

// Stats returns the current open/close counts for inspection.
func (t *TrackedTransport) Stats() (opened, closed int32) {
	return t.opened.Load(), t.closed.Load()
}

// trackedBody wraps an io.ReadCloser to invoke a callback on first close.
type trackedBody struct {
	io.ReadCloser
	onClose func()
	once    sync.Once
}

// Close calls the tracking callback exactly once, then closes the underlying body.
func (b *trackedBody) Close() error {
	b.once.Do(b.onClose)
	return b.ReadCloser.Close()
}
