package ws

import (
	"encoding/json"
	"log"
	"time"

	"1claw-server/internal/model"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second
	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second
	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = 30 * time.Second
	// Maximum message size allowed from peer.
	maxMessageSize = 65536
	// Send channel buffer size.
	sendBufSize = 256
)

// Client represents a single WebSocket connection.
type Client struct {
	ID     string
	Hub    *Hub
	Conn   *websocket.Conn
	Send   chan []byte
	ActiveProfile string

	// Handler for incoming chat messages.
	OnChat func(client *Client, msg model.WSMessage)

	// Handler for other incoming messages (start_profile, etc.)
	OnMessage func(client *Client, msg model.WSMessage)
}

// NewClient creates a new Client.
func NewClient(id string, hub *Hub, conn *websocket.Conn) *Client {
	return &Client{
		ID:     id,
		Hub:    hub,
		Conn:   conn,
		Send:   make(chan []byte, sendBufSize),
		OnChat: defaultChatHandler,
	}
}

// ReadPump pumps messages from the WebSocket connection to the hub.
func (c *Client) ReadPump() {
	defer func() {
		c.Hub.unregister <- c
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[ws] read error: %v", err)
			}
			break
		}

		var msg model.WSMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("[ws] invalid message: %v", err)
			c.sendError("invalid_message", "Invalid message format")
			continue
		}

		c.handleMessage(msg)
	}
}

// WritePump pumps messages from the hub to the WebSocket connection.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel.
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("[ws] write error: %v", err)
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) handleMessage(msg model.WSMessage) {
	msgType := msg.Type
	switch msgType {
	case "ping":
		c.sendPong()

	case "chat":
		if c.OnChat != nil {
			c.OnChat(c, msg)
		}

	case "switch_profile":
		c.ActiveProfile = msg.ProfileID
		log.Printf("[ws] client %s switched to profile %s", c.ID, msg.ProfileID)

	case "get_status":
		c.sendPong()

	default:
		// Forward to generic handler
		if c.OnMessage != nil {
			c.OnMessage(c, msg)
		} else {
			c.sendError("unknown_type", "Unknown message type: "+msgType)
		}
	}
}

func (c *Client) sendPong() {
	c.SendJSON(model.WSResponse{Type: "pong"})
}

func (c *Client) sendError(code, message string) {
	c.SendJSON(model.WSResponse{
		Type:    "error",
		Code:    code,
		Message: message,
	})
}

// SendJSON marshals and sends a JSON response to this client.
func (c *Client) SendJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("[ws] marshal error: %v", err)
		return
	}
	select {
	case c.Send <- data:
	default:
		log.Printf("[ws] client %s send buffer full, dropping message", c.ID)
	}
}

// defaultChatHandler logs and ignores chat messages.
func defaultChatHandler(client *Client, msg model.WSMessage) {
	log.Printf("[ws] chat from %s to %s: %s (truncated)",
		client.ID, msg.ProfileID, truncate(msg.Content, 50))
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
