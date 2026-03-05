(ns doxme.db.ops-test
  (:require [clojure.test :refer [deftest testing is are use-fixtures]]
            [doxme.db.node :as node]
            [doxme.db.ops :as sut]
            [clojure.string :as str]))

;; ============================================================
;; Fixtures — create and tear down in-memory XTDB node
;; ============================================================

(def ^:dynamic *node* nil)

(defn node-fixture [f]
  (let [n (node/create-node {:storage :memory})]
    (assert (not (contains? n :error))
            (str "Failed to create XTDB node: " (pr-str n)))
    (binding [*node* n]
      (try
        (f)
        (finally
          (node/close-node n))))))

(use-fixtures :each node-fixture)

;; ============================================================
;; put-doc
;; ============================================================

(deftest put-new-profile-test
  (testing "put a new profile document"
    (let [doc    {:xt/id            :profile/294817365-(-1001987654321)
                  :profile/user-id  294817365
                  :profile/chat-id  -1001987654321
                  :profile/username "veschin"
                  :profile/salary   "150k USD"}
          result (sut/put-doc *node* doc)]
      (is (:tx-committed? result))
      (let [retrieved (sut/get-doc *node* :profile/294817365-(-1001987654321))]
        (is (= "veschin" (:profile/username retrieved)))
        (is (= "150k USD" (:profile/salary retrieved)))))))

(deftest put-user-stats-test
  (testing "put user-stats document"
    (let [doc    {:xt/id                    :user-stats/294817365-(-1001987654321)
                  :user-stats/user-id       294817365
                  :user-stats/chat-id       -1001987654321
                  :user-stats/message-count 1}
          result (sut/put-doc *node* doc)]
      (is (:tx-committed? result)))))

(deftest put-doc-missing-id-test
  (testing "put document without :xt/id returns missing-id error"
    (let [result (sut/put-doc *node* {:profile/user-id 294817365
                                      :profile/salary  "150k USD"})]
      (is (= :missing-id (:error result)))
      (is (= "Document must have :xt/id" (:message result))))))

(deftest put-doc-nil-id-test
  (testing "put document with nil :xt/id returns missing-id error"
    (let [result (sut/put-doc *node* {:xt/id nil :profile/salary "100k"})]
      (is (= :missing-id (:error result)))
      (is (= "Document must have :xt/id" (:message result))))))

(deftest put-overwrites-existing-test
  (testing "put overwrites existing document (last write wins)"
    (sut/put-doc *node* {:xt/id           :profile/294817365-(-1001987654321)
                         :profile/salary  "150k USD"})
    (let [result (sut/put-doc *node* {:xt/id           :profile/294817365-(-1001987654321)
                                      :profile/salary  "200k USD"})]
      (is (:tx-committed? result))
      (let [doc (sut/get-doc *node* :profile/294817365-(-1001987654321))]
        (is (= "200k USD" (:profile/salary doc)))))))

(deftest put-on-closed-node-test
  (testing "put on a closed node returns node-closed error"
    (let [n (node/create-node {:storage :memory})]
      (node/close-node n)
      (let [result (sut/put-doc n {:xt/id :profile/test :profile/salary "100k"})]
        (is (= :node-closed (:error result)))
        (is (= "Node is already closed" (:message result)))))))

(deftest concurrent-puts-test
  (testing "concurrent puts to same :xt/id - last write wins"
    (let [id  :profile/294817365-(-1001987654321)
          f1  (future (sut/put-doc *node* {:xt/id id :profile/salary "150k USD"}))
          f2  (future (sut/put-doc *node* {:xt/id id :profile/salary "200k USD"}))]
      (is (:tx-committed? @f1))
      (is (:tx-committed? @f2))
      ;; After both complete, the last indexed write wins
      (let [doc (sut/get-doc *node* id)]
        (is (= "200k USD" (:profile/salary doc))
            "Second (later) write should win"))))

  (testing "all writes are synchronous - function returns after tx is indexed"
    (let [doc {:xt/id :profile/sync-test :profile/salary "sync"}]
      (sut/put-doc *node* doc)
      ;; Immediately calling get-doc should return the document
      (is (some? (sut/get-doc *node* :profile/sync-test))))))

