---
id: summarize
kind: spec
touches:
  - internal/bot/summarize.go
  - internal/bot/deferred.go
  - internal/domain/summarize/
  - internal/storage/deferred_repo.go
written: 2026-05-15
updated: 2026-05-16
---

# Chat summarization (`/summarize`)

Admin-only, **always on**. An LLM (DeepSeek V4 Flash via the OMP/Pi
CLI) condenses the recent chat into a short Russian digest posted
publicly in the group. Migrated 2026-07 from the Zhipu GLM HTTP
provider to the local `omp` CLI (commits caa8f55, 848ad21, 874b198).
No opt-in toggle: the feature is part of the image; a missing `omp`
binary is a **startup failure** (`exec.LookPath` in main.go, `os.Exit(1)`).

## Hard constraint that shapes everything

The Telegram **Bot API cannot read history**: no `getChatHistory`,
consumed `getUpdates` are discarded within 24h, never refetchable. A
bot can only summarize what it *kept* as messages streamed by. So
"last N" means **the last N this process heard since it started** -
not retroactive history. This is inherent, not a TODO. Combined with
the privacy model, the recorder fills only when the bot sees message
content (privacy OFF).

## Storage model: RAM-only ring buffer

`internal/domain/summarize.Buffer`. Per-chat ring, **never persisted**:
not in bbolt, not in backups, gone on restart by design. Raw member
text never touches disk. Bounds: 2000 msgs/chat, 4 MiB text/chat, 256
distinct chats (LRU-evicted), oldest-evicted on overflow.

Fed by a passive middleware (`summarizeRecorder`) mirroring the stats
predicate exactly (non-bot, not anonymous admin, no `sender_chat`, has
text/caption), additionally skipping the bot's own `/`-commands so a
`/summarize` never pollutes the next transcript. Registered among the
passive observers BEFORE the sanitizer/repost middlewares so it sees
the original human message.

## Invocation & authorization

- Public supergroup command `/summarize [N] [questions...]`, alias
  `/итог`. The alias is matched by `textCommandPredicate`, not
  `th.CommandEqual` (ASCII-only RE2 - never matches Cyrillic).
  Typed-only (setMyCommands rejects non-ASCII names). `N` defaults to
  200 (first token after command/`@bot` parsed as int; if not a
  number, everything is treated as questions), clamped to [1, 4000]
  and to the live window size. Everything after `N` (or after the
  command if no `N`) is the **questions text**, passed to the LLM as
  additional instructions after a `---` separator; sanitized via
  `sanitizeLine` (no format injection).
- Admin-only via `shared.AdminCache` (getChatAdministrators, 60s TTL,
  re-checked every call). Non-admins get **no reply** (anti-spam).
  Anonymous admins are told to disable anonymous mode.
- Cost controls: per-admin 30s cooldown (`summarizeCooldown`, silent
  drop); **response cache** (RAM-only, 10-minute TTL) keyed by
  `(chatID, lastMsgID, N, questionsHash)`; per-chat single-flight
  (second call replies "уже собираю"); **process-wide ceiling 40
  calls / rolling hour** across all chats/admins (`GlobalAllow`) - a
  compromised admin is not an unbounded financial DoS. The expensive
  call runs in a tracked background goroutine (`App.inFlight` + app
  context): a placeholder message is posted, then
  `EditMessageText`-swapped in place for the result; SIGTERM cancels
  it inside the shutdown budget.

## Provider: OMP/Pi CLI

`internal/domain/summarize/pi_runner.go` runs the `omp` binary
(`PI_BINARY`, default `omp`; `PI_MODEL` default
`deepseek/deepseek-v4-flash`) with `--mode json --no-session
--no-tools --no-lsp --no-extensions --no-skills --no-rules
--thinking=minimal -p --system-prompt <sp> --model <model>`. The
transcript is passed as an anonymous memfd (`unix.MemfdCreate`) via fd
3 - nothing on disk, no temp file. Output is parsed as NDJSON:
`message_end` events, cost from `message.usage.cost.total`. stderr is
discarded (may carry provider details). **Memory bound**: per-event
scanner cap 4 MiB (`maxOMPJSONEventBytes`) - a code-dense transcript
cannot OOM the process.

