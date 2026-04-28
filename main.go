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
	"1claw-server/internal/ws"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	hermesHome := flag.String("hermes-home", "", "Path to Hermes home (~/.hermes)")
	useMock := flag.Bool("mock", false, "Use MockBridge instead of real Hermes")
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
	cfg.Profiles = userProfiles

	// Initialize WebSocket hub
	hub := ws.NewHub()
	go hub.Run()

	// Initialize agent bridge
	var bridge agent.Provider
	var pythonPath string

	if *useMock || !hermesAvailable() {
		if *useMock {
			log.Println("[bridge] Using MockBridge (--mock flag)")
		} else {
			log.Println("[bridge] Hermes not available, falling back to MockBridge")
		}
		mb := agent.NewMockBridge()
		mb.LoadProfiles(cfg.Profiles)
		bridge = mb
	} else {
		log.Println("[bridge] Starting Hermes Python bridge ...")
		// Find the Hermes venv python
		pythonPath = filepath.Join(hh, "hermes-agent", "venv", "bin", "python3")
		if _, err := os.Stat(pythonPath); os.IsNotExist(err) {
			log.Printf("Warning: Hermes python not found at %s, falling back to MockBridge", pythonPath)
			mb := agent.NewMockBridge()
			mb.LoadProfiles(cfg.Profiles)
			bridge = mb
		} else {
			scriptPath := filepath.Join(findServerDir(), "scripts", "hermes_bridge.py")
			hb := agent.NewHermesBridge()
			hb.LoadProfiles(cfg.Profiles)
			subCtx := context.Background()
			if err := hb.StartSubprocess(subCtx, pythonPath, scriptPath); err != nil {
				log.Printf("Warning: failed to start Hermes bridge: %v, falling back to MockBridge", err)
				mb := agent.NewMockBridge()
				mb.LoadProfiles(cfg.Profiles)
				bridge = mb
			} else {
				// Send profiles to the bridge now that stdin is ready
				if err := hb.SendInit(); err != nil {
					log.Printf("Warning: failed to init Hermes bridge: %v", err)
				}
				bridge = hb
			}
		}
	}

	// Start all agent profiles
	ctx := context.Background()
	if err := bridge.StartAll(ctx); err != nil {
		log.Fatalf("Failed to start agents: %v", err)
	}
	log.Printf("Loaded %d agent profiles", len(cfg.Profiles))

	// Create API server + WS handler
	apiServer := api.NewServer(hub, bridge, cfg)
	wsHandler := api.NewWSHandler(hub, bridge, cfg)
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
		log.Printf("  Bridge:    %s", bridgeType(bridge))

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

	// Close Hermes bridge subprocess
	if hb, ok := bridge.(*agent.HermesBridge); ok {
		if err := hb.Close(); err != nil {
			log.Printf("Error closing bridge: %v", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server stopped gracefully")
}

func hermesAvailable() bool {
	_, err := exec.LookPath("hermes")
	return err == nil
}

func findServerDir() string {
	// Try to find the server dir relative to the binary
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		// Check if scripts/ exists next to the binary
		if _, err := os.Stat(filepath.Join(dir, "scripts")); err == nil {
			return dir
		}
		// Check parent
		parent := filepath.Dir(dir)
		if _, err := os.Stat(filepath.Join(parent, "scripts")); err == nil {
			return parent
		}
	}
	// Fallback
	return "/home/j/Codes/1claw/1claw-server"
}

func bridgeType(b agent.Provider) string {
	switch b.(type) {
	case *agent.HermesBridge:
		return "Hermes AI (real) — subprocess running"
	default:
		return "Mock (simulated responses)"
	}
}
