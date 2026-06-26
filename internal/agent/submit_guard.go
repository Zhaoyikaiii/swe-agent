package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/problemtrace"
)

type taskRequirements struct {
	Guarded              bool   `json:"guarded"`
	RequiresChange       bool   `json:"requires_change"`
	RequiresValidation   bool   `json:"requires_validation"`
	RequiresCommit       bool   `json:"requires_commit"`
	RequiresPush         bool   `json:"requires_push"`
	RequiresPRInspection bool   `json:"requires_pr_inspection"`
	RequiresPRComments   bool   `json:"requires_pr_comments"`
	PRURL                string `json:"pr_url,omitempty"`
}

type submitEvidence struct {
	WorkspaceChanged bool `json:"workspace_changed"`
	ValidationPassed bool `json:"validation_passed"`
	CommitSucceeded  bool `json:"commit_succeeded"`
	PushSucceeded    bool `json:"push_succeeded"`
	PRViewed         bool `json:"pr_viewed"`
	PRCommentsRead   bool `json:"pr_comments_read"`
	GHAuthChecked    bool `json:"gh_auth_checked"`
}

var (
	prURLPattern      = regexp.MustCompile(`https?://[^\s"'<>]+/pull/[0-9]+`)
	prWordPattern     = regexp.MustCompile(`(?i)\bpr\b`)
	fixWordPattern    = regexp.MustCompile(`(?i)\b(fix|fixes|fixed|repair|resolve|resolved|address)\b`)
	commitWordPattern = regexp.MustCompile(`(?i)\bcommit\b`)
	pushWordPattern   = regexp.MustCompile(`(?i)\bpush\b`)
	testWordPattern   = regexp.MustCompile(`(?i)\b(test|tests|testing|verify|verification|validate|validation)\b`)
)

func detectTaskRequirements(task string) taskRequirements {
	lower := strings.ToLower(task)
	req := taskRequirements{
		PRURL: firstPRURL(task),
	}
	req.RequiresCommit = commitWordPattern.MatchString(task)
	req.RequiresPush = pushWordPattern.MatchString(task) || strings.Contains(task, "推送")
	req.RequiresPRComments = containsAny(lower,
		"unresolved comment",
		"unresolved review",
		"review comment",
		"review thread",
		"requested changes",
	) || strings.Contains(task, "未解决") || strings.Contains(task, "评论")
	req.RequiresPRInspection = req.PRURL != "" ||
		prWordPattern.MatchString(task) ||
		strings.Contains(lower, "pull request") ||
		req.RequiresPRComments
	req.RequiresChange = fixWordPattern.MatchString(task) ||
		req.RequiresPRComments ||
		containsAny(task, "修复", "解决")
	req.RequiresValidation = req.RequiresChange ||
		testWordPattern.MatchString(task) ||
		containsAny(task, "测试", "验证")
	req.Guarded = req.RequiresChange ||
		req.RequiresValidation ||
		req.RequiresCommit ||
		req.RequiresPush ||
		req.RequiresPRInspection ||
		req.RequiresPRComments
	return req
}

func submitConditionSummary(req taskRequirements) string {
	if !req.Guarded {
		return ""
	}
	var lines []string
	if req.RequiresPRInspection {
		lines = append(lines, "- Inspect the referenced GitHub PR before submitting.")
	}
	if req.RequiresPRComments {
		lines = append(lines, "- Read the PR review comments or unresolved threads before submitting.")
	}
	if req.RequiresChange {
		lines = append(lines, "- Record a workspace change or a successful commit before submitting.")
	}
	if req.RequiresValidation {
		lines = append(lines, "- Run focused validation or tests successfully before submitting.")
	}
	if req.RequiresCommit {
		lines = append(lines, "- Run git commit successfully before submitting.")
	}
	if req.RequiresPush {
		lines = append(lines, "- Run git push successfully before submitting.")
	}
	return strings.Join(lines, "\n")
}

