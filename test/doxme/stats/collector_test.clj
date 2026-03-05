(ns doxme.stats.collector-test
  (:require [clojure.test :refer [deftest testing is are]]
            [doxme.stats.collector :as sut]
            [clojure.core.async :as a]))

;; ──────────────────────────────────────────────
;; Fixtures — concrete seed data from BDD scenarios
;; ──────────────────────────────────────────────

(def chat {:id -1001234567890
           :title "Clojure Russia"
           :type "supergroup"})

(defn text-message [user-id username text]
  {:update_id (rand-int 999999)
   :message {:message_id (rand-int 99999)
             :from {:id user-id
                    :is_bot false
                    :first_name username
                    :username username
                    :language_code "ru"}
             :chat chat
             :date 1772582400
             :text text}})

(defn photo-message [user-id username caption]
  {:update_id (rand-int 999999)
   :message {:message_id (rand-int 99999)
             :from {:id user-id
                    :is_bot false
                    :first_name username
                    :username username}
             :chat chat
             :date 1772582520
             :photo [{:file_id "AgACAgIAAxkBAAIBi2X" :width 90 :height 90}
                     {:file_id "AgACAgIAAxkBAAIBi2Z" :width 800 :height 800}]
             :caption caption}})

(defn sticker-message [user-id username]
  {:update_id (rand-int 999999)
   :message {:message_id (rand-int 99999)
             :from {:id user-id
                    :is_bot false
                    :first_name username
                    :username username}
             :chat chat
             :date 1772582580
             :sticker {:file_id "CAACAgIAAxkBAAICaGX"
                       :type "regular"
                       :width 512
                       :height 512
                       :emoji "\uD83D\uDC4D"
                       :set_name "HotCherry"}}})

(defn video-message [user-id username caption]
  {:update_id (rand-int 999999)
   :message {:message_id (rand-int 99999)
             :from {:id user-id
                    :is_bot false
                    :first_name username
                    :username username}
             :chat chat
             :date 1772582640
             :video {:file_id "BAACAgIAAxkBAAIBk2X"
                     :width 1920 :height 1080 :duration 45}
             :caption caption}})

(defn document-message [user-id username filename]
  {:update_id (rand-int 999999)
   :message {:message_id (rand-int 99999)
             :from {:id user-id
                    :is_bot false
                    :first_name username
                    :username username}
             :chat chat
             :date 1772582700
             :document {:file_id "BQACAgIAAxkBAAICb2X"
                        :file_name filename
                        :mime_type "application/pdf"
                        :file_size 245760}}})

(defn bot-message [bot-id bot-username text]
  {:update_id (rand-int 999999)
   :message {:message_id (rand-int 99999)
             :from {:id bot-id
                    :is_bot true
                    :first_name bot-username
                    :username bot-username}
             :chat chat
             :date 1772582980
             :text text}})

(defn edited-message-update []
  {:update_id 100007
   :edited_message {:message_id 5001
                    :from {:id 111222333
                           :is_bot false
                           :first_name "Alexey"
                           :username "alexey_dev"}
                    :chat chat
                    :date 1772582400
                    :edit_date 1772582800
                    :text "Has anyone tried Datomic Cloud? Specifically v2?"}})

(defn service-message [service-key service-val]
  {:update_id (rand-int 999999)
   :message (merge {:message_id (rand-int 99999)
                    :from {:id 111222333
                           :is_bot false
                           :first_name "Alexey"
                           :username "alexey_dev"}
                    :chat chat
                    :date 1772583040}
                   {service-key service-val})})

(defn forwarded-message [forwarder-id forwarder-username original-sender-id]
  {:update_id (rand-int 999999)
   :message {:message_id (rand-int 99999)
             :from {:id forwarder-id
                    :is_bot false
                    :first_name forwarder-username
                    :username forwarder-username}
             :chat chat
             :date 1772583200
             :forward_from {:id original-sender-id
                            :first_name "External User"}
             :forward_date 1772400000
             :text "Check out this interesting article"}})

;; ──────────────────────────────────────────────
;; Message classification — countable?
;; ──────────────────────────────────────────────

