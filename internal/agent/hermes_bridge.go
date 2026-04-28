package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"1claw-server/internal/model"
)

// HermesBridge implements AgentBridge by spawning a Python subprocess
// that uses real AIAgent.chat() from the Hermes codebase.
type HermesBridge struct {
	cmd      *exec.Cmd
	stdin    *json.Encoder
	stdout   *bufio.Scanner
	mu       sync.RWMutex
	profiles map[string]*model.Profile
	agents   map[string]bool // profile_id → loaded
	ready    chan struct{}
	stopCh   chan struct{}

	// OnChatResponse is called when the Python bridge responds to a chat.
	// Set by the WS handler to route responses to WebSocket clients.
	OnChatResponse func(profileID, content, msgID string)
}

// NewHermesBridge creates a new bridge connected to the Hermes Python subprocess.
func NewHermesBridge() *HermesBridge {
	return &HermesBridge{
		profiles: make(map[string]*model.Profile),
		agents:   make(map[string]bool),
		ready:    make(chan struct{}),
		stopCh:   make(chan struct{}),
	}
}

// StartSubprocess launches the Hermes Python bridge subprocess.
// pythonPath: path to the Hermes venv python (e.g., ~/.hermes/hermes-agent/venv/bin/python3)
// scriptPath: path to hermes_bridge.py
func (b *HermesBridge) StartSubprocess(ctx context.Context, pythonPath, scriptPath string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cmd != nil {
		return fmt.Errorf("bridge already running")
	}

	b.cmd = exec.CommandContext(ctx, pythonPath, scriptPath)

	// Stdin pipe for sending commands
	stdinPipe, err := b.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	b.stdin = json.NewEncoder(stdinPipe)

	// Stdout pipe for reading responses
	stdoutPipe, err := b.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	b.stdout = bufio.NewScanner(stdoutPipe)
	b.stdout.Buffer(make([]byte, 0, 256*1024), 256*1024)

	// Stderr goes to our log
	b.cmd.Stderr = os.Stderr

	if err := b.cmd.Start(); err != nil {
		return fmt.Errorf("start subprocess: %w", err)
	}

	log.Printf("[hermes] bridge subprocess started (pid=%d)", b.cmd.Process.Pid)

	// Start reading responses in background
	go b.readResponses()

	return nil
}

// LoadProfiles stores profile configurations (queued for send after subprocess starts).
func (b *HermesBridge) LoadProfiles(profiles []model.Profile) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range profiles {
		prof := p
		b.profiles[p.ID] = &prof
	}
}

// SendInit sends the init command to the Python bridge with all loaded profiles.
// Must be called after StartSubprocess.
func (b *HermesBridge) SendInit() error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	profileList := make([]map[string]interface{}, 0, len(b.profiles))
	for _, p := range b.profiles {
		profileList = append(profileList, map[string]interface{}{
			"id":             p.ID,
			"name":           p.Name,
			"emoji":          p.Emoji,
			"description":    p.Description,
			"hermes_profile": p.HermesProfile,
			"color":          p.Color,
		})
	}

	err := b.send(map[string]interface{}{
		"type":     "init",
		"profiles": profileList,
	})
	return err
}

// waitReady blocks until the bridge sends "ready".
func (b *HermesBridge) waitReady(timeout time.Duration) error {
	select {
	case <-b.ready:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("bridge not ready after %v", timeout)
	}
}

// StartAll implements AgentBridge: sends init, waits for ready.
// For the subprocess bridge, profiles must already be loaded.
func (b *HermesBridge) Start(ctx context.Context, profile *model.Profile) error {
	b.mu.Lock()
	b.agents[profile.ID] = true
	b.mu.Unlock()
	return nil
}

// StartAll waits for the bridge to be ready and marks all profiles as online.
func (b *HermesBridge) StartAll(ctx context.Context) error {
	if err := b.waitReady(60 * time.Second); err != nil {
		return err
	}
	// Mark all profiles as online
	b.mu.Lock()
	defer b.mu.Unlock()
	for id := range b.profiles {
		b.agents[id] = true
	}
	return nil
}

// Stop implements AgentBridge.
func (b *HermesBridge) Stop(profileID string) error {
	b.mu.Lock()
	delete(b.agents, profileID)
	b.mu.Unlock()
	return nil
}

