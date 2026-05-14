# BidloBot PRD

## Product Overview

Telegram-бот для IT-сообществ. Три функции: профили участников, статистика чата, модерация.

Бот работает в supergroup-чатах. Регистрация профиля через DM. Статистика и модерация - в группе.

### Требования к развёртыванию

- Бот должен быть **администратором** в группе (необходимо для: видимости всех сообщений, restrict/ban прав)
- Минимальные admin permissions: `can_restrict_members`, `can_delete_messages`
- Privacy mode значения не имеет при наличии admin-статуса (admin всегда видит все сообщения)

### Идентификация пользователей

- Primary key: `user_id` (стабилен, не меняется)
- `username` - опционален, может отсутствовать у пользователя. Используется только для отображения и удобного поиска, **не для идентификации**
- Lookup по username - best-effort: поиск по последнему известному username в профилях текущего чата

### Составные ключи (ID scheme)

Формат: `{entity}:{user_id}:{abs(chat_id)}` - chat_id всегда хранится как абсолютное значение (без минуса). Supergroup IDs в Telegram отрицательные (например -1001234567890), при хранении и в deep link payload используется `abs()`: `1001234567890`.

Примеры:
- Профиль: `profile:123456:1001234567890`
- Статистика: `stats:123456:1001234567890`
- Warning: `warn:{uuid}` (глобально уникален, chat_id внутри документа)

### Типы чатов

| Тип | Поведение бота |
|-----|---------------|
| `private` | Регистрация профиля (FSM-форма), `/help`, `/cancel` |
| `group` | Не поддерживается. Бот отвечает: "Add the bot to a supergroup." |
| `supergroup` | Полный функционал: команды, статистика, модерация |
| `channel` | Игнорируется |

Группы автоматически мигрируют в supergroup при определённых условиях (>200 участников, public username, persistent history). При миграции `chat_id` меняется - бот должен обрабатывать ошибку `migrate_to_chat_id` и обновлять хранимый chat_id.

### Anonymous admins

В supergroups с включенной анонимностью администраторов сообщения от админов приходят с `from.id == 1087968824` (фиксированный ID сервисного аккаунта GroupAnonymousBot). Определение: `from.id == 1087968824` или `from.is_bot == true && from.username == "GroupAnonymousBot"`. Бот обрабатывает их следующим образом:

- **Stats:** сообщения от anonymous admins **не считаются** (невозможно атрибутировать конкретному пользователю)
- **Moderation commands:** anonymous admin **не может** использовать модерационные команды через бота (невозможно проверить, кто именно отправил команду). Ответ: "Moderation commands are not available in anonymous admin mode. Disable 'Remain Anonymous' to use moderation."
- **Profile commands:** `/register`, `/profile`, `/update` от anonymous admin -> "This command requires a non-anonymous account."

### Linked channel messages

Сообщения, автоматически перенаправленные из привязанного канала в supergroup, приходят с `sender_chat` вместо `from`. Бот **игнорирует** такие сообщения (не считает в статистике, не обрабатывает как команды).

---

## Feature 1: User Profiles

### Назначение

Участники IT-чата регистрируют профили (стек, роль, био) для поиска коллег по навыкам.

### Данные профиля

| Поле | Тип | Обязательное | Ограничения |
|------|-----|-------------|-------------|
| stack | string | да | 1-200 символов |
| role | string | да | 1-100 символов |
| bio | string | нет | 1-500 символов |

Технические поля (заполняются автоматически):
- `user_id` - Telegram user ID
- `chat_id` - abs(chat_id) чата, к которому привязан профиль
- `username` - последний известный Telegram username (обновляется при каждом взаимодействии)
- `first_name` - имя из Telegram
- `created_at` - дата создания
- `updated_at` - дата последнего обновления

### Профиль привязан к чату

Один пользователь может иметь разные профили в разных чатах (разные роли, стеки). ID профиля: `profile:{user_id}:{abs(chat_id)}`.

### /start в DM

