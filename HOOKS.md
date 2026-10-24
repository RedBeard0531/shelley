# Shelley Hooks

You can customize Shelley behavior by placing executable scripts
in `$HOME/.config/shelley/hooks/<name>`.

If a hook fails (non-zero exit, invalid output, etc.) the operation it
belongs to is aborted. The `end-of-turn` hook is the exception: by the
time it fires there is no operation left to abort, so failures are just
logged. A failed `status-bar` hook shows its error in its row.

## Available Hooks

| Hook | Stdin | Stdout |
|---|---|---|
| `system-prompt` | system prompt text | replacement prompt (non-empty) |
| `new-conversation` | JSON | JSON (mutable fields) |
| `chat-message` | JSON | JSON (`message` field) |
| `end-of-turn` | JSON | ignored |
| `status-bar` | JSON | JSON (`body`, `bgcolor`, `color`) |

## Example payloads

The samples below are stdin captured from a real Shelley instance with
`tee`-only hooks installed.

### `system-prompt`

Stdin is the rendered prompt text. The first lines look like:

```
You are a subagent of Shelley, a coding agent. You have been delegated
a specific task by the parent agent.

Key constraints:
- Complete your assigned task thoroughly
- Your final message will be returned to the parent agent as the result
...

Working directory: /home/user/project

Git repository root: /home/user/project

<skills>
...
</skills>
```

Stdout must be the (possibly modified) replacement prompt text. A
non-empty result is required.

### `new-conversation`

```json
{
  "prompt": "hello shelley",
  "model": "predictable",
  "cwd": "",
  "readonly": {
    "conversation_id": "cH3UICU",
    "is_subagent": false,
    "headers": [
      ["Accept", "*/*"],
      ["Content-Length", "49"],
      ["Content-Type", "application/json"],
      ["User-Agent", "curl/8.5.0"],
      ["X-Custom", "demo"],
      ["X-Exedev-Email", "test@example.com"]
    ]
  }
}
```

Stdout (all fields optional; empty stdout = no-op):

```json
{ "prompt": "", "model": "", "cwd": "", "slug": "" }
```

For subagent conversations, `readonly.is_subagent` is `true`,
`readonly.parent_id` is set, and `readonly.headers` is absent.

### `chat-message`

```json
{
  "message": "follow-up question",
  "readonly": {
    "conversation_id": "cH3UICU",
    "model": "predictable",
    "reasoning_level": "high",
    "queued": false,
    "headers": [
      ["Accept", "*/*"],
      ["Content-Length", "54"],
      ["Content-Type", "application/json"],
      ["User-Agent", "curl/8.5.0"],
      ["X-Exedev-Email", "test@example.com"]
    ]
  }
}
```

`reasoning_level` is the conversation override when set; otherwise Shelley materializes the service's configured default. It is empty only when the provider chooses the default dynamically and Shelley cannot know it in advance.

Stdout (empty = no-op):

```json
{ "message": "the rewritten message" }
```

### `end-of-turn`

```json
{
  "type": "end_of_turn",
  "conversation_id": "cMT7MTV",
  "timestamp": "2026-05-27T00:34:31.961478145Z",
  "hostname": "vm.exe.xyz",
  "model": "predictable",
  "conversation_url": "https://vm.exe.xyz/",
  "vm_name": "vm",
  "final_response": "Done."
}
```

Stdout is ignored.

### `status-bar`

Adds a row above the conversation's status bar (above the composer on
phones), e.g. with the git branch. It runs in the conversation's
directory, for each browser tab showing the conversation:

- when the conversation opens;
- after each new message: yours, the agent's, each tool result;
- when you come back to the tab or its window.

It doesn't run for a tab in the background, a draft, an archived
conversation, or a conversation no tab shows. One run at a time per
tab: triggers during a run make one more run after it, and switching
conversations cancels the run. The row keeps the last output until the
next run finishes. A conversation whose directory is gone shows nothing.

It often runs alongside the agent's tools, so Shelley sets
`GIT_OPTIONAL_LOCKS=0`; a `git status` in the hook then can't make the
agent's `git add` fail on `index.lock`. Times out after 2s, so keep it
cheap: `git branch --show-current`, not a `git status` of a big repo.

```json
{
  "conversation_id": "cMT7MTV",
  "slug": "fix-login",
  "cwd": "/home/exedev/myapp",
  "model": "predictable"
}
```

Stdout is one JSON object; every field is optional, and empty stdout
shows nothing. `body` is one line of plain text, with URLs linkified;
`bgcolor` and `color` are CSS colors. A failure (non-zero exit, invalid
JSON, unknown fields, over 4 KiB, a background process left holding
its output) shows the error instead.

```json
{ "body": "main", "bgcolor": "#204a87", "color": "white" }
```
