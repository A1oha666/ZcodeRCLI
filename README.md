# Zcoder Go

Zcoder Go 是一个运行在终端里的 AI Agent CLI，面向真实项目开发场景：读写文件、搜索代码、执行命令、联网检索、调用 MCP 工具、加载 Skill、保存记忆、生成快照、恢复现场，并通过 Runtime API 对外提供 threads、turns 和 events 能力。

## 功能特性

- ReAct Agent 循环与 OpenAI-compatible tool calling
- OpenAI-compatible 流式 LLM 客户端，默认面向 DeepSeek 配置
- 全屏终端 TUI，基于 Bubble Tea、Bubbles textarea、Lip Gloss 和 Glamour 渲染
- 单次 prompt 模式，适合脚本、管道和自动化调用
- 内置文件、目录、glob、grep、shell、项目创建、联网搜索、网页抓取等工具
- 本地 RAG 代码索引、检索和简单关系图
- Skill 三层扫描、frontmatter 解析、启用状态管理和 `load_skill` 延迟注入
- MCP stdio/HTTP 基础握手、`tools/list`、动态工具注册和调用
- Plan-and-Execute、Multi-Agent 编排入口
- Agent 集群：几十到上千个 agent 并发 coding，git worktree 隔离、限流调度、分段进度条
- Runtime API：threads、turns、events
- PathGuard、CommandGuard、危险操作审计日志、快照/恢复基础能力

## 环境要求

- Go 1.26 或更新版本
- 可选：`rg`，用于更快的本地搜索
- 可选：MCP server 所需的本地运行时，例如 Node.js、Python 或远端 HTTP MCP 服务

## 快速开始

```bash
git clone https://github.com/itwanger/zcoder-go.git
cd zcoder-go
go run ./cmd/zcoder doctor
go run ./cmd/zcoder --once "你好，介绍一下当前项目"
go run ./cmd/zcoder --mode plan --once "分析这个需求并实现"
go run ./cmd/zcoder
```

常用命令：

```bash
go run ./cmd/zcoder --plain
go run ./cmd/zcoder index .
go run ./cmd/zcoder search "Agent"
go run ./cmd/zcoder cluster --agents 8 "给 README 提出改进建议"
go run ./cmd/zcoder serve --port 8080
go run ./cmd/zcoder wechat status
```

可用环境变量：

```bash
export ZCODER_PROVIDER=deepseek
export ZCODER_MODEL=deepseek-v4-pro
export ZCODER_API_KEY=...
export DEEPSEEK_API_KEY=...
export STEP_API_KEY=...
export KIMI_API_KEY=...
export GLM_API_KEY=...
export OPENAI_API_KEY=...
export SERPAPI_API_KEY=...
export SEARXNG_BASE_URL=http://localhost:8080
```

## 交互命令

进入 `go run ./cmd/zcoder` 后，可以使用：

```text
/help
/exit
/plan <task>
/team <task>
/cluster [--agents N] [--concurrency N] [--simulate] <task>
```

`--once` 和 `--plain` 也支持 `/plan <task>`、`/team <task>` 前缀；脚本化调用时也可以通过 `--mode plan` 或 `--mode team` 显式选择执行模式。

更多命令会随着 Java/Python 版本能力对齐逐步补齐。

## Agent 集群

集群把一个任务拆成 N 个子任务，交给并发 worker 池同时执行，最后汇总成一份报告。利用 Go 的并发能力支持几十到上千个 agent 同时 coding（单机进程内 demo 级实现，不做分布式）。

```bash
# 模拟模式：无需 API key，体验完整调度链路
go run ./cmd/zcoder cluster --agents 200 --concurrency 50 --simulate "给项目补充单元测试"

# 真实模式：每个 agent 发起真实的 LLM 调用
go run ./cmd/zcoder cluster --agents 4 "给 README 提出四条不同角度的改进建议"
```

工作流程：

1. **Coordinator**：一次 LLM 调用把任务拆成 N 个子任务（解析失败自动降级为按视角切分）
2. **Worker Pool**：goroutine 池 + 信号量限流（`--concurrency`，默认 8），每个 worker 运行完整的 ReAct 循环并调用真实工具
3. **Aggregator**：最后一次 LLM 调用合并 worker 产出（最多采样 20 份，防上下文溢出）

隔离与沙箱：

- 真实模式下每个 worker 在独立的 **git worktree**（detached HEAD）中工作，存放在 `~/.zcoder/cluster/<runID>/`，互不冲突
- 运行结束后 worktree 默认自动回收；`--keep-worktrees` 可保留改动供人工检查或手动合并（集群不会自动 commit/merge 回主分支）
- 非 git 目录自动降级为普通目录沙箱；`--simulate` 模式使用轻量目录沙箱
- `--isolation worktree|dir` 可显式指定隔离方式

进度展示：

- CLI 默认单行原地刷新分段进度条：绿█完成、红█失败、黄▒运行中、暗░排队，附完成/失败/运行计数
- `--verbose` 恢复逐 worker 明细输出
- TUI 中输入 `/cluster ...` 可看到带边框的实时进度面板（彩色进度条 + 最近 worker 事件）

单个 worker 失败不会中止集群，错误会记录在统计和报告中。

## 内置工具

Zcoder Go 当前内置的 Agent 工具包括：

- `read_file`
- `write_file`
- `list_dir`
- `glob_files`
- `grep_code`
- `execute_command`
- `create_project`
- `web_search`
- `web_fetch`
- `save_memory`
- `load_skill`
- `search_code`
- `restore_snapshot`

写文件、执行命令、恢复快照等危险动作会经过路径和命令安全策略处理，并写入审计日志。

## MCP 与 Runtime API

Zcoder Go 可以读取用户级和项目级 MCP 配置，并把远端工具注册为：

```text
mcp__<server-name>__<tool-name>
```

Runtime API 可以通过以下命令启动：

```bash
go run ./cmd/zcoder serve --port 8080
```

