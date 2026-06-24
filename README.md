# swe-agent

一个 Go 语言 SWE-agent MVP，实现了任务循环、模型适配、工具注册、本地 runtime、策略控制和 JSONL 轨迹记录。

## 快速开始

```bash
go test ./...
go run ./cmd/sweagent
make smoke
go run ./cmd/sweagent tools
go run ./cmd/sweagent run --task "finish immediately" --repo . --json
```

默认无参数会进入交互式 TUI。在底部输入 task 后回车即可发起一次运行；运行完成后可以继续输入下一条 task。常用 slash command 包括 `/history` 查看并切换历史 task、`/clear` 清空当前 TUI 会话视图、`/quit` 退出、`/trace` 查看轨迹路径。

默认使用 `mock` 模型，适合本地验证框架链路。接入 OpenAI-compatible 模型时，修改 `configs/default.yaml` 或通过命令行覆盖：

```bash
OPENAI_API_KEY=... go run ./cmd/sweagent run \
  --model-provider openai-compatible \
  --model gpt-4.1 \
  --task "fix the failing test" \
  --repo . \
  --auto-approve
```

也可以把本地 Codex CLI 用作 provider。这个模式下 Codex 只负责输出下一步 `swe_shell` action，实际命令执行仍由本项目的 runtime、policy 和 tool 层控制：

```bash
go run ./cmd/sweagent run \
  --model-provider codex-cli \
  --task "fix the failing test" \
  --repo . \
  --auto-approve
```

对应配置示例：

```yaml
model:
  provider: codex-cli
  command: codex
  sandbox: read-only
  approval_policy: never
```

如果使用本地开源 provider，可以在配置里加：

```yaml
model:
  provider: codex-cli
  oss: true
  local_provider: ollama
```

## 模块

- `cmd/sweagent`: CLI 入口，提供默认 TUI、`tui`、`run`、`tools`、`config`。
- `internal/agent`: agent 主循环和状态机。
- `internal/action`: 模型输出到工具调用的解析器。
- `internal/model`: mock、OpenAI-compatible 与 Codex CLI 模型适配。
- `internal/runtime`: 本地命令执行 runtime。
- `internal/tool`: 文件、搜索、diff、patch、测试、提交等工具。
- `internal/policy`: 工具审批、危险命令拦截、观测结果过滤。
- `internal/trajectory`: JSONL 轨迹记录。
- `internal/workspace`: 工作区定位与 diff 摘要。

更多架构说明见 `docs/`。
