# Библиотечные контракты: clj-tg-bot-api & zen

> Практическое руководство по использованию библиотек в BidloBot

---

## 1. clj-tg-bot-api

### 1.1 Зависимости

```clojure
;; deps.edn
{:deps
 {com.github.marksto/clj-tg-bot-api {:mvn/version "2.6.0"}
  com.github.oliyh/martian-hato {:mvn/version "0.1.29"}}}
```

### 1.2 Инициализация клиента

```clojure
(ns bidlobot.tg.client
  (:require [marksto.clj-tg-bot-api.core :as tg]))

(defn create-client [config]
  (tg/->client
    {:bot-token    (:token config)
     :limiter-opts {:send-message {:per-chat {:rate 1}
                                   :in-total {:rate 30}}}}))

;; Для тестов
(defn create-test-client []
  (tg/->client
    {:bot-token    "test-token"
     :limiter-opts nil  ; отключить rate limiter
     :responses    {:get-me {:status 200
                             :body {:ok true
                                    :result {:id 123
                                             :is_bot true}}}}}))
```

### 1.3 Контракт: Update Loop

```clojure
(ns bidlobot.tg.polling
  (:require [marksto.clj-tg-bot-api.core :as tg]
            [marksto.clj-tg-bot-api.utils :as tg-utils]))

(def allowed-updates
  ["message" "edited_message" "callback_query" "inline_query"])

(defn poll-updates [client handler]
  (loop [offset 0]
    (let [updates (tg/make-request! client :get-updates
                    {:offset offset
                     :timeout 30
                     :limit 100
                     :allowed-updates allowed-updates})]
      (doseq [update updates]
        (handler update))
      (when (seq updates)
        (recur (inc (apply max (map :update_id updates))))))))
```

### 1.4 Контракт: Update Router

```clojure
(ns bidlobot.tg.router
  (:require [marksto.clj-tg-bot-api.utils :as tg-utils]))

(defmulti handle-update 
  (fn [client update]
    (tg-utils/get-update-type update)))

(defmethod handle-update :message [client update]
  ;; => {:message {...}}
  )

(defmethod handle-update :callback_query [client update]
  ;; => {:callback_query {...}}
  )

(defmethod handle-update :inline_query [client update]
  ;; => {:inline_query {...}}
  )

(defmethod handle-update :default [_ _] nil)
```

### 1.5 Контракт: Inline Query Handler

```clojure
(ns bidlobot.tg.inline
  (:require [marksto.clj-tg-bot-api.core :as tg]))

;; Структура inline_query из update:
;; {:id "AQADBA..."
;;  :from {:id 123456 :username "user" ...}
;;  :query "поисковый текст"
;;  :offset ""
;;  :chat_type "sender"}

(defn answer-inline [client query-id results]
  (tg/make-request! client :answer-inline-query
    {:inline-query-id query-id
     :results results
     :cache-time 0       ; не кешировать
     :is-personal true})) ; персональные результаты

;; Формат результата (article):
(defn make-article [id title description text]
  {:type "article"
   :id id
   :title title
   :description description
   :input-message-content {:message-text text}})

;; Формат результата (профиль пользователя):
(defn make-profile-article [username field value]
  {:type "article"
   :id (str "profile-" username "-" field)
   :title (str username "'s " field)
   :description (str value)
   :input-message-content {:message-text (format "👤 @%s\n%s: %s" 
                                                  username field value)}})
```

### 1.6 Контракт: Callback Query Handler

```clojure
(ns bidlobot.tg.callback
  (:require [marksto.clj-tg-bot-api.core :as tg]))

;; Структура callback_query из update:
;; {:id "123456"
;;  :from {:id 123456 ...}
;;  :message {:message_id 1 :chat {:id 123456} :text "..."}
;;  :data "form:next"}

(defn answer-callback [client callback-id & [text]]
  (tg/make-request! client :answer-callback-query
    {:callback-query-id callback-id
     :text text
     :show-alert false}))

(defn edit-message [client chat-id msg-id text keyboard]
  (tg/make-request! client :edit-message-text
    {:chat-id chat-id
     :message-id msg-id
     :text text
     :parse-mode "HTML"
     :reply-markup {:inline-keyboard keyboard}}))
```

### 1.7 Контракт: Sending Messages

