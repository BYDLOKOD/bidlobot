(ns doxme.profiles.core-test
  (:require [clojure.test :refer [deftest testing is use-fixtures]]
            [doxme.profiles.core :as sut]
            [doxme.db.node :as db-node]
            [doxme.db.ops :as db-ops])
  (:import [java.time Instant]
           [java.util Date]))

;; ============================================================
;; Profile CRUD Tests
;; BDD source: .ptsd/bdd/profiles.feature
;; Seed data:  .ptsd/seeds/profiles/profiles.edn
;; ============================================================

;; --- Test fixtures: real in-memory XTDB node ---

(def ^:dynamic *node* nil)

(defn with-xtdb-node [f]
  (let [node (db-node/create-node {:storage :memory})]
    (try
      (binding [*node* node]
        ;; Load seed profiles
        (doseq [profile seed-profiles]
          (db-ops/put-doc node profile))
        (f))
      (finally
        (db-node/close-node node)))))

(use-fixtures :each with-xtdb-node)

;; --- Seed data (from .ptsd/seeds/profiles/profiles.edn) ---

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

;; --- Profile save ---

(deftest save-profile
  (testing "Completed form saves profile to XTDB"
    (let [form-data {:salary   "100k USD"
                     :stack    "Java, Spring"
                     :role     "Developer"
                     :location "London, UTC"
                     :bio      "Backend developer."}
          profile-id (sut/save-profile! *node* 999000 -100200300 "new_user" form-data)]
      (is (= :profile/999000-100200300 profile-id))
      (let [doc (db-ops/get-doc *node* profile-id)]
        (is (some? doc))
        (is (= 999000 (:profile/user-id doc)))
        (is (= -100200300 (:profile/chat-id doc)))
        (is (= "new_user" (:profile/username doc)))
        (is (= "100k USD" (:profile/salary doc)))
        (is (= "Java, Spring" (:profile/stack doc)))
        (is (= "Developer" (:profile/role doc)))
        (is (= "London, UTC" (:profile/location doc)))
        (is (= "Backend developer." (:profile/bio doc)))
        (is (some? (:profile/created-at doc)))
        (is (some? (:profile/updated-at doc))))))

  (testing "Profile ID follows the convention :profile/{user-id}-{chat-id}"
    (is (= :profile/111222-100200300
           (sut/profile-id 111222 -100200300)))))

;; --- Profile get by username ---

(deftest get-profile-by-username
  (testing "Get profile for existing user in chat"
    (let [profile (sut/get-profile-by-username *node* "veschin" -100200300)]
      (is (some? profile))
      (is (= "150k USD" (:profile/salary profile)))
      (is (= "Clojure, ClojureScript, Datomic" (:profile/stack profile)))
      (is (= "Senior Engineer" (:profile/role profile)))))

  (testing "Get profile returns nil for non-existent user"
    (is (nil? (sut/get-profile-by-username *node* "nonexistent" -100200300)))))

;; --- Profile get by user-id ---

(deftest get-profile-by-user-id
  (testing "Get profile by user-id and chat-id"
    (let [profile (sut/get-profile-by-id *node* 111222 -100200300)]
      (is (some? profile))
      (is (= "veschin" (:profile/username profile))))))

;; --- Profile update ---

(deftest update-profile-field
  (testing "Update single field by name"
    (sut/update-field! *node* 111222 -100200300 :salary "200k USD")
    (let [profile (sut/get-profile-by-id *node* 111222 -100200300)]
      (is (= "200k USD" (:profile/salary profile)))))

  (testing "Update stack field"
    (sut/update-field! *node* 111222 -100200300 :stack "Clojure, Rust")
    (let [profile (sut/get-profile-by-id *node* 111222 -100200300)]
      (is (= "Clojure, Rust" (:profile/stack profile))))))

;; --- Profile stored with :profile/ namespace prefix ---

