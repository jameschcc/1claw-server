package api

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"1claw-server/internal/agent"
	"1claw-server/internal/model"
	"1claw-server/internal/store"
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
	Store  *store.ChatStore
}

// NewWSHandler creates a new WebSocket handler.
func NewWSHandler(hub *ws.Hub, bridge agent.Provider, cfg *model.ServerConfig, chatStore *store.ChatStore) *WSHandler {
	h := &WSHandler{
		Hub:    hub,
		Bridge: bridge,
		Config: cfg,
		Store:  chatStore,
	}

	if hb, ok := bridge.(*agent.HermesBridge); ok {
		hb.OnChatResponse = func(profileID, content, msgID, sessionID string) {
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
					SessionID: sessionID,
				}
				h.Hub.BroadcastJSON(resp)
				return
			}
			if strings.HasPrefix(content, "__chunk__:") {
				chunkContent := strings.TrimPrefix(content, "__chunk__:")
				resp := model.WSResponse{
					Type:      "chat_chunk",
					ProfileID: profileID,
					Content:   chunkContent,
					ID:        msgID,
					SessionID: sessionID,
					Timestamp: time.Now().UTC().Format(time.RFC3339),
				}
				h.Hub.BroadcastJSON(resp)
				return
			}

			if strings.HasPrefix(content, "__cancelled__:") {
				cancelMessage := strings.TrimPrefix(content, "__cancelled__:")
				resp := model.WSResponse{
					Type:      "cancelled",
					ProfileID: profileID,
					ID:        msgID,
					SessionID: sessionID,
					Message:   cancelMessage,
				}
				h.Hub.BroadcastJSON(resp)
				return
			}

			// Normal chat response — broadcast + persist
			resp := model.WSResponse{
				Type:      "chat",
				ProfileID: profileID,
				Content:   content,
				ID:        msgID,
				SessionID: sessionID,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			}
			h.Hub.BroadcastJSON(resp)

			// Persist agent response to all active conversations
			h.persistChatToAll(profileID, "agent", content, msgID)
		}

		hb.OnChatDispatched = func(profileID, msgID, sessionID string) {
			resp := model.WSResponse{
				Type:      "thinking",
				ProfileID: profileID,
				ID:        msgID,
				SessionID: sessionID,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			}
			h.Hub.BroadcastJSON(resp)
		}

		log.Println("[ws] Hermes bridge async response routing enabled")
	}

	return h
}

// persistChatToAll stores a broadcast message in the global profile_messages table.
// This ensures cross-device history: any device can retrieve all messages for
// a profile regardless of which conversation/device they came from.
func (h *WSHandler) persistChatToAll(profileID, role, content, msgID string) {
	if h.Store == nil {
		return
	}
	if msgID == "" {
		msgID = "glob_" + time.Now().Format("150405.000000")
	}
	if err := h.Store.SaveProfileMessage(profileID, role, content, msgID); err != nil {
		log.Printf("[store] save profile message error: %v", err)
	}
}

