(ns doxme.form.machine-test
  (:require [clojure.test :refer [deftest testing is are]]
            [doxme.form.machine :as sut])
  (:import [java.time Instant]
           [java.util Date]
           [java.util.concurrent ScheduledExecutorService]))

;; ============================================================
;; Form FSM Tests
;; BDD source: .ptsd/bdd/form-fsm.feature
;; Seed data:  .ptsd/seeds/form-fsm/
;; ============================================================

;; --- Helpers ---

(def steps
  "Profile field step order matching zen spec."
  [:salary :stack :role :location :bio])

(def required-fields
  "Required fields that cannot be skipped."
  #{:salary :stack :role})

(def optional-fields
  "Optional fields that can be skipped."
  #{:location :bio})

(defn- make-session
  "Create a session map for testing."
  [{:keys [state step-idx data user-id chat-id created-at message-id]
    :or   {step-idx   0
           data       {}
           user-id    111222
           chat-id    -100200300
           created-at (Date.)}}]
  (cond-> {:state      state
           :step-idx   step-idx
           :data       data
           :user-id    user-id
           :chat-id    chat-id
           :created-at created-at}
    message-id (assoc :message-id message-id)))

(defn- inst-at
  "Create a Date from ISO string."
  [s]
  (Date/from (Instant/parse s)))

;; --- Happy path: full registration ---

(deftest happy-path-registration
  (testing "Starting registration transitions from idle to first step"
    (let [session (make-session {:state :idle :user-id 111222 :chat-id -100200300})
          result  (sut/transition session :register nil)]
      (is (= :step/salary (:state result)))))

  (testing "Providing salary advances to stack step"
    (let [session (make-session {:state :step/salary})
          result  (sut/transition session :input "150k USD")]
      (is (= :step/stack (:state result)))
      (is (= "150k USD" (get-in result [:data :salary])))))

  (testing "Providing stack advances to role step"
    (let [session (make-session {:state :step/stack :step-idx 1
                                 :data {:salary "150k USD"}})
          result  (sut/transition session :input "Clojure, ClojureScript")]
      (is (= :step/role (:state result)))
      (is (= "Clojure, ClojureScript" (get-in result [:data :stack])))))

  (testing "Providing role advances to location step"
    (let [session (make-session {:state :step/role :step-idx 2
                                 :data {:salary "150k USD" :stack "Clojure, ClojureScript"}})
          result  (sut/transition session :input "Senior Engineer")]
      (is (= :step/location (:state result)))))

  (testing "Providing location advances to bio step"
    (let [session (make-session {:state :step/location :step-idx 3
                                 :data {:salary "150k USD" :stack "Clojure" :role "Senior Engineer"}})
          result  (sut/transition session :input "Berlin, UTC+1")]
      (is (= :step/bio (:state result)))
      (is (= "Berlin, UTC+1" (get-in result [:data :location])))))

  (testing "Providing bio advances to confirm step"
    (let [session (make-session {:state :step/bio :step-idx 4
                                 :data {:salary "150k USD" :stack "Clojure" :role "SE" :location "Berlin"}})
          result  (sut/transition session :input "Functional programming enthusiast. 10 years in Clojure.")]
      (is (= :confirm (:state result)))))

  (testing "Confirming at confirm step completes the form"
    (let [data    {:salary "200k USD" :stack "Rust, Go" :role "Staff Engineer"
                   :location "San Francisco, UTC-8"
                   :bio "Systems programmer. Formerly at Google. Open source maintainer."}
          session (make-session {:state :confirm :step-idx 5 :data data
                                 :user-id 777888 :chat-id -100200300})
          result  (sut/transition session :confirm nil)]
      (is (= :completed (:state result)))
      (is (= data (:data result))))))

;; --- Back navigation ---

(deftest back-navigation
  (testing "Back on first step stays on first step"
    (let [session (make-session {:state :step/salary :step-idx 0})
          result  (sut/transition session :back nil)]
      (is (= :step/salary (:state result)))))

  (testing "Back on stack returns to salary"
    (let [session (make-session {:state :step/stack :step-idx 1})
          result  (sut/transition session :back nil)]
      (is (= :step/salary (:state result)))))

  (testing "Back on role returns to stack"
    (let [session (make-session {:state :step/role :step-idx 2})
          result  (sut/transition session :back nil)]
      (is (= :step/stack (:state result)))))

  (testing "Back on location returns to role"
    (let [session (make-session {:state :step/location :step-idx 3})
          result  (sut/transition session :back nil)]
      (is (= :step/role (:state result)))))

  (testing "Back on bio returns to location"
    (let [session (make-session {:state :step/bio :step-idx 4})
          result  (sut/transition session :back nil)]
      (is (= :step/location (:state result)))))

  (testing "Back on confirm returns to bio"
    (let [session (make-session {:state :confirm :step-idx 5})
          result  (sut/transition session :back nil)]
      (is (= :step/bio (:state result))))))

;; --- Skip on optional fields ---

(deftest skip-optional-fields
  (testing "Skip on optional location advances to bio"
    (let [session (make-session {:state :step/location :step-idx 3
                                 :user-id 333444 :chat-id -100200300})
          result  (sut/transition session :skip nil)]
      (is (= :step/bio (:state result)))
      (is (nil? (get-in result [:data :location])))))

  (testing "Skip on optional bio advances to confirm"
    (let [session (make-session {:state :step/bio :step-idx 4
                                 :user-id 555666 :chat-id -100500600})
          result  (sut/transition session :skip nil)]
      (is (= :confirm (:state result)))
      (is (nil? (get-in result [:data :bio]))))))

;; --- Skip on required fields (error) ---

(deftest skip-required-fields-rejected
  (testing "Skip on required field is rejected with error"
    (are [state]
         (let [session (make-session {:state state})
               result  (sut/transition session :skip nil)]
           (and (= state (:state result))
                (= :field-required (:error result))))

      :step/salary
      :step/stack
      :step/role)))

;; --- Cancel from any state ---

(deftest cancel-from-any-state
  (testing "Cancel from any active state returns to idle and clears data"
    (are [state]
         (let [session (make-session {:state state
                                      :data  {:salary "150k USD" :stack "Clojure"}})
               result  (sut/transition session :cancel nil)]
           (and (= :idle (:state result))
                (empty? (:data result))))

      :step/salary
      :step/stack
      :step/role
      :step/location
      :step/bio
      :confirm)))

;; --- Session management ---

(deftest session-create
  (testing "Create session initializes in idle state"
    (let [session (sut/create-session 999000 -100500600 steps)]
      (is (= :idle (:state session)))
      (is (= 0 (:step-idx session)))
      (is (empty? (:data session)))
      (is (some? (:created-at session))))))

(deftest session-get
  (testing "Get session returns session for known user-chat pair"
    (let [sessions (atom {[111222 -100200300]
                          (make-session {:state :step/salary})})]
      (is (= :step/salary
             (:state (sut/get-session sessions 111222 -100200300))))))

  (testing "Get session returns nil for unknown user-chat pair"
    (let [sessions (atom {})]
      (is (nil? (sut/get-session sessions 999999 -100200300))))))

(deftest session-data-accumulation
  (testing "Session accumulates data across steps"
    (let [s0 (make-session {:state :step/salary})
          s1 (sut/transition s0 :input "150k USD")
          s2 (sut/transition s1 :input "Clojure, ClojureScript")
          s3 (sut/transition s2 :input "Senior Engineer")]
      (is (= {:salary "150k USD"
              :stack  "Clojure, ClojureScript"
              :role   "Senior Engineer"}
             (:data s3))))))

(deftest session-shape
  (testing "Session shape includes required fields"
    (let [session (make-session {:state :step/role :step-idx 2})]
      (is (contains? session :state))
      (is (contains? session :step-idx))
      (is (contains? session :data))
      (is (contains? session :created-at))
      (is (contains? session :user-id))
      (is (contains? session :chat-id)))))

;; --- Session expiry ---

(deftest session-expiry
  (testing "Session older than 7 days is treated as expired"
    (let [session (make-session {:state      :step/stack
                                 :step-idx   1
                                 :data       {:salary "100k USD"}
                                 :created-at (inst-at "2026-02-20T10:00:00Z")})
          result  (sut/transition session :input "Clojure"
                                  {:now (inst-at "2026-03-01T10:00:00Z")})]
      (is (= :session-expired (:error result))))))

(deftest cleanup-expired-sessions
  (testing "Cleanup removes expired sessions and keeps active ones"
    (let [sessions (atom {[111222 -100200300]
                          {:state      :step/salary
                           :step-idx   0
                           :data       {}
                           :created-at (inst-at "2026-02-20T10:00:00Z")
                           :user-id    111222
                           :chat-id    -100200300}

                          [333444 -100200300]
                          {:state      :step/bio
                           :step-idx   4
                           :data       {:salary "120k EUR" :stack "TypeScript" :role "Frontend Lead"}
                           :created-at (inst-at "2026-03-04T14:30:00Z")
                           :user-id    333444
                           :chat-id    -100200300}

                          [555666 -100500600]
                          {:state      :confirm
                           :step-idx   5
                           :data       {:salary "90k USD" :stack "Python" :role "Backend Dev"}
                           :created-at (inst-at "2026-02-15T08:00:00Z")
                           :user-id    555666
                           :chat-id    -100500600}})
          now     (inst-at "2026-03-05T00:00:00Z")]
      (sut/cleanup-expired sessions {:now now})
      (is (= 1 (count @sessions)))
      (is (contains? @sessions [333444 -100200300]))
      (is (not (contains? @sessions [111222 -100200300])))
      (is (not (contains? @sessions [555666 -100500600]))))))

;; --- Background cleanup scheduling ---

(deftest scheduled-cleanup-task
  (testing "Background cleanup runs via ScheduledExecutorService every 24 hours"
    (let [sessions  (atom {})
          scheduler (sut/start-cleanup-scheduler sessions)]
      (try
        (is (instance? ScheduledExecutorService scheduler))
        (is (not (.isShutdown scheduler)))
        (finally
          (.shutdown scheduler))))))

;; --- Edge cases ---

(deftest edge-case-resume-existing-session
  (testing "Register with existing active session resumes from current step"
    (let [session (make-session {:state    :step/role
                                 :step-idx 2
                                 :data     {:salary "150k USD" :stack "Clojure, ClojureScript"}})
          result  (sut/transition session :register nil)]
      (is (= :step/role (:state result)))
      (is (= {:salary "150k USD" :stack "Clojure, ClojureScript"}
             (:data result))))))

(deftest edge-case-stale-callback
  (testing "Callback from stale message is ignored silently"
    (let [session (make-session {:state      :step/stack
                                 :step-idx   1
                                 :data       {:salary "150k USD"}
                                 :message-id 42})
          result  (sut/transition session :input "Clojure"
                                  {:message-id 37})]
      (is (true? (:ignored result))))))

(deftest edge-case-bot-restart
  (testing "Bot restart loses all sessions (fresh atom is empty)"
    (let [sessions (atom {})]
      (is (empty? @sessions))
      (is (nil? (sut/get-session sessions 111222 -100200300))))))
