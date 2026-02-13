(ns validate-zen
  (:require [zen.core :as zen]))

(defn validate-project []
  (println "Loading zen project from zrc/...")

  ;; Создаём контекст с тестовыми env переменными (keywords!)
  (let [ztx (zen/new-context
             {:paths ["zrc"]
              :env {:TG_BOT_TOKEN "test-token-123"
                    :TG_API_URL "https://api.telegram.org"
                    :DEFAULT_LANG "en"
                    :DEBUG "false"}})]

    ;; Загружаем основной namespace
    (println "\nLoading namespace: bidlobot.bot")
    (zen/read-ns ztx 'bidlobot.bot)

    ;; Проверяем ошибки загрузки
    (if-let [errs (seq (zen/errors ztx :order :as-is))]
      (do
        (println "\n❌ Errors found:")
        (doseq [err errs]
          (println "  -" err)))
      (println "\n✅ No loading errors"))

    ;; Показываем что загрузилось
    (println "\n📋 Loaded schemas:")
    (doseq [sym (zen/get-tag ztx 'zen/schema)]
      (println "  -" sym))

    ;; Проверяем что env переменные подставились
    (println "\n📋 Bot config (with env vars):")
    (let [cfg (zen/get-symbol ztx 'bidlobot.bot/bot-config)]
      (println "  token:" (pr-str (:token cfg)))
      (println "  api-url:" (:api-url cfg))
      (println "  default-language:" (:default-language cfg))
      (println "  debug:" (:debug cfg)))

    (println "\n📋 Profile fields:")
    (doseq [field (zen/get-tagged ztx 'bidlobot.bot/profile-field)]
      (println "  -" (or (:zen/desc field)
                         (-> field :zen/name str)
                         "unnamed")))

    (println "\n📋 Inline commands:")
    (doseq [cmd (zen/get-tagged ztx 'bidlobot.bot/inline-command)]
      (println "  -" (:command cmd) ":" (:zen/desc cmd)))

    (println "\n📋 Bot commands:")
    (doseq [cmd (zen/get-tagged ztx 'bidlobot.bot/bot-command)]
      (println "  -" (:command cmd) ":" (:zen/desc cmd)))

    ;; Тестовая валидация данных профиля
    (println "\n🧪 Testing profile validation:")
    (let [valid-profile {:user-id 123
                         :chat-id 456
                         :username "testuser"
                         :salary "100k"
                         :stack "Clojure"
                         :role "Engineer"}
          result (zen/validate ztx ['bidlobot.bot/profile] valid-profile)]
      (if (empty? (:errors result))
        (println "  ✅ Valid profile accepted")
        (println "  ❌ Validation errors:" (:errors result))))

    ;; Тест невалидных данных
    (let [invalid-profile {:user-id "not-a-number"}
          result (zen/validate ztx ['bidlobot.bot/profile] invalid-profile)]
      (if (seq (:errors result))
        (println "  ✅ Invalid profile correctly rejected:"
                 (-> (:errors result) first :type))
        (println "  ❌ Should have rejected invalid profile")))

    (println "\n✅ Validation complete!")))

(validate-project)
