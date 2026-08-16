---
id: referral
kind: spec
touches:
  - internal/bot/referral.go
  - internal/domain/referral/
  - internal/storage/referral_repo.go
  - internal/storage/migrate.go
written: 2026-08-16
updated: 2026-08-16
---

# Referral catalog

Chat-scoped referral catalog (commit 04a4bfa): members register
referral links ("рефки") for services they use, the chat can browse
them, and admins can remove bad/duplicate entries. Public supergroup
surface, read-mostly.

## Commands (supergroup, cooldown 5s shared across aliases)

| Command | Access | Action |
|---------|--------|--------|
| `/refs` (alias `/ref`) | all | list registered referrals (chunked at 4096 chars) |
| `/refreg` (alias `/ref-reg`) | all | register a referral (inline picker) |
| `/refreport <id>` | admins | remove a referral (confirm keyboard) |

`/ref` and `/ref-reg` are typed-only aliases (absent from the slash
menu; the canonical forms are listed). Registration replies are
matched only when replying to the active prompt
(`RegistrationInputPredicate`) - ordinary chat traffic is never
swallowed.

## Registration UX

`/refreg` -> inline-keyboard picker (8 services/page, paginated) ->
tap an existing service then reply with the URL, or "Новый сервис"
then reply with `сервис\nссылка` (2 lines) or
`сервис\nэффект\nссылка` (3 lines). Name matching is
fuzzy/normalized (`referral.MatchServices`: lowercase alnum only)
with an exact-effect conflict prompt.

Duplicates rejected: same owner+service, same URL in chat, existing
normalized service name (`ErrServiceExists` / `ErrOwnerServiceExists`
/ `ErrURLExists`).

## Moderation

`/refreport <id>` -> admin confirm keyboard "Неверное оформление" /
"Скам" / "Отмена". Deleting the last referral of a service prunes the
service row + name index (`DeleteReferral`).

## Storage & validation

bbolt buckets `referral_services` (`rfs:<absChat>:<svcID>`),
`referral_services_name_idx` (`rfsn:<absChat>:<NormalizeName>` ->
service ID), `referrals` (`rf:<absChat>:<refID>`). IDs from
`NextSequence` (globally unique, migration-safe).

Limits: service name <=80 runes, effect <=160 runes, label <=48 runes,
URL <=2048 bytes and https-only. Interactions (pending registration
prompts) are 16-hex crypto-random tokens, actor/chat/prompt-locked,
TTL 10 min, lazily pruned.

Chat-ID migration rekeys referrals (`migrate.go migrateReferrals`).

## Privacy

Stores user-submitted URLs + owner display names in chat-scoped
buckets. Needs privacy OFF to see `/refreg` prompt replies in the
group (prompt replies are ordinary messages).