The provider credential (`DEEPSEEK_API_KEY`) is read by the `omp` CLI
from its own environment - the Go binary never sees, parses, or logs
it. Compose forwards it from the host env ([70_deployment.md](70_deployment.md)).

## Token budget

Input budget 120K estimated tokens (rune/2 estimate - deliberately
rune- not byte-based; a bytes/4 heuristic mis-budgets Russian badly),
max output 2048, per-call timeout 180s. NOT a guaranteed upper bound:
code/URLs can exceed 0.5 token/rune, so the provider's own context
limit is the hard backstop (mapped to "lower N").

## Weighted digest with cost (848ad21)

The system prompt (`prompt.go`) instructs relevance-weighted
selection: threads contributing <5% of the transcript are omitted
unless they carry a decision/action/high-impact fact; 2-4 paragraphs
<1800 chars; questions answered after a `---` separator. The footer
discloses provenance + price:

```
итог M сообщений (HH:MM-HH:MM МСК), сгенерировано DeepSeek V4 Flash
via Pi по запросу @X
расчетная стоимость: $Y
```

Cost is 4-decimal from provider usage metadata; cached summaries keep
their original generation cost.

## Output

Plain text only (no ParseMode): the model is untrusted and the result
is posted publicly - markup/entities from it must not be interpreted.
Telegram still auto-links a bare `@username`, so the transcript feeds
plain names (no leading `@`) and the final body+footer is run through
`defuseMentions` (U+2060 WORD JOINER after every `@`, invisible,
breaks the mention parse). Russian, sectioned, hard-truncated at 3500
runes.

## Error taxonomy (user-facing, Russian)

| Cause | Sentinel | Admin sees |
|-------|----------|------------|
| provider/LLM failure | `ErrProviderFailure` | временная ошибка |
| ctx deadline | `ErrTimeout` | не успел, меньшее N |
| empty window | `ErrNoMessages` | пока нечего суммировать |
| already running (single-flight) | `ErrBusy` | уже собираю |
| not configured (unwired test app) | `MsgSummarizeNotConfigured` | не настроено |

The old GLM sentinels (`ErrAuth`/`ErrQuota`/`ErrRateLimited`/
`ErrContextTooLong` and the 1113/Coding-Plan lore) are gone with the
provider. `ErrSummarizeAuth/Quota/RateLimited/TooLong` strings remain
in `text/messages.go` but are no longer produced.

## Deferred retry

On provider failure the job is persisted to the **per-user deferred
queue** (`deferred_jobs`, type `summarize`, payload
`SummarizePayload{N, Questions, PlaceholderID, Requester}`) - the
placeholder stays up. The requester runs `/flush` to retry
(`retrySummarize` re-runs the summary and edits the placeholder with a
25s edit timeout). Success deletes the job. See
[60_architecture.md](60_architecture.md) "Deferred queue".

## Privacy

`/summarize` sends recent member message text to an external provider
(DeepSeek) over TLS via the omp CLI. Deliberate, owner-approved
tradeoff for one admin-only feature, mitigated by: RAM-only window (no
disk/backup), explicit in-message provenance + cost footer, key never
seen/logged by the bot, memfd transcript (no temp file). Operators
should disclose this to their community. The recorder needs privacy
OFF to see message content.

## Documented limitations (v1)

1. **Forward-only / restart-volatile.** Only messages heard since
   process start; redeploy/crash empties the window.
2. **Edits & deletions not tracked.** No `edited_message` subscription;
   deleted messages stay in the window until evicted.
3. **Anonymous admins cannot invoke** (no identifiable `From.ID`).
4. **Times are MSK** in the transcript and footer (no per-chat tz).
5. **Process-wide budget** (40/h) can throttle legit multi-chat use.
