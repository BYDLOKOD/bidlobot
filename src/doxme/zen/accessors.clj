(ns doxme.zen.accessors
  (:require [zen.core :as zen]
            [clojure.string :as str]))

(defn- unwrap-ztx
  "Extract zen context atom from either a raw atom or a {:ztx atom} wrapper.
   Allows callers to pass loader/create-context result directly."
  [ztx]
  (if (and (map? ztx) (contains? ztx :ztx))
    (:ztx ztx)
    ztx))

(defn get-config
  "Get bot configuration from zen context.
   Returns the bot-config symbol data as a plain map."
  [ztx]
  (let [ztx (unwrap-ztx ztx)
        sym (zen/get-symbol ztx 'doxme.bot/bot-config)]
    (dissoc sym :zen/tags :zen/name :zen/file)))

(defn get-profile-fields
  "Get all profile field definitions, sorted: required first (alphabetically),
   then optional (alphabetically). Returns a vector of field maps."
  [ztx]
  (let [ztx (unwrap-ztx ztx)
        tag-syms (zen/get-tag ztx 'doxme.bot/profile-field)]
    (if (or (nil? tag-syms) (empty? tag-syms))
      []
      (let [fields (->> tag-syms
                        (map (fn [sym]
                               (let [data (zen/get-symbol ztx sym)
                                     field-name (keyword (name (symbol (name sym))))]
                                 (merge (dissoc data :zen/tags :zen/name :zen/file)
                                        {:name field-name}))))
                        (sort-by (fn [f]
                                   [(if (:required f) 0 1)
                                    (name (:name f))]))
                        vec)]
        fields))))

(defn get-inline-commands
  "Get all inline command definitions, sorted by :command.
   Returns a vector of command maps."
  [ztx]
  (let [ztx (unwrap-ztx ztx)
        tag-syms (zen/get-tag ztx 'doxme.bot/inline-command)]
    (if (or (nil? tag-syms) (empty? tag-syms))
      []
      (->> tag-syms
           (map (fn [sym]
                  (let [data (zen/get-symbol ztx sym)]
                    (dissoc data :zen/tags :zen/name :zen/file))))
           (sort-by :command)
           vec))))

(defn get-bot-commands
  "Get all bot command definitions, sorted by :command.
   Returns a vector of command maps."
  [ztx]
  (let [ztx (unwrap-ztx ztx)
        tag-syms (zen/get-tag ztx 'doxme.bot/bot-command)]
    (if (or (nil? tag-syms) (empty? tag-syms))
      []
      (->> tag-syms
           (map (fn [sym]
                  (let [data (zen/get-symbol ztx sym)]
                    (dissoc data :zen/tags :zen/name :zen/file))))
           (sort-by :command)
           vec))))

(defn get-i18n
  "Get translations for a specific language.
   Returns the translations map for that language, or nil if not found."
  [ztx lang]
  (let [ztx (unwrap-ztx ztx)
        i18n (zen/get-symbol ztx 'doxme.bot/i18n)]
    (get i18n lang)))

(defn validate-profile
  "Validate profile data against the profile schema.
   Also checks that all required profile fields are present.
   Returns {:valid true} or {:valid false :errors [...]}."
  [ztx data]
  (let [ztx (unwrap-ztx ztx)
        result (zen/validate ztx #{'doxme.bot/profile} data)
        zen-errors (:errors result)
        ;; Also check required fields from profile-field definitions
        required-fields (->> (get-profile-fields ztx)
                             (filter :required)
                             (map :name))
        missing-errors (->> required-fields
                            (remove #(contains? data %))
                            (mapv (fn [field]
                                    {:message (str field " is required")
                                     :path [field]
                                     :type :require})))
        all-errors (into (vec (mapv (fn [e]
                                      (cond-> {:message (or (:message e) (str e))}
                                        (:path e) (assoc :path (:path e))
                                        (:type e) (assoc :type (:type e))))
                                    zen-errors))
                         missing-errors)]
    (if (empty? all-errors)
      {:valid true}
      {:valid false :errors all-errors})))
