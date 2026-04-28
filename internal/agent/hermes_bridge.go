package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"1claw-server/internal/model"
)

// agentProcess represents a single persistent Hermes agent subprocess.
type agentProcess struct {
	cmd       *exec.Cmd
	stdin     *json.Encoder
	stdout    *bufio.Scanner
	profileID string
	ready     bool
	mu        sync.Mutex
}

// HermesBridge manages one subprocess per profile.
// HermesBridge manages one subprocess per profile.
type HermesBridge struct {
	mu       sync.RWMutex
	agents   map[string]*agentProcess // profile_id → subprocess
	python   string                   // path to python3
	script   string                   // path to hermes_agent.py
	HermesHome string

	// Callback for async chat responses
	OnChatResponse func(profileID, content, msgID string)
}

// NewHermesBridge creates a new per-profile bridge.
func NewHermesBridge() *HermesBridge {
	return &HermesBridge{
		agents: make(map[string]*agentProcess),
	}
}

// Init sets paths and discovers python/hermes-home.
func (b *HermesBridge) Init() {
	b.python = filepath.Join(b.HermesHome, "hermes-agent", "venv", "bin", "python3")
	b.script = findScript("scripts/hermes_agent.py")
	if b.python == "" || !fileExists(b.python) {
		b.python = "python3"
	}
	log.Printf("[hermes] python=%s script=%s hermes-home=%s", b.python, b.script, b.HermesHome)
}

// SpawnProfile starts a subprocess for a single profile.
func (b *HermesBridge) SpawnProfile(pid string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.agents[pid]; exists {
		return nil // already running
	}

	ap := &agentProcess{profileID: pid}
	ctx := context.Background()
	ap.cmd = exec.CommandContext(ctx, b.python, b.script)

	// Set profile-specific environment
	ap.cmd.Env = os.Environ()
	ap.cmd.Env = append(ap.cmd.Env, "HERMES_PROFILE="+pid)
	ap.cmd.Env = append(ap.cmd.Env, "HERMES_HOME="+b.HermesHome)

	// Stdin pipe
	stdinPipe, err := ap.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	ap.stdin = json.NewEncoder(stdinPipe)

	// Stdout pipe
	stdoutPipe, err := ap.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	ap.stdout = bufio.NewScanner(stdoutPipe)
	ap.stdout.Buffer(make([]byte, 0, 256*1024), 256*1024)

	ap.cmd.Stderr = os.Stderr

	if err := ap.cmd.Start(); err != nil {
		return fmt.Errorf("start agent %s: %w", pid, err)
	}

	b.agents[pid] = ap
	log.Printf("[hermes] spawned agent %s (pid=%d)", pid, ap.cmd.Process.Pid)

	// Read responses in background
	go b.readAgentResponses(ap)

	return nil
}

// WaitReady blocks until the agent subprocess sends "ready".
func (b *HermesBridge) WaitReady(pid string, timeout time.Duration) error {
	b.mu.RLock()
	ap, ok := b.agents[pid]
	b.mu.RUnlock()
	if !ok {
		return fmt.Errorf("agent %s not found", pid)
	}

	deadline := time.After(timeout)
	for {
		ap.mu.Lock()
		ready := ap.ready
		ap.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-deadline:
			return fmt.Errorf("agent %s not ready after %v", pid, timeout)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// LoadProfiles stores profile list (no-op for per-process bridge).
func (b *HermesBridge) LoadProfiles(profiles []model.Profile) {}

// StartAll spawns one subprocess per profile.
func (b *HermesBridge) StartAll(ctx context.Context) error {
	b.mu.RLock()
	pids := make([]string, 0, len(b.agents))
	for pid := range b.agents {
		pids = append(pids, pid)
	}
	b.mu.RUnlock()

	for _, pid := range pids {
		if err := b.WaitReady(pid, 120*time.Second); err != nil {
			log.Printf("[hermes] %s", err)
		}
	}
	return nil
}

// Start implements Provider interface (marks as loaded).
func (b *HermesBridge) Start(ctx context.Context, profile *model.Profile) error {
	return b.SpawnProfile(profile.ID)
}

// Stop kills a profile's subprocess.
func (b *HermesBridge) Stop(profileID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	ap, ok := b.agents[profileID]
	if !ok {
		return nil
	}
	delete(b.agents, profileID)
	if ap.cmd != nil && ap.cmd.Process != nil {
		return ap.cmd.Process.Kill()
	}
	return nil
}

// SendMessage sends a chat to the specific profile's process and returns async.
func (b *HermesBridge) SendMessage(ctx context.Context, profileID, message string) (string, error) {
	b.mu.RLock()
	ap, ok := b.agents[profileID]
	b.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("agent %s not running", profileID)
	}

	msgID := fmt.Sprintf("msg_%d", time.Now().UnixMilli())
	err := ap.stdin.Encode(map[string]string{
		"type":    "chat",
		"content": message,
		"id":      msgID,
	})
	if err != nil {
		return "", fmt.Errorf("send chat: %w", err)
	}

	return "", fmt.Errorf("async: response via callback")
}

// GetStatus returns whether the profile's subprocess is alive.
func (b *HermesBridge) GetStatus(profileID string) model.AgentStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.agents[profileID]
	return model.AgentStatus{
		ProfileID: profileID,
		Online:    ok,
	}
}

