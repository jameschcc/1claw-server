package ws

import (
	"encoding/json"
	"log"
	"sync"

	"1claw-server/internal/model"
)

// Hub maintains the set of active WebSocket clients and broadcasts messages.
type Hub struct {
	// Registered clients.
	clients map[*Client]bool
	// Register requests.
	register chan *Client
	// Unregister requests.
	unregister chan *Client
	// Inbound messages from clients.
	broadcast chan []byte
	// Profile updates to broadcast.
	profileUpdates chan []model.Profile
	mu             sync.RWMutex
}

// NewHub creates a new Hub instance.
func NewHub() *Hub {
	return &Hub{
		clients:        make(map[*Client]bool),
		register:       make(chan *Client),
		unregister:     make(chan *Client),
		broadcast:      make(chan []byte, 256),
		profileUpdates: make(chan []model.Profile, 64),
	}
}

// Run starts the hub's event loop.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Printf("[ws] client connected: %s (total: %d)", client.ID, len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.Send)
			}
			h.mu.Unlock()
			log.Printf("[ws] client disconnected: %s (total: %d)", client.ID, len(h.clients))

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.Send <- message:
				default:
					// Client's send buffer is full, drop the message.
					h.mu.RUnlock()
					h.mu.Lock()
					delete(h.clients, client)
					close(client.Send)
					h.mu.Unlock()
					h.mu.RLock()
				}
			}
			h.mu.RUnlock()

		case profiles := <-h.profileUpdates:
			resp := model.WSResponse{
				Type:     "status",
				Profiles: profiles,
			}
			data, err := json.Marshal(resp)
			if err != nil {
				log.Printf("[ws] marshal error: %v", err)
				continue
			}
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.Send <- data:
				default:
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast sends raw bytes to all connected clients.
func (h *Hub) Broadcast(data []byte) {
	h.broadcast <- data
}

// BroadcastJSON sends a JSON-serializable response to all clients.
func (h *Hub) BroadcastJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	h.broadcast <- data
	return nil
}

// RegisterClient sends a client to the register channel.
func (h *Hub) RegisterClient(client *Client) {
	h.register <- client
}

// NotifyProfileUpdate broadcasts status updates to all clients.
func (h *Hub) NotifyProfileUpdate(profiles []model.Profile) {
	h.profileUpdates <- profiles
}

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