```clojure
(ns bidlobot.tg.message
  (:require [marksto.clj-tg-bot-api.core :as tg]))

(defn send-message [client chat-id text]
  (tg/make-request! client :send-message
    {:chat-id chat-id
     :text text}))

(defn send-with-keyboard [client chat-id text buttons]
  ;; buttons = [[{:text "Btn1" :callback_data "btn1"}]
  ;;            [{:text "Btn2" :callback_data "btn2"}]]
  (tg/make-request! client :send-message
    {:chat-id chat-id
     :text text
     :reply-markup {:inline-keyboard buttons}}))

;; Кнопки навигации формы
(def form-nav-buttons
  [[{:text "◀️ Назад" :callback_data "form:back"}
    {:text "⏭️ Пропустить" :callback_data "form:skip"}]
   [{:text "❌ Отмена" :callback_data "form:cancel"}]])

(def confirm-buttons
  [[{:text "✅ Подтвердить" :callback_data "form:confirm"}
    {:text "◀️ Назад" :callback_data "form:back"}]
   [{:text "❌ Отмена" :callback_data "form:cancel"}]])
```

### 1.8 Контракт: Utils

```clojure
(ns bidlobot.tg.utils
  (:require [marksto.clj-tg-bot-api.utils :as tg-utils]))

;; Типы updates
;; tg-utils/get-update-type update => :message | :callback_query | :inline_query | ...

;; Получение данных из update
;; tg-utils/get-update-chat update => {:id 123 :type "private"}
;; tg-utils/get-update-message update => {:message_id 1 :text "..."}

;; Константы
;; tg-utils/parse-mode:md => "MarkdownV2"
;; tg-utils/parse-mode:html => "HTML"

;; Проверки
;; tg-utils/is-private? chat-type => true/false
;; tg-utils/is-group? chat-type => true/false
```

---

## 2. zen-lang/zen

### 2.1 Зависимости

```clojure
;; deps.edn
{:deps
 {zen-lang/zen {:mvn/version "0.0.1-SNAPSHOT"}}}
```

### 2.2 Контракт: Context Initialization

```clojure
(ns bidlobot.zen.core
  (:require [zen.core :as zen]))

(defn create-context [paths env]
  (zen/new-context
    {:paths paths
     :env env}))

(defn load-spec! [ztx namespace]
  (zen/read-ns ztx namespace)
  (when-let [errs (zen/errors ztx)]
    (throw (ex-info "Zen loading errors" {:errors errs})))
  ztx)

;; Использование:
(def ztx (-> (create-context ["resources"] 
                             {"TG_BOT_TOKEN" "123:ABC"
                              "DB_HOST" "localhost"})
             (load-spec! 'bidlobot.bot)))
```

### 2.3 Контракт: Schema Definition

```edn
;; resources/bidlobot/bot.edn
{ns bidlobot.bot

 ;; === TAGS ===
 
 ;; Тег для схем профиля
 profile-field
 {:zen/tags #{zen/tag zen/schema}
  :zen/desc "Profile field definition"
  :type zen/map
  :keys {:type {:type zen/keyword 
                :enum [{:value :string}
                       {:value :integer}
                       {:value :boolean}]}
         :required {:type zen/boolean}
         :zen/desc {:type zen/string}
         :prompt {:type zen/string}}}

 ;; Тег для inline команд
 inline-command
 {:zen/tags #{zen/tag}
  :zen/desc "Inline query command"}

 ;; Тег для обработчиков
 handler
 {:zen/tags #{zen/tag}
  :zen/desc "Bot handler"}

 ;; === PROFILE SCHEMA ===
 
 profile
 {:zen/tags #{zen/schema}
  :zen/desc "User profile schema"
  :type zen/map
  :keys {:salary {:type zen/string :zen/desc "Salary expectation"}
         :stack {:type zen/string :zen/desc "Tech stack"}
         :role {:type zen/string :zen/desc "Current role"}
         :location {:type zen/string :zen/desc "Location"}
         :bio {:type zen/string :zen/desc "About" :max-length 500}}}

 ;; === PROFILE FIELDS (тегированные) ===
 
 salary
 {:zen/tags #{profile-field}
  :type :string
  :required true
  :zen/desc "Salary expectation"
  :prompt "Your salary expectation?"}

 stack
 {:zen/tags #{profile-field}
  :type :string
  :required false
  :zen/desc "Tech stack"
  :prompt "Your tech stack?"}

 ;; === INLINE COMMANDS ===
 
 user-command
 {:zen/tags #{inline-command}
  :command :user
  :zen/desc "Query user profile"
  :examples [":user veschin :get :salary"
             ":user veschin :profile"]}

 help-command
 {:zen/tags #{inline-command}
  :command :help
  :zen/desc "Show help"}

 ;; === CONFIG ===
 
 config
 {:zen/tags #{zen/schema}
  :type zen/map
  :keys {:token {:type zen/string}
         :default-language {:type zen/keyword}}}

 bot-config
 {:zen/tags #{config}
  :token #env TG_BOT_TOKEN
  :default-language :en}

 ;; === I18N ===
 
 i18n
 {:en {:form/title "Registration"
       :form/back "Back"
       :form/next "Next"
       :form/skip "Skip"
       :form/cancel "Cancel"
       :form/confirm "Confirm"}
  :ru {:form/title "Регистрация"
       :form/back "Назад"
       :form/next "Далее"
       :form/skip "Пропустить"
       :form/cancel "Отмена"
       :form/confirm "Подтвердить"}}}
```

