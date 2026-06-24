package action

import (
	"testing"

	"github.com/local/swe-agent/internal/core"
)

func TestParserParsesSingleShellBlock(t *testing.T) {
	parser := NewParser()
	calls, err := parser.Parse(core.ModelResponse{
		Message: core.Message{Role: core.RoleAssistant, Content: "THOUGHT\n\n```swe_shell\ngo test ./...\n```"},
	})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "shell" {
		t.Fatalf("expected shell tool, got %q", calls[0].Name)
	}
	if got := calls[0].Args["command"]; got != "go test ./..." {
		t.Fatalf("unexpected command: %#v", got)
	}
}

func TestParserMapsSubmitCommand(t *testing.T) {
	parser := NewParser()
	calls, err := parser.Parse(core.ModelResponse{
		Message: core.Message{Role: core.RoleAssistant, Content: "```swe_shell\nsubmit\n```"},
	})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(calls) != 1 || calls[0].Name != "submit" {
		t.Fatalf("expected submit call, got %#v", calls)
	}
}

func TestParserRejectsMultipleBlocks(t *testing.T) {
	parser := NewParser()
	_, err := parser.Parse(core.ModelResponse{
		Message: core.Message{Role: core.RoleAssistant, Content: "```swe_shell\necho one\n```\n```swe_shell\necho two\n```"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
