# BidloBot PRD

Product Requirements Document for DoxMe — Telegram bot for IT community profile management.

---

<!-- feature:zen-loader -->

# zen-loader: Zen Config Loader

Load and validate `zrc/doxme/bot.edn` via zen-lang. Provide typed accessors to config, profile fields, commands, and i18n data.

## Problem

Bot behavior (profile schema, commands, translations) should be declared in a single EDN spec, not scattered across Clojure code. Zen provides schema validation and tag-based queries to make this work.

**Affects:** all other features — they depend on zen context for config and schemas.

## Acceptance Criteria

**Context lifecycle:**
- [ ] `(create-context ["zrc"] env-map)` returns zen context with `doxme.bot` namespace loaded
- [ ] Missing `zrc/` directory returns `{:error :config-not-found}`
- [ ] Invalid EDN returns `{:error :config-parse-error :message "..."}`
- [ ] Schema violations return `{:error :config-validation-error :errors [...]}`
- [ ] Environment variables resolved via `#env`, `#env-integer`, `#env-boolean`, `#env-keyword` tags
- [ ] `env-map` passed as `{:env env-map}` to `zen/new-context`

**Accessors (pure reads from zen context):**
- [ ] `(get-config ztx)` returns `{:token "..." :default-language :en :debug false}`
- [ ] `(get-profile-fields ztx)` returns vector of maps with `:name`, `:type`, `:required`, `:prompt`, `:zen/desc`
- [ ] `(get-profile-fields ztx)` returns fields in deterministic order (sorted: required first, then by name)
- [ ] `(get-inline-commands ztx)` returns vector of `{:command :user :zen/desc "..." :syntax "..." :examples [...]}`
- [ ] `(get-bot-commands ztx)` returns vector of `{:command "/start" :zen/desc "..." :handler :start-handler}`
- [ ] `(get-i18n ztx :en)` returns flat map `{:form/title "Profile Registration" ...}`
- [ ] `(get-i18n ztx :nonexistent)` returns nil

**Validation:**
- [ ] `(validate-profile ztx data)` with valid data returns `{:valid true}`
- [ ] `(validate-profile ztx data)` with missing required field returns `{:valid false :errors [...]}`

## Non-Goals

- Hot-reload (restart bot to pick up changes)
- Multiple config files (single `bot.edn`)
- Config UI (edit EDN manually)
- Bot lifecycle (covered by `bot-lifecycle` feature)

## Edge Cases

| Input | Result |
|-------|--------|
| Empty `bot.edn` | `:config-parse-error` |
| `bot.edn` without `ns` declaration | `:config-validation-error` |
| `#env TG_BOT_TOKEN` when env var missing | zen throws with var name in message |
| `#env [VAR "default"]` when var missing | uses default value |
| No symbols tagged `profile-field` | `get-profile-fields` returns `[]` |

## Implementation Files

- `src/doxme/zen/loader.clj` — context creation, namespace loading
- `src/doxme/zen/accessors.clj` — typed accessors (get-config, get-profile-fields, etc.)
- `test/doxme/zen/loader_test.clj`
- `test/doxme/zen/accessors_test.clj`

---

<!-- feature:xtdb -->

# xtdb: XTDB Database

XTDB node lifecycle and document CRUD. Provides storage primitives for profiles, stats, and admin data.

## Problem

Multiple features (profiles, stats, admin) need persistent document storage. XTDB is schemaless — new fields added at application level via zen validation, no migrations needed.

**Affects:** profiles, chat-stats, chat-admin.

## Acceptance Criteria

**Node lifecycle:**
- [ ] `(create-node {:storage :memory})` returns in-memory XTDB node (for dev/test)
- [ ] `(create-node {:storage :rocksdb :path "/data/xtdb"})` returns persistent node
- [ ] `(close-node node)` gracefully shuts down node, flushes pending writes
- [ ] Storage config from env: `XTDB_STORAGE_TYPE` (memory|rocksdb), `XTDB_STORAGE_PATH`

**Document operations:**
- [ ] `(put-doc node doc)` stores document; `doc` must have `:xt/id`; returns tx receipt
- [ ] `(get-doc node id)` returns document map or nil if not found
- [ ] `(delete-doc node id)` removes document; returns tx receipt
- [ ] `(query node q params)` executes Datalog query, returns vector of result tuples

**Transactions:**
- [ ] All writes (put, delete) are synchronous — function returns after tx is indexed
- [ ] Concurrent puts to same `:xt/id` — last write wins (XTDB default)

**ID conventions (enforced by callers, documented here):**
- [ ] Profile: `:profile/{user-id}-{chat-id}`
- [ ] User stats: `:user-stats/{user-id}-{chat-id}`
- [ ] Chat stats: `:chat-stats/{chat-id}`
- [ ] Admin: `:admin/{user-id}-{chat-id}`
- [ ] Warning: `:warn/{uuid}`
- [ ] Mute: `:mute/{user-id}-{chat-id}`
- [ ] Ban: `:ban/{user-id}-{chat-id}`

## Non-Goals

- Schema migrations (schemaless, zen validates at app level)
- Connection pooling (single node per bot process)
- Multi-node clustering
- Bitemporal queries (current state only)
- Backup/restore automation

## Edge Cases

| Input | Result |
|-------|--------|
| `create-node` with non-writable path | throws `:storage-error` with path |
| `get-doc` for non-existent ID | returns nil |
| `put-doc` without `:xt/id` | throws `:missing-id` |
| `close-node` on already-closed node | throws `:node-closed` |
| `delete-doc` for non-existent ID | no-op, returns tx receipt |

## Implementation Files

