(ns doxme.glm.client-test
  (:require [clojure.test :refer [deftest testing is are]]
            [doxme.glm.client :as sut]))

;; ──────────────────────────────────────────────
;; Fixtures — from glm-requests.edn seed data
;; ──────────────────────────────────────────────

(def valid-config
  {:api-key "glm-test-key-abc123def456ghi789"
   :base-url "https://open.bigmodel.cn/api/paas/v4"
   :model "glm-4-flash"})

;; ──────────────────────────────────────────────
;; Client creation
;; ──────────────────────────────────────────────

(deftest create-client-test
  (testing "Scenario: Valid config creates a client"
    (let [client (sut/create-client valid-config)]
      (is (some? client) "Client should be created with valid config")
      (is (= "glm-4-flash" (:model client)))
      (is (= "https://open.bigmodel.cn/api/paas/v4" (:base-url client)))))

  (testing "Scenario: Empty GLM API key returns invalid-api-key error"
    (let [result (sut/create-client (assoc valid-config :api-key ""))]
      (is (= :invalid-api-key (:error result)))
      (is (= "YouTube summary feature is not configured. Invalid GLM API key."
             (:message result)))))

  (testing "Scenario: Nil GLM API key returns invalid-api-key error"
    (let [result (sut/create-client (assoc valid-config :api-key nil))]
      (is (= :invalid-api-key (:error result))))))

;; ──────────────────────────────────────────────
;; Request construction
;; ──────────────────────────────────────────────

(deftest build-request-test
  (testing "Scenario: Successful English summary request construction"
    (let [client  (sut/create-client valid-config)
          request (sut/build-summarize-request
                   client
                   {:title "Rich Hickey - Simple Made Easy (Strange Loop 2011)"
                    :duration-seconds 2213
                    :transcript "So what I want to talk about today is sort of two words..."
                    :lang "en"})]
      (is (= "glm-4-flash" (:model request))
          "Request should use the configured model")
      (is (= 500 (:max_tokens request))
          "Max tokens should be 500")
      (is (= 0.7 (:temperature request))
          "Temperature should be 0.7")
      (is (= 2 (count (:messages request)))
          "Request should have system and user messages")
      (is (= "system" (:role (first (:messages request))))
          "First message should be system role")
      (is (= "user" (:role (second (:messages request))))
          "Second message should be user role")))

  (testing "Scenario: Request has Authorization header"
    (let [client  (sut/create-client valid-config)
          headers (sut/auth-headers client)]
      (is (= "Bearer glm-test-key-abc123def456ghi789"
             (get headers "Authorization"))
          "Authorization header should be Bearer + API key")))

  (testing "Scenario: System message instructs summary structure"
    (let [client  (sut/create-client valid-config)
          request (sut/build-summarize-request
                   client
                   {:title "Test Video"
                    :duration-seconds 600
                    :transcript "Some transcript..."
                    :lang "en"})
          system-msg (:content (first (:messages request)))]
      (is (re-find #"Main Topics" system-msg)
          "System message should mention Main Topics")
      (is (re-find #"Key Points" system-msg)
          "System message should mention Key Points")
      (is (re-find #"Worth watching if" system-msg)
          "System message should mention Worth watching if"))))

;; ──────────────────────────────────────────────
;; Russian language request
;; ──────────────────────────────────────────────

(deftest russian-language-request-test
  (testing "Scenario: Summary in Russian when requester language is ru"
    (let [client  (sut/create-client valid-config)
          request (sut/build-summarize-request
                   client
                   {:title "Clojure \u0434\u043b\u044f \u043d\u0430\u0447\u0438\u043d\u0430\u044e\u0449\u0438\u0445 \u2014 \u0412\u0432\u0435\u0434\u0435\u043d\u0438\u0435"
                    :duration-seconds 1800
                    :transcript "Some transcript..."
                    :lang "ru"})
          system-msg (:content (first (:messages request)))]
      (is (re-find #"\u041e\u0441\u043d\u043e\u0432\u043d\u044b\u0435 \u0442\u0435\u043c\u044b" system-msg)
          "Russian system message should contain '\u041e\u0441\u043d\u043e\u0432\u043d\u044b\u0435 \u0442\u0435\u043c\u044b'")
      (is (re-find #"\u041a\u043b\u044e\u0447\u0435\u0432\u044b\u0435 \u043c\u043e\u043c\u0435\u043d\u0442\u044b" system-msg)
          "Russian system message should contain '\u041a\u043b\u044e\u0447\u0435\u0432\u044b\u0435 \u043c\u043e\u043c\u0435\u043d\u0442\u044b'")
      (is (re-find #"\u0421\u0442\u043e\u0438\u0442 \u0441\u043c\u043e\u0442\u0440\u0435\u0442\u044c, \u0435\u0441\u043b\u0438" system-msg)
          "Russian system message should contain '\u0421\u0442\u043e\u0438\u0442 \u0441\u043c\u043e\u0442\u0440\u0435\u0442\u044c, \u0435\u0441\u043b\u0438'"))))

;; ──────────────────────────────────────────────
;; Transcript truncation
;; ──────────────────────────────────────────────

(deftest truncate-transcript-test
  (testing "Scenario: Transcript longer than 10000 chars is truncated"
    (let [long-transcript (apply str (repeat 24500 "a"))
          result          (sut/truncate-transcript long-transcript 10000)]
      (is (<= (count (:text result)) 10000)
          "Truncated transcript should not exceed 10000 chars")
      (is (true? (:truncated result))
          "Should indicate that truncation occurred")
      (is (.endsWith (:text result) "... [transcript truncated]")
          "Truncated content should end with marker")))

  (testing "Short transcript is not truncated"
    (let [short-transcript "Short text"
          result           (sut/truncate-transcript short-transcript 10000)]
      (is (= "Short text" (:text result)))
      (is (false? (:truncated result))))))

;; ──────────────────────────────────────────────
;; Response parsing
;; ──────────────────────────────────────────────

(deftest parse-response-test
  (testing "Scenario: Parse successful GLM response"
    (let [response {:id "glm-req-a1b2c3d4e5f6"
                    :created 1772582400
                    :model "glm-4-flash"
                    :choices [{:index 0
                               :finish_reason "stop"
                               :message {:role "assistant"
                                         :content "Summary content here"}}]
                    :usage {:prompt_tokens 1250
                            :completion_tokens 180
                            :total_tokens 1430}}
          result   (sut/parse-response {:status 200 :body response})]
      (is (= "Summary content here" (:summary result)))
      (is (nil? (:error result))))))

;; ──────────────────────────────────────────────
;; Error handling
;; ──────────────────────────────────────────────

(deftest error-handling-test
  (testing "Scenario: GLM API returns server error"
    (let [result (sut/parse-response {:status 500
                                      :body {:error {:message "Internal server error"}}})]
      (is (= :glm-api-error (:error result)))
      (is (= "Summary service temporarily unavailable." (:message result)))))

  (testing "Scenario: GLM API request times out"
    (let [result (sut/handle-timeout)]
      (is (= :timeout (:error result)))
      (is (= "Summary service timed out. Please try again." (:message result)))))

  (testing "Scenario: GLM API returns HTTP 429 rate limit"
    (let [result (sut/parse-response {:status 429
                                      :headers {"retry-after" "60"}
                                      :body {:error {:message "Rate limit exceeded"}}})]
      (is (= :rate-limited (:error result)))
      (is (= "Summary service is busy. Please try again in a minute."
             (:message result))))))
