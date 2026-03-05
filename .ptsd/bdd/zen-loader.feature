@feature:zen-loader
Feature: Zen Config Loader
  Load and validate zrc/doxme/bot.edn via zen-lang.
  Provide typed accessors to config, profile fields, commands, and i18n data.

  Background:
    Given environment variables are set
      | TG_BOT_TOKEN | 7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi |
      | TG_API_URL   | https://api.telegram.org                         |
      | DEFAULT_LANG | en                                               |
      | DEBUG        | false                                            |
    And a valid "zrc/doxme/bot.edn" file exists

  # --- Context Lifecycle ---

  Scenario: Create context with valid config and env map
    When I call create-context with paths ["zrc"] and the env map
    Then the result is a zen context with namespace "doxme.bot" loaded
    And there are no validation errors

  Scenario: Create context resolves #env tags from env map
    When I call create-context with paths ["zrc"] and env map passed as {:env env-map}
    Then get-config returns token "7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi"
    And get-config returns api-url "https://api.telegram.org"
    And get-config returns default-language :en
    And get-config returns debug false

  Scenario: Environment defaults applied when optional vars are missing
    Given environment variables are set
      | TG_BOT_TOKEN | 7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi |
    And no other environment variables are set
    When I call create-context with paths ["zrc"] and the env map
    Then get-config returns api-url "https://api.telegram.org"
    And get-config returns default-language :en
    And get-config returns debug false

  Scenario: Missing zrc directory returns config-not-found error
    Given the "zrc/" directory does not exist
    When I call create-context with paths ["zrc"] and the env map
    Then the result is {:error :config-not-found}

  Scenario: Empty bot.edn returns config-parse-error
    Given the file "zrc/doxme/bot.edn" is empty
    When I call create-context with paths ["zrc"] and the env map
    Then the result contains {:error :config-parse-error}
    And the result contains :message "Empty configuration file"

  Scenario: Malformed EDN returns config-parse-error
    Given the file "zrc/doxme/bot.edn" contains "{ns doxme.bot\n config {:token \"abc\""
    When I call create-context with paths ["zrc"] and the env map
    Then the result contains {:error :config-parse-error}
    And the result contains :message matching "Unexpected end of input"

  Scenario: EDN without ns declaration returns config-validation-error
    Given the file "zrc/doxme/bot.edn" contains "{salary {:zen/tags #{profile-field} :type :string :required true :prompt \"test\"}}"
    When I call create-context with paths ["zrc"] and the env map
    Then the result contains {:error :config-validation-error}
    And the :errors vector contains an entry with {:type :missing-ns}

  Scenario: Missing required #env variable without default throws with var name
    Given environment variables are empty
    And the file "zrc/doxme/bot.edn" references "#env TG_BOT_TOKEN"
    When I call create-context with paths ["zrc"] and the env map
    Then the result contains {:error :config-validation-error}
    And the :message contains "TG_BOT_TOKEN"

  Scenario: #env with default uses default when var is missing
    Given environment variable "TG_API_URL" is not set
    And bot.edn contains "#env [TG_API_URL \"https://api.telegram.org\"]"
    When I call create-context with paths ["zrc"] and the env map
    Then get-config returns api-url "https://api.telegram.org"

  Scenario Outline: Schema validation catches invalid field values
    Given the file "zrc/doxme/bot.edn" has <field> set to <value>
    When I call create-context with paths ["zrc"] and the env map
    Then the result contains {:error :config-validation-error}
    And the :errors vector contains an entry with path <path> and type <error_type>

    Examples:
      | field                        | value     | path                 | error_type     |
      | profile-field :type          | :array    | [:type]              | :enum          |
      | bot-config :default-language | :de       | [:default-language]  | :enum          |
      | bot-config :debug            | "yes"     | [:debug]             | :type-mismatch |

  Scenario: Missing required :token in bot-config
    Given the file "zrc/doxme/bot.edn" has bot-config without :token
    When I call create-context with paths ["zrc"] and the env map
    Then the result contains {:error :config-validation-error}
    And the :errors vector contains an entry with path [:token] and message "is required"

  Scenario: Profile field missing required :prompt key
    Given the file "zrc/doxme/bot.edn" has a profile-field without :prompt
    When I call create-context with paths ["zrc"] and the env map
    Then the result contains {:error :config-validation-error}
    And the :errors vector contains an entry with path [:prompt] and message "is required"

  Scenario: Duplicate ns declaration returns parse error
    Given the file "zrc/doxme/bot.edn" contains "{ns doxme.bot\n ns doxme.other}"
    When I call create-context with paths ["zrc"] and the env map
    Then the result contains {:error :config-parse-error}
    And the result contains :message "Duplicate key: ns"

  # --- Accessor: get-config ---

  Scenario: get-config returns full configuration map
    Given a valid zen context is loaded
    When I call get-config with the zen context
    Then the result is a map with keys :token, :api-url, :default-language, :debug
    And :token is "7204518376:AAH3kV9xTcLmJfRqWnB8y5pZvNdUoKsEgMi"
    And :api-url is "https://api.telegram.org"
    And :default-language is :en
    And :debug is false

  # --- Accessor: get-profile-fields ---

  Scenario: get-profile-fields returns all fields in deterministic order
    Given a valid zen context is loaded
    When I call get-profile-fields with the zen context
    Then the result is a vector of 5 maps
    And the fields are sorted: required first alphabetically, then optional alphabetically
    And field 0 is {:name :role :type :string :required true :prompt "Your current role? (e.g., Senior Engineer)" :zen/desc "Current job role"}
    And field 1 is {:name :salary :type :string :required true :prompt "Your salary expectation? (e.g., 100k USD)" :zen/desc "Salary expectation"}
    And field 2 is {:name :stack :type :string :required true :prompt "Your technology stack? (e.g., Clojure, TypeScript)" :zen/desc "Technology stack"}
    And field 3 has :name :bio and :required false and :max-length 500
    And field 4 is {:name :location :type :string :required false :prompt "Your location or timezone? (optional)" :zen/desc "Location or timezone"}

  Scenario: get-profile-fields returns empty vector when no symbols tagged
    Given a zen context loaded from a config with no profile-field tags
    When I call get-profile-fields with the zen context
    Then the result is an empty vector []

  # --- Accessor: get-inline-commands ---

  Scenario: get-inline-commands returns all inline commands
    Given a valid zen context is loaded
    When I call get-inline-commands with the zen context
    Then the result is a vector of 3 maps sorted alphabetically by :command
    And command 0 has :command :chat and :syntax ":chat :<action>" and examples [":chat :users" ":chat :stats"]
    And command 1 has :command :help and :syntax ":help [<command>]" and examples [":help" ":help :user"]
    And command 2 has :command :user and :syntax ":user <username> :get <field>" and examples including ":user veschin :get :salary"

  # --- Accessor: get-bot-commands ---

  Scenario: get-bot-commands returns all bot commands
    Given a valid zen context is loaded
    When I call get-bot-commands with the zen context
    Then the result is a vector of 4 maps sorted alphabetically by :command
    And command 0 has :command "/help" and :handler :help-handler
    And command 1 has :command "/profile" and :handler :profile-handler
    And command 2 has :command "/register" and :handler :register-handler
    And command 3 has :command "/start" and :handler :start-handler

  # --- Accessor: get-i18n ---

  Scenario: get-i18n returns English translations
    Given a valid zen context is loaded
    When I call get-i18n with the zen context and language :en
    Then the result is a flat map with 17 keys
    And :form/title is "Profile Registration"
    And :form/progress is "Step {current} of {total}"
    And :bot/name is "BidloBot"
    And :profile/not-found is "Profile not found. Register with /register"
    And :error/invalid-command is "Invalid command. Use :help for usage."

  Scenario: get-i18n returns Russian translations
    Given a valid zen context is loaded
    When I call get-i18n with the zen context and language :ru
    Then the result is a flat map with 17 keys
    And :form/title is "\u0420\u0435\u0433\u0438\u0441\u0442\u0440\u0430\u0446\u0438\u044f \u043f\u0440\u043e\u0444\u0438\u043b\u044f"
    And :form/progress is "\u0428\u0430\u0433 {current} \u0438\u0437 {total}"
    And :bot/name is "BidloBot"

  Scenario: get-i18n with nonexistent language returns nil
    Given a valid zen context is loaded
    When I call get-i18n with the zen context and language :nonexistent
    Then the result is nil

  # --- Accessor: validate-profile ---

  Scenario: validate-profile with valid data returns valid true
    Given a valid zen context is loaded
    When I call validate-profile with data:
      | salary   | 150k USD                                                    |
      | stack    | Clojure, TypeScript                                         |
      | role     | Senior Engineer                                             |
      | location | UTC+3, Tbilisi                                              |
      | bio      | Functional programming enthusiast. 8 years in backend.      |
    Then the result is {:valid true}

  Scenario: validate-profile with missing required field returns errors
    Given a valid zen context is loaded
    When I call validate-profile with data:
      | stack | Clojure |
    Then the result contains {:valid false}
    And the :errors vector contains an entry with path [:salary] and message "is required"
