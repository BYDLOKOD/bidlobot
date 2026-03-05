(ns doxme.glm.client
  "GLM API client for YouTube video summarization."
  (:require [clojure.string :as str]
            [clojure.tools.logging :as log]))

(defn create-client
  "Creates a GLM client from config.
   Returns {:error :invalid-api-key :message ...} if api-key is missing/empty."
  [{:keys [api-key base-url model] :as config}]
  (if (or (nil? api-key) (str/blank? api-key))
    {:error :invalid-api-key
     :message "YouTube summary feature is not configured. Invalid GLM API key."}
    {:api-key api-key
     :base-url base-url
     :model model}))

(defn auth-headers
  "Returns HTTP headers with Authorization for GLM API."
  [client]
  {"Authorization" (str "Bearer " (:api-key client))})

(defn- system-message
  "Builds the system prompt based on language."
  [lang]
  (if (= lang "ru")
    "Вы помощник, который резюмирует транскрипции YouTube видео. Создайте краткое резюме с основными темами, ключевыми моментами и рекомендацией. Формат:\n\n<название>\n<продолжительность>\n\nОсновные темы:\n- Тема 1\n- Тема 2\n\nКлючевые моменты:\n- Момент 1\n- Момент 2\n\nСтоит смотреть, если: <рекомендация>"
    "You are a helpful assistant that summarizes YouTube video transcripts. Produce a concise summary with main topics, key points, and a recommendation. Format:\n\n<title>\n<duration>\n\nMain Topics:\n- Topic 1\n- Topic 2\n\nKey Points:\n- Point 1\n- Point 2\n\nWorth watching if: <recommendation>"))

(defn- format-duration
  "Formats seconds to human-readable duration."
  [seconds]
  (let [minutes (quot seconds 60)]
    (str minutes " minutes")))

(defn build-summarize-request
  "Builds a GLM API request for video summarization."
  [client {:keys [title duration-seconds transcript lang]}]
  {:model (:model client)
   :max_tokens 500
   :temperature 0.7
   :messages
   [{:role "system"
     :content (system-message lang)}
    {:role "user"
     :content (str "Summarize this video transcript:\n\n"
                   "Title: " title "\n"
                   "Duration: " (format-duration duration-seconds) "\n\n"
                   "Transcript:\n" transcript)}]})

(defn truncate-transcript
  "Truncates transcript to max-length chars if needed.
   Returns {:text ... :truncated true/false}."
  ([text max-length]
   (if (nil? text)
     {:text nil :truncated false}
     (let [len (count text)
           marker "... [transcript truncated]"
           marker-len (count marker)]
       (if (<= len max-length)
         {:text text :truncated false}
         (do
           (log/warn "Transcript truncated from" len "to" max-length "characters")
           (if (<= max-length marker-len)
             {:text (subs marker 0 max-length) :truncated true}
             {:text (str (subs text 0 (- max-length marker-len)) marker)
              :truncated true}))))))
  ([text]
   (truncate-transcript text 10000)))

(defn parse-response
  "Parses GLM API response.
   Returns {:summary ...} on success or {:error ... :message ...} on error."
  [{:keys [status body headers] :as response}]
  (condp = status
    200
    (let [content (get-in body [:choices 0 :message :content])]
      {:summary content})
    429
    {:error :rate-limited
     :message "Summary service is busy. Please try again in a minute."}
    {:error :glm-api-error
     :message "Summary service temporarily unavailable."}))

(defn handle-timeout
  "Returns error result for timeout scenarios."
  []
  {:error :timeout
   :message "Summary service timed out. Please try again."})
