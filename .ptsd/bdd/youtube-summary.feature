@feature:youtube-summary
Feature: YouTube Summaries
  Summarize YouTube videos posted in chat via GLM API.
  Includes URL parsing, video metadata validation, GLM client, rate limiting,
  and summary formatting.

  Background:
    Given the bot is running with XTDB in-memory node
    And the environment variable "GLM_API_KEY" is set to "glm-test-key-abc123def456ghi789"
    And the environment variable "YOUTUBE_API_KEY" is set to "AIzaSyC_valid_key_here"
    And the GLM client is configured with:
      | base_url | https://open.bigmodel.cn/api/paas/v4 |
      | model    | glm-4-flash                          |
    And a supergroup chat "Clojure Russia" with id -1001234567890

  # ──────────────────────────────────────────────
  # URL parsing — valid URLs
  # ──────────────────────────────────────────────

  Scenario Outline: Parse valid YouTube URLs and extract video ID
    When the bot parses the URL "<url>"
    Then the extracted video ID is "<video_id>"

    Examples:
      | url                                                                                 | video_id     |
      | https://www.youtube.com/watch?v=dQw4w9WgXcQ                                        | dQw4w9WgXcQ  |
      | https://youtube.com/watch?v=LJ4d5CGN6bI                                            | LJ4d5CGN6bI  |
      | https://youtu.be/kJQP7kiw5Fk                                                       | kJQP7kiw5Fk  |
      | https://m.youtube.com/watch?v=9bZkp7q19f0                                          | 9bZkp7q19f0  |
      | https://www.youtube.com/watch?v=hY7m5jjJ9mM&t=120                                  | hY7m5jjJ9mM  |
      | https://www.youtube.com/watch?v=Ks-_Mh1QhMc&list=PLRqwX-V7Uu6ZiZxtDDRCi6uhfTH4FilpH&index=3 | Ks-_Mh1QhMc  |
      | https://youtu.be/rfscVS0vtbw?t=300                                                 | rfscVS0vtbw  |
      | http://www.youtube.com/watch?v=ZZ5LpwO-An4                                         | ZZ5LpwO-An4  |
      | https://www.youtube.com/watch?v=_-x0WkL3bXE                                        | _-x0WkL3bXE |
      | https://www.youtube.com/embed/M7lc1UVf-VE                                          | M7lc1UVf-VE  |

  # ──────────────────────────────────────────────
  # URL parsing — invalid URLs
  # ──────────────────────────────────────────────

  Scenario Outline: Reject invalid URLs with appropriate error
    When the bot parses the URL "<url>"
    Then the bot returns error "<error_code>" with message "<error_message>"

    Examples:
      | url                                                                      | error_code        | error_message                              |
      | https://vimeo.com/123456789                                              | not-youtube-url   | Only YouTube videos are supported.         |
      | https://example.com/watch?v=abc123                                       | not-youtube-url   | Only YouTube videos are supported.         |
      | https://www.youtube.com/                                                 | missing-video-id  | Invalid YouTube URL: no video ID found.    |
      | https://www.youtube.com/@ClojureTV                                       | not-video-url     | Invalid YouTube URL: not a video link.     |
      | https://www.youtube.com/playlist?list=PLRqwX-V7Uu6ZiZxtDDRCi6uhfTH4FilpH | not-video-url     | Invalid YouTube URL: not a video link.     |
      |                                                                          | empty-url         | No URL provided.                           |
      | not-a-url-at-all                                                         | invalid-url       | Invalid URL format.                        |
      | https://www.youtube.com/shorts/dQw4w9WgXcQ                               | not-video-url     | Invalid YouTube URL: not a video link.     |
      | https://www.youtube.com/watch?v=abc                                      | invalid-video-id  | Invalid YouTube URL: malformed video ID.   |

  # ──────────────────────────────────────────────
  # Video metadata validation
  # ──────────────────────────────────────────────

  Scenario: Valid video with captions is accepted
    Given the YouTube Data API returns metadata for video "dQw4w9WgXcQ":
      | title    | Rick Astley - Never Gonna Give You Up (Official Music Video) |
      | duration | PT3M33S                                                      |
      | caption  | true                                                         |
    When the bot fetches metadata for video "dQw4w9WgXcQ"
    Then the video is accepted with title "Rick Astley - Never Gonna Give You Up (Official Music Video)"
    And the parsed duration is 213 seconds

  Scenario: Valid tech talk video is accepted
    Given the YouTube Data API returns metadata for video "LJ4d5CGN6bI":
      | title    | Rich Hickey - Simple Made Easy (Strange Loop 2011) |
      | duration | PT36M53S                                           |
      | caption  | true                                               |
    When the bot fetches metadata for video "LJ4d5CGN6bI"
    Then the video is accepted with title "Rich Hickey - Simple Made Easy (Strange Loop 2011)"
    And the parsed duration is 2213 seconds

  Scenario: Video longer than 1 hour is rejected
    Given the YouTube Data API returns metadata for video "9bZkp7q19f0":
      | title    | Full Day Workshop: Building Systems with Clojure |
      | duration | PT8H15M42S                                       |
    When the bot fetches metadata for video "9bZkp7q19f0"
    Then the bot returns error "video-too-long" with message "Video too long (max 1 hour)."

  Scenario: Video shorter than 1 minute is rejected
    Given the YouTube Data API returns metadata for video "kJQP7kiw5Fk":
      | title    | Quick Tip: REPL Shortcut |
      | duration | PT15S                    |
    When the bot fetches metadata for video "kJQP7kiw5Fk"
    Then the bot returns error "video-too-short" with message "Video too short to summarize."

  Scenario: Live stream is rejected
    Given the YouTube Data API returns metadata for video "hY7m5jjJ9mM":
      | title                 | LIVE: Clojure Conf 2026 - Day 1 |
      | duration              | P0D                              |
      | liveBroadcastContent  | live                             |
    When the bot fetches metadata for video "hY7m5jjJ9mM"
    Then the bot returns error "live-stream" with message "Cannot summarize live streams."

  Scenario: Deleted or private video returns not found
    Given the YouTube Data API returns empty items for video "XXXXXXXXXXX"
    When the bot fetches metadata for video "XXXXXXXXXXX"
    Then the bot returns error "video-not-found" with message "Video not found or unavailable."

  Scenario: Video without captions is rejected
    Given the YouTube Data API returns metadata for video "Ks-_Mh1QhMc":
      | title    | Coding Session: No Commentary |
      | duration | PT25M10S                      |
      | caption  | false                         |
    When the bot fetches transcript for video "Ks-_Mh1QhMc"
    Then the bot returns error "no-subtitles" with message "No subtitles available for this video."

  # ──────────────────────────────────────────────
  # Duration boundary tests
  # ──────────────────────────────────────────────

  Scenario: Video exactly 1 hour is accepted (boundary)
    Given the YouTube Data API returns metadata for video "M7lc1UVf-VE":
      | title    | Exactly One Hour Video |
      | duration | PT1H                   |
      | caption  | true                   |
    When the bot fetches metadata for video "M7lc1UVf-VE"
    Then the video is accepted
    And the parsed duration is 3600 seconds

  Scenario: Video 1 hour and 1 second is rejected (boundary)
    Given the YouTube Data API returns metadata for video "ZZ5LpwO-An4":
      | title    | Slightly Over One Hour |
      | duration | PT1H0M1S               |
    When the bot fetches metadata for video "ZZ5LpwO-An4"
    Then the bot returns error "video-too-long" with message "Video too long (max 1 hour)."

  Scenario: Video exactly 1 minute is accepted (boundary)
    Given the YouTube Data API returns metadata for video "_-x0WkL3bXE":
      | title    | Exactly One Minute Video |
      | duration | PT1M                     |
      | caption  | true                     |
    When the bot fetches metadata for video "_-x0WkL3bXE"
    Then the video is accepted
    And the parsed duration is 60 seconds

  Scenario: Video 59 seconds is rejected as too short (boundary)
    Given the YouTube Data API returns metadata for video "rfscVS0vtbw":
      | title    | Almost One Minute |
      | duration | PT59S             |
    When the bot fetches metadata for video "rfscVS0vtbw"
    Then the bot returns error "video-too-short" with message "Video too short to summarize."

  # ──────────────────────────────────────────────
  # ISO 8601 duration parsing
  # ──────────────────────────────────────────────

  Scenario Outline: Parse ISO 8601 duration to seconds
    When the bot parses ISO duration "<iso_duration>"
    Then the result is <seconds> seconds

    Examples:
      | iso_duration | seconds |
      | PT3M33S      | 213     |
      | PT36M53S     | 2213    |
      | PT8H15M42S   | 29742   |
      | PT15S        | 15      |
      | PT1H         | 3600    |
      | PT59M59S     | 3599    |
      | P0D          | 0       |
      | PT25M10S     | 1510    |

  # ──────────────────────────────────────────────
  # GLM client
  # ──────────────────────────────────────────────

  Scenario: Successful English summary via GLM API
    Given video "LJ4d5CGN6bI" has title "Rich Hickey - Simple Made Easy (Strange Loop 2011)" and duration 2213 seconds
    And the transcript starts with "So what I want to talk about today is sort of two words that we've been using interchangeably"
    And the requester's language is "en"
    When the bot sends the transcript to GLM for summarization
    Then the GLM request contains:
      | model        | glm-4-flash |
      | max_tokens   | 500         |
      | temperature  | 0.7         |
    And the request has Authorization header "Bearer glm-test-key-abc123def456ghi789"
    And the system message instructs to produce a summary with Main Topics, Key Points, and Worth watching if
    And the bot returns a formatted summary containing "Main Topics:" and "Key Points:" and "Worth watching if:"

  Scenario: Summary in Russian when requester language is ru
    Given video "rfscVS0vtbw" has title "Clojure для начинающих — Введение" and duration 1800 seconds
    And the requester's language is "ru"
    When the bot sends the transcript to GLM for summarization
    Then the system message is in Russian
    And the summary format uses Russian headings "Основные темы:" and "Ключевые моменты:" and "Стоит смотреть, если:"

  Scenario: Transcript longer than 10000 chars is truncated
    Given a transcript of 24500 characters for video "LJ4d5CGN6bI"
    When the bot prepares the transcript for GLM
    Then the transcript is truncated to 10000 characters
    And a warning is logged about truncation from 24500 to 10000 characters
    And the truncated content ends with "... [transcript truncated]"

  Scenario: Empty GLM API key returns invalid-api-key error
    Given the environment variable "GLM_API_KEY" is set to ""
    When the bot creates a GLM client
    Then the bot returns error "invalid-api-key" with message "YouTube summary feature is not configured. Invalid GLM API key."

  Scenario: GLM API returns server error
    Given the GLM API returns HTTP 500 with body "Internal server error"
    When the bot sends a summarization request
    Then the bot returns error "glm-api-error" with message "Summary service temporarily unavailable."

  Scenario: GLM API request times out after 30 seconds
    Given the GLM API does not respond within 30000 milliseconds
    When the bot sends a summarization request
    Then the bot returns error "timeout" with message "Summary service timed out. Please try again."

  Scenario: GLM API returns HTTP 429 rate limit
    Given the GLM API returns HTTP 429 with retry-after header "60"
    When the bot sends a summarization request
    Then the bot returns error "rate-limited" with message "Summary service is busy. Please try again in a minute."

  # ──────────────────────────────────────────────
  # Summary trigger (/summarize command)
  # ──────────────────────────────────────────────

  Scenario: /summarize command generates and posts a summary
    Given video "LJ4d5CGN6bI" exists with valid metadata and captions
    And the rate limit for chat -1001234567890 has not been exceeded
    When a user sends "/summarize https://www.youtube.com/watch?v=LJ4d5CGN6bI" in chat -1001234567890
    Then the bot fetches video metadata from YouTube Data API
    And the bot fetches the transcript
    And the bot sends the transcript to GLM for summarization
    And the bot replies with a formatted summary

  Scenario: /summarize works in private chats
    Given a private chat with user 111222333
    And video "dQw4w9WgXcQ" exists with valid metadata and captions
    When the user sends "/summarize https://www.youtube.com/watch?v=dQw4w9WgXcQ" in the private chat
    Then the bot replies with a formatted summary

  # ──────────────────────────────────────────────
  # Summary format
  # ──────────────────────────────────────────────

  Scenario: Summary follows the expected format
    Given a successful summarization of video "LJ4d5CGN6bI"
    Then the summary output matches the format:
      """
      Rich Hickey - Simple Made Easy (Strange Loop 2011)
      36 minutes

      Main Topics:
      - The distinction between "simple" and "easy" in software design
      - How complexity arises from complecting (interleaving) concerns
      - Choosing simple constructs over familiar (easy) ones

      Key Points:
      - Simple means "one fold/braid" — not interleaved with other concerns
      - Easy means "near at hand" — familiar, convenient, but not necessarily simple
      - Complexity is the primary source of bugs and maintenance burden
      - Prefer values over state, composition over inheritance, queues over direct coupling
      - Testing and type systems do not address fundamental complexity

      Worth watching if: You want a paradigm-shifting perspective on why simplicity matters more than convenience in software architecture.
      """

  Scenario: Summary language matches the requester's Telegram language
    Given a user with language_code "ru" requests a summary
    When the summary is generated
    Then the summary is in Russian

  # ──────────────────────────────────────────────
  # Rate limiting
  # ──────────────────────────────────────────────

  Scenario: Rate limit exceeded — 10 summaries per hour per chat
    Given chat -1001234567890 has 10 summary requests in the last hour at:
      | 2026-03-05T08:05:00.000Z |
      | 2026-03-05T08:10:00.000Z |
      | 2026-03-05T08:15:00.000Z |
      | 2026-03-05T08:20:00.000Z |
      | 2026-03-05T08:25:00.000Z |
      | 2026-03-05T08:30:00.000Z |
      | 2026-03-05T08:35:00.000Z |
      | 2026-03-05T08:40:00.000Z |
      | 2026-03-05T08:45:00.000Z |
      | 2026-03-05T08:50:00.000Z |
    When a user sends "/summarize https://www.youtube.com/watch?v=dQw4w9WgXcQ" at "2026-03-05T08:55:00.000Z" in chat -1001234567890
    Then the bot replies with "Summary limit reached. Try again later."

  Scenario: Rate limit window expires — old timestamps are evicted
    Given chat -1001234567890 has summary request timestamps:
      | 2026-03-05T07:00:00.000Z |
      | 2026-03-05T07:05:00.000Z |
      | 2026-03-05T07:10:00.000Z |
      | 2026-03-05T08:30:00.000Z |
      | 2026-03-05T08:35:00.000Z |
    When a user sends "/summarize https://www.youtube.com/watch?v=dQw4w9WgXcQ" at "2026-03-05T08:55:00.000Z" in chat -1001234567890
    Then only 2 timestamps are within the last hour
    And the request is allowed

  Scenario: Rate limit resets on bot restart
    Given the bot is restarted
    Then the rate limit atom is empty for all chats
    And summary requests are allowed immediately

  # ──────────────────────────────────────────────
  # Missing API keys
  # ──────────────────────────────────────────────

  Scenario: GLM_API_KEY not set — feature disabled
    Given the environment variable "GLM_API_KEY" is not set
    When a user sends "/summarize https://www.youtube.com/watch?v=dQw4w9WgXcQ" in chat -1001234567890
    Then the bot replies with "YouTube summary feature is not configured. Missing GLM API key."

  Scenario: YOUTUBE_API_KEY not set — feature disabled
    Given the environment variable "YOUTUBE_API_KEY" is not set
    And the environment variable "GLM_API_KEY" is set to "glm-key-valid"
    When a user sends "/summarize https://www.youtube.com/watch?v=dQw4w9WgXcQ" in chat -1001234567890
    Then the bot replies with "YouTube summary feature is not configured. Missing YouTube API key."

  Scenario: Both API keys missing — GLM error takes priority
    Given the environment variable "GLM_API_KEY" is not set
    And the environment variable "YOUTUBE_API_KEY" is not set
    When a user sends "/summarize https://www.youtube.com/watch?v=dQw4w9WgXcQ" in chat -1001234567890
    Then the bot replies with "YouTube summary feature is not configured. Missing GLM API key."
