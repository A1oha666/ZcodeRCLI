# Zcoder Go

运行在终端里的 AI Agent CLI。

## 功能特性

- ReAct Agent 循环，OpenAI-compatible 工具调用与流式输出
- 全屏 TUI，单次 prompt 模式，Runtime API
- 内置文件/搜索/shell/联网等工具，MCP、Skill、RAG、记忆、快照
- Agent 集群：几十到上千个 agent 并发 coding，git worktree 隔离、限流调度、分段进度条

## 环境要求

- Go 1.26 或更新版本

## 快速开始

```bash
git clone https://github.com/itwanger/zcoder-go.git
cd zcoder-go
go run ./cmd/zcoder doctor
go run ./cmd/zcoder --once "你好，介绍一下当前项目"
go run ./cmd/zcoder
```

```bash
go run ./cmd/zcoder --plain                      # 简易 REPL
go run ./cmd/zcoder index . && go run ./cmd/zcoder search "Agent"
go run ./cmd/zcoder serve --port 8080            # Runtime API
go run ./cmd/zcoder wechat status
```

配置通过环境变量：`ZCODER_PROVIDER`、`ZCODER_MODEL`、`ZCODER_API_KEY`，以及 `DEEPSEEK_API_KEY`、`GLM_API_KEY`、`KIMI_API_KEY`、`OPENAI_API_KEY`、`SERPAPI_API_KEY` 等；`go run ./cmd/zcoder doctor` 可检查当前配置。

## 交互命令

```text
/help
/exit
/plan <task>
/team <task>
/cluster [--agents N] [--concurrency N] [--simulate] <task>
```

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

## 其他

- 内置工具：`read_file`、`write_file`、`list_dir`、`glob_files`、`grep_code`、`execute_command`、`create_project`、`web_search`、`web_fetch`、`save_memory`、`load_skill`、`search_code`、`restore_snapshot`；危险动作有路径/命令安全策略和审计日志
- MCP：读取用户级和项目级配置，远端工具注册为 `mcp__<server-name>__<tool-name>`
