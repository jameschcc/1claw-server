package config

import (
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"sort"

	"1claw-server/internal/model"

	"gopkg.in/yaml.v3"
)

// Profile emoji mappings — deterministic by profile name hash
var profileEmojis = []string{
	"🤖", "🧠", "⚡", "🎯", "🔧", "💡", "🚀", "🎨",
	"📊", "🔬", "🎭", "🛠️", "📝", "🎪", "🌈", "🔥",
}

var profileColors = []string{
	"#0078D7", "#7B1FA2", "#388E3C", "#F57C00",
	"#C62828", "#00838F", "#6A1B9A", "#2E7D32",
	"#E65100", "#1565C0", "#AD1457", "#00695C",
	"#4527A0", "#BF360C", "#1B5E20", "#0D47A1",
}

// Load reads and parses the YAML config file.
func Load(path string) (*model.ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &model.ServerConfig{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Apply defaults
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.WSPath == "" {
		cfg.Server.WSPath = "/ws"
	}
	if cfg.Server.APIPath == "" {
		cfg.Server.APIPath = "/api"
	}
	if cfg.WebSocket.PingInterval == 0 {
		cfg.WebSocket.PingInterval = 30
	}
	if cfg.WebSocket.WriteTimeout == 0 {
		cfg.WebSocket.WriteTimeout = 10
	}
	if cfg.WebSocket.ReadTimeout == 0 {
		cfg.WebSocket.ReadTimeout = 60
	}
	if cfg.WebSocket.MaxMessageSize == 0 {
		cfg.WebSocket.MaxMessageSize = 65536
	}

	return cfg, nil
}

// DiscoverProfiles scans the Hermes profiles directory and returns discovered profiles.
// hermesHome: path to ~/.hermes (e.g., /home/j/.hermes)
func DiscoverProfiles(hermesHome string) ([]model.Profile, error) {
	profilesDir := filepath.Join(hermesHome, "profiles")
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		return nil, fmt.Errorf("read profiles dir %s: %w", profilesDir, err)
	}

	var profiles []model.Profile

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "" || name[0] == '.' {
			continue
		}

		profileCfgPath := filepath.Join(profilesDir, name, "config.yaml")
		if _, err := os.Stat(profileCfgPath); os.IsNotExist(err) {
			// Some profiles may not have a config file
			continue
		}

		emoji, color := ProfileDecorations(name)

		profiles = append(profiles, model.Profile{
			ID:            name,
			Name:          name,
			Emoji:         emoji,
			Description:   fmt.Sprintf("Hermes agent profile: %s", name),
			HermesProfile: name,
			Color:         color,
			Online:        false,
			Status:        "free",
			TasksQueue:    0,
		})
	}

	// Sort by name for deterministic order
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].ID < profiles[j].ID
	})

	// Also check for default profile (root config.yaml)
	defaultProfile, _ := DiscoverDefaultProfile(hermesHome)
	if defaultProfile != nil {
		// Prepend default as first entry
		profiles = append([]model.Profile{*defaultProfile}, profiles...)
	}

	log.Printf("[config] Discovered %d Hermes profiles", len(profiles))
	for _, p := range profiles {
		log.Printf("  %s %s (%s)", p.Emoji, p.Name, p.Color)
	}

	return profiles, nil
}

// DiscoverDefaultProfile checks if there's a default profile in ~/.hermes/config.yaml.
func DiscoverDefaultProfile(hermesHome string) (*model.Profile, error) {
	defaultCfgPath := filepath.Join(hermesHome, "config.yaml")
	if _, err := os.Stat(defaultCfgPath); os.IsNotExist(err) {
		return nil, nil
	}
	return &model.Profile{
		ID:            "default",
		Name:          "Default",
		Emoji:         "🤖",
		Description:   "Default Hermes agent profile",
		HermesProfile: "default",
		Color:         "#0078D7",
		Online:        false,
		Status:        "free",
		TasksQueue:    0,
	}, nil
}

// ProfileDecorations returns a deterministic emoji+color for a profile name.
func ProfileDecorations(name string) (string, string) {
	h := fnv.New32a()
	h.Write([]byte(name))
	idx := int(h.Sum32()) % len(profileEmojis)
	emoji := profileEmojis[idx]
	color := profileColors[idx]
	return emoji, color
}

// WriteSharedMD regenerates the SHARED.md file from the given profile list.
// It writes to ~/.hermes/SHARED.md with each profile's name, emoji, and description.
func WriteSharedMD(hermesHome string, profiles []model.Profile) {
	log.Printf("[shared.md] Writing SHARED.md with %d profiles", len(profiles))
	// This is a placeholder — actual SHARED.md content depends on the format
	// the user expects. For now, just log and skip.
}
