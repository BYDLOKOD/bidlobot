---
id: devlog-03
kind: log
---

# Devlog #03: load/correctness audit, hot-path fixes, cleanup operating model

Date: 2026-05-15
Commits: aab4276 (privacy docs finalize), be90a00 (load fixes)
Duration: ~1 session

## Context

Operator asked for a full code audit with real tooling (coverage,
dead-code, lint) plus a logical/load audit: imagine a chat with 30+
active users hammering the bot non-stop - how do stats, games, caching
behave; what is not in its place.

## Timeline

- Tooling: `go test -race -cover`, `go tool cover`, `golangci-lint`,
  `golang.org/x/tools/cmd/deadcode`. Baseline ~58% stmt, green.
- Logical/load reading of the hot path. Two opus critic rounds. Found
  and fixed three real defects (commit `be90a00`):
  1. **Rate-limiter bypass on the public path.** Games, `/stats`,
     help, onboarding, mod-redirect sent via the raw `*telego.Bot`,
     bypassing the per-chat 15/min budget + retry - exactly inverted
     vs. where load is. Routed through `tgclient` (`Client.SendDice`
     added; `App.sender` a required `NewApp` param; games via
     `GamesSender`, stats via `MessageSender`). DM console / inline
     answer / v1 callback dispatcher stay raw by design.
  2. **cooldown lazy-init data race** under telego's
     goroutine-per-update. Eager-init in `NewApp`; nil-check removed;
     `-race` test added.
  3. **`go bh.Start()` swallowed its error** -> silent zombie.
     Surfaced via a select so the app shuts down instead of hanging.
  - Critic round 2 caught a missed `membership.go:95` (member branch
    still raw) and that the sender setter was a nil-deref footgun ->
    made it a required ctor param + `Run()` guard + a regression test
    driving the real `my_chat_member` handler through a recording
    sender. Dead `notAnonymous/hasFrom` predicates removed.
- Deployed `be90a00` to <deploy-host>; container healthy, polling.
  Live `getMe` verification revealed `can_read_all=false` (BotFather
  privacy ON).
- Clarified the cleanup operating model (operator pushed back on
  "disable privacy"): privacy does NOT need disabling. Recommended
  model is periodic & import-driven (fresh export -> import ->
  `/cleanup` immediately; bot admin so reactions flow). Disabling
  privacy is only for continuous live message stats and leaks all
  content into the bot. Documented in `35_history_import.md`
  "Operating model" + `70_deployment.md` Prerequisites.

## Outcome

Three hot-path defects fixed, verified (`-race` green, two critic
rounds), deployed. FINDING #3 (per-event membership fsync) deferred as
a bounded latent optimization, not a correctness bug. Privacy-mode is
recorded as an operator choice, not a defect. Honest test-coverage and
methodology-verification gaps logged in `handoff.md`.
