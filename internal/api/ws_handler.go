package api

import (
	"context"
	"log"
	"net/http"
	"strings"
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
		return true
	},
}

// WSHandler handles WebSocket upgrade requests and wires them to the hub.
type WSHandler struct {
	Hub    *ws.Hub
	Bridge agent.Provider
	Config *model.ServerConfig
}

// NewWSHandler creates a new WebSocket handler.
func NewWSHandler(hub *ws.Hub, bridge agent.Provider, cfg *model.ServerConfig) *WSHandler {
	h := &WSHandler{
		Hub:    hub,
		Bridge: bridge,
		Config: cfg,
	}

	// If bridge supports async responses, wire up the callback
	if hb, ok := bridge.(*agent.HermesBridge); ok {
		hb.OnChatResponse = func(profileID, content, msgID string) {
			// Route to all connected clients
			resp := model.WSResponse{
				Type:      "chat",
				ProfileID: profileID,
				Content:   content,
				ID:        msgID,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			}
			h.Hub.BroadcastJSON(resp)
		}
		log.Println("[ws] Hermes bridge async response routing enabled")
	}

	return h
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
			// Async bridge: response comes later via OnChatResponse callback.
			// Don't send error — just log it.
			errMsg := err.Error()
			if strings.HasPrefix(errMsg, "async:") {
				log.Printf("[ws] async send to %s (msg %s)", msg.ProfileID, msg.ID)
				return
			}
			// Real error
			c.SendJSON(model.WSResponse{
				Type:    "error",
				Code:    "agent_error",
				Message: errMsg,
			})
			return
		}

		// Sync bridge (MockBridge): send response immediately
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
