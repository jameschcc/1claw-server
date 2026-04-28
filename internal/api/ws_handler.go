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

	if hb, ok := bridge.(*agent.HermesBridge); ok {
		hb.OnChatResponse = func(profileID, content, msgID string) {
			if content == "__agent_ready__" {
				profiles := updateProfileStatus(h.Bridge)
				h.Hub.NotifyProfileUpdate(profiles)
				return
			}
			if content == "__agent_starting__" {
				profiles := h.Bridge.GetProfiles()
				for i := range profiles {
					st := h.Bridge.GetStatus(profiles[i].ID)
					profiles[i].Online = st.Online
					if profiles[i].ID == profileID {
						profiles[i].Status = "starting"
					}
				}
				h.Hub.NotifyProfileUpdate(profiles)
				return
			}
			if strings.HasPrefix(content, "__reasoning__:") {
				reasoningText := strings.TrimPrefix(content, "__reasoning__:")
				resp := model.WSResponse{
					Type:      "reasoning",
					ProfileID: profileID,
					Content:   reasoningText,
					ID:        msgID,
				}
				h.Hub.BroadcastJSON(resp)
				return
			}

			// Normal chat response
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

func (h *WSHandler) handleClientMessage(c *ws.Client, msg model.WSMessage) {
	switch msg.Type {
	case "start_profile":
		pid := msg.ProfileID
		if pid == "" {
			c.SendJSON(model.WSResponse{Type: "error", Code: "missing_profile", Message: "profile_id required"})
			return
		}
		if hb, ok := h.Bridge.(*agent.HermesBridge); ok {
			hb.SendRaw(map[string]interface{}{
				"type":       "start_profile",
				"profile_id": pid,
			})
		}
		// Broadcast status update so all clients see the change
		profiles := h.Bridge.GetProfiles()
		for i := range profiles {
			st := h.Bridge.GetStatus(profiles[i].ID)
			profiles[i].Online = st.Online
		}
		h.Hub.NotifyProfileUpdate(profiles)

	default:
		c.SendJSON(model.WSResponse{Type: "error", Code: "unknown_type", Message: "Unknown: " + msg.Type})
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

	client.OnChat = func(c *ws.Client, msg model.WSMessage) {
		if msg.ProfileID == "" {
			c.SendJSON(model.WSResponse{Type: "error", Code: "missing_profile", Message: "profile_id required"})
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		response, err := h.Bridge.SendMessage(ctx, msg.ProfileID, msg.Content)
		if err != nil {
			errMsg := err.Error()
			if strings.HasPrefix(errMsg, "async:") {
				log.Printf("[ws] async send to %s (msg %s)", msg.ProfileID, msg.ID)
				return
			}
			c.SendJSON(model.WSResponse{Type: "error", Code: "agent_error", Message: errMsg})
			return
		}

		c.SendJSON(model.WSResponse{
			Type:      "chat",
			ProfileID: msg.ProfileID,
			Content:   response,
			ID:        msg.ID,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	}

	client.OnMessage = h.handleClientMessage

	h.Hub.RegisterClient(client)

	go client.WritePump()
	go client.ReadPump()

	log.Printf("[ws] new connection: %s", clientID)

	profiles := h.Bridge.GetProfiles()
	for i := range profiles {
		st := h.Bridge.GetStatus(profiles[i].ID)
		profiles[i].Online = st.Online
	}
	client.SendJSON(model.WSResponse{
		Type:     "status",
		Profiles: profiles,
	})
}

func updateProfileStatus(b agent.Provider) []model.Profile {
	profiles := b.GetProfiles()
	for i := range profiles {
		st := b.GetStatus(profiles[i].ID)
		profiles[i].Online = st.Online
	}
	return profiles
}
