---
id: summarize
kind: spec
---

# Chat summarization (`/summarize`)

Admin-only, opt-in. An LLM (Zhipu GLM) condenses the recent chat into a
short Russian digest posted publicly in the group. Added 2026-05-15 at
the owner's explicit request; reconciled against the dropped "YouTube
Summary" rationale in [10_scope.md](10_scope.md).

## Hard constraint that shapes everything

The Telegram **Bot API cannot read history**: no `getChatHistory`,
consumed `getUpdates` are discarded within 24h, never refetchable
(verified against core.telegram.org/bots/api). A bot can only summarize
what it *kept* as messages streamed by. So "last N" means **the last N
this process heard since it started** - not retroactive history. This is
inherent, not a TODO.

## Storage model: RAM-only ring buffer

`internal/domain/summarize.Buffer`. Per-chat ring, **never persisted**:
not in bbolt, not in `bidlobot-backup`, gone on restart by design. Raw
member text never touches disk. Bounds (defaults): 2000 msgs/chat, 4 MiB
text/chat, 256 distinct chats (LRU-evicted), oldest-evicted on overflow.

Fed by a passive middleware (`summarizeRecorder`) mirroring
`monthstats.ExtractSample`'s predicate exactly - non-bot, not anonymous
admin, no `sender_chat`, has text/caption - and additionally skipping
the bot's own `/`-commands so a `/summarize` never pollutes the next
transcript. Wired only when the feature is configured.

## Invocation & authorization

- Public supergroup command `/summarize [N]`, alias `/итог [N]`. The
  alias is matched by `textCommandPredicate`, **not** `th.CommandEqual`:
  telego's CommandEqual compiles to an ASCII-only RE2 `\w` regex that
  never matches Cyrillic. Typed-only (setMyCommands also rejects
  non-ASCII names, so it stays out of the slash menu). `N` defaults to
  200, parsed leniently (first positive int after the command, `@bot`
  token skipped), clamped to `[1, 4000]` and to the live window size.
- Admin-only via `shared.AdminCache` (getChatAdministrators, 60s TTL,
  re-checked every call) - the project standard. Non-admins get **no
  reply** (anti-spam). Anonymous admins are told to disable anonymous
  mode (no `From.ID` to match - same limit the DM moderation surface
  documents).
- Cost controls on a paid API: per-admin 90s cooldown via `gateMsg`
  (silent drop); per-chat single-flight (a second `/summarize` while one
  runs replies "уже собираю"); and a **process-wide ceiling** across all
  chats/admins (`GlobalAllow`, default 40 calls / rolling hour) - the
  single-flight is per-chat only, so without the global cap an admin in
  many chats (or a compromised account) is an unbounded financial DoS.
  Checked after the per-chat slot so a busy chat never burns global
  budget.
- The expensive call runs in a tracked background goroutine
  (`App.InFlight()` + app context, like the cleanup executor): a
  placeholder message is posted, then `EditMessageText`-swapped in place
  for the result - one public artifact, never two; SIGTERM cancels it
  cleanly inside the shutdown budget.

## Token budget & provider

- GLM-5 window is 200K (input+output combined). Input budget default
  **120K** est. tokens (config `Config.InputBudgetTokens`), with margin
  for output and for the estimator under-counting code-dense windows.
  The buffer cap and `N` bind first.
- Token estimate is **rune-based** (~1 token / 2 runes). The
  load-bearing point is rune- not byte-based (a byte chars/4 heuristic
  would mis-budget Russian badly). It is NOT a guaranteed upper bound:
  code/URLs/snake_case can exceed 0.5 token/rune, so a code-dense window
  can still under-count - hence the budget margin and the provider's own
  context-length 400 as the hard backstop -> mapped to "lower N".
- Provider: `internal/shared/glm`, OpenAI-compatible
  `POST {base}/chat/completions`, `Authorization: Bearer {id}.{secret}`
  (no JWT - verified docs.bigmodel.cn, May 2026). Base/model
  configurable; defaults `https://open.bigmodel.cn/api/paas/v4` /
  `glm-5`. Retry: 429 once (Retry-After honored), 5xx bounded ladder;
  retry tighter than Telegram's because each call is expensive.

## Output

Plain text only (no ParseMode): the model is untrusted and the result
is posted publicly - markup/entities from it must not be interpreted,
and this also avoids the double-escape footgun. Plain text alone is not
enough, though: Telegram still auto-links a bare `@username`, so a
member could steer the summary into mass-pinging the chat. The
transcript therefore feeds plain names (no leading `@`) and the final
body+footer is run through `defuseMentions` (a U+2060 WORD JOINER after
every `@`, invisible, breaks the mention parse). Russian, sectioned
(Кратко / Темы / Решения / Ссылки / Вопросы, empties omitted), capped
~2800 chars by the prompt and hard-truncated at 3500 runes. Footer
discloses provenance: `- итог M сообщений (HH:MM-HH:MM UTC),
сгенерировано внешним AI (GLM) по запросу @admin`.

## Error taxonomy (user-facing, Russian, no swallowing)

| Cause | glm sentinel | Admin sees |
|-------|--------------|------------|
| no/invalid key, 401/403 | `ErrAuth` | ключ GLM отклонён |
| **no funds (code 1113)** | `ErrQuota` | нет средств, пополните баланс |
| throttled 429 | `ErrRateLimited` | перегружен, позже |
| input too large | `ErrContextTooLong` | уменьшите N |
| ctx deadline | `ErrTimeout` | не успел, меньшее N |
| 5xx / empty / other | `ErrProvider`/`ErrEmpty` | временная ошибка |
| empty window | `ErrNoMessages` | пока нечего суммировать |

`ErrQuota` is distinct on purpose: bigmodel.cn returns out-of-funds as
**HTTP 429**, indistinguishable from real throttling by status; treating
it as transient ("try later") or retrying it would both be wrong. It is
terminal and actionable - the operator must top up.

## Privacy

`/summarize` sends recent member message text to an **external provider
(Zhipu, China)** over TLS. This is a deliberate, owner-approved tradeoff
for one admin-only feature, mitigated by: RAM-only (no disk/backup),
opt-in (off without the key), explicit in-message provenance footer,
key never logged. Operators should disclose this to their community.

## Documented limitations (v1, not silent)

1. **Forward-only / restart-volatile.** Only messages heard since
   process start; redeploy/crash empties the window.
2. **Edits & deletions not tracked.** The bot does not subscribe to
   `edited_message` (consistent with the rest of the codebase, e.g. the
   YouTube sanitizer); deleted messages stay in the window until
   evicted. `Buffer.Update` exists for a future opt-in.
3. **Anonymous admins cannot invoke** (no identifiable `From.ID`).
4. **Times are UTC** in the transcript and footer (no per-chat tz).
5. **Requires a funded provider account** - an empty balance yields the
   `ErrQuota` path; the integration is otherwise verified end-to-end.

## Config

`GLM_API_KEY` (empty = feature off, bot still starts), `GLM_BASE_URL`,
`GLM_MODEL` - see [70_deployment.md](70_deployment.md). Key lives only
in the gitignored env file; rotate if ever exposed (env-var design makes
rotation a one-line change, no code touch).
