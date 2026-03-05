(ns doxme.youtube.url
  (:require [clojure.string :as str]))

;; Video ID format: 11 chars [A-Za-z0-9_-]
(def video-id-pattern #"[A-Za-z0-9_-]{11}")

(defn youtube-url?
  "Returns true if the URL is a YouTube URL (youtube.com, m.youtube.com, or youtu.be)."
  [url]
  (boolean
   (when (string? url)
     (or (re-find #"(?i)^(https?://)?(www\.)?(m\.)?youtube\.com" url)
         (re-find #"(?i)^(https?://)?(www\.)?youtu\.be" url)))))

(defn- valid-url-format?
  "Returns true if the string looks like a valid URL."
  [url]
  (boolean (re-find #"(?i)^https?://" url)))

(defn- extract-video-id-raw
  "Extract video ID from a YouTube URL (any length). Returns nil if not found."
  [url]
  (or
   ;; youtube.com/watch?v=VIDEO_ID
   (second (re-find #"[?&]v=([A-Za-z0-9_-]+)" url))
   ;; youtu.be/VIDEO_ID
   (second (re-find #"youtu\.be/([A-Za-z0-9_-]+)" url))
   ;; youtube.com/embed/VIDEO_ID
   (second (re-find #"youtube\.com/embed/([A-Za-z0-9_-]+)" url))))

(defn- valid-video-id?
  "Returns true if the video ID is exactly 11 chars [A-Za-z0-9_-]."
  [video-id]
  (and video-id
       (string? video-id)
       (re-matches video-id-pattern video-id)))

(defn- video-url-pattern?
  "Returns true if the URL matches a video URL pattern (watch, embed, youtu.be)."
  [url]
  (boolean
   (or (re-find #"(?i)youtube\.com/watch\?" url)
       (re-find #"(?i)youtube\.com/embed/" url)
       (re-find #"(?i)youtu\.be/" url))))

(defn- youtube-homepage?
  "Returns true if the URL is just the YouTube homepage (no video path)."
  [url]
  (boolean
   (re-find #"(?i)^https?://(www\.)?(m\.)?youtube\.com/?(\?.*)?$" url)))

(defn parse-url
  "Parse a YouTube URL and extract the video ID.
   Returns {:video-id \"...\"} on success.
   Returns {:error :keyword :message \"string\"} on failure."
  [url]
  (cond
    ;; Empty or nil
    (or (nil? url) (and (string? url) (str/blank? url)))
    {:error :empty-url :message "No URL provided."}

    ;; Not a string
    (not (string? url))
    {:error :invalid-url :message "Invalid URL format."}

    ;; Not a valid URL format
    (not (valid-url-format? url))
    {:error :invalid-url :message "Invalid URL format."}

    ;; Check if it's a YouTube URL
    (not (youtube-url? url))
    {:error :not-youtube-url :message "Only YouTube videos are supported."}

    ;; YouTube homepage (no video ID)
    (youtube-homepage? url)
    {:error :missing-video-id :message "Invalid YouTube URL: no video ID found."}

    ;; Check for supported video URL patterns
    (not (video-url-pattern? url))
    {:error :not-video-url :message "Invalid YouTube URL: not a video link."}

    ;; Extract and validate video ID
    :else
    (let [video-id (extract-video-id-raw url)]
      (cond
        (nil? video-id)
        {:error :missing-video-id :message "Invalid YouTube URL: no video ID found."}

        (not (valid-video-id? video-id))
        {:error :invalid-video-id :message "Invalid YouTube URL: malformed video ID."}

        :else
        {:video-id video-id}))))

(defn parse-iso-duration
  "Parse ISO 8601 duration string (e.g., PT3M33S) to seconds.
   Returns 0 for P0D or unparseable durations."
  [iso-duration]
  (if (or (nil? iso-duration) (not (string? iso-duration)))
    0
    (if (= iso-duration "P0D")
      0
      (let [;; Extract hours, minutes, seconds
            hours (if-let [h (re-find #"(?:(\d+)H)" iso-duration)]
                    (Long/parseLong (second h))
                    0)
            minutes (if-let [m (re-find #"(?:(\d+)M)" iso-duration)]
                      (Long/parseLong (second m))
                      0)
            seconds (if-let [s (re-find #"(?:(\d+)S)" iso-duration)]
                      (Long/parseLong (second s))
                      0)]
        (+ (* hours 3600) (* minutes 60) seconds)))))

(defn validate-duration
  "Validate video duration is within acceptable range (60s to 3600s).
   Returns {:error nil} for valid durations.
   Returns {:error :keyword :message \"string\"} for invalid durations."
  [seconds]
  (cond
    (< seconds 60)
    {:error :video-too-short :message "Video too short to summarize."}

    (> seconds 3600)
    {:error :video-too-long :message "Video too long (max 1 hour)."}

    :else
    {:error nil}))
