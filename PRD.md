# PRD: BidloBot - Telegram Profile & Query Bot

## 1. Overview

BidloBot — это Telegram бот для управления профилями пользователей в групповых чатах с inline query language интерфейсом.

### Core Concept
- Пользователи регистрируются в приватном чате с ботом
- Каждый чат имеет свой контекст — профили per-chat
- Inline mode позволяет запрашивать данные через query language
- Zen-спека описывает команды, поля профиля и поведение бота

---

## 2. User Stories

### 2.1 Регистрация профиля
```
Как участник чата с ботом,
Я хочу зарегистрировать свой профиль,
Чтобы другие могли узнать обо мне через inline запросы.
```

**Flow:**
1. Пользователь открывает приватный чат с ботом
2. Отправляет `/start` или `/register`
3. Бот запускает многошаговую форму
4. Пользователь заполняет поля (может пропустить опциональные)
5. После заполнения обязательных полей — профиль активен

### 2.2 Inline запрос профиля
```
Как участник чата,
Я хочу запросить информацию о коллеге через @bidlobot,
Чтобы быстро узнать о нём без лишних сообщений.
```

**Flow:**
1. В любом чате пишется `@bidlobot :user username :get :field`
2. Бот парсит query language
3. Возвращает результат как inline article
4. Пользователь выбирает — отправляет в чат

### 2.3 Обновление профиля
```
Как зарегистрированный пользователь,
Я хочу обновить своё поле профиля,
Чтобы поддерживать актуальность информации.
```

---

## 3. Architecture

