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
	"github.com/local/swe-agent/internal/tui"
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
		return tuiCommand(nil)
	}
	switch args[0] {
	case "run":
		return runCommand(args[1:])
	case "tui":
		return tuiCommand(args[1:])
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

type agentOptions struct {
	configPath    string
	repo          string
	modelProvider string
	modelName     string
	trajectoryDir string
	mockResponses string
	autoApprove   bool
}

func defaultAgentOptions() agentOptions {
	return agentOptions{
		configPath: "configs/default.yaml",
		repo:       ".",
	}
}

func bindAgentFlags(fs *flag.FlagSet, opts *agentOptions) {
	fs.StringVar(&opts.configPath, "config", opts.configPath, "path to YAML config")
	fs.StringVar(&opts.repo, "repo", opts.repo, "repository/workspace path")
	fs.StringVar(&opts.modelProvider, "model-provider", "", "override model provider")
	fs.StringVar(&opts.modelName, "model", "", "override model name")
	fs.StringVar(&opts.trajectoryDir, "trajectory-dir", "", "override trajectory output directory")
	fs.BoolVar(&opts.autoApprove, "auto-approve", false, "auto approve read/write/exec tools")
	fs.StringVar(&opts.mockResponses, "mock-response", "", "mock model response; repeat actions with ||| separator")
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	opts := defaultAgentOptions()
	bindAgentFlags(fs, &opts)
	taskText := fs.String("task", "", "task text")
	taskFile := fs.String("task-file", "", "path to task text file")
	jsonOutput := fs.Bool("json", false, "print result as JSON")
	tuiMode := fs.Bool("tui", false, "run with an interactive terminal UI")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tuiMode && *jsonOutput {
		return errors.New("--tui and --json cannot be used together")
	}
	task, err := resolveTask(*taskText, *taskFile)
	if err != nil {
		return err
	}
	ag, tuiSession, store, err := buildAgent(opts, *tuiMode)
	if err != nil {
		return err
	}
	defer store.Close()
	taskSpec := core.Task{Text: task, Repo: ag.Workspace.Root()}
	if *tuiMode {
		result, err := tuiSession.Run(context.Background(), ag, taskSpec)
		if result.Status != "" {
			printTextResult(result)
		}
		return err
	}

	result, err := ag.Run(context.Background(), taskSpec)
	if *jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
	} else {
		printTextResult(result)
	}
	return err
}

func tuiCommand(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	opts := defaultAgentOptions()
	bindAgentFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ag, tuiSession, store, err := buildAgent(opts, true)
	if err != nil {
		return err
	}
	defer store.Close()
	_, err = tuiSession.Loop(context.Background(), ag, ag.Workspace.Root())
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func buildAgent(opts agentOptions, interactive bool) (*agentpkg.Agent, *tui.Session, *trajectory.JSONLStore, error) {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return nil, nil, nil, err
	}
	if opts.modelProvider != "" {
		cfg.Model.Provider = opts.modelProvider
	}
	if opts.modelName != "" {
		cfg.Model.Model = opts.modelName
	}
	if opts.trajectoryDir != "" {
		cfg.Trajectory.Dir = opts.trajectoryDir
	}
	if opts.autoApprove {
		cfg.Policy.AutoApproveRead = true
		cfg.Policy.AutoApproveWrite = true
		cfg.Policy.AutoApproveExec = true
	}

	ws, err := workspace.New(opts.repo)
	if err != nil {
		return nil, nil, nil, err
	}
	rt := localruntime.NewLocal(cfg.Runtime.Env)
	registry := tool.NewRegistry(cfg.Tools.Enabled)
	store, err := trajectory.NewJSONLStore(cfg.Trajectory.Dir)
	if err != nil {
		return nil, nil, nil, err
	}
	llm, err := buildModel(cfg, opts.mockResponses)
	if err != nil {
		store.Close()
		return nil, nil, nil, err
	}

	var runnerPolicy core.Policy = policy.NewSimple(cfg.Policy)
	var eventSink agentpkg.EventSink
	var tuiSession *tui.Session
	if interactive {
		tuiSession = tui.NewSession()
		runnerPolicy = policy.NewInteractive(cfg.Policy, tuiSession)
		eventSink = tuiSession
	}

	ag := &agentpkg.Agent{
		Config:     cfg,
		Model:      llm,
		Runtime:    rt,
		Tools:      registry,
		Parser:     action.NewParser(),
		Policy:     runnerPolicy,
		Trajectory: store,
		Workspace:  ws,
		EventSink:  eventSink,
	}
	return ag, tuiSession, store, nil
}

func printTextResult(result agentpkg.Result) {
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
  sweagent
  sweagent tui [--repo .]
  sweagent run --task "fix the failing test" --repo . [--auto-approve]
  sweagent run --task "fix the failing test" --repo . --tui
  sweagent tools
  sweagent config

Commands:
  tui      open the interactive terminal UI
  run      execute one SWE-agent task
  tools    list enabled tools
  config   print merged configuration`)
}