### 2.4 Контракт: Getting Data from Zen

```clojure
(ns bidlobot.zen.queries
  (:require [zen.core :as zen]))

;; Получить конкретную модель
(defn get-config [ztx]
  (zen/get-symbol ztx 'bidlobot.bot/bot-config))
;; => {:token "123:ABC" :default-language :en}

;; Получить все поля профиля (по тегу)
(defn get-profile-fields [ztx]
  (zen/get-tagged ztx 'bidlobot.bot/profile-field))
;; => [{:zen/tags #{...} :type :string :required true ...} ...]

;; Получить все inline команды (по тегу)
(defn get-inline-commands [ztx]
  (zen/get-tagged ztx 'bidlobot.bot/inline-command))
;; => [{:command :user :examples [...]} ...]

;; Получить i18n данные
(defn get-i18n [ztx lang]
  (-> (zen/get-symbol ztx 'bidlobot.bot/i18n)
      (get lang)))
;; => {:form/title "Registration" ...}
```

### 2.5 Контракт: Validation

```clojure
(ns bidlobot.zen.validation
  (:require [zen.core :as zen]))

;; Валидация данных профиля
(defn validate-profile [ztx data]
  (let [result (zen/validate ztx 
                             ['bidlobot.bot/profile]
                             data)]
    (if (empty? (:errors result))
      {:valid true}
      {:valid false
       :errors (:errors result)})))

;; Пример ошибки:
;; {:errors [{:path [:salary]
;;            :message "is required"
;;            :type "require"}]}
```

### 2.6 Контракт: Form Steps from Zen

```clojure
(ns bidlobot.form.steps
  (:require [zen.core :as zen]
            [clojure.string :as str]))

;; Генерация шагов формы из zen модели
(defn build-form-steps [ztx]
  (let [fields (zen/get-tagged ztx 'bidlobot.bot/profile-field)
        sorted (sort-by (fn [f] (if (:required f) 0 1)) fields)]
    (map-indexed
      (fn [idx field]
        {:step (inc idx)
         :field (-> field :zen/name name keyword)
         :prompt (:prompt field)
         :required (:required field)
         :type (:type field)})
      sorted)))

;; => [{:step 1 :field :salary :prompt "Your salary?" :required true}
;;     {:step 2 :field :stack :prompt "Your tech stack?" :required false}
;;     ...]
```

### 2.7 Контракт: Env Variables

```edn
;; В bot.edn:
bot-config
{:zen/tags #{config}
 :token #env TG_BOT_TOKEN           ;; обязательная переменная
 :host #env [DB_HOST "localhost"]   ;; с дефолтом
 :port #env-integer [DB_PORT 5432]  ;; как integer
 :debug #env-boolean [DEBUG false]} ;; как boolean
```

```clojure
;; При создании context:
(def ztx (zen/new-context 
           {:paths ["resources"]
            :env {"TG_BOT_TOKEN" (System/getenv "TG_BOT_TOKEN")
                  "DB_HOST" (System/getenv "DB_HOST")
                  "DB_PORT" (System/getenv "DB_PORT")
                  "DEBUG" (System/getenv "DEBUG")}}))
```