(deftest put-special-characters-test
  (testing "put document with special characters in username"
    (let [result (sut/put-doc *node* {:xt/id            :profile/294817365-(-1001987654321)
                                      :profile/username "user.name-test_123"})]
      (is (:tx-committed? result))
      (let [doc (sut/get-doc *node* :profile/294817365-(-1001987654321))]
        (is (= "user.name-test_123" (:profile/username doc)))))))

(deftest put-large-bio-test
  (testing "put document with large bio field"
    (let [bio    (apply str (repeat 500 "A"))
          result (sut/put-doc *node* {:xt/id        :profile/111222333-(-1001987654321)
                                      :profile/bio  bio})]
      (is (:tx-committed? result)))))

;; ============================================================
;; get-doc
;; ============================================================

(deftest get-existing-profile-test
  (testing "get existing profile document"
    (sut/put-doc *node* {:xt/id              :profile/294817365-(-1001987654321)
                         :profile/user-id    294817365
                         :profile/username   "veschin"
                         :profile/salary     "150k USD"})
    (let [doc (sut/get-doc *node* :profile/294817365-(-1001987654321))]
      (is (map? doc))
      (is (= "veschin" (:profile/username doc)))
      (is (= "150k USD" (:profile/salary doc))))))

(deftest get-nonexistent-document-test
  (testing "get non-existent document returns nil"
    (is (nil? (sut/get-doc *node* :profile/999999999-(-1001987654321)))))

  (testing "get non-existent document with arbitrary ID returns nil"
    (is (nil? (sut/get-doc *node* :profile/000000000-(-1009999999999))))))

(deftest get-warning-by-uuid-test
  (testing "get warning document by UUID"
    (sut/put-doc *node* {:xt/id       :warn/a1b2c3d4-e5f6-7890-abcd-ef1234567890
                         :warn/reason "Spam links in chat"})
    (let [doc (sut/get-doc *node* :warn/a1b2c3d4-e5f6-7890-abcd-ef1234567890)]
      (is (= "Spam links in chat" (:warn/reason doc))))))

;; ============================================================
;; delete-doc
;; ============================================================

(deftest delete-existing-document-test
  (testing "delete existing document"
    (sut/put-doc *node* {:xt/id :profile/294817365-(-1001987654321) :profile/salary "150k"})
    (let [result (sut/delete-doc *node* :profile/294817365-(-1001987654321))]
      (is (:tx-committed? result))
      (is (nil? (sut/get-doc *node* :profile/294817365-(-1001987654321)))))))

(deftest delete-nonexistent-document-test
  (testing "delete non-existent document is a no-op with tx receipt"
    (let [result (sut/delete-doc *node* :profile/000000000-(-1001987654321))]
      (is (:tx-committed? result))))

  (testing "delete non-existent document with arbitrary ID returns tx receipt"
    (let [result (sut/delete-doc *node* :profile/nonexistent-(-1001987654321))]
      (is (:tx-committed? result)))))

;; ============================================================
;; query
;; ============================================================

(deftest query-all-profiles-in-chat-test
  (testing "query all profiles in a chat"
    (doseq [doc [{:xt/id              :profile/294817365-(-1001987654321)
                  :profile/chat-id    -1001987654321
                  :profile/username   "veschin"}
                 {:xt/id              :profile/518293746-(-1001987654321)
                  :profile/chat-id    -1001987654321
                  :profile/username   "anna_dev"}
                 {:xt/id              :profile/738192045-(-1001987654321)
                  :profile/chat-id    -1001987654321
                  :profile/username   "max_clj"}]]
      (sut/put-doc *node* doc))
    (let [results (sut/query *node*
                    '{:find  [?e ?username]
                      :where [[?e :profile/chat-id -1001987654321]
                              [?e :profile/username ?username]]})]
      (is (= 3 (count results)))
      (let [usernames (set (map second results))]
        (is (contains? usernames "veschin"))
        (is (contains? usernames "anna_dev"))
        (is (contains? usernames "max_clj"))))))

