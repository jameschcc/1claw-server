package store

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"1claw-server/internal/model"

	_ "github.com/mattn/go-sqlite3"
)

const defaultDBPath = "1claw-chat.db"
const maxMessagesPerRole = 100

// Timestamp format with millisecond precision for proper ordering.
const timestampFormat = "2006-01-02T15:04:05.000Z07:00"

// ChatStore provides SQLite-backed persistent storage for conversations and messages.
// This enables cross-device chat: clients identify with a client_id, and the server
// remembers the conversation across reconnects and different devices.
//
// Profile-scoped messages (profile_messages table) enable cross-device history:
// messages are stored by profile_id globally, so any device can retrieve all
// messages for a profile regardless of which conversation/device they came from.
type ChatStore struct {
	db  *sql.DB
	mu  sync.RWMutex
}

// NewChatStore opens (or creates) the SQLite database at dbPath.
// Creates tables if they don't exist.
func NewChatStore(dbPath string) (*ChatStore, error) {
	if dbPath == "" {
		dbPath = defaultDBPath
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	s := &ChatStore{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	log.Printf("[store] chat store opened: %s", dbPath)
	return s, nil
}

func (s *ChatStore) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS conversations (
			id TEXT PRIMARY KEY,
			client_id TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL REFERENCES conversations(id),
			profile_id TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			timestamp TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_conv
		 ON messages(conversation_id, timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_conv_role
		 ON messages(conversation_id, role, timestamp DESC)`,
		// Profile-scoped messages — enables cross-device history retrieval
		`CREATE TABLE IF NOT EXISTS profile_messages (
			id TEXT PRIMARY KEY,
			profile_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			timestamp TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_profile_msgs
		 ON profile_messages(profile_id, timestamp)`,
	}
	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate query: %w", err)
		}
	}
	return nil
}

// Close shuts down the database.
func (s *ChatStore) Close() error {
	return s.db.Close()
}

// GetOrCreateConversation looks up a conversation by client_id, or creates one.
// Returns the conversation ID.
func (s *ChatStore) GetOrCreateConversation(clientID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(timestampFormat)

	// Try to find existing
	var convID string
	err := s.db.QueryRow(
		"SELECT id FROM conversations WHERE client_id = ?", clientID,
	).Scan(&convID)

	if err == sql.ErrNoRows {
		// Create new
		convID = fmt.Sprintf("conv_%d", time.Now().UnixMilli())
		_, err = s.db.Exec(
			"INSERT INTO conversations (id, client_id, created_at, updated_at) VALUES (?, ?, ?, ?)",
			convID, clientID, now, now,
		)
		if err != nil {
			return "", fmt.Errorf("create conversation: %w", err)
		}
		log.Printf("[store] new conversation %s for client %s", convID, clientID)
	} else if err != nil {
		return "", fmt.Errorf("lookup conversation: %w", err)
	}

	return convID, nil
}

// SaveMessage stores a message in the conversation and enforces the per-role cap.
// Excess messages (beyond maxMessagesPerRole) are pruned oldest-first.
func (s *ChatStore) SaveMessage(conversationID, profileID, role, content, msgID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(timestampFormat)

	_, err := s.db.Exec(
		"INSERT INTO messages (id, conversation_id, profile_id, role, content, timestamp) VALUES (?, ?, ?, ?, ?, ?)",
		msgID, conversationID, profileID, role, content, now,
	)
	if err != nil {
		return fmt.Errorf("save message: %w", err)
	}

	// Update conversation timestamp
	_, _ = s.db.Exec("UPDATE conversations SET updated_at = ? WHERE id = ?", now, conversationID)

	// Prune oldest messages beyond per-role cap
	s.prune(conversationID, role)

	return nil
}

// SaveMessageWithoutPrune stores a message without pruning (for bulk inserts).
func (s *ChatStore) SaveMessageWithoutPrune(conversationID, profileID, role, content, msgID string) error {
	now := time.Now().UTC().Format(timestampFormat)
	_, err := s.db.Exec(
		"INSERT INTO messages (id, conversation_id, profile_id, role, content, timestamp) VALUES (?, ?, ?, ?, ?, ?)",
		msgID, conversationID, profileID, role, content, now,
	)
	return err
}

func (s *ChatStore) prune(conversationID, role string) {
	// Delete messages beyond the cap for this conversation + role
	_, err := s.db.Exec(`
		DELETE FROM messages
		WHERE rowid IN (
			SELECT rowid FROM messages
			WHERE conversation_id = ? AND role = ?
			ORDER BY rowid DESC
			LIMIT -1 OFFSET ?
		)
	`, conversationID, role, maxMessagesPerRole)
	if err != nil {
		log.Printf("[store] prune error: %v", err)
	}
}

// GetRecentMessages returns the most recent messages (across all roles) for a conversation.
func (s *ChatStore) GetRecentMessages(conversationID string, limit int) ([]model.ChatMessage, error) {
	if limit <= 0 || limit > maxMessagesPerRole*3 {
		limit = maxMessagesPerRole * 3 // max 300 total
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, profile_id, role, content, timestamp
		FROM messages
		WHERE conversation_id = ?
		ORDER BY rowid ASC
	`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var messages []model.ChatMessage
	for rows.Next() {
		var m model.ChatMessage
		var ts string
		if err := rows.Scan(&m.ID, &m.ProfileID, &m.Role, &m.Content, &ts); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.Timestamp, _ = time.Parse(timestampFormat, ts)
		messages = append(messages, m)
	}

	// Take only the last N
	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}

	return messages, nil
}

// SaveProfileMessage stores a message in the profile-scoped table (cross-device).
// Unlike conversation-scoped messages, these are accessible from any device.
func (s *ChatStore) SaveProfileMessage(profileID, role, content, msgID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(timestampFormat)
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO profile_messages (id, profile_id, role, content, timestamp) VALUES (?, ?, ?, ?, ?)",
		msgID, profileID, role, content, now,
	)
	return err
}

// GetProfileMessages returns all stored messages for a given profile (cross-device).
// Messages are ordered oldest-first. Limit caps the total returned.
func (s *ChatStore) GetProfileMessages(profileID string, limit int) ([]model.ChatMessage, error) {
	if limit <= 0 || limit > maxMessagesPerRole*3 {
		limit = maxMessagesPerRole * 3
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, profile_id, role, content, timestamp
		FROM profile_messages
		WHERE profile_id = ?
		ORDER BY timestamp ASC
	`, profileID)
	if err != nil {
		return nil, fmt.Errorf("query profile messages: %w", err)
	}
	defer rows.Close()

	var messages []model.ChatMessage
	for rows.Next() {
		var m model.ChatMessage
		var ts string
		if err := rows.Scan(&m.ID, &m.ProfileID, &m.Role, &m.Content, &ts); err != nil {
			return nil, fmt.Errorf("scan profile message: %w", err)
		}
		m.Timestamp, _ = time.Parse(timestampFormat, ts)
		messages = append(messages, m)
	}

	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}

	return messages, nil
}

// GetRecentMessagesByProfile returns recent messages for a specific profile within a conversation.
func (s *ChatStore) GetRecentMessagesByProfile(conversationID, profileID string, limit int) ([]model.ChatMessage, error) {
	if limit <= 0 || limit > maxMessagesPerRole {
		limit = maxMessagesPerRole
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, profile_id, role, content, timestamp
		FROM messages
		WHERE conversation_id = ? AND profile_id = ?
		ORDER BY rowid ASC
	`, conversationID, profileID)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var messages []model.ChatMessage
	for rows.Next() {
		var m model.ChatMessage
		var ts string
		if err := rows.Scan(&m.ID, &m.ProfileID, &m.Role, &m.Content, &ts); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.Timestamp, _ = time.Parse(timestampFormat, ts)
		messages = append(messages, m)
	}

	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}

	return messages, nil
}
