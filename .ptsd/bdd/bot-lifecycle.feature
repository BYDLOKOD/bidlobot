@feature:bot-lifecycle
Feature: Bot Lifecycle
  Telegram client creation, long-polling update loop, update routing to handlers.
  Wires everything together with startup and shutdown sequences.

  Background:
    Given environment variables are set
      | TG_BOT_TOKEN      | 7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi |
      | TG_API_URL        | https://api.telegram.org                         |
      | DEFAULT_LANG      | en                                               |
      | DEBUG             | false                                            |
      | XTDB_STORAGE_TYPE | memory                                           |
    And a valid zen context is loaded

  # --- Client Creation ---

  Scenario: create-bot reads config from zen context and creates TG client
    When I call create-bot with the zen context
    Then the result is a map with keys :client and :ztx
    And :client is a valid TG client instance
    And :ztx is the zen context that was passed in

  Scenario: TG client is configured with bot-token from config
    When I call create-bot with the zen context
    Then the TG client is created with bot-token "7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi"
    And the TG client has rate limiter defaults configured

  Scenario: create-bot with invalid token raises invalid-token error
    Given a zen context with config token "INVALID_TOKEN_12345"
    And the TG API responds with status 401 and body {:ok false :error_code 401 :description "Unauthorized"}
    When I call create-bot with the zen context
    Then the result contains {:error :invalid-token}
    And the :message is "Bot token is invalid or revoked"

  # --- Polling ---

  Scenario: start-polling starts long-polling loop in a thread
    Given a valid bot map with :client and :ztx
    And a handler function that records received updates
    When I call start-polling with the bot and handler function
    Then a polling loop is started in a separate thread
    And the function returns immediately

  Scenario: Polling calls getUpdates with correct parameters
    Given a valid bot with active polling
    When the polling loop makes a getUpdates call
    Then the request includes timeout 30
    And the request includes limit 100
    And the request includes allowed_updates ["message" "callback_query" "inline_query"]

  Scenario: Each update in batch is dispatched to handler function
    Given a valid bot with active polling
    And the TG API returns a batch of 3 updates with update_ids 200001, 200002, 200003
    When the polling loop processes the batch
    Then the handler function is called 3 times
    And each update is passed individually to the handler

  Scenario: Offset tracked after each batch
    Given a valid bot with active polling
    And the TG API returns updates with update_ids [200001, 200002, 200003]
    When the polling loop processes the batch
    Then the next getUpdates call uses offset 200004

  Scenario: Empty updates response continues polling
    Given a valid bot with active polling
    And the TG API returns {:ok true :result []}
    When the polling loop processes the response
    Then the offset remains unchanged
    And polling continues normally

  Scenario: Full batch of 100 updates processed correctly
    Given a valid bot with active polling
    And the TG API returns 100 updates with update_ids 400001 through 400100
    When the polling loop processes the batch
    Then all 100 updates are dispatched to the handler
    And the next offset is 400101

  # --- Update Routing ---

  Scenario: Route message command update to command handler
    Given a valid bot map
    And a Telegram message update:
      | update_id  | 200001                        |
      | user_id    | 294817365                     |
      | username   | veschin                       |
      | chat_type  | private                       |
      | text       | /start                        |
    When I call route-update with the bot and the update
    Then the update is dispatched to the command handler
    And the detected update type is :message

  Scenario: Route regular text message to stats collector
    Given a valid bot map
    And a Telegram message update with no command:
      | update_id  | 200003                                       |
      | user_id    | 294817365                                    |
      | username   | veschin                                      |
      | chat_type  | supergroup                                   |
      | text       | Has anyone tried babashka for scripting?      |
    When I call route-update with the bot and the update
    Then the update is dispatched to the stats collector

  Scenario: Route callback_query update to form FSM handler
    Given a valid bot map
    And a Telegram callback query update:
      | update_id | 200005              |
      | user_id   | 518293746           |
      | username  | anna_dev            |
      | data      | form:next           |
    When I call route-update with the bot and the update
    Then the update is dispatched to the form FSM handler
    And the detected update type is :callback_query

  Scenario: Route inline_query update to query language handler
    Given a valid bot map
    And a Telegram inline query update:
      | update_id | 200009                          |
      | user_id   | 294817365                       |
      | username  | veschin                         |
      | query     | :user anna_dev :get :salary     |
    When I call route-update with the bot and the update
    Then the update is dispatched to the query language handler
    And the detected update type is :inline_query

  Scenario: Unknown update type is ignored silently
    Given a valid bot map
    And a Telegram update with only edited_message:
      | update_id  | 300002                        |
      | user_id    | 294817365                     |
      | username   | veschin                       |
      | text       | Fixed typo                    |
    When I call route-update with the bot and the update
    Then no handler is invoked
    And no error is raised

  Scenario: Unexpected channel_post update type is ignored
    Given a valid bot map
    And a Telegram update with channel_post:
      | update_id | 300003                         |
      | chat_id   | -1001111111111                 |
      | text      | Channel announcement           |
    When I call route-update with the bot and the update
    Then no handler is invoked

  Scenario: Malformed update with only update_id is ignored
    Given a valid bot map
    And a Telegram update with only {:update_id 300004}
    When I call route-update with the bot and the update
    Then no handler is invoked
    And no error is raised

  Scenario: Photo message routed to stats collector
    Given a valid bot map
    And a Telegram message update with a photo:
      | update_id  | 200004                        |
      | user_id    | 738192045                     |
      | username   | max_clj                       |
      | chat_type  | supergroup                    |
      | caption    | My REPL setup                 |
    When I call route-update with the bot and the update
    Then the update is dispatched to the stats collector

  # --- Error Handling During Polling ---

  Scenario: Network timeout during poll retries next iteration
    Given a valid bot with active polling
    And the TG API returns a timeout error
    When the polling loop encounters the timeout
    Then a warning is logged
    And polling continues on next iteration

  Scenario: TG API returns 429 rate limit
    Given a valid bot with active polling
    And the TG API returns status 429 with retry_after 30
    When the polling loop encounters the rate limit
    Then the clj-tg-bot-api limiter handles backoff automatically
    And polling resumes after the backoff period

  Scenario: TG API returns 500 server error
    Given a valid bot with active polling
    And the TG API returns status 500 "Internal Server Error"
    When the polling loop encounters the server error
    Then an error is logged
    And polling continues on next iteration

  Scenario: Handler throws exception during update processing
    Given a valid bot with active polling
    And a handler that throws RuntimeException "Simulated handler failure"
    And the TG API returns update:
      | update_id | 300001                        |
      | user_id   | 294817365                     |
      | username  | veschin                       |
      | text      | /crash-trigger                |
    When the handler processes the update and throws
    Then the error is logged
    And the polling loop is NOT terminated
    And subsequent updates continue to be processed

  # --- Shutdown ---

  Scenario: stop-bot sets stop flag and waits for current poll
    Given a valid bot with active polling
    When I call stop-bot on the bot
    Then the stop flag is set
    And the function waits for the current poll to finish
    And no new polls are started after stop-bot is called
    And the function returns

  Scenario: stop-bot blocks until in-flight handlers complete with 5s timeout
    Given a valid bot with active polling
    And a handler is currently processing an update
    When I call stop-bot on the bot
    Then the function blocks until the handler completes or 5 seconds elapse

  Scenario: stop-bot during active poll waits for poll response then stops
    Given a valid bot with an in-flight getUpdates request
    When I call stop-bot on the bot
    Then the bot waits for the getUpdates response
    And then stops without starting a new poll

  # --- Startup Sequence ---

  Scenario: start! creates full system map
    When I call start! with the required environment variables
    Then the result is a system map with keys :ztx, :node, and :bot
    And :ztx is a valid zen context
    And :node is an open XTDB node
    And :bot contains :client and :ztx

  Scenario: start! sequence is load zen context then create XTDB node then create TG client then start polling
    When I call start!
    Then the zen context is loaded first
    And the XTDB node is created second
    And the TG client is created third
    And polling is started last

  Scenario: start! fails at zen context if TG_BOT_TOKEN is missing
    Given environment variable "TG_BOT_TOKEN" is not set
    When I call start!
    Then the result contains {:error :config-validation-error}
    And the :message contains "TG_BOT_TOKEN"
    And no XTDB node or TG client is created

  Scenario: start! fails at XTDB if storage path is not writable
    Given environment variables are set
      | TG_BOT_TOKEN      | 7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi |
      | XTDB_STORAGE_TYPE | rocksdb                                          |
      | XTDB_STORAGE_PATH | /root/no-access                                  |
    When I call start!
    Then the result contains {:error :storage-error}
    And the zen context was created successfully
    And no TG client is created

  # --- Stop Sequence ---

  Scenario: stop! stops polling and closes XTDB node
    Given a running system map with :ztx, :node, and :bot
    When I call stop! with the system map
    Then polling is stopped first
    And the XTDB node is closed second

  Scenario: Double stop! is idempotent
    Given a system that has already been stopped
    When I call stop! with the system map again
    Then no error is raised
    And the call is a no-op
