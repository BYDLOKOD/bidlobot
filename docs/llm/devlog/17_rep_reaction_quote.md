---
id: devlog-17
kind: log
written: 2026-11-17
---

# The rep report says what was liked, and links it

The reaction report used to name only the credited author and the new
balances. A reader could not tell what the like was for, and there was
no way to jump to the message that earned it. Each block now quotes the
reacted message and that quote is the permalink of the message.

## What changed

- `internal/shared/link.go`: `MessageLink(chatID, threadID, messageID)`
  builds `https://t.me/c/<internal-id>[/<topic-id>]/<message-id>` from a
  supergroup id, returning `""` for anything else. The bot never
  resolves a chat username, so the private-chat form is the only one
  available - and it is the form Telegram clients themselves produce for
  a private supergroup.
- `internal/bot/rep_reaction.go`: the author index entry grew a `quote`
  (cleaned message text, 120 runes) and the message's `threadID`.
  `repEvent` carries `quote` + `link`, and the composer renders the
  quote as the link text. Without a quote the author's name carries the
  link. Quotes are HTML-escaped and mention-defused, and link previews
  are disabled for the report.
- `ownerRecorder` took a struct (`ownerRecord`) instead of three
  positional arguments: the reposters now hand over what Telegram
  stored for the bot's message, so a liked TikTok/X/Instagram repost or
  TikTok comment quote is quoted too.
- The batcher splits a batch that outgrows 3800 chars into several
  messages on block boundaries. Twenty reactions inside one 10s window
  were previously able to exceed Telegram's 4096-char limit and be
  dropped whole by the API.
- `internal/shared/link_test.go`,
  `internal/bot/rep_reaction_test.go`: link forms (supergroup, topic,
  General topic, basic group, junk ids), quote cleaning and rune-safe
  truncation, media labels, escaping and U+2060 defusing, the name-link
  fallback, chunking, preview suppression, and the end-to-end path for
  an incoming message, a forum topic message, and a bot repost.
- `docs/llm/59_reputation.md`, `docs/llm/60_architecture.md`.

## Decisions

- **A quote as the link text, not a separate URL line.** One tappable
  line answers both questions ("what was it" and "where is it") without
  spending a second line on a raw URL, and a batch of them stays
  readable.
- **No reply-based jump.** A reply would be nicer UI, but the batcher
  combines several reactions into one message and a reply has exactly
  one parent. Splitting the batch to get replies would undo the whole
  point of batching.
- **A RAM quote, not a stored one.** The Bot API has no "get message by
  id", so the text can only come from the update stream. It inherits the
  index's limits: RAM only, 5000 entries, and nothing before a restart -
  the same rule that already governs author attribution.
- **Escape only `&`, `<`, `>`.** Telegram's HTML parser does not
  understand the numeric entities `html.EscapeString` emits for quotes,
  and chat messages carry apostrophes constantly; `'` and `"` are safe
  as plain characters in a text node.

## Limits

- A reaction on a message sent before the bot started still yields
  nothing, as before.
- A message deleted inside the 10s window keeps its quote in the report.
- The `t.me/c/` link needs membership; anyone in the chat can open it,
  nobody else can.
- No live verification of the permalink form or of the forum-topic
  segment was possible from the test suite: both need one check in a
  real chat after deploy.
