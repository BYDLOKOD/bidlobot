---
id: birthdays
kind: spec
touches:
  - internal/domain/birthday/
  - internal/storage/birthday_repo.go
  - internal/storage/keys.go
  - internal/storage/migrate.go
  - internal/bot/app.go
  - cmd/bidlobot/
  - internal/text/messages.go
written: 2026-10-27
updated: 2026-10-27
---

# Birthday Greetings

Daily public greeting for members whose Telegram profile carries a birth
date. The bot owns no profile and stores no date of birth.

See also: [PRD.md](PRD.md), [50_telegram.md](50_telegram.md),
[60_architecture.md](60_architecture.md), [70_deployment.md](70_deployment.md).

## Requirement

`BIRTHDAY_ENABLED=1` attaches the job. Every day at 10:00 Europe/Moscow
(UTC+3, no daylight saving) the bot lists the chats it is installed in,
reads every observed member's Telegram profile through `getChat`, and
posts one greeting per chat for the members whose
`ChatFullInfo.birthdate` day and month match the Moscow date.

`User` has no birth date; the field lives only in `ChatFullInfo` for
private chats. The bot knows only members it has observed (no member
enumeration), so a member who never wrote, reacted, or changed status
after installation is outside the coverage.

## Source and privacy

- `ChatFullInfo.birthdate` is optional. A missing field means the user
  hid the date or did not set it; the member is skipped.
- The optional year is ignored. The bot never stores, logs, or displays
  the birth date, year, or age.
- `displayName` refuses a `SourceImport` first name: a history-import
  record's `FirstName` is the exporting account's private address-book
  label (see [30_stats.md](30_stats.md)). Such a member is mentioned as
  "именинник" until a live event supplies a real name.
- No opt-out command exists. Hiding the date in the Telegram profile
  stops future greetings; the next daily check sees no `birthdate`.

## Selection and delivery

- The bot skips bots, members with local status `left`/`kicked`, and
  members already marked for the day.
- Before sending, it checks live membership with `getChatMember`; a
  left, banned, or restricted non-member is skipped.
- A 29 February birthday is compared to 28 February in a non-leap year.
- One message per chat. Several birthdays are listed together; more
  than 10 mentions are split into several messages.
- The mention is `<a href="tg://user?id=<id>">Name</a>` with the name
  HTML-escaped. This is a sanctioned exception to the no-third-party-ping
  rule: the ping targets the birthday member, not a bystander.
- Sends go through `tgclient`, so the 15 msg/min/chat budget and the
  retry policy apply.

## Idempotency

`birthday_greeted` stores `bg:<absChat>:<YYYY-MM-DD>:<userID>` with an
8-byte UTC timestamp. The marker is written before the send and cleared
if the send fails. A crash between the marker and the send loses one
greeting; it never duplicates one. The daily job prunes markers older
than 48 hours at the start of each chat pass.

## Schedule

- Daily at 10:00 Moscow.
- Startup catch-up runs once between 10:00 and 20:00 Moscow if the bot
  was down at the slot; after 20:00 it waits for the next day.
- The worker is tracked in `App.inFlight`; `Stop` waits for it before
  closing bbolt.

## Failure handling

| Failure | Response |
|---------|----------|
| `getChat` error or nil `birthdate` | skip the member; debug/warn log without the date |
| `getChatMember` error | skip the member; warn log |
| `SendMessage` error | `tgclient` retries; after exhaustion clear the markers, log, no same-day retry |
| bbolt marker write error | do not send; clear already-written markers |
| chat bot status `left`/`kicked` | skip the chat |
| group -> supergroup migration | `MigrateChatID` rekeys `birthday_greeted` with the other chat-scoped buckets |

## Configuration

`BIRTHDAY_ENABLED` (default `false`) attaches the worker. No time or
timezone knob: the slot is fixed at 10:00 Moscow, matching the project's
MSK day boundary.

## Tests

`internal/domain/birthday/service_test.go` covers matching dates, the
29 February rule, the Moscow timezone, duplicate markers, a missing
birthdate, local and live left members, combined messages, HTML
escaping, the import-name privacy guard, send failure and marker
clearing, the left-bot chat, and `nextRun`.

`internal/storage/birthday_repo_test.go` covers mark/is/clear, prune,
and the `MigrateChatID` rekey.

## Gaps

- The first live run must confirm that `getChat` returns `birthdate`
  for a user observed in a common group who never started the bot.
- Telegram may withhold the date under some privacy settings; the
  member is skipped.
- The greeting is public by owner choice; the only per-user control is
  hiding the date in the Telegram profile.
