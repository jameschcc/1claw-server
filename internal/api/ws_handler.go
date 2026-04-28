package api

import (
	"context"
	"log"
	"net/http"
	"time"

	"1claw-server/internal/agent"
	"1claw-server/internal/model"
	"1claw-server/internal/ws"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for development
	},
}

// WSHandler handles WebSocket upgrade requests and wires them to the hub.
type WSHandler struct {
	Hub    *ws.Hub
	Bridge *agent.MockBridge
	Config *model.ServerConfig
}

// NewWSHandler creates a new WebSocket handler.
func NewWSHandler(hub *ws.Hub, bridge *agent.MockBridge, cfg *model.ServerConfig) *WSHandler {
	return &WSHandler{
		Hub:    hub,
		Bridge: bridge,
		Config: cfg,
	}
}

// ServeWS upgrades the HTTP connection to WebSocket and starts read/write pumps.
func (h *WSHandler) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade error: %v", err)
		return
	}

	clientID := r.RemoteAddr + "-" + time.Now().Format("150405.000")
	client := ws.NewClient(clientID, h.Hub, conn)

	// Wire up the chat handler
	client.OnChat = func(c *ws.Client, msg model.WSMessage) {
		if msg.ProfileID == "" {
			c.SendJSON(model.WSResponse{
				Type:    "error",
				Code:    "missing_profile",
				Message: "profile_id is required for chat messages",
			})
			return
		}

		// Forward message to agent bridge
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		response, err := h.Bridge.SendMessage(ctx, msg.ProfileID, msg.Content)
		if err != nil {
			c.SendJSON(model.WSResponse{
				Type:    "error",
				Code:    "agent_error",
				Message: err.Error(),
			})
			return
		}

		// Send response back to the requesting client
		c.SendJSON(model.WSResponse{
			Type:      "chat",
			ProfileID: msg.ProfileID,
			Content:   response,
			ID:        msg.ID,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	}

	h.Hub.RegisterClient(client)

	// Start read/write pumps
	go client.WritePump()
	go client.ReadPump()

	log.Printf("[ws] new connection: %s", clientID)

	// Send initial status to the new client
	profiles := h.Bridge.GetProfiles()
	for i := range profiles {
		status := h.Bridge.GetStatus(profiles[i].ID)
		profiles[i].Online = status.Online
	}
	client.SendJSON(model.WSResponse{
		Type:     "status",
		Profiles: profiles,
	})
}
