@feature:i18n
Feature: Internationalization
  Translation system with EN (base) and RU.
  Loads from zen spec, provides interpolation and language detection from Telegram updates.

  Background:
    Given a valid zen context is loaded with i18n translations for :en and :ru
    And the default language in config is :en

  # --- Translation Lookup ---

  Scenario: Lookup English translation for form title
    When I call t with language :en and key :form/title
    Then the result is "Profile Registration"

  Scenario: Lookup Russian translation for form title
    When I call t with language :ru and key :form/title
    Then the result is "\u0420\u0435\u0433\u0438\u0441\u0442\u0440\u0430\u0446\u0438\u044f \u043f\u0440\u043e\u0444\u0438\u043b\u044f"

  Scenario: Lookup English bot name
    When I call t with language :en and key :bot/name
    Then the result is "BidloBot"

  Scenario: Lookup Russian bot description
    When I call t with language :ru and key :bot/desc
    Then the result is "\u0411\u043e\u0442 \u0443\u043f\u0440\u0430\u0432\u043b\u0435\u043d\u0438\u044f \u043f\u0440\u043e\u0444\u0438\u043b\u044f\u043c\u0438 \u0434\u043b\u044f \u043a\u043e\u043c\u0430\u043d\u0434\u043d\u044b\u0445 \u0447\u0430\u0442\u043e\u0432"

  Scenario Outline: Lookup various English translations
    When I call t with language :en and key <key>
    Then the result is "<expected>"

    Examples:
      | key                    | expected                                             |
      | :form/complete         | Registration complete!                                |
      | :form/expired          | Session expired. Please start again with /register    |
      | :profile/not-found     | Profile not found. Register with /register            |
      | :profile/saved         | Profile saved successfully!                           |
      | :profile/updated       | Profile updated!                                      |
      | :error/invalid-command | Invalid command. Use :help for usage.                 |
      | :error/user-not-found  | User not found in this chat.                          |
      | :error/not-registered  | You are not registered. Use /register first.          |

  Scenario Outline: Lookup various Russian translations
    When I call t with language :ru and key <key>
    Then the result is "<expected>"

    Examples:
      | key                | expected                                                      |
      | :form/complete     | \u0420\u0435\u0433\u0438\u0441\u0442\u0440\u0430\u0446\u0438\u044f \u0437\u0430\u0432\u0435\u0440\u0448\u0435\u043d\u0430!                                          |
      | :profile/saved     | \u041f\u0440\u043e\u0444\u0438\u043b\u044c \u0443\u0441\u043f\u0435\u0448\u043d\u043e \u0441\u043e\u0445\u0440\u0430\u043d\u0451\u043d!                                |
      | :profile/updated   | \u041f\u0440\u043e\u0444\u0438\u043b\u044c \u043e\u0431\u043d\u043e\u0432\u043b\u0451\u043d!                                                                         |

  # --- Fallback Chain ---

  Scenario: Nonexistent key returns key as string
    When I call t with language :en and key :nonexistent/key
    Then the result is ":nonexistent/key"

  Scenario: Unsupported language falls back to English
    When I call t with language :de and key :form/title
    Then the result is "Profile Registration"

  Scenario: Unsupported language falls back to English for profile/saved
    When I call t with language :de and key :profile/saved
    Then the result is "Profile saved successfully!"

  Scenario: Full fallback chain - unsupported lang and missing key
    When I call t with language :de and key :totally/missing
    Then the result is ":totally/missing"

  Scenario: Key exists in :ru but not in :en returns :ru value
    Given the i18n translations include {:ru {:ru-only/key "\u0422\u043e\u043b\u044c\u043a\u043e \u0440\u0443\u0441\u0441\u043a\u0438\u0439"}}
    When I call t with language :ru and key :ru-only/key
    Then the result is "\u0422\u043e\u043b\u044c\u043a\u043e \u0440\u0443\u0441\u0441\u043a\u0438\u0439"

  Scenario: Nil language uses default from config
    When I call t with language nil and key :form/title
    Then the result is "Profile Registration"

  Scenario: Nil key returns empty string
    When I call t with language :en and key nil
    Then the result is ""

  Scenario: Empty string as translation value is returned as-is
    Given the i18n translations include {:en {:empty/key ""}}
    When I call t with language :en and key :empty/key
    Then the result is ""

  Scenario: Simple keyword without namespace works
    Given the i18n translations include {:en {:greeting "Hello!"}}
    When I call t with language :en and key :greeting
    Then the result is "Hello!"

  Scenario: Nil zen context returns error
    When I call t with a nil zen context and language :en and key :form/title
    Then the result is {:error :invalid-context}

  # --- Interpolation ---

  Scenario: Interpolate form progress in English
    When I call t with language :en, key :form/progress, and vars {:current 2 :total 5}
    Then the result is "Step 2 of 5"

  Scenario: Interpolate form progress in Russian
    When I call t with language :ru, key :form/progress, and vars {:current 2 :total 5}
    Then the result is "\u0428\u0430\u0433 2 \u0438\u0437 5"

  Scenario Outline: Interpolate progress at various steps
    When I call t with language :en, key :form/progress, and vars {:current <current> :total <total>}
    Then the result is "<expected>"

    Examples:
      | current | total | expected       |
      | 1       | 5     | Step 1 of 5    |
      | 2       | 5     | Step 2 of 5    |
      | 5       | 5     | Step 5 of 5    |
      | 0       | 5     | Step 0 of 5    |

  Scenario: Missing variable in vars map leaves placeholder unchanged
    When I call t with language :en, key :form/progress, and vars {:current 3}
    Then the result is "Step 3 of {total}"

  Scenario: Empty vars map leaves all placeholders unchanged
    When I call t with language :en, key :form/progress, and vars {}
    Then the result is "Step {current} of {total}"

  Scenario: Nil value in vars map replaced with empty string
    When I call t with language :en, key :form/progress, and vars {:current nil :total 5}
    Then the result is "Step  of 5"

  Scenario: Nil vars map leaves all placeholders unchanged
    When I call t with language :en, key :form/progress, and vars nil
    Then the result is "Step {current} of {total}"

  Scenario: Template without placeholders returned as-is even with vars
    When I call t with language :en, key :form/title, and vars {:foo "bar"}
    Then the result is "Profile Registration"

  Scenario: String variable values interpolated correctly
    When I call t with language :en, key :form/progress, and vars {:current "two" :total "five"}
    Then the result is "Step two of five"

  Scenario: Vars passed but template has no placeholders
    When I call t with language :en, key :form/complete, and vars {:foo "bar"}
    Then the result is "Registration complete!"

  Scenario: Same placeholder appears twice in template
    Given the i18n translations include {:en {:test/repeat "{name} and {name}"}}
    When I call t with language :en, key :test/repeat, and vars {:name "Alice"}
    Then the result is "Alice and Alice"

  Scenario: Fallback to English then interpolate
    When I call t with language :de, key :form/progress, and vars {:current 3 :total 5}
    Then the result is "Step 3 of 5"

  # --- Language Detection ---

  Scenario: Detect Russian language from message update
    Given a Telegram update with message.from.language_code "ru"
      | update_id  | 100001                                    |
      | user_id    | 294817365                                 |
      | username   | veschin                                   |
      | chat_id    | -1001987654321                             |
      | text       | /register                                 |
    When I call detect-language with the zen context and the update
    Then the result is :ru

  Scenario: Detect English language from message update
    Given a Telegram update with message.from.language_code "en"
      | update_id  | 100002                                    |
      | user_id    | 518293746                                 |
      | username   | anna_dev                                  |
      | chat_id    | -1001987654321                             |
      | text       | /profile                                  |
    When I call detect-language with the zen context and the update
    Then the result is :en

  Scenario: Detect language from inline_query.from.language_code
    Given a Telegram inline query update with from.language_code "ru"
      | update_id  | 100003                                    |
      | user_id    | 294817365                                 |
      | username   | veschin                                   |
      | query      | :user anna_dev :get :salary               |
    When I call detect-language with the zen context and the update
    Then the result is :ru

  Scenario: Detect language from callback_query.from.language_code
    Given a Telegram callback query update with from.language_code "ru"
      | update_id  | 100006                                    |
      | user_id    | 294817365                                 |
      | username   | veschin                                   |
      | data       | form:next                                 |
    When I call detect-language with the zen context and the update
    Then the result is :ru

  Scenario Outline: Unsupported or missing language_code falls back to default
    Given a Telegram update with message.from.language_code "<code>"
    When I call detect-language with the zen context and the update
    Then the result is :en

    Examples:
      | code  |
      | de    |
      | uk    |

  Scenario: Missing language_code field entirely falls back to default
    Given a Telegram update with message.from that has no language_code field
      | update_id  | 100005                                    |
      | user_id    | 901827364                                 |
      | first_name | NoLang                                    |
    When I call detect-language with the zen context and the update
    Then the result is :en

  Scenario: Nil update returns default language
    When I call detect-language with the zen context and nil update
    Then the result is :en

  Scenario: Empty update map returns default language
    When I call detect-language with the zen context and empty map {}
    Then the result is :en

  Scenario: BCP47-style language code "en-US" detects as :en
    Given a Telegram update with message.from.language_code "en-US"
      | update_id  | 100007                                    |
      | user_id    | 112233445                                 |
      | username   | john_us                                   |
    When I call detect-language with the zen context and the update
    Then the result is :en

  Scenario: Ukrainian language_code falls back to default
    Given a Telegram update with message.from.language_code "uk"
      | update_id  | 100008                                    |
      | user_id    | 556677889                                 |
      | username   | taras_ua                                  |
    When I call detect-language with the zen context and the update
    Then the result is :en
