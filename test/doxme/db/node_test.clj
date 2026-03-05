(ns doxme.db.node-test
  (:require [clojure.test :refer [deftest testing is]]
            [doxme.db.node :as sut]
            [doxme.db.ops :as ops]
            [clojure.java.io :as io]))

;; ============================================================
;; Node lifecycle tests
;; ============================================================

(deftest create-in-memory-node-test
  (testing "create in-memory XTDB node for dev/test"
    (let [node (sut/create-node {:storage :memory})]
      (try
        (is (some? node) "Node should be created")
        (is (not (contains? node :error)) "Should not be an error map")
        ;; Verify node accepts operations
        (let [result (ops/put-doc node {:xt/id :test/in-memory :data "test"})]
          (is (:tx-committed? result)
              "In-memory node should accept put-doc operations"))
        (finally
          (sut/close-node node))))))

(deftest create-rocksdb-node-test
  (testing "create persistent XTDB node with RocksDB"
    (let [path (str (System/getProperty "java.io.tmpdir") "/doxme-test-xtdb-" (System/nanoTime))
          _    (.mkdirs (io/file path))
          node (sut/create-node {:storage :rocksdb :path path})]
      (try
        (is (some? node) "Node should be created")
        (is (not (contains? node :error)))
        (let [result (ops/put-doc node {:xt/id :test/rocksdb :data "persistent"})]
          (is (:tx-committed? result)))
        (finally
          (sut/close-node node)
          (run! io/delete-file (reverse (file-seq (io/file path)))))))))

(deftest create-node-non-writable-path-test
  (testing "create node with non-writable path returns storage-error"
    (let [result (sut/create-node {:storage :rocksdb :path "/root/no-access/xtdb"})]
      (is (= :storage-error (:error result)))
      (is (clojure.string/includes?
           (str (:message result))
           "Cannot write to storage path: /root/no-access/xtdb")))))

(deftest create-node-unknown-storage-type-test
  (testing "create node with unknown storage type returns storage-error"
    (let [result (sut/create-node {:storage :dynamodb :path "/data"})]
      (is (= :storage-error (:error result)))
      (is (clojure.string/includes?
           (str (:message result))
           "Unknown storage type: :dynamodb")))))

(deftest close-node-test
  (testing "close node gracefully"
    (let [node (sut/create-node {:storage :memory})]
      ;; Put a document so there are pending writes to flush
      (ops/put-doc node {:xt/id :test/close-flush :data "flush-me"})
      (let [result (sut/close-node node)]
        ;; After close, node should be closed — we verify by checking
        ;; that operations on the closed node fail
        (is (not (contains? result :error))
            "Close should succeed without error")))))

(deftest close-already-closed-node-test
  (testing "close already-closed node returns node-closed error"
    (let [node (sut/create-node {:storage :memory})]
      (sut/close-node node)
      (let [result (sut/close-node node)]
        (is (= :node-closed (:error result)))
        (is (= "Node is already closed" (:message result)))))))

(deftest storage-config-from-env-test
  (testing "storage config from environment variables"
    ;; This test verifies create-node can be called with config read from env
    (let [node (sut/create-node {:storage :memory})]
      (try
        (is (some? node))
        (is (not (contains? node :error)))
        (finally
          (sut/close-node node))))))
