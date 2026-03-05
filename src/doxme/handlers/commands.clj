(ns doxme.handlers.commands
  "Telegram command handlers for profile operations."
  (:require [clojure.string :as str]
            [doxme.profiles.core :as profiles]))

(def valid-fields
  "Set of valid profile fields that can be updated."
  #{:salary :stack :role :location :bio})

;; --- Helper functions ---

(defn- get-chat-type [update]
  (get-in update [:message :chat :type]))

(defn- get-user-id [update]
  (get-in update [:message :from :id]))

(defn- get-chat-id [update]
  (get-in update [:message :chat :id]))

(defn- get-username [update]
  (get-in update [:message :from :username]))

(defn- get-message-text [update]
  (get-in update [:message :text]))

(defn- group-chat? [update]
  (= "group" (get-chat-type update)))

(defn- private-chat? [update]
  (= "private" (get-chat-type update)))

(defn- session-key [update]
  [(get-user-id update) (get-chat-id update)])

(defn- get-session [ctx update]
  (get @(:sessions ctx) (session-key update)))

(defn- parse-profile-args
  "Parse /profile arguments. Returns username if specified, nil otherwise."
  [text]
  (when-let [args (second (re-matches #"/profile\s+(.+)" text))]
    (profiles/strip-at-prefix args)))

(defn- parse-update-args
  "Parse /update arguments. Returns {:field :value} or {} if no args."
  [text]
  (if-let [args (second (re-matches #"/update\s+(.+)" text))]
    (let [[field-str & value-parts] (str/split args #"\s+" 2)
          field-kw (when (str/starts-with? field-str ":")
                     (keyword (subs field-str 1)))]
      {:field field-kw
       :value (str/join " " value-parts)})
    {}))

;; --- Command handlers ---

(defn handle-register
  "Handle /register command.
   - In group chat: sends deep link to private chat
   - In private chat: starts or resumes registration form"
  [ctx update]
  (let [sessions (:sessions ctx)]
    (cond
      (group-chat? update)
      {:action :send-deep-link}

      (private-chat? update)
      (if-let [session (get-session ctx update)]
        ;; Resume existing session
        {:action :resume-form
         :state (:state session)}
        ;; Start new registration
        {:action :start-form
         :initial-state :step/salary})

      :else
      {:action :error :error :unsupported-chat-type})))

(defn handle-profile
  "Handle /profile command.
   - No args: show own profile
   - With @username: show that user's profile"
  [ctx update]
  (let [node (:node ctx)
        user-id (get-user-id update)
        chat-id (get-chat-id update)
        text (get-message-text update)
        target-username (parse-profile-args text)]
    (if target-username
      ;; Looking up another user
      (if-let [profile (profiles/get-profile-by-username node target-username chat-id)]
        {:action :show-profile
         :target-username target-username
         :profile {:salary (:profile/salary profile)
                   :stack (:profile/stack profile)
                   :role (:profile/role profile)
                   :location (:profile/location profile)
                   :bio (:profile/bio profile)}}
        {:action :error
         :error :profile/not-found})
      ;; Looking up own profile
      (if-let [profile (profiles/get-profile-by-id node user-id chat-id)]
        {:action :show-profile
         :profile {:salary (:profile/salary profile)
                   :stack (:profile/stack profile)
                   :role (:profile/role profile)
                   :location (:profile/location profile)
                   :bio (:profile/bio profile)}}
        {:action :error
         :error :error/not-registered}))))

(defn handle-update
  "Handle /update command.
   - With :field value: update single field
   - No args: start edit form"
  [ctx update]
  (let [node (:node ctx)
        user-id (get-user-id update)
        chat-id (get-chat-id update)
        text (get-message-text update)
        {:keys [field value]} (parse-update-args text)]
    (cond
      ;; No args - start edit form
      (nil? field)
      {:action :start-edit-form}

      ;; Unknown field
      (not (contains? valid-fields field))
      {:action :error
       :error :unknown-field}

      ;; Check if user is registered
      :else
      (if (profiles/get-profile-by-id node user-id chat-id)
        {:action :update-field
         :field field
         :value value
         :message :profile/updated}
        {:action :error
         :error :error/not-registered}))))

(defn handle-cancel
  "Handle /cancel command. Clears active session state."
  [ctx update]
  (let [sessions (:sessions ctx)
        key (session-key update)]
    (when-let [session (get @sessions key)]
      (swap! sessions assoc-in [key :state] :idle))
    {:action :cancelled}))
