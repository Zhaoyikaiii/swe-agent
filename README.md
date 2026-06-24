# swe-agent

一个 Go 语言 SWE-agent MVP，实现了任务循环、模型适配、工具注册、本地 runtime、策略控制和 JSONL 轨迹记录。

## 快速开始

```bash
go test ./...
go run ./cmd/sweagent tools
go run ./cmd/sweagent run --task "finish immediately" --repo . --json
```

默认使用 `mock` 模型，适合本地验证框架链路。接入 OpenAI-compatible 模型时，修改 `configs/default.yaml` 或通过命令行覆盖：

```bash
OPENAI_API_KEY=... go run ./cmd/sweagent run \
  --model-provider openai-compatible \
  --model gpt-4.1 \
  --task "fix the failing test" \
  --repo . \
  --auto-approve
```

## 模块

- `cmd/sweagent`: CLI 入口，提供 `run`、`tools`、`config`。
- `internal/agent`: agent 主循环和状态机。
- `internal/action`: 模型输出到工具调用的解析器。
- `internal/model`: mock 与 OpenAI-compatible 模型适配。
- `internal/runtime`: 本地命令执行 runtime。
- `internal/tool`: 文件、搜索、diff、patch、测试、提交等工具。
- `internal/policy`: 工具审批、危险命令拦截、观测结果过滤。
- `internal/trajectory`: JSONL 轨迹记录。
- `internal/workspace`: 工作区定位与 diff 摘要。

更多架构说明见 `docs/`。
