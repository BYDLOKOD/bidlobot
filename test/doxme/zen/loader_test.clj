(ns doxme.zen.loader-test
  (:require [clojure.test :refer [deftest testing is are use-fixtures]]
            [doxme.zen.loader :as sut]
            [clojure.java.io :as io]
            [clojure.string :as str]))

;; ============================================================
;; Fixtures & helpers
;; ============================================================

(def env-map
  {"TG_BOT_TOKEN" "7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi"
   "TG_API_URL"   "https://api.telegram.org"
   "DEFAULT_LANG" "en"
   "DEBUG"        "false"})

(def minimal-env-map
  {"TG_BOT_TOKEN" "7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi"})

;; ============================================================
;; Context lifecycle tests
;; ============================================================

(deftest context-creation-test
  (testing "create context with valid config and env map"
    (let [result (sut/create-context ["zrc"] {:env env-map})]
      (is (not (contains? result :error))
          "Valid config should not return error")
      (is (some? result)
          "Result should be a zen context")))

  (testing "create context resolves #env tags from env map"
    (let [ztx (sut/create-context ["zrc"] {:env env-map})]
      (is (not (contains? ztx :error)))
      ;; Actual accessor checks are in accessors_test; here we just verify
      ;; context loads without error with all env vars provided.
      (is (some? ztx)))))

(deftest env-defaults-test
  (testing "environment defaults applied when optional vars are missing"
    (let [ztx (sut/create-context ["zrc"] {:env minimal-env-map})]
      (is (not (contains? ztx :error))
          "Context should load with only TG_BOT_TOKEN set"))))

(deftest missing-zrc-directory-test
  (testing "missing zrc directory returns config-not-found"
    (let [result (sut/create-context ["nonexistent-zrc-path"] {:env env-map})]
      (is (= :config-not-found (:error result))))))

(deftest empty-config-file-test
  (testing "empty bot.edn returns config-parse-error"
    (let [tmp-dir (str (System/getProperty "java.io.tmpdir") "/doxme-test-empty-" (System/nanoTime))
          ns-dir  (io/file tmp-dir "doxme")]
      (try
        (.mkdirs ns-dir)
        (spit (io/file ns-dir "bot.edn") "")
        (let [result (sut/create-context [tmp-dir] {:env env-map})]
          (is (= :config-parse-error (:error result)))
          (is (= "Empty configuration file" (:message result))))
        (finally
          (run! io/delete-file (reverse (file-seq (io/file tmp-dir)))))))))

(deftest malformed-edn-test
  (testing "malformed EDN returns config-parse-error"
    (let [tmp-dir (str (System/getProperty "java.io.tmpdir") "/doxme-test-malformed-" (System/nanoTime))
          ns-dir  (io/file tmp-dir "doxme")]
      (try
        (.mkdirs ns-dir)
        (spit (io/file ns-dir "bot.edn") "{ns doxme.bot\n config {:token \"abc\"")
        (let [result (sut/create-context [tmp-dir] {:env env-map})]
          (is (= :config-parse-error (:error result)))
          (is (str/includes? (str (:message result)) "Unexpected end of input")))
        (finally
          (run! io/delete-file (reverse (file-seq (io/file tmp-dir)))))))))

