---
id: handoff
kind: guide
---

# Handoff: next session action plan

Last updated: 2026-05-15, after the monthly-stats / mini-games /
YouTube-si= / DM-import session. Branch `feat/monthly-stats-dm-import-games-yt`,
**not yet merged to master, not yet deployed.** Base was `be90a00`.

## Current state

`go build ./...` green; `go test ./...` green; `go test -race` green on
the concurrent packages (bot, monthstats, histimport, storage, shared).
An opus critic reviewed the whole branch: its one BLOCKER and three
should-fix items are resolved (commit `87543c9`); S4 accepted as a
documented tradeoff (below). Image ships `bidlobot` + `bidlobot-backup`
+ `bidlobot-probe` (the `bidlobot-import` CLI was removed).

Added this session:

- **Monthly nominations** (`internal/domain/monthstats`): per-calendar-
  month report with the user's own chat-export.org titles verbatim
  (самый срущий автор / по длине сообщения / самое длинное сообщение /
  самый кодирующий / емоджинутый / тегающий / говорящие с ботами /
  самый курсористый тип). `/stats month [YYYY-MM]` + `/stats months`
  (public read-only + DM). Fed by the live handler AND the importer;
  idempotent. Past months memoized, auto-invalidated on re-import.
- **DM history import** (`internal/histimport` + `internal/bot/
  dm_console_import.go`): `/import` in DM, then send the export
  (`.json`/`.gz`/`.zip`); in-process download + decompress + parse +
  pre-commit confirm + abortable background ingest. Idempotent
  (message-id watermark + atomic `ApplyImport`). No CLI, no bot stop.
- **7 mini-games**: `/poll` (native), `/8ball`, `/roast` `/praise`,
  `/guess`, `/hangman`, `/duel`, `/trivia`.
- **YouTube si= sanitizer**: repost-then-delete (failed repost keeps the
  original), host-scoped, media by file_id, reply fallback.
- Stats display now `Name (@handle)` via `shared.UserDisplayFull`.

## Known follow-ups / limitations (documented, not silent)

1. **Import-only users have no @handle.** The Telegram Desktop export
   carries display names, not usernames. Imported-but-never-live users
   show just the name until they write live (then the @handle fills in;
   `SourceMessage` overwrites `SourceImport`). Inherent Telegram limit.
2. **`monthBuffer` shutdown tail (critic S4, accepted).** Like the
   pre-existing `statsBuffer`, the monthly buffer's Run goroutine is not
   in `app.InFlight()`; a SIGTERM can lose up to ~60s of unflushed live
   monthly counts. Same tradeoff the lifetime stats spec already
   sanctions ("crash = loss of up to 60s, acceptable"), and monthly is
   import-recoverable. Revisit only if it proves to matter.
3. **YouTube `edited_message` not covered (v1).** A clean link edited to
   add `si=` later is not sanitized. Documented in the file header.
4. **N1**: a malicious 20 MB archive can spill ~1 GiB to a temp file
   before parse (admin-triggered, trust-bounded). **N2**: `MessageTime`
   treats the export `date` string as UTC when `date_unixtime` is
   absent (legacy-verbatim; modern exports always carry unixtime).
5. Pre-existing: fresh-deploy reconcile, `resolveUsername` stub, ~200ms
   public-/ban visibility window (unchanged this session).

## Immediate next steps

1. **Operator manual verification in the test chat + DM** (Claude
   cannot drive Telegram - see below). Then merge the branch and deploy.
2. The BYDLOKOD backfill: add the bot to chat `1009000003` as admin
   FIRST, then DM `/import` and send `result.json` gzipped (~4 MB; raw
   31 MB exceeds Telegram's 20 MB bot-download cap). See memory
   `bydlokod-import-workflow`.
3. Optional: tighten N1 cap; cover `edited_message` for the sanitizer.

## Manual verification (operator must run; Claude cannot click Telegram)

Automated tests + critic are done. The Telegram-interactive surface is
NOT machine-verifiable here (no bot token, can't tap buttons, can't be
the human sender). In `@testovaya...` + a DM with the deployed test bot:

1. `/stats month` and `/stats months` in the group render correctly.
2. DM `/import` -> send a small gzipped export -> confirm preview ->
   tap Load -> report; re-send the same file -> "уже загружены"
   (idempotent, counts unchanged); `/stats month` reflects it.
3. Oversize raw `.json` (>20 MB) in DM -> rejected with the zip hint,
   no download attempted.
4. Each new game once: `/poll Q | a | b`, `/8ball x`, `/roast`,
   `/guess` then `/guess 50`, `/hangman` then a letter, `/duel @u`,
   `/trivia`. Cooldowns hold under repeat.
5. Post a `youtu.be/X?si=Y` link -> deleted + reposted attributed,
   link sans `si=`; post a Spotify `?si=` -> untouched.
6. `/stats` shows `Name (@handle)` for live users.

## Read before starting

- `docs/llm/30_stats.md` (monthly engine, legacy semantics, display)
- `docs/llm/35_history_import.md` (DM import model)
- `docs/llm/devlog/04_monthly_stats_games_yt_dm_import.md`
- memory: `bydlokod-import-workflow`, `project_direction`

## Anti-patterns

1. Do NOT re-sanitize the user's chat-export.org nomination titles -
   the crude register is deliberate chat culture.
2. Do NOT reintroduce a standalone import CLI or "stop the bot" import.
3. Do NOT read `displayFor`/`UserDisplayFull` output and re-`EscapeHTML`
   it (double-escape).
4. Monthly counters are additive: never bypass the message-id watermark
   / `ApplyImport` atomicity; never RMW `MonthState` outside the
   provided atomic methods.
5. All public sends through the rate-limited `tgclient` wrapper, never
   the raw `*telego.Bot`.
