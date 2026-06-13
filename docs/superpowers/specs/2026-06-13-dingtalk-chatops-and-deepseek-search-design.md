# Design: DingTalk ChatOps + DeepSeek-native search for /ask

Date: 2026-06-13
Branch: `feat/dingtalk-chatops-and-deepseek-search`
Status: building (user authorized direct implementation; design-approval gate waived)

## Two goals (from the user)

1. **DingTalk ChatOps** — type commands in a DingTalk group to make Claude Code think,
   modify, and test *this* repository, with results posted back to the group. (Migrate the
   "drive Claude Code on this codebase" loop into the DingTalk group.)
2. **Better /ask search** — route `/ask` and @-engage through DeepSeek's **Anthropic-format
   endpoint with the native `web_search` server tool + thinking**, replacing the brittle
   DuckDuckGo HTML scraper. "目前的搜索太垃圾".

## Research verdicts (verified, not assumed)

- DeepSeek officially exposes an Anthropic-format endpoint: `https://api.deepseek.com/anthropic`,
  models `deepseek-v4-pro` (=opus), `deepseek-v4-flash` (=haiku/sonnet).
  Verbatim from https://api-docs.deepseek.com/guides/anthropic_api:
  - `server_tool_use` ✅ Supported, `web_search_tool_result` ✅ Supported → **native web search works**.
  - `thinking` ✅ Supported (`budget_tokens` ignored), `tools`/`tool_use`/`tool_result` ✅ Fully supported.
  - `mcp_servers` / `mcp_tool_use` / `mcp_tool_result` ❌ **Not supported** → **the football RAG
    must be passed as inline context, NOT via MCP**. Built-in WebSearch/WebFetch are the search path.
  - images / documents / `code_execution` ❌ not supported.
- DingTalk inbound: the current custom-webhook robot is outbound-only. Receiving group messages
  needs an **enterprise internal app robot** + **Stream mode** (websocket, no public URL).
  Official Go SDK exists: `github.com/open-dingtalk/dingtalk-stream-sdk-go` v0.9.1.
  Reply via the inbound `sessionWebhook` (short-lived).

## Environment reality (gates final acceptance)

- `claude` CLI v2.1.177 present and **authenticated** (real Claude available as a subprocess).
- Node v24, Python 3.12, Go 1.23 present. Network egress OK.
- **No DeepSeek API key and no DingTalk AppKey/Secret anywhere** (scrubbed/empty).
  Therefore: everything is built + unit/mock-tested now; live end-to-end runs are gated on the
  user supplying credentials at acceptance (see Acceptance Checklist).

## Plan 2 — DeepSeek-native search for /ask (build first; smaller, reuses code)

### Architecture
- New `internal/llm/anthropic.go`: an Anthropic Messages API client targeting DeepSeek's
  `/anthropic` base URL. Implements the SAME interfaces the bot already depends on
  (`digest.LLM.Generate`, `qa.LLM.Generate`, `qa.ThinkingLLM.GenerateThink`) so it is a drop-in.
  - Enables the server `web_search` tool (`{"type":"web_search_20250305","name":"web_search"}`)
    so the model searches autonomously; reads the final assistant text. `thinking` toggled per call.
  - Graceful: on any error, callers already degrade (digests fall back to data-only; /ask shows an
    error). The OpenAI-format `llm.Client` stays for back-compat.
- Config: `llm.api_format: "anthropic" | "openai"` (default auto: "anthropic" when base_url ends
  with `/anthropic`), `llm.web_search: true`. Construction in `main.go` picks the client.
- `internal/qa/grounding.go`: when the active LLM advertises native search (new
  `NativeSearchLLM` marker interface), **skip** the DuckDuckGo `webSearch`/`verifyFacts` path
  (set `r.Search=""`) but KEEP assembling authoritative ESPN structured data (schedule, standings,
  leaderboards, player season stats, match detail) inline — the model handles web facts via its own
  search. The old DuckDuckGo path remains as the fallback when native search is off.

### Why not the literal "Claude Agent SDK sidecar"
A Node sidecar running `query()` against DeepSeek would deliver the *same* DeepSeek web_search, but
adds a second runtime, IPC, per-question agent spawn latency, and the MCP-can't-carry-RAG constraint
forces inline RAG anyway. Calling DeepSeek's documented Anthropic endpoint directly from Go yields
the identical native search + thinking, reuses the entire existing persona/profile/RAG pipeline, and
is fully unit-testable. The search the user gets is the same search DeepSeek's gateway gives Claude
Code. (The sidecar remains a documented alternative if ever needed.)

