package ws

// Message types for internal WS handler registration.
// The actual message models are in internal/model/types.go.

// MessageHandler defines a function that processes a WSMessage from a client.
type MessageHandler func(clientID string, msgType string, payload []byte)