// handleClientMessage handles non-chat client messages.
func (h *WSHandler) handleClientMessage(c *ws.Client, msg model.WSMessage) {
	switch msg.Type {
	case "start_profile":
		pid := msg.ProfileID
		if pid == "" {
			c.SendJSON(model.WSResponse{Type: "error", Code: "missing_profile", Message: "profile_id required"})
			return
		}
		if hb, ok := h.Bridge.(*agent.HermesBridge); ok {
			hb.SendRaw(pid, map[string]interface{}{
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

	case "cancel_chat":
		if msg.ProfileID == "" {
			c.SendJSON(model.WSResponse{Type: "error", Code: "missing_profile", Message: "profile_id required"})
			return
		}
		if err := h.Bridge.CancelMessage(msg.ProfileID, msg.SessionID); err != nil {
			c.SendJSON(model.WSResponse{Type: "error", Code: "cancel_failed", Message: err.Error(), SessionID: msg.SessionID})
			return
		}

	case "get_status":
		// Re-read profiles from bridge and send status to the requesting client
		profiles := updateProfileStatus(h.Bridge)
		c.SendJSON(model.WSResponse{
			Type:     "status",
			Profiles: profiles,
		})

	case "get_profile_history":
		pid := msg.ProfileID
		if pid == "" {
			c.SendJSON(model.WSResponse{Type: "error", Code: "missing_profile", Message: "profile_id required"})
			return
		}
		if h.Store == nil {
			c.SendJSON(model.WSResponse{Type: "error", Code: "no_store", Message: "History not available"})
			return
		}
		messages, err := h.Store.GetProfileMessages(pid, 200)
		if err != nil {
			log.Printf("[store] profile history error: %v", err)
			c.SendJSON(model.WSResponse{Type: "error", Code: "history_error", Message: "Failed to load history"})
			return
		}
		c.SendJSON(model.WSResponse{
			Type:      "profile_history",
			ProfileID: pid,
			Messages:  messages,
		})

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

	// Extract client_id from WebSocket query parameter
	clientID := r.URL.Query().Get("client_id")
	if clientID == "" {
		// Fall back to remote addr if no client_id provided
		clientID = r.RemoteAddr + "-" + time.Now().Format("150405.000")
	}

	client := ws.NewClient(clientID, h.Hub, conn)

	// Resolve or create conversation
	var convID string
	if h.Store != nil {
		cid, err := h.Store.GetOrCreateConversation(clientID)
		if err != nil {
			log.Printf("[ws] conversation error: %v", err)
		} else {
			convID = cid
			client.ConversationID = convID
		}
	}

	// Send conversation ID to client
	client.SendJSON(model.WSResponse{
		Type:           "conversation",
		ConversationID: convID,
	})

	// Send auto-history on connect (conversation-scoped)
	if h.Store != nil && convID != "" {
		messages, err := h.Store.GetRecentMessages(convID, 100)
		if err != nil {
			log.Printf("[ws] history error: %v", err)
		} else if len(messages) > 0 {
			client.SendJSON(model.WSResponse{
				Type:           "history",
				ConversationID: convID,
				Messages:       messages,
			})
		}
	}

	// Send profile-scoped history for all profiles (cross-device)
	if h.Store != nil {
		profiles := h.Bridge.GetProfiles()
		for _, p := range profiles {
			msgs, err := h.Store.GetProfileMessages(p.ID, 200)
			if err != nil {
				log.Printf("[ws] profile history error for %s: %v", p.ID, err)
				continue
			}
			if len(msgs) == 0 {
				continue
			}
			client.SendJSON(model.WSResponse{
				Type:      "profile_history",
				ProfileID: p.ID,
				Messages:  msgs,
			})
		}
	}

	// Wire up chat handler — stores user message, forwards to agent, persists response
	client.OnChat = func(c *ws.Client, msg model.WSMessage) {
		if msg.ProfileID == "" {
			c.SendJSON(model.WSResponse{Type: "error", Code: "missing_profile", Message: "profile_id required"})
			return
		}

		// Persist user message to conversation + profile messages (cross-device)
		msgID := msg.ID
		if msgID == "" {
			msgID = "msg_" + time.Now().Format("150405.000000")
		}
		if h.Store != nil && convID != "" {
			if err := h.Store.SaveMessage(convID, msg.ProfileID, "user", msg.Content, msgID); err != nil {
				log.Printf("[store] save user message: %v", err)
			}
		}
		// Also save to profile-scoped messages for cross-device history
		// (independent of convID — works for all devices)
		if h.Store != nil {
			if err := h.Store.SaveProfileMessage(msg.ProfileID, "user", msg.Content, msgID); err != nil {
				log.Printf("[store] save user profile message: %v", err)
			}
		}

		// Broadcast user message to ALL connected clients so multi-device
		// users see each other's messages in real-time.
		h.Hub.BroadcastJSON(model.WSResponse{
			Type:      "user_message",
			ProfileID: msg.ProfileID,
			Content:   msg.Content,
			ID:        msgID,
			SessionID: msg.SessionID,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})

		// Forward to agent
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		response, err := h.Bridge.SendMessage(ctx, model.ChatRequest{
			ProfileID: msg.ProfileID,
			Content:   msg.Content,
			ID:        msg.ID,
			SessionID: msg.SessionID,
			History:   msg.History,
		})
		if err != nil {
			errMsg := err.Error()
			if strings.HasPrefix(errMsg, "async:") {
				log.Printf("[ws] async send to %s (msg %s)", msg.ProfileID, msgID)
				return
			}
			c.SendJSON(model.WSResponse{Type: "error", Code: "agent_error", Message: errMsg})
			return
		}

		// Sync response
		c.SendJSON(model.WSResponse{
			Type:      "chat",
			ProfileID: msg.ProfileID,
			Content:   response,
			ID:        msgID,
			SessionID: msg.SessionID,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})

		// Persist agent response
		if h.Store != nil && convID != "" {
			respID := "resp_" + msgID
			if err := h.Store.SaveMessage(convID, msg.ProfileID, "agent", response, respID); err != nil {
				log.Printf("[store] save agent message: %v", err)
			}
		}
		// Also save to profile-scoped messages (cross-device)
		if h.Store != nil {
			respID := "resp_" + msgID
			if err := h.Store.SaveProfileMessage(msg.ProfileID, "agent", response, respID); err != nil {
				log.Printf("[store] save agent profile message: %v", err)
			}
		}
	}

	// Wire up history request handler
	client.OnHistoryRequest = func(c *ws.Client) {
		if h.Store == nil || c.ConversationID == "" {
			c.SendJSON(model.WSResponse{Type: "error", Code: "no_history", Message: "History not available"})
			return
		}
		messages, err := h.Store.GetRecentMessages(c.ConversationID, 100)
		if err != nil {
			log.Printf("[store] history error: %v", err)
			c.SendJSON(model.WSResponse{Type: "error", Code: "history_error", Message: "Failed to load history"})
			return
		}
		c.SendJSON(model.WSResponse{
			Type:           "history",
			ConversationID: c.ConversationID,
			Messages:       messages,
		})
	}

	client.OnMessage = h.handleClientMessage

	h.Hub.RegisterClient(client)

	go client.WritePump()
	go client.ReadPump()

	log.Printf("[ws] new connection: %s (conversation: %s)", clientID, convID)

	// Send initial profile status
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
