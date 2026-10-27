---
id: devlog-16
kind: log
written: 2026-10-27
---

# Birthday greetings from the Telegram profile

Implemented the daily birthday greeting feature. The source is
`ChatFullInfo.birthdate` read through `getChat`; the bot keeps no
bot-side profile and stores no date of birth.

## What changed

- `internal/domain/birthday/`: the daily service, the 10:00 Moscow
  schedule, the 29 February rule, the combined message, the HTML
  mention, live membership check, and the 48-hour marker lifecycle.
- `internal/storage/birthday_repo.go` plus `birthday_greeted` bucket,
  `bg:` keys, and the `MigrateChatID` rekey.
- `internal/bot/app.go`: `AttachBirthdays` and the tracked worker.
- `cmd/bidlobot/`: `BIRTHDAY_ENABLED` (default false) and wiring.
- `internal/text/messages.go`: one-person and several-person greetings.
- `internal/shared/format.go`, `docs/llm/30_stats.md`,
  `docs/llm/50_telegram.md`, `docs/llm/60_architecture.md`: the
  birthday mention is a second sanctioned notifying output.
- `docs/llm/66_birthdays.md`, the index, PRD scope, and ROADMAP E12.

## Privacy decision

`displayName` refuses `FirstName` for `KnownVia == SourceImport`: that
field is the exporting account's private address-book label, not the
member's own profile name. Such a member is mentioned as "именинник"
until a live event supplies a real name. This mirrors the stats display
resolver.

## Verification

- `go build ./...` clean.
- `go vet ./...` clean; `gofmt -l internal/ cmd/` empty.
- `go test ./...`: 22 packages ok, 3 no tests.
- `internal/domain/birthday/`: matching, leap day, timezone, duplicate,
  missing date, local/live left member, several birthdays, HTML
  escaping, import-name privacy, send failure and marker clearing,
  left-bot chat, `nextRun`.
- `internal/storage/`: mark/is/clear, prune, migrate.

## Not done

- Not deployed. `BIRTHDAY_ENABLED` defaults to false.
- The first live run still has to confirm that `getChat` returns
  `birthdate` for a member of a common group who never started the bot.
- The greeting is public by owner choice; hiding the date in the
  Telegram profile is the only per-user control.
