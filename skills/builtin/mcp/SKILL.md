---
name: mcp
description: Use MCP (Model Context Protocol) servers from bash with `shelley mcp`. Use when the user mentions MCP, asks to add or connect an MCP server, or a task needs an external service that may be reachable through a registered MCP server.
---

MCP servers registered with Shelley aren't model tools: use them by running `shelley mcp` from bash (`shelley mcp -h` for all commands).

```bash
shelley mcp list                    # servers
shelley mcp list SERVER             # its instructions and tools as TypeScript-style signatures
shelley mcp list SERVER.TOOL -schema  # one tool with its JSON schemas
shelley mcp call linear.search_issues query='login bug' limit=5 'states=["open"]'
```

Read a server's instructions and signatures before first calling its tools. `KEY=VALUE` values are taken as is for string parameters and as JSON otherwise, so quote JSON for the shell. Images, audio and binary resources are saved to temporary files (the output says where; look at images with read_image). If the tool reports an error, it is printed to stderr and the exit status is 1. A call that timed out may still have run.

Compose calls like any shell command; keep output small with jq:

```bash
shelley mcp call SERVER.TOOL -json | jq '.structuredContent.items[] | {id, title}'
jq -n --arg q "$query" '{query: $q, limit: 5}' | shelley mcp call SERVER.search -   # arguments as JSON on stdin
for id in 1 2 3; do shelley mcp call SERVER.get_item id="$id"; done
shelley mcp call A.list_rows -json > /tmp/rows.json
jq -c '.structuredContent.rows[] | {title: .name}' /tmp/rows.json |
  while read -r args; do echo "$args" | shelley mcp call B.create_item -; done
```

## Log in

When a command says a server needs you to log in, give the user the link exactly as printed, wait for them to say they've logged in, and retry. Never open or fetch the link yourself: it's for the user's browser, and only they can log in. `shelley mcp auth SERVER` shows the login state and link. Never ask for or invent tokens for servers that log in this way.

## Register

```bash
shelley mcp add NAME -d 'what it is for' https://example.com/mcp   # logs in with OAuth if needed
shelley mcp add NAME -H 'Authorization: Bearer TOKEN' https://example.com/mcp
shelley mcp rm NAME
```

For servers that take an API key, ask the user for it. Shelley only connects to Streamable HTTP servers, not to stdio ones (local commands such as `npx ...`).

## Sessions

Each conversation has its own session with each server, so server state (open pages, selections) lasts across calls; forks and subagents start fresh. `shelley mcp restart SERVER` starts this conversation's session over, losing its state.
