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
```

`--once` 和 `--plain` 也支持 `/plan <task>`、`/team <task>` 前缀；脚本化调用时也可以通过 `--mode plan` 或 `--mode team` 显式选择执行模式。

更多命令会随着 Java/Python 版本能力对齐逐步补齐。

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

