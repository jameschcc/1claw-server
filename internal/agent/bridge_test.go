package agent

import (
	"context"
	"testing"
	"time"

	"1claw-server/internal/model"
)

func TestMockBridge_LoadAndStart(t *testing.T) {
	b := NewMockBridge()
	profile := model.Profile{
		ID:            "test",
		Name:          "Test Agent",
		Emoji:         "🧪",
		HermesProfile: "test",
	}

	b.LoadProfiles([]model.Profile{profile})
	if err := b.Start(context.Background(), &profile); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	status := b.GetStatus("test")
	if !status.Online {
		t.Error("expected profile to be online after Start()")
	}
}

func TestMockBridge_SendMessage(t *testing.T) {
	b := NewMockBridge()
	profile := model.Profile{ID: "test", Name: "Test"}
	b.LoadProfiles([]model.Profile{profile})
	b.Start(context.Background(), &profile)

	resp, err := b.SendMessage(context.Background(), model.ChatRequest{
		ProfileID: "test",
		Content:   "Hello",
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if resp == "" {
		t.Error("expected non-empty response")
	}
}

func TestMockBridge_Stop(t *testing.T) {
	b := NewMockBridge()
	profile := model.Profile{ID: "test", Name: "Test"}
	b.LoadProfiles([]model.Profile{profile})
	b.Start(context.Background(), &profile)
	b.Stop("test")

	status := b.GetStatus("test")
	if status.Online {
		t.Error("expected profile to be offline after Stop()")
	}
}

func TestMockBridge_SendOffline(t *testing.T) {
	b := NewMockBridge()
	profile := model.Profile{ID: "test", Name: "Test"}
	b.LoadProfiles([]model.Profile{profile})
	// Don't start it

	_, err := b.SendMessage(context.Background(), model.ChatRequest{
		ProfileID: "test",
		Content:   "Hello",
	})
	if err == nil {
		t.Error("expected error when sending to offline profile")
	}
}

func TestMockBridge_StartAll(t *testing.T) {
	b := NewMockBridge()
	b.LoadProfiles([]model.Profile{
		{ID: "a", Name: "A"},
		{ID: "b", Name: "B"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := b.StartAll(ctx); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}

	statuses := b.GetAllStatus()
	if len(statuses) != 2 {
		t.Errorf("expected 2 profiles, got %d", len(statuses))
	}
	for _, s := range statuses {
		if !s.Online {
			t.Errorf("expected profile %s to be online", s.ProfileID)
		}
	}
}