---

## 3. Интеграционные контракты

### 3.1 Bot Initialization Flow

```clojure
(ns bidlobot.core
  (:require [bidlobot.zen.core :as zen]
            [bidlobot.tg.client :as tg]
            [bidlobot.tg.polling :as polling]
            [bidlobot.tg.router :as router]))

(defn start-bot! []
  ;; 1. Загружаем конфигурацию из zen
  (let [ztx (zen/create-context ["resources"] 
                                (System/getenv))
        _ (zen/load-spec! ztx 'bidlobot.bot)
        
        config (zen/get-config ztx)
        
        ;; 2. Создаем Telegram клиент
        client (tg/create-client config)
        
        ;; 3. Передаем ztx в router для доступа к схемам
        handler (partial router/handle-update client ztx)]
    
    ;; 4. Запускаем polling
    (polling/poll-updates client handler)
    
    {:ztx ztx
     :client client}))
```

### 3.2 Form Flow с Zen

```clojure
(ns bidlobot.form.machine
  (:require [bidlobot.zen.queries :as zq]
            [bidlobot.tg.message :as msg]
            [bidlobot.tg.callback :as cb]))

;; Шаги генерируются из zen
(defn init-form [ztx client chat-id]
  (let [steps (zq/build-form-steps ztx)
        i18n (zq/get-i18n ztx :ru)] ;; или :en
    
    ;; Отправляем первый шаг
    (msg/send-with-keyboard 
      client chat-id
      (format "%s\n\n%s" 
              (:form/title i18n)
              (:prompt (first steps)))
      [[{:text (:form/next i18n) :callback_data "form:next"}
        {:text (:form/skip i18n) :callback_data "form:skip"}]
       [{:text (:form/cancel i18n) :callback_data "form:cancel"}]])))
```

### 3.3 Inline Query с Zen Commands

```clojure
(ns bidlobot.query.handler
  (:require [bidlobot.zen.queries :as zq]
            [bidlobot.tg.inline :as inline]))

;; Команды загружаются из zen
(defn handle-inline-query [ztx client update]
  (let [iq (:inline_query update)
        query-id (:id iq)
        query-text (:query iq)
        commands (zq/get-inline-commands ztx)]
    
    ;; Парсим query и генерируем результаты
    (let [results (parse-and-execute ztx query-text commands)]
      (inline/answer-inline client query-id results))))
```

---

## 4. Тестовые контракты

### 4.1 Mock Telegram Client

```clojure
(defn mock-tg-client []
  (tg/->client
    {:bot-token "test"
     :limiter-opts nil
     :responses
     {:get-me {:status 200 :body {:ok true :result {:id 123}}}
      :send-message {:status 200 :body {:ok true :result {:message_id 1}}}
      :answer-inline-query {:status 200 :body {:ok true}}
      :answer-callback-query {:status 200 :body {:ok true}}}}))
```

### 4.2 Test Zen Context

```clojure
(defn test-zen-context []
  (zen/new-context
    {:memory-store
     {'bidlobot.test
      '{:ns bidlobot.test
        profile-field {:zen/tags #{zen/tag zen/schema}
                       :type zen/map}
        test-field {:zen/tags #{profile-field}
                    :type :string
                    :required true}}}}))
```

---

## 5. Краткий справочник API

### clj-tg-bot-api

| Функция | Назначение |
|---------|------------|
| `tg/->client` | Создание клиента |
| `tg/make-request!` | Выполнение API запроса |
| `tg/build-response` | Webhook ответ |
| `tg-utils/get-update-type` | Тип update |
| `tg-utils/get-update-chat` | Chat из update |
| `tg-utils/get-update-message` | Message из update |

### zen

| Функция | Назначение |
|---------|------------|
| `zen/new-context` | Создание контекста |
| `zen/read-ns` | Загрузка namespace |
| `zen/get-symbol` | Получить модель по имени |
| `zen/get-tag` | Символы с тегом (set) |
| `zen/get-tagged` | Модели с тегом (vector) |
| `zen/validate` | Валидация данных |
| `zen/errors` | Ошибки загрузки |
