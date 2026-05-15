---
id: devlog-03
kind: log
---

# 2026-05-15: monthly nominations, mini-games, YouTube si=, DM import

Single session, branch `feat/monthly-stats-dm-import-games-yt`. Four
workstreams, much of it built by parallel sub-agents and integrated on
the foreground.

## Monthly statistics (`internal/domain/monthstats`)

New parallel domain; the lifetime `stats` package is untouched. Buckets
`stats_month` / `stats_month_idx` / `stats_month_state` /
`stats_month_summary`. Reproduces the legacy chat-export.org per-month
report: top by messages/runes (+%), longest message, top by
code/custom_emoji/mention/bot_command, keyword champ, unique users,
users >20. Buffer mirrors `stats.Buffer` (swap/replay/merge) plus a
MonthMeta singleton (userID 0) and `LiveTrackStart` persisted on first
flush. Service owns the seal lifecycle: in-progress month rendered fresh
from the DB+buffer merge; a past month is memoized and auto-invalidated
when `MonthState.UpdatedAt` (advanced by an import) passes the summary's
`BuiltAt`, or on `SummarySchemaVer` bump. Commands `/stats month
[YYYY-MM]` / `/stats months` on the public surface and the DM console
(nil-safe delegation). Legacy semantics pinned and documented in
30_stats.md: Go rune count (not Clojure UTF-16), strict `>20`,
integer-truncated %, deterministic `FirstSeen` tie-break, zero-drop only
on entity/keyword sections.

## History import (`internal/histimport`, CLI removed)

The streaming parser + rollup were extracted into `internal/histimport`
(`Parse`/`Ingest`/`WrapDecompressed`/`FormatDMReport`). The standalone
`cmd/bidlobot-import` binary was deleted - import is now in-process via a
DM `/import` (B2). In-process reuses the bot's open bbolt handle, so the
flock that forced "stop the bot" is gone. Idempotency: per-chat
message-id high-water-mark + `ApplyImport` writing the batch and the
advanced `MonthState` in one bbolt transaction (a crash leaves neither,
so a retry re-skips by the unchanged watermark). The `ts >=
LiveTrackStart` skip applies only when `LiveTrackStart` is non-zero, so
importing into a chat the bot has never tracked (the BYDLOKOD case)
counts everything. `WrapDecompressed` sniffs gzip/zip/raw by magic bytes
with a decompressed-size bomb guard. Docs/Dockerfile/README updated to
the DM model; image now ships only `bidlobot` + `bidlobot-backup` +
`bidlobot-probe`.

## Mini-games

Seven new games, all rate-limited through the wrapped client and
per-user cooldown-gated like dice/quiz, registered in `GamesRegistry` /
`registerGameRoutes`: `/poll` (native `SendPoll`, regular + quiz),
`/8ball`, `/roast` `/praise`, `/guess`, `/hangman`, `/duel`, `/trivia`.
`tgclient.Client` gained rate-limited `SendPoll` (and, for the
sanitizer, media sends). Trivia reuses the quiz leaderboard store; its
narrow callback predicate registers before quiz's broad one.

## YouTube si= sanitizer (`internal/bot/youtube_sanitizer.go`)

Supergroup middleware after the passive stats/membership observers.
Host-scoped to youtube.com / youtu.be / youtube-nocookie.com /
music.youtube.com (Spotify and other `si=` left alone). Deletes the
original and reposts as the bot with a `👤 <name> писал(а):`
attribution and the `si` param stripped; media re-sent by file_id (no
20 MB issue). No delete right -> non-destructive reply with the cleaned
link. Documented gaps: `text_link`-embedded URLs (reply fallback),
edited messages (v1 out of scope), media groups (caption item only).

## Verification

Whole module builds; `go test ./...` and `go test -race` on the
concurrent packages (bot, monthstats, histimport, storage) green.
`internal/histimport` has an end-to-end test (real bbolt: membership +
monthly + double-import idempotency + overlap + cleanup contract).
Sampled real-export fixture at `testdata/chat_export_sample.json` (243
msgs, 7 months) for manual e2e; the 31 MB real export is not committed.
