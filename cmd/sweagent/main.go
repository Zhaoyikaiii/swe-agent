package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/local/swe-agent/internal/action"
	agentpkg "github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/config"
	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/model"
	"github.com/local/swe-agent/internal/policy"
	localruntime "github.com/local/swe-agent/internal/runtime"
	"github.com/local/swe-agent/internal/tool"
	"github.com/local/swe-agent/internal/trajectory"
	"github.com/local/swe-agent/internal/workspace"
	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	switch args[0] {
	case "run":
		return runCommand(args[1:])
	case "tools":
		return toolsCommand(args[1:])
	case "config":
		return configCommand(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("config", "configs/default.yaml", "path to YAML config")
	taskText := fs.String("task", "", "task text")
	taskFile := fs.String("task-file", "", "path to task text file")
	repo := fs.String("repo", ".", "repository/workspace path")
	modelProvider := fs.String("model-provider", "", "override model provider")
	modelName := fs.String("model", "", "override model name")
	trajectoryDir := fs.String("trajectory-dir", "", "override trajectory output directory")
	autoApprove := fs.Bool("auto-approve", false, "auto approve read/write/exec tools")
	jsonOutput := fs.Bool("json", false, "print result as JSON")
	mockResponses := fs.String("mock-response", "", "mock model response; repeat actions with ||| separator")
	if err := fs.Parse(args); err != nil {
		return err
	}
	task, err := resolveTask(*taskText, *taskFile)
	if err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *modelProvider != "" {
		cfg.Model.Provider = *modelProvider
	}
	if *modelName != "" {
		cfg.Model.Model = *modelName
	}
	if *trajectoryDir != "" {
		cfg.Trajectory.Dir = *trajectoryDir
	}
	if *autoApprove {
		cfg.Policy.AutoApproveRead = true
		cfg.Policy.AutoApproveWrite = true
		cfg.Policy.AutoApproveExec = true
	}

	ws, err := workspace.New(*repo)
	if err != nil {
		return err
	}
	rt := localruntime.NewLocal(cfg.Runtime.Env)
	registry := tool.NewRegistry(cfg.Tools.Enabled)
	store, err := trajectory.NewJSONLStore(cfg.Trajectory.Dir)
	if err != nil {
		return err
	}
	defer store.Close()
	llm, err := buildModel(cfg, *mockResponses)
	if err != nil {
		return err
	}

	ag := &agentpkg.Agent{
		Config:     cfg,
		Model:      llm,
		Runtime:    rt,
		Tools:      registry,
		Parser:     action.NewParser(),
		Policy:     policy.NewSimple(cfg.Policy),
		Trajectory: store,
		Workspace:  ws,
	}
	result, err := ag.Run(context.Background(), core.Task{Text: task, Repo: ws.Root()})
	if *jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("status: %s\n", result.Status)
		fmt.Printf("steps: %d\n", result.Steps)
		fmt.Printf("trajectory: %s\n", result.TrajectoryPath)
		if result.Submission != "" {
			fmt.Printf("submission: %s\n", result.Submission)
		}
		if result.Diff != "" {
			fmt.Printf("\ndiff:\n%s\n", result.Diff)
		}
	}
	return err
}

func toolsCommand(args []string) error {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	configPath := fs.String("config", "configs/default.yaml", "path to YAML config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	registry := tool.NewRegistry(cfg.Tools.Enabled)
	for _, spec := range registry.List() {
		t, _ := registry.Get(spec.Name)
		fmt.Printf("%-14s %-6s %s\n", spec.Name, t.Risk(), spec.Description)
	}
	return nil
}

func configCommand(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	configPath := fs.String("config", "configs/default.yaml", "path to YAML config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	fmt.Print(string(data))
	return nil
}

func buildModel(cfg agentpkg.Config, mockResponses string) (core.Model, error) {
	switch strings.ToLower(cfg.Model.Provider) {
	case "mock":
		var responses []string
		if mockResponses != "" {
			responses = strings.Split(mockResponses, "|||")
		}
		return model.NewMock(responses), nil
	case "openai", "openai-compatible":
		return model.NewOpenAICompatible(cfg.Model.BaseURL, cfg.Model.APIKeyEnv, cfg.Model.Model)
	case "codex", "codex-cli", "local-codex":
		return model.NewCodexCLI(model.CodexCLIOptions{
			Command:        cfg.Model.Command,
			Model:          cfg.Model.Model,
			Profile:        cfg.Model.Profile,
			OSS:            cfg.Model.OSS,
			LocalProvider:  cfg.Model.LocalProvider,
			Sandbox:        cfg.Model.Sandbox,
			ApprovalPolicy: cfg.Model.ApprovalPolicy,
			ExtraArgs:      cfg.Model.ExtraArgs,
		})
	default:
		return nil, fmt.Errorf("unsupported model provider %q", cfg.Model.Provider)
	}
}

func resolveTask(taskText, taskFile string) (string, error) {
	if taskText != "" && taskFile != "" {
		return "", errors.New("use either --task or --task-file, not both")
	}
	if taskFile != "" {
		data, err := os.ReadFile(taskFile)
		if err != nil {
			return "", err
		}
		taskText = string(data)
	}
	taskText = strings.TrimSpace(taskText)
	if taskText == "" {
		return "", errors.New("--task or --task-file is required")
	}
	return taskText, nil
}

func printUsage() {
	fmt.Println(`Usage:
  sweagent run --task "fix the failing test" --repo . [--auto-approve]
  sweagent tools
  sweagent config

Commands:
  run      execute one SWE-agent task
  tools    list enabled tools
  config   print merged configuration`)
}
