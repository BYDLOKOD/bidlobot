(ns doxme.admin.warnings-test
  (:require [clojure.test :refer [deftest testing is are]]
            [doxme.admin.warnings :as sut]))

;; ──────────────────────────────────────────────
;; Fixtures — from warnings.edn seed data
;; ──────────────────────────────────────────────

(def chat-id -1001234567890)

(def target-user {:user-id 666777888
                  :username "troublemaker42"
                  :first-name "Igor"})

(def warning-1
  {:xt/id :warn/a1b2c3d4-e5f6-7890-abcd-ef1234567890
   :warn/user-id 666777888
   :warn/chat-id chat-id
   :warn/username "troublemaker42"
   :warn/reason "Spam links in chat"
   :warn/issued-by 111222333
   :warn/issued-by-username "alexey_dev"
   :warn/warning-number 1
   :warn/created-at #inst "2026-02-15T10:00:00.000Z"})

(def warning-2
  {:xt/id :warn/b2c3d4e5-f6a7-8901-bcde-f12345678901
   :warn/user-id 666777888
   :warn/chat-id chat-id
   :warn/username "troublemaker42"
   :warn/reason "Off-topic political debate"
   :warn/issued-by 222333444
   :warn/issued-by-username "marina_fp"
   :warn/warning-number 2
   :warn/created-at #inst "2026-02-20T16:30:00.000Z"})

(def warning-3
  {:xt/id :warn/c3d4e5f6-a7b8-9012-cdef-123456789012
   :warn/user-id 666777888
   :warn/chat-id chat-id
   :warn/username "troublemaker42"
   :warn/reason "Personal insults toward another member"
   :warn/issued-by 111222333
   :warn/issued-by-username "alexey_dev"
   :warn/warning-number 3
   :warn/created-at #inst "2026-03-01T09:15:00.000Z"})

(def all-warnings [warning-1 warning-2 warning-3])

;; ──────────────────────────────────────────────
;; Warning creation and document shape
;; ──────────────────────────────────────────────

(deftest create-warning-test
  (testing "Scenario: First warning -- admin warns a user with reason"
    (let [prior-warnings []
          result (sut/create-warning {:user-id 666777888
                                      :username "troublemaker42"
                                      :chat-id chat-id
                                      :reason "Spam links in chat"
                                      :issued-by 111222333
                                      :issued-by-username "alexey_dev"}
                                     prior-warnings)]
      (is (= 666777888 (:warn/user-id (:doc result))))
      (is (= chat-id (:warn/chat-id (:doc result))))
      (is (= "Spam links in chat" (:warn/reason (:doc result))))
      (is (= 111222333 (:warn/issued-by (:doc result))))
      (is (= 1 (:warn/warning-number (:doc result))))
      (is (string? (name (:xt/id (:doc result))))
          "Warning doc ID should contain a UUID")))

  (testing "Scenario: Admin warns without providing a reason"
    (let [result (sut/create-warning {:user-id 666777888
                                      :username "troublemaker42"
                                      :chat-id chat-id
                                      :reason nil
                                      :issued-by 111222333
                                      :issued-by-username "alexey_dev"}
                                     [])]
      (is (= "No reason given" (or (:warn/reason (:doc result)) "No reason given"))
          "Missing reason should default to 'No reason given'"))))

;; ──────────────────────────────────────────────
;; Warning notification messages
;; ──────────────────────────────────────────────

(deftest warning-notification-test
  (testing "Scenario: First warning notification"
    (let [notification (sut/format-notification "troublemaker42"
                                                "Spam links in chat"
                                                1)]
      (is (= "@troublemaker42 warned: Spam links in chat (warning 1/3)"
             notification))))

  (testing "Scenario: Second warning from a different admin"
    (let [notification (sut/format-notification "troublemaker42"
                                                "Off-topic political debate"
                                                2)]
      (is (= "@troublemaker42 warned: Off-topic political debate (warning 2/3)"
             notification))))

  (testing "Scenario: Warning without reason"
    (let [notification (sut/format-notification "troublemaker42"
                                                "No reason given"
                                                1)]
      (is (= "@troublemaker42 warned: No reason given (warning 1/3)"
             notification)))))

;; ──────────────────────────────────────────────
;; Escalation — 3 strikes -> auto-mute
;; ──────────────────────────────────────────────

(deftest escalation-test
  (testing "Scenario: First warning — no auto-mute"
    (let [result (sut/check-escalation 1)]
      (is (false? (:auto-mute result)))))

  (testing "Scenario: Second warning — no auto-mute"
    (let [result (sut/check-escalation 2)]
      (is (false? (:auto-mute result)))))

  (testing "Scenario: Third warning triggers auto-mute for 24 hours"
    (let [result (sut/check-escalation 3)]
      (is (true? (:auto-mute result)))
      (is (= 86400 (:mute-duration-seconds result))
          "Auto-mute duration should be 24 hours (86400 seconds)"))))

(deftest third-warning-notification-test
  (testing "Scenario: Third warning notification includes auto-mute"
    (let [notification (sut/format-escalation-notification
                        "troublemaker42"
                        "Personal insults toward another member"
                        3)]
      (is (= "@troublemaker42 warned: Personal insults toward another member (warning 3/3). Auto-muted for 24 hours."
             notification)))))

;; ──────────────────────────────────────────────
;; /warns — Warning history
;; ──────────────────────────────────────────────

(deftest warning-history-test
  (testing "Scenario: /warns shows warning history for a user"
    (let [report (sut/format-history "troublemaker42" all-warnings)]
      (is (= (str "Warnings for @troublemaker42 (3/3):\n"
                  "\n"
                  "1. Spam links in chat \u2014 by @alexey_dev (Feb 15, 2026)\n"
                  "2. Off-topic political debate \u2014 by @marina_fp (Feb 20, 2026)\n"
                  "3. Personal insults toward another member \u2014 by @alexey_dev (Mar 1, 2026)")
             report)))))

;; ──────────────────────────────────────────────
;; /warn :clear — Clear all warnings
;; ──────────────────────────────────────────────

(deftest clear-warnings-test
  (testing "Scenario: Admin clears all warnings for a user"
    (let [result (sut/clear-warnings "troublemaker42" all-warnings)]
      (is (= 3 (:warnings-removed result))
          "All 3 warnings should be marked for removal")
      (is (= "All warnings cleared for @troublemaker42." (:notification result)))
      (is (= (map :xt/id all-warnings) (:doc-ids-to-remove result))
          "Should return IDs of all warning documents to remove"))))

(deftest clear-warnings-empty-test
  (testing "Clearing warnings for a user with no warnings"
    (let [result (sut/clear-warnings "clean_user" [])]
      (is (= 0 (:warnings-removed result)))
      (is (empty? (:doc-ids-to-remove result))))))

;; ──────────────────────────────────────────────
;; Warning count for a user
;; ──────────────────────────────────────────────

(deftest warning-count-test
  (testing "Count warnings for a user"
    (is (= 3 (sut/warning-count all-warnings)))
    (is (= 0 (sut/warning-count [])))))
