package tool

import (
	"context"

	"github.com/local/swe-agent/internal/core"
)

type Submit struct{}

func (Submit) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "submit",
		Description: "Mark the task as complete. Optional submission text can summarize the result.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"submission": map[string]any{"type": "string"},
			},
		},
	}
}

func (Submit) Risk() core.RiskLevel { return core.RiskRead }

func (Submit) Execute(ctx context.Context, input core.ToolInput) (core.ToolResult, error) {
	submission := stringArg(input.Call.Args, "submission")
	if submission == "" {
		submission = "submitted"
	}
	return core.ToolResult{Output: "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n" + submission + "\n"}, nil
}
