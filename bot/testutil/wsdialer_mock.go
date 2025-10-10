package testutil

// wsConn matches the bot.wsConn interface for testing
type wsConn interface {
	ReadJSON(v interface{}) error
	WriteJSON(v interface{}) error
	Close() error
}

// MockWSDialer is a mock WebSocket dialer for testing
// It allows injecting a pre-configured wsConn or simulating dial errors
type MockWSDialer struct {
	// Conn is the connection to return on successful dial
	Conn wsConn
	// Err is the error to return from Dial
	Err error
	// DialedURL captures the URL that was dialed for assertions
	DialedURL string
}

// Dial implements the wsDialer interface for testing
func (m *MockWSDialer) Dial(urlStr string, requestHeader map[string][]string) (wsConn, error) {
	m.DialedURL = urlStr
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Conn, nil
}
