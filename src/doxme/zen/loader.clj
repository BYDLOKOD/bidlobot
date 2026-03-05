(ns doxme.zen.loader
  (:require [zen.core :as zen]
            [clojure.java.io :as io]
            [clojure.string :as str]
            [edamame.core]))

(def ^:private zen-internal-keys
  "Zen injects :zen/name, :zen/tags, :zen/file into every symbol.
   Validation reports these as 'unknown key' — they are harmless."
  #{:zen/name :zen/tags :zen/file})

(defn- zen-internal-error?
  "Returns true for zen-internal validation noise that should be ignored:
   - 'unknown key' for :zen/name, :zen/tags, :zen/file (injected metadata)
   - bootstrap 'Could not resolve symbol' for self-referential zen/type, zen/tag"
  [e]
  (and (map? e)
       (or ;; unknown-key for zen-injected metadata
        ;; :type can be string "unknown-key" or keyword :unknown-key
        (and (let [tp (:type e)]
               (= "unknown-key" (if (keyword? tp) (name tp) (str tp))))
             (some zen-internal-keys (:path e)))
           ;; bootstrap self-referential symbol errors
        (let [msg (str (:message e))]
          (and (str/starts-with? msg "Could not resolve symbol")
               (str/includes? msg " in zen/"))))))

(defn error?
  "Returns true if result from create-context is an error.
   Works with both error maps {:error ...} and success maps {:ztx ...}."
  [result]
  (contains? result :error))

(defn- dir-exists? [path]
  (let [f (io/file path)]
    (and (.exists f) (.isDirectory f))))

(defn- make-tag-readers [env-map]
  (let [env-map (or env-map {})]
    {:readers
     {'env (fn [x]
             (if (vector? x)
               (let [[var-name default] x]
                 (or (get env-map (str var-name)) default))
               (if-let [v (get env-map (str x))]
                 v
                 (throw (ex-info (str "Missing required environment variable: " x)
                                 {:var (str x)})))))
      'env-keyword (fn [[var-name default]]
                     (if-let [v (get env-map (str var-name))]
                       (keyword v)
                       default))
      'env-boolean (fn [[var-name default]]
                     (if-let [v (get env-map (str var-name))]
                       (Boolean/parseBoolean v)
                       default))}}))

(defn- check-empty-file [paths]
  (let [pth "doxme/bot.edn"]
    (some (fn [base-path]
            (let [file (io/file base-path pth)]
              (when (.exists file)
                (let [content (slurp file)]
                  (when (str/blank? content)
                    :empty)))))
          paths)))

(defn- pre-check-edn [paths env-map]
  "Pre-read the EDN to catch parse errors before zen processes it.
   Returns nil if OK, or an error map."
  (let [pth "doxme/bot.edn"]
    (some (fn [base-path]
            (let [file (io/file base-path pth)]
              (when (.exists file)
                (let [content (slurp file)]
                  (when (str/blank? content)
                    {:error :config-parse-error
                     :message "Empty configuration file"})))))
          paths)))

