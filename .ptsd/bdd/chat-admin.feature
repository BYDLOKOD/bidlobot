@feature:chat-admin
Feature: Chat Management
  Admin tools for community moderation: warnings, mutes, bans.
  Permission system based on Telegram admin detection and bot-managed admin list.

  Background:
    Given a supergroup chat "Clojure Russia" with id -1001234567890
    And the bot is running with XTDB in-memory node
    And the bot has admin rights with "can_restrict_members" permission in chat -1001234567890
    And the following admins are configured:
      | user_id   | username    | role    |
      | 111222333 | alexey_dev  | creator |
      | 222333444 | marina_fp   | admin   |
      | 333444555 | dmi3_arch   | admin   |
    And user 444555666 "pavel_clj" is a regular member (non-admin)
    And user 666777888 "troublemaker42" is a regular member in the chat

  # ──────────────────────────────────────────────
  # Permission system
  # ──────────────────────────────────────────────

  Scenario: Bot detects Telegram chat admins via getChatAdministrators API
    When the bot calls getChatAdministrators for chat -1001234567890
    Then the admin cache contains user 111222333 with status "creator"
    And the admin cache contains user 222333444 with status "administrator"
    And the admin cache contains bot 987654321 "doxme_bot" with status "administrator"

  Scenario: /admin :list shows all admins to any user
    When user 444555666 "pavel_clj" sends "/admin :list" in chat -1001234567890
    Then the bot replies with:
      """
      Chat Admins:

      1. @alexey_dev (creator)
      2. @marina_fp (admin)
      3. @dmi3_arch (admin)
      """

  Scenario: Creator adds a new bot-admin
    When user 111222333 "alexey_dev" sends "/admin :add @pavel_clj" in chat -1001234567890
    Then the bot replies with "@pavel_clj has been added as admin."
    And user 444555666 "pavel_clj" is stored as admin in XTDB with id "admin/444555666--1001234567890"

  Scenario: Non-creator cannot add bot-admins
    When user 222333444 "marina_fp" sends "/admin :add @pavel_clj" in chat -1001234567890
    Then the bot replies with "Only the chat creator can manage admins."

  Scenario: Creator removes a bot-admin
    When user 111222333 "alexey_dev" sends "/admin :remove @marina_fp" in chat -1001234567890
    Then the bot replies with "@marina_fp has been removed from admins."
    And user 222333444 "marina_fp" is no longer stored as admin in XTDB

  Scenario: Creator cannot remove themselves from admins
    When user 111222333 "alexey_dev" sends "/admin :remove @alexey_dev" in chat -1001234567890
    Then the bot replies with "Cannot remove the chat creator from admins."

  Scenario: Non-admin user is rejected from admin commands
    When user 444555666 "pavel_clj" sends "/warn @troublemaker42 \"Being rude\"" in chat -1001234567890
    Then the bot replies with "You don't have permission to use this command."

  # ──────────────────────────────────────────────
  # Warning system
  # ──────────────────────────────────────────────

  Scenario: First warning — admin warns a user with reason
    Given user 666777888 "troublemaker42" has 0 warnings in chat -1001234567890
    When user 111222333 "alexey_dev" sends "/warn @troublemaker42 \"Spam links in chat\"" in chat -1001234567890
    Then a warning document is stored in XTDB with:
      | field              | value               |
      | warn/user-id       | 666777888           |
      | warn/chat-id       | -1001234567890      |
      | warn/reason        | Spam links in chat  |
      | warn/issued-by     | 111222333           |
      | warn/warning-number| 1                   |
    And the bot sends a public notification "@troublemaker42 warned: Spam links in chat (warning 1/3)"

  Scenario: Second warning from a different admin
    Given user 666777888 "troublemaker42" has 1 warning in chat -1001234567890
    When user 222333444 "marina_fp" sends "/warn @troublemaker42 \"Off-topic political debate\"" in chat -1001234567890
    Then the bot sends a public notification "@troublemaker42 warned: Off-topic political debate (warning 2/3)"

  Scenario: Third warning triggers auto-mute for 24 hours
    Given user 666777888 "troublemaker42" has 2 warnings in chat -1001234567890
    When user 111222333 "alexey_dev" sends "/warn @troublemaker42 \"Personal insults toward another member\"" in chat -1001234567890
    Then the bot sends a public notification "@troublemaker42 warned: Personal insults toward another member (warning 3/3). Auto-muted for 24 hours."
    And user 666777888 is muted in chat -1001234567890 for 86400 seconds via restrictChatMember API

  Scenario: /warns shows warning history for a user
    Given user 666777888 "troublemaker42" has the following warnings in chat -1001234567890:
      | reason                                  | issued_by_username | date                         |
      | Spam links in chat                      | alexey_dev         | 2026-02-15T10:00:00.000Z     |
      | Off-topic political debate              | marina_fp          | 2026-02-20T16:30:00.000Z     |
      | Personal insults toward another member  | alexey_dev         | 2026-03-01T09:15:00.000Z     |
    When user 111222333 "alexey_dev" sends "/warns @troublemaker42" in chat -1001234567890
    Then the bot replies with:
      """
      Warnings for @troublemaker42 (3/3):

      1. Spam links in chat — by @alexey_dev (Feb 15, 2026)
      2. Off-topic political debate — by @marina_fp (Feb 20, 2026)
      3. Personal insults toward another member — by @alexey_dev (Mar 1, 2026)
      """

  Scenario: Admin clears all warnings for a user
    Given user 666777888 "troublemaker42" has 3 warnings in chat -1001234567890
    When user 111222333 "alexey_dev" sends "/warn :clear @troublemaker42" in chat -1001234567890
    Then the bot replies with "All warnings cleared for @troublemaker42."
    And all warning documents for user 666777888 in chat -1001234567890 are removed from XTDB

  Scenario: Admin warns without providing a reason
    Given user 666777888 "troublemaker42" has 0 warnings in chat -1001234567890
    When user 111222333 "alexey_dev" sends "/warn @troublemaker42" in chat -1001234567890
    Then the bot sends a public notification "@troublemaker42 warned: No reason given (warning 1/3)"

  # ──────────────────────────────────────────────
  # Mute system
  # ──────────────────────────────────────────────

  Scenario Outline: Admin mutes a user with various durations
    When user 111222333 "alexey_dev" sends "/mute @troublemaker42 <duration>" in chat -1001234567890
    Then the bot replies with "@troublemaker42 muted for <display_duration>."
    And restrictChatMember is called with can_send_messages false for user 666777888 in chat -1001234567890
    And a mute document is stored in XTDB with duration <seconds> seconds

    Examples:
      | duration | display_duration | seconds |
      | 30m      | 30 minutes       | 1800    |
      | 1h       | 1 hour           | 3600    |
      | 2h       | 2 hours          | 7200    |
      | 1d       | 1 day            | 86400   |
      | 7d       | 7 days           | 604800  |

  Scenario: Admin mutes a user indefinitely
    When user 111222333 "alexey_dev" sends "/mute @troublemaker42" in chat -1001234567890
    Then the bot replies with "@troublemaker42 muted indefinitely."
    And a mute document is stored in XTDB with no expiry

  Scenario: Admin unmutes a user
    Given user 666777888 "troublemaker42" is muted in chat -1001234567890
    When user 111222333 "alexey_dev" sends "/unmute @troublemaker42" in chat -1001234567890
    Then the bot replies with "@troublemaker42 has been unmuted."
    And restrictChatMember is called with can_send_messages true for user 666777888 in chat -1001234567890
    And the mute document for user 666777888 is marked inactive in XTDB

  Scenario: Timed mute expires automatically
    Given user 666777888 "troublemaker42" is muted for 3600 seconds starting at "2026-03-01T14:00:00.000Z"
    When the current time reaches "2026-03-01T15:00:00.000Z"
    Then the background mute-expiry job calls restrictChatMember with can_send_messages true
    And the mute document is marked inactive

  Scenario: Non-admin user cannot mute
    When user 444555666 "pavel_clj" sends "/mute @troublemaker42 1h" in chat -1001234567890
    Then the bot replies with "You don't have permission to use this command."

  # ──────────────────────────────────────────────
  # Ban system
  # ──────────────────────────────────────────────

  Scenario: Admin bans a user with reason
    When user 111222333 "alexey_dev" sends "/ban @crypto_scammer \"Scam links\"" in chat -1001234567890
    Then the bot replies with "@crypto_scammer has been banned: Scam links"
    And banChatMember is called for the target user in chat -1001234567890
    And a ban document is stored in XTDB with reason "Cryptocurrency scam links"

  Scenario: Admin unbans a user
    Given user 777000333 "forgiven_one" is banned in chat -1001234567890
    When user 111222333 "alexey_dev" sends "/unban @forgiven_one" in chat -1001234567890
    Then the bot replies with "@forgiven_one has been unbanned."
    And unbanChatMember is called with only_if_banned true for the target user

  Scenario: Non-admin cannot ban
    When user 444555666 "pavel_clj" sends "/ban @someone \"No reason\"" in chat -1001234567890
    Then the bot replies with "You don't have permission to use this command."

  # ──────────────────────────────────────────────
  # Logging
  # ──────────────────────────────────────────────

  Scenario Outline: Admin actions are logged to the admin-log channel
    Given an admin-log channel is configured in zen bot-config
    When an admin action "<action>" is performed by @<admin> on @<target> with reason "<reason>"
    Then a log entry is sent to the admin-log channel matching "<log_message>"

    Examples:
      | action | admin       | target          | reason                     | log_message                                                                     |
      | MUTE   | marina_fp   | troublemaker42  | Repeated off-topic messages | [MUTE] @marina_fp -> @troublemaker42: Repeated off-topic messages               |
      | BAN    | alexey_dev  | crypto_scammer  | Cryptocurrency scam links   | [BAN] @alexey_dev -> @crypto_scammer: Cryptocurrency scam links                 |
      | UNBAN  | alexey_dev  | forgiven_one    |                             | [UNBAN] @alexey_dev -> @forgiven_one                                            |
      | UNMUTE | alexey_dev  | silent_treatment|                             | [UNMUTE] @alexey_dev -> @silent_treatment                                       |

  # ──────────────────────────────────────────────
  # Edge cases
  # ──────────────────────────────────────────────

  Scenario: Admin tries to warn themselves
    When user 111222333 "alexey_dev" sends "/warn @alexey_dev \"Testing\"" in chat -1001234567890
    Then the bot replies with "Cannot warn yourself."

  Scenario: Admin warns a user who is not in the chat
    When user 111222333 "alexey_dev" sends "/warn @ghost_user \"Some reason\"" in chat -1001234567890
    Then the bot replies with "User @ghost_user is not in this chat."

  Scenario: Admin tries to ban another admin
    When user 111222333 "alexey_dev" sends "/ban @marina_fp \"Disagreement\"" in chat -1001234567890
    Then the bot replies with "Cannot ban an admin. Remove admin rights first."

  Scenario: Regular admin tries to ban the creator
    When user 222333444 "marina_fp" sends "/ban @alexey_dev \"Coup attempt\"" in chat -1001234567890
    Then the bot replies with "Cannot ban an admin. Remove admin rights first."

  Scenario: Bot does not have admin rights in the chat
    Given the bot does not have admin rights in chat -1001234567890
    When user 111222333 "alexey_dev" sends "/mute @troublemaker42 1h" in chat -1001234567890
    Then the bot replies with "Bot needs admin rights with 'Restrict Members' permission to perform this action."

  Scenario: Admin commands in a private chat are rejected
    Given a private chat with user 111222333
    When the user sends "/warn @someone \"reason\"" in the private chat
    Then the bot replies with "Admin commands are only available in group chats."

  Scenario: Muting an already-muted user extends the duration
    Given user 666777888 "troublemaker42" is muted for 3600 seconds in chat -1001234567890
    When user 111222333 "alexey_dev" sends "/mute @troublemaker42 2h" in chat -1001234567890
    Then the bot replies with "@troublemaker42 muted for 2 hours."
    And the mute document is updated with duration 7200 seconds

  Scenario: Admin tries to warn the bot itself
    When user 111222333 "alexey_dev" sends "/warn @doxme_bot \"Bad bot\"" in chat -1001234567890
    Then the bot replies with "Cannot warn a bot."

  Scenario Outline: All admin commands are rejected in private chats
    Given a private chat with user 111222333
    When the user sends "<command>" in the private chat
    Then the bot replies with "Admin commands are only available in group chats."

    Examples:
      | command                        |
      | /warn @someone "reason"        |
      | /mute @someone 1h             |
      | /ban @someone "reason"        |
      | /admin :add @someone          |
