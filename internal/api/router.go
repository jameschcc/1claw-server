package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	api.HandleFunc("/profiles/spawn", s.handleSpawnProfile).Methods("POST")
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
	var req model.CreateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
		return
	}

	// Sanitize name: lowercase, no spaces, no special chars
	safeName := strings.ToLower(strings.TrimSpace(req.Name))
	if safeName == "" {
		http.Error(w, `{"error":"name must not be empty"}`, http.StatusBadRequest)
		return
	}

	profilesDir := filepath.Join(s.HermesHome, "profiles")
	newDir := filepath.Join(profilesDir, safeName)

	// Check for existing
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		http.Error(w, fmt.Sprintf(`{"error":"profile '%s' already exists"}`, safeName), http.StatusConflict)
		return
	}

	// Determine source profile for inheritance
	sourceDir := ""
	if req.InheritFrom != "" {
		sourceDir = filepath.Join(profilesDir, req.InheritFrom)
		if _, err := os.Stat(filepath.Join(sourceDir, "config.yaml")); os.IsNotExist(err) {
			http.Error(w, fmt.Sprintf(`{"error":"source profile '%s' not found"}`, req.InheritFrom), http.StatusBadRequest)
			return
		}
	} else {
		// Use default profile
		defaultDir := filepath.Join(s.HermesHome, "profiles", "default")
		if _, err := os.Stat(filepath.Join(defaultDir, "config.yaml")); err == nil {
			sourceDir = defaultDir
		}
	}

	// Create directory
	if err := os.MkdirAll(newDir, 0755); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to create directory: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Copy files from source
	if sourceDir != "" {
		copyFile := func(name string) {
			src := filepath.Join(sourceDir, name)
			dst := filepath.Join(newDir, name)
			data, err := os.ReadFile(src)
			if err != nil {
				return
			}
			os.WriteFile(dst, data, 0644)
		}
		// Copy key files
		for _, f := range []string{"SOUL.md", "MEMORY.md", "USER.md", "config.yaml", ".env"} {
			copyFile(f)
		}
		// Copy skills directory
		srcSkills := filepath.Join(sourceDir, "skills")
		if dstInfo, err := os.Stat(srcSkills); err == nil && dstInfo.IsDir() {
			cpCmd := exec.Command("cp", "-r", srcSkills, filepath.Join(newDir, "skills"))
			cpCmd.Run()
		}
	}

	// Update SHARED.md to register the new profile
	s.updateSharedMD()

	// Create the profile object
	emoji, color := config.ProfileDecorations(safeName)
	profile := model.Profile{
		ID:            safeName,
		Name:          safeName,
		Emoji:         emoji,
		Description:   fmt.Sprintf("Hermes agent profile: %s", safeName),
		HermesProfile: safeName,
		Color:         color,
		Online:        false,
		Status:        "starting",
		CreatedAt:     time.Now(),
	}

	// Spawn the profile's Hermes process
	ctx := context.Background()
	if err := s.Bridge.Start(ctx, &profile); err != nil {
		log.Printf("[create] spawn error for %s: %v", safeName, err)
	}

	// Notify all clients
	profiles := s.Bridge.GetProfiles()
	for i := range profiles {
		st := s.Bridge.GetStatus(profiles[i].ID)
		profiles[i].Online = st.Online
		if profiles[i].ID == safeName {
			profiles[i] = profile
			profiles[i].Online = true
		}
	}
	s.Hub.NotifyProfileUpdate(profiles)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(profile)

	log.Printf("[create] profile '%s' created from '%s'", safeName, req.InheritFrom)
}

// handleSpawnProfile spawns a duplicate agent process for an existing profile
// without creating any new files on disk.
func (s *Server) handleSpawnProfile(w http.ResponseWriter, r *http.Request) {
	var req model.SpawnProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.ProfileID == "" {
		http.Error(w, `{"error":"profile_id is required"}`, http.StatusBadRequest)
		return
	}

	// Only HermesBridge supports spawning
	hb, ok := s.Bridge.(*agent.HermesBridge)
	if !ok {
		http.Error(w, `{"error":"spawn not supported with current bridge"}`, http.StatusBadRequest)
		return
	}

	// Generate the next spawn ID
	spawnID := hb.GetSpawnNextID(req.ProfileID)

	// Spawn a new process using the same hermest profile name
	if err := hb.SpawnProfile(spawnID, req.ProfileID); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"spawn failed: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Notify all clients
	profiles := s.Bridge.GetProfiles()
	for i := range profiles {
		st := s.Bridge.GetStatus(profiles[i].ID)
		profiles[i].Online = st.Online
	}
	s.Hub.NotifyProfileUpdate(profiles)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "spawned",
		"profile_id": spawnID,
		"name":       spawnID,
		"hermes_profile": req.ProfileID,
		"is_spawn":   true,
	})

	log.Printf("[spawn] spawned copy '%s' from profile '%s'", spawnID, req.ProfileID)
}

// updateSharedMD re-generates the SHARED.md file from discovered profiles.
func (s *Server) updateSharedMD() {
	profiles, err := config.DiscoverProfiles(s.HermesHome)
	if err != nil {
		log.Printf("[shared.md] discover error: %v", err)
		return
	}
	config.WriteSharedMD(s.HermesHome, profiles)
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
