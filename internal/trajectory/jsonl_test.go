package trajectory

import (
	"context"
	"testing"
	"time"

	"github.com/local/swe-agent/internal/core"
)

func TestJSONLStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	defer store.Close()

	event := core.Event{Type: "tool_call", Time: time.Unix(10, 0), Data: map[string]any{"tool": "grep"}}
	if err := store.Append(ctx, event); err != nil {
		t.Fatalf("Append: %v", err)
	}
	events, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "tool_call" || events[0].Data["tool"] != "grep" {
		t.Fatalf("unexpected event: %#v", events[0])
	}
}
