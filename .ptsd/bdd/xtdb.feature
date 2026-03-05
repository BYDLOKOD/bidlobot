@feature:xtdb
Feature: XTDB Database
  XTDB node lifecycle and document CRUD.
  Provides storage primitives for profiles, stats, and admin data.

  # --- Node Lifecycle ---

  Scenario: Create in-memory XTDB node for dev/test
    When I call create-node with {:storage :memory}
    Then the result is an open XTDB node
    And the node accepts put-doc operations

  Scenario: Create persistent XTDB node with RocksDB
    Given a writable directory "/tmp/doxme-test-xtdb" exists
    When I call create-node with {:storage :rocksdb :path "/tmp/doxme-test-xtdb"}
    Then the result is an open XTDB node
    And the node accepts put-doc operations

  Scenario: Create node with non-writable path returns storage-error
    When I call create-node with {:storage :rocksdb :path "/root/no-access/xtdb"}
    Then the result contains {:error :storage-error}
    And the :message contains "Cannot write to storage path: /root/no-access/xtdb"

  Scenario: Create node with unknown storage type returns storage-error
    When I call create-node with {:storage :dynamodb :path "/data"}
    Then the result contains {:error :storage-error}
    And the :message contains "Unknown storage type: :dynamodb"

  Scenario: Close node gracefully
    Given an open in-memory XTDB node
    When I call close-node on the node
    Then the node is closed
    And pending writes are flushed

  Scenario: Close already-closed node returns node-closed error
    Given an in-memory XTDB node that has already been closed
    When I call close-node on the node
    Then the result contains {:error :node-closed}
    And the :message is "Node is already closed"

  Scenario: Storage config from environment variables
    Given environment variables are set
      | XTDB_STORAGE_TYPE | memory |
    When I call create-node reading config from environment
    Then the result is an open in-memory XTDB node

  # --- Document Operations: put-doc ---

  Background:
    Given an open in-memory XTDB node

  Scenario: Put a new profile document
    When I call put-doc with document:
      | :xt/id           | :profile/294817365-(-1001987654321) |
      | :profile/user-id | 294817365                           |
      | :profile/chat-id | -1001987654321                      |
      | :profile/username| veschin                             |
      | :profile/salary  | 150k USD                            |
    Then the result contains {:tx-committed? true}
    And get-doc for :profile/294817365-(-1001987654321) returns the document

  Scenario: Put user-stats document
    When I call put-doc with document:
      | :xt/id                    | :user-stats/294817365-(-1001987654321) |
      | :user-stats/user-id       | 294817365                              |
      | :user-stats/chat-id       | -1001987654321                         |
      | :user-stats/message-count | 1                                      |
    Then the result contains {:tx-committed? true}

  Scenario: Put document without :xt/id returns missing-id error
    When I call put-doc with document:
      | :profile/user-id | 294817365 |
      | :profile/salary  | 150k USD  |
    Then the result contains {:error :missing-id}
    And the :message is "Document must have :xt/id"

  Scenario: Put document with nil :xt/id returns missing-id error
    When I call put-doc with a document where :xt/id is nil
    Then the result contains {:error :missing-id}
    And the :message is "Document must have :xt/id"

  Scenario: Put overwrites existing document (last write wins)
    Given a document exists with :xt/id :profile/294817365-(-1001987654321) and :profile/salary "150k USD"
    When I call put-doc with document:
      | :xt/id           | :profile/294817365-(-1001987654321) |
      | :profile/salary  | 200k USD                            |
    Then the result contains {:tx-committed? true}
    And get-doc for :profile/294817365-(-1001987654321) returns :profile/salary "200k USD"

  Scenario: Put on a closed node returns node-closed error
    Given an in-memory XTDB node that has already been closed
    When I call put-doc with document:
      | :xt/id          | :profile/test |
      | :profile/salary | 100k          |
    Then the result contains {:error :node-closed}
    And the :message is "Node is already closed"

  Scenario: Concurrent puts to same :xt/id - last write wins
    When I submit two concurrent put-doc operations for :xt/id :profile/294817365-(-1001987654321):
      | first  | :profile/salary | 150k USD |
      | second | :profile/salary | 200k USD |
    Then both transactions commit successfully
    And get-doc for :profile/294817365-(-1001987654321) returns :profile/salary "200k USD"

  Scenario: All writes are synchronous - function returns after tx is indexed
    When I call put-doc with a valid document
    Then the function blocks until the transaction is indexed
    And immediately calling get-doc returns the document

  Scenario: Put document with special characters in username
    When I call put-doc with document:
      | :xt/id            | :profile/294817365-(-1001987654321) |
      | :profile/username | user.name-test_123                  |
    Then the result contains {:tx-committed? true}
    And get-doc returns :profile/username "user.name-test_123"

  Scenario: Put document with large bio field
    When I call put-doc with a document containing a 500-character :profile/bio
    Then the result contains {:tx-committed? true}

  # --- Document Operations: get-doc ---

  Scenario: Get existing profile document
    Given a document exists with :xt/id :profile/294817365-(-1001987654321) and :profile/username "veschin" and :profile/salary "150k USD"
    When I call get-doc with id :profile/294817365-(-1001987654321)
    Then the result is the full document map
    And it contains :profile/username "veschin"
    And it contains :profile/salary "150k USD"

  Scenario: Get non-existent document returns nil
    When I call get-doc with id :profile/999999999-(-1001987654321)
    Then the result is nil

  Scenario: Get non-existent document with arbitrary ID returns nil
    When I call get-doc with id :profile/000000000-(-1009999999999)
    Then the result is nil

  Scenario: Get warning document by UUID
    Given a document exists with :xt/id :warn/a1b2c3d4-e5f6-7890-abcd-ef1234567890 and :warn/reason "Spam links in chat"
    When I call get-doc with id :warn/a1b2c3d4-e5f6-7890-abcd-ef1234567890
    Then the result contains :warn/reason "Spam links in chat"

  # --- Document Operations: delete-doc ---

  Scenario: Delete existing document
    Given a document exists with :xt/id :profile/294817365-(-1001987654321)
    When I call delete-doc with id :profile/294817365-(-1001987654321)
    Then the result contains {:tx-committed? true}
    And get-doc for :profile/294817365-(-1001987654321) returns nil

  Scenario: Delete non-existent document is a no-op with tx receipt
    When I call delete-doc with id :profile/000000000-(-1001987654321)
    Then the result contains {:tx-committed? true}

  Scenario: Delete non-existent document with arbitrary ID returns tx receipt
    When I call delete-doc with id :profile/nonexistent-(-1001987654321)
    Then the result contains {:tx-committed? true}

  # --- Document Operations: query ---

  Scenario: Query all profiles in a chat
    Given the following profile documents exist:
      | :xt/id                                       | :profile/chat-id | :profile/username |
      | :profile/294817365-(-1001987654321)           | -1001987654321   | veschin           |
      | :profile/518293746-(-1001987654321)           | -1001987654321   | anna_dev          |
      | :profile/738192045-(-1001987654321)           | -1001987654321   | max_clj           |
    When I call query with find [?e ?username] where [[?e :profile/chat-id -1001987654321] [?e :profile/username ?username]]
    Then the result contains tuples for "veschin", "anna_dev", and "max_clj"

  Scenario: Query top users by message count
    Given profile and user-stats documents exist for chat -1001987654321:
      | username | message-count |
      | veschin  | 4827          |
      | anna_dev | 1293          |
      | max_clj  | 312           |
    When I query top users by message count in chat -1001987654321 with limit 10 ordered by count desc
    Then the result is [["veschin" 4827] ["anna_dev" 1293] ["max_clj" 312]]

  Scenario: Query warnings for a specific user
    Given warning documents exist for user 738192045 in chat -1001987654321:
      | :xt/id                                             | :warn/reason                  | :warn/created-at                |
      | :warn/a1b2c3d4-e5f6-7890-abcd-ef1234567890        | Spam links in chat            | 2026-02-28T16:30:00.000Z       |
      | :warn/b2c3d4e5-f6a7-8901-bcde-f12345678901        | Repeated off-topic messages   | 2026-03-02T11:15:00.000Z       |
    When I query warnings for user 738192045 in chat -1001987654321 ordered by timestamp asc
    Then the result contains 2 tuples ordered by created-at ascending

  Scenario: Query with no matching results returns empty vector
    When I call query with find [?e] where [[?e :profile/chat-id -1009999999999]]
    Then the result is an empty vector []

  Scenario: Query with invalid Datalog syntax returns query-error
    When I call query with malformed Datalog {:find [?e] :where "not-a-clause-vector"}
    Then the result contains {:error :query-error}
    And the :message contains "Invalid query syntax"

  # --- ID Conventions ---

  Scenario Outline: Documents follow DoxMe ID conventions
    When I put-doc with :xt/id <id>
    Then the document is stored and retrievable by <id>

    Examples:
      | id                                              |
      | :profile/294817365-(-1001987654321)              |
      | :user-stats/294817365-(-1001987654321)           |
      | :chat-stats/(-1001987654321)                     |
      | :admin/294817365-(-1001987654321)                |
      | :warn/a1b2c3d4-e5f6-7890-abcd-ef1234567890      |
      | :mute/738192045-(-1001987654321)                 |
      | :ban/901827364-(-1001987654321)                  |