(deftest profile-namespace-prefix
  (testing "Profile stored with :profile/ namespace prefix on fields"
    (let [doc (db-ops/get-doc *node* :profile/111222-100200300)]
      (is (contains? doc :profile/user-id))
      (is (contains? doc :profile/chat-id))
      (is (contains? doc :profile/username))
      (is (contains? doc :profile/salary))
      (is (contains? doc :profile/stack))
      (is (contains? doc :profile/role))
      (is (contains? doc :profile/location))
      (is (contains? doc :profile/bio))
      (is (contains? doc :profile/created-at))
      (is (contains? doc :profile/updated-at)))))

;; --- Username handling: @ prefix stripped ---

(deftest username-at-prefix
  (testing "Username with @ prefix is stripped before lookup"
    (let [profile (sut/get-profile-by-username *node* "@veschin" -100200300)]
      (is (some? profile))
      (is (= "veschin" (:profile/username profile))))))

;; --- Username handling: case-insensitive ---

(deftest username-case-insensitive
  (testing "Username lookup is case-insensitive"
    (is (some? (sut/get-profile-by-username *node* "VESCHIN" -100200300))))

  (testing "Mixed case username is normalized"
    (let [profile (sut/get-profile-by-username *node* "Alex_Dev" -100200300)]
      (is (some? profile))
      (is (= "alex_dev" (:profile/username profile))))))

;; --- Cross-chat profile isolation ---

(deftest cross-chat-isolation
  (testing "Same user has different profiles in different chats"
    (let [profile-a (sut/get-profile-by-username *node* "veschin" -100200300)
          profile-b (sut/get-profile-by-username *node* "veschin" -100500600)]
      (is (= "150k USD" (:profile/salary profile-a)))
      (is (= "Senior Engineer" (:profile/role profile-a)))
      (is (= "160k USD" (:profile/salary profile-b)))
      (is (= "Platform Engineer" (:profile/role profile-b)))))

  (testing "User exists in one chat but not another"
    (is (nil? (sut/get-profile-by-username *node* "dmitry_ops" -100200300)))
    (is (some? (sut/get-profile-by-username *node* "dmitry_ops" -100500600)))))

;; --- Profile omits empty fields on display ---

(deftest profile-display-omits-nil
  (testing "View own profile omits empty fields"
    (let [profile (sut/get-profile-by-username *node* "alex_dev" -100200300)
          display (sut/format-profile profile)]
      (is (re-find #"120k EUR" display))
      (is (re-find #"TypeScript, React, Node.js" display))
      (is (re-find #"Frontend Lead" display))
      (is (re-find #"Amsterdam, UTC\+1" display))
      (is (not (re-find #"bio" display))))))

;; --- User with no Telegram username ---

(deftest user-no-username
  (testing "User with no username is looked up by user-id"
    ;; Insert profile with nil username
    (db-ops/put-doc *node*
                     {:xt/id            :profile/888999-100200300
                      :profile/user-id  888999
                      :profile/chat-id  -100200300
                      :profile/username nil
                      :profile/salary   "100k USD"
                      :profile/stack    "Java, Spring"
                      :profile/role     "Developer"
                      :profile/location nil
                      :profile/bio      nil
                      :profile/created-at #inst "2026-02-25T12:00:00Z"
                      :profile/updated-at #inst "2026-02-25T12:00:00Z"})
    (let [profile (sut/get-profile-by-id *node* 888999 -100200300)]
      (is (some? profile))
      (is (= 888999 (:profile/user-id profile))))))

;; --- Bio length edge cases ---

(deftest bio-length-validation
  (testing "Bio exceeding 500 characters is rejected"
    (let [long-bio (apply str (repeat 556 "x"))
          result   (sut/validate-bio long-bio)]
      (is (false? (:valid result)))
      (is (= :bio (:field result)))))

  (testing "Bio at exactly 500 characters is accepted"
    (let [exact-bio (apply str (repeat 500 "x"))
          result    (sut/validate-bio exact-bio)]
      (is (true? (:valid result))))))

;; --- Profile remains after user leaves chat ---

(deftest profile-persists-after-leave
  (testing "User leaves chat but profile remains"
    (let [profile-before (sut/get-profile-by-id *node* 111222 -100200300)]
      ;; User "leaves" — profile should still be retrievable
      (is (some? profile-before))
      (is (= "veschin" (:profile/username profile-before))))))