### 3.1 High-Level Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        Telegram API                         │
└─────────────────────────────────────────────────────────────┘
                              ▲
                              │ HTTPS (webhook/polling)
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                      BidloBot Core                          │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │   Handlers   │  │  Query Lang  │  │  Form Machine    │  │
│  │  - Commands  │  │   Parser     │  │   (FSM)          │  │
│  │  - Callbacks │  │   Evaluator  │  │                  │  │
│  │  - Inline    │  │              │  │                  │  │
│  └──────────────┘  └──────────────┘  └──────────────────┘  │
│  ┌──────────────────────────────────────────────────────┐  │
│  │                    Zen Spec Loader                    │  │
│  │  (reads bot.edn → validates → builds handlers)       │  │
│  └──────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                       XTDB                                  │
│  - Users & Profiles (per-chat)                              │
│  - Form Sessions (bitemporal)                               │
│  - Query History                                            │
└─────────────────────────────────────────────────────────────┘
```

### 3.2 Project Structure

```
bidlobot/
├── deps.edn
├── resources/
│   ├── bot.edn              # Zen спецификация бота
│   └── i18n/
│       ├── en.edn
│       └── ru.edn
├── src/
│   └── bidlobot/
│       ├── core.clj         # Entry point
│       ├── config.clj       # Env vars & config
│       ├── db/
│       │   ├── core.clj     # XTDB connection
│       │   ├── users.clj    # User queries/txes
│       │   └── sessions.clj # Form sessions
│       ├── handlers/
│       │   ├── commands.clj # /start, /register, /profile
│       │   ├── callbacks.clj# Form navigation
│       │   ├── inline.clj   # Inline queries
│       │   └── messages.clj # Text input for forms
│       ├── form/
│       │   ├── machine.clj  # FSM for multi-step forms
│       │   ├── steps.clj    # Step definitions from zen
│       │   └── renderer.clj # Form UI rendering
│       ├── query/
│       │   ├── parser.clj   # Query language parser
│       │   ├── evaluator.clj# Query execution
│       │   └── autocomplete.clj # Inline suggestions
│       ├── zen/
│       │   ├── loader.clj   # Load & validate bot.edn
│       │   └── schema.clj   # Zen schema definitions
│       └── i18n.clj         # Internationalization
├── test/
│   └── bidlobot/
│       ├── integration/
│       │   └── bot_test.clj # E2E tests with mock TG
│       └── unit/
│           ├── query_test.clj
│           └── form_test.clj
├── docker-compose.yml
├── Dockerfile
└── README.md
```

---

## 4. Data Model

### 4.1 XTDB Documents

#### User (Telegram User)
```edn
{:xt/id :user/123456789
 :user/id 123456789
 :user/username "veschin"
 :user/first-name "Vladimir"
 :user/last-name "Eshchin"
 :user/language-code "ru"
 :user/registered-at #inst "2026-02-13"}
```

#### Profile (per-chat)
```edn
{:xt/id :profile/123456789-100
 :profile/user :user/123456789
 :profile/chat 100
 :profile/salary "100k"
 :profile/stack "Clojure, TypeScript"
 :profile/role "Senior Engineer"
 :profile/location "Remote"
 :profile/bio "Building things"
 :profile/created-at #inst "2026-02-13"
 :profile/updated-at #inst "2026-02-13"}
```

#### Form Session
```edn
{:xt/id :session/123456789
 :session/user :user/123456789
 :session/chat 100
 :session/state :form/step-salary
 :session/data {:name "Vladimir" :role "Engineer"}
 :session/message-id 42
 :session/history [:form/step-name :form/step-role]
 :session/created-at #inst "2026-02-13"
 :session/expires-at #inst "2026-02-20"}  ;; 7 дней
```

### 4.2 Schema Evolution (XTDB approach)

XTDB — bitemporal document store. Schema определяется на уровне приложения через Zen:

```edn
;; В bot.edn
{:profile/fields
 {:salary {:type :string :required true}
  :stack {:type :string :required false}
  :role {:type :string :required true}
  ;; Новое поле добавляется здесь
  :company {:type :string :required false}}}
```

**Миграции:**
- XTDB не требует миграций схемы (dynamic schema)
- Новые поля добавляются в Zen спеку
- При чтении профиля — default значения из спеки
- Bitemporal — история изменений встроена

---

## 5. Query Language

### 5.1 Grammar (EBNF)

```ebnf
query       = command (":" argument)*
command     = "user" | "chat" | "help"
argument    = identifier | value
identifier  = letter (letter | digit | "_")*
value       = string | number
string      = word+
word        = letter (letter | digit)*
```

### 5.2 Commands

| Command | Description | Example |
|---------|-------------|---------|
| `:user <name> :get <field>` | Get user's field | `:user veschin :get :salary` |
| `:user <name> :profile` | Get full profile | `:user veschin :profile` |
| `:chat :users` | List users in chat | `:chat :users` |
| `:help` | Show help | `:help` |
| `:help <command>` | Command help | `:help :user` |

### 5.3 Inline Results

```clojure
;; Ответ на :user veschin :get :salary
{:type "article"
 :id "1"
 :title "veschin's salary"
 :description "100k USD"
 :input-message-content {:message-text "💰 veschin: 100k USD"}}
```

---

## 6. Multi-Step Form Flow

### 6.1 State Machine

```
                    ┌─────────────┐
                    │    START    │
                    └──────┬──────┘
                           │ /register
                           ▼
                    ┌─────────────┐
              ┌────▶│ STEP: NAME  │◀────┐
              │     └──────┬──────┘     │
              │            │ next       │ back
              │            ▼            │
              │     ┌─────────────┐     │
              │     │ STEP: ROLE  │─────┘
              │     └──────┬──────┘
              │            │
              │            ▼
              │     ┌─────────────┐
              │     │STEP: SALARY │
              │     └──────┬──────┘
              │            │
              │            ▼
              │     ┌─────────────┐
              │     │  ... etc    │
              │     └──────┬──────┘
              │            │
              │            ▼
              │     ┌─────────────┐
              └─────│CONFIRMATION │
                    └──────┬──────┘
                           │ confirm
                           ▼
                    ┌─────────────┐
                    │  COMPLETED  │
                    └─────────────┘
```

### 6.2 Actions per Step

| Action | Keyboard Button | Callback Data |
|--------|-----------------|---------------|
| Next | "Далее ▶️" | `form:next` |
| Back | "◀️ Назад" | `form:back` |
| Skip | "⏭️ Пропустить" | `form:skip` (optional fields) |
| Cancel | "❌ Отмена" | `form:cancel` |
| Confirm | "✅ Подтвердить" | `form:confirm` |

### 6.3 Session Expiry

- Unfinished forms expire after **7 days**
- Background job cleans up expired sessions daily
- On expiry — notification sent to user

---

## 7. Zen Spec Structure

### 7.1 bot.edn Template

```edn
{ns bidlobot.bot

 imports #{zen}

 ;; Profile schema
 profile
 {:zen/tags #{zen/schema}
  :type zen/map
  :keys {:salary {:type zen/string
                  :zen/desc "Salary expectation"
                  :required true}
         :stack {:type zen/string
                 :zen/desc "Tech stack"
                 :required false}
         :role {:type zen/string
                :zen/desc "Current role"
                :required true}
         :location {:type zen/string
                    :zen/desc "Location/Timezone"
                    :required false}
         :bio {:type zen/string
               :zen/desc "About yourself"
               :max-length 500
               :required false}}}

 ;; Inline query commands
 commands
 {:user {:zen/desc "Query user profile"
         :params {:name {:type zen/string :required true}
                  :field {:type zen/keyword :optional true}}
         :examples [":user veschin :get :salary"
                    ":user veschin :profile"]}
  :chat {:zen/desc "Query chat data"
         :params {:action {:type zen/keyword}}
         :examples [":chat :users"]}
  :help {:zen/desc "Show help"
         :params {:command {:type zen/keyword :optional true}}}}

 ;; Form steps (derived from profile schema)
 form-steps
 [{:field :name :prompt "What's your name?"}
  {:field :role :prompt "What's your current role?"}
  {:field :salary :prompt "Your salary expectation?"}
  {:field :stack :prompt "Your tech stack?" :optional true}
  {:field :location :prompt "Your location?" :optional true}
  {:field :bio :prompt "Tell about yourself" :optional true}]

 ;; i18n keys
 i18n
 {:en {:form/title "Registration"
       :form/step-n "Step {n} of {total}"
       :form/back "Back"
       :form/next "Next"
       :form/skip "Skip"
       :form/cancel "Cancel"
       :form/confirm "Confirm"
       :profile/saved "Profile saved!"}
  :ru {:form/title "Регистрация"
       :form/step-n "Шаг {n} из {total}"
       :form/back "Назад"
       :form/next "Далее"
       :form/skip "Пропустить"
       :form/cancel "Отмена"
       :form/confirm "Подтвердить"
       :profile/saved "Профиль сохранён!"}}}
```

---

## 8. Technical Decisions

### 8.1 Dependencies

| Dependency | Version | Purpose |
|------------|---------|---------|
| Clojure | 1.12.x | Runtime |
| XTDB | 2.x | Database |
| clj-tg-bot-api | latest | Telegram client |
| martian-hato | latest | HTTP client for TG |
| zen | latest | Spec language |
| integrant | latest | Component lifecycle |
| aero | latest | Config |

### 8.2 HTTP Client Choice

**Рекомендация: hato**

Причины:
- Современный, на основе Java 11+ HttpClient
- Поддержка HTTP/2
- Асинхронный API
- Меньше зависимостей чем clj-http

### 8.3 Update Method

**Рекомендация: Webhook для production, Long polling для dev**

- Production: Webhook + reverse proxy (nginx/caddy)
- Development: Long polling (проще локально)

### 8.4 Rate Limiting

Библиотека clj-tg-bot-api имеет встроенный rate limiter с defaults из Telegram FAQ:
- 1 msg/sec per chat
- 20 msgs/min per group
- 30 msgs/sec total

Кастомизация через `:limiter-opts` если нужно.

### 8.5 Testing Strategy

```
Unit Tests (clojure.test)
├── Query parser/evaluator
├── Form FSM transitions
├── Zen spec validation
└── i18n formatting

Integration Tests
├── XTDB operations (with in-memory node)
├── Handler logic with mock TG client
└── Form flow end-to-end

E2E Tests
└── Full bot simulation with VCR-style recorded responses
```

---

## 9. Configuration

### 9.1 Environment Variables

```bash
# Telegram
TG_BOT_TOKEN=123456:ABC-DEF...

# XTDB
XTDB_URI=xtdb://localhost:5432/bidlobot
# or for local dev:
XTDB_STORAGE_TYPE=memory  # or 'rocksdb'
XTDB_STORAGE_PATH=/data/xtdb

# App
APP_ENV=development  # development | production
APP_PORT=8080        # for webhook
APP_LOG_LEVEL=info

# i18n
DEFAULT_LANGUAGE=en
```

### 9.2 deps.edn

```clojure
{:paths ["src" "resources"]
 :deps {org.clojure/clojure {:mvn/version "1.12.0"}
        com.github.marksto/clj-tg-bot-api {:mvn/version "latest"}
        com.github.oliyh/martian-hato {:mvn/version "0.2.2"}
        com.xtdb/xtdb-core {:mvn/version "2.x.x"}
        zen-lang/zen {:mvn/version "latest"}
        integrant/integrant {:mvn/version "0.13"}
        aero/aero {:mvn/version "1.1.6"}}
 :aliases
 {:dev {:extra-paths ["dev" "test"]
        :extra-deps {nrepl/nrepl {:mvn/version "1.3"}}}
  :test {:extra-paths ["test"]
         :exec-fn kaocha.runner/exec-fn
         :extra-deps {lambdaisland/kaocha {:mvn/version "1.91"}}}
  :run {:exec-fn bidlobot.core/-main}}}
```

---

## 10. Deployment

### 10.1 Docker Compose

```yaml
version: '3.8'
services:
  xtdb:
    image: xtdb/xtdb:latest
    volumes:
      - xtdb-data:/var/lib/xtdb
    ports:
      - "5432:5432"
  
  bot:
    build: .
    environment:
      - TG_BOT_TOKEN=${TG_BOT_TOKEN}
      - XTDB_URI=xtdb://xtdb:5432/bidlobot
      - APP_ENV=production
    depends_on:
      - xtdb
    ports:
      - "8080:8080"

volumes:
  xtdb-data:
```

### 10.2 Dockerfile

```dockerfile
FROM eclipse-temurin:21-jre
WORKDIR /app
COPY target/bidlobot.jar /app/
CMD ["java", "-jar", "bidlobot.jar"]
```

---

## 11. Open Questions

1. **Админка** — пока не определено, оставить на future iteration
2. **Статистика чата** — механизм (real-time vs periodic dump) не уточнён
3. **Конкретные поля профиля** — будут определены в bot.edn
4. **Формат inline результатов** — TBD (article, photo, etc)

---

## 12. Success Metrics

- [ ] Пользователь может зарегистрироваться через многошаговую форму
- [ ] Inline запросы возвращают корректные данные профиля
- [ ] Профили per-chat работают изолированно
- [ ] Формы можно прервать, пропустить поля, вернуться назад
- [ ] Незавершённые формы чистятся через 7 дней
- [ ] Мультиязычность (EN/RU) работает
- [ ] Test coverage > 80%

---

## 13. References

- [clj-tg-bot-api](https://github.com/marksto/clj-tg-bot-api) - Telegram client
- [zen-lang/zen](https://github.com/zen-lang/zen) - Spec language
- [XTDB](https://xtdb.com) - Database
- [Telegram Bot API](https://core.telegram.org/bots/api) - Official docs
