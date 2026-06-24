package tool

import (
	"context"
	"testing"

	"github.com/local/swe-agent/internal/core"
)

func TestSubmitWithoutSubmissionDoesNotInventSummary(t *testing.T) {
	result, err := Submit{}.Execute(context.Background(), core.ToolInput{
		Call: core.ToolCall{Name: "submit"},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Output != "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n\n" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}
