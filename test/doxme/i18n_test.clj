(ns doxme.i18n-test
  (:require [clojure.test :refer [deftest testing is are use-fixtures]]
            [doxme.i18n :as sut]
            [doxme.zen.loader :as loader]
            [clojure.string :as str]))

;; ============================================================
;; Fixtures — load real zen context
;; ============================================================

(def env-map
  {"TG_BOT_TOKEN" "7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi"
   "TG_API_URL"   "https://api.telegram.org"
   "DEFAULT_LANG" "en"
   "DEBUG"        "false"})

(def ^:dynamic *ztx* nil)

(defn zen-context-fixture [f]
  (let [ztx (loader/create-context ["zrc"] {:env env-map})]
    (assert (not (contains? ztx :error))
            (str "Failed to load zen context: " (pr-str ztx)))
    (binding [*ztx* ztx]
      (f))))

(use-fixtures :once zen-context-fixture)

;; ============================================================
;; Translation lookup
;; ============================================================

(deftest lookup-english-form-title-test
  (testing "lookup English translation for form title"
    (is (= "Profile Registration" (sut/t *ztx* :en :form/title)))))

(deftest lookup-russian-form-title-test
  (testing "lookup Russian translation for form title"
    (is (= "\u0420\u0435\u0433\u0438\u0441\u0442\u0440\u0430\u0446\u0438\u044f \u043f\u0440\u043e\u0444\u0438\u043b\u044f"
           (sut/t *ztx* :ru :form/title)))))

(deftest lookup-english-bot-name-test
  (testing "lookup English bot name"
    (is (= "BidloBot" (sut/t *ztx* :en :bot/name)))))

(deftest lookup-russian-bot-desc-test
  (testing "lookup Russian bot description"
    (is (= "\u0411\u043e\u0442 \u0443\u043f\u0440\u0430\u0432\u043b\u0435\u043d\u0438\u044f \u043f\u0440\u043e\u0444\u0438\u043b\u044f\u043c\u0438 \u0434\u043b\u044f \u043a\u043e\u043c\u0430\u043d\u0434\u043d\u044b\u0445 \u0447\u0430\u0442\u043e\u0432"
           (sut/t *ztx* :ru :bot/desc)))))

(deftest lookup-various-english-translations-test
  (testing "lookup various English translations"
    (are [key expected]
         (= expected (sut/t *ztx* :en key))

      :form/complete          "Registration complete!"
      :form/expired           "Session expired. Please start again with /register"
      :profile/not-found      "Profile not found. Register with /register"
      :profile/saved          "Profile saved successfully!"
      :profile/updated        "Profile updated!"
      :error/invalid-command  "Invalid command. Use :help for usage."
      :error/user-not-found   "User not found in this chat."
      :error/not-registered   "You are not registered. Use /register first.")))

(deftest lookup-various-russian-translations-test
  (testing "lookup various Russian translations"
    (are [key expected]
         (= expected (sut/t *ztx* :ru key))

      :form/complete     "\u0420\u0435\u0433\u0438\u0441\u0442\u0440\u0430\u0446\u0438\u044f \u0437\u0430\u0432\u0435\u0440\u0448\u0435\u043d\u0430!"
      :profile/saved     "\u041f\u0440\u043e\u0444\u0438\u043b\u044c \u0443\u0441\u043f\u0435\u0448\u043d\u043e \u0441\u043e\u0445\u0440\u0430\u043d\u0451\u043d!"
      :profile/updated   "\u041f\u0440\u043e\u0444\u0438\u043b\u044c \u043e\u0431\u043d\u043e\u0432\u043b\u0451\u043d!")))

;; ============================================================
;; Fallback chain
;; ============================================================

(deftest fallback-nonexistent-key-test
  (testing "nonexistent key returns key as string"
    (is (= ":nonexistent/key" (sut/t *ztx* :en :nonexistent/key)))))

(deftest fallback-unsupported-language-test
  (testing "unsupported language falls back to English"
    (is (= "Profile Registration" (sut/t *ztx* :de :form/title))))

  (testing "unsupported language falls back to English for profile/saved"
    (is (= "Profile saved successfully!" (sut/t *ztx* :de :profile/saved)))))

(deftest fallback-full-chain-test
  (testing "full fallback chain - unsupported lang and missing key"
    (is (= ":totally/missing" (sut/t *ztx* :de :totally/missing)))))

(deftest fallback-nil-language-test
  (testing "nil language uses default from config"
    (is (= "Profile Registration" (sut/t *ztx* nil :form/title)))))

(deftest fallback-nil-key-test
  (testing "nil key returns empty string"
    (is (= "" (sut/t *ztx* :en nil)))))

