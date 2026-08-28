package skill

const builtinWebAccess = `---
name: web-access
description: |
  All web access must go through this skill: search, fetch, login-required browsing,
  dynamic pages, social sites and browser fallback decisions.
version: "1.0.0"
author: Zcoder
tags: [web, browser, fetch]
---

# Web Access

Use deterministic local tools first when the task is about the current repository.
Use ` + "`web_search`" + ` for fresh or time-sensitive public information and ` + "`web_fetch`" + `
for direct URL extraction. If a page is dynamic, login-gated, or returns an empty
article body, fall back to browser MCP tools such as chrome-devtools.

Do not claim current facts from memory when the user asks for latest, today,
current price, laws, product versions, schedules or breaking news.

For sites that often block static fetching, prefer browser tools after one clear
fetch failure. Keep source URLs in the final answer when web data shaped the result.
`
