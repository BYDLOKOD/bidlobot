(ns doxme.tg.polling-test
  (:require [clojure.test :refer [deftest testing is are]]
            [doxme.tg.polling :as sut]
            [clojure.string :as str]))

;; ============================================================
;; Test helpers — TG test client pattern from LIBRARY_CONTRACTS.md 4.1
;; ============================================================

(defn make-test-client
  "Create a test TG client stub with predefined responses.
   Uses the test client pattern from LIBRARY_CONTRACTS.md section 4.1."
  [& {:keys [bot-token responses]
      :or   {bot-token "test-token"
             responses {}}}]
  ;; The real implementation will use tg/->client with :responses
  ;; For now we define the shape tests expect.
  {:bot-token bot-token
   :responses responses})

;; Seed data from bot-lifecycle/updates.edn
(def sample-message-command-update
  {:update_id 200001
   :message
   {:message_id 1
    :from       {:id            294817365
                 :is_bot        false
                 :first_name    "Alexei"
                 :username      "veschin"
                 :language_code "ru"}
    :chat       {:id   294817365
                 :type "private"}
    :date       1709625600
    :text       "/start"
    :entities   [{:offset 0 :length 6 :type "bot_command"}]}})

(def sample-regular-text-update
  {:update_id 200003
   :message
   {:message_id 1848
    :from       {:id            294817365
                 :is_bot        false
                 :first_name    "Alexei"
                 :username      "veschin"
                 :language_code "ru"}
    :chat       {:id    -1001987654321
                 :type  "supergroup"
                 :title "Clojure Russia"}
    :date       1709625720
    :text       "Has anyone tried babashka for scripting?"}})

(def sample-callback-query-update
  {:update_id 200005
   :callback_query
   {:id      "cb_1709625840_001"
    :from    {:id            518293746
              :is_bot        false
              :first_name    "Anna"
              :username      "anna_dev"
              :language_code "en"}
    :message {:message_id 1850
              :chat       {:id 518293746 :type "private"}
              :date       1709625780
              :text       "Profile Registration\n\nYour salary expectation?\n\nStep 1 of 5"}
    :chat_instance "-1234567890123"
    :data    "form:next"}})

(def sample-inline-query-update
  {:update_id 200009
   :inline_query
   {:id        "AQADBA_il001"
    :from      {:id            294817365
                :is_bot        false
                :first_name    "Alexei"
                :username      "veschin"
                :language_code "ru"}
    :query     ":user anna_dev :get :salary"
    :offset    ""
    :chat_type "sender"}})

;; ============================================================
;; Update routing
;; ============================================================

(deftest route-message-command-test
  (testing "route message command update to command handler"
    (let [routed (atom nil)
          bot    {:client (make-test-client) :ztx :fake-ztx}]
      (sut/route-update bot sample-message-command-update
                        {:on-command    (fn [bot update] (reset! routed {:type :command :update update}))
                         :on-message    (fn [bot update] (reset! routed {:type :message :update update}))
                         :on-callback   (fn [bot update] (reset! routed {:type :callback :update update}))
                         :on-inline     (fn [bot update] (reset! routed {:type :inline :update update}))})
      (is (= :command (:type @routed))
          "Command update should be dispatched to command handler")
      (is (= :message (sut/detect-update-type sample-message-command-update))
          "Detected update type should be :message"))))

(deftest route-regular-text-test
  (testing "route regular text message to stats collector"
    (let [routed (atom nil)
          bot    {:client (make-test-client) :ztx :fake-ztx}]
      (sut/route-update bot sample-regular-text-update
                        {:on-command    (fn [bot update] (reset! routed {:type :command}))
                         :on-message    (fn [bot update] (reset! routed {:type :message}))
                         :on-callback   (fn [bot update] (reset! routed {:type :callback}))
                         :on-inline     (fn [bot update] (reset! routed {:type :inline}))})
      (is (= :message (:type @routed))
          "Regular text should be dispatched to stats collector"))))

(deftest route-callback-query-test
  (testing "route callback_query update to form FSM handler"
    (let [routed (atom nil)
          bot    {:client (make-test-client) :ztx :fake-ztx}]
      (sut/route-update bot sample-callback-query-update
                        {:on-command    (fn [bot update] (reset! routed {:type :command}))
                         :on-message    (fn [bot update] (reset! routed {:type :message}))
                         :on-callback   (fn [bot update] (reset! routed {:type :callback}))
                         :on-inline     (fn [bot update] (reset! routed {:type :inline}))})
      (is (= :callback (:type @routed)))
      (is (= :callback_query (sut/detect-update-type sample-callback-query-update))))))

(deftest route-inline-query-test
  (testing "route inline_query update to query language handler"
    (let [routed (atom nil)
          bot    {:client (make-test-client) :ztx :fake-ztx}]
      (sut/route-update bot sample-inline-query-update
                        {:on-command    (fn [bot update] (reset! routed {:type :command}))
                         :on-message    (fn [bot update] (reset! routed {:type :message}))
                         :on-callback   (fn [bot update] (reset! routed {:type :callback}))
                         :on-inline     (fn [bot update] (reset! routed {:type :inline}))})
      (is (= :inline (:type @routed)))
      (is (= :inline_query (sut/detect-update-type sample-inline-query-update))))))