- `src/doxme/db/node.clj` — node lifecycle (create, close)
- `src/doxme/db/ops.clj` — document CRUD + query
- `test/doxme/db/node_test.clj`
- `test/doxme/db/ops_test.clj`

---

<!-- feature:i18n -->

# i18n: Internationalization

Translation system: EN (base) + RU. Loads from zen spec, provides interpolation and language detection from Telegram updates.

## Problem

Bot serves international IT communities. Messages, prompts, and errors must be localized. Telegram provides user's `language_code` — we use it to pick the right language.

**Affects:** form-fsm (button labels, prompts), profiles (messages), chat-stats, chat-admin, youtube-summary.

## Acceptance Criteria

**Translation lookup:**
- [ ] `(t ztx :en :form/title)` returns `"Profile Registration"`
- [ ] `(t ztx :ru :form/title)` returns `"Регистрация профиля"`
- [ ] `(t ztx :en :nonexistent/key)` returns `":nonexistent/key"` (key as string fallback)
- [ ] `(t ztx :de :form/title)` falls back to `:en`, returns `"Profile Registration"`
- [ ] Fallback chain: requested lang -> `:en` -> key as string

**Interpolation:**
- [ ] `(t ztx :en :form/progress {:current 2 :total 5})` returns `"Step 2 of 5"`
- [ ] `(t ztx :ru :form/progress {:current 2 :total 5})` returns `"Шаг 2 из 5"`
- [ ] Missing variable in vars map: placeholder stays unchanged `"Step {current} of {total}"`
- [ ] Nil value in vars map: replaced with `""`
- [ ] Mechanism: `clojure.string/replace` for each `{key}` in template

**Language detection:**
- [ ] `(detect-language ztx update)` reads `update.message.from.language_code` or `update.inline_query.from.language_code`
- [ ] `"ru"` -> `:ru`, `"en"` -> `:en`
- [ ] Missing/unknown language_code -> default from `(get-config ztx)` `:default-language`
- [ ] Nil update -> default language

## Non-Goals

- Auto-translation (manual translations only)
- More than 2 languages in v1 (extensible later)
- Per-user language preference (uses Telegram language_code)
- Pluralization rules (v2)
- Nested keys like `:a/b/c` (flat namespace only)

## Edge Cases

| Input | Result |
|-------|--------|
| Language `:de` (unsupported) | Fallback to `:en` |
| Key exists in `:ru` but not in `:en` | Returns value from `:ru` |
| Key missing in all languages | Returns `":namespace/key"` |
| Nil language parameter | Uses default from config |
| Interpolation `{key}` with key not in vars | Placeholder unchanged |

## Implementation Files

- `src/doxme/i18n.clj` — `t`, `detect-language`
- `test/doxme/i18n_test.clj`

---

<!-- feature:bot-lifecycle -->

# bot-lifecycle: Bot Lifecycle

Telegram client creation, long-polling update loop, update routing to handlers. Wires everything together.

## Problem

The bot needs a startup sequence: load config -> create TG client -> start polling -> route updates to handlers. And graceful shutdown.

**Affects:** all features that handle Telegram updates.

## Acceptance Criteria

**Client creation:**
- [ ] `(create-bot ztx)` reads config from zen context, creates TG client via `clj-tg-bot-api`
- [ ] Returns `{:client <tg-client> :ztx <zen-context>}`
- [ ] Client configured with `:bot-token` from config, rate limiter defaults

**Polling:**
- [ ] `(start-polling bot handler-fn)` starts long-polling loop in a thread
- [ ] Polls `getUpdates` with `timeout: 30`, `limit: 100`
- [ ] `allowed_updates`: `["message" "callback_query" "inline_query"]`
- [ ] Each update dispatched to `handler-fn`
- [ ] Offset tracked: `(inc (apply max (map :update_id updates)))` after each batch

**Update routing:**
- [ ] `(route-update bot update)` dispatches by update type (multimethod on `tg-utils/get-update-type`)
- [ ] `:message` updates -> command handlers or stats collection
- [ ] `:callback_query` updates -> form FSM handler
- [ ] `:inline_query` updates -> query language handler
- [ ] Unknown update types -> ignored (no error)

**Shutdown:**
- [ ] `(stop-bot bot)` sets stop flag, waits for current poll to finish, returns
- [ ] No new polls started after `stop-bot` called
- [ ] Blocks until in-flight update handlers complete (timeout: 5s)

**Startup sequence:**
- [ ] `(start!)`: load zen context -> create XTDB node -> create TG client -> start polling
- [ ] Returns system map: `{:ztx ... :node ... :bot ...}`
- [ ] `(stop! system)`: stop polling -> close XTDB node

## Non-Goals

- Webhook mode (polling only in v1)
- Multiple bot instances
- Health check endpoint
- Metrics/monitoring
- Auto-restart on crash

## Edge Cases

| Input | Result |
|-------|--------|
| Invalid bot token | TG API returns 401, `create-bot` throws with `:invalid-token` |
| Network timeout during poll | Retry next iteration (polling loop continues) |
| Handler throws exception | Log error, continue polling (don't crash) |
| `stop-bot` during active poll | Waits for poll response, then stops |
| TG API returns 429 (rate limit) | clj-tg-bot-api limiter handles backoff |

## Implementation Files

- `src/doxme/bot.clj` — `start!`, `stop!`, system lifecycle
- `src/doxme/tg/client.clj` — TG client creation
- `src/doxme/tg/polling.clj` — polling loop
- `src/doxme/tg/router.clj` — update routing multimethod
- `test/doxme/bot_test.clj`
- `test/doxme/tg/polling_test.clj`
