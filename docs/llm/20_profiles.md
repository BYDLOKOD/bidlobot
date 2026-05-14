---
id: profiles
kind: spec
---

# User Profiles

See also: [10_scope.md](10_scope.md), [50_telegram.md](50_telegram.md).

## Data model

| Field | Type | Required | Constraints |
|-------|------|----------|-------------|
| stack | string | yes | 1-200 chars |
| role | string | yes | 1-100 chars |
| bio | string | no | 1-500 chars |

Auto-populated: `user_id`, `chat_id` (abs), `username`, `first_name`, `created_at`, `updated_at`.

Key: `profile:{user_id}:{abs(chat_id)}`. One user can have different profiles in different chats.

## /start in DM

- `/start` no payload -> "Welcome to BidloBot! Use /register in a group chat to create your profile."
- `/start reg_{abs_chat_id}` -> begin registration FSM
- `/start upd_{abs_chat_id}` -> begin update FSM
- Invalid payload -> same as no payload

## Registration FSM

**Trigger:** `/register` in supergroup.

**Group-side:**
1. Not supergroup -> "This command is only available in supergroup chats."
2. Profile exists -> "Profile already exists. Use /update to edit."
3. Bot sends message with inline URL button: `t.me/{bot}?start=reg_{abs_chat_id}`
4. Auto-delete message after 30s (best-effort)

**DM-side (after user clicks deep link):**
1. Parse payload, reconstruct chat_id (`-{abs_chat_id}`)
2. Check bot is member of chat -> "Bot is not a member of this chat."
3. Check user is member of chat -> "You are not a member of this chat."
4. Check no active FSM session -> "You already have an active registration. Complete it or /cancel first."

**Copy flow (before form steps):**
- User has profiles in other chats -> query `getChat` for titles (skip if bot was removed from that chat)
- Show up to 5 most recent profiles (by `updated_at`): [Copy from {title}] [Fill manually]
- Copy -> jump to confirmation. Fill manually -> step 1.

**Form steps:**

| Step | Field | Required | Keyboard |
|------|-------|----------|----------|
| 1 | stack | yes | [Cancel] |
| 2 | role | yes | [Back] [Cancel] |
| 3 | bio | no | [Back] [Skip] [Cancel] |
| 4 | confirm | - | [Confirm] [Back] [Cancel] |

- Text input only. Non-text (photo, sticker, voice) -> "Please send a text message."
- Back preserves entered values in session, shows current value in prompt.
- Cancel deletes session -> "Registration cancelled."
- Confirm saves to DB -> "Profile created!"

**Session timeout:** 1 hour inactivity -> session deleted. Next DM message -> "Registration session expired. Use /register in a group chat to start again."

**Concurrency guard:** Before DB write, re-check profile doesn't exist (another session could have created it).

## Viewing

**Own:** `/profile` in supergroup.
- No profile -> "You don't have a profile in this chat. Use /register to create one."

**Other:** `/profile @username` or `/profile {user_id}`.
- `@` prefix -> case-insensitive search by `username` field in current chat's profiles
- Numeric -> direct lookup `profile:{user_id}:{abs(chat_id)}`
- Other -> "Invalid argument. Use: /profile @username or /profile user_id"
- Not found -> "Profile not found. User is not registered in this chat."

**Output (HTML):**
```
<b>@username</b> (First Name)
<b>Stack:</b> Go, PostgreSQL, Kubernetes
<b>Role:</b> Senior Backend Developer
<b>Bio:</b> Building distributed systems
```
No bio -> omit line. No username -> first_name only.

## Updating

**Full:** `/update` (no args) in supergroup -> deep link `t.me/{bot}?start=upd_{abs_chat_id}` -> same FSM with pre-filled values. Confirm -> overwrite profile.

**Inline:** `/update {field} {value}` in supergroup.
- First word = field name, rest = value
- `/update bio` (no value) -> "Please provide a value: /update bio Your bio text"
- Unknown field -> "Unknown field. Available: stack, role, bio."
- No profile -> "Profile not found. Use /register to create one."

## Username sync

On every interaction (command, callback, message): if `username` in update differs from stored, update all user's profiles. Background, non-blocking.

## Data lifecycle

- User leaves chat -> profile preserved. Viewable by user_id.
- Account deleted -> profile preserved with stored first_name.
- Bot removed and re-added -> all data intact (keyed by chat_id).