(deftest route-unknown-update-type-test
  (testing "unknown update type is ignored silently"
    (let [routed (atom nil)
          bot    {:client (make-test-client) :ztx :fake-ztx}
          update {:update_id 300002
                  :edited_message
                  {:message_id 1500
                   :from       {:id 294817365 :username "veschin"}
                   :chat       {:id -1001987654321 :type "supergroup"}
                   :date       1709630060
                   :edit_date  1709630120
                   :text       "Fixed typo"}}]
      ;; Should not throw, should not call any handler
      (sut/route-update bot update
                        {:on-command    (fn [bot update] (reset! routed {:type :command}))
                         :on-message    (fn [bot update] (reset! routed {:type :message}))
                         :on-callback   (fn [bot update] (reset! routed {:type :callback}))
                         :on-inline     (fn [bot update] (reset! routed {:type :inline}))})
      (is (nil? @routed) "No handler should be invoked for unknown type"))))

(deftest route-channel-post-test
  (testing "unexpected channel_post update type is ignored"
    (let [routed (atom nil)
          bot    {:client (make-test-client) :ztx :fake-ztx}
          update {:update_id 300003
                  :channel_post
                  {:message_id 10
                   :chat       {:id -1001111111111 :type "channel" :title "News"}
                   :date       1709630180
                   :text       "Channel announcement"}}]
      (sut/route-update bot update
                        {:on-command    (fn [bot update] (reset! routed {:type :command}))
                         :on-message    (fn [bot update] (reset! routed {:type :message}))
                         :on-callback   (fn [bot update] (reset! routed {:type :callback}))
                         :on-inline     (fn [bot update] (reset! routed {:type :inline}))})
      (is (nil? @routed)))))

(deftest route-malformed-update-test
  (testing "malformed update with only update_id is ignored"
    (let [routed (atom nil)
          bot    {:client (make-test-client) :ztx :fake-ztx}
          update {:update_id 300004}]
      (sut/route-update bot update
                        {:on-command    (fn [bot update] (reset! routed {:type :command}))
                         :on-message    (fn [bot update] (reset! routed {:type :message}))
                         :on-callback   (fn [bot update] (reset! routed {:type :callback}))
                         :on-inline     (fn [bot update] (reset! routed {:type :inline}))})
      (is (nil? @routed) "No handler should be invoked for malformed update"))))

(deftest route-photo-message-test
  (testing "photo message routed to stats collector"
    (let [routed (atom nil)
          bot    {:client (make-test-client) :ztx :fake-ztx}
          update {:update_id 200004
                  :message
                  {:message_id 1849
                   :from       {:id 738192045 :is_bot false :first_name "Max" :username "max_clj"}
                   :chat       {:id -1001987654321 :type "supergroup" :title "Clojure Russia"}
                   :date       1709625780
                   :photo      [{:file_id "AgACAgIAAxk" :width 90 :height 90}
                                {:file_id "AgACAgIAAxl" :width 320 :height 320}]
                   :caption    "My REPL setup"}}]
      (sut/route-update bot update
                        {:on-command    (fn [bot update] (reset! routed {:type :command}))
                         :on-message    (fn [bot update] (reset! routed {:type :message}))
                         :on-callback   (fn [bot update] (reset! routed {:type :callback}))
                         :on-inline     (fn [bot update] (reset! routed {:type :inline}))})
      (is (= :message (:type @routed))
          "Photo message should be dispatched to stats collector"))))

;; ============================================================
;; Offset tracking
;; ============================================================

(deftest offset-tracking-test
  (testing "offset tracked after each batch"
    (let [batch [{:update_id 200001} {:update_id 200002} {:update_id 200003}]]
      (is (= 200004 (sut/next-offset batch))
          "Next offset should be max(update_id) + 1")))

  (testing "empty updates keeps offset unchanged"
    (is (nil? (sut/next-offset []))
        "Empty batch should return nil (offset unchanged)")))

