(ns doxme.query.parser)

(def ^:private known-commands #{:user :chat :help})

(def ^:private max-query-length 500)

(defn- parse-token [token]
  (if (and (seq token) (= \: (first token)))
    (keyword (subs token 1))
    token))

(defn- extract-command [token]
  (when (and (seq token) (= \: (first token)))
    (subs token 1)))

(defn parse [query]
  (cond
    (> (count query) max-query-length)
    {:error :query-too-long}

    (or (nil? query) (clojure.string/blank? query))
    {:error :empty-query}

    :else
    (let [trimmed (clojure.string/trim query)
          tokens (clojure.string/split trimmed #"\s+")
          first-token (first tokens)]
      (cond
        (or (clojure.string/blank? first-token)
            (not= \: (first first-token)))
        {:error :invalid-syntax}

        (clojure.string/includes? first-token "::")
        {:error :invalid-syntax}

        :else
        (let [cmd-str (extract-command first-token)
              cmd-kw (keyword cmd-str)]
          (cond
            (clojure.string/blank? cmd-str)
            {:error :invalid-syntax}

            (not (contains? known-commands cmd-kw))
            {:error :unknown-command :command cmd-str}

            :else
            (let [arg-tokens (rest tokens)
                  args (mapv parse-token arg-tokens)]
              (if (some #(clojure.string/includes? % "::") arg-tokens)
                {:error :invalid-syntax}
                {:cmd cmd-kw :args args}))))))))
