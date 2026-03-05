(ns doxme.handlers.commands-test
  (:require [clojure.test :refer [deftest testing is use-fixtures]]
            [doxme.handlers.commands :as sut]
            [doxme.db.node :as db-node]
            [doxme.db.ops :as db-ops]
            [doxme.profiles.core :as profiles]))

;; ============================================================
;; Command Handler Tests
;; BDD source: .ptsd/bdd/profiles.feature (command sections)
;; Seed data:  .ptsd/seeds/profiles/commands.edn
;; ============================================================

;; --- Test fixtures: real in-memory XTDB node ---

(def ^:dynamic *node* nil)
(def ^:dynamic *sessions* nil)

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
    :profile/updated-at #inst "2026-02-01T11:45:00Z"}])

(defn with-xtdb-and-sessions [f]
  (let [node     (db-node/create-node {:storage :memory})
        sessions (atom {})]
    (try
      (binding [*node*     node
                *sessions* sessions]
        (doseq [profile seed-profiles]
          (db-ops/put-doc node profile))
        (f))
      (finally
        (db-node/close-node node)))))

(use-fixtures :each with-xtdb-and-sessions)

;; --- /register command ---

(deftest register-in-group-chat
  (testing "Register in group chat sends deep link to private chat"
    (let [ctx    {:node     *node*
                  :sessions *sessions*}
          update {:message {:from    {:id 999000 :username "new_user"}
                            :chat    {:id -100200300 :type "group"}
                            :text    "/register"}}
          result (sut/handle-register ctx update)]
      (is (= :send-deep-link (:action result))))))

(deftest register-in-private-chat
  (testing "Register in private chat starts form-fsm registration"
    (let [ctx    {:node     *node*
                  :sessions *sessions*}
          update {:message {:from    {:id 999000 :username "new_user"}
                            :chat    {:id 999000 :type "private"}
                            :text    "/register"}}
          result (sut/handle-register ctx update)]
      (is (= :start-form (:action result)))
      (is (= :step/salary (:initial-state result))))))

(deftest register-resume-existing-session
  (testing "Register with existing active session resumes from current step"
    ;; Pre-populate an active session
    (swap! *sessions* assoc [111222 111222]
           {:state    :step/role
            :step-idx 2
            :data     {:salary "150k USD" :stack "Clojure, ClojureScript"}
            :user-id  111222
            :chat-id  111222
            :created-at (java.util.Date.)})
    (let [ctx    {:node     *node*
                  :sessions *sessions*}
          update {:message {:from    {:id 111222 :username "veschin"}
                            :chat    {:id 111222 :type "private"}
                            :text    "/register"}}
          result (sut/handle-register ctx update)]
      (is (= :resume-form (:action result)))
      (is (= :step/role (:state result))))))

;; --- /profile command ---

(deftest profile-view-own
  (testing "View own profile shows all filled fields"
    (let [ctx    {:node *node*}
          update {:message {:from {:id 111222 :username "veschin"}
                            :chat {:id -100200300 :type "group"}
                            :text "/profile"}}
          result (sut/handle-profile ctx update)]
      (is (= :show-profile (:action result)))
      (is (= "150k USD" (get-in result [:profile :salary])))
      (is (= "Clojure, ClojureScript, Datomic" (get-in result [:profile :stack])))
      (is (= "Senior Engineer" (get-in result [:profile :role])))
      (is (= "Berlin, UTC+1" (get-in result [:profile :location])))
      (is (some? (get-in result [:profile :bio]))))))

(deftest profile-view-own-omits-empty
  (testing "View own profile omits empty fields"
    (let [ctx    {:node *node*}
          update {:message {:from {:id 333444 :username "alex_dev"}
                            :chat {:id -100200300 :type "group"}
                            :text "/profile"}}
          result (sut/handle-profile ctx update)]
      (is (= :show-profile (:action result)))
      (is (= "120k EUR" (get-in result [:profile :salary])))
      (is (= "TypeScript, React, Node.js" (get-in result [:profile :stack])))
      (is (= "Frontend Lead" (get-in result [:profile :role])))
      (is (= "Amsterdam, UTC+1" (get-in result [:profile :location])))
      (is (nil? (get-in result [:profile :bio]))))))

