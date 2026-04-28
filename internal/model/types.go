package model

import "time"

// Profile represents an agent profile configuration.
type Profile struct {
	ID            string    `json:"id" yaml:"id"`
	Name          string    `json:"name" yaml:"name"`
	Emoji         string    `json:"emoji" yaml:"emoji"`
	Description   string    `json:"description" yaml:"description"`
	HermesProfile string    `json:"hermes_profile" yaml:"hermes_profile"`
	Color         string    `json:"color" yaml:"color"`
	Online        bool      `json:"online" yaml:"-"`
	Status        string    `json:"status" yaml:"-"`
	TasksQueue    int       `json:"tasks_queue" yaml:"-"`
	CreatedAt     time.Time `json:"created_at" yaml:"-"`
	UpdatedAt     time.Time `json:"updated_at" yaml:"-"`
}

// ChatMessage represents a single message in a conversation.
type ChatMessage struct {
	ID        string    `json:"id"`
	ProfileID string    `json:"profile_id"`
	Content   string    `json:"content"`
	Role      string    `json:"role"` // "user" or "agent"
	Timestamp time.Time `json:"timestamp"`
}

// ConversationTurn is a minimal chat turn used for history bootstrap.
type ConversationTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest represents a normalized chat request sent to an agent bridge.
type ChatRequest struct {
	ProfileID string
	Content   string
	ID        string
	SessionID string
	History   []ConversationTurn
}

// WSMessage represents a WebSocket protocol message.
type WSMessage struct {
	Type      string             `json:"type"`
	ProfileID string             `json:"profile_id,omitempty"`
	Content   string             `json:"content,omitempty"`
	ID        string             `json:"id,omitempty"`
	SessionID string             `json:"session_id,omitempty"`
	History   []ConversationTurn `json:"history,omitempty"`
	Timestamp string             `json:"timestamp,omitempty"`
}

// WSResponse is the server-to-client response envelope.
type WSResponse struct {
	Type      string      `json:"type"`
	Profiles  []Profile   `json:"profiles,omitempty"`
	ProfileID string      `json:"profile_id,omitempty"`
	Content   string      `json:"content,omitempty"`
	ID        string      `json:"id,omitempty"`
	SessionID string      `json:"session_id,omitempty"`
	Timestamp string      `json:"timestamp,omitempty"`
	Message   string      `json:"message,omitempty"`
	Code      string      `json:"code,omitempty"`
	Data      interface{} `json:"data,omitempty"`
	Title     string      `json:"title,omitempty"`
}

// ServerConfig holds the full server configuration.
type ServerConfig struct {
	Server    ServerSettings `yaml:"server"`
	WebSocket WSSettings     `yaml:"websocket"`
	Profiles  []Profile      `yaml:"profiles"`
}

// ServerSettings holds HTTP server settings.
type ServerSettings struct {
	Host    string `yaml:"host"`
	Port    int    `yaml:"port"`
	WSPath  string `yaml:"ws_path"`
	APIPath string `yaml:"api_path"`
}

// WSSettings holds WebSocket connection settings.
type WSSettings struct {
	PingInterval   int `yaml:"ping_interval"`
	WriteTimeout   int `yaml:"write_timeout"`
	ReadTimeout    int `yaml:"read_timeout"`
	MaxMessageSize int `yaml:"max_message_size"`
}

// AgentStatus represents the status of an agent profile.
type AgentStatus struct {
	ProfileID string `json:"profile_id"`
	Online    bool   `json:"online"`
	Error     string `json:"error,omitempty"`
}
