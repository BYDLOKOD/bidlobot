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

---

<!-- feature:query-lang -->

# query-lang: Query Language Parser

Parser for DoxMe inline query syntax. Converts `:user veschin :get :salary` strings into structured AST. Pure function, no data dependencies.

## Problem

Inline mode needs a structured query language so users can query profiles and stats via `@botname :cmd :args`. The parser converts text to AST; evaluation is handled by the `profiles` feature (which owns the data).

**Affects:** profiles (uses parsed AST to evaluate queries).

## Acceptance Criteria

**Parsing:**
- [ ] `(parse ":user veschin :get :salary")` returns `{:cmd :user :args ["veschin" :get :salary]}`
- [ ] `(parse ":user veschin :profile")` returns `{:cmd :user :args ["veschin" :profile]}`
- [ ] `(parse ":chat :stats")` returns `{:cmd :chat :args [:stats]}`
- [ ] `(parse ":chat :stats :top")` returns `{:cmd :chat :args [:stats :top]}`
- [ ] `(parse ":help")` returns `{:cmd :help :args []}`
- [ ] `(parse ":help :user")` returns `{:cmd :help :args [:user]}`
- [ ] `(parse "")` returns `{:error :empty-query}`
- [ ] `(parse "   ")` returns `{:error :empty-query}`
- [ ] `(parse "no colon")` returns `{:error :invalid-syntax}`
- [ ] `(parse ":unknown-cmd foo")` returns `{:error :unknown-command :command "unknown-cmd"}`

**Token rules:**
- [ ] Token starting with `:` parsed as keyword (`:get` -> `:get`)
- [ ] Token without `:` parsed as string word (`"veschin"` -> `"veschin"`)
- [ ] Whitespace between tokens is ignored (1+ spaces/tabs)
- [ ] Query must start with `:` followed by a known command

**Known commands:**
- [ ] `:user` — query user profile data
- [ ] `:chat` — query chat statistics
- [ ] `:help` — show help information

## Grammar (EBNF)

```ebnf
query       = ":" command { " " token }
command     = "user" | "chat" | "help"
token       = keyword | word
keyword     = ":" identifier
word        = letter { letter | digit | "_" | "-" }
identifier  = letter { letter | digit | "_" | "-" }
```

## Non-Goals

