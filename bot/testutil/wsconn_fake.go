package testutil

import (
	"encoding/json"
	"fmt"
	"sync"
)

// MockWsConn is a fake WebSocket connection for testing
// It uses channels to script reads and record writes
type MockWsConn struct {
	mu           sync.Mutex
	readQueue    []interface{}
	writeRecords []interface{}
	readErr      error
	writeErr     error
	closed       bool
}

// NewMockWsConn creates a new mock WebSocket connection
func NewMockWsConn() *MockWsConn {
	return &MockWsConn{
		readQueue:    make([]interface{}, 0),
		writeRecords: make([]interface{}, 0),
	}
}

// QueueRead adds a message to be returned by the next ReadJSON call
func (m *MockWsConn) QueueRead(v interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readQueue = append(m.readQueue, v)
}

// SetReadError configures the error to return on next ReadJSON
func (m *MockWsConn) SetReadError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readErr = err
}

// SetWriteError configures the error to return on WriteJSON
func (m *MockWsConn) SetWriteError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeErr = err
}

// ReadJSON implements wsConn interface
func (m *MockWsConn) ReadJSON(v interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.readErr != nil {
		return m.readErr
	}

	if len(m.readQueue) == 0 {
		return fmt.Errorf("no more queued reads")
	}

	// Pop first item
	item := m.readQueue[0]
	m.readQueue = m.readQueue[1:]

	// Marshal and unmarshal to simulate JSON behavior
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// WriteJSON implements wsConn interface
func (m *MockWsConn) WriteJSON(v interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.writeErr != nil {
		return m.writeErr
	}

	// Record the write
	m.writeRecords = append(m.writeRecords, v)
	return nil
}

// Close implements wsConn interface
func (m *MockWsConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// GetWrites returns all recorded writes for assertions
func (m *MockWsConn) GetWrites() []interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]interface{}{}, m.writeRecords...)
}

// IsClosed returns whether Close was called
func (m *MockWsConn) IsClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}