(deftest countable-message-types-test
  (testing "Scenario: Count a text message from a non-bot user"
    (let [update (text-message 111222333 "alexey_dev" "Has anyone tried Datomic Cloud?")]
      (is (true? (sut/countable? update))
          "Text messages from non-bot users should be counted")))

  (testing "Scenario: Count a photo message"
    (let [update (photo-message 333444555 "dmi3_arch" "Architecture diagram for the new service")]
      (is (true? (sut/countable? update))
          "Photo messages should be counted")))

  (testing "Scenario: Count a sticker message"
    (let [update (sticker-message 111222333 "alexey_dev")]
      (is (true? (sut/countable? update))
          "Sticker messages should be counted")))

  (testing "Scenario: Count a video message"
    (let [update (video-message 444555666 "pavel_clj" "Quick demo of the REPL workflow")]
      (is (true? (sut/countable? update))
          "Video messages should be counted")))

  (testing "Scenario: Count a document message"
    (let [update (document-message 555666777 "olga_data" "benchmark-results.pdf")]
      (is (true? (sut/countable? update))
          "Document messages should be counted"))))

(deftest non-countable-message-types-test
  (testing "Scenario Outline: Do not count non-content service messages"
    (are [update-type service-key service-val]
         (false? (sut/countable? (service-message service-key service-val)))

      "new_chat_members"  :new_chat_members  [{:id 666777888 :is_bot false :first_name "Igor"}]
      "left_chat_member"  :left_chat_member  {:id 777888999 :is_bot false :first_name "Sergey"}
      "new_chat_title"    :new_chat_title    "Clojure & Friends"
      "pinned_message"    :pinned_message    {:message_id 5002 :text "Pinned"}))

  (testing "Scenario: Edited messages are not counted"
    (is (false? (sut/countable? (edited-message-update)))
        "Edited message updates should not be counted"))

  (testing "Scenario: Do not count bot messages"
    (let [update (bot-message 987654321 "doxme_bot" "Welcome to Clojure Russia!")]
      (is (false? (sut/countable? update))
          "Bot messages should not be counted")))

  (testing "Scenario: Do not count messages from other bots"
    (let [update (bot-message 136817688 "GroupHelpBot" "Welcome! Please read the rules.")]
      (is (false? (sut/countable? update))
          "Messages from other bots should not be counted"))))

;; ──────────────────────────────────────────────
;; Extract message info for stats storage
;; ──────────────────────────────────────────────

(deftest extract-stats-info-test
  (testing "Scenario: Extract user-id and chat-id from a countable message"
    (let [update (text-message 111222333 "alexey_dev" "Hello")
          info   (sut/extract-stats-info update)]
      (is (= 111222333 (:user-id info)))
      (is (= -1001234567890 (:chat-id info)))
      (is (= "alexey_dev" (:username info)))))

  (testing "Scenario: Forwarded messages count for the forwarder"
    (let [update (forwarded-message 111222333 "alexey_dev" 999888777)
          info   (sut/extract-stats-info update)]
      (is (= 111222333 (:user-id info))
          "Forwarded message should attribute to the forwarder, not the original sender"))))

;; ──────────────────────────────────────────────
;; Async stats channel
;; ──────────────────────────────────────────────

(deftest stats-channel-non-blocking-test
  (testing "Scenario: Stats collection is non-blocking via core.async channel"
    (let [ch      (a/chan 256)
          updates (repeatedly 100 #(text-message (rand-int 999999) "test_user" "msg"))]
      ;; All 100 messages should be accepted without blocking
      (doseq [u updates]
        (is (a/offer! ch (sut/extract-stats-info u))
            "Putting a message on the stats channel should not block"))
      ;; Drain the channel and verify count
      (let [results (loop [acc []]
                      (if-let [v (a/poll! ch)]
                        (recur (conj acc v))
                        acc))]
        (is (= 100 (count results))
            "All 100 messages should be received from the channel"))
      (a/close! ch))))

;; ──────────────────────────────────────────────
;; First message creates user-stats entry
;; ──────────────────────────────────────────────

(deftest first-message-creates-stats-entry-test
  (testing "Scenario: First message from a new user creates user-stats entry"
    (let [update  (text-message 666777888 "igor_newbie" "Hello everyone!")
          info    (sut/extract-stats-info update)
          doc-id  (sut/user-stats-id (:user-id info) (:chat-id info))]
      (is (= "user-stats/666777888--1001234567890" doc-id)
          "User stats document ID should follow the convention user-stats/{user-id}-{chat-id}"))))
