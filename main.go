package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"1claw-server/internal/agent"
	"1claw-server/internal/api"
	"1claw-server/internal/config"
	"1claw-server/internal/model"
	"1claw-server/internal/ws"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	hermesHome := flag.String("hermes-home", "", "Path to Hermes home (~/.hermes)")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Resolve Hermes home — find via hermes binary symlink
	hh := *hermesHome
	if hh == "" {
		if hermesPath, err := exec.LookPath("hermes"); err == nil {
			if target, err := filepath.EvalSymlinks(hermesPath); err == nil {
				// Walk up from binary until we find profiles/ dir
				dir := filepath.Dir(target)
				for i := 0; i < 5; i++ {
					if _, err := os.Stat(filepath.Join(dir, "profiles")); err == nil {
						hh = dir
						break
					}
					parent := filepath.Dir(dir)
					if parent == dir {
						break
					}
					dir = parent
				}
			}
		}
		if hh == "" || hh == "." {
			hh = filepath.Join(os.Getenv("HOME"), ".hermes")
		}
	}
	log.Printf("Hermes home: %s", hh)

	// Discover user's Hermes profiles
	userProfiles, err := config.DiscoverProfiles(hh)
	if err != nil {
		log.Printf("Warning: could not discover profiles: %v", err)
	}
	if len(userProfiles) == 0 {
		log.Println("Warning: no Hermes profiles found, trying default")
		defaultProf, _ := config.DiscoverDefaultProfile(hh)
		if defaultProf != nil {
			userProfiles = append(userProfiles, *defaultProf)
		}
	}

	// Merge: config.yaml profiles override auto-discovered ones
	profileMap := make(map[string]model.Profile)
	for _, p := range userProfiles {
		profileMap[p.ID] = p
	}
	for _, p := range cfg.Profiles {
		profileMap[p.ID] = p
	}

	// Convert back to slice
	profiles := make([]model.Profile, 0, len(profileMap))
	for _, p := range profileMap {
		profiles = append(profiles, p)
	}
	cfg.Profiles = profiles

	// Initialize WebSocket hub
	hub := ws.NewHub()
	go hub.Run()

	// Initialize agent bridge
	bridge := agent.NewMockBridge()
	bridge.LoadProfiles(cfg.Profiles)

	// Start all agent profiles
	ctx := context.Background()
	if err := bridge.StartAll(ctx); err != nil {
		log.Fatalf("Failed to start agents: %v", err)
	}
	log.Printf("Loaded %d agent profiles", len(cfg.Profiles))

	// Create API server
	apiServer := api.NewServer(hub, bridge, cfg)

	// Create WebSocket handler
	wsHandler := api.NewWSHandler(hub, bridge, cfg)

	// Register WebSocket route
	apiServer.Router.HandleFunc(cfg.Server.WSPath, wsHandler.ServeWS)

	// Build the HTTP server
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      apiServer.Router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in a goroutine
	go func() {
		log.Printf("1Claw Server starting on %s", addr)
		log.Printf("  WebSocket: ws://%s%s", addr, cfg.Server.WSPath)
		log.Printf("  API:       http://%s%s", addr, cfg.Server.APIPath)
		log.Printf("  Health:    http://%s/health", addr)

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// Stop all agents
	for _, p := range bridge.GetProfiles() {
		if err := bridge.Stop(p.ID); err != nil {
			log.Printf("Error stopping profile %s: %v", p.ID, err)
		}
	}

	// Shutdown HTTP server with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server stopped gracefully")
}
