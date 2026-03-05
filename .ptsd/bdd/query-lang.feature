@feature:query-lang
Feature: Query Language Parser
  Parse DoxMe inline query syntax into structured AST.
  Converts strings like ":user veschin :get :salary" into {:cmd :user :args ["veschin" :get :salary]}.
  Pure function, no data dependencies.

  # --- Parsing known commands ---

  Scenario: Parse :user command with :get field
    When the parser receives ":user veschin :get :salary"
    Then the result is {:cmd :user :args ["veschin" :get :salary]}

  Scenario: Parse :user command with :profile subcommand
    When the parser receives ":user veschin :profile"
    Then the result is {:cmd :user :args ["veschin" :profile]}

  Scenario: Parse :user command with no args
    When the parser receives ":user"
    Then the result is {:cmd :user :args []}

  Scenario: Parse :chat command with :stats arg
    When the parser receives ":chat :stats"
    Then the result is {:cmd :chat :args [:stats]}

  Scenario: Parse :chat command with multiple keyword args
    When the parser receives ":chat :stats :top"
    Then the result is {:cmd :chat :args [:stats :top]}

  Scenario: Parse :chat command with no args
    When the parser receives ":chat"
    Then the result is {:cmd :chat :args []}

  Scenario: Parse :help command with no args
    When the parser receives ":help"
    Then the result is {:cmd :help :args []}

  Scenario: Parse :help command with topic arg
    When the parser receives ":help :user"
    Then the result is {:cmd :help :args [:user]}

  Scenario: Parse :help command with :chat topic
    When the parser receives ":help :chat"
    Then the result is {:cmd :help :args [:chat]}

  # --- Parametrized valid :user queries for all profile fields ---

  Scenario Outline: Parse :user :get for each profile field
    When the parser receives ":user veschin :get <field>"
    Then the result is {:cmd :user :args ["veschin" :get <field_kw>]}

    Examples:
      | field    | field_kw  |
      | :salary  | :salary   |
      | :stack   | :stack    |
      | :role    | :role     |
      | :location| :location |
      | :bio     | :bio      |

  # --- Token rules ---

  Scenario: Token starting with colon is parsed as keyword
    When the parser receives ":chat :stats :top :today"
    Then the result is {:cmd :chat :args [:stats :top :today]}

  Scenario: Token without colon is parsed as string word
    When the parser receives ":user veschin :get :salary"
    Then the args contain "veschin" as a string at position 0

  Scenario: Username with underscore is parsed as word
    When the parser receives ":user dev_ops :get :salary"
    Then the result is {:cmd :user :args ["dev_ops" :get :salary]}

  Scenario: Username with hyphen is parsed as word
    When the parser receives ":user maria-fe :get :stack"
    Then the result is {:cmd :user :args ["maria-fe" :get :stack]}

  Scenario: Username with mixed underscores and hyphens
    When the parser receives ":user a_b-c_d :profile"
    Then the result is {:cmd :user :args ["a_b-c_d" :profile]}

  Scenario: Username with leading underscores
    When the parser receives ":user __leading :get :role"
    Then the result is {:cmd :user :args ["__leading" :get :role]}

  Scenario: Single character username
    When the parser receives ":user x :get :salary"
    Then the result is {:cmd :user :args ["x" :get :salary]}

  Scenario: Alphanumeric username
    When the parser receives ":user user123 :get :stack"
    Then the result is {:cmd :user :args ["user123" :get :stack]}

  Scenario: Query must start with colon followed by known command
    When the parser receives ":user alex_dev :get :salary"
    Then the result cmd is :user

  # --- Whitespace handling ---

  Scenario: Multiple spaces between tokens are ignored
    When the parser receives ":user   veschin   :get   :salary"
    Then the result is {:cmd :user :args ["veschin" :get :salary]}

  Scenario: Leading and trailing whitespace is trimmed
    When the parser receives "  :help  "
    Then the result is {:cmd :help :args []}

  Scenario: Tab characters between tokens are treated as whitespace
    When the parser receives ":user\tveschin\t:get\t:salary"
    Then the result is {:cmd :user :args ["veschin" :get :salary]}

  Scenario: Mixed spaces and tabs between tokens
    When the parser receives ":user  \t  veschin  \t  :profile"
    Then the result is {:cmd :user :args ["veschin" :profile]}

  Scenario: Leading spaces before command
    When the parser receives "   :user veschin :profile"
    Then the result is {:cmd :user :args ["veschin" :profile]}

  Scenario: Trailing spaces after command with no args
    When the parser receives ":help   "
    Then the result is {:cmd :help :args []}

  # --- Error handling: empty and whitespace ---

  Scenario: Empty string returns empty-query error
    When the parser receives ""
    Then the result is {:error :empty-query}

  Scenario: Whitespace-only string returns empty-query error
    When the parser receives "   "
    Then the result is {:error :empty-query}

  Scenario: Tab-only string returns empty-query error
    When the parser receives "\t\t"
    Then the result is {:error :empty-query}

  Scenario: Newline-only string returns empty-query error
    When the parser receives "\n"
    Then the result is {:error :empty-query}

  # --- Error handling: missing colon prefix ---

  Scenario Outline: String without leading colon returns invalid-syntax
    When the parser receives "<input>"
    Then the result is {:error :invalid-syntax}

    Examples:
      | input        |
      | no colon     |
      | user veschin |
      | help         |

  # --- Error handling: unknown commands ---

  Scenario Outline: Unknown command returns unknown-command error
    When the parser receives "<input>"
    Then the result contains {:error :unknown-command :command "<command>"}

    Examples:
      | input              | command     |
      | :unknown-cmd foo   | unknown-cmd |
      | :search veschin    | search      |
      | :ban user123       | ban         |
      | :stats             | stats       |

  # --- Error handling: uppercase commands ---

  Scenario Outline: Uppercase command name returns unknown-command error
    When the parser receives "<input>"
    Then the result contains {:error :unknown-command :command "<command>"}

    Examples:
      | input            | command |
      | :USER veschin    | USER    |
      | :Help            | Help    |
      | :CHAT :stats     | CHAT    |
      | :HELP            | HELP    |
      | :User veschin    | User    |

  # --- Error handling: invalid syntax patterns ---

  Scenario: Bare colon returns invalid-syntax
    When the parser receives ":"
    Then the result is {:error :invalid-syntax}

  Scenario: Colon followed by space returns invalid-syntax
    When the parser receives ": "
    Then the result is {:error :invalid-syntax}

  Scenario: Double colon returns invalid-syntax
    When the parser receives "::"
    Then the result is {:error :invalid-syntax}

  Scenario: Double colon in token without space
    When the parser receives ":user::name"
    Then the result is {:error :invalid-syntax}

  Scenario: Double colon in arguments
    When the parser receives ":user veschin ::get :salary"
    Then the result is {:error :invalid-syntax}

  # --- Error handling: query length ---

  Scenario: Query exceeding 500 characters returns query-too-long error
    Given a query string longer than 500 characters starting with ":user "
    When the parser receives the oversized query
    Then the result is {:error :query-too-long}
