package api

import (
	"encoding/json"
	"log"
	"net/http"

	"1claw-server/internal/agent"
	"1claw-server/internal/model"
	"1claw-server/internal/ws"

	"github.com/gorilla/mux"
)

// Server handles HTTP and WebSocket requests.
type Server struct {
	Router  *mux.Router
	Hub     *ws.Hub
	Bridge  *agent.MockBridge
	Config  *model.ServerConfig
}

// NewServer creates a new API server.
func NewServer(hub *ws.Hub, bridge *agent.MockBridge, cfg *model.ServerConfig) *Server {
	s := &Server{
		Router: mux.NewRouter(),
		Hub:    hub,
		Bridge: bridge,
		Config: cfg,
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
