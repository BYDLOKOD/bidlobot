# PRD: BidloBot (DoxMe)

Telegram bot for IT community profile management with inline query language.

> **Canonical feature PRDs:** `.ptsd/docs/PRD.md`
> This document provides high-level architecture overview. Per-feature requirements live in the PTSD-managed PRD.

---

## Overview

- Users register profiles in private chat with the bot
- Each chat has its own context — profiles are per-chat
- Inline mode enables `@bot :user name :get :field` query syntax
- Zen spec (`zrc/doxme/bot.edn`) declares commands, profile fields, i18n, and config

## Features

### Layer 1: Infrastructure

| Feature | Dependencies | Description |
|---|---|---|
| zen-loader | — | Load/validate bot.edn, typed accessors |
| xtdb | — | XTDB node lifecycle, document CRUD |
| i18n | — | EN/RU translations, interpolation, language detection |
| bot-lifecycle | zen-loader | TG client, polling loop, update routing |

### Layer 2: Domain

| Feature | Dependencies | Description |
|---|---|---|
| query-lang | — | Parser for inline query syntax (string -> AST) |
| form-fsm | zen-loader, i18n | Multi-step registration form state machine |
| profiles | query-lang, form-fsm, xtdb, i18n | Profile CRUD + query evaluator |

### Layer 3: Features

| Feature | Dependencies | Description |
|---|---|---|
| chat-stats | xtdb, profiles, i18n | Message counts, top users, activity reports |
| chat-admin | xtdb, bot-lifecycle, i18n | Warnings, mutes, bans |
| youtube-summary | zen-loader, i18n | Summarize YouTube videos via GLM |

## Architecture

```
Telegram API
    |
    v
Bot Lifecycle (polling, routing)
  |
  +-- Zen Loader (config, schemas)
  +-- i18n (translations)
  +-- XTDB (storage)
  |
  +-- Query Language (parser)
  +-- Form FSM (registration)
  +-- Profiles (CRUD + evaluator)
  |
  +-- Chat Stats
  +-- Chat Admin
  +-- YouTube Summary (GLM)
```

## Technology Stack

| Component | Technology |
|---|---|
| Language | Clojure 1.12 |
| Bot Framework | clj-tg-bot-api 2.6.0 + martian-hato |
| Database | XTDB (in-memory dev, RocksDB prod) |
| Config/Schema | zen-lang |
| AI | GLM API |
| Testing | clojure.test |

## Environment Variables

```bash
TG_BOT_TOKEN=...
TG_API_URL=https://api.telegram.org    # optional
DEFAULT_LANG=en                         # optional
DEBUG=false                             # optional
XTDB_STORAGE_TYPE=memory                # memory | rocksdb
XTDB_STORAGE_PATH=/data/xtdb            # required for rocksdb
GLM_API_KEY=...                         # for youtube-summary
GLM_API_URL=https://open.bigmodel.cn/api/paas/v4
GLM_MODEL=...
YOUTUBE_API_KEY=...                     # for youtube-summary
```
