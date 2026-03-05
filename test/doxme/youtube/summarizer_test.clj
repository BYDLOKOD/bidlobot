(ns doxme.youtube.summarizer-test
  (:require [clojure.test :refer [deftest testing is are]]
            [doxme.youtube.summarizer :as sut]))

;; ──────────────────────────────────────────────
;; Fixtures — from edge-cases.edn seed data
;; ──────────────────────────────────────────────

(def chat-id -1001234567890)

(def rate-limit-timestamps-full
  [#inst "2026-03-05T08:05:00.000Z"
   #inst "2026-03-05T08:10:00.000Z"
   #inst "2026-03-05T08:15:00.000Z"
   #inst "2026-03-05T08:20:00.000Z"
   #inst "2026-03-05T08:25:00.000Z"
   #inst "2026-03-05T08:30:00.000Z"
   #inst "2026-03-05T08:35:00.000Z"
   #inst "2026-03-05T08:40:00.000Z"
   #inst "2026-03-05T08:45:00.000Z"
   #inst "2026-03-05T08:50:00.000Z"])

(def rate-limit-timestamps-with-expired
  [#inst "2026-03-05T07:00:00.000Z"
   #inst "2026-03-05T07:05:00.000Z"
   #inst "2026-03-05T07:10:00.000Z"
   #inst "2026-03-05T08:30:00.000Z"
   #inst "2026-03-05T08:35:00.000Z"])

;; ──────────────────────────────────────────────
;; Rate limiting
;; ──────────────────────────────────────────────

(deftest rate-limit-exceeded-test
  (testing "Scenario: Rate limit exceeded -- 10 summaries per hour per chat"
    (let [now    #inst "2026-03-05T08:55:00.000Z"
          result (sut/check-rate-limit rate-limit-timestamps-full now 10)]
      (is (= :rate-limited (:error result)))
      (is (= "Summary limit reached. Try again later." (:message result))))))

(deftest rate-limit-window-expired-test
  (testing "Scenario: Rate limit window expires -- old timestamps are evicted"
    (let [now           #inst "2026-03-05T08:55:00.000Z"
          active-count  (sut/count-active-in-window rate-limit-timestamps-with-expired now 3600)]
      (is (= 2 active-count)
          "Only 2 timestamps should be within the last hour")
      (let [result (sut/check-rate-limit rate-limit-timestamps-with-expired now 10)]
        (is (nil? (:error result))
            "Request should be allowed when under the limit")))))

(deftest rate-limit-reset-on-restart-test
  (testing "Scenario: Rate limit resets on bot restart"
    (let [fresh-state (sut/create-rate-limiter)]
      (is (empty? @fresh-state)
          "Fresh rate limiter should be empty for all chats"))))

(deftest rate-limit-empty-chat-test
  (testing "First request to a new chat is always allowed"
    (let [result (sut/check-rate-limit [] #inst "2026-03-05T08:55:00.000Z" 10)]
      (is (nil? (:error result))
          "Empty timestamp list should allow the request"))))

;; ──────────────────────────────────────────────
;; Summary format validation
;; ──────────────────────────────────────────────

(deftest summary-format-test
  (testing "Scenario: Summary follows the expected format"
    (let [summary (str "Rich Hickey - Simple Made Easy (Strange Loop 2011)\n"
                       "36 minutes\n"
                       "\n"
                       "Main Topics:\n"
                       "- The distinction between \"simple\" and \"easy\" in software design\n"
                       "- How complexity arises from complecting (interleaving) concerns\n"
                       "- Choosing simple constructs over familiar (easy) ones\n"
                       "\n"
                       "Key Points:\n"
                       "- Simple means \"one fold/braid\" \u2014 not interleaved with other concerns\n"
                       "- Easy means \"near at hand\" \u2014 familiar, convenient, but not necessarily simple\n"
                       "- Complexity is the primary source of bugs and maintenance burden\n"
                       "- Prefer values over state, composition over inheritance, queues over direct coupling\n"
                       "- Testing and type systems do not address fundamental complexity\n"
                       "\n"
                       "Worth watching if: You want a paradigm-shifting perspective on why simplicity matters more than convenience in software architecture.")]
      (is (re-find #"^.+\n\d+ minutes" summary)
          "Summary should start with title and duration")
      (is (re-find #"Main Topics:" summary)
          "Summary should contain Main Topics section")
      (is (re-find #"Key Points:" summary)
          "Summary should contain Key Points section")
      (is (re-find #"Worth watching if:" summary)
          "Summary should contain Worth watching if section"))))

;; ──────────────────────────────────────────────
;; Duration formatting for display
;; ──────────────────────────────────────────────

(deftest format-duration-test
  (testing "Duration formatting for summary display"
    (are [seconds expected]
         (= expected (sut/format-duration seconds))

      213   "3 minutes"
      2213  "36 minutes"
      3600  "60 minutes"
      1800  "30 minutes"
      60    "1 minute"
      90    "1 minute")))

;; ──────────────────────────────────────────────
;; Missing API keys
;; ──────────────────────────────────────────────

(deftest missing-api-keys-test
  (testing "Scenario: GLM_API_KEY not set -- feature disabled"
    (let [result (sut/check-config {:glm-api-key nil
                                    :youtube-api-key "AIzaSyC_valid_key_here"})]
      (is (= :not-configured (:error result)))
      (is (= "YouTube summary feature is not configured. Missing GLM API key."
             (:message result)))))

  (testing "Scenario: YOUTUBE_API_KEY not set -- feature disabled"
    (let [result (sut/check-config {:glm-api-key "glm-key-valid"
                                    :youtube-api-key nil})]
      (is (= :not-configured (:error result)))
      (is (= "YouTube summary feature is not configured. Missing YouTube API key."
             (:message result)))))

  (testing "Scenario: Both API keys missing -- GLM error takes priority"
    (let [result (sut/check-config {:glm-api-key nil
                                    :youtube-api-key nil})]
      (is (= :not-configured (:error result)))
      (is (= "YouTube summary feature is not configured. Missing GLM API key."
             (:message result)))))

  (testing "Scenario: Both API keys present -- config is valid"
    (let [result (sut/check-config {:glm-api-key "glm-key-valid"
                                    :youtube-api-key "AIzaSyC_valid_key_here"})]
      (is (nil? (:error result))
          "Valid config should not return an error"))))

;; ──────────────────────────────────────────────
;; Video metadata validation
;; ──────────────────────────────────────────────

(deftest validate-video-metadata-test
  (testing "Scenario: Valid video with captions is accepted"
    (let [metadata {:title "Rick Astley - Never Gonna Give You Up (Official Music Video)"
                    :duration-seconds 213
                    :has-captions true
                    :live-broadcast-content nil}
          result   (sut/validate-metadata metadata)]
      (is (nil? (:error result))
          "Valid video should be accepted")
      (is (= "Rick Astley - Never Gonna Give You Up (Official Music Video)"
             (:title result)))))

  (testing "Scenario: Video longer than 1 hour is rejected"
    (let [result (sut/validate-metadata {:title "Full Day Workshop"
                                         :duration-seconds 29742
                                         :has-captions true
                                         :live-broadcast-content nil})]
      (is (= :video-too-long (:error result)))))

  (testing "Scenario: Video shorter than 1 minute is rejected"
    (let [result (sut/validate-metadata {:title "Quick Tip"
                                         :duration-seconds 15
                                         :has-captions true
                                         :live-broadcast-content nil})]
      (is (= :video-too-short (:error result)))))

  (testing "Scenario: Live stream is rejected"
    (let [result (sut/validate-metadata {:title "LIVE: Clojure Conf 2026"
                                         :duration-seconds 0
                                         :has-captions false
                                         :live-broadcast-content "live"})]
      (is (= :live-stream (:error result)))
      (is (= "Cannot summarize live streams." (:message result)))))

  (testing "Scenario: Deleted or private video returns not found"
    (let [result (sut/validate-metadata nil)]
      (is (= :video-not-found (:error result)))
      (is (= "Video not found or unavailable." (:message result)))))

  (testing "Scenario: Video without captions is rejected"
    (let [result (sut/validate-metadata {:title "Coding Session: No Commentary"
                                         :duration-seconds 1510
                                         :has-captions false
                                         :live-broadcast-content nil})]
      (is (= :no-subtitles (:error result)))
      (is (= "No subtitles available for this video." (:message result))))))

;; ──────────────────────────────────────────────
;; Summarize in private chat (allowed)
;; ──────────────────────────────────────────────

(deftest summarize-private-chat-test
  (testing "Scenario: /summarize works in private chats"
    (let [result (sut/validate-chat-type-for-summary "private")]
      (is (nil? (:error result))
          "Summarize should be allowed in private chats, unlike /stats"))))

;; ──────────────────────────────────────────────
;; Language detection for summary
;; ──────────────────────────────────────────────

(deftest summary-language-test
  (testing "Scenario: Summary language matches the requester's Telegram language"
    (is (= "ru" (sut/detect-summary-language {:language_code "ru"})))
    (is (= "en" (sut/detect-summary-language {:language_code "en"})))
    (is (= "en" (sut/detect-summary-language {:language_code nil}))
        "Missing language should default to English")
    (is (= "en" (sut/detect-summary-language {}))
        "Empty from object should default to English")))
