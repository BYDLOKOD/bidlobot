# Handoff - 2026-10-27 (birthday greetings)

## 1. State (what is true right now)

- **Birthday greetings implemented, not deployed.** `BIRTHDAY_ENABLED`
  defaults to `false`.
- Source: `ChatFullInfo.birthdate` read through `getChat`. The bot
  stores no birth date; it reads the day and month during the daily
  check and discards them.
- Schedule: daily at 10:00 Europe/Moscow (UTC+3), with one startup
  catch-up between 10:00 and 20:00 Moscow if the bot was down at the
  slot.
- Delivery: one public message per chat; several birthdays are listed
  together, more than 10 mentions are split. The mention is
  `<a href="tg://user?id=...">Name</a>` with the name HTML-escaped.
  The birthday member is the only ping - the sanctioned exception to
  the no-third-party-ping rule.
- 29 February is greeted on 28 February in a non-leap year.
- `birthday_greeted` stores `bg:<absChat>:<YYYY-MM-DD>:<userID>` with
  an 8-byte timestamp, 48h TTL. The marker is written before the send
  and cleared on failure; a crash loses one greeting, never duplicates
  it. `MigrateChatID` rekeys the bucket.
- Import-only `FirstName` is refused: it is the exporting account's
  private address-book label. The fallback mention text is
  "именинник".
- Tests: `internal/domain/birthday/service_test.go` and
  `internal/storage/birthday_repo_test.go`.
- **Verified.** `go build ./...` clean; `go vet ./...` clean;
  `gofmt -l internal/ cmd/` empty; `go test ./...` reports 22 packages
  ok and 3 with no tests; `bash docs/llm/validate.sh` reports 0 errors
  (3 warnings for the uncommitted birthday paths, 30 pre-existing
  stale specs).

## 2. Negatives (what does NOT exist / is not proven)

- No live proof that `getChat` returns `birthdate` for a member of a
  common group who never started the bot. The first test-chat run must
  confirm it.
- Telegram may withhold the date under privacy settings; such a member
  is skipped.
- Coverage is observed members only. Bot API has no member
  enumeration; a member who never wrote, reacted, or changed status
  after installation cannot be greeted.
- No opt-out command. Hiding the date in the Telegram profile is the
  only per-user control.
- Not deployed; production still runs the previous build without the
  feature.

## 3. Queue

- Deploy the new master (owner gate).
- Enable `BIRTHDAY_ENABLED=1` in one test chat first. Confirm one live
  greeting and no duplicate after a restart.
- If `getChat` returns no `birthdate` for a known birthday member, stop
  and report: the source is unavailable in that setup.
- Then enable the remaining chats.

## 4. Read order

1. `docs/llm/66_birthdays.md` - source, schedule, markers, privacy,
   limits.
2. `internal/domain/birthday/service.go` - daily service, matching,
   message assembly.
3. `internal/storage/birthday_repo.go` - marker bucket and prune.
4. `docs/llm/60_architecture.md` - bucket, worker, and failure tables.

## 5. Smoke test (run before touching anything)

```sh
go build ./... && go vet ./...
gofmt -l internal/ cmd/
go test ./...
bash docs/llm/validate.sh
```

## 6. Agent errors

- Jev plan review refused the plan three times and released it as an
  OPEN item; no approval was recorded. The implementation proceeded
  under the owner's explicit "приступай к реализации" after the
  release. Do not treat the plan as approved.
