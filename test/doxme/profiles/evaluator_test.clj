(ns doxme.profiles.evaluator-test
  (:require [clojure.test :refer [deftest testing is are use-fixtures]]
            [doxme.profiles.evaluator :as sut]
            [doxme.db.node :as db-node]
            [doxme.db.ops :as db-ops]))

;; ============================================================
;; Query Evaluator Tests
;; BDD source: .ptsd/bdd/profiles.feature (inline query evaluation)
;; Seed data:  .ptsd/seeds/profiles/evaluator.edn
;; ============================================================

;; --- Test fixtures: real in-memory XTDB node ---

(def ^:dynamic *node* nil)

(def seed-profiles
  [{:xt/id            :profile/111222-100200300
    :profile/user-id  111222
    :profile/chat-id  -100200300
    :profile/username "veschin"
    :profile/salary   "150k USD"
    :profile/stack    "Clojure, ClojureScript, Datomic"
    :profile/role     "Senior Engineer"
    :profile/location "Berlin, UTC+1"
    :profile/bio      "Functional programming enthusiast. 10 years in Clojure. Open source contributor."
    :profile/created-at #inst "2026-01-15T10:30:00Z"
    :profile/updated-at #inst "2026-02-20T14:00:00Z"}

   {:xt/id            :profile/333444-100200300
    :profile/user-id  333444
    :profile/chat-id  -100200300
    :profile/username "alex_dev"
    :profile/salary   "120k EUR"
    :profile/stack    "TypeScript, React, Node.js"
    :profile/role     "Frontend Lead"
    :profile/location "Amsterdam, UTC+1"
    :profile/bio      nil
    :profile/created-at #inst "2026-01-20T09:00:00Z"
    :profile/updated-at #inst "2026-01-20T09:00:00Z"}

   {:xt/id            :profile/555666-100200300
    :profile/user-id  555666
    :profile/chat-id  -100200300
    :profile/username "maria_fe"
    :profile/salary   "130k USD"
    :profile/stack    "Kotlin, Spring Boot, PostgreSQL"
    :profile/role     "Backend Developer"
    :profile/location nil
    :profile/bio      "Building microservices at scale. Previously at Spotify."
    :profile/created-at #inst "2026-02-01T11:45:00Z"
    :profile/updated-at #inst "2026-02-01T11:45:00Z"}

   {:xt/id            :profile/111222-100500600
    :profile/user-id  111222
    :profile/chat-id  -100500600
    :profile/username "veschin"
    :profile/salary   "160k USD"
    :profile/stack    "Clojure, Terraform, Kubernetes"
    :profile/role     "Platform Engineer"
    :profile/location "Berlin, UTC+1"
    :profile/bio      nil
    :profile/created-at #inst "2026-02-10T08:00:00Z"
    :profile/updated-at #inst "2026-02-10T08:00:00Z"}

   {:xt/id            :profile/777888-100500600
    :profile/user-id  777888
    :profile/chat-id  -100500600
    :profile/username "dmitry_ops"
    :profile/salary   "140k USD"
    :profile/stack    "Go, Docker, AWS, Terraform"
    :profile/role     "SRE"
    :profile/location "Moscow, UTC+3"
    :profile/bio      "Site reliability engineer. Automate everything."
    :profile/created-at #inst "2026-02-12T16:20:00Z"
    :profile/updated-at #inst "2026-03-01T10:00:00Z"}])

(defn with-xtdb-node [f]
  (let [node (db-node/create-node {:storage :memory})]
    (try
      (binding [*node* node]
        (doseq [profile seed-profiles]
          (db-ops/put-doc node profile))
        (f))
      (finally
        (db-node/close-node node)))))

(use-fixtures :each with-xtdb-node)

;; --- Evaluate :user :get for each profile field ---

(deftest evaluate-user-get-fields
  (testing "Evaluate :user :get for each profile field"
    (are [field value field-name]
         (= {:result value :title (str "veschin's " field-name)}
            (sut/evaluate *node* {:cmd :user :args ["veschin" :get field]} -100200300))

      :salary   "150k USD"                                                                          "salary"
      :stack    "Clojure, ClojureScript, Datomic"                                                   "stack"
      :role     "Senior Engineer"                                                                    "role"
      :location "Berlin, UTC+1"                                                                      "location"
      :bio      "Functional programming enthusiast. 10 years in Clojure. Open source contributor."   "bio")))

;; --- Evaluate :user :profile ---

(deftest evaluate-user-profile
  (testing "Evaluate :user :profile returns full profile text"
    (let [result (sut/evaluate *node* {:cmd :user :args ["veschin" :profile]} -100200300)]
      (is (= "veschin's profile" (:title result)))
      (is (re-find #"salary: 150k USD" (:result result)))
      (is (re-find #"stack: Clojure, ClojureScript, Datomic" (:result result)))
      (is (re-find #"role: Senior Engineer" (:result result)))
      (is (re-find #"location: Berlin, UTC\+1" (:result result))))))

;; --- Evaluate :user :get for another user ---

(deftest evaluate-user-get-other-user
  (testing "Evaluate :user :get for another user"
    (is (= {:result "120k EUR" :title "alex_dev's salary"}
           (sut/evaluate *node* {:cmd :user :args ["alex_dev" :get :salary]} -100200300)))))

;; --- Same user different chat ---

(deftest evaluate-same-user-different-chat
  (testing "Same user different chat returns chat-specific data"
    (is (= {:result "160k USD" :title "veschin's salary"}
           (sut/evaluate *node* {:cmd :user :args ["veschin" :get :salary]} -100500600)))))

;; --- Error: user not found ---

(deftest evaluate-user-not-found
  (testing "Evaluate :user :get for nonexistent user returns error"
    (is (= {:error :user-not-found}
           (sut/evaluate *node* {:cmd :user :args ["nonexistent" :get :salary]} -100200300))))

  (testing "Evaluate :user :profile for nonexistent user returns error"
    (is (= {:error :user-not-found}
           (sut/evaluate *node* {:cmd :user :args ["ghost_user" :profile]} -100200300)))))

;; --- Error: field not found ---

(deftest evaluate-field-not-found
  (testing "Evaluate :user :get for nonexistent field returns error"
    (is (= {:error :field-not-found}
           (sut/evaluate *node* {:cmd :user :args ["veschin" :get :nonexistent]} -100200300))))

  (testing "Evaluate :user :get for unknown field name returns error"
    (is (= {:error :field-not-found}
           (sut/evaluate *node* {:cmd :user :args ["veschin" :get :email]} -100200300)))))

;; --- Optional field that is nil ---

(deftest evaluate-nil-optional-field
  (testing "Evaluate :user :get for nil optional field returns field-not-found"
    (is (= {:error :field-not-found}
           (sut/evaluate *node* {:cmd :user :args ["alex_dev" :get :bio]} -100200300))))

  (testing "Evaluate :user :get for nil location returns field-not-found"
    (is (= {:error :field-not-found}
           (sut/evaluate *node* {:cmd :user :args ["maria_fe" :get :location]} -100200300)))))

;; --- Profile omits nil fields ---

(deftest evaluate-profile-omits-nil
  (testing "Profile view omits nil fields"
    (let [result (sut/evaluate *node* {:cmd :user :args ["alex_dev" :profile]} -100200300)]
      (is (= "alex_dev's profile" (:title result)))
      (is (re-find #"salary: 120k EUR" (:result result)))
      (is (re-find #"stack: TypeScript, React, Node.js" (:result result)))
      (is (re-find #"role: Frontend Lead" (:result result)))
      (is (re-find #"location: Amsterdam, UTC\+1" (:result result)))
      (is (not (re-find #"bio:" (:result result)))))))

;; --- Evaluate :help command ---

(deftest evaluate-help-general
  (testing "Evaluate :help with no args returns general help"
    (let [result (sut/evaluate *node* {:cmd :help :args []} -100200300)]
      (is (= "Help" (:title result)))
      (is (re-find #"Available commands" (:result result))))))

(deftest evaluate-help-user
  (testing "Evaluate :help :user returns user command help"
    (let [result (sut/evaluate *node* {:cmd :help :args [:user]} -100200300)]
      (is (= "Help: :user" (:title result)))
      (is (re-find #":user <username> :get <field>" (:result result))))))

(deftest evaluate-help-chat
  (testing "Evaluate :help :chat returns chat command help"
    (let [result (sut/evaluate *node* {:cmd :help :args [:chat]} -100200300)]
      (is (= "Help: :chat" (:title result)))
      (is (re-find #":chat" (:result result))))))

;; --- Username edge cases in evaluator ---

(deftest evaluate-username-at-prefix
  (testing "Inline query with @ prefix strips it"
    (let [result (sut/evaluate *node* {:cmd :user :args ["@alex_dev" :get :salary]} -100200300)]
      (is (= {:result "120k EUR" :title "alex_dev's salary"} result)))))

(deftest evaluate-username-case-insensitive
  (testing "Username lookup is case-insensitive in evaluator"
    (is (= {:result "150k USD" :title "veschin's salary"}
           (sut/evaluate *node* {:cmd :user :args ["VESCHIN" :get :salary]} -100200300))))

  (testing "Uppercase in inline query matches case-insensitively"
    (is (= {:result "Kotlin, Spring Boot, PostgreSQL" :title "maria_fe's stack"}
           (sut/evaluate *node* {:cmd :user :args ["MARIA_FE" :get :stack]} -100200300))))

  (testing "@ prefix combined with uppercase is handled"
    (is (= {:result "150k USD" :title "veschin's salary"}
           (sut/evaluate *node* {:cmd :user :args ["@VESCHIN" :get :salary]} -100200300)))))

;; --- Cross-chat isolation in evaluator ---

(deftest evaluate-cross-chat-isolation
  (testing "User exists in one chat but not another"
    (is (= {:error :user-not-found}
           (sut/evaluate *node* {:cmd :user :args ["dmitry_ops" :get :salary]} -100200300)))
    (is (= {:result "140k USD" :title "dmitry_ops's salary"}
           (sut/evaluate *node* {:cmd :user :args ["dmitry_ops" :get :salary]} -100500600)))))

;; --- Inline result formatting ---

(deftest inline-result-formatting
  (testing "Inline result is formatted as Telegram article"
    (let [result (sut/evaluate-for-inline *node*
                                           {:cmd :user :args ["veschin" :get :salary]}
                                           -100200300)]
      (is (= "article" (:type result)))
      (is (some? (:id result)))
      (is (= "veschin's salary" (:title result)))
      (is (some? (get-in result [:input-message-content :message-text])))
      (is (= 0 (:cache-time result)))
      (is (true? (:is-personal result))))))
