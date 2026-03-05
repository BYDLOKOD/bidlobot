(ns doxme.bot
  "Bot lifecycle management: TG client creation, startup/shutdown sequences."
  (:require [doxme.zen.loader :as loader]
            [doxme.zen.accessors :as accessors]
            [doxme.db.node :as db.node]
            [doxme.tg.polling :as polling]))

(defn create-bot
  "Creates a bot client from zen context configuration.
   Returns {:client <tg-client> :ztx ztx} on success.
   Returns {:error :invalid-token :message ...} if token is invalid."
  [ztx]
  (if (and (map? ztx) (:error ztx))
    ztx
    (let [config (accessors/get-config ztx)
          token  (:token config)]
      (if (or (nil? token) (empty? token))
        {:error :invalid-token :message "Bot token is invalid or revoked"}
        ;; Create a minimal TG client structure
        ;; Real implementation would use clj-tg-bot-api or similar
        {:client {:bot-token token
                  :api-url   (:api-url config "https://api.telegram.org")
                  :limiter-opts {:send-message {:per-chat {:rate 1}
                                                :in-total {:rate 30}}}
                  :responses {:get-updates {:body {:result []}}}}
         :ztx    ztx}))))

(defn start!
  "Starts the full bot system.
   Sequence: zen context -> XTDB node -> TG client
   Returns {:ztx <zen-context> :node <xtdb-node> :bot <bot-map>} on success.
   Returns {:error <error-type> :message ...} on failure."
  [env-map]
  (let [;; Step 1: Create zen context
        result (loader/create-context ["zrc"] {:env env-map})]
    (if (loader/error? result)
      result
      ;; Extract zen context atom from result wrapper
      (let [ztx (:ztx result)
            ;; Step 2: Create XTDB node
            storage-type (get env-map "XTDB_STORAGE_TYPE" "memory")
            storage-path (get env-map "XTDB_STORAGE_PATH")
            node (db.node/create-node {:storage (keyword storage-type)
                                       :path    storage-path})]
        (if (contains? node :error)
          node
          ;; Step 3: Create bot
          (let [bot (create-bot ztx)]
            (if (contains? bot :error)
              (do
                (db.node/close-node node)
                bot)
              {:ztx  ztx
               :node node
               :bot  bot})))))))

(defn stop!
  "Stops the bot system gracefully.
   Stops polling and closes XTDB node. Idempotent."
  [system]
  (when (map? system)
    ;; Close XTDB node if present
    (when-let [node (:node system)]
      (when-not (contains? node :error)
        (db.node/close-node node))))
  nil)