Обработка `/start` в private chat:

- `/start` без payload -> "Welcome to BidloBot! Use /register in a group chat to create your profile."
- `/start reg_{abs_chat_id}` -> начало FSM регистрации (flow ниже)
- `/start upd_{abs_chat_id}` -> начало FSM обновления (flow ниже)
- `/start` с невалидным payload -> та же реакция, что без payload

### Регистрация (FSM в DM)

**Триггер:** пользователь отправляет `/register` в supergroup-чате.

**Flow:**

1. Бот проверяет: является ли чат supergroup?
   - Нет -> ответ: "This command is only available in supergroup chats."
   - Да -> продолжаем

2. Бот проверяет: есть ли уже профиль для `{user_id}:{abs(chat_id)}`?
   - Да -> ответ: "Profile already exists. Use /update to edit."
   - Нет -> продолжаем

3. Бот отправляет в группу сообщение с inline-кнопкой:
   - Текст: "To register, continue in private messages"
   - Кнопка: URL `https://t.me/{bot_username}?start=reg_{abs_chat_id}`
   - Удаляется автоматически через 30 секунд (best-effort, не критично при ошибке API)

4. Пользователь кликает кнопку -> попадает в DM -> бот получает `/start reg_{abs_chat_id}`

5. Бот парсит payload, извлекает `chat_id` (добавляет минус обратно: `-{abs_chat_id}`), проверяет последовательно:
   - Бот является участником чата? (через `getChatMember(chat_id, bot_id)`) - если нет -> "Bot is not a member of this chat."
   - Пользователь является участником чата? (через `getChatMember(chat_id, user_id)`) - если нет -> "You are not a member of this chat."
   - Обе проверки пройдены -> продолжаем

6. Проверка активной сессии:
   - Уже есть активная FSM-сессия (для любого чата)? -> "You already have an active registration. Complete it or /cancel first."
   - Нет -> начинаем FSM

7. Проверка существующих профилей (copy flow):
   - У пользователя есть профили в других чатах? -> бот запрашивает title этих чатов через `getChat`
   - Если `getChat` вернул ошибку (бот удалён из того чата) -> пропускаем этот профиль
   - Если есть доступные профили -> "You have a profile in {chat_title}. Copy it?"
   - Inline keyboard: [Copy from {chat_title}] [Fill manually]
   - Если профилей в нескольких чатах -> показать список (максимум 5, по дате `updated_at` desc). Остальные не показываются - 5 самых свежих достаточно
   - Copy -> профиль копируется -> показать подтверждение (шаг 10)
   - Fill manually -> перейти к шагу 8
   - Нет профилей -> перейти к шагу 8

8. **Шаг 1: Stack** (обязательный)
   - Бот: "Your tech stack? (languages, frameworks, tools)"
   - Inline keyboard: [Cancel]
   - Пользователь отправляет **текст** -> валидация (1-200 символов) -> переход к шагу 9
   - Пользователь отправляет non-text (фото, стикер, голос, документ и т.д.) -> "Please send a text message." (FSM остаётся на текущем шаге)
   - Пользователь нажимает Cancel -> сессия удаляется -> "Registration cancelled."

9. **Шаг 2: Role** (обязательный)
   - Бот: "Your role? (e.g., Backend Developer, DevOps, Team Lead)"
   - Inline keyboard: [Back] [Cancel]
   - Пользователь отправляет **текст** -> валидация (1-100 символов) -> переход к шагу 9.5
   - Non-text -> "Please send a text message."
   - Back -> возврат к шагу 8 (введённый stack сохраняется в сессии, показывается в промпте как текущее значение)
   - Cancel -> сессия удаляется -> "Registration cancelled."

9.5. **Шаг 3: Bio** (опциональный)
   - Бот: "Tell about yourself (optional)"
   - Inline keyboard: [Back] [Skip] [Cancel]
   - Пользователь отправляет **текст** -> валидация (1-500 символов) -> переход к шагу 10
   - Non-text -> "Please send a text message."
   - Skip -> bio пустое -> переход к шагу 10
   - Back -> возврат к шагу 9 (введённый role сохраняется)
   - Cancel -> сессия удаляется -> "Registration cancelled."

