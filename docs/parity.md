# Java Parity Ledger

This file tracks the Go port against the Java Zcoder surface in
`/Users/itwanger/Documents/GitHub/zcoder`.

## Implemented in the Go baseline

- CLI: `zcoder`, `doctor`, `index`, `search`, `graph`, `serve`, `wechat`.
- TUI: full-window Bubble Tea alternate-screen UI, solid `π` logo, transcript,
  textarea input area, mode/status bar.
- Agent: ReAct loop, tool-call execution, shared run-mode dispatch for ReAct,
  Plan-and-Execute and Multi-Agent roles.
- Tools: file read/write/list/glob, deterministic grep, shell execution, project creation,
  RAG `search_code`, web search, web fetch, memory save, skill load, snapshot restore.
- Search/RAG: local code index with token scoring and import/function relation extraction.
- MCP: user/project config loading, stdio/HTTP JSON-RPC, dynamic `mcp__server__tool` registration.
- Skill: builtin/user/project lookup, frontmatter parser, enabled/disabled state, lazy context buffer.
- Runtime API: local authenticated threads/turns/events with `react`, `plan`
  and `team` turn modes.
- WeChat: command surface, account store/status, formatter and non-interactive safety policy.
- Safety: workspace PathGuard, command blacklist, dangerous operation audit log.

## Known gaps versus Java v23

- Browser shared/isolated switching is exposed through MCP registration, but Chrome-specific
  sensitive-page ownership metadata is not yet as detailed as Java.
- RAG uses local token vectors and relation extraction; Java's SQLite/vector embedding path is
  richer and should be upgraded with configurable embedding providers.
- LSP diagnostics currently focus on Go command diagnostics and post-write checks need more
  language adapters.
- WeChat media download/decrypt/upload and daemon supervisor are not complete.
- Full Lanterna-style pane layout is not ported yet; the default Go UI now uses
  Bubble Tea alternate-screen rendering.