(deftest profile-view-another-user
  (testing "View another user's profile by username"
    (let [ctx    {:node *node*}
          update {:message {:from {:id 111222 :username "veschin"}
                            :chat {:id -100200300 :type "group"}
                            :text "/profile @alex_dev"}}
          result (sut/handle-profile ctx update)]
      (is (= :show-profile (:action result)))
      (is (= "alex_dev" (:target-username result)))
      (is (= "120k EUR" (get-in result [:profile :salary])))
      (is (= "Frontend Lead" (get-in result [:profile :role]))))))

(deftest profile-view-nonexistent-user
  (testing "View profile of unregistered user shows not-found message"
    (let [ctx    {:node *node*}
          update {:message {:from {:id 111222 :username "veschin"}
                            :chat {:id -100200300 :type "group"}
                            :text "/profile @nonexistent"}}
          result (sut/handle-profile ctx update)]
      (is (= :error (:action result)))
      (is (= :profile/not-found (:error result))))))

(deftest profile-view-own-not-registered
  (testing "View own profile when not registered shows error"
    (let [ctx    {:node *node*}
          update {:message {:from {:id 999000 :username "unregistered"}
                            :chat {:id -100200300 :type "group"}
                            :text "/profile"}}
          result (sut/handle-profile ctx update)]
      (is (= :error (:action result)))
      (is (= :error/not-registered (:error result))))))

;; --- /update command ---

(deftest update-single-field
  (testing "Update single field by name"
    (let [ctx    {:node *node*}
          update {:message {:from {:id 111222 :username "veschin"}
                            :chat {:id -100200300 :type "group"}
                            :text "/update :salary 200k USD"}}
          result (sut/handle-update ctx update)]
      (is (= :update-field (:action result)))
      (is (= :salary (:field result)))
      (is (= "200k USD" (:value result)))
      (is (= :profile/updated (:message result))))))

(deftest update-stack-field
  (testing "Update stack field"
    (let [ctx    {:node *node*}
          update {:message {:from {:id 111222 :username "veschin"}
                            :chat {:id -100200300 :type "group"}
                            :text "/update :stack Clojure, Rust"}}
          result (sut/handle-update ctx update)]
      (is (= :update-field (:action result)))
      (is (= :stack (:field result)))
      (is (= "Clojure, Rust" (:value result)))
      (is (= :profile/updated (:message result))))))

(deftest update-unknown-field
  (testing "Update unknown field returns error"
    (let [ctx    {:node *node*}
          update {:message {:from {:id 111222 :username "veschin"}
                            :chat {:id -100200300 :type "group"}
                            :text "/update :nonexistent value"}}
          result (sut/handle-update ctx update)]
      (is (= :error (:action result)))
      (is (= :unknown-field (:error result))))))

(deftest update-without-args-starts-edit-form
  (testing "Update without args starts edit form"
    (let [ctx    {:node     *node*
                  :sessions *sessions*}
          update {:message {:from {:id 111222 :username "veschin"}
                            :chat {:id -100200300 :type "group"}
                            :text "/update"}}
          result (sut/handle-update ctx update)]
      (is (= :start-edit-form (:action result))))))

(deftest update-unregistered-user
  (testing "Update by unregistered user returns error"
    (let [ctx    {:node *node*}
          update {:message {:from {:id 999000 :username "unregistered"}
                            :chat {:id -100200300 :type "group"}
                            :text "/update :salary 200k"}}
          result (sut/handle-update ctx update)]
      (is (= :error (:action result)))
      (is (= :error/not-registered (:error result))))))

;; --- /cancel command ---

(deftest cancel-during-registration
  (testing "Cancel during registration delegates to form-fsm"
    (swap! *sessions* assoc [999000 999000]
           {:state    :step/stack
            :step-idx 1
            :data     {:salary "100k USD"}
            :user-id  999000
            :chat-id  999000
            :created-at (java.util.Date.)})
    (let [ctx    {:node     *node*
                  :sessions *sessions*}
          update {:message {:from {:id 999000 :username "new_user"}
                            :chat {:id 999000 :type "private"}
                            :text "/cancel"}}
          result (sut/handle-cancel ctx update)]
      (is (= :idle (get-in @*sessions* [[999000 999000] :state]))))))

;; --- Username @ prefix stripping in /profile ---

(deftest profile-at-prefix-stripped
  (testing "Username with @ prefix is stripped before lookup"
    (let [ctx    {:node *node*}
          update {:message {:from {:id 111222 :username "veschin"}
                            :chat {:id -100200300 :type "group"}
                            :text "/profile @veschin"}}
          result (sut/handle-profile ctx update)]
      (is (= :show-profile (:action result)))
      (is (= "150k USD" (get-in result [:profile :salary]))))))