10. **Подтверждение**
    - Бот показывает сводку:
      ```
      Stack: Go, PostgreSQL, Kubernetes
      Role: Senior Backend Developer
      Bio: Building distributed systems
      ```
    - Inline keyboard: [Confirm] [Back] [Cancel]
    - Confirm -> профиль сохраняется в БД -> "Profile created!"
    - Back -> возврат к шагу 9.5
    - Cancel -> сессия удаляется -> "Registration cancelled."

**Таймаут сессии:** 1 час неактивности -> сессия удаляется. При следующем сообщении в DM бот отвечает: "Registration session expired. Use /register in a group chat to start again."

**Конкурентная проверка при сохранении:** перед записью в БД бот проверяет, что профиль ещё не существует (другая сессия могла создать его). Если уже существует -> "Profile was already created. Use /update to edit."

### Просмотр профиля

**Свой профиль:** `/profile` в supergroup -> бот отвечает профилем пользователя для этого чата.
- Профиль не найден -> "You don't have a profile in this chat. Use /register to create one."

**Чужой профиль:** `/profile @username` или `/profile {user_id}` в supergroup -> бот ищет профиль:
1. Аргумент начинается с `@` -> поиск в профилях текущего чата по полю `username` (case-insensitive, без `@`)
2. Аргумент - число -> прямой lookup по `profile:{user_id}:{abs(chat_id)}`
3. Иначе -> "Invalid argument. Use: /profile @username or /profile user_id"
4. Не найден -> "Profile not found. User is not registered in this chat."

**Формат вывода (HTML parse mode):**

```
<b>@username</b> (First Name)
<b>Stack:</b> Go, PostgreSQL, Kubernetes
<b>Role:</b> Senior Backend Developer
<b>Bio:</b> Building distributed systems
```

Если bio отсутствует - строка Bio не показывается. Если username отсутствует - показывается только first_name.

### Обновление профиля

**Полное:** `/update` без аргументов в supergroup -> бот отправляет deep link в DM:
- Кнопка: URL `https://t.me/{bot_username}?start=upd_{abs_chat_id}`
- DM flow: такой же FSM, как при регистрации, но поля предзаполнены текущими значениями (показываются в промпте: "Your tech stack? Current: Go, PostgreSQL")
- Confirm -> перезаписывает профиль. `updated_at` обновляется.

**Поточное:** `/update {field} {value}` в supergroup -> обновляет одно поле.

Парсинг: первое слово после `/update` - имя поля, всё остальное - значение.
- `/update stack Go, Rust, PostgreSQL` -> field=stack, value="Go, Rust, PostgreSQL"
- `/update bio` без value -> "Please provide a value: /update bio Your bio text"
- `/update bio ` (пробел, но пустое значение) -> та же ошибка

Допустимые поля: `stack`, `role`, `bio`. Валидация та же, что в FSM.

Несуществующее поле -> "Unknown field. Available: stack, role, bio."

Нет профиля -> "Profile not found. Use /register to create one."

### Обновление username

При каждом взаимодействии пользователя с ботом (команда, callback, сообщение) - если `username` в update отличается от хранимого, обновить во всех профилях пользователя. Фоновая операция, не блокирует обработку команды.

### Удалённые аккаунты и покинувшие чат

- Пользователь покинул чат -> профиль **сохраняется**. Доступен по `/profile @username` или user_id. Нет автоматической очистки.
- Аккаунт Telegram удалён -> профиль сохраняется. При попытке отобразить: `first_name` из хранимых данных, username может быть неактуальным. Нет механизма автоочистки в v1.
- Бот удалён из чата и добавлен обратно -> все данные (профили, stats, warnings) **сохраняются** (привязаны к chat_id).

---

## Feature 2: Chat Statistics

