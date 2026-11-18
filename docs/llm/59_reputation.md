---
id: reputation
kind: spec
touches:
  - internal/bot/reputation.go
  - internal/bot/rep_reaction.go
  - internal/bot/games_quip.go
  - internal/domain/reputation/
  - internal/shared/link.go
  - internal/storage/reputation_repo.go
written: 2026-08-16
updated: 2026-11-18
---

# Reputation

Per-chat wallet and title score (REP-1..5). The wallet is spendable
reputation; the title score is a separate social standing that no
transfer, purchase, rent or game result may change. A registered user
starts with wallet 10 and score 0, regardless of Telegram administrator
status.

## Model

`reputation.Account{ChatID, UserID, Available, Reserved, Score}`.
`Available` is spendable, `Reserved` is held for an accepted commitment,
`Score` is the title score. Both wallet values and the score are
nonnegative.

| Title | Threshold | Unlocked activity |
|-------|-----------|-------------------|
| Участник | 0 | крутить автомат, вступать в инциденты, торговать, арендовать и выращивать |
| Дежурный | 12 | открывать инцидент |
| Мастер | 36 | сдавать свои слоты производства |
| Хранитель прода | 72 | просить дополнительную подсказку в инциденте |

`TitleForScore` returns the highest reached title; `NextTitle` returns
the next threshold and whether one exists. Four valid praises take a new
player to the first threshold.

## Commands (supergroup)

| Command | Cooldown | Action |
|---------|----------|--------|
| `/praise [help\|explanation\|review] [@user\|reply]` | 8s | actor wallet −1; target wallet +3 and score +3 |
| `/roast [@user\|reply]` | 8s | actor wallet −1; target removes up to one unreserved wallet unit and up to one score point |
| `/rep` | 5s | own available, reserved, score, title and distance to the next title |
| `/reptop` | 5s | ten current scored members by title score |

Target resolution: reply-to or `@username` via the membership store.
The optional reason token selects praise wording only; it never changes
the amount and is never inferred from the quoted message.

## Settlement (`reputation_repo.go`)

One `Settle` call runs in one bbolt write transaction:

1. Deduplicate the source event by actor, source kind and source message
   id. Re-adding the same reaction is a no-op, not a second settlement.
2. Enforce one settlement per unordered member pair per chat per UTC day.
3. Enforce three outgoing settlements per sender per UTC day, shared by
   slash and reaction inputs.
4. Debit the actor one wallet unit. Insufficient spendable funds fail
   before any write.
5. Praise credits the target +3 wallet and +3 score. Roast removes at
   most one `Available` unit (never `Reserved`) and at most one score
   point; a zero wallet does not block the score change.
6. Commit pair, sender and source markers with the wallet writes.

Administrators receive exactly the same deltas. There is no
administrator branch in storage or in the handlers.

Validation errors are `ErrSelfTarget`, `ErrInsufficientBalance`,
`ErrPairLimit`, `ErrSenderLimit`, `ErrDuplicateSource`,
`ErrInsufficientReserve` and `ErrInvalidAmount`. A failed settlement
does not consume a limit or create a source marker.

Day-scoped counters are pruned once per chat when the UTC day changes;
source deduplication markers are retained so an old reaction cannot
settle again.

## Shared wallet API

Other domains settle inside their own bbolt transaction and never write
wallet fields directly:

```go
ReputationWallet(tx *bolt.Tx, chatID, userID int64) (reputation.Account, error)
AdjustReputationWallet(tx *bolt.Tx, chatID, userID int64, available, reserved int) error
```

The read returns defaults for an absent record and never writes. The
adjustment takes signed deltas, normalizes the chat id with
`storage.AbsChatID`, rejects a negative `Available` or `Reserved`, and
never changes `Score`. Reservation capture is therefore
`Adjust(..., -n, +n)`; spending a reservation is the reverse. No game or
farm module may call a title-score setter.

## Storage and migration

Bucket `reputation`, key `r:<absChat>:<userID>`. The current value is
JSON `{available,reserved,score}`.

Legacy v1 stored `{balance}`. Migration seeds `Available` and `Score`
from that balance exactly once and never invents activity history; an
absent legacy row takes the new-member defaults. `meta:v=2` is committed
in the same transaction as the rewritten rows.

- `ReputationMigrationNeeded(db)` is a read-only startup probe: it tells
  the caller whether a pre-cutover backup is required without creating
  one on every boot.
- `MigrateReputation(db, backup)` writes a consistent snapshot when
  `backup` is non-nil and then performs the migration. A second call is a
  no-op and cannot double a balance.
- First settlement or wallet adjustment also migrates lazily, so an
  unnoticed v1 database cannot be settled with a zero wallet.

Operational order is: stop the old writer, back up, run the new binary.
Do not boot an old binary against a database after new settlements: it
would read `{balance:0}` and write the old shape. Roll back only to a
consistent backup after reconciling post-cutover activity.

Chat-id migration must rekey `r:`, `rp:`, `ro:` and `rs:` keys and the
`rd:` day marker. `migrateReputationChat(tx, oldAbs, newAbs)` performs
the in-transaction rekey; the destination row wins, so a move cannot
double a wallet.

## `/rep` and `/reptop`

`/rep` shows available, reserved, score, title and distance to the next
title, or states that the highest title is reached. `/reptop` reads only
title scores, requires positive score, filters to members observed in
the chat (a left/kicked record is not current), sorts score descending
then user ID ascending, and caps at ten. A preformatted text table shows
place, name, score and title, without mentions or per-row explanations.
Reads use observed membership; writes use live Telegram verification.