(defn- read-ns-from-paths
  "Read a zen namespace from :paths directories (not classpath).
   Uses edamame with custom readers from the context atom.
   Falls back to zen/read-ns if not found in paths."
  [ztx ns-sym]
  (let [pth (str (str/replace (str ns-sym) #"\." "/") ".edn")
        readers (:readers @ztx)
        content (loop [[p & ps] (:paths @ztx)]
                  (when p
                    (let [file (io/file p pth)]
                      (if (.exists file)
                        (slurp file)
                        (recur ps)))))]
    (if content
      (let [parse-opts (when readers {:readers readers})
            nmsps (if parse-opts
                    (edamame.core/parse-string content parse-opts)
                    (edamame.core/parse-string content))]
        (when-not (get nmsps 'ns)
          (throw (ex-info "No ns declaration found in config"
                          {:type :missing-ns})))
        (zen/load-ns ztx nmsps))
      ;; Fall back to classpath lookup
      (zen/read-ns ztx ns-sym))))

(defn create-context
  "Creates a zen context from config paths.
   Returns {:ztx <zen-context-atom>} on success, or {:error <type> ...} on failure.
   Use (loader/error? result) to check. Extract atom via (:ztx result).
   Options:
     :env - map of environment variables for #env tag resolution"
  [paths {:keys [env] :as opts}]
  (let [paths (or paths ["zrc"])]
    (if-not (some dir-exists? paths)
      {:error :config-not-found}
      ;; Check for empty file first
      (if-let [empty-err (pre-check-edn paths env)]
        empty-err
        ;; Create zen context with custom readers for #env tags
        (try
          (let [reader-opts (make-tag-readers env)
                ztx (zen/new-context {:paths paths})]
            ;; Inject custom tag readers into the context atom
            (swap! ztx merge reader-opts)
            ;; Clear bootstrap errors from new-context (zen/type, zen/tag self-refs)
            (swap! ztx dissoc :errors)
            ;; Read the namespace from :paths first (not classpath)
            (read-ns-from-paths ztx 'doxme.bot)
            (let [ctx @ztx
                  ;; Filter out zen-internal noise (unknown-key for :zen/name etc.)
                  errors (remove zen-internal-error? (:errors ctx))]
              (if (seq errors)
                (let [error-details (mapv (fn [e]
                                            (if (map? e)
                                              e
                                              {:message (str e)}))
                                          errors)]
                  (cond
                    ;; Missing ns — "No file for ns" from zen read-ns
                    (some #(and (map? %)
                                (str/includes? (str (:message %)) "No file for ns"))
                          errors)
                    {:error :config-validation-error
                     :errors [{:type :missing-ns :message (str (:message (first errors)))}]}

                    ;; Schema validation or other errors with structured data
                    :else
                    {:error :config-validation-error
                     :errors (mapv (fn [e]
                                     (let [msg (or (:message e) (str e))
                                           tp  (:type e)
                                           ;; Normalize zen error types to keywords
                                           ;; and map zen-internal names to test-expected names
                                           tp-kw (when tp
                                                   (let [s (if (keyword? tp) (name tp) (str tp))]
                                                     (case s
                                                       "primitive-type" :type-mismatch
                                                       (keyword s))))]
                                       (cond-> {:message msg}
                                         (:path e) (assoc :path (:path e))
                                         tp-kw     (assoc :type tp-kw))))
                                   errors)}))
                ;; Success — wrap in map so callers can use (contains? result :error)
                {:ztx ztx})))
          (catch clojure.lang.ExceptionInfo e
            (let [msg (ex-message e)
                  data (ex-data e)]
              (cond
                (= :missing-ns (:type data))
                {:error :config-validation-error
                 :errors [{:type :missing-ns
                           :message "No ns declaration found in config"}]}

                (str/includes? msg "Missing required environment variable")
                {:error :config-validation-error
                 :message msg}

                (or (str/includes? msg "Duplicate key")
                    (str/includes? msg "duplicate key"))
                {:error :config-parse-error
                 :message (str/replace msg #"(?i).*duplicate key:\s*" "Duplicate key: ")}

                (or (str/includes? msg "EOF")
                    (str/includes? msg "Unexpected end of input"))
                {:error :config-parse-error
                 :message "Unexpected end of input"}

                :else
                {:error :config-parse-error
                 :message msg})))
          (catch Exception e
            (let [msg (str (ex-message e))]
              (cond
                (or (str/includes? msg "Duplicate key")
                    (str/includes? msg "duplicate key"))
                {:error :config-parse-error :message msg}

                (or (str/includes? msg "Unexpected end of input")
                    (str/includes? msg "EOF"))
                {:error :config-parse-error :message "Unexpected end of input"}

                :else
                {:error :config-parse-error :message msg}))))))))