### Назначение

Отображение активности чата: кто сколько пишет, общие метрики.

### Требование: бот-администратор

Для сбора статистики бот должен быть администратором чата (видит все сообщения независимо от privacy mode). Если бот не админ - сообщения до получения admin-статуса не учтены, но бот не блокирует `/stats`: показывает то, что успел насчитать.

### Что считается

Каждое входящее сообщение в supergroup с реальным `from.id` (не бот, не anonymous) инкрементирует счётчик пользователя.

**Считаются:** сообщения с любым контентом (text, photo, video, document, sticker, voice, video_note, audio, animation, poll, location, contact, venue, game и т.д.) - если есть валидный `from` с `is_bot == false`.

**Не считаются:**
- Сообщения от ботов (`from.is_bot == true`)
- Сообщения от anonymous admins (`from.id == chat_id` или `is_automatic_forward`)
- Сообщения из linked channels (`sender_chat` присутствует)
- Service messages (new_chat_members, left_chat_member, pinned_message, etc.) - определяются по отсутствию контентных полей
- Edited messages (`edited_message` updates) - message_id уже учтён при первой отправке
- Forwarded messages - **считаются**, атрибутируются отправителю (не оригинальному автору)

### Модель данных

**Per-user stats:** `stats:{user_id}:{abs(chat_id)}` -> `{message_count, first_seen, last_seen}`

- `first_seen` - timestamp первого сообщения в чате
- `last_seen` - timestamp последнего сообщения в чате
- `message_count` - total messages

**Сбор:** In-memory buffer (map of counters), flush в БД каждые 60 секунд. При graceful shutdown - финальный flush.

**Запросы `/stats` читают из БД + in-memory buffer.** Результат = persisted + buffered. Никогда не показывать stale данные, если свежие доступны в памяти.

При потере in-memory буфера (crash) - потеря максимум 60 секунд данных. Допустимо для статистики.

### Команды

**`/stats`** - обзор чата

```
Chat Statistics
Total messages: 12,847
Total users: 156
Average per user: 82
Most active: @poweruser (2,341 messages)
Tracking since: Mar 4, 2026
```

**`/stats top`** - топ-5

```
Top Contributors
1. @poweruser - 2,341
2. @activeguy - 1,892
3. @coder42 - 1,456
4. @debater - 1,203
5. @helper - 987
```

Tie-breaking: при равном количестве сообщений - пользователь с более ранним `first_seen` выше.

**`/stats today`** - сегодня (UTC 00:00 boundary)

```
Today's Activity
Messages: 127
Active users: 23
```

**`/stats @username`** или **`/stats {user_id}`** - статистика пользователя

```
Stats for @username
Messages: 1,892
Rank: #2 of 156
First seen: Jan 15, 2026
Last seen: Today
```

Пользователь не найден -> "User not found in chat statistics."

**Невалидный subcommand:** `/stats foo` -> "Unknown subcommand. Available: top, today, @username."

**`/stats`** в private chat -> "Statistics are only available in group chats."

### Форматирование

- Числа с разделителем тысяч: `12,847`
- Даты: `Mon DD, YYYY`. Сегодня (UTC) -> `Today`
- Username с `@`. Без username -> first_name

---

## Feature 3: Moderation

### Назначение

Предупреждения, муты, баны для поддержания порядка в чате.

### Модель прав

Модерационные команды доступны только **Telegram-администраторам чата** (статус `creator` или `administrator`). Единый источник правды: Telegram API `getChatAdministrators`.

При вызове модерационной команды бот:
1. Вызывает `getChatAdministrators` (или использует кеш)
2. Фильтрует результат: исключает записи с `user.is_bot == true`
3. Проверяет наличие `from.id` в отфильтрованном списке
4. Нет -> "You don't have permission to use this command."
5. Есть -> выполняет

**Кеширование:** результат `getChatAdministrators` кешируется на 5 минут per-chat. Инвалидация: при получении `chat_member_updated` (Telegram присылает при смене прав) кеш для чата сбрасывается.