## Reaction report (`rep_reaction.go`)

Reactions are the second social input and share the same limits and
settlement path:

| Reaction | Kind | Delta |
|----------|------|-------|
| 👍 | praise | actor −1; author +3 wallet and +3 score |
| 👎 / 🤡 | roast | actor −1; author up to −1 wallet and −1 score |

- **Author attribution.** `message_reaction` carries no message author,
  so the bot indexes `(chat, messageID) -> {author, quote, thread}` from
  the message stream and from its own reposts (`RecordOwner`). Bounded
  at 5000 entries, RAM only, FIFO eviction. A reaction whose message is
  not indexed (sent before a restart, or evicted) is dropped - the Bot
  API cannot read a message back by id.
- **Live membership.** Before settlement the bot verifies the reactor and
  the author through `GetChatMember`; a missing verifier or a failed
  lookup drops the reaction rather than settling on stale membership.
- **Deduplication.** One source reaction per actor and message can
  settle. Removing the reaction does not roll it back, and re-adding it
  does not settle again.
- **Limits.** A reaction consumes the same pair and sender limits as
  `/praise` and `/roast`; a dropped reaction produces no chat message.
- **Batching.** One combined message per chat per 10s window; several
  reactions in the window become several blocks. A burst that outgrows
  `repMessageMaxChars` (3800) is split on block boundaries into several
  messages: truncating would drop a balance change the actor already
  paid for.
- **What was liked.** Each block names the credited author, quotes the
  reacted message, and makes that quote the permalink - tapping it jumps
  to the message. Only a record that carries no text at all falls back to
  the author's name as the link text: a bot-sent message with no caption
  (a bare reposted video) or a message kind `repMediaLabel` does not name.
  An incoming photo or sticker is not such a case - it gets a label.
- **Quote source and shape.** Message text or caption, whitespace runs
  collapsed, cut at `repQuoteMaxRunes` (120) runes with an ellipsis. An
  incoming message with no text contributes a media label: "фото",
  "видео", "гифка", "кружок", "голосовое", "аудио", "стикер", "кубик",
  "опрос", "геолокация", "контакт", "игра", "файл". Bot-sent messages
  quote the text the caller supplies for that message - the tweet text
  (`xpost`), the comment text (`tiktok_comment`), the sender's caption of
  a repost, or the raw caption of a TikTok slideshow (`tiktok_photos`,
  whose rich message decodes into a `Message` with neither `Text` nor
  `Caption` in telego v1.8). `RecordOwner` does not invent a media label:
  no text means no quote.
- **Untrusted text.** A quote is somebody else's message: `<`, `>`, `&`
  are escaped, and every `@` gets a U+2060 WORD JOINER so quoting a
  message can never ping the people it names. Link previews are disabled
  on the report.

### Permalink form

`https://t.me/c/<internal-id>/<message-id>`, where the internal id is
the supergroup id with the `-100` prefix stripped (`shared.MessageLink`).
A forum topic inserts its id: `/c/<internal-id>/<topic-id>/<message-id>`
(topic 1 is the General topic and omits the segment), so the recorded
topic must be the one the message was actually sent into - for a bot-sent
message that is what the send call returned, never the topic of the
message that triggered the repost. The id in the link is chat-internal,
and the report is posted back into the same chat, so every expected
reader is already a member; whether a non-member can open the link
depends on the chat's visibility and is not assumed here. `MessageLink`
returns `""` for a chat id without a usable `-100` internal part (legacy
basic groups, private chats, user ids). Channels share the `-100` form,
so the function does not reject them - they are simply never indexed,
the index being supergroup-only, and a channel message therefore never
reaches the composer.

### Limits

- A message the bot never saw yields no rep at all (see attribution).
- A message deleted inside the 10s window keeps its quote in the report:
  the bot re-publishes the text it already holds instead of showing
  "message deleted" the way a reply would.
- The quote is a same-chat republication of content the audience already
  saw; it reaches no new reader. Content requirement is unchanged: the
  bot needs message content (privacy off), exactly as it did for author
  attribution.

## Copy catalogue (`games_quip.go`)

The positive catalogue is the 40-line launch set:

- lines 1-16: generic praise (`praiseGeneric`);
- lines 17-24: help (`praiseHelp`);
- lines 25-32: explanation (`praiseExplanation`);
- lines 33-40: review (`praiseReview`).

A line is a complete public sentence. The deterministic signed receipt
follows it and carries the actual wallet and score changes; the
catalogue never supplies an amount, a consent or an error reason.
Reason-specific lines appear only when the sender writes the reason word
(`help`, `explanation`, `review`, with Russian aliases). The selector
never infers a reason from the quoted message.

Selection avoids repeating a line among the last five public uses of the
same family in the same chat where alternatives exist. The history is
process-local and bounded; rotation does not need durable state.

Roast and refusal copy is deterministic Russian: the receipt names the
actor and target with the signed deltas, and each refusal names its cause
(pair limit, sender limit, duplicate source, empty wallet, departed
member, bot target, self target). Names are inert and never ping.

## Privacy

Needs message content (reply/`@user` targeting) - privacy OFF.
`/praise` and `/roast` are reputation transfers, not mini-games; see
[25_games.md](25_games.md) for the removed legacy game surfaces.
