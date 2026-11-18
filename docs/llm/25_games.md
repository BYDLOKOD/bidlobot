---
id: games
kind: spec
touches:
  - internal/gameengine/
  - internal/farmweb/
  - internal/bot/game_actions.go
  - internal/bot/game_mentions.go
  - internal/storage/game_state.go
written: 2026-05-15
updated: 2026-11-18
---

# Chat games

[TZ.md](../../TZ.md) defines the rules and release scenarios. [Reputation](59_reputation.md)
owns the wallet and the title score; spending never changes the score.

| Command | Group activity |
|---|---|
| `/excuses` | Confirm a spin for 8 reputation, collect and equip a phrase, and explicitly enable or disable mention replies. |
| `/prod` | Sponsor a three-turn group incident for 18 reputation. Guests join and vote for free. The final card links to the chronological retrospective. |
| `/farm` | Grow crops, build production, sell goods, rent slots and deliver co-op contracts. The shop charges 4 per seed, 20 per plot, 30 for a mill or 40 for an oven, plus building materials where required. Telegram and the Mini App share the same farm prices and state. |
| `/8ball` | Ask the ball a question. It can also respond to an ordinary group question as a separate message in the same topic, with no reply or mention. |

## Common engine

The transport verifies current Telegram membership before a write.
The engine commits the module action, wallet postings, deadline and
request key together in bbolt. Paid Telegram confirmations use the
original card as their request key. Mini App writes use an authenticated
actor and a client request ID. A retry does not spend twice. The scheduler
processes persisted deadlines after restart; the incident worker edits the
shared Telegram card from a durable render intent without replaying a vote
or a wallet action.

## Module boundaries

- `automat`: A spin selects from the owner's unused phrases. The result
  leads with the drawn phrase, and the collection opens separately.
  Selecting a phrase does not enable automatic replies. Replies use only
  explicit Telegram mention entities and the first eligible target. An
  ambiguous send is not retried.
- `prod`: The lobby holds the price until three current participants start.
  A cancelled or expired lobby releases it. Each new round uses the current
  scenario version, with signals and consequences determined by prior
  choices. Running rounds keep their saved rules. The retrospective keeps
  accepted changes of vote and the results for 30 days.
- `farm`: Plants and production run on persisted deadlines. The market
  reserves listed stock; offers reserve free production capacity.
  Accepted leases survive departure. The co-op has a finite daily
  budget. Bare `/farm` opens the visual Mini App; `/farm help` lists
  explicit text commands. See [deployment](70_deployment.md) for the web listener.

## Legacy cutover

`/dice`, `/battle`, `/quiz`, `/poll`, `/guess`, `/hangman`,
`/duel` and `/trivia` are no longer routed, suggested or listed. Their
stored rows remain inert. Telegram's own polls and dice are unaffected.
[59_reputation.md](59_reputation.md) covers the retained social commands.