(deftest fallback-nil-context-test
  (testing "nil zen context returns error"
    (is (= {:error :invalid-context} (sut/t nil :en :form/title)))))

;; ============================================================
;; Interpolation
;; ============================================================

(deftest interpolate-english-progress-test
  (testing "interpolate form progress in English"
    (is (= "Step 2 of 5" (sut/t *ztx* :en :form/progress {:current 2 :total 5})))))

(deftest interpolate-russian-progress-test
  (testing "interpolate form progress in Russian"
    (is (= "\u0428\u0430\u0433 2 \u0438\u0437 5"
           (sut/t *ztx* :ru :form/progress {:current 2 :total 5})))))

(deftest interpolate-various-steps-test
  (testing "interpolate progress at various steps"
    (are [current total expected]
         (= expected (sut/t *ztx* :en :form/progress {:current current :total total}))

      1 5 "Step 1 of 5"
      2 5 "Step 2 of 5"
      5 5 "Step 5 of 5"
      0 5 "Step 0 of 5")))

(deftest interpolate-missing-variable-test
  (testing "missing variable in vars map leaves placeholder unchanged"
    (is (= "Step 3 of {total}" (sut/t *ztx* :en :form/progress {:current 3})))))

(deftest interpolate-empty-vars-test
  (testing "empty vars map leaves all placeholders unchanged"
    (is (= "Step {current} of {total}" (sut/t *ztx* :en :form/progress {})))))

(deftest interpolate-nil-value-test
  (testing "nil value in vars map replaced with empty string"
    (is (= "Step  of 5" (sut/t *ztx* :en :form/progress {:current nil :total 5})))))

(deftest interpolate-nil-vars-test
  (testing "nil vars map leaves all placeholders unchanged"
    (is (= "Step {current} of {total}" (sut/t *ztx* :en :form/progress nil)))))

(deftest interpolate-no-placeholders-test
  (testing "template without placeholders returned as-is even with vars"
    (is (= "Profile Registration" (sut/t *ztx* :en :form/title {:foo "bar"})))))

(deftest interpolate-string-values-test
  (testing "string variable values interpolated correctly"
    (is (= "Step two of five"
           (sut/t *ztx* :en :form/progress {:current "two" :total "five"})))))

(deftest interpolate-no-placeholders-complete-test
  (testing "vars passed but template has no placeholders"
    (is (= "Registration complete!"
           (sut/t *ztx* :en :form/complete {:foo "bar"})))))

(deftest interpolate-repeated-placeholder-test
  (testing "same placeholder appears twice in template"
    ;; This requires a custom translation entry; test that interpolation
    ;; handles the same var name appearing multiple times.
    ;; The i18n module should support this via global replace.
    (let [;; We simulate by calling t with a template that has {name} twice.
          ;; If the i18n module uses zen context, we need to verify via an
          ;; entry that has a repeated placeholder. For now, test the
          ;; interpolation function directly.
          result (sut/interpolate "{name} and {name}" {:name "Alice"})]
      (is (= "Alice and Alice" result)))))

(deftest interpolate-fallback-then-interpolate-test
  (testing "fallback to English then interpolate"
    (is (= "Step 3 of 5"
           (sut/t *ztx* :de :form/progress {:current 3 :total 5})))))

;; ============================================================
;; Edge cases: ru-only key, empty string value, simple keyword
;; ============================================================

(deftest ru-only-key-test
  (testing "key exists in :ru but not in :en returns :ru value"
    ;; This test verifies that when a key exists only in the requested
    ;; language and not in the fallback (:en), it still returns the value.
    ;; Requires the i18n module to check the requested language first.
    ;; In a real scenario, this would need a custom zen context with
    ;; an extra :ru-only/key entry. We test the lookup behavior:
    ;; if the key is found in :ru, return it regardless of :en.
    (let [translations {:ru {:ru-only/key "\u0422\u043e\u043b\u044c\u043a\u043e \u0440\u0443\u0441\u0441\u043a\u0438\u0439"}}
          result       (sut/t-with-translations translations :en :ru :ru-only/key)]
      (is (= "\u0422\u043e\u043b\u044c\u043a\u043e \u0440\u0443\u0441\u0441\u043a\u0438\u0439" result)))))

(deftest empty-string-translation-test
  (testing "empty string as translation value is returned as-is"
    (let [translations {:en {:empty/key ""}}
          result       (sut/t-with-translations translations :en :en :empty/key)]
      (is (= "" result)))))

