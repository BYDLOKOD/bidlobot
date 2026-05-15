---
id: games
kind: spec
---

# Mini-games

See also: [10_scope.md](10_scope.md), [50_telegram.md](50_telegram.md)
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
| `/roast [@user]` / `/praise [@user]` | 8s each | none | curated SFW templates; target = @arg or caller |
| `/guess` / `/guess N` / `/guess top` | 5s | per-chat round + wins | number 1-100, first correct wins; stale round (>1h) auto-recycled |
| `/hangman` / `/hangman <letter>` | 5s | per-chat round | IT word list; 6 wrong = loss |
| `/duel @user` | 15s | none | immediate two-dice resolution; rejects self/bot |
| `/trivia` / `/trivia top` | 8s | quiz leaderboard (shared) | IT multiple-choice; callback predicate registered BEFORE quiz's broad one |

## Rules

- Group surface only (supergroup). Inline `@bot <cmd> ...` produces a pure
  slash-command suggestion (the slash handler does the work - one code
  path). Empty inline query lists the catalog.
- Over-frequency: the handler is not run; exactly one "не части - /X
  раз в Nс" reply is sent per (user,command) window, then silence - a
  flooder cannot amplify, a normal user still gets feedback. Details in
  [50_telegram.md](50_telegram.md).
- All curated text (8ball/roast/praise/hangman words) is SFW - these run
  in a 200-member chat.
- Randomness is injectable so tests are deterministic.
- `/nominations` (the monthly awards) is NOT here - it is the monthly
  stats board, see [30_stats.md](30_stats.md).
