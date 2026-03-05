@feature:profiles
Feature: User Profiles
  Profile CRUD, inline query evaluation, and Telegram command handlers.
  Owns profile data and the query evaluator that connects parsed AST to actual data.
  Profiles are per-chat. Each user can have different profiles in different chats.

  Background:
    Given the following profiles exist in XTDB:
      | xt/id                      | user-id | chat-id     | username   | salary    | stack                           | role             | location          | bio                                                                               |
      | :profile/111222-100200300  | 111222  | -100200300  | veschin    | 150k USD  | Clojure, ClojureScript, Datomic | Senior Engineer  | Berlin, UTC+1     | Functional programming enthusiast. 10 years in Clojure. Open source contributor.  |
      | :profile/333444-100200300  | 333444  | -100200300  | alex_dev   | 120k EUR  | TypeScript, React, Node.js      | Frontend Lead    | Amsterdam, UTC+1  |                                                                                   |
      | :profile/555666-100200300  | 555666  | -100200300  | maria_fe   | 130k USD  | Kotlin, Spring Boot, PostgreSQL | Backend Developer|                   | Building microservices at scale. Previously at Spotify.                            |
      | :profile/111222-100500600  | 111222  | -100500600  | veschin    | 160k USD  | Clojure, Terraform, Kubernetes  | Platform Engineer| Berlin, UTC+1     |                                                                                   |
      | :profile/777888-100500600  | 777888  | -100500600  | dmitry_ops | 140k USD  | Go, Docker, AWS, Terraform      | SRE              | Moscow, UTC+3     | Site reliability engineer. Automate everything.                                   |

  # --- Registration: /register command ---

  Scenario: Register in group chat sends deep link to private chat
    Given user 999000 with username "new_user" is in group chat -100200300
    When user sends "/register" in the group chat
    Then the bot sends a deep link button to private chat

  Scenario: Register in private chat starts form-fsm registration
    Given user 999000 with username "new_user" is in private chat
    When user sends "/register"
    Then the registration form starts at state :step/salary

  Scenario: Register with existing active session resumes from current step
    Given user 111222 with username "veschin" has an active session in state :step/role
    When user sends "/register" in private chat
    Then the registration form resumes at state :step/role

  Scenario: Completed form saves profile to XTDB
    Given user 999000 with username "new_user" completed the registration form with data:
      | salary   | stack          | role       | location     | bio                |
      | 100k USD | Java, Spring   | Developer  | London, UTC  | Backend developer. |
    When the form reaches :completed state
    Then a profile document is saved to XTDB with id ":profile/999000-100200300"
    And the profile contains all form data with :profile/ namespace prefix

  Scenario: Profile ID follows the convention :profile/{user-id}-{chat-id}
    Given user 111222 in chat -100200300
    Then the profile ID is ":profile/111222-100200300"

  # --- Profile viewing: /profile command ---

  Scenario: View own profile shows all filled fields
    Given user 111222 with username "veschin" is in chat -100200300
    When user sends "/profile"
    Then the bot shows profile with salary "150k USD"
    And the bot shows profile with stack "Clojure, ClojureScript, Datomic"
    And the bot shows profile with role "Senior Engineer"
    And the bot shows profile with location "Berlin, UTC+1"
    And the bot shows profile with bio "Functional programming enthusiast. 10 years in Clojure. Open source contributor."

  Scenario: View own profile omits empty fields
    Given user 333444 with username "alex_dev" is in chat -100200300
    When user sends "/profile"
    Then the bot shows profile with salary "120k EUR"
    And the bot shows profile with stack "TypeScript, React, Node.js"
    And the bot shows profile with role "Frontend Lead"
    And the bot shows profile with location "Amsterdam, UTC+1"
    And the profile display does not include bio

  Scenario: View another user's profile by username
    Given user 111222 is in chat -100200300
    When user sends "/profile @alex_dev"
    Then the bot shows alex_dev's profile with salary "120k EUR"
    And the bot shows alex_dev's profile with role "Frontend Lead"

  Scenario: View profile of unregistered user shows not-found message
    Given user 111222 is in chat -100200300
    When user sends "/profile @nonexistent"
    Then the bot responds with i18n message :profile/not-found

  Scenario: View own profile when not registered shows error
    Given user 999000 with username "unregistered" is in chat -100200300
    And user 999000 has no profile in chat -100200300
    When user sends "/profile"
    Then the bot responds with i18n message :error/not-registered

  # --- Profile update: /update command ---

  Scenario: Update single field by name
    Given user 111222 with username "veschin" is in chat -100200300
    When user sends "/update :salary 200k USD"
    Then the profile field :salary is updated to "200k USD"
    And the bot confirms with i18n message :profile/updated

  Scenario: Update stack field
    Given user 111222 with username "veschin" is in chat -100200300
    When user sends "/update :stack Clojure, Rust"
    Then the profile field :stack is updated to "Clojure, Rust"
    And the bot confirms with i18n message :profile/updated

  Scenario: Update unknown field returns error
    Given user 111222 is in chat -100200300
    When user sends "/update :nonexistent value"
    Then the bot responds with error :unknown-field

  Scenario: Update without args starts edit form
    Given user 111222 with username "veschin" is in chat -100200300
    When user sends "/update"
    Then the edit form starts via form-fsm

  Scenario: Update by unregistered user returns error
    Given user 999000 with username "unregistered" is in chat -100200300
    And user 999000 has no profile in chat -100200300
    When user sends "/update :salary 200k"
    Then the bot responds with i18n message :error/not-registered

  # --- Inline query evaluation: :user command ---

  Scenario Outline: Evaluate :user :get for each profile field
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["veschin" :get <field>]}
    Then the result is {:result "<value>" :title "veschin's <field_name>"}

    Examples:
      | field     | value                                                                            | field_name |
      | :salary   | 150k USD                                                                         | salary     |
      | :stack    | Clojure, ClojureScript, Datomic                                                  | stack      |
      | :role     | Senior Engineer                                                                  | role       |
      | :location | Berlin, UTC+1                                                                    | location   |
      | :bio      | Functional programming enthusiast. 10 years in Clojure. Open source contributor. | bio        |

  Scenario: Evaluate :user :profile returns full profile text
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["veschin" :profile]}
    Then the result title is "veschin's profile"
    And the result text contains "salary: 150k USD"
    And the result text contains "stack: Clojure, ClojureScript, Datomic"
    And the result text contains "role: Senior Engineer"
    And the result text contains "location: Berlin, UTC+1"

  Scenario: Evaluate :user :get for another user
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["alex_dev" :get :salary]}
    Then the result is {:result "120k EUR" :title "alex_dev's salary"}

  Scenario: Same user different chat returns chat-specific data
    Given inline query is evaluated in chat -100500600
    When the evaluator receives AST {:cmd :user :args ["veschin" :get :salary]}
    Then the result is {:result "160k USD" :title "veschin's salary"}

  Scenario: Evaluate :user :get for nonexistent user returns error
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["nonexistent" :get :salary]}
    Then the result is {:error :user-not-found}

  Scenario: Evaluate :user :profile for nonexistent user returns error
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["ghost_user" :profile]}
    Then the result is {:error :user-not-found}

  Scenario: Evaluate :user :get for nonexistent field returns error
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["veschin" :get :nonexistent]}
    Then the result is {:error :field-not-found}

  Scenario: Evaluate :user :get for unknown field name returns error
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["veschin" :get :email]}
    Then the result is {:error :field-not-found}

  Scenario: Evaluate :user :get for nil optional field returns field-not-found
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["alex_dev" :get :bio]}
    Then the result is {:error :field-not-found}

  Scenario: Evaluate :user :get for nil location returns field-not-found
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["maria_fe" :get :location]}
    Then the result is {:error :field-not-found}

  Scenario: Profile view omits nil fields
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["alex_dev" :profile]}
    Then the result title is "alex_dev's profile"
    And the result text contains "salary: 120k EUR"
    And the result text contains "stack: TypeScript, React, Node.js"
    And the result text contains "role: Frontend Lead"
    And the result text contains "location: Amsterdam, UTC+1"
    And the result text does not contain "bio:"

  # --- Inline query evaluation: :help command ---

  Scenario: Evaluate :help with no args returns general help
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :help :args []}
    Then the result title is "Help"
    And the result text contains "Available commands"

  Scenario: Evaluate :help :user returns user command help
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :help :args [:user]}
    Then the result title is "Help: :user"
    And the result text contains ":user <username> :get <field>"

  Scenario: Evaluate :help :chat returns chat command help
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :help :args [:chat]}
    Then the result title is "Help: :chat"
    And the result text contains ":chat"

  # --- Inline result formatting ---

  Scenario: Inline result is formatted as Telegram article
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["veschin" :get :salary]}
    Then the inline result type is "article"
    And the inline result has an :id field
    And the inline result has :title "veschin's salary"
    And the inline result has :input-message-content with :message-text
    And the inline result has cache-time 0
    And the inline result has is-personal true

  # --- Data model ---

  Scenario: Profile stored with :profile/ namespace prefix on fields
    Given user 111222 has a profile in chat -100200300
    Then the stored document has keys prefixed with :profile/
    And the document contains :profile/user-id, :profile/chat-id, :profile/username
    And the document contains :profile/salary, :profile/stack, :profile/role
    And the document contains :profile/location, :profile/bio
    And the document contains :profile/created-at, :profile/updated-at

  # --- Edge cases: username handling ---

  Scenario: Username with @ prefix is stripped before lookup
    Given user 111222 is in chat -100200300
    When user sends "/profile @veschin"
    Then the lookup is performed for username "veschin"

  Scenario: Inline query with @ prefix strips it
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["@alex_dev" :get :salary]}
    Then the lookup is performed for username "alex_dev"
    And the result is {:result "120k EUR" :title "alex_dev's salary"}

  Scenario: Username lookup is case-insensitive
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["VESCHIN" :get :salary]}
    Then the result is {:result "150k USD" :title "veschin's salary"}

  Scenario: Mixed case username is normalized
    Given user looks up username "Alex_Dev" in chat -100200300
    Then the lookup matches profile with username "alex_dev"

  Scenario: Uppercase in inline query matches case-insensitively
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["MARIA_FE" :get :stack]}
    Then the result is {:result "Kotlin, Spring Boot, PostgreSQL" :title "maria_fe's stack"}

  Scenario: @ prefix combined with uppercase is handled
    Given inline query is evaluated in chat -100200300
    When the evaluator receives AST {:cmd :user :args ["@VESCHIN" :get :salary]}
    Then the lookup is performed for username "veschin"
    And the result is {:result "150k USD" :title "veschin's salary"}

  # --- Edge cases: user with no Telegram username ---

  Scenario: User with no username is looked up by user-id
    Given a profile exists for user-id 888999 in chat -100200300 with no username
    When profile is requested for user-id 888999 in chat -100200300
    Then the profile is found
    And the display uses first_name instead of username

  # --- Edge cases: bio length ---

  Scenario: Bio exceeding 500 characters is rejected
    Given a bio input of 556 characters
    When the bio is validated by zen schema
    Then validation fails with reason "max-length 500 exceeded" for field :bio

  Scenario: Bio at exactly 500 characters is accepted
    Given a bio input of exactly 500 characters
    When the bio is validated by zen schema
    Then validation passes

  # --- Edge cases: cross-chat isolation ---

  Scenario: Same user has different profiles in different chats
    Given user 111222 with username "veschin"
    When profile is fetched from chat -100200300
    Then the salary is "150k USD" and role is "Senior Engineer"
    When profile is fetched from chat -100500600
    Then the salary is "160k USD" and role is "Platform Engineer"

  Scenario: User exists in one chat but not another
    Given user "dmitry_ops" has a profile in chat -100500600
    When profile is looked up for "dmitry_ops" in chat -100200300
    Then the result is {:error :user-not-found}

  # --- Edge cases: other ---

  Scenario: User leaves chat but profile remains
    Given user 111222 has a profile in chat -100200300
    When user 111222 leaves chat -100200300
    Then the profile for user 111222 in chat -100200300 still exists

  Scenario: Cancel during registration delegates to form-fsm
    Given user 999000 is in an active registration session
    When user sends "/cancel"
    Then the form-fsm cancel action is triggered
    And the session returns to :idle state
