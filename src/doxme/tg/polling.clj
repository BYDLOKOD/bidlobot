(ns doxme.tg.polling
  (:require [clojure.tools.logging :as log]))

;; Polling configuration
(def poll-timeout 30)
(def poll-limit 100)
(def allowed-updates ["message" "callback_query" "inline_query"])

;; Update type detection
(defn detect-update-type
  "Detects the type of a Telegram update.
   Returns :message, :callback_query, :inline_query, or nil."
  [update]
  (cond
    (:message update) :message
    (:callback_query update) :callback_query
    (:inline_query update) :inline_query
    :else nil))

;; Command detection helper
(defn- message-command?
  "Checks if a message update contains a bot command."
  [update]
  (when-let [entities (get-in update [:message :entities])]
    (some #(= "bot_command" (:type %)) entities)))

;; Update routing
(defn route-update
  "Routes an update to the appropriate handler based on its type.
   Handlers map: {:on-command :on-message :on-callback :on-inline}"
  [bot update handlers]
  (let [update-type (detect-update-type update)]
    (case update-type
      :message
      (if (message-command? update)
        (when-let [handler (:on-command handlers)]
          (handler bot update))
        (when-let [handler (:on-message handlers)]
          (handler bot update)))
      :callback_query
      (when-let [handler (:on-callback handlers)]
        (handler bot update))
      :inline_query
      (when-let [handler (:on-inline handlers)]
        (handler bot update))
      nil)))

;; Offset tracking
(defn next-offset
  "Returns the next offset for getUpdates.
   Returns max(update_id) + 1 for non-empty batch, nil for empty."
  [batch]
  (when (seq batch)
    (inc (apply max (map :update_id batch)))))

;; Batch dispatch
(defn dispatch-batch
  "Dispatches each update in the batch to the handler.
   Catches exceptions and continues processing remaining updates."
  [batch handler]
  (doseq [update batch]
    (try
      (handler update)
      (catch Exception e
        (log/error e "Handler exception for update" (:update_id update))))))

;; Error handling
(defn handle-poll-error
  "Determines action for polling errors.
   Returns {:action :continue} or {:action :backoff}."
  [error]
  (condp = (:type error)
    :rate-limited {:action :backoff}
    {:action :continue}))

;; Polling loop state
(defrecord Poller [thread running])

;; Backoff constants
(def ^:private base-sleep-ms 100)
(def ^:private max-backoff-ms 30000)

(defn- get-updates-from-client
  "Extracts updates from the bot client.
   In production this would call the TG API via HTTP; currently reads
   from the client's :responses map (test-client pattern)."
  [client _offset]
  (try
    (let [resp (get-in client [:responses :get-updates])]
      {:ok true
       :result (get-in resp [:body :result] [])})
    (catch Exception e
      {:ok false
       :error {:type :network-error :message (.getMessage e)}})))

(defn- poll-loop
  "Main polling loop. Calls get-updates-fn and dispatches to handler.
   Sleeps between iterations to avoid tight spinning. Applies exponential
   backoff on errors."
  [get-updates-fn handler poller]
  (loop [offset nil
         backoff-ms 0]
    (when @(:running poller)
      (let [result (try
                     (get-updates-fn offset)
                     (catch Exception e
                       {:ok false
                        :error {:type :network-error :message (.getMessage e)}}))]
        (if (:ok result)
          (let [updates (:result result)
                new-offset (or (next-offset updates) offset)]
            (when (seq updates)
              (dispatch-batch updates handler))
            (Thread/sleep base-sleep-ms)
            (recur new-offset 0))
          ;; Error path — use handle-poll-error for backoff decision
          (let [{:keys [action]} (handle-poll-error (:error result))
                sleep-ms (if (= action :backoff)
                           (min max-backoff-ms (max base-sleep-ms (* 2 (if (pos? backoff-ms) backoff-ms base-sleep-ms))))
                           base-sleep-ms)]
            (log/warn "Poll error, action:" action "backoff:" sleep-ms "ms" (:error result))
            (Thread/sleep sleep-ms)
            (recur offset sleep-ms)))))))

(defn start-polling
  "Starts the long-polling loop in a background thread.
   Accepts an optional get-updates-fn (fn [offset] -> {:ok bool :result [updates]}).
   If not provided, uses the client's built-in responses.
   Returns a poller handle immediately (non-blocking)."
  ([bot handler]
   (start-polling bot handler nil))
  ([bot handler get-updates-fn]
   (let [running (atom true)
         poller  (->Poller (atom nil) running)
         fetch   (or get-updates-fn
                     (fn [offset] (get-updates-from-client (:client bot) offset)))
         thread  (Thread. (fn [] (poll-loop fetch handler poller)))]
     (reset! (:thread poller) thread)
     (.start thread)
     poller)))

(defn stop-polling
  "Stops the polling loop. Blocks until current poll/handlers complete (up to 5s)."
  [poller]
  (reset! (:running poller) false)
  (when-let [thread @(:thread poller)]
    (.join thread 5000)))
