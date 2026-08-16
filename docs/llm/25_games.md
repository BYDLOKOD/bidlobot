---
id: games
kind: spec
touches:
  - internal/bot/games.go
  - internal/bot/games_8ball.go
  - internal/bot/games_battle.go
  - internal/bot/games_dice.go
  - internal/bot/games_duel.go
  - internal/bot/games_guess.go
  - internal/bot/games_hangman.go
  - internal/bot/games_inline.go
  - internal/bot/games_poll.go
  - internal/bot/games_quip.go
  - internal/bot/games_quiz.go
  - internal/bot/games_trivia.go
  - internal/bot/inline.go
  - internal/bot/cooldown.go
  - internal/games/
  - internal/storage/dice_repo.go
  - internal/storage/quiz_repo.go
  - internal/storage/guess_repo.go
  - internal/storage/hangman_repo.go
written: 2026-05-15
updated: 2026-05-15
---

# Mini-games

See also: [PRD.md](PRD.md), [50_telegram.md](50_telegram.md)
(per-user cooldown + bounded "slow down" notice).

Public, read-only chat-engagement commands. All sends go through the
rate-limited `tgclient` wrapper (never the raw `*telego.Bot`); every
command is wrapped by `App.gateMsg(key, every, handler)` for a per-user
cooldown. Wired in `internal/bot/games.go` (`GamesRegistry` +
`registerGameRoutes`); inline suggestions in `internal/bot/games_inline.go`
+ the `inline.go` catalog. Per-chat state (where any) lives in its own
bbolt bucket mirroring `dice_leaderboard`/`quiz_leaderboard`.

## Commands & cooldowns

| Command | Cooldown | State | Notes |
|---|---|---|---|
| `/dice [emoji]` | 5s | leaderboard | 6 dice emoji |
| `/battle X Y` | 30s | in-mem | 60s reaction vote |
| `/quiz` / `/quiz top` | 8s | leaderboard | guess language by snippet |
| `/poll Q \| a \| b \| ...` | 10s | none | native `SendPoll`; `/poll quiz Q \| *correct \| ...` = quiz poll; 2-10 options |
| `/8ball <question>` | 5s | none | curated SFW IT verdicts; injectable rand |
| `/guess` / `/guess N` / `/guess top` | 5s | per-chat round + wins | number 1-100, first correct wins; stale round (>1h) auto-recycled |
| `/hangman` / `/hangman <letter>` | 5s | per-chat round | IT word list; 6 wrong = loss |
| `/duel @user` | 15s | none | immediate two-dice resolution; rejects self/bot; opponent must be a member of THIS chat (membership-checked) |
| `/trivia` / `/trivia top` | 8s | quiz leaderboard (shared) | IT multiple-choice; callback predicate registered BEFORE quiz's broad one |

`/roast` and `/praise` were game commands until 2026-07; since commit
334bc94 they are **reputation transfers** (`/praise`: +3 to target,
`/roast`: -1 to target, both −1 from actor) - see
[59_reputation.md](59_reputation.md). `/rep` `/reptop` are reputation
commands too, registered alongside the games in `registerGameRoutes`.

## Rules

- Group surface only (supergroup). Inline `@bot <cmd> ...` produces a pure
  slash-command suggestion (the slash handler does the work - one code
  path). Empty inline query lists the catalog.
- Over-frequency: the handler is not run; exactly one "не части - /X
  раз в Nс" reply is sent per (user,command) window, then silence - a
  flooder cannot amplify, a normal user still gets feedback. Details in
  [50_telegram.md](50_telegram.md).
- All curated text (8ball/roast/praise/hangman words) is SFW - these run
  in a 200-member chat. (Roast/praise templates still exist in the text
  catalog but are now used by the reputation handler, see above.)
- Randomness is injectable so tests are deterministic.
- `/nominations` (the monthly awards) is NOT here - it is the monthly
  stats board, see [30_stats.md](30_stats.md).