(deftest simple-keyword-test
  (testing "simple keyword without namespace works"
    (let [translations {:en {:greeting "Hello!"}}
          result       (sut/t-with-translations translations :en :en :greeting)]
      (is (= "Hello!" result)))))

;; ============================================================
;; Language detection
;; ============================================================

(deftest detect-russian-from-message-test
  (testing "detect Russian language from message update"
    (let [update {:update_id 100001
                  :message
                  {:message_id 42
                   :from       {:id            294817365
                                :is_bot        false
                                :first_name    "Alexei"
                                :username      "veschin"
                                :language_code "ru"}
                   :chat       {:id   -1001987654321
                                :type "supergroup"}
                   :date       1709625720
                   :text       "/register"}}]
      (is (= :ru (sut/detect-language *ztx* update))))))

(deftest detect-english-from-message-test
  (testing "detect English language from message update"
    (let [update {:update_id 100002
                  :message
                  {:message_id 43
                   :from       {:id            518293746
                                :is_bot        false
                                :first_name    "Anna"
                                :username      "anna_dev"
                                :language_code "en"}
                   :chat       {:id   -1001987654321
                                :type "supergroup"}
                   :date       1709625780
                   :text       "/profile"}}]
      (is (= :en (sut/detect-language *ztx* update))))))

(deftest detect-language-from-inline-query-test
  (testing "detect language from inline_query.from.language_code"
    (let [update {:update_id 100003
                  :inline_query
                  {:id     "AQADBA1234567890"
                   :from   {:id            294817365
                            :is_bot        false
                            :first_name    "Alexei"
                            :username      "veschin"
                            :language_code "ru"}
                   :query  ":user anna_dev :get :salary"
                   :offset ""
                   :chat_type "sender"}}]
      (is (= :ru (sut/detect-language *ztx* update))))))

(deftest detect-language-from-callback-query-test
  (testing "detect language from callback_query.from.language_code"
    (let [update {:update_id 100006
                  :callback_query
                  {:id      "cb_98765"
                   :from    {:id            294817365
                             :is_bot        false
                             :first_name    "Alexei"
                             :username      "veschin"
                             :language_code "ru"}
                   :message {:message_id 50
                             :chat       {:id -1001987654321 :type "supergroup"}
                             :text       "Profile Registration"}
                   :data    "form:next"}}]
      (is (= :ru (sut/detect-language *ztx* update))))))

(deftest detect-unsupported-language-fallback-test
  (testing "unsupported or missing language_code falls back to default"
    (are [code]
         (= :en (sut/detect-language *ztx*
                                     {:update_id 100004
                                      :message
                                      {:message_id 44
                                       :from       {:id            738192045
                                                    :is_bot        false
                                                    :first_name    "Max"
                                                    :username      "max_clj"
                                                    :language_code code}
                                       :chat       {:id -1001987654321 :type "supergroup"}
                                       :date       1709625840
                                       :text       "/help"}}))

      "de"
      "uk")))

(deftest detect-missing-language-code-test
  (testing "missing language_code field entirely falls back to default"
    (let [update {:update_id 100005
                  :message
                  {:message_id 45
                   :from       {:id         901827364
                                :is_bot     false
                                :first_name "NoLang"}
                   :chat       {:id -1001987654321 :type "supergroup"}
                   :date       1709625900
                   :text       "Hello"}}]
      (is (= :en (sut/detect-language *ztx* update))))))

(deftest detect-nil-update-test
  (testing "nil update returns default language"
    (is (= :en (sut/detect-language *ztx* nil)))))

(deftest detect-empty-update-test
  (testing "empty update map returns default language"
    (is (= :en (sut/detect-language *ztx* {})))))

(deftest detect-bcp47-language-code-test
  (testing "BCP47-style language code 'en-US' detects as :en"
    (let [update {:update_id 100007
                  :message
                  {:message_id 46
                   :from       {:id            112233445
                                :is_bot        false
                                :first_name    "John"
                                :username      "john_us"
                                :language_code "en-US"}
                   :chat       {:id -1001987654321 :type "supergroup"}
                   :date       1709625960
                   :text       "/start"}}]
      (is (= :en (sut/detect-language *ztx* update))))))

(deftest detect-ukrainian-fallback-test
  (testing "Ukrainian language_code falls back to default"
    (let [update {:update_id 100008
                  :message
                  {:message_id 47
                   :from       {:id            556677889
                                :is_bot        false
                                :first_name    "Taras"
                                :username      "taras_ua"
                                :language_code "uk"}
                   :chat       {:id -1001987654321 :type "supergroup"}
                   :date       1709626020
                   :text       "/register"}}]
      (is (= :en (sut/detect-language *ztx* update))))))
