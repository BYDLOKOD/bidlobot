(ns doxme.stats.reporter-test
  (:require [clojure.test :refer [deftest testing is are]]
            [doxme.stats.reporter :as sut]))

;; ──────────────────────────────────────────────
;; Seed data — from chat-stats.edn and reports.edn
;; ──────────────────────────────────────────────

(def chat-stats
  {:xt/id "chat-stats/-1001234567890"
   :chat-stats/chat-id -1001234567890
   :chat-stats/total-messages 4237
   :chat-stats/created-at #inst "2025-12-01T10:00:00.000Z"})

(def user-stats
  [{:xt/id "user-stats/111222333--1001234567890"
    :user-stats/user-id 111222333
    :user-stats/chat-id -1001234567890
    :user-stats/username "alexey_dev"
    :user-stats/first-name "Alexey"
    :user-stats/message-count 1542
    :user-stats/first-seen #inst "2025-12-01T10:05:00.000Z"
    :user-stats/last-seen #inst "2026-03-05T08:30:00.000Z"}
   {:xt/id "user-stats/222333444--1001234567890"
    :user-stats/user-id 222333444
    :user-stats/chat-id -1001234567890
    :user-stats/username "marina_fp"
    :user-stats/first-name "Marina"
    :user-stats/message-count 987
    :user-stats/first-seen #inst "2025-12-03T14:20:00.000Z"
    :user-stats/last-seen #inst "2026-03-04T22:15:00.000Z"}
   {:xt/id "user-stats/333444555--1001234567890"
    :user-stats/user-id 333444555
    :user-stats/chat-id -1001234567890
    :user-stats/username "dmi3_arch"
    :user-stats/first-name "Dmitry"
    :user-stats/message-count 856
    :user-stats/first-seen #inst "2025-12-05T09:00:00.000Z"
    :user-stats/last-seen #inst "2026-03-05T07:45:00.000Z"}
   {:xt/id "user-stats/444555666--1001234567890"
    :user-stats/user-id 444555666
    :user-stats/chat-id -1001234567890
    :user-stats/username "pavel_clj"
    :user-stats/first-name "Pavel"
    :user-stats/message-count 614
    :user-stats/first-seen #inst "2025-12-10T16:30:00.000Z"
    :user-stats/last-seen #inst "2026-03-03T19:00:00.000Z"}
   {:xt/id "user-stats/555666777--1001234567890"
    :user-stats/user-id 555666777
    :user-stats/chat-id -1001234567890
    :user-stats/username "olga_data"
    :user-stats/first-name "Olga"
    :user-stats/message-count 238
    :user-stats/first-seen #inst "2026-01-15T11:00:00.000Z"
    :user-stats/last-seen #inst "2026-03-02T14:30:00.000Z"}])

;; ──────────────────────────────────────────────
;; Number formatting
;; ──────────────────────────────────────────────

(deftest format-number-test
  (testing "Scenario Outline: Numbers are formatted with commas"
    (are [raw-count formatted]
         (= formatted (sut/format-number raw-count))

      1542  "1,542"
      987   "987"
      15234 "15,234"
      4237  "4,237")))

;; ──────────────────────────────────────────────
;; Date formatting
;; ──────────────────────────────────────────────

(deftest format-date-test
  (testing "Scenario Outline: Dates are formatted as 'Mon DD, YYYY'"
    (are [iso-date formatted]
         (= formatted (sut/format-date (java.time.Instant/parse iso-date)))

      "2025-12-01T10:00:00.000Z" "Dec 1, 2025"
      "2025-12-03T14:20:00.000Z" "Dec 3, 2025"
      "2026-01-15T11:00:00.000Z" "Jan 15, 2026"
      "2026-03-04T22:15:00.000Z" "Mar 4, 2026")))

(deftest format-date-today-test
  (testing "Scenario: Today's date is shown as 'Today' instead of the date"
    (is (= "Today" (sut/format-date (java.time.Instant/now))))))

;; ──────────────────────────────────────────────
;; /stats — Chat overview report
;; ──────────────────────────────────────────────

(deftest stats-overview-report-test
  (testing "Scenario: /stats shows chat overview with formatted numbers"
    (let [report (sut/overview-report "Clojure Russia" chat-stats user-stats)]
      (is (= (str "Chat Statistics: Clojure Russia\n"
                  "\n"
                  "Total messages: 4,237\n"
                  "Total users: 5\n"
                  "Avg messages/user: 847\n"
                  "Most active: @alexey_dev (1,542 messages)\n"
                  "\n"
                  "Stats since: Dec 1, 2025")
             report)))))

;; ──────────────────────────────────────────────
;; /stats :top — Top users report
;; ──────────────────────────────────────────────

(deftest stats-top-report-test
  (testing "Scenario: /stats :top shows top users sorted by message count"
    (let [report (sut/top-report "Clojure Russia" user-stats)]
      (is (= (str "Top Users: Clojure Russia\n"
                  "\n"
                  "1. @alexey_dev \u2014 1,542 messages\n"
                  "2. @marina_fp \u2014 987 messages\n"
                  "3. @dmi3_arch \u2014 856 messages\n"
                  "4. @pavel_clj \u2014 614 messages\n"
                  "5. @olga_data \u2014 238 messages")
             report)))))

(deftest stats-top-shows-all-when-fewer-than-10-test
  (testing "Scenario: /stats :top shows all users when chat has fewer than 10"
    (let [report (sut/top-report "Clojure Russia" user-stats)]
      ;; 5 users total, all should be in the report
      (is (= 5 (count (re-seq #"\d+\. @" report)))
          "Top list should contain exactly 5 entries when the chat has 5 users"))))

;; ──────────────────────────────────────────────
;; /stats :user — Per-user stats
;; ──────────────────────────────────────────────

(deftest stats-user-report-test
  (testing "Scenario: /stats :user @username shows individual stats with rank"
    (let [report (sut/user-report "marina_fp" user-stats)]
      (is (= (str "Stats for @marina_fp\n"
                  "\n"
                  "Messages: 987\n"
                  "First seen: Dec 3, 2025\n"
                  "Last seen: Mar 4, 2026\n"
                  "Rank: #2 of 5")
             report))))

  (testing "Scenario: /stats :user for the least active user shows correct rank"
    (let [report (sut/user-report "olga_data" user-stats)]
      (is (= (str "Stats for @olga_data\n"
                  "\n"
                  "Messages: 238\n"
                  "First seen: Jan 15, 2026\n"
                  "Last seen: Mar 2, 2026\n"
                  "Rank: #5 of 5")
             report))))

  (testing "Scenario: /stats :user for non-existent user shows not found"
    (let [report (sut/user-report "nonexistent" user-stats)]
      (is (= "User @nonexistent not found in this chat." report)))))

;; ──────────────────────────────────────────────
;; /stats :today — Today's activity
;; ──────────────────────────────────────────────

(deftest stats-today-report-test
  (testing "Scenario: /stats :today shows today's message count using UTC boundaries"
    (let [report (sut/today-report "Clojure Russia" 23 2)]
      (is (= (str "Today's Activity: Clojure Russia\n"
                  "\n"
                  "Messages today: 23\n"
                  "Active users today: 2")
             report))))

  (testing "/stats :today with zero messages"
    (let [report (sut/today-report "Empty Chat" 0 0)]
      (is (= (str "Today's Activity: Empty Chat\n"
                  "\n"
                  "Messages today: 0\n"
                  "Active users today: 0")
             report)))))

;; ──────────────────────────────────────────────
;; Edge cases
;; ──────────────────────────────────────────────

(deftest stats-empty-chat-test
  (testing "Scenario: /stats in a new chat with no messages"
    (let [report (sut/overview-report "New Empty Chat" nil [])]
      (is (= "No activity yet in this chat." report))))

  (testing "Scenario: /stats :top in a new chat with no messages"
    (let [report (sut/top-report "New Empty Chat" [])]
      (is (= "No activity yet in this chat." report)))))

(deftest stats-private-chat-test
  (testing "Scenario: /stats in a private chat is rejected"
    (let [result (sut/validate-chat-type "private")]
      (is (= {:error :groups-only
              :message "Stats are only available in group chats."}
             result)))))

(deftest stats-single-user-chat-test
  (testing "Scenario: Single-user chat shows correct overview"
    (let [single-chat-stats {:xt/id "chat-stats/-1005550001234"
                             :chat-stats/chat-id -1005550001234
                             :chat-stats/total-messages 42
                             :chat-stats/created-at #inst "2026-02-20T08:00:00.000Z"}
          single-user-stats [{:xt/id "user-stats/111222333--1005550001234"
                              :user-stats/user-id 111222333
                              :user-stats/chat-id -1005550001234
                              :user-stats/username "lonely_coder"
                              :user-stats/first-name "Solo"
                              :user-stats/message-count 42
                              :user-stats/first-seen #inst "2026-02-20T08:05:00.000Z"
                              :user-stats/last-seen #inst "2026-03-05T09:00:00.000Z"}]
          report (sut/overview-report "Solo Dev Chat" single-chat-stats single-user-stats)]
      (is (= (str "Chat Statistics: Solo Dev Chat\n"
                  "\n"
                  "Total messages: 42\n"
                  "Total users: 1\n"
                  "Avg messages/user: 42\n"
                  "Most active: @lonely_coder (42 messages)\n"
                  "\n"
                  "Stats since: Feb 20, 2026")
             report)))))
