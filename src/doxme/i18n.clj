(ns doxme.i18n
  (:require [zen.core :as zen]
            [clojure.string :as str]))

(def ^:private supported-languages #{:en :ru})

(defn- unwrap-ztx
  "Extract zen context atom from either a raw atom or a {:ztx atom} wrapper."
  [ztx]
  (if (and (map? ztx) (contains? ztx :ztx))
    (:ztx ztx)
    ztx))

(defn- normalize-lang-code [code]
  (when code
    (let [s (if (keyword? code) (name code) code)
          base (first (str/split s #"-"))]
      (keyword base))))

(defn- get-language-code [update]
  (or (some-> update :message :from :language_code)
      (some-> update :inline_query :from :language_code)
      (some-> update :callback_query :from :language_code)))

(defn- get-default-lang [ztx]
  (or (some-> ztx (zen/get-symbol 'doxme.bot/bot-config) :default-language)
      :en))

(defn- get-translations [ztx]
  (zen/get-symbol ztx 'doxme.bot/i18n))

(defn interpolate
  "Replace {var} placeholders in template with values from vars map.
   Missing vars leave placeholder unchanged. Nil values become empty strings."
  [template vars]
  (if (or (nil? vars) (empty? vars))
    template
    (reduce (fn [tmpl [k v]]
              (let [placeholder (str "{" (name k) "}")
                    replacement (if (nil? v) "" (str v))]
                (str/replace tmpl (re-pattern (str/replace placeholder "{" "\\{"))
                             replacement)))
            template
            vars)))

(defn t-with-translations
  "Translate key using provided translations map with explicit default-lang.
   Fallback chain: lang -> default-lang -> key as string."
  [translations default-lang lang key]
  (cond
    (nil? key) ""

    :else
    (let [lang-map (or (get translations lang)
                       (get translations default-lang)
                       {})
          value (get lang-map key)]
      (cond
        (string? value) value
        (some? value) (str value)
        :else (str key)))))

(defn t
  "Translate key for language lang using zen context.
   Fallback chain: lang -> :en -> key as string.
   Optional vars map for interpolation."
  ([ztx lang key]
   (t ztx lang key nil))
  ([ztx lang key vars]
   (cond
     (nil? ztx) {:error :invalid-context}
     (nil? key) ""
     :else
     (let [ztx (unwrap-ztx ztx)
           translations (get-translations ztx)
           default-lang (get-default-lang ztx)
           normalized-lang (or (normalize-lang-code lang) default-lang)
           effective-lang (if (contains? supported-languages normalized-lang)
                            normalized-lang
                            default-lang)
           lang-map (or (get translations effective-lang)
                        (get translations default-lang)
                        {})
           value (get lang-map key)
           template (cond
                      (string? value) value
                      (some? value) (str value)
                      :else (str key))]
       (interpolate template vars)))))

(defn detect-language
  "Detect language from Telegram update.
   Reads language_code from message/inline_query/callback_query.from.
   Falls back to default language if unsupported or missing."
  [ztx update]
  (let [ztx (unwrap-ztx ztx)
        default-lang (get-default-lang ztx)
        raw-code (get-language-code update)
        normalized (normalize-lang-code raw-code)]
    (if (contains? supported-languages normalized)
      normalized
      default-lang)))