**Проверка прав бота:** перед выполнением restrict/ban бот проверяет свои права через `getChatMember(chat_id, bot_id)`. Результат кешируется вместе с admin list (тот же 5-минутный TTL, та же инвалидация по `chat_member_updated`). Если `can_restrict_members == false` -> "Bot needs 'Restrict Members' permission to perform this action."

### Warning System

**`/warn @username reason`** или `/warn` как reply на сообщение (reason опционален)

Flow:
1. Проверка прав вызывающего (admin? не anonymous?)
2. Определение target:
   - Если reply -> target = автор сообщения, на которое ответили
   - Если `@username` -> поиск по username в чате (через `getChatMember` если возможно, или по last-known в stats/profiles)
   - Если ни то, ни другое -> "Specify a user: /warn @username reason - or reply to a message."
3. Валидация target:
   - Бот? (`is_bot == true`) -> "Can't warn a bot."
   - Админ? (есть в admin list) -> "Can't warn an administrator."
   - Сам себя? (`from.user_id == target.user_id`) -> "Can't warn yourself."
4. Создание warning record: `warn:{uuid}` -> `{target_user_id, chat_id, issuer_user_id, reason, timestamp, active: true}`
5. Подсчёт **active** warnings для target в этом чате (where `active == true`)
6. Ответ в чат:

```
⚠️ @username warned (1/3)
Reason: Spam links
Issued by: @admin
```

Если reason пуст -> строка Reason не показывается.

**Auto-escalation при 3 warnings:**
- Бот автоматически мьютит пользователя на 24 часа
- Сообщение:

```
🔇 @username muted for 24h (3 warnings reached)
```

- Если бот не может замьютить (нет прав, target стал админом между warning и mute) -> предупреждение записывается, мьют не применяется. Сообщение: "⚠️ Warning recorded. Auto-mute failed: {reason from Telegram API}."
- **После 3 warnings:** счётчик НЕ сбрасывается автоматически. Warnings 4, 5, ... записываются, но не вызывают повторного auto-mute. Для повторной эскалации админ должен `/warns clear` и начать заново.
- **Формат ответа при 4+ warnings:** `"⚠️ @username warned (4 total). Auto-mute threshold already reached."` - дробь `X/3` показывается только при 1-3, после порога - абсолютное число.

**Конкурентность:** warning count читается и инкрементируется атомарно. Если два `/warn` приходят одновременно, один из них увидит count=2, другой count=3 (не оба count=2). Реализация: compare-and-swap при записи или DB-level serialization.

**`/warns @username`** - история предупреждений

```
Warnings for @username (2/3)
1. Spam links - by @admin1, Mar 4, 2026
2. Off-topic flood - by @admin2, Mar 5, 2026
```

Показывает только active warnings. Cleared warnings не отображаются.

Доступна **всем** участникам чата (не только админам).

**`/warns clear @username`** - очистка warnings (admin only)

Все warning records для этого пользователя в этом чате помечаются `active: false`. Записи **не удаляются** (audit trail). Счётчик active warnings сбрасывается до 0.

Ответ: "Warnings cleared for @username."

### Mute

**`/mute @username [duration]`** или `/mute` как reply

Duration format: `30m`, `1h`, `2h`, `12h`, `1d`, `7d`, `30d`. Без duration -> 1 час (default).

Границы: минимум 1 минута, максимум 366 дней (ограничение Telegram API). Невалидный формат -> "Invalid duration format. Examples: 30m, 1h, 7d."

Flow:
1. Проверка прав (admin? не anonymous? bot has restrict permission?)
2. Определение target (reply или @username)
3. Валидация:
   - Бот? -> "Can't mute a bot."
   - Админ? -> "Can't mute an administrator."
   - Сам себя? -> "Can't mute yourself."
