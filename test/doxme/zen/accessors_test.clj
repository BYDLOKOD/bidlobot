(ns doxme.zen.accessors-test
  (:require [clojure.test :refer [deftest testing is are use-fixtures]]
            [doxme.zen.loader :as loader]
            [doxme.zen.accessors :as sut]
            [clojure.string :as str]))

;; ============================================================
;; Fixtures — load real zen context from zrc/doxme/bot.edn
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
;; get-config
;; ============================================================

(deftest get-config-test
  (testing "get-config returns full configuration map"
    (let [config (sut/get-config *ztx*)]
      (is (map? config))
      (is (every? #(contains? config %) [:token :api-url :default-language :debug]))
      (is (= "7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi" (:token config)))
      (is (= "https://api.telegram.org" (:api-url config)))
      (is (= :en (:default-language config)))
      (is (= false (:debug config))))))

;; ============================================================
;; get-profile-fields
;; ============================================================

(deftest get-profile-fields-test
  (testing "get-profile-fields returns all fields in deterministic order"
    (let [fields (sut/get-profile-fields *ztx*)]
      (is (vector? fields))
      (is (= 5 (count fields)))

      ;; Sorted: required first alphabetically, then optional alphabetically
      (testing "field 0 is role (required, alphabetically first)"
        (let [f (nth fields 0)]
          (is (= :role (:name f)))
          (is (= :string (:type f)))
          (is (= true (:required f)))
          (is (= "Your current role? (e.g., Senior Engineer)" (:prompt f)))
          (is (= "Current job role" (:zen/desc f)))))

      (testing "field 1 is salary"
        (let [f (nth fields 1)]
          (is (= :salary (:name f)))
          (is (= :string (:type f)))
          (is (= true (:required f)))
          (is (= "Your salary expectation? (e.g., 100k USD)" (:prompt f)))
          (is (= "Salary expectation" (:zen/desc f)))))

      (testing "field 2 is stack"
        (let [f (nth fields 2)]
          (is (= :stack (:name f)))
          (is (= :string (:type f)))
          (is (= true (:required f)))
          (is (= "Your technology stack? (e.g., Clojure, TypeScript)" (:prompt f)))
          (is (= "Technology stack" (:zen/desc f)))))

      (testing "field 3 is bio (optional, has max-length)"
        (let [f (nth fields 3)]
          (is (= :bio (:name f)))
          (is (= false (:required f)))
          (is (= 500 (:max-length f)))))

      (testing "field 4 is location (optional)"
        (let [f (nth fields 4)]
          (is (= :location (:name f)))
          (is (= :string (:type f)))
          (is (= false (:required f)))
          (is (= "Your location or timezone? (optional)" (:prompt f)))
          (is (= "Location or timezone" (:zen/desc f))))))))

(deftest get-profile-fields-empty-test
  (testing "get-profile-fields returns empty vector when no symbols tagged"
    ;; Create a minimal zen context without profile-field tags
    (let [tmp-dir (str (System/getProperty "java.io.tmpdir") "/doxme-test-noprofile-" (System/nanoTime))
          ns-dir  (clojure.java.io/file tmp-dir "doxme")]
      (try
        (.mkdirs ns-dir)
        (spit (clojure.java.io/file ns-dir "bot.edn")
              (str "{ns doxme.bot\n"
                   " profile-field {:zen/tags #{zen/tag zen/schema} :type zen/map :keys {:type {:type zen/keyword} :prompt {:type zen/string}}}\n"
                   " config {:zen/tags #{zen/tag zen/schema} :type zen/map :require #{:token} :keys {:token {:type zen/string}}}\n"
                   " bot-config {:zen/tags #{config} :token \"test\"}}"))
        (let [ztx (loader/create-context [tmp-dir] {:env env-map})]
          (when-not (contains? ztx :error)
            (let [fields (sut/get-profile-fields ztx)]
              (is (= [] fields)))))
        (finally
          (run! clojure.java.io/delete-file
                (reverse (file-seq (clojure.java.io/file tmp-dir)))))))))

;; ============================================================
;; get-inline-commands
;; ============================================================

(deftest get-inline-commands-test
  (testing "get-inline-commands returns all inline commands sorted by :command"
    (let [cmds (sut/get-inline-commands *ztx*)]
      (is (vector? cmds))
      (is (= 3 (count cmds)))

      (testing "command 0 is :chat"
        (let [c (nth cmds 0)]
          (is (= :chat (:command c)))
          (is (= ":chat :<action>" (:syntax c)))
          (is (= [":chat :users" ":chat :stats"] (:examples c)))))

      (testing "command 1 is :help"
        (let [c (nth cmds 1)]
          (is (= :help (:command c)))
          (is (= ":help [<command>]" (:syntax c)))
          (is (= [":help" ":help :user"] (:examples c)))))

      (testing "command 2 is :user"
        (let [c (nth cmds 2)]
          (is (= :user (:command c)))
          (is (= ":user <username> :get <field>" (:syntax c)))
          (is (some #(= ":user veschin :get :salary" %) (:examples c))))))))

;; ============================================================
;; get-bot-commands
;; ============================================================

(deftest get-bot-commands-test
  (testing "get-bot-commands returns all bot commands sorted by :command"
    (let [cmds (sut/get-bot-commands *ztx*)]
      (is (vector? cmds))
      (is (= 4 (count cmds)))

      (testing "command 0 is /help"
        (let [c (nth cmds 0)]
          (is (= "/help" (:command c)))
          (is (= :help-handler (:handler c)))))

      (testing "command 1 is /profile"
        (let [c (nth cmds 1)]
          (is (= "/profile" (:command c)))
          (is (= :profile-handler (:handler c)))))

      (testing "command 2 is /register"
        (let [c (nth cmds 2)]
          (is (= "/register" (:command c)))
          (is (= :register-handler (:handler c)))))

      (testing "command 3 is /start"
        (let [c (nth cmds 3)]
          (is (= "/start" (:command c)))
          (is (= :start-handler (:handler c))))))))

;; ============================================================
;; get-i18n
;; ============================================================

(deftest get-i18n-english-test
  (testing "get-i18n returns English translations"
    (let [i18n (sut/get-i18n *ztx* :en)]
      (is (map? i18n))
      (is (= 17 (count i18n)))
      (is (= "Profile Registration" (:form/title i18n)))
      (is (= "Step {current} of {total}" (:form/progress i18n)))
      (is (= "BidloBot" (:bot/name i18n)))
      (is (= "Profile not found. Register with /register" (:profile/not-found i18n)))
      (is (= "Invalid command. Use :help for usage." (:error/invalid-command i18n))))))

(deftest get-i18n-russian-test
  (testing "get-i18n returns Russian translations"
    (let [i18n (sut/get-i18n *ztx* :ru)]
      (is (map? i18n))
      (is (= 17 (count i18n)))
      (is (= "\u0420\u0435\u0433\u0438\u0441\u0442\u0440\u0430\u0446\u0438\u044f \u043f\u0440\u043e\u0444\u0438\u043b\u044f" (:form/title i18n)))
      (is (= "\u0428\u0430\u0433 {current} \u0438\u0437 {total}" (:form/progress i18n)))
      (is (= "BidloBot" (:bot/name i18n))))))

(deftest get-i18n-nonexistent-test
  (testing "get-i18n with nonexistent language returns nil"
    (is (nil? (sut/get-i18n *ztx* :nonexistent)))))

;; ============================================================
;; validate-profile
;; ============================================================

(deftest validate-profile-valid-test
  (testing "validate-profile with valid data returns valid true"
    (let [data   {:salary   "150k USD"
                  :stack    "Clojure, TypeScript"
                  :role     "Senior Engineer"
                  :location "UTC+3, Tbilisi"
                  :bio      "Functional programming enthusiast. 8 years in backend."}
          result (sut/validate-profile *ztx* data)]
      (is (= {:valid true} result)))))

(deftest validate-profile-missing-required-test
  (testing "validate-profile with missing required field returns errors"
    (let [data   {:stack "Clojure"}
          result (sut/validate-profile *ztx* data)]
      (is (= false (:valid result)))
      (is (some #(and (= [:salary] (:path %))
                      (str/includes? (str (:message %)) "is required"))
                (:errors result))))))