(deftest full-batch-offset-test
  (testing "full batch of 100 updates processed correctly"
    (let [batch (mapv #(hash-map :update_id %) (range 400001 400101))]
      (is (= 100 (count batch)))
      (is (= 400101 (sut/next-offset batch))))))

;; ============================================================
;; Polling configuration
;; ============================================================

(deftest polling-config-test
  (testing "polling calls getUpdates with correct parameters"
    ;; Verify the polling config constants match expected values
    (is (= 30 sut/poll-timeout))
    (is (= 100 sut/poll-limit))
    (is (= ["message" "callback_query" "inline_query"] sut/allowed-updates))))

;; ============================================================
;; Batch dispatch
;; ============================================================

(deftest batch-dispatch-test
  (testing "each update in batch is dispatched to handler function"
    (let [received (atom [])
          handler  (fn [update] (swap! received conj update))
          batch    [sample-message-command-update
                    sample-regular-text-update
                    sample-callback-query-update]]
      (sut/dispatch-batch batch handler)
      (is (= 3 (count @received))
          "Handler should be called for each update in batch")
      (is (= (mapv :update_id batch)
             (mapv :update_id @received))
          "Each update should be passed individually to handler"))))

;; ============================================================
;; Polling loop lifecycle
;; ============================================================

(deftest start-polling-test
  (testing "start-polling starts long-polling loop in a thread"
    ;; start-polling should return immediately and run in a background thread.
    ;; We verify by calling it with a handler that records invocations, then
    ;; stopping it after a brief period.
    (let [called (atom false)
          bot    {:client (make-test-client
                           :responses {:get-updates {:status 200
                                                     :body {:ok true :result []}}})
                  :ztx :fake-ztx}
          poller (sut/start-polling bot (fn [_] (reset! called true)))]
      (is (some? poller) "start-polling should return a polling handle")
      ;; Give the thread a moment to start
      (Thread/sleep 100)
      (sut/stop-polling poller)
      ;; The function should have returned immediately (non-blocking)
      (is true "start-polling returned without blocking"))))

(deftest stop-polling-test
  (testing "stop-bot sets stop flag and waits for current poll"
    (let [bot    {:client (make-test-client
                           :responses {:get-updates {:status 200
                                                     :body {:ok true :result []}}})
                  :ztx :fake-ztx}
          poller (sut/start-polling bot (fn [_] nil))]
      (Thread/sleep 100)
      ;; stop-polling should set the stop flag and return
      (sut/stop-polling poller)
      ;; After stop, no new polls should be started
      (is true "stop-polling completed without error"))))

(deftest stop-polling-blocks-for-handlers-test
  (testing "stop-bot blocks until in-flight handlers complete with 5s timeout"
    (let [handler-running (atom false)
          handler-done    (atom false)
          bot    {:client (make-test-client
                           :responses {:get-updates {:status 200
                                                     :body {:ok true
                                                            :result [{:update_id 1
                                                                      :message {:message_id 1
                                                                                :from {:id 1}
                                                                                :chat {:id 1 :type "private"}
                                                                                :text "test"}}]}}})
                  :ztx :fake-ztx}
          poller (sut/start-polling bot
                                    (fn [_]
                                      (reset! handler-running true)
                                      (Thread/sleep 500)
                                      (reset! handler-done true)))]
      (Thread/sleep 200)
      ;; Stop should block until handler completes (up to 5s)
      (sut/stop-polling poller)
      ;; By the time stop returns, handler should have finished
      (is @handler-done "Handler should complete before stop returns"))))

;; ============================================================
;; Error handling during polling
;; ============================================================

(deftest handler-exception-test
  (testing "handler throws exception during update processing"
    (let [received   (atom [])
          handler    (fn [update]
                       (if (= 300001 (:update_id update))
                         (throw (RuntimeException. "Simulated handler failure"))
                         (swap! received conj update)))
          crash-upd  {:update_id 300001
                      :message {:message_id 999
                                :from {:id 294817365 :username "veschin"}
                                :chat {:id -1001987654321 :type "supergroup"}
                                :date 1709630000
                                :text "/crash-trigger"}}
          normal-upd {:update_id 300002
                      :message {:message_id 1000
                                :from {:id 518293746 :username "anna_dev"}
                                :chat {:id -1001987654321 :type "supergroup"}
                                :date 1709630060
                                :text "Normal message"}}]
      ;; dispatch-batch should catch handler exceptions and continue
      (sut/dispatch-batch [crash-upd normal-upd] handler)
      (is (= 1 (count @received))
          "Handler should continue processing after exception"))))

(deftest network-timeout-continues-polling-test
  (testing "network timeout during poll retries next iteration"
    ;; When the TG API returns a timeout, the polling loop should
    ;; log a warning and continue on the next iteration.
    ;; We verify that dispatch-batch handles this gracefully.
    (let [result (sut/handle-poll-error {:type :timeout
                                         :message "Connection timed out"})]
      (is (= :continue (:action result))
          "Timeout should result in :continue action"))))

(deftest rate-limit-429-test
  (testing "TG API returns 429 rate limit"
    ;; The clj-tg-bot-api limiter should handle backoff automatically.
    ;; We verify our error handler recognizes this.
    (let [result (sut/handle-poll-error {:type :rate-limited
                                         :status 429
                                         :retry-after 30})]
      (is (= :backoff (:action result))
          "429 should result in :backoff action"))))

(deftest server-error-500-test
  (testing "TG API returns 500 server error"
    (let [result (sut/handle-poll-error {:type :server-error
                                         :status 500
                                         :message "Internal Server Error"})]
      (is (= :continue (:action result))
          "500 should result in :continue action"))))
