---
id: reputation
kind: spec
touches:
  - internal/bot/reputation.go
  - internal/bot/rep_reaction.go
  - internal/domain/reputation/
  - internal/storage/reputation_repo.go
written: 2026-08-16
updated: 2026-08-16
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

## Privacy

Needs message content (reply/`@user` targeting) - privacy OFF. Note:
`/roast` `/praise` historically were curated-template games; since
commit 334bc94 they are reputation transfers (see
[25_games.md](25_games.md) - the game table no longer lists them).