func firstPRURL(task string) string {
	match := prURLPattern.FindString(task)
	return strings.TrimRight(match, ".,);]")
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func (a *Agent) runPRPreflight(ctx context.Context, state *State) string {
	command := buildPRPreflightCommand(state.Requirements.PRURL)
	call := core.ToolCall{Name: "shell", Args: map[string]any{"command": command}}

	var toolContext problemtrace.TraceContext
	if a.Trace != nil {
		tc, events := a.Trace.StartToolCall(ctx, 0, call)
		toolContext = tc
		a.appendEvents(ctx, events)
	}
	callData := map[string]any{"tool": call.Name, "args": call.Args, "system": "pr_preflight"}
	if toolContext.TraceID != "" {
		callData["trace_context"] = toolContext
	}
	a.appendEvent(ctx, core.Event{Type: "tool_call", Data: callData})

	res, err := a.Runtime.Execute(ctx, core.ExecRequest{
		Command: command,
		Cwd:     a.Workspace.Root(),
		Timeout: a.Config.RuntimeTimeout(),
	})
	if err != nil {
		res.Stderr += err.Error()
		if res.Code == 0 {
			res.Code = -1
		}
	}
	out := res.Stdout
	if res.Stderr != "" {
		out += res.Stderr
	}
	result := a.Policy.FilterObservation(ctx, core.ToolResult{
		Output:   out,
		Code:     res.Code,
		TimedOut: res.TimedOut,
	})
	a.recordToolEvidence(state, call, result)
	resultData := buildToolResultEventData(call, result)
	if toolContext.TraceID != "" {
		resultData["trace_context"] = toolContext
	}
	resultData["system"] = "pr_preflight"
	a.appendEvent(ctx, core.Event{Type: "tool_result", Data: resultData})
	if a.Trace != nil {
		a.appendEvents(ctx, a.Trace.ObserveToolResult(ctx, toolContext, call, result))
	}
	state.Messages = append(state.Messages, core.Message{Role: core.RoleTool, Name: "shell", Content: formatToolObservation(result)})
	if result.Code != 0 || result.TimedOut {
		return "Blocked: GitHub PR preflight failed; cannot inspect the PR with gh. Check gh auth status, repository access, and the PR URL."
	}
	return ""
}

func buildPRPreflightCommand(prURL string) string {
	quotedURL := shellSingleQuote(prURL)
	return strings.Join([]string{
		"printf '%s\\n' '== git remote -v ==' && git remote -v",
		"printf '%s\\n' '== git branch --show-current ==' && git branch --show-current",
		"printf '%s\\n' '== gh auth status ==' && gh auth status",
		"auth_code=$?",
		"printf '%s\\n' '== gh pr view ==' && gh pr view " + quotedURL + " --json number,title,headRefName,baseRefName,url",
		"view_code=$?",
		`if [ "$auth_code" -ne 0 ] || [ "$view_code" -ne 0 ]; then exit 1; fi`,
	}, "\n")
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func (a *Agent) recordToolEvidence(state *State, call core.ToolCall, result core.ToolResult) {
	if result.Code != 0 || result.TimedOut {
		return
	}
	command := commandArg(call)
	lowerCommand := strings.ToLower(command)

	switch call.Name {
	case "apply_patch":
		state.Evidence.WorkspaceChanged = true
	case "git_diff":
		if strings.TrimSpace(result.Output) != "" {
			state.Evidence.WorkspaceChanged = true
		}
	case "run_tests":
		state.Evidence.ValidationPassed = true
	case "shell":
		if isValidationShellCommand(lowerCommand) {
			state.Evidence.ValidationPassed = true
		}
		if strings.Contains(lowerCommand, "git commit") {
			state.Evidence.CommitSucceeded = true
			state.Evidence.WorkspaceChanged = true
		}
		if strings.Contains(lowerCommand, "git push") {
			state.Evidence.PushSucceeded = true
		}
		if strings.Contains(lowerCommand, "gh auth status") {
			state.Evidence.GHAuthChecked = true
		}
		if isPRViewCommand(lowerCommand) {
			state.Evidence.PRViewed = true
		}
		if isPRCommentsCommand(lowerCommand) {
			state.Evidence.PRCommentsRead = true
			state.Evidence.PRViewed = true
		}
	}
}

func commandArg(call core.ToolCall) string {
	if call.Args == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(call.Args["command"]))
}

func isValidationShellCommand(command string) bool {
	return strings.Contains(command, "go test") ||
		strings.Contains(command, "npm test") ||
		strings.Contains(command, "pnpm test") ||
		strings.Contains(command, "yarn test") ||
		strings.Contains(command, "pytest") ||
		strings.Contains(command, "cargo test") ||
		strings.Contains(command, "make test") ||
		strings.Contains(command, "make smoke")
}

func isPRViewCommand(command string) bool {
	return strings.Contains(command, "gh pr view") ||
		strings.Contains(command, "gh pr checkout") ||
		strings.Contains(command, "gh api")
}

func isPRCommentsCommand(command string) bool {
	return strings.Contains(command, "reviewthreads") ||
		strings.Contains(command, "review threads") ||
		strings.Contains(command, "pulls/comments") ||
		strings.Contains(command, "pull_request_review") ||
		strings.Contains(command, "--comments") ||
		strings.Contains(command, "reviews")
}

func (a *Agent) submitRejection(ctx context.Context, state *State) string {
	req := state.Requirements
	if !req.Guarded {
		return ""
	}
	var missing []string
	if req.RequiresPRInspection && !state.Evidence.PRViewed {
		missing = append(missing, "task references a PR, but no successful PR inspection was recorded")
	}
	if req.RequiresPRComments && !state.Evidence.PRCommentsRead {
		missing = append(missing, "task asks for PR review comments, but no successful review comment inspection was recorded")
	}
	if req.RequiresChange && !state.Evidence.WorkspaceChanged && !a.workspaceHasChanges(ctx) {
		missing = append(missing, "task asks for a fix, but no workspace change or successful commit was recorded")
	}
	if req.RequiresValidation && !state.Evidence.ValidationPassed {
		missing = append(missing, "task asks for a fix or validation, but no successful test or validation command was recorded")
	}
	if req.RequiresCommit && !state.Evidence.CommitSucceeded {
		missing = append(missing, "task asks to commit, but no successful git commit was recorded")
	}
	if req.RequiresPush && !state.Evidence.PushSucceeded {
		missing = append(missing, "task asks to push, but no successful git push was recorded")
	}
	if len(missing) == 0 {
		return ""
	}
	return "submit rejected: " + strings.Join(missing, "; ")
}

func (a *Agent) workspaceHasChanges(ctx context.Context) bool {
	status, err := a.Workspace.Status(ctx)
	if err == nil && strings.TrimSpace(status) != "" {
		return true
	}
	diff, err := a.Workspace.Diff(ctx)
	return err == nil && strings.TrimSpace(diff) != ""
}