// SendMessage sends a chat message to the specified profile via the Python bridge.
func (b *HermesBridge) SendMessage(ctx context.Context, profileID, message string) (string, error) {
	msgID := fmt.Sprintf("msg_%d", time.Now().UnixMilli())

	err := b.send(map[string]interface{}{
		"type":       "chat",
		"profile_id": profileID,
		"content":    message,
		"id":         msgID,
	})
	if err != nil {
		return "", fmt.Errorf("send chat: %w", err)
	}

	// The response will arrive asynchronously via readResponses.
	// We store a pending channel to wait for the specific response.
	// For simplicity: return a pending indicator — the response is
	// routed back through the WS handler directly.
	return "", fmt.Errorf("async: response will be routed via WebSocket")
}

// GetStatus returns the current status of a profile.
func (b *HermesBridge) GetStatus(profileID string) model.AgentStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, loaded := b.agents[profileID]
	_, exists := b.profiles[profileID]
	return model.AgentStatus{
		ProfileID: profileID,
		Online:    loaded && exists,
	}
}

// GetAllStatus returns status for all profiles.
func (b *HermesBridge) GetAllStatus() []model.AgentStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	statuses := make([]model.AgentStatus, 0, len(b.profiles))
	for id := range b.profiles {
		_, loaded := b.agents[id]
		statuses = append(statuses, model.AgentStatus{
			ProfileID: id,
			Online:    loaded,
		})
	}
	return statuses
}

// GetProfiles returns all registered profiles.
func (b *HermesBridge) GetProfiles() []model.Profile {
	b.mu.RLock()
	defer b.mu.RUnlock()
	profiles := make([]model.Profile, 0, len(b.profiles))
	for _, p := range b.profiles {
		profiles = append(profiles, *p)
	}
	return profiles
}

// Close shuts down the Python bridge subprocess.
func (b *HermesBridge) Close() error {
	_ = b.send(map[string]string{"type": "shutdown"})
	close(b.stopCh)
	if b.cmd != nil && b.cmd.Process != nil {
		return b.cmd.Wait()
	}
	return nil
}

// SendRaw sends an arbitrary JSON command to the Python bridge.
func (b *HermesBridge) SendRaw(v map[string]interface{}) {
	if err := b.send(v); err != nil {
		log.Printf("[hermes] send error: %v", err)
	}
}

// --- internal ---

func (b *HermesBridge) send(v interface{}) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.stdin == nil {
		return fmt.Errorf("bridge not started")
	}
	return b.stdin.Encode(v)
}

func (b *HermesBridge) readResponses() {
	for b.stdout.Scan() {
		line := b.stdout.Text()
		var resp map[string]interface{}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			log.Printf("[hermes] parse error: %v", err)
			continue
		}

		msgType, _ := resp["type"].(string)
		log.Printf("[hermes] response: type=%s", msgType)

		switch msgType {
		case "ready":
			profileCount, _ := resp["profile_count"].(float64)
			log.Printf("[hermes] bridge ready with %.0f profiles", profileCount)
			close(b.ready)

		case "agent_ready":
			pid, _ := resp["profile_id"].(string)
			status, _ := resp["status"].(string)
			log.Printf("[hermes] agent %s is %s", pid, status)
			if b.OnChatResponse != nil && status == "real" {
				b.OnChatResponse(pid, "__agent_ready__", "")
			}

		case "agent_starting":
			pid, _ := resp["profile_id"].(string)
			log.Printf("[hermes] agent %s starting...", pid)
			if b.OnChatResponse != nil {
				b.OnChatResponse(pid, "__agent_starting__", "")
			}

		case "reasoning":
			pid, _ := resp["profile_id"].(string)
			content, _ := resp["content"].(string)
			msgID, _ := resp["id"].(string)
			if b.OnChatResponse != nil {
				b.OnChatResponse(pid, "__reasoning__:"+content, msgID)
			}

		case "chat":
			pid, _ := resp["profile_id"].(string)
			content, _ := resp["content"].(string)
			msgID, _ := resp["id"].(string)
			if b.OnChatResponse != nil {
				b.OnChatResponse(pid, content, msgID)
			}

		case "status":
			profilesRaw, ok := resp["profiles"].([]interface{})
			if ok {
				log.Printf("[hermes] status: %d profiles", len(profilesRaw))
			}

		case "error":
			code, _ := resp["code"].(string)
			message, _ := resp["message"].(string)
			log.Printf("[hermes] error [%s]: %s", code, message)
		}
	}

	if err := b.stdout.Err(); err != nil {
		log.Printf("[hermes] stdout error: %v", err)
	}
	log.Println("[hermes] bridge stdout closed")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
