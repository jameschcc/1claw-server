package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"1claw-server/internal/agent"
	"1claw-server/internal/api"
	"1claw-server/internal/config"
	"1claw-server/internal/ws"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

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
	log.Printf("Started %d agent profiles", len(cfg.Profiles))

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
