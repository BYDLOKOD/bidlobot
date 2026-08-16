---
id: admission
kind: spec
touches:
  - internal/bot/captcha.go
  - internal/bot/membership.go
  - internal/domain/captcha/
  - internal/storage/captcha_repo.go
  - internal/storage/admission_attempt_repo.go
written: 2026-08-16
updated: 2026-08-16
---

# New-member captcha & unauthorized-admission gate

Two related entry controls: who may **add the bot to a chat** (owner
gate, always on) and what a **new member** must do on join (math
captcha, opt-in via env).

## Unauthorized-admission gate (always on)

`internal/bot/membership.go`, `membershipMyChatMemberHandler` on
`my_chat_member` updates (routes.go). On a bot add transition
(`left|kicked -> member|administrator`):

- actor == `BOT_OWNER_ID` -> normal onboarding (admin -> "BidloBot
  подключён..." message; member -> "нужны права администратора";
  plain group -> "только супергруппы").
- actor != owner -> **immediate `LeaveChat`** + an owner DM notice
  "Unauthorized bot admission rejected. Chat: ... Added by: ..."
  (English). The notice is suppressed after 2 lifetime attempts per
  actor (`unauthorizedAdmissionNoticeLimit`), counted in the
  `admission_attempts` bucket (`aa:<userID>` = 8-byte big-endian
  counter) - stops an attacker spamming the owner's DM.

`BOT_OWNER_ID` is **required** at startup (config validation fails
without it) - see [70_deployment.md](70_deployment.md).

## Captcha (opt-in: `CAPTCHA_ENABLED`)

`internal/bot/captcha.go` + `internal/domain/captcha`. Default **off**
- when off, the bot is unchanged (nil service, pass-through handler).

### Join flow (`captchaChatMemberHandler`, chat_member updates)

Fires only on a genuine new join (`left|kicked -> member`),
supergroup, non-bot/non-anonymous-admin. A `restricted -> member`
transition (own unmute) never re-captchas.

`Service.OnJoin`:
1. Delete any stale challenge for the user (rejoin guard).
2. Mute the newcomer: all send permissions off, `UntilDate = now +
   CAPTCHA_TIMEOUT` (best-effort).
3. Post "Добро пожаловать, %s! Решите капчу..." + `a + b = ?` with 4
   inline buttons (`cap:ans:<id>:<answer>`; answers = sum + up to 3
   distractors ±3, shuffled; crypto-random 16-hex challenge id).

### Answers (`OnAnswer`, callback `cap:`)

- Wrong -> toast "Неправильный ответ." + ban+unban **kick** (rejoinable;
  live membership status re-checked first).
- Correct -> edit challenge to "Капча пройдена", unmute (restores chat
  default permissions via GetChat), then **fire-and-forget** async
  welcome animation: the embedded `welcome.mp4` (`//go:embed
  assets/welcome.mp4`) `SendAnimation` with onboarding questions
  ("Скинь свой (neo/fast)fetch / Чем ты занимаешься? / Какой у тебя
  грейд? / Сколько платят? / Почему решил зайти в чат?"). The send
  MUST be async (`go svc.sendWelcome(context.Background(), ...)`) - a
  synchronous multi-hundred-KB upload inside the sequential update
  loop stalls every subsequent update for seconds.

### Timeout (`runCaptchaSweep`)

Interval = min(timeout/3, 30s), >=5s; tracked in `App.inFlight`. Kicks
still-muted newcomers who never answered and rewrites the notice
("%s кикнут: не решил капчу."). No DB write per challenge beyond the
bucket entries.

### Storage

Buckets `captcha` (`cc:<id>`) and `captcha_user_idx`
(`ccu:<absChat>:<userID>`).

## Env

- `CAPTCHA_ENABLED` (default false; 1/true/yes/on enables).
- `CAPTCHA_TIMEOUT` (default `1m`, validated 1m..30m).
