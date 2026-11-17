---
id: reputation
kind: spec
touches:
  - internal/bot/reputation.go
  - internal/bot/rep_reaction.go
  - internal/domain/reputation/
  - internal/shared/link.go
  - internal/storage/reputation_repo.go
written: 2026-08-16
updated: 2026-11-17
---

# Reputation

Durable per-chat reputation economy (commits 334bc94, 04a4bfa). Users
earn/spend balance by praising and roasting each other; balance and
leaderboards are public. Registered in `registerGameRoutes`
(`games.go:107-112`) with the games - the commands share the game
cooldown table.

## Commands (supergroup)

| Command | Cooldown | Action |
|---------|----------|--------|
| `/praise [@user\|reply]` | 8s | actor −1, target +3 (+6 if target is admin) |
| `/roast [@user\|reply]` | 8s | actor −1, target −1 |
| `/rep` | 5s | own balance (italic report) |
| `/reptop` | 5s | top-10 leaderboard |

Target resolution: reply-to or `@username` via the membership store.

## Rules (`reputation_repo.go`)

- Default balance **10** (admins 20). Floors at 0 -
  `ErrInsufficientBalance` (actor) / `ErrTargetInsufficientBalance`
  (target would go negative).
- Self-target rejected (`ErrSelfTarget`).
- **Live membership check before ANY mutation**: `GetChatMember`
  fail-closed - an old replied-to message can outlive a missed leave
  event; a left member is not debited/credited (`isStillMember`).
- Admin flags resolved via `AdminCache` at apply time (a fresh
  promotion counts immediately).
- Persisted in bbolt bucket `reputation`, key `r:<absChat>:<userID>` =
  JSON `{balance}`; lazy-init on first read; leaderboard = chat-prefix
  scan, sort balance desc / userID asc, limit 10.

## UX

Randomized template lines (Russian) + italic balance report; top-10
list renders display names with numeric-id fallback (inert - no `@`,
no `tg://user?id=`, per `command-output-no-third-party-ping`).
Errors: "его уже нет в чате" / "себе нельзя" / "у тебя ничего не
осталось".

## Reaction report (`rep_reaction.go`)

Reactions are the second rep path and need no command:

| Reaction | Kind | Delta |
|----------|------|-------|
| 👍 | praise | +3 (+6 when the message author is admin) |
| 👎 / 🤡 | roast | -1 |

- **Author attribution.** `message_reaction` carries no message author,
  so the bot indexes `(chat, messageID) -> {author, quote, thread}` from
  the message stream and from its own reposts (`RecordOwner`). Bounded
  at 5000 entries, RAM only, FIFO eviction. A reaction whose message is
  not indexed (sent before a restart, or evicted) is dropped - the Bot
  API cannot read a message back by id.
- **Batching.** One combined message per chat per 10s window; several
  reactions in the window become several blocks. A burst that outgrows
  `repMessageMaxChars` (3800) is split on block boundaries into several
  messages: truncating would drop a balance change the actor already
  paid for.
- **What was liked.** Each block names the credited author, quotes the
  reacted message, and makes that quote the permalink - tapping it jumps
  to the message. Without a quote (a text-less repost, a media message
  with no caption) the author's name carries the link instead, so the
  block always leads somewhere.
- **Quote source and shape.** Message text or caption, whitespace runs
  collapsed, cut at `repQuoteMaxRunes` (120) runes with an ellipsis. A
  message with no text contributes a media label: "фото", "видео",
  "гифка", "кружок", "голосовое", "аудио", "стикер", "опрос", "файл".
  Bot reposts are quoted from what Telegram stored for the bot's message
  (`Caption` after parsing), not from the HTML the bot built.
- **Untrusted text.** A quote is somebody else's message: `<`, `>`, `&`
  are escaped, and every `@` gets a U+2060 WORD JOINER so quoting a
  message can never ping the people it names
  (`command-output-no-third-party-ping`). Link previews are disabled on
  the report.

### Permalink form

`https://t.me/c/<internal-id>/<message-id>`, where the internal id is
the supergroup id with the `-100` prefix stripped (`shared.MessageLink`).
A forum topic inserts its id: `/c/<internal-id>/<topic-id>/<message-id>`
(topic 1 is the General topic and omits the segment). These links
resolve for chat members only - a non-member gets "not found" - which is
the intended behaviour, since the report is posted back into the same
chat. A basic group has no `c/` link and such a block renders without
one.

### Limits

- A message the bot never saw yields no rep at all (see attribution).
- A message deleted inside the 10s window keeps its quote in the report:
  the bot re-publishes the text it already holds instead of showing
  "message deleted" the way a reply would.
- The quote is a same-chat republication of content the audience already
  saw; it reaches no new reader. Content requirement is unchanged: the
  bot needs message content (privacy off), exactly as it did for author
  attribution.

## Privacy

Needs message content (reply/`@user` targeting) - privacy OFF. Note:
`/roast` `/praise` historically were curated-template games; since
commit 334bc94 they are reputation transfers (see
[25_games.md](25_games.md) - the game table no longer lists them).