(deftest missing-ns-declaration-test
  (testing "EDN without ns declaration returns config-validation-error"
    (let [tmp-dir (str (System/getProperty "java.io.tmpdir") "/doxme-test-nons-" (System/nanoTime))
          ns-dir  (io/file tmp-dir "doxme")]
      (try
        (.mkdirs ns-dir)
        (spit (io/file ns-dir "bot.edn")
              "{salary {:zen/tags #{profile-field} :type :string :required true :prompt \"test\"}}")
        (let [result (sut/create-context [tmp-dir] {:env env-map})]
          (is (= :config-validation-error (:error result)))
          (is (some #(= :missing-ns (:type %)) (:errors result))
              "Errors should contain a :missing-ns entry"))
        (finally
          (run! io/delete-file (reverse (file-seq (io/file tmp-dir)))))))))

(deftest missing-required-env-var-test
  (testing "missing required #env variable without default throws with var name"
    (let [result (sut/create-context ["zrc"] {:env {}})]
      (is (= :config-validation-error (:error result)))
      (is (str/includes? (str (:message result)) "TG_BOT_TOKEN")))))

(deftest env-with-default-test
  (testing "#env with default uses default when var is missing"
    (let [ztx (sut/create-context ["zrc"] {:env minimal-env-map})]
      (is (not (contains? ztx :error))
          "Context should load using defaults for optional env vars"))))

(deftest schema-validation-test
  (testing "schema validation catches invalid field values"
    (are [field value path error-type]
         (let [tmp-dir (str (System/getProperty "java.io.tmpdir") "/doxme-test-schema-" (System/nanoTime))
               ns-dir  (io/file tmp-dir "doxme")]
           (try
             (.mkdirs ns-dir)
          ;; Write a minimal config that triggers the specific validation error
             (spit (io/file ns-dir "bot.edn")
                   (case field
                     "profile-field :type"
                     (str "{ns doxme.bot\n"
                          " profile-field {:zen/tags #{zen/tag zen/schema} :type zen/map :require #{:type :prompt} :keys {:type {:type zen/keyword :enum [{:value :string} {:value :integer} {:value :boolean}]} :prompt {:type zen/string}}}\n"
                          " bad-field {:zen/tags #{profile-field} :type :array :prompt \"test\"}}")

                     "bot-config :default-language"
                     (str "{ns doxme.bot\n"
                          " config {:zen/tags #{zen/tag zen/schema} :type zen/map :keys {:token {:type zen/string} :default-language {:type zen/keyword :enum [{:value :en} {:value :ru}]}}}\n"
                          " bot-config {:zen/tags #{config} :token \"test\" :default-language :de}}")

                     "bot-config :debug"
                     (str "{ns doxme.bot\n"
                          " config {:zen/tags #{zen/tag zen/schema} :type zen/map :keys {:token {:type zen/string} :debug {:type zen/boolean}}}\n"
                          " bot-config {:zen/tags #{config} :token \"test\" :debug \"yes\"}}")))
             (let [result (sut/create-context [tmp-dir] {:env env-map})]
               (is (= :config-validation-error (:error result)))
               (is (some #(and (= path (:path %))
                               (= error-type (:type %)))
                         (:errors result))
                   (str "Expected error with path " path " and type " error-type)))
             (finally
               (run! io/delete-file (reverse (file-seq (io/file tmp-dir)))))))

      ;; Examples table rows:
      ;; field                       value     path                error-type
      "profile-field :type"          :array    [:type]             :enum
      "bot-config :default-language" :de       [:default-language] :enum
      "bot-config :debug"            "yes"     [:debug]            :type-mismatch)))

(deftest missing-required-token-test
  (testing "missing required :token in bot-config returns validation error"
    (let [tmp-dir (str (System/getProperty "java.io.tmpdir") "/doxme-test-notoken-" (System/nanoTime))
          ns-dir  (io/file tmp-dir "doxme")]
      (try
        (.mkdirs ns-dir)
        (spit (io/file ns-dir "bot.edn")
              (str "{ns doxme.bot\n"
                   " config {:zen/tags #{zen/tag zen/schema} :type zen/map :require #{:token} :keys {:token {:type zen/string}}}\n"
                   " bot-config {:zen/tags #{config} :default-language :en}}"))
        (let [result (sut/create-context [tmp-dir] {:env env-map})]
          (is (= :config-validation-error (:error result)))
          (is (some #(and (= [:token] (:path %))
                          (str/includes? (str (:message %)) "is required"))
                    (:errors result))))
        (finally
          (run! io/delete-file (reverse (file-seq (io/file tmp-dir)))))))))

(deftest missing-required-prompt-test
  (testing "profile field missing required :prompt key returns validation error"
    (let [tmp-dir (str (System/getProperty "java.io.tmpdir") "/doxme-test-noprompt-" (System/nanoTime))
          ns-dir  (io/file tmp-dir "doxme")]
      (try
        (.mkdirs ns-dir)
        (spit (io/file ns-dir "bot.edn")
              (str "{ns doxme.bot\n"
                   " profile-field {:zen/tags #{zen/tag zen/schema} :type zen/map :require #{:type :prompt} :keys {:type {:type zen/keyword} :prompt {:type zen/string}}}\n"
                   " bad-field {:zen/tags #{profile-field} :type :string}}"))
        (let [result (sut/create-context [tmp-dir] {:env env-map})]
          (is (= :config-validation-error (:error result)))
          (is (some #(and (= [:prompt] (:path %))
                          (str/includes? (str (:message %)) "is required"))
                    (:errors result))))
        (finally
          (run! io/delete-file (reverse (file-seq (io/file tmp-dir)))))))))

(deftest duplicate-ns-declaration-test
  (testing "duplicate ns declaration returns parse error"
    (let [tmp-dir (str (System/getProperty "java.io.tmpdir") "/doxme-test-dupns-" (System/nanoTime))
          ns-dir  (io/file tmp-dir "doxme")]
      (try
        (.mkdirs ns-dir)
        (spit (io/file ns-dir "bot.edn") "{ns doxme.bot\n ns doxme.other}")
        (let [result (sut/create-context [tmp-dir] {:env env-map})]
          (is (= :config-parse-error (:error result)))
          (is (str/includes? (str (:message result)) "Duplicate key: ns")))
        (finally
          (run! io/delete-file (reverse (file-seq (io/file tmp-dir)))))))))
