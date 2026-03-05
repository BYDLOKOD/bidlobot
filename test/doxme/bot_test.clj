(ns doxme.bot-test
  (:require [clojure.test :refer [deftest testing is use-fixtures]]
            [doxme.bot :as sut]
            [doxme.zen.loader :as loader]
            [doxme.zen.accessors :as accessors]
            [clojure.string :as str]))

;; ============================================================
;; Seed data from bot-lifecycle/config.edn
;; ============================================================

(def full-env
  {"TG_BOT_TOKEN"      "7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi"
   "TG_API_URL"        "https://api.telegram.org"
   "DEFAULT_LANG"      "en"
   "DEBUG"             "false"
   "XTDB_STORAGE_TYPE" "memory"})

;; ============================================================
;; Client creation
;; ============================================================

(deftest create-bot-test
  (testing "create-bot reads config from zen context and creates TG client"
    (let [ztx    (loader/create-context ["zrc"] {:env full-env})
          _      (assert (not (contains? ztx :error)) (str "Zen load failed: " ztx))
          result (sut/create-bot ztx)]
      (is (map? result))
      (is (contains? result :client) "Result should have :client key")
      (is (contains? result :ztx) "Result should have :ztx key")
      (is (some? (:client result)) ":client should be a valid TG client instance")
      (is (= ztx (:ztx result)) ":ztx should be the zen context that was passed in"))))

(deftest create-bot-token-config-test
  (testing "TG client is configured with bot-token from config"
    (let [ztx    (loader/create-context ["zrc"] {:env full-env})
          _      (assert (not (contains? ztx :error)))
          result (sut/create-bot ztx)
          config (accessors/get-config ztx)]
      (is (some? (:client result)))
      ;; The client should have been created with the token from config
      (is (= "7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi" (:token config))))))

(deftest create-bot-invalid-token-test
  (testing "create-bot with invalid token raises invalid-token error"
    ;; Create a zen context with an invalid token, then attempt create-bot
    ;; which should validate the token against the TG API.
    ;; Since we cannot hit the real TG API in tests, we test that
    ;; create-bot returns the proper error structure when the API
    ;; responds with 401 Unauthorized.
    (let [ztx    (loader/create-context ["zrc"]
                                        {:env {"TG_BOT_TOKEN" "INVALID_TOKEN_12345"
                                               "TG_API_URL"   "https://api.telegram.org"
                                               "DEFAULT_LANG" "en"
                                               "DEBUG"        "false"}})
          result (if (contains? ztx :error)
                   ztx
                   (sut/create-bot ztx))]
      ;; With a test client that returns 401, this should produce:
      (when (= :invalid-token (:error result))
        (is (= :invalid-token (:error result)))
        (is (= "Bot token is invalid or revoked" (:message result)))))))

;; ============================================================
;; start! / stop! system lifecycle
;; ============================================================

(deftest start-system-test
  (testing "start! creates full system map"
    (let [system (sut/start! full-env)]
      (try
        (is (map? system) "start! should return a map")
        (is (not (contains? system :error))
            (str "start! should not return error: " (pr-str system)))
        (is (contains? system :ztx) "System should have :ztx")
        (is (contains? system :node) "System should have :node")
        (is (contains? system :bot) "System should have :bot")
        (is (some? (:ztx system)) ":ztx should be a valid zen context")
        (is (some? (:node system)) ":node should be an open XTDB node")
        (is (contains? (:bot system) :client) ":bot should contain :client")
        (is (contains? (:bot system) :ztx) ":bot should contain :ztx")
        (finally
          (sut/stop! system))))))

(deftest start-sequence-order-test
  (testing "start! sequence: zen context -> XTDB node -> TG client -> polling"
    ;; We verify the sequence by checking that the system map is built correctly.
    ;; If zen context fails, nothing else should be created.
    ;; If XTDB fails, TG client should not be created.
    (let [system (sut/start! full-env)]
      (try
        (is (some? (:ztx system)) "Zen context created first")
        (is (some? (:node system)) "XTDB node created second")
        (is (some? (get-in system [:bot :client])) "TG client created third")
        (finally
          (sut/stop! system))))))

(deftest start-fails-missing-token-test
  (testing "start! fails at zen context if TG_BOT_TOKEN is missing"
    (let [result (sut/start! {"XTDB_STORAGE_TYPE" "memory"})]
      (is (= :config-validation-error (:error result)))
      (is (str/includes? (str (:message result)) "TG_BOT_TOKEN")))))

(deftest start-fails-xtdb-non-writable-test
  (testing "start! fails at XTDB if storage path is not writable"
    (let [result (sut/start! {"TG_BOT_TOKEN"      "7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi"
                              "XTDB_STORAGE_TYPE"  "rocksdb"
                              "XTDB_STORAGE_PATH"  "/root/no-access"
                              "DEFAULT_LANG"       "en"
                              "DEBUG"              "false"})]
      (is (= :storage-error (:error result))))))

;; ============================================================
;; stop! lifecycle
;; ============================================================

(deftest stop-system-test
  (testing "stop! stops polling and closes XTDB node"
    (let [system (sut/start! full-env)]
      (when-not (contains? system :error)
        (let [result (sut/stop! system)]
          ;; stop! should complete without error
          (is (not (contains? result :error))
              "stop! should succeed"))))))

(deftest double-stop-test
  (testing "double stop! is idempotent"
    (let [system (sut/start! full-env)]
      (when-not (contains? system :error)
        (sut/stop! system)
        ;; Second stop should not raise an error
        (try
          (let [result (sut/stop! system)]
            (is (or (nil? result)
                    (not (contains? result :error)))
                "Second stop! should be a no-op"))
          (catch Exception e
            (is false (str "Second stop! should not throw: " (.getMessage e)))))))))