4. Вызов `restrictChatMember` с permissions: все `false`. `until_date` = now + duration.
5. Если Telegram API вернул ошибку:
   - `"user is an administrator"` -> "Can't mute an administrator." (admin list мог измениться)
   - `"not enough rights"` -> "Bot needs 'Restrict Members' permission."
   - Другая ошибка -> логирование, ответ "Failed to mute. Please try again."
6. Ответ при успехе:

```
🔇 @username muted for 1h
By: @admin
```

**`/unmute @username`** или `/unmute` как reply

Восстановление прав чата: бот вызывает `getChat(chat_id)` для получения `permissions` (default chat permissions), затем `restrictChatMember` с этими permissions (НЕ all-true, а именно default permissions чата). Если `getChat` возвращает `permissions: null` (чат без явно заданных ограничений) - fallback: all-true (стандартное поведение Telegram для чата без ограничений).

```
🔊 @username unmuted
By: @admin
```

### Ban

**`/ban @username [reason]`** или `/ban` как reply

Flow:
1. Проверка прав
2. Определение target
3. Валидация (бот, админ, сам себя - аналогично mute)
4. Вызов `banChatMember(chat_id, user_id, revoke_messages: false)`. Ban permanent (без until_date).
5. Обработка ошибок API: аналогично mute.
6. Ответ:

```
🚫 @username banned
Reason: Repeated violations
By: @admin
```

Если reason пуст -> строка Reason не показывается.

`revoke_messages: false` - сообщения не удаляются.

**`/unban @username`**

Flow:
1. Проверка прав (admin?)
2. Определение target
3. Проверка ban-статуса: `getChatMember(chat_id, user_id)` - если `status != "kicked"` -> "User is not banned."
4. Вызов `unbanChatMember(chat_id, user_id, only_if_banned: true)`

**`only_if_banned: true` обязателен** - без этого флага метод удалит активного участника из чата.

При успехе:
```
✅ @username unbanned
```

Пользователь может вернуться по invite-ссылке.

### Определение target через reply

Все модерационные команды поддерживают два режима:
1. **Reply:** админ отвечает на сообщение нарушителя командой `/warn`, `/mute`, `/ban` - target берётся из `reply_to_message.from`
2. **Explicit:** `/warn @username reason` - target по username

Reply-режим приоритетнее: если команда отправлена как reply и содержит @username, используется reply target, @username и текст после него трактуются как reason.

### Что происходит с данными при бане

- Профиль **сохраняется**. Доступен для просмотра через `/profile`.
- Warnings **сохраняются**. Доступны через `/warns`.
- Stats **сохраняются**.
- При `/unban` и возвращении пользователя - все данные на месте.

---

## Feature 4: Help & Onboarding

### Первое добавление в группу

Когда бот добавляется в supergroup, он получает `my_chat_member` update. Бот:
1. Проверяет, является ли он админом (new status = `administrator`)
2. Если админ -> молча начинает работу (stats counting, command handling)
3. Если не админ -> отправляет одно сообщение: "I need administrator rights to function. Please promote me with 'Restrict Members' permission."
4. Если добавлен в обычную group (не supergroup) -> "I only work in supergroups. Please upgrade this group."

### /help в supergroup

```
BidloBot - profiles, stats, moderation

Profiles:
  /register - create your profile
  /profile - view your profile
  /profile @user - view someone's profile
  /update - edit your profile
  /update field value - quick edit

Stats:
  /stats - chat overview
  /stats top - top contributors
  /stats today - today's activity
  /stats @user - user stats

Moderation (admins only):
  /warn @user reason - issue warning
  /warns @user - view warnings
  /warns clear @user - clear warnings
  /mute @user [duration] - mute (default: 1h)
  /unmute @user - unmute
  /ban @user [reason] - ban
  /unban @user - unban
```

### /help в DM

```
BidloBot - profiles for IT communities

Use /register in a group chat to create your profile.

If you're currently registering, type /cancel to abort.
```

### /cancel в DM

Если есть активная FSM-сессия -> удалить сессию, ответ: "Registration cancelled."

Если нет активной сессии -> "Nothing to cancel."