// GetAllStatus returns status for all profiles.
func (b *HermesBridge) GetAllStatus() []model.AgentStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	statuses := make([]model.AgentStatus, 0, len(b.agents))
	for pid := range b.agents {
		statuses = append(statuses, model.AgentStatus{
			ProfileID: pid,
			Online:    true,
		})
	}
	return statuses
}

// GetProfiles returns all managed profile IDs as profiles.
func (b *HermesBridge) GetProfiles() []model.Profile {
	b.mu.RLock()
	defer b.mu.RUnlock()
	profiles := make([]model.Profile, 0, len(b.agents))
	for pid := range b.agents {
		profiles = append(profiles, model.Profile{
			ID:     pid,
			Name:   pid,
			Online: true,
		})
	}
	return profiles
}

// Close kills all subprocesses.
func (b *HermesBridge) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for pid, ap := range b.agents {
		if ap.cmd != nil && ap.cmd.Process != nil {
			ap.cmd.Process.Kill()
		}
		delete(b.agents, pid)
	}
	return nil
}

// SendRaw sends an arbitrary JSON command to a specific profile's process.
func (b *HermesBridge) SendRaw(profileID string, v map[string]interface{}) {
	b.mu.RLock()
	ap, ok := b.agents[profileID]
	b.mu.RUnlock()
	if !ok {
		return
	}
	if err := ap.stdin.Encode(v); err != nil {
		log.Printf("[hermes] send to %s: %v", profileID, err)
	}
}

// --- internal ---

func (b *HermesBridge) readAgentResponses(ap *agentProcess) {
	for ap.stdout.Scan() {
		line := ap.stdout.Text()
		var resp map[string]interface{}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			log.Printf("[hermes] %s parse error: %v", ap.profileID, err)
			continue
		}

		msgType, _ := resp["type"].(string)
		switch msgType {
		case "ready":
			ap.mu.Lock()
			ap.ready = true
			ap.mu.Unlock()
			log.Printf("[hermes] agent %s ready", ap.profileID)

		case "reasoning":
			content, _ := resp["content"].(string)
			msgID, _ := resp["id"].(string)
			if b.OnChatResponse != nil {
				b.OnChatResponse(ap.profileID, "__reasoning__:"+content, msgID)
			}

		case "chat":
			content, _ := resp["content"].(string)
			msgID, _ := resp["id"].(string)
			if b.OnChatResponse != nil {
				b.OnChatResponse(ap.profileID, content, msgID)
			}

		case "error":
			code, _ := resp["code"].(string)
			message, _ := resp["message"].(string)
			log.Printf("[hermes] %s error [%s]: %s", ap.profileID, code, message)
			if b.OnChatResponse != nil {
				b.OnChatResponse(ap.profileID, "__error__:"+message, "")
			}
		}
	}

	if err := ap.stdout.Err(); err != nil {
		log.Printf("[hermes] %s stdout error: %v", ap.profileID, err)
	}
	log.Printf("[hermes] agent %s process exited", ap.profileID)
}

func findScript(rel string) string {
	// Try relative to executable
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		if fileExists(filepath.Join(dir, rel)) {
			return filepath.Join(dir, rel)
		}
		if fileExists(filepath.Join(filepath.Dir(dir), rel)) {
			return filepath.Join(filepath.Dir(dir), rel)
		}
	}
	// Try absolute fallback
	candidates := []string{
		"/home/j/Codes/1claw/1claw-server/" + rel,
		filepath.Join(os.Getenv("HOME"), "Codes/1claw/1claw-server", rel),
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return rel
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
