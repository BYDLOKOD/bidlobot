(ns doxme.query.parser-test
  (:require [clojure.test :refer [deftest testing is are]]
            [doxme.query.parser :as sut]))

;; ============================================================
;; Query Language Parser Tests
;; BDD source: .ptsd/bdd/query-lang.feature
;; Seed data:  .ptsd/seeds/query-lang/
;; ============================================================

;; --- Parsing known commands ---

(deftest parse-user-command
  (testing "Parse :user command with :get field"
    (is (= {:cmd :user :args ["veschin" :get :salary]}
           (sut/parse ":user veschin :get :salary"))))

  (testing "Parse :user command with :profile subcommand"
    (is (= {:cmd :user :args ["veschin" :profile]}
           (sut/parse ":user veschin :profile"))))

  (testing "Parse :user command with no args"
    (is (= {:cmd :user :args []}
           (sut/parse ":user")))))

(deftest parse-chat-command
  (testing "Parse :chat command with :stats arg"
    (is (= {:cmd :chat :args [:stats]}
           (sut/parse ":chat :stats"))))

  (testing "Parse :chat command with multiple keyword args"
    (is (= {:cmd :chat :args [:stats :top]}
           (sut/parse ":chat :stats :top"))))

  (testing "Parse :chat command with no args"
    (is (= {:cmd :chat :args []}
           (sut/parse ":chat")))))

(deftest parse-help-command
  (testing "Parse :help command with no args"
    (is (= {:cmd :help :args []}
           (sut/parse ":help"))))

  (testing "Parse :help command with topic arg"
    (is (= {:cmd :help :args [:user]}
           (sut/parse ":help :user"))))

  (testing "Parse :help command with :chat topic"
    (is (= {:cmd :help :args [:chat]}
           (sut/parse ":help :chat")))))

;; --- Parametrized valid :user queries for all profile fields ---

(deftest parse-user-get-all-profile-fields
  (testing "Parse :user :get for each profile field"
    (are [field field-kw]
         (= {:cmd :user :args ["veschin" :get field-kw]}
            (sut/parse (str ":user veschin :get " field)))

      ":salary"   :salary
      ":stack"    :stack
      ":role"     :role
      ":location" :location
      ":bio"      :bio)))

;; --- Token rules ---

(deftest token-rules
  (testing "Token starting with colon is parsed as keyword"
    (is (= {:cmd :chat :args [:stats :top :today]}
           (sut/parse ":chat :stats :top :today"))))

  (testing "Token without colon is parsed as string word"
    (let [result (sut/parse ":user veschin :get :salary")]
      (is (string? (get (:args result) 0)))
      (is (= "veschin" (get (:args result) 0)))))

  (testing "Username with underscore is parsed as word"
    (is (= {:cmd :user :args ["dev_ops" :get :salary]}
           (sut/parse ":user dev_ops :get :salary"))))

  (testing "Username with hyphen is parsed as word"
    (is (= {:cmd :user :args ["maria-fe" :get :stack]}
           (sut/parse ":user maria-fe :get :stack"))))

  (testing "Username with mixed underscores and hyphens"
    (is (= {:cmd :user :args ["a_b-c_d" :profile]}
           (sut/parse ":user a_b-c_d :profile"))))

  (testing "Username with leading underscores"
    (is (= {:cmd :user :args ["__leading" :get :role]}
           (sut/parse ":user __leading :get :role"))))

  (testing "Single character username"
    (is (= {:cmd :user :args ["x" :get :salary]}
           (sut/parse ":user x :get :salary"))))

  (testing "Alphanumeric username"
    (is (= {:cmd :user :args ["user123" :get :stack]}
           (sut/parse ":user user123 :get :stack"))))

  (testing "Query must start with colon followed by known command"
    (is (= :user (:cmd (sut/parse ":user alex_dev :get :salary"))))))

;; --- Whitespace handling ---

(deftest whitespace-handling
  (testing "Multiple spaces between tokens are ignored"
    (is (= {:cmd :user :args ["veschin" :get :salary]}
           (sut/parse ":user   veschin   :get   :salary"))))

  (testing "Leading and trailing whitespace is trimmed"
    (is (= {:cmd :help :args []}
           (sut/parse "  :help  "))))

  (testing "Tab characters between tokens are treated as whitespace"
    (is (= {:cmd :user :args ["veschin" :get :salary]}
           (sut/parse ":user\tveschin\t:get\t:salary"))))

  (testing "Mixed spaces and tabs between tokens"
    (is (= {:cmd :user :args ["veschin" :profile]}
           (sut/parse ":user  \t  veschin  \t  :profile"))))

  (testing "Leading spaces before command"
    (is (= {:cmd :user :args ["veschin" :profile]}
           (sut/parse "   :user veschin :profile"))))

  (testing "Trailing spaces after command with no args"
    (is (= {:cmd :help :args []}
           (sut/parse ":help   ")))))

;; --- Error handling: empty and whitespace ---

(deftest error-empty-and-whitespace
  (testing "Empty string returns empty-query error"
    (is (= {:error :empty-query}
           (sut/parse ""))))

  (testing "Whitespace-only string returns empty-query error"
    (is (= {:error :empty-query}
           (sut/parse "   "))))

  (testing "Tab-only string returns empty-query error"
    (is (= {:error :empty-query}
           (sut/parse "\t\t"))))

  (testing "Newline-only string returns empty-query error"
    (is (= {:error :empty-query}
           (sut/parse "\n")))))

