(ns doxme.youtube.url-test
  (:require [clojure.test :refer [deftest testing is are]]
            [doxme.youtube.url :as sut]))

;; ──────────────────────────────────────────────
;; Valid YouTube URLs — extract video ID
;; ──────────────────────────────────────────────

(deftest parse-valid-urls-test
  (testing "Scenario Outline: Parse valid YouTube URLs and extract video ID"
    (are [url video-id]
         (= {:video-id video-id} (sut/parse-url url))

      ;; Standard youtube.com/watch?v=
      "https://www.youtube.com/watch?v=dQw4w9WgXcQ"   "dQw4w9WgXcQ"
      ;; Without www
      "https://youtube.com/watch?v=LJ4d5CGN6bI"       "LJ4d5CGN6bI"
      ;; Short youtu.be format
      "https://youtu.be/kJQP7kiw5Fk"                  "kJQP7kiw5Fk"
      ;; Mobile URL
      "https://m.youtube.com/watch?v=9bZkp7q19f0"     "9bZkp7q19f0"
      ;; With timestamp parameter
      "https://www.youtube.com/watch?v=hY7m5jjJ9mM&t=120"  "hY7m5jjJ9mM"
      ;; With playlist and index parameters
      "https://www.youtube.com/watch?v=Ks-_Mh1QhMc&list=PLRqwX-V7Uu6ZiZxtDDRCi6uhfTH4FilpH&index=3"  "Ks-_Mh1QhMc"
      ;; youtu.be with timestamp
      "https://youtu.be/rfscVS0vtbw?t=300"             "rfscVS0vtbw"
      ;; HTTP (non-HTTPS)
      "http://www.youtube.com/watch?v=ZZ5LpwO-An4"    "ZZ5LpwO-An4"
      ;; Video ID with hyphens and underscores
      "https://www.youtube.com/watch?v=_-x0WkL3bXE"   "_-x0WkL3bXE"
      ;; Embed format
      "https://www.youtube.com/embed/M7lc1UVf-VE"     "M7lc1UVf-VE")))

;; ──────────────────────────────────────────────
;; Invalid YouTube URLs — appropriate errors
;; ──────────────────────────────────────────────

(deftest parse-invalid-urls-test
  (testing "Scenario Outline: Reject invalid URLs with appropriate error"
    (are [url error-code error-message]
         (= {:error error-code :message error-message}
            (sut/parse-url url))

      ;; Not a YouTube URL (Vimeo)
      "https://vimeo.com/123456789"
      :not-youtube-url "Only YouTube videos are supported."

      ;; Random website with watch?v= pattern
      "https://example.com/watch?v=abc123"
      :not-youtube-url "Only YouTube videos are supported."

      ;; YouTube homepage (no video ID)
      "https://www.youtube.com/"
      :missing-video-id "Invalid YouTube URL: no video ID found."

      ;; YouTube channel URL
      "https://www.youtube.com/@ClojureTV"
      :not-video-url "Invalid YouTube URL: not a video link."

      ;; YouTube playlist URL (no specific video)
      "https://www.youtube.com/playlist?list=PLRqwX-V7Uu6ZiZxtDDRCi6uhfTH4FilpH"
      :not-video-url "Invalid YouTube URL: not a video link."

      ;; Empty string
      ""
      :empty-url "No URL provided."

      ;; Not a URL at all
      "not-a-url-at-all"
      :invalid-url "Invalid URL format."

      ;; YouTube shorts (not supported)
      "https://www.youtube.com/shorts/dQw4w9WgXcQ"
      :not-video-url "Invalid YouTube URL: not a video link."

      ;; Truncated video ID (less than 11 chars)
      "https://www.youtube.com/watch?v=abc"
      :invalid-video-id "Invalid YouTube URL: malformed video ID.")))

;; ──────────────────────────────────────────────
;; ISO 8601 duration parsing
;; ──────────────────────────────────────────────

(deftest parse-iso-duration-test
  (testing "Scenario Outline: Parse ISO 8601 duration to seconds"
    (are [iso-duration seconds]
         (= seconds (sut/parse-iso-duration iso-duration))

      "PT3M33S"    213
      "PT36M53S"   2213
      "PT8H15M42S" 29742
      "PT15S"      15
      "PT1H"       3600
      "PT59M59S"   3599
      "P0D"        0
      "PT25M10S"   1510)))

;; ──────────────────────────────────────────────
;; Video duration validation
;; ──────────────────────────────────────────────

(deftest validate-duration-test
  (testing "Scenario: Video exactly 1 hour is accepted (boundary)"
    (let [result (sut/validate-duration 3600)]
      (is (nil? (:error result))
          "Exactly 3600 seconds should be accepted")))

  (testing "Scenario: Video 1 hour and 1 second is rejected (boundary)"
    (let [result (sut/validate-duration 3601)]
      (is (= :video-too-long (:error result)))
      (is (= "Video too long (max 1 hour)." (:message result)))))

  (testing "Scenario: Video exactly 1 minute is accepted (boundary)"
    (let [result (sut/validate-duration 60)]
      (is (nil? (:error result))
          "Exactly 60 seconds should be accepted")))

  (testing "Scenario: Video 59 seconds is rejected as too short (boundary)"
    (let [result (sut/validate-duration 59)]
      (is (= :video-too-short (:error result)))
      (is (= "Video too short to summarize." (:message result)))))

  (testing "Scenario: Video longer than 1 hour is rejected"
    (let [result (sut/validate-duration 29742)]
      (is (= :video-too-long (:error result)))))

  (testing "Scenario: Video shorter than 1 minute is rejected"
    (let [result (sut/validate-duration 15)]
      (is (= :video-too-short (:error result)))))

  (testing "Scenario: Live stream (0 seconds) is rejected"
    (let [result (sut/validate-duration 0)]
      (is (some? (:error result))
          "Zero-duration (live stream) should be rejected"))))

;; ──────────────────────────────────────────────
;; youtube? predicate
;; ──────────────────────────────────────────────

(deftest youtube-url-predicate-test
  (testing "Recognizes youtube.com variants"
    (is (true? (sut/youtube-url? "https://www.youtube.com/watch?v=abc12345678")))
    (is (true? (sut/youtube-url? "https://youtube.com/watch?v=abc12345678")))
    (is (true? (sut/youtube-url? "https://m.youtube.com/watch?v=abc12345678"))))

  (testing "Recognizes youtu.be"
    (is (true? (sut/youtube-url? "https://youtu.be/abc12345678"))))

  (testing "Rejects non-YouTube URLs"
    (is (false? (sut/youtube-url? "https://vimeo.com/123456789")))
    (is (false? (sut/youtube-url? "https://example.com/watch?v=abc123")))))