В supergroup: `/cancel` -> игнорируется (не зарегистрирована в group scope, не показывается в меню).

### Bot command scopes

Разные меню команд для разных контекстов (через `setMyCommands` при старте бота):

| Scope | Commands |
|-------|---------|
| `BotCommandScopeAllPrivateChats` | /help, /cancel |
| `BotCommandScopeAllGroupChats` | /register, /profile, /update, /stats, /help |
| `BotCommandScopeAllChatAdministrators` | /register, /profile, /update, /stats, /warn, /warns, /mute, /unmute, /ban, /unban, /help |

### Edited messages

Бот **не обрабатывает** `edited_message` updates. Если пользователь отредактирует сообщение в команду - она не будет выполнена. Это стандартное поведение для Telegram-ботов.

### Mentions без команды

`@botname` без команды -> бот **не реагирует**.

---

## Cross-Cutting Concerns

### Internationalization

**Вне скоупа v1.** Весь UI на английском. Добавление языков - отдельная фича.

Решение: все пользовательские строки вынесены в один файл/пакет для будущей локализации, но переключение языков не реализуется.

### Error responses

Все ошибки - reply на сообщение пользователя. Формат: одна строка, plain text, без форматирования.

Ошибки от Telegram API не пробрасываются пользователю дословно. Бот логирует оригинальную ошибку и отвечает human-readable сообщением.

### Rate limiting (внутренний)

Бот ограничивает отправку в каждый чат: максимум 15 сообщений в минуту (ниже лимита Telegram в 20/min). При превышении - сообщения ставятся в очередь (не отбрасываются). Очередь per-chat, максимум 50 сообщений. При переполнении очереди - старейшие сообщения отбрасываются с логированием.

### Graceful shutdown

Сигнал: SIGTERM или SIGINT.

1. Прекратить polling (не запрашивать новые updates)
2. Дождаться завершения in-flight обработчиков (timeout: 10 секунд)
3. Flush stats buffer в БД
4. Закрыть БД-соединение
5. Exit

### Logging

Structured logging (JSON). Каждое сообщение содержит: `chat_id`, `user_id`, `command`, `duration_ms`, `error` (если есть).

Уровни: ERROR (Telegram API failures, DB errors), WARN (rate limits, permission issues), INFO (commands executed), DEBUG (message processing).

**Никогда не логировать:** текст сообщений пользователей, содержимое профилей, bot token.

### Миграция group -> supergroup

При получении ошибки `migrate_to_chat_id` в ответе API:
1. Обновить все записи в БД со старым abs(chat_id) на новый abs(chat_id)
2. Повторить оригинальный API-вызов с новым chat_id
3. Залогировать миграцию
4. Инвалидировать admin cache для старого chat_id

### Потеря admin-прав во время операции

Если Telegram API возвращает ошибку `"not enough rights"` во время restrict/ban:
1. Инвалидировать кеш admin list
2. Перепроверить права бота через `getChatMember`
3. Ответить пользователю: "Bot lost administrator rights. Please re-promote the bot."
4. Залогировать

---

## Technical Constraints

### Database

Key-value store с поддержкой prefix scan. Минимальные требования:
- CRUD по ключу `{entity}:{user_id}:{abs(chat_id)}`
- Prefix scan: все профили чата (`profile:*:{abs(chat_id)}`), все warnings пользователя в чате
- Persistent (переживает рестарт)

Не требуется: SQL, relations, complex queries. Простой embedded KV (BoltDB, Badger, SQLite в KV-режиме).

### Deployment

Single binary. Конфигурация через env vars:
- `TG_BOT_TOKEN` - Telegram bot token (required)
- `DB_PATH` - путь к файлу/директории БД (default: `./data`)
- `LOG_LEVEL` - уровень логирования (default: `info`)

### Performance targets

- Обработка update: < 100ms (p95)
- Memory: < 100MB при 50 активных чатах
- CPU: minimal (polling + lightweight handlers)
