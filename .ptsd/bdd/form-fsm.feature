@feature:form-fsm
Feature: Form FSM
  Multi-step form state machine for user profile registration.
  Handles step navigation, validation, data collection, session management, and expiry.
  Steps derived from zen profile-field definitions.
  Step order: salary -> stack -> role -> location -> bio -> confirm -> completed.

  Background:
    Given the profile fields in order are salary, stack, role, location, bio
    And salary, stack, role are required fields
    And location, bio are optional fields

  # --- Happy path: full registration ---

  Scenario: Starting registration transitions from idle to first step
    Given a session in state :idle for user 111222 in chat -100200300
    When the user triggers :register
    Then the session state is :step/salary

  Scenario: Providing salary advances to stack step
    Given a session in state :step/salary for user 111222 in chat -100200300
    When the user provides input "150k USD"
    Then the session state is :step/stack
    And the session data contains {:salary "150k USD"}

  Scenario: Providing stack advances to role step
    Given a session in state :step/stack for user 111222 in chat -100200300
    When the user provides input "Clojure, ClojureScript"
    Then the session state is :step/role
    And the session data contains {:stack "Clojure, ClojureScript"}

  Scenario: Providing role advances to location step
    Given a session in state :step/role for user 111222 in chat -100200300
    When the user provides input "Senior Engineer"
    Then the session state is :step/location

  Scenario: Providing location advances to bio step
    Given a session in state :step/location for user 111222 in chat -100200300
    When the user provides input "Berlin, UTC+1"
    Then the session state is :step/bio
    And the session data contains {:location "Berlin, UTC+1"}

  Scenario: Providing bio advances to confirm step
    Given a session in state :step/bio for user 111222 in chat -100200300
    When the user provides input "Functional programming enthusiast. 10 years in Clojure."
    Then the session state is :confirm

  Scenario: Confirming at confirm step completes the form
    Given a session in state :confirm for user 777888 in chat -100200300
    And the session data is {:salary "200k USD" :stack "Rust, Go" :role "Staff Engineer" :location "San Francisco, UTC-8" :bio "Systems programmer. Formerly at Google. Open source maintainer."}
    When the user triggers :confirm
    Then the session state is :completed
    And the completed data map is returned

  # --- Back navigation ---

  Scenario: Back on first step stays on first step
    Given a session in state :step/salary for user 111222 in chat -100200300
    When the user triggers :back
    Then the session state is :step/salary

  Scenario: Back on stack returns to salary
    Given a session in state :step/stack for user 111222 in chat -100200300
    When the user triggers :back
    Then the session state is :step/salary

  Scenario: Back on role returns to stack
    Given a session in state :step/role for user 111222 in chat -100200300
    When the user triggers :back
    Then the session state is :step/stack

  Scenario: Back on location returns to role
    Given a session in state :step/location for user 111222 in chat -100200300
    When the user triggers :back
    Then the session state is :step/role

  Scenario: Back on bio returns to location
    Given a session in state :step/bio for user 111222 in chat -100200300
    When the user triggers :back
    Then the session state is :step/location

  Scenario: Back on confirm returns to bio
    Given a session in state :confirm for user 111222 in chat -100200300
    When the user triggers :back
    Then the session state is :step/bio

  # --- Skip on optional fields ---

  Scenario: Skip on optional location advances to bio
    Given a session in state :step/location for user 333444 in chat -100200300
    When the user triggers :skip
    Then the session state is :step/bio
    And the session data does not contain :location

  Scenario: Skip on optional bio advances to confirm
    Given a session in state :step/bio for user 555666 in chat -100500600
    When the user triggers :skip
    Then the session state is :confirm
    And the session data does not contain :bio

  # --- Skip on required fields (error) ---

  Scenario Outline: Skip on required field is rejected with error
    Given a session in state <state> for user 111222 in chat -100200300
    When the user triggers :skip
    Then the session state is <state>
    And the result contains error :field-required

    Examples:
      | state        |
      | :step/salary |
      | :step/stack  |
      | :step/role   |

  # --- Cancel from any state ---

  Scenario Outline: Cancel from any active state returns to idle and clears data
    Given a session in state <state> for user 111222 in chat -100200300
    When the user triggers :cancel
    Then the session state is :idle
    And the session data is empty

    Examples:
      | state          |
      | :step/salary   |
      | :step/stack    |
      | :step/role     |
      | :step/location |
      | :step/bio      |
      | :confirm       |

  # --- Session management ---

  Scenario: Create session initializes in idle state
    When a session is created for user 999000 in chat -100500600 with steps [salary, stack, role, location, bio]
    Then the session state is :idle
    And the session step-idx is 0
    And the session data is empty
    And the session has a created-at timestamp

  Scenario: Get session returns session for known user-chat pair
    Given a session exists for user 111222 in chat -100200300 in state :step/salary
    When get-session is called for user 111222 in chat -100200300
    Then a session is returned with state :step/salary

  Scenario: Get session returns nil for unknown user-chat pair
    When get-session is called for user 999999 in chat -100200300
    Then nil is returned

  Scenario: Session accumulates data across steps
    Given a session in state :step/salary for user 111222 in chat -100200300
    When the user provides input "150k USD"
    And the user provides input "Clojure, ClojureScript"
    And the user provides input "Senior Engineer"
    Then the session data contains {:salary "150k USD" :stack "Clojure, ClojureScript" :role "Senior Engineer"}

  Scenario: Session shape includes required fields
    Given a session in state :step/role for user 111222 in chat -100200300
    Then the session has keys :state, :step-idx, :data, :created-at, :user-id, :chat-id

  # --- UI rendering ---

  Scenario: Render step shows prompt text and progress indicator
    Given a session in state :step/stack at step-idx 1 for user 111222
    When render-step is called with language :en
    Then the result contains text with the step prompt from zen
    And the result contains text with "Step 2 of 5"

  Scenario: Render first step does not show back button
    Given a session in state :step/salary at step-idx 0 for user 111222
    When render-step is called with language :en
    Then the keyboard does not contain a :back button
    And the keyboard contains a :cancel button

  Scenario: Render optional field shows skip button
    Given a session in state :step/location at step-idx 3 for user 111222
    When render-step is called with language :en
    Then the keyboard contains a :skip button
    And the keyboard contains a :back button
    And the keyboard contains a :cancel button

  Scenario: Render required field does not show skip button
    Given a session in state :step/salary at step-idx 0 for user 111222
    When render-step is called with language :en
    Then the keyboard does not contain a :skip button

  Scenario: Render confirm step shows data summary and confirm button
    Given a session in state :confirm with data {:salary "200k USD" :stack "Rust, Go" :role "Staff Engineer" :location "San Francisco, UTC-8" :bio "Systems programmer. Formerly at Google. Open source maintainer."}
    When render-step is called with language :en
    Then the result contains text with "200k USD"
    And the result contains text with "Rust, Go"
    And the result contains text with "Staff Engineer"
    And the keyboard contains a :confirm button
    And the keyboard contains a :back button
    And the keyboard contains a :cancel button

  Scenario: All button labels come from i18n
    Given a session in state :step/stack at step-idx 1 for user 111222
    When render-step is called with language :ru
    Then all button labels are translated to :ru

  # --- Session expiry ---

  Scenario: Session older than 7 days is treated as expired
    Given a session created at "2026-02-20T10:00:00Z" for user 111222 in chat -100200300
    And the current time is "2026-03-01T10:00:00Z"
    When the user provides input "Clojure"
    Then the result contains error :session-expired

  Scenario: Cleanup removes expired sessions and keeps active ones
    Given sessions exist:
      | user-id | chat-id     | created-at               | state        |
      | 111222  | -100200300  | 2026-02-20T10:00:00Z     | :step/salary |
      | 333444  | -100200300  | 2026-03-04T14:30:00Z     | :step/bio    |
      | 555666  | -100500600  | 2026-02-15T08:00:00Z     | :confirm     |
    And the current time is "2026-03-05T00:00:00Z"
    When cleanup-expired is called
    Then only the session for user 333444 in chat -100200300 remains
    And sessions for users 111222 and 555666 are removed

  Scenario: Background cleanup runs via ScheduledExecutorService every 24 hours
    Given the bot has started
    Then a scheduled cleanup task is registered with period 24 hours

  # --- Edge cases ---

  Scenario: Register with existing active session resumes from current step
    Given a session in state :step/role for user 111222 in chat -100200300
    And the session data is {:salary "150k USD" :stack "Clojure, ClojureScript"}
    When the user triggers :register
    Then the session state is :step/role
    And the session data still contains {:salary "150k USD" :stack "Clojure, ClojureScript"}

  Scenario: Callback from stale message is ignored silently
    Given a session in state :step/stack for user 111222 in chat -100200300 tracking message-id 42
    When a callback arrives from message-id 37 with input "Clojure"
    Then the callback is ignored

  Scenario: Bot restart loses all sessions
    Given active sessions exist in memory
    When the bot restarts
    Then all sessions are lost
    And the next user interaction starts a fresh session