## Plan 1 — DingTalk ChatOps → Claude Code

### Components
- `internal/dingbot/dingbot.go`: thin wrapper over the official Stream SDK. Connects with
  AppKey/AppSecret, registers a chatbot handler, normalizes inbound to `InboundMsg`
  {Text, SenderStaffId, SenderNick, ConversationId, ConversationType, SessionWebhook, IsAdmin},
  dispatches to an injected handler, and offers `ReplyText`/`ReplyMarkdown` (sessionWebhook).
  Pure normalization/parse is unit-tested; the live websocket needs creds.
- `internal/chatops/chatops.go`: the command brain.
  - **Admin gate**: only `senderStaffId` in `admin_staff_ids` may run anything that mutates or
    executes. Others get a polite refusal.
  - **Commands** (after stripping the @-mention):
    - `做 <prompt>` / `claude <prompt>` → run Claude Code with edits on an isolated work branch.
    - `问 <prompt>` / `ask <prompt>` → read-only (plan mode), no file changes.
    - `测试` / `test` → `go test ./...` in the worktree, report pass/fail + tail.
    - `状态` / `status` → `git status` + current branch of the worktree.
    - `diff` → `git diff --stat` of the worktree.
    - `pr` → push the work branch and open a PR via `gh` (if configured).
    - `help` → command list.
  - **Isolation**: all work happens in a **dedicated git worktree** (`chatops.worktree_dir`,
    default a sibling dir) on a `chatops/<ts>` branch, so the running service's own source tree is
    never edited mid-flight and master is never touched directly.
  - **Runner** (`internal/chatops/runner.go`): `claude -p <prompt> --output-format stream-json
    --permission-mode <mode> --model <model> --add-dir <worktree> [--allowedTools ...]`, `cmd.Dir`
    = worktree. Streams JSON events, extracts the final `result` text + any tool/error summary, with
    a hard timeout (`max_run_min`). Model defaults to real Claude for code quality; configurable to
    DeepSeek. Pure arg-building + stream-json parsing are unit-tested with fixtures and a fake runner.
  - **UX for long runs**: immediate ack ("🤖 收到，开始干活…"), final result posted when done.
    sessionWebhook expiry risk noted; an OpenAPI `robot/groupMessages/send` fallback is a follow-up.
  - **Guardrails**: command timeout; mutating ops only on the work branch; never auto-push master;
    `pr` is the only path off the branch and it opens a PR (human merges).
- Config: new `dingtalk_bot` section: `enabled`, `app_key`, `app_secret`, `admin_staff_ids`,
  `worktree_dir`, `claude_bin`, `claude_model`, `permission_mode`, `allowed_tools`, `max_run_min`.
- `main.go`: when `dingtalk_bot.enabled`, start the stream client in a goroutine bound to `ctx`.

## Testing strategy
- Plan 2: unit-test the Anthropic client request shape (web_search tool present, thinking toggle,
  message mapping) against a fake HTTP server; unit-test grounding's "skip DuckDuckGo when native
  search" branch. `go test ./...` green.
- Plan 1: unit-test command parsing, admin gate, runner arg construction, and stream-json parsing
  with fixtures + a fake runner/transport. `go test ./...` green.
- Live E2E (gated on creds): documented step-by-step in the Acceptance Checklist.

## Acceptance checklist (what the user must supply to finalize)
1. **DeepSeek key** → set `llm.api_key` (or `LLM_API_KEY`), `llm.base_url:
   https://api.deepseek.com/anthropic`, `llm.api_format: anthropic`, `llm.web_search: true`.
   Verify: `/ask 梅西本赛季进球` triggers a real web search and a grounded answer.
2. **DingTalk enterprise internal app robot** (Stream mode enabled) → `dingtalk_bot.app_key/secret`,
   add own staffId to `admin_staff_ids`, set `enabled: true`.
   Verify: @-bot `help` lists commands; `做 给 README 加一行测试注释` produces a branch + diff reply.
3. Confirm model choice for ChatOps (real Claude vs DeepSeek) via `dingtalk_bot.claude_model`.

## Non-goals (YAGNI)
- No replacement of the digest/recap LLM path (still works via either client).
- No MCP server (unsupported on DeepSeek; inline RAG instead).
- No auto-merge/auto-deploy from chat (PR only; human merges).
