---
id: scope
kind: spec
---

# Scope & Decisions

## What this bot does

Three capabilities for IT community supergroups in Telegram:

1. **Profiles** - members register tech stack, role, bio. Per-chat. Registration via DM.
2. **Statistics** - message counters per user, top contributors, activity reports.
3. **Moderation** - warn/mute/ban with 3-strike auto-mute. Telegram-native admin model.

## What was dropped (and why)

| Feature | Why |
|---------|-----|
| YouTube Summary | Unrelated to core, incomplete, obscure LLM provider dependency (GLM/BigModel.cn) |
| Inline Query DSL | Overengineered parser+evaluator for 3 commands. Standard commands suffice |
| Salary field | Public salary in group chat = guaranteed conflict |
| zen-lang config | 200-line loader for reading a bot token and 5 strings. Env vars suffice |
| i18n (v1) | All strings in English. Centralized in one package for future i18n |
| Bot-managed admin list | Duplicates Telegram's native admin system |

## Deployment model

Single Go binary. Long-polling. Embedded KV database (BoltDB/Badger/SQLite).

Env vars:
- `TG_BOT_TOKEN` (required)
- `DB_PATH` (default: `./data`)
- `LOG_LEVEL` (default: `info`)

## ID scheme

Format: `{entity}:{user_id}:{abs(chat_id)}`

Chat IDs stored as absolute values (supergroup IDs are negative in Telegram). Users identified by `user_id` (stable, never changes). Username is display-only.

Examples:
- Profile: `profile:123456:1001234567890`
- Stats: `stats:123456:1001234567890`
- Warning: `warn:{uuid}` (globally unique, chat_id inside document)

## Commands

| Command | Context | Access |
|---------|---------|--------|
| `/register` | supergroup | all |
| `/profile [@user]` | supergroup | all |
| `/update [field value]` | supergroup | all |
| `/stats [top\|today\|@user]` | supergroup | all |
| `/warn @user [reason]` | supergroup | admins |
| `/warns @user` | supergroup | all |
| `/warns clear @user` | supergroup | admins |
| `/mute @user [duration]` | supergroup | admins |
| `/unmute @user` | supergroup | admins |
| `/ban @user [reason]` | supergroup | admins |
| `/unban @user` | supergroup | admins |
| `/help` | supergroup + DM | all |
| `/cancel` | DM | all |

## Performance targets

- Update processing: < 100ms (p95)
- Memory: < 100MB at 50 active chats
