package agent

import (
	"strings"
	"testing"

	"github.com/local/swe-agent/internal/core"
)

func TestToolResultEventDataRedactsSecretAndHugeOutput(t *testing.T) {
	output := "GITHUB_TOKEN=super-secret\n" + strings.Repeat("x", traceOutputPreviewLimit+200)
	data := buildToolResultEventData(core.ToolCall{Name: "shell"}, core.ToolResult{
		Code:   1,
		Output: output,
	})

	if _, ok := data["output"]; ok {
		t.Fatalf("tool_result event data should not include raw output: %#v", data)
	}
	preview := strings.TrimSpace(data["output_preview"].(string))
	if strings.Contains(preview, "super-secret") {
		t.Fatalf("output preview leaked secret: %q", preview)
	}
	if !strings.Contains(preview, "GITHUB_TOKEN=[REDACTED]") {
		t.Fatalf("expected redacted token marker in preview, got %q", preview)
	}
	if data["output_truncated"] != true {
		t.Fatalf("expected output_truncated=true, got %#v", data["output_truncated"])
	}
	if data["output_chars"] != len([]rune(output)) {
		t.Fatalf("expected original output char count, got %#v", data["output_chars"])
	}
	if hash := strings.TrimSpace(data["output_hash"].(string)); len(hash) != 64 {
		t.Fatalf("expected sha256 output hash, got %q", hash)
	}
}
