package store

import (
	"fmt"
	"os"
	"testing"
)

func TestChatStore(t *testing.T) {
	dbPath := "/tmp/test-1claw-chat.db"
	os.Remove(dbPath)
	defer os.Remove(dbPath)

	s, err := NewChatStore(dbPath)
	if err != nil {
		t.Fatalf("NewChatStore: %v", err)
	}
	defer s.Close()

	// Get or create conversation
	convID, err := s.GetOrCreateConversation("test-client-1")
	if err != nil {
		t.Fatalf("GetOrCreateConversation: %v", err)
	}
	if convID == "" {
		t.Fatal("expected non-empty conversation ID")
	}

	// Same client returns same conversation
	sameID, err := s.GetOrCreateConversation("test-client-1")
	if err != nil {
		t.Fatalf("GetOrCreateConversation same: %v", err)
	}
	if sameID != convID {
		t.Fatalf("expected same conv ID %s, got %s", convID, sameID)
	}

	// Store messages
	if err := s.SaveMessage(convID, "dev", "user", "Hello", "msg_1"); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if err := s.SaveMessage(convID, "dev", "agent", "Hi there!", "msg_2"); err != nil {
		t.Fatalf("SaveMessage agent: %v", err)
	}
	if err := s.SaveMessage(convID, "assist", "user", "How are you?", "msg_3"); err != nil {
		t.Fatalf("SaveMessage 3: %v", err)
	}

	// Retrieve messages
	msgs, err := s.GetRecentMessages(convID, 10)
	if err != nil {
		t.Fatalf("GetRecentMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	// Verify ordering (ascending)
	if msgs[0].ID != "msg_1" || msgs[2].ID != "msg_3" {
		t.Fatalf("unexpected order: %v", msgs)
	}

	// Test pruning: store >100 messages for one role with unique IDs
	for i := 0; i < 105; i++ {
		mid := fmt.Sprintf("prune_%d", i)
		if err := s.SaveMessage(convID, "dev", "user", "msg", mid); err != nil {
			t.Fatalf("SaveMessage prune %d: %v", i, err)
		}
	}

	msgs, _ = s.GetRecentMessages(convID, 1000)
	if len(msgs) > maxMessagesPerRole+5 {
		t.Fatalf("expected <=%d messages after prune, got %d", maxMessagesPerRole, len(msgs))
	}
	t.Logf("Prune test: %d messages kept", len(msgs))
}