- Query evaluation (lives in `profiles` feature)
- Fuzzy matching or autocomplete
- Query validation beyond syntax (semantic validation is evaluator's job)
- Natural language queries

## Edge Cases

| Input | Result |
|-------|--------|
| Empty string | `{:error :empty-query}` |
| Only whitespace | `{:error :empty-query}` |
| `:user` (no args) | `{:cmd :user :args []}` (valid parse, evaluator checks args) |
| `:user::name` (double colon) | `{:error :invalid-syntax}` |
| Query > 500 chars | `{:error :query-too-long}` |
| `:USER veschin` (uppercase cmd) | `{:error :unknown-command}` (commands are lowercase) |
| Username with underscore `:user dev_ops :get :salary` | Parses `"dev_ops"` as word |

## Implementation Files

- `src/doxme/query/parser.clj` — `parse` function
- `test/doxme/query/parser_test.clj`

---

<!-- feature:form-fsm -->

# form-fsm: Form FSM

Multi-step form state machine for user registration. Handles step navigation, validation, data collection, session expiry. Steps derived from zen profile-field definitions.

## Problem

Collecting 5 profile fields requires guiding users through sequential steps with back/forward navigation, optional field skipping, and session persistence across messages.

**Affects:** profiles (triggers form on `/register`, receives completed form data).

## Acceptance Criteria

**State transitions:**
- [ ] `:idle` + `/register` -> `:step/salary` (first required field)
- [ ] `:step/salary` + valid input -> `:step/stack` (next step)
- [ ] `:step/salary` + `:back` -> stays `:step/salary` (first step, can't go back)
- [ ] `:step/stack` + input -> `:step/role`
- [ ] `:step/role` + input -> `:step/location`
- [ ] `:step/location` + `:skip` -> `:step/bio` (optional field, skippable)
- [ ] `:step/bio` + input -> `:confirm`
- [ ] `:confirm` + `:confirm` -> `:completed`
- [ ] `:confirm` + `:back` -> `:step/bio`
- [ ] Any state + `:cancel` -> `:idle` (abandon form, clear data)

**Step order:** salary -> stack -> role -> location -> bio (deterministic, from `get-profile-fields` sorted order).

**Session management:**
- [ ] Sessions stored in atom: `{[user-id chat-id] session-map}`
- [ ] `(create-session user-id chat-id steps)` creates session in `:idle` state
- [ ] `(get-session sessions user-id chat-id)` returns session or nil
- [ ] Session shape: `{:state :step/salary :step-idx 0 :data {:salary "150k"} :created-at <inst>}`
- [ ] `(cleanup-expired sessions)` removes sessions older than 7 days

**Data collection:**
- [ ] Session accumulates data per step: `{:salary "150k" :stack "Clojure"}`
- [ ] Required fields reject `:skip` action with error
- [ ] Optional fields accept `:skip` (no data stored for that field)
- [ ] On `:completed`, returns collected data map

**UI rendering:**
- [ ] `(render-step ztx session lang)` returns `{:text "..." :keyboard [[button ...]]}`
- [ ] Text includes step prompt from zen + progress "Step 2 of 5"
- [ ] Keyboard buttons: `:back` (not on first step), `:skip` (only on optional), `:cancel` (always)
- [ ] Confirm step shows collected data summary + `:confirm`/`:back`/`:cancel` buttons
- [ ] All button labels from i18n

**Session expiry:**
- [ ] Sessions expire after 7 days of inactivity
- [ ] Expired session interaction returns `{:error :session-expired}`
- [ ] Background cleanup via `ScheduledExecutorService`, period 24h, starts on bot startup

## Non-Goals

- Persisting sessions across bot restarts (in-memory only)
- Conditional branching (linear steps)
- File/media uploads in form fields (text only)
- Multiple form types (single registration form)
- Editing individual fields after completion (use `/update` command in profiles)

## Edge Cases

| Scenario | Result |
|----------|--------|
| `/register` with existing active session | Resume from current step |
| `:skip` on required field | Error: "This field is required" |
| `:back` on first step | No-op, stay on first step |
| Expired session, user clicks button | `{:error :session-expired}` |
| Bot restart with active sessions | Sessions lost; next interaction starts fresh |
| Callback from wrong/stale message | Ignore silently |

## Implementation Files

- `src/doxme/form/machine.clj` — state machine, transitions
- `src/doxme/form/session.clj` — session CRUD, cleanup
- `src/doxme/form/renderer.clj` — UI rendering (text + keyboard)
- `test/doxme/form/machine_test.clj`
- `test/doxme/form/renderer_test.clj`

---

<!-- feature:profiles -->

# profiles: User Profiles

Profile CRUD, inline query evaluation, and Telegram command handlers. Owns profile data and the query evaluator that connects parsed AST to actual data.

## Problem

IT community members want to share and discover teammate info (salary, stack, role). Profiles are per-chat. Inline queries need an evaluator that reads profile data — it lives here because this feature owns the data.

**Affects:** chat-stats (needs profile data for user display names).

## Acceptance Criteria

**Registration (`/register` command):**
- [ ] `/register` in group chat -> sends deep link button to private chat
- [ ] `/register` in private chat -> starts form-fsm registration flow
- [ ] Completed form saves profile to XTDB via `put-doc`
- [ ] Profile ID: `:profile/{user-id}-{chat-id}`

**Profile viewing (`/profile` command):**
- [ ] `/profile` shows own profile (all filled fields, empty fields omitted)
- [ ] `/profile @username` shows that user's profile
- [ ] Unregistered user -> i18n `:profile/not-found` message

**Profile update (`/update` command):**
- [ ] `/update :salary 200k` updates single field
- [ ] `/update :nonexistent value` -> error "Unknown field"
- [ ] `/update` without args -> starts edit form (reuses form-fsm)

**Inline query evaluation:**
- [ ] `(evaluate node parsed-query)` takes parsed AST from query-lang, returns result
- [ ] `:user veschin :get :salary` -> `{:result "150k USD" :title "veschin's salary"}`
- [ ] `:user veschin :profile` -> `{:result "salary: 150k\nstack: Clojure\n..." :title "veschin's profile"}`
- [ ] `:user nonexistent :get :salary` -> `{:error :user-not-found}`
- [ ] `:user veschin :get :nonexistent` -> `{:error :field-not-found}`
- [ ] `:help` -> `{:result "Available commands:\n:user ..." :title "Help"}`
- [ ] `:help :user` -> `{:result "Usage: :user <name> :get <field>\n..." :title "Help: :user"}`

**Inline result formatting:**
- [ ] Each result wrapped as Telegram inline article: `{:type "article" :id ... :title ... :description ... :input-message-content {:message-text ...}}`
- [ ] `cache-time: 0`, `is-personal: true`

**Data model:**
- [ ] Profile stored with `:profile/` namespace prefix on fields
- [ ] Fields: `:profile/user-id`, `:profile/chat-id`, `:profile/username`, `:profile/salary`, `:profile/stack`, `:profile/role`, `:profile/location`, `:profile/bio`, `:profile/created-at`, `:profile/updated-at`
- [ ] Username lookup: case-insensitive, strip leading `@`

## Non-Goals

- Cross-chat profile aggregation
- Profile privacy settings (all visible to chat members)
- Fuzzy username matching (exact only)
- Profile photos (use Telegram photo)
- Profile versioning/history

## Edge Cases

| Scenario | Result |
|----------|--------|
| Username with `@` prefix | Strip `@`, lookup by bare name |
| Username case mismatch | Case-insensitive lookup |
| User has no Telegram username | Lookup by user-id; display `first_name` |
| User leaves chat | Profile remains (historical data) |
| Same user, different chats | Separate profiles per chat |
| Bio > 500 chars | Reject in form validation (zen schema `max-length: 500`) |
| `/cancel` during registration | Delegates to form-fsm cancel |

## Implementation Files

- `src/doxme/profiles/core.clj` — profile CRUD (save, get, update)
- `src/doxme/profiles/evaluator.clj` — query evaluator (AST -> result)
- `src/doxme/handlers/commands.clj` — /register, /profile, /update handlers
- `src/doxme/handlers/inline.clj` — inline query handler
- `test/doxme/profiles/core_test.clj`
- `test/doxme/profiles/evaluator_test.clj`
- `test/doxme/handlers/commands_test.clj`

---

<!-- feature:chat-stats -->

# chat-stats: Chat Statistics

Track message counts per user per chat. Provide `/stats` command and inline query for activity reports.

## Problem

Community managers need visibility into chat activity — who's active, how many messages, top contributors. Stats collected passively from all messages.

**Affects:** none (leaf feature).

## Acceptance Criteria

**Stats collection:**
- [ ] Every non-bot message in group chat increments user's message count
- [ ] Counted: text, photo, video, document, sticker messages
- [ ] Not counted: edited messages, service messages (join/leave), bot messages
- [ ] Collection is async via `core.async` channel (non-blocking)
- [ ] First message from user creates user-stats entry with `first-seen` timestamp
- [ ] Each message updates `last-seen` timestamp

**Data model:**
- [ ] Chat stats: `{:xt/id :chat-stats/{chat-id} :chat-stats/total-messages N :chat-stats/created-at <inst>}`
- [ ] User stats: `{:xt/id :user-stats/{user-id}-{chat-id} :user-stats/user-id ... :user-stats/chat-id ... :user-stats/message-count N :user-stats/first-seen <inst> :user-stats/last-seen <inst>}`
- [ ] `total-users` computed on read as count of user-stats docs for chat

**Stats commands:**
- [ ] `/stats` -> chat overview: total messages, total users, avg messages/user, most active user
- [ ] `/stats :top` -> top 10 users by message count
- [ ] `/stats :today` -> today's message count (UTC day boundaries)
- [ ] `/stats :user @username` -> specific user's stats (messages, first-seen, last-seen, rank)

**Inline stats:**
- [ ] `:chat :stats` -> chat overview as inline article
- [ ] `:chat :stats :top` -> top users as inline article

**Report format:**
- [ ] Numbers formatted with commas (15,234)
- [ ] Dates formatted: "Jan 15, 2026"
- [ ] "Today" for current UTC day, otherwise date

## Non-Goals

- Real-time monitoring / live dashboard
- Message content analysis
- Export to CSV/JSON
- Historical time-range queries beyond today/week
- Private chat stats (groups only)
- Per-day breakdown / charts

## Edge Cases

| Scenario | Result |
|----------|--------|
| New chat, no messages yet | `/stats` shows "No activity yet" |
| `/stats` in private chat | Error "Stats only available in groups" |
| User with 0 messages in top | Not shown in `:top` list |
| Chat with < 10 users | `:top` shows all users |
| Bot added to existing chat | Stats start from 0, no backfill |

## Implementation Files

- `src/doxme/stats/collector.clj` — async message counter
- `src/doxme/stats/reporter.clj` — report generation + formatting
- `src/doxme/handlers/stats.clj` — /stats command handler
- `test/doxme/stats/collector_test.clj`
- `test/doxme/stats/reporter_test.clj`

---

<!-- feature:chat-admin -->

# chat-admin: Chat Management

Admin tools: warnings, mutes, bans. Permission system based on Telegram admin detection + bot-managed admin list.

## Problem

Community managers need moderation tools — warn disruptive users, mute repeat offenders, ban if needed. All actions logged.

**Affects:** none (leaf feature).

## Acceptance Criteria

**Permission system:**
- [ ] Bot detects Telegram chat admins via `getChatAdministrators` API, caches per chat
- [ ] Bot maintains separate admin list in XTDB (`:admin/{user-id}-{chat-id}`)
- [ ] `/admin :add @user` promotes to bot-admin (creator-only)
- [ ] `/admin :remove @user` demotes bot-admin (creator-only)
- [ ] `/admin :list` shows all admins
- [ ] Non-admins get error on admin commands

**Warning system:**
- [ ] `/warn @user "reason"` issues warning (admin-only)
- [ ] Warning stored in XTDB: `:warn/{uuid}` with user-id, chat-id, reason, issuer, timestamp
- [ ] Public notification in chat: "@user warned: reason (warning N/3)"
- [ ] After 3 warnings -> auto-mute 24 hours
- [ ] `/warns @user` shows warning history
- [ ] `/warn :clear @user` clears all warnings

**Mute system:**
- [ ] `/mute @user 1h` mutes for 1 hour (via `restrictChatMember` API, `can_send_messages: false`)
- [ ] `/mute @user 1d` mutes for 1 day
- [ ] `/mute @user` mutes indefinitely
- [ ] `/unmute @user` removes restriction
- [ ] Bot needs `can_restrict_members` admin right
- [ ] Timed mutes: background job checks expiry, calls `restrictChatMember` with `can_send_messages: true`

**Ban system:**
- [ ] `/ban @user "reason"` bans via `banChatMember` API (removes from chat)
- [ ] `/unban @user` unbans via `unbanChatMember` API
- [ ] Ban logged in XTDB: `:ban/{user-id}-{chat-id}`

**Logging:**
- [ ] All admin actions logged to configured admin-log channel (if set in config)
- [ ] Log format: `[ACTION] admin -> target: reason (timestamp)`

## Non-Goals

- Auto-moderation (spam/link detection)
- Appeal system
- Temporary bans (bans are permanent until `/unban`)
- Cross-chat bans
- Warning expiry (warnings persist until cleared)

## Edge Cases

| Scenario | Result |
|----------|--------|
| Admin warns themselves | Error "Cannot warn yourself" |
| Warn user not in chat | Error "User not in chat" |
| Mute already muted user | Extends duration |
| Ban another admin | Error "Cannot ban admin" |
| Bot not admin in chat | Error "Bot needs admin rights" |
| Admin commands in private chat | Error "Only in groups" |
| Creator tries to be demoted | Error "Cannot remove creator" |

## Implementation Files

- `src/doxme/admin/permissions.clj` — permission checks
- `src/doxme/admin/warnings.clj` — warning CRUD + escalation
- `src/doxme/admin/mutes.clj` — mute/unmute + expiry
- `src/doxme/admin/bans.clj` — ban/unban
- `src/doxme/admin/logging.clj` — action logging
- `src/doxme/handlers/admin.clj` — command handlers
- `test/doxme/admin/permissions_test.clj`
- `test/doxme/admin/warnings_test.clj`

---

<!-- feature:youtube-summary -->

# youtube-summary: YouTube Summaries

Summarize YouTube videos posted in chat via GLM API. Includes GLM client, URL parsing, and rate limiting.

## Problem

IT community members share YouTube videos (talks, tutorials) but not everyone watches 30+ minute content. Summaries help decide if content is relevant.

**Affects:** none (leaf feature).

## Acceptance Criteria

**URL parsing:**
- [ ] Recognizes `youtube.com/watch?v=ID`, `youtu.be/ID`, `m.youtube.com/watch?v=ID`
- [ ] Extracts video ID from URL
- [ ] Rejects non-YouTube URLs with "Only YouTube videos supported"

**Video metadata:**
- [ ] Fetch title and duration via YouTube Data API v3 (`YOUTUBE_API_KEY` env var)
- [ ] Videos > 1 hour rejected: "Video too long (max 1 hour)"
- [ ] Videos < 1 min rejected: "Video too short to summarize"

**Transcript:**
- [ ] Fetch captions via YouTube captions endpoint
- [ ] No captions available -> error "No subtitles available"
- [ ] Transcript truncated to 10,000 chars before sending to GLM

**GLM client:**
- [ ] `(create-glm-client config)` with API key, base URL, model from env vars
- [ ] `(summarize client content opts)` sends ChatCompletion request, returns summary string
- [ ] Request: `{:model M :messages [{:role "system" ...} {:role "user" :content C}] :max_tokens 500 :temperature 0.7}`
- [ ] Auth: `Authorization: Bearer {GLM_API_KEY}`
- [ ] Empty API key -> `{:error :invalid-api-key}`
- [ ] Content > 10,000 chars -> truncated with warning in log
- [ ] Timeout 30s -> `{:error :timeout}`
- [ ] HTTP 429 -> `{:error :rate-limited}`

**Summary trigger:**
- [ ] `/summarize <url>` generates and posts summary
- [ ] Auto-summarize if `:auto-summarize true` in zen bot-config (optional setting)

**Summary format:**
```
<title>
<duration>

Main Topics:
- Topic 1
- Topic 2

Key Points:
- Point 1
- Point 2

Worth watching if: <recommendation>
```
- [ ] Summary language matches requester's language (via i18n detect-language)

**Rate limiting:**
- [ ] Max 10 summaries per hour per chat
- [ ] Tracked in atom `{chat-id [timestamps]}`, resets on bot restart (acceptable)
- [ ] Exceeded -> "Summary limit reached. Try again later."

## Non-Goals

- Other video platforms (Vimeo, Twitch)
- Summary caching
- Multiple summary formats/lengths
- Custom prompts
- Cost tracking

## Edge Cases

| Scenario | Result |
|----------|--------|
| Invalid YouTube URL | "Invalid YouTube URL" |
| Deleted/private video | "Video not found or unavailable" |
| Live stream | "Cannot summarize live streams" |
| GLM API error | "Summary service temporarily unavailable" |
| Rate limit exceeded | "Summary limit reached" |
| No `GLM_API_KEY` set | Feature disabled, `/summarize` returns "Not configured" |
| No `YOUTUBE_API_KEY` set | Feature disabled |

## Implementation Files

- `src/doxme/youtube/url.clj` — URL parsing + validation
- `src/doxme/youtube/fetcher.clj` — metadata + transcript fetching
- `src/doxme/glm/client.clj` — GLM API client
- `src/doxme/youtube/summarizer.clj` — orchestration (fetch + summarize)
- `src/doxme/handlers/youtube.clj` — /summarize command handler
- `test/doxme/youtube/url_test.clj`
- `test/doxme/glm/client_test.clj`
- `test/doxme/youtube/summarizer_test.clj`