;; --- Error handling: missing colon prefix ---

(deftest error-invalid-syntax-no-colon
  (testing "String without leading colon returns invalid-syntax"
    (are [input]
         (= {:error :invalid-syntax}
            (sut/parse input))

      "no colon"
      "user veschin"
      "help")))

;; --- Error handling: unknown commands ---

(deftest error-unknown-command
  (testing "Unknown command returns unknown-command error"
    (are [input command]
         (let [result (sut/parse input)]
           (and (= :unknown-command (:error result))
                (= command (:command result))))

      ":unknown-cmd foo"   "unknown-cmd"
      ":search veschin"    "search"
      ":ban user123"       "ban"
      ":stats"             "stats")))

;; --- Error handling: uppercase commands ---

(deftest error-uppercase-command
  (testing "Uppercase command name returns unknown-command error"
    (are [input command]
         (let [result (sut/parse input)]
           (and (= :unknown-command (:error result))
                (= command (:command result))))

      ":USER veschin"    "USER"
      ":Help"            "Help"
      ":CHAT :stats"     "CHAT"
      ":HELP"            "HELP"
      ":User veschin"    "User")))

;; --- Error handling: invalid syntax patterns ---

(deftest error-invalid-syntax-patterns
  (testing "Bare colon returns invalid-syntax"
    (is (= {:error :invalid-syntax}
           (sut/parse ":"))))

  (testing "Colon followed by space returns invalid-syntax"
    (is (= {:error :invalid-syntax}
           (sut/parse ": "))))

  (testing "Double colon returns invalid-syntax"
    (is (= {:error :invalid-syntax}
           (sut/parse "::"))))

  (testing "Double colon in token without space"
    (is (= {:error :invalid-syntax}
           (sut/parse ":user::name"))))

  (testing "Double colon in arguments"
    (is (= {:error :invalid-syntax}
           (sut/parse ":user veschin ::get :salary")))))

;; --- Error handling: query length ---

(deftest error-query-too-long
  (testing "Query exceeding 500 characters returns query-too-long error"
    (let [long-username (apply str (repeat 500 "a"))
          long-query (str ":user " long-username)]
      (is (> (count long-query) 500))
      (is (= {:error :query-too-long}
             (sut/parse long-query))))))

;; --- Seed data coverage: valid-queries.edn ---

(deftest seed-valid-queries
  (testing "All valid query seeds from valid-queries.edn"
    (are [input expected]
         (= expected (sut/parse input))

      ":user veschin :get :salary"       {:cmd :user :args ["veschin" :get :salary]}
      ":user veschin :get :stack"        {:cmd :user :args ["veschin" :get :stack]}
      ":user veschin :get :role"         {:cmd :user :args ["veschin" :get :role]}
      ":user veschin :get :location"     {:cmd :user :args ["veschin" :get :location]}
      ":user veschin :get :bio"          {:cmd :user :args ["veschin" :get :bio]}
      ":user veschin :profile"           {:cmd :user :args ["veschin" :profile]}
      ":user alex_dev :get :salary"      {:cmd :user :args ["alex_dev" :get :salary]}
      ":user maria_fe :profile"          {:cmd :user :args ["maria_fe" :profile]}
      ":user"                            {:cmd :user :args []}
      ":chat :stats"                     {:cmd :chat :args [:stats]}
      ":chat :stats :top"               {:cmd :chat :args [:stats :top]}
      ":chat :users"                     {:cmd :chat :args [:users]}
      ":chat"                            {:cmd :chat :args []}
      ":help"                            {:cmd :help :args []}
      ":help :user"                      {:cmd :help :args [:user]}
      ":help :chat"                      {:cmd :help :args [:chat]}
      ":user   veschin   :get   :salary" {:cmd :user :args ["veschin" :get :salary]}
      "  :help  "                        {:cmd :help :args []})))

;; --- Seed data coverage: invalid-queries.edn ---

(deftest seed-invalid-queries
  (testing "All invalid query seeds from invalid-queries.edn"
    ;; empty / whitespace
    (is (= {:error :empty-query} (sut/parse "")))
    (is (= {:error :empty-query} (sut/parse "   ")))
    (is (= {:error :empty-query} (sut/parse "\t\t")))
    (is (= {:error :empty-query} (sut/parse "\n")))

    ;; missing colon prefix
    (is (= {:error :invalid-syntax} (sut/parse "no colon")))
    (is (= {:error :invalid-syntax} (sut/parse "user veschin")))
    (is (= {:error :invalid-syntax} (sut/parse "help")))

    ;; unknown commands
    (let [r1 (sut/parse ":unknown-cmd foo")]
      (is (= :unknown-command (:error r1)))
      (is (= "unknown-cmd" (:command r1))))
    (let [r2 (sut/parse ":search veschin")]
      (is (= :unknown-command (:error r2)))
      (is (= "search" (:command r2))))

    ;; uppercase commands
    (let [r3 (sut/parse ":USER veschin")]
      (is (= :unknown-command (:error r3)))
      (is (= "USER" (:command r3))))
    (let [r4 (sut/parse ":Help")]
      (is (= :unknown-command (:error r4)))
      (is (= "Help" (:command r4))))

    ;; invalid syntax
    (is (= {:error :invalid-syntax} (sut/parse ":")))
    (is (= {:error :invalid-syntax} (sut/parse ": ")))
    (is (= {:error :invalid-syntax} (sut/parse "::")))))
