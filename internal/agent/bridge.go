package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"1claw-server/internal/model"
)

// AgentBridge defines the interface for communicating with agent profiles.
type AgentBridge interface {
	// Start initializes an agent profile and begins listening.
	Start(ctx context.Context, profile *model.Profile) error
	// Stop terminates a running profile.
	Stop(profileID string) error
	// SendMessage sends a user message to the agent and returns the response.
	SendMessage(ctx context.Context, req model.ChatRequest) (string, error)
	// CancelMessage interrupts the active response for a profile/session.
	CancelMessage(profileID, sessionID string) error
	// GetStatus returns the current status of a profile.
	GetStatus(profileID string) model.AgentStatus
}

// Provider is the full interface used by the server and API layers.
// Both MockBridge and HermesBridge implement it.
type Provider interface {
	LoadProfiles(profiles []model.Profile)
	StartAll(ctx context.Context) error
	Start(ctx context.Context, profile *model.Profile) error
	Stop(profileID string) error
	SendMessage(ctx context.Context, req model.ChatRequest) (string, error)
	CancelMessage(profileID, sessionID string) error
	GetStatus(profileID string) model.AgentStatus
	GetAllStatus() []model.AgentStatus
	GetProfiles() []model.Profile
}

// AgentStatus represents the runtime state of an agent.
type AgentState struct {
	Profile  *model.Profile
	Online   bool
	LastPing time.Time
	mu       sync.RWMutex
}

// MockBridge implements AgentBridge with simulated responses.
// This is the development placeholder — replace with real Hermes integration.
type MockBridge struct {
	agents map[string]*AgentState
	mu     sync.RWMutex
}

// NewMockBridge creates a new mock agent bridge.
func NewMockBridge() *MockBridge {
	return &MockBridge{
		agents: make(map[string]*AgentState),
	}
}

// LoadProfiles registers profiles to be managed.
func (b *MockBridge) LoadProfiles(profiles []model.Profile) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range profiles {
		prof := p // copy
		b.agents[p.ID] = &AgentState{
			Profile:  &prof,
			Online:   false,
			LastPing: time.Now(),
		}
	}
}

// Start begins an agent profile.
func (b *MockBridge) Start(ctx context.Context, profile *model.Profile) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	state, exists := b.agents[profile.ID]
	if !exists {
		return fmt.Errorf("profile %s not found", profile.ID)
	}

	state.mu.Lock()
	state.Online = true
	state.LastPing = time.Now()
	state.Profile.Online = true
	state.mu.Unlock()

	return nil
}

// StartAll starts all loaded profiles.
func (b *MockBridge) StartAll(ctx context.Context) error {
	b.mu.RLock()
	profiles := make([]*model.Profile, 0, len(b.agents))
	for _, state := range b.agents {
		profiles = append(profiles, state.Profile)
	}
	b.mu.RUnlock()

	for _, p := range profiles {
		if err := b.Start(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// Stop stops an agent profile.
func (b *MockBridge) Stop(profileID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	state, exists := b.agents[profileID]
	if !exists {
		return fmt.Errorf("profile %s not found", profileID)
	}

	state.mu.Lock()
	state.Online = false
	state.Profile.Online = false
	state.mu.Unlock()

	return nil
}

// SendMessage sends a message and returns a mock response.
func (b *MockBridge) SendMessage(ctx context.Context, req model.ChatRequest) (string, error) {
	profileID := req.ProfileID
	b.mu.RLock()
	state, exists := b.agents[profileID]
	b.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("profile %s not found", profileID)
	}

	if !state.Online {
		return "", fmt.Errorf("profile %s is offline", profileID)
	}

	// Mock response — in production, this calls the real Hermes agent.
	return fmt.Sprintf("[%s] You said: %s", state.Profile.Name, req.Content), nil
}

// CancelMessage is a no-op for the mock bridge.
func (b *MockBridge) CancelMessage(profileID, sessionID string) error {
	return nil
}

// GetStatus returns the profile status.
func (b *MockBridge) GetStatus(profileID string) model.AgentStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()

	state, exists := b.agents[profileID]
	if !exists {
		return model.AgentStatus{
			ProfileID: profileID,
			Online:    false,
			Error:     "profile not found",
		}
	}

	state.mu.RLock()
	defer state.mu.RUnlock()
	return model.AgentStatus{
		ProfileID: profileID,
		Online:    state.Online,
	}
}

// GetAllStatus returns status for all profiles.
func (b *MockBridge) GetAllStatus() []model.AgentStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()

	statuses := make([]model.AgentStatus, 0, len(b.agents))
	for _, state := range b.agents {
		state.mu.RLock()
		statuses = append(statuses, model.AgentStatus{
			ProfileID: state.Profile.ID,
			Online:    state.Online,
		})
		state.mu.RUnlock()
	}
	return statuses
}

// GetProfiles returns all registered profiles.
func (b *MockBridge) GetProfiles() []model.Profile {
	b.mu.RLock()
	defer b.mu.RUnlock()

	profiles := make([]model.Profile, 0, len(b.agents))
	for _, state := range b.agents {
		state.mu.RLock()
		profiles = append(profiles, *state.Profile)
		state.mu.RUnlock()
	}
	return profiles
}