(deftest query-top-users-by-message-count-test
  (testing "query top users by message count"
    ;; Insert profiles
    (doseq [doc [{:xt/id            :profile/294817365-(-1001987654321)
                  :profile/user-id  294817365
                  :profile/chat-id  -1001987654321
                  :profile/username "veschin"}
                 {:xt/id            :profile/518293746-(-1001987654321)
                  :profile/user-id  518293746
                  :profile/chat-id  -1001987654321
                  :profile/username "anna_dev"}
                 {:xt/id            :profile/738192045-(-1001987654321)
                  :profile/user-id  738192045
                  :profile/chat-id  -1001987654321
                  :profile/username "max_clj"}]]
      (sut/put-doc *node* doc))
    ;; Insert user stats
    (doseq [doc [{:xt/id                    :user-stats/294817365-(-1001987654321)
                  :user-stats/user-id       294817365
                  :user-stats/chat-id       -1001987654321
                  :user-stats/message-count 4827}
                 {:xt/id                    :user-stats/518293746-(-1001987654321)
                  :user-stats/user-id       518293746
                  :user-stats/chat-id       -1001987654321
                  :user-stats/message-count 1293}
                 {:xt/id                    :user-stats/738192045-(-1001987654321)
                  :user-stats/user-id       738192045
                  :user-stats/chat-id       -1001987654321
                  :user-stats/message-count 312}]]
      (sut/put-doc *node* doc))
    (let [results (sut/query *node*
                    '{:find     [?username ?count]
                      :where    [[?e :user-stats/chat-id  -1001987654321]
                                 [?e :user-stats/user-id  ?uid]
                                 [?p :profile/user-id     ?uid]
                                 [?p :profile/chat-id     -1001987654321]
                                 [?p :profile/username    ?username]
                                 [?e :user-stats/message-count ?count]]
                      :order-by [[?count :desc]]
                      :limit    10})]
      (is (= [["veschin" 4827] ["anna_dev" 1293] ["max_clj" 312]]
             results)))))

(deftest query-warnings-test
  (testing "query warnings for a specific user"
    (doseq [doc [{:xt/id           :warn/a1b2c3d4-e5f6-7890-abcd-ef1234567890
                  :warn/user-id    738192045
                  :warn/chat-id    -1001987654321
                  :warn/reason     "Spam links in chat"
                  :warn/created-at #inst "2026-02-28T16:30:00.000Z"}
                 {:xt/id           :warn/b2c3d4e5-f6a7-8901-bcde-f12345678901
                  :warn/user-id    738192045
                  :warn/chat-id    -1001987654321
                  :warn/reason     "Repeated off-topic messages"
                  :warn/created-at #inst "2026-03-02T11:15:00.000Z"}]]
      (sut/put-doc *node* doc))
    (let [results (sut/query *node*
                    '{:find     [?e ?reason ?ts]
                      :where    [[?e :warn/user-id    738192045]
                                 [?e :warn/chat-id    -1001987654321]
                                 [?e :warn/reason     ?reason]
                                 [?e :warn/created-at ?ts]]
                      :order-by [[?ts :asc]]})]
      (is (= 2 (count results)))
      ;; First warning is older
      (is (= "Spam links in chat" (second (first results))))
      ;; Second warning is newer
      (is (= "Repeated off-topic messages" (second (second results)))))))

(deftest query-no-matches-test
  (testing "query with no matching results returns empty vector"
    (let [results (sut/query *node*
                    '{:find  [?e]
                      :where [[?e :profile/chat-id -1009999999999]]})]
      (is (= [] results)))))

(deftest query-invalid-syntax-test
  (testing "query with invalid Datalog syntax returns query-error"
    (let [result (sut/query *node*
                   {:find '[?e] :where "not-a-clause-vector"})]
      (is (= :query-error (:error result)))
      (is (str/includes? (str (:message result)) "Invalid query syntax")))))

;; ============================================================
;; ID conventions
;; ============================================================

(deftest id-conventions-test
  (testing "documents follow DoxMe ID conventions"
    (are [id]
      (let [result (sut/put-doc *node* {:xt/id id :data "test"})]
        (is (:tx-committed? result))
        (is (some? (sut/get-doc *node* id))))

      ;; ID convention examples
      :profile/294817365-(-1001987654321)
      :user-stats/294817365-(-1001987654321)
      :chat-stats/(-1001987654321)
      :admin/294817365-(-1001987654321)
      :warn/a1b2c3d4-e5f6-7890-abcd-ef1234567890
      :mute/738192045-(-1001987654321)
      :ban/901827364-(-1001987654321))))
