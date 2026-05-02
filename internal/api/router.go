package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"1claw-server/internal/agent"
	"1claw-server/internal/config"
	"1claw-server/internal/model"
	"1claw-server/internal/ws"

	"github.com/gorilla/mux"
)

// Server handles HTTP and WebSocket requests.
type Server struct {
	Router     *mux.Router
	Hub        *ws.Hub
	Bridge     agent.Provider
	Config     *model.ServerConfig
	HermesHome string
}

// NewServer creates a new API server.
func NewServer(hub *ws.Hub, bridge agent.Provider, cfg *model.ServerConfig, hermesHome string) *Server {
	s := &Server{
		Router:     mux.NewRouter(),
		Hub:        hub,
		Bridge:     bridge,
		Config:     cfg,
		HermesHome: hermesHome,
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	s.Router.Use(corsMiddleware)
	s.Router.Use(loggingMiddleware)

	api := s.Router.PathPrefix(s.Config.Server.APIPath).Subrouter()
	api.HandleFunc("/status", s.handleStatus).Methods("GET")
	api.HandleFunc("/profiles", s.handleListProfiles).Methods("GET")
	api.HandleFunc("/profiles", s.handleCreateProfile).Methods("POST")
	api.HandleFunc("/profiles/{id}", s.handleUpdateProfile).Methods("PUT")
	api.HandleFunc("/profiles/{id}", s.handleDeleteProfile).Methods("DELETE")
	api.HandleFunc("/profiles/reload", s.handleReloadProfiles).Methods("POST")

	// Notify all connected clients
	api.HandleFunc("/notify", s.handleNotify).Methods("POST")

	// Export all data as a zip archive (password-protected)
	api.HandleFunc("/export", s.handleExport).Methods("POST")

	// Health check
	s.Router.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}).Methods("GET")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	profiles := s.Bridge.GetProfiles()
	// Ensure online status is set
	for i := range profiles {
		status := s.Bridge.GetStatus(profiles[i].ID)
		profiles[i].Online = status.Online
	}

	resp := model.WSResponse{
		Type:     "status",
		Profiles: profiles,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	profiles := s.Bridge.GetProfiles()
	for i := range profiles {
		status := s.Bridge.GetStatus(profiles[i].ID)
		profiles[i].Online = status.Online
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"profiles": profiles,
		"count":    len(profiles),
	})
}

// handleReloadProfiles re-scans the Hermes profiles directory, adds newly
// discovered profiles, stops and removes deleted ones, then broadcasts the
// updated list to all connected WebSocket clients.
func (s *Server) handleReloadProfiles(w http.ResponseWriter, r *http.Request) {
	// Re-scan profiles directory
	newProfiles, err := config.DiscoverProfiles(s.HermesHome)
	if err != nil {
		log.Printf("[reload] scan failed: %v", err)
		http.Error(w, `{"error":"scan failed"}`, http.StatusInternalServerError)
		return
	}

	// Build lookup maps
	currentProfiles := s.Bridge.GetProfiles()
	currentIDs := make(map[string]bool, len(currentProfiles))
	for _, p := range currentProfiles {
		currentIDs[p.ID] = true
	}
	newIDs := make(map[string]bool, len(newProfiles))
	for _, p := range newProfiles {
		newIDs[p.ID] = true
	}

	// Stop removed profiles
	removed := make([]string, 0)
	for _, p := range currentProfiles {
		if !newIDs[p.ID] {
			if err := s.Bridge.Stop(p.ID); err != nil {
				log.Printf("[reload] stop error for %s: %v", p.ID, err)
			}
			removed = append(removed, p.ID)
			log.Printf("[reload] removed profile: %s", p.ID)
		}
	}

	// Start newly discovered profiles
	added := make([]string, 0)
	ctx := context.Background()
	for i := range newProfiles {
		if !currentIDs[newProfiles[i].ID] {
			if err := s.Bridge.Start(ctx, &newProfiles[i]); err != nil {
				log.Printf("[reload] start error for %s: %v", newProfiles[i].ID, err)
			}
			added = append(added, newProfiles[i].ID)
			log.Printf("[reload] added profile: %s", newProfiles[i].ID)
		}
	}

	// Notify all WebSocket clients
	profiles := s.Bridge.GetProfiles()
	for i := range profiles {
		st := s.Bridge.GetStatus(profiles[i].ID)
		profiles[i].Online = st.Online
	}
	s.Hub.NotifyProfileUpdate(profiles)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "reloaded",
		"profiles": profiles,
		"count":    len(profiles),
		"added":    added,
		"removed":  removed,
	})

	log.Printf("[reload] complete: %d added, %d removed, %d total", len(added), len(removed), len(profiles))
}

func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var profile model.Profile
	if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if profile.ID == "" {
		http.Error(w, `{"error":"profile id is required"}`, http.StatusBadRequest)
		return
	}

	s.Bridge.LoadProfiles([]model.Profile{profile})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(profile)

	// Notify all clients of the new profile
	s.Hub.NotifyProfileUpdate(s.Bridge.GetProfiles())
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	var updates model.Profile
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	profiles := s.Bridge.GetProfiles()
	found := false
	for i, p := range profiles {
		if p.ID == id {
			// Apply updates
			if updates.Name != "" {
				profiles[i].Name = updates.Name
			}
			if updates.Emoji != "" {
				profiles[i].Emoji = updates.Emoji
			}
			if updates.Description != "" {
				profiles[i].Description = updates.Description
			}
			if updates.Color != "" {
				profiles[i].Color = updates.Color
			}
			found = true
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(profiles[i])
			break
		}
	}

	if !found {
		http.Error(w, `{"error":"profile not found"}`, http.StatusNotFound)
	}
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	if err := s.Bridge.Stop(id); err != nil {
		http.Error(w, `{"error":"failed to stop profile"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": id})
}

// NotifyRequest is the request body for sending a notification to all users.
type NotifyRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// handleNotify sends a broadcast notification to all connected WebSocket clients.
func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	var req NotifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, `{"error":"content is required"}`, http.StatusBadRequest)
		return
	}

	resp := model.WSResponse{
		Type:      "notification",
		Title:     req.Title,
		Content:   req.Content,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.Hub.BroadcastJSON(resp); err != nil {
		log.Printf("[notify] broadcast error: %v", err)
		http.Error(w, `{"error":"broadcast failed"}`, http.StatusInternalServerError)
		return
	}

	clientCount := s.Hub.ClientCount()
	log.Printf("[notify] broadcast notification to %d clients: %s", clientCount, truncate(req.Content, 80))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":       "sent",
		"clients":      clientCount,
		"title":        req.Title,
		"content":      req.Content,
		"timestamp":    resp.Timestamp,
	})
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// corsMiddleware adds CORS headers.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware logs incoming requests.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[api] %s %s %s", r.Method, r.URL.Path, r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}
