---
id: devlog-05
kind: devlog
date: 2026-05-15
---

# 05 - Cleanup evidence grading + daily tag->grace->kick lifecycle

Branch `feat/cleanup-rework` (worktree). Built on `feat/summarize-glm`
HEAD `6942061` (the cleanup code lives there, not on master).

## Why

Owner report: `/cleanup 6mo` right after an import listed 6 members as
`id 1250985701 - никогда не писал` - no names, no @handles, and it
flagged people the bot had never observed at all (join-only members from
the export's service events). The export has ~166 days of data and no
reactions, yet the bot asserted "inactive 6 months". The human-confirm
safety was defeated because the human could not identify anyone.

Then a redesign request: make removal humane - a daily public tag, a
grace window, kick only if still silent. Owner answers: public in-chat;
saved by message OR reaction; 3-day grace; candidates drawn only from
the already-inactive set.

## What shipped

**P1 - evidence-graded engine (`internal/domain/cleanup`).**
`PreviewInactive` now splits inactive members into `Candidates`
(observed, then went quiet - real evidence) and `NoEvidence` (never
observed at all - a data gap). Only `Candidates` is actionable;
`NoEvidence` is surfaced for manual review and never auto-kicked. New
`ResolveIdentities` fills Name/@handle via `getChatMember` (bounded;
the export carries neither) and flags left/kicked/admin/bot.
`ThresholdExceedsWindow` drives a loud warning when the requested
period is longer than the data window. DM `/cleanup` preview rewritten:
grouped, named, honest empty states, confirm kicks proven-stale only.

**P2 - daily lifecycle (`internal/domain/gracekick`).** Opt-in
(`CLEANUP_DAILY_ENABLED`, OFF by default). Per administered chat per
day: sweep expired grace tickets (spare anyone who wrote or reacted
after `TaggedAt`, kick the rest via the shared `cleanup.Service`), then
tag a fresh batch of proven-stale members publicly with a grace
deadline. `NoEvidence` is never tagged. Announcement-send failure
persists no ticket (no silent kick of an unwarned member). bbolt repo
`gracekick`; scheduler `App.runDailyCleanup` at `CLEANUP_DAILY_AT` UTC.

## Decisions / tradeoffs

- **Privacy invariant reversed, deliberately.** The daily lifecycle
  posts publicly, overriding "moderation is never visible to chat
  members". Owner-approved (the public @-tag IS the mechanism); scoped
  by opt-in + proven-stale-only + batch cap + grace. Recorded in
  `10_scope.md` / `40_moderation.md`.
- **Grace clearing needs no hot-path hook.** "Reappeared" is read from
  the live membership `LastMessageAt`/`LastReactionAt` at sweep time -
  no coupling into the message handler.
- **Privacy-mode caveat.** Under BotFather privacy ON the bot sees
  reactions and replies-to-itself but not ordinary messages; the tag
  copy asks members to reply or react accordingly.
- **One period parser.** `cleanup.ParsePeriod` is now the single source
  of truth; the DM parser and the daily config both delegate to it.

## Verification

`go build ./...`, `go test ./...` (19 pkg), `go vet`, `gofmt` all
green. New tests: evidence grading, `ThresholdExceedsWindow`,
`ResolveIdentities` (named/left/protected/error/cap), full gracekick
lifecycle (tag proven-only, batch cap, no-retag, sweep save on
message/reaction, kick still-silent, announce-fail persists nothing),
`GraceKickRepo` round-trip + chat isolation.

Not machine-verifiable here: the Telegram-interactive surface (real
public tag, real kick, real reaction-clears-grace). Operator must
exercise it in the test chat - see handoff.
