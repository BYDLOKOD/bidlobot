@feature:chat-stats
Feature: Chat Statistics
  Track message counts per user per chat. Provide /stats command and inline
  query for activity reports.

  Background:
    Given a supergroup chat "Clojure Russia" with id -1001234567890
    And the following users exist in the chat:
      | user_id    | username    | first_name |
      | 111222333  | alexey_dev  | Alexey     |
      | 222333444  | marina_fp   | Marina     |
      | 333444555  | dmi3_arch   | Dmitry     |
      | 444555666  | pavel_clj   | Pavel      |
      | 555666777  | olga_data   | Olga       |
    And the bot is running with XTDB in-memory node

  # ──────────────────────────────────────────────
  # Stats Collection
  # ──────────────────────────────────────────────

  Scenario: Count a text message from a non-bot user
    When user 111222333 "alexey_dev" sends a text message "Has anyone tried Datomic Cloud?" in chat -1001234567890
    Then the message count for user 111222333 in chat -1001234567890 is incremented by 1
    And the last-seen timestamp for user 111222333 in chat -1001234567890 is updated

  Scenario: Count a photo message
    When user 333444555 "dmi3_arch" sends a photo message with caption "Architecture diagram for the new service" in chat -1001234567890
    Then the message count for user 333444555 in chat -1001234567890 is incremented by 1

  Scenario: Count a sticker message
    When user 111222333 "alexey_dev" sends a sticker message in chat -1001234567890
    Then the message count for user 111222333 in chat -1001234567890 is incremented by 1

  Scenario: Count a video message
    When user 444555666 "pavel_clj" sends a video message with caption "Quick demo of the REPL workflow" in chat -1001234567890
    Then the message count for user 444555666 in chat -1001234567890 is incremented by 1

  Scenario: Count a document message
    When user 555666777 "olga_data" sends a document "benchmark-results.pdf" in chat -1001234567890
    Then the message count for user 555666777 in chat -1001234567890 is incremented by 1

  Scenario Outline: Do not count non-content messages
    When the bot receives an update of type "<update_type>" in chat -1001234567890
    Then the total message count for chat -1001234567890 is not incremented

    Examples:
      | update_type          |
      | edited_message       |
      | new_chat_members     |
      | left_chat_member     |
      | new_chat_title       |
      | pinned_message       |

  Scenario: Do not count bot messages
    When bot 987654321 "doxme_bot" sends a text message "Welcome to Clojure Russia!" in chat -1001234567890
    Then the total message count for chat -1001234567890 is not incremented

  Scenario: Do not count messages from other bots
    When bot 136817688 "GroupHelpBot" sends a text message "Welcome! Please read the rules." in chat -1001234567890
    Then the total message count for chat -1001234567890 is not incremented

  Scenario: First message from a new user creates user-stats entry
    Given user 666777888 "igor_newbie" has no stats in chat -1001234567890
    When user 666777888 "igor_newbie" sends a text message "Hello everyone!" in chat -1001234567890
    Then a user-stats document is created with id "user-stats/666777888--1001234567890"
    And the message count is 1
    And the first-seen timestamp is set to the current time
    And the last-seen timestamp is set to the current time

  Scenario: Stats collection is non-blocking via core.async channel
    When 100 messages arrive in rapid succession in chat -1001234567890
    Then all messages are accepted into the stats channel without blocking the update handler
    And eventually all 100 messages are counted in the stats

  Scenario: Forwarded messages count for the forwarder
    When user 111222333 "alexey_dev" forwards a message originally from user 999888777 in chat -1001234567890
    Then the message count for user 111222333 in chat -1001234567890 is incremented by 1
    And the message count for user 999888777 is not affected

  # ──────────────────────────────────────────────
  # /stats command — Overview
  # ──────────────────────────────────────────────

  Scenario: /stats shows chat overview with formatted numbers
    Given the chat stats for -1001234567890 are:
      | total_messages | 4237 |
      | created_at     | 2025-12-01T10:00:00.000Z |
    And the user stats for chat -1001234567890 are:
      | username    | message_count | first_seen                   | last_seen                    |
      | alexey_dev  | 1542          | 2025-12-01T10:05:00.000Z     | 2026-03-05T08:30:00.000Z     |
      | marina_fp   | 987           | 2025-12-03T14:20:00.000Z     | 2026-03-04T22:15:00.000Z     |
      | dmi3_arch   | 856           | 2025-12-05T09:00:00.000Z     | 2026-03-05T07:45:00.000Z     |
      | pavel_clj   | 614           | 2025-12-10T16:30:00.000Z     | 2026-03-03T19:00:00.000Z     |
      | olga_data   | 238           | 2026-01-15T11:00:00.000Z     | 2026-03-02T14:30:00.000Z     |
    When a user sends "/stats" in chat -1001234567890
    Then the bot replies with:
      """
      Chat Statistics: Clojure Russia

      Total messages: 4,237
      Total users: 5
      Avg messages/user: 847
      Most active: @alexey_dev (1,542 messages)

      Stats since: Dec 1, 2025
      """

  # ──────────────────────────────────────────────
  # /stats :top — Top users
  # ──────────────────────────────────────────────

  Scenario: /stats :top shows top users sorted by message count
    Given the user stats for chat -1001234567890 are populated as above
    When a user sends "/stats :top" in chat -1001234567890
    Then the bot replies with:
      """
      Top Users: Clojure Russia

      1. @alexey_dev — 1,542 messages
      2. @marina_fp — 987 messages
      3. @dmi3_arch — 856 messages
      4. @pavel_clj — 614 messages
      5. @olga_data — 238 messages
      """

  # ──────────────────────────────────────────────
  # /stats :today — Today's activity
  # ──────────────────────────────────────────────

  Scenario: /stats :today shows today's message count using UTC boundaries
    Given the current UTC date is "2026-03-05"
    And 23 messages have been sent today in chat -1001234567890 by 2 users
    When a user sends "/stats :today" in chat -1001234567890
    Then the bot replies with:
      """
      Today's Activity: Clojure Russia

      Messages today: 23
      Active users today: 2
      """

  # ──────────────────────────────────────────────
  # /stats :user — Per-user stats
  # ──────────────────────────────────────────────

  Scenario: /stats :user @username shows individual stats with rank
    Given the user stats for chat -1001234567890 are populated as above
    When a user sends "/stats :user @marina_fp" in chat -1001234567890
    Then the bot replies with:
      """
      Stats for @marina_fp

      Messages: 987
      First seen: Dec 3, 2025
      Last seen: Mar 4, 2026
      Rank: #2 of 5
      """

  Scenario: /stats :user for the least active user shows correct rank
    Given the user stats for chat -1001234567890 are populated as above
    When a user sends "/stats :user @olga_data" in chat -1001234567890
    Then the bot replies with:
      """
      Stats for @olga_data

      Messages: 238
      First seen: Jan 15, 2026
      Last seen: Mar 2, 2026
      Rank: #5 of 5
      """

  Scenario: /stats :user for non-existent user shows not found
    When a user sends "/stats :user @nonexistent" in chat -1001234567890
    Then the bot replies with "User @nonexistent not found in this chat."

  # ──────────────────────────────────────────────
  # Inline stats queries
  # ──────────────────────────────────────────────

  Scenario: Inline query ":chat :stats" returns chat overview as inline article
    Given the chat stats for -1001234567890 show 4237 total messages
    When a user sends inline query ":chat :stats" from chat -1001234567890
    Then the bot responds with an inline article containing the chat overview
    And the result has cache_time 0 and is_personal true

  Scenario: Inline query ":chat :stats :top" returns top users as inline article
    Given the user stats for chat -1001234567890 are populated
    When a user sends inline query ":chat :stats :top" from chat -1001234567890
    Then the bot responds with an inline article containing the top users list

  # ──────────────────────────────────────────────
  # Report formatting
  # ──────────────────────────────────────────────

  Scenario Outline: Numbers are formatted with commas
    Given a message count of <raw_count>
    When the count is formatted for display
    Then it appears as "<formatted>"

    Examples:
      | raw_count | formatted |
      | 1542      | 1,542     |
      | 987       | 987       |
      | 15234     | 15,234    |
      | 4237      | 4,237     |

  Scenario Outline: Dates are formatted as "Mon DD, YYYY"
    Given a timestamp of "<iso_date>"
    When the date is formatted for display
    Then it appears as "<formatted>"

    Examples:
      | iso_date                     | formatted     |
      | 2025-12-01T10:00:00.000Z    | Dec 1, 2025   |
      | 2025-12-03T14:20:00.000Z    | Dec 3, 2025   |
      | 2026-01-15T11:00:00.000Z    | Jan 15, 2026  |
      | 2026-03-04T22:15:00.000Z    | Mar 4, 2026   |

  Scenario: Today's date is shown as "Today" instead of the date
    Given a timestamp from the current UTC day
    When the date is formatted for display
    Then it appears as "Today"

  # ──────────────────────────────────────────────
  # Edge cases
  # ──────────────────────────────────────────────

  Scenario: /stats in a new chat with no messages
    Given a supergroup chat "New Empty Chat" with id -1009876543210
    And no messages have been recorded in chat -1009876543210
    When a user sends "/stats" in chat -1009876543210
    Then the bot replies with "No activity yet in this chat."

  Scenario: /stats :top in a new chat with no messages
    Given a supergroup chat "New Empty Chat" with id -1009876543210
    And no messages have been recorded in chat -1009876543210
    When a user sends "/stats :top" in chat -1009876543210
    Then the bot replies with "No activity yet in this chat."

  Scenario: /stats in a private chat is rejected
    Given a private chat with user 111222333
    When the user sends "/stats" in the private chat
    Then the bot replies with "Stats are only available in group chats."

  Scenario: /stats :top shows all users when chat has fewer than 10
    Given chat -1001234567890 has only 5 users with stats
    When a user sends "/stats :top" in chat -1001234567890
    Then the top list contains exactly 5 entries

  Scenario: Single-user chat shows correct overview
    Given a supergroup chat "Solo Dev Chat" with id -1005550001234
    And the chat stats for -1005550001234 are:
      | total_messages | 42 |
      | created_at     | 2026-02-20T08:00:00.000Z |
    And the only user stats in chat -1005550001234 are:
      | username      | message_count |
      | lonely_coder  | 42            |
    When a user sends "/stats" in chat -1005550001234
    Then the bot replies with:
      """
      Chat Statistics: Solo Dev Chat

      Total messages: 42
      Total users: 1
      Avg messages/user: 42
      Most active: @lonely_coder (42 messages)

      Stats since: Feb 20, 2026
      """

  Scenario: Bot added to existing chat starts stats from zero
    Given a supergroup chat "Old Chat" with id -1007770001234
    And the bot was just added to the chat
    When a user sends "/stats" in chat -1007770001234
    Then the bot replies with "No activity yet in this chat."
    And no backfill of historical messages is performed
