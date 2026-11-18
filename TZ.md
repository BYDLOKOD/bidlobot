# BidloBot - software requirements specification

Status: approved implementation target. Section 2 records the pre-cutover product; the linked specifications describe their respective surfaces. Acceptance scenarios are release criteria, not test results or a deployment claim.

## 1. Purpose and scope

Give chat reputation a use without making purchases reduce social standing. Provide three distinct games through one action and settlement boundary. The actors are chat members, sponsors, traders, asset owners, chat administrators and the bot owner. Keep state and permissions isolated by Telegram chat; players use their existing Telegram identities. No cash, Stars, withdrawals, real assets, or cross-chat trading.

## 2. Pre-cutover product inventory

The target changes only the rows marked replace or remove.

| Surface | Pre-cutover behavior | Target |
|---|---|---|
| [Statistics](docs/llm/30_stats.md) | Per-chat message counters, contributor lists, daily activity and monthly nominations through `/stats`. | Retain. |
| [Reputation](docs/llm/59_reputation.md) | `/praise`, `/roast`, `/rep`, `/reptop`; positive/negative reactions, quoted source links; one durable balance per member and chat. | Replace the model, preserve social entry points and quote behavior. |
| [Mini-games](docs/llm/25_games.md) | Dice, battle, code quiz, poll and quiz-poll, 8-ball, number guessing, hangman, duel, IT trivia; legacy slash and inline surfaces. | Remove the full set; Telegram's own polls and rolls are unaffected. |
| [Referrals](docs/llm/58_referral.md) | Chat-scoped link catalogue, member registration, administrator reports. | Retain. |
| [Summaries](docs/llm/45_summarize.md) | Administrator `/summarize` or `/итог`, with a RAM-only chat window, external summarization provider and deferred retry. | Retain. |
| [YouTube](docs/llm/55_youtube_sanitizer.md) | Strips share-tracking parameters and preserves message entities through copy-before-delete. | Retain. |
| [TikTok](docs/llm/56_tiktok_repost.md) | Attributed video and photo slideshow reposts, audio where supported, comment quoting, deferred export where applicable. | Retain. |
| [X](docs/llm/57_xpost.md) | Attributed native media repost with link to the original. | Retain. |
| [Instagram](docs/llm/61_instagram_repost.md) | Attributed supported-video repost; unsupported posts receive a decline. | Retain. |
| [Telegram surfaces](docs/llm/50_telegram.md) | Private `/start`, `/help`, owner `/chats`; group help; inline discovery; membership and update observers. | Retain non-game routes and observers. |
| [Admission and captcha](docs/llm/65_admission.md) | Owner-gated installation; opt-in math captcha and welcome animation. | Retain. |
| [Birthdays](docs/llm/66_birthdays.md) | Opt-in daily greetings using the Telegram profile, without persisting birth dates. | Retain, independent of deployment status. |
| [Deferred work](docs/llm/60_architecture.md) | Caller-scoped `/flush` with a 48-hour lifetime for deferred jobs. | Retain. |
| [Deployment](docs/llm/70_deployment.md) | Go process, embedded bbolt, Telegram long polling, health checks, backup and rollback. | Retain; add a narrowly scoped HTTPS route for the farm client behind the ingress. |

## 3. Reputation and titles (R1)

- **REP-1.** The bot shall maintain a nonnegative spendable wallet and a separate nonnegative title score for each `(chat, user)`. No transfer, refund, purchase, rent, or game result may modify title score. A registered user starts with wallet 10 and score 0, regardless of Telegram administrator status.
- **REP-2.** When migrating a stored legacy balance, the bot shall seed both values from that balance exactly once, without fabricating activity history. Use a version marker committed with the migration and back up before cutover. An absent legacy record takes the new-member defaults. Disable the old writer before migration. Do not boot an old binary against a database after new settlements; roll back to a consistent backup only after explicitly reconciling post-cutover activity.
- **REP-3.** When a valid member praises another current member, the bot shall debit one wallet unit from the sender, credit three to the recipient and add three to the recipient's title score. A roast costs the sender one, removes up to one unreserved wallet unit and subtracts up to one title point. Negative actions cannot consume reservations. Self-targeting, bots, insufficient sender funds and unverifiable memberships fail before settlement. Telegram administrators receive the same deltas.
- **REP-4.** The bot shall allow at most one social settlement per unordered member pair per chat per UTC day and three outgoing settlements per sender per UTC day, shared across slash and reaction inputs. One source reaction per actor and message can settle; removing it does not roll back settlement. Deduplicate source events before applying these limits; retain the existing bounded reaction attribution and safe source quotes. Thus two accounts cannot alternate pluses to generate unlimited wallet balance.
- **REP-5.** `/reptop` shall list ten current scored members sorted by title score descending, stable user identifier ascending on ties, with inert names, titles and a useful unlocked activity. Use observed membership for the listing and live verification for writes. `/rep` shall show own spendable amount, reserved amount, score, title and distance to the next title. Never ping people from either view.

Proposed launch thresholds, cumulative unlocks: `0 Участник` (spin, join incidents, trade, rent and grow), `12 Дежурный` (sponsor an incident), `36 Мастер` (offer owned production slots for rent), `72 Хранитель прода` (request one extra clue per incident, shared across all holders). Never sell rank or give ranked users better slot odds. If a title drops, already accepted trades, leases and sessions retain their terms; new actions use the current title. A new player can reach the first threshold with four valid praises. Calibrate thresholds against an anonymized balance distribution before rollout; never rewrite an existing wallet as part of calibration.

## 4. Game engine (R2)

- **ENG-1.** Each game shall own its domain state, validated actions, deadlines and presentation data. Register Automat, Prod and Farm in process. The common engine shall resolve chat/actor/session/action, validate membership, enforce deadline and version constraints, and apply the module result through the shared wallet. Reputation owns all wallet entries and the title table; no module writes balances directly.
- **ENG-2.** When applying an action, the engine shall atomically persist its immutable request key, business outcome, wallet postings, reservations, module state and any outgoing intent in one bbolt write transaction. Never perform Telegram or HTTP I/O inside the write. Key commands by original message, callbacks by issued intent plus actor and semantics, Mini App writes by authenticated actor plus request ID. Reusing an identifier with different input fails. A true retry returns the prior result. Validate positive quantities, bounded checked integer arithmetic, ownership and availability before writing.
- **ENG-3.** Deadlines use stored UTC instants and a stable task key. On restart, settle overdue jobs once; re-render committed state without running the command again. Keep chat timeline outputs bounded; mark ambiguous non-idempotent sends for reconciliation rather than issuing them twice. An unavailable Telegram API must never charge twice. Reuse the existing rate-limited sender. Revalidate chat membership outside the transaction and bind the decision to that actor and chat.
- **ENG-4.** The engine shall expose chat-scoped read views without walking all chats. Retain paid economic records and deduplication evidence until a bounded compaction policy can preserve ledger totals and retry semantics. No general scripting engine, generic economic plugin service, or new database is required. Extend chat migration to every new keyed record, pending action and deadline; after migration the retired chat key cannot accept actions.

## 5. "Автомат отмазок" (R3)

- **SLOT-1.** `/excuses` shall show collection, equipped phrase, automatic reply status and the 8-reputation spin price. A confirmed spin shall debit 8 once and select one curated catalogue item once on the server. Use a finite no-duplicate pool until the collection is complete; once complete, decline without charging. No rated payout. The result is collectible immediately even if animation delivery fails. Use a reusable silent GIF/MP4 as optional presentation; do not display a Telegram slot result that suggests odds different from the awarded phrase.
- **SLOT-2.** When the player explicitly equips a phrase and enables automatic replies in that chat, the bot shall reply to a new human message tagging them with the selected phrase, attributed as `Отмазка Лены: "..."`, in the source topic and without triggering a mention. Turning it off must prevent subsequent replies, including queued but unsent ones. Ownership, enabled state and membership are checked at delivery time.
- **SLOT-3.** Recognize Telegram `mention` and `text_mention` entities in message text/captions, including UTF-16 offsets, not raw `@` matches. Resolve current handles to chat members; ignore unresolved handles, bot/anonymous messages, commands, edits, self-tags and disabled members. For several tags choose the first eligible target in text order. Post at most once per original message, once per target per 10 minutes and once per chat per 30 seconds. Never speak through the user's account, imitate their identity or mention them again. No background replies outside the group.

## 6. "Прод не спит" (R4)

- **PROD-1.** A qualified sponsor shall be able to reserve 18 reputation and open one incident lobby per chat. The lobby expires in 90 seconds. Require three distinct current participants (the sponsor may join); capture the reservation at launch, otherwise release it. Only the lobby may be cancelled by its sponsor. No participant pays to join or vote. An existing lobby retains its original reservation amount.
- **PROD-2.** Run three turns of 45 seconds. Each scenario has authored clues, valid options and a deterministic outcome matrix based on prior choices. At least one winning sequence must follow from visible clues. Participants may change their vote until the deadline. At close apply the largest count; ties choose the scenario's displayed conservative option when tied, otherwise published option order. No votes consumes the turn without applying a change. A permitted clue reveals information and never purchases a vote. Terminal states: restored, unresolved, cancelled.
- **PROD-3.** Before joining, show that choices appear in a same-chat retrospective. When a player presses a valid gameplay control, record sequence, turn, timestamp, actor, action and accepted effect, including a changed vote. Keep transport retries out of history; give stale/invalid inputs a private notice. The public final card shows cause, outcome, choices that ran and why they affected the service. Its `Ретроспектива` button paginates accepted actions, including votes that did not win, for current members of the same chat for 30 days. Do not store chat text in the game log or attribute blame; neutralize pings. Example: `Ход 2 · Лена: "Откатить". Затем: "Вернуть индекс". Выполнили: "Вернуть индекс" (4:2).` After expiry show `Разбор больше не хранится.`
- **PROD-4.** Ship distinct authored scenarios for search, queue backlog and certificate rotation, each with multiple viable choices, contextual recovery messages and an authored retrospective. Store scenario version in each incident so a deployment cannot change a live round's rules.

## 7. Shared farm (R5)

### Place and progression

One persistent shared farm per chat. Public board shows holdings, active jobs, offers, rentals, and collective milestones. Each joined member has one starter plot, one nontransferable first-crop seed (convertible only via planting), and personal inventory. Players buy extra plots and construct buildings from the same wallet used in the other games. Ownership is personal but the economy and milestones are shared. One milestone, the community bakery, unlocks the second co-op contract tier after ten accepted bread deliveries across the chat. No resetting balances or assets on a calendar tick.

The shared board assigns each player a visible farmyard with fixed positions for plots, mills and ovens. A tile shows owner, asset type, occupancy and remaining time. Selecting a tile opens its available actions or rental offer. Viewers can browse other yards and the shared market without moving or modifying another player's assets. The market also shows recent sale prices by good, excluding expired or cancelled offers; it does not quote them as guaranteed future prices.

| Launch item | Rule |
|---|---|
| Seed | Shop price 4 reputation; one seed yields three grain in 4 hours on one plot. |
| Extra plot | 20 reputation; two additional plots per player; each plot runs one job at a time. |
| Mill | Build for 30 reputation and two grain; one slot; two grain produce one flour in 2 hours. |
| Oven | Build for 40 reputation and one flour; one slot; two flour produce two bread in 2 hours. |
| Asset expansion | Up to two mills and two ovens per player. Construction consumes input and reputation up front; ownership becomes usable after 1 hour. |
| Capacity | Occupied across the entire `[start,end)` interval; no lease or job overlaps on one slot. Jobs may end exactly at lease expiry. |

- **FARM-1.** When the owner or permitted renter starts a job, the farm shall consume the exact inputs and persist a completion deadline. Settle output once to the initiating player's inventory; collect via board without an extra charge. Run multiple stages only through explicit actions. Lease expiry must not destroy already earned output. The board shows open capacity and the next completion.
- **FARM-2.** A member shall list goods for sale with quantity, unit price and 24-hour expiry. Listing reserves stock. Other members can buy some or all units; within one transaction debit buyer, credit seller and deliver goods at the confirmed price. Cancellation and expiry release only remaining stock. The board sorts sell listings by price then creation; show per-unit price, remaining quantity and expiration. Disallow self-fills, accept only positive integers, and cap ten live listings per seller. No instant buy-back from the shop.
- **FARM-3.** A qualified owner shall offer an immediately free mill or oven slot for 6, 12 or 24 hours at a fixed total price. The offer holds the slot for up to one hour; the owner can cancel it while unaccepted. The lease begins at acceptance. Acceptance charges the renter and credits the owner once, binds the slot exclusively to the renter and shows its expiry. An accepted lease cannot be cancelled or repriced by either party. Start only jobs that fit in the remaining time; the owner gets capacity back exactly at expiry. Existing leases continue if an owner leaves; remove unaccepted offers on departure and reject new actions from departed members. Jobs and accepted obligations settle after departure; assets remain in the chat world and cannot move to another chat.
- **FARM-4.** The co-op shall post finite grain and bread contracts, offering another customer when the market is thin. Every day at 00:00 UTC, fund a budget of 12 reputation for the chat, discard unused budget and expire old contracts. Initial contracts pay 1 for three grain, or 4 for two bread; the community bakery unlocks a second bread contract at the same price. Limit to two fills per player per UTC day and at most the budget in total. Prices, remaining budgets and accepted transactions are public. At launch, a seed bought from the shop cannot turn a guaranteed profit through the grain contract. Player trade and rent redistribute reputation; only bounded issuance creates it. No automated shop buyback, compounding passive rent or balance payout for idle ownership.
- **FARM-5.** `/farm` shall show board and entry to play. Core actions shall also work through Telegram buttons with acknowledgement and clear receipts; Mini App is the shared spatial view. The app shall use the same read/write service. Launch from chat with an expiring opaque chat reference. Validate Telegram `initData` signature and age on the server, bind identity to the launch context and require current membership for reads and writes. Never trust an actor, title, amount, price, ownership claim or chat key supplied by the browser. Use authorized polling or refresh for the shared board; do not place Telegram bot credentials in client assets.
- **FARM-6.** A failed purchase or rental shall leave stock, capacity and wallets unchanged. A stale offer shall show current quantities, price or expiry and require reconfirmation. The board explains recovery at zero balance: use the unspent starter seed, sell remaining stock, or earn reputation from peers. A player with no wallet, seed or stock relies on peers; the bot never issues another starter grant. Accept only current-chat members; repeated or parallel purchases cannot overfill the same listing or reserve the same equipment window.

Example: Lena plants on two plots and sells four grain to Misha. Misha rents Ira's mill and produces two flour in two jobs, then rents an oven and bakes two bread. Each receipt shows the quantity, agreed price and balance change. The rental receipt also gives the expiry. Trade and rent settle between players; the co-op delivery uses the published subsidy budget.

## 8. Russian writing and legacy cutover (R6-R9)

- **COPY-1.** The bot shall use curated Russian for changed public and private surfaces. A choice button names the action; a receipt states what changed and by how much; a refusal identifies the cause and a real recovery action. State deadlines in the displayed time zone. Use natural plural forms, consistent terminology and placeholders with an explicit type. Never let joke text replace price, consent or an error reason. No runtime language model for social replies or game copy; the separate summary feature keeps its own provider.
- **COPY-2.** Review actual renders as Russian, including large names, HTML metacharacters, numbers 1/2/5/21, missing entities and declines. Use neutral third-person wording for names without known gender; no mock praise, generic motivational lines, decorative punctuation or repeated boilerplate. Do not assume the source message's topic from the phrase selection. Prefer a specific line when the event has a known type; use context-neutral praise for generic likes. Selection shall not repeat a line within the last five public uses of the same family in the chat where alternatives exist. Keep semantics, amounts and formatting in the deterministic receipt, not the selected line.
- **CUT-1.** Remove `/dice`, `/battle`, `/quiz`, `/poll` (both modes), `/8ball`, `/guess`, `/hangman`, `/duel`, `/trivia`, rankings tied to them, legacy help/menu/inline entries, legacy callback dispatch and reaction voting. Remove their start-up wiring, implementations and obsolete tests. Keep `/praise`, `/roast`, `/rep`, `/reptop`, stats, nominations, independent reaction attribution and relevant existing protections. Old buttons do not start a game. Retain old saved game rows inert; no production database purge or deployment implied.

### Russian phrase catalogue

These are copy assets, not English specification prose. A public praise line is followed by the actual signed change. Lines make no claims about text the bot has not classified.

#### Positive reputation (40)

Use 1-16 for unclassified praise. Offer 17-40 only if the sender explicitly selects the corresponding reason: help (17-24), explanation (25-32), or review (33-40). The reason changes wording only. Never infer the reason from the quoted message.

1. Тут плюс к репе напрашивался.
2. Этот плюс по адресу.
3. За такое и репу не жалко.
4. Вот и повод сказать спасибо.
5. Хорошо, что чат заметил.
6. За это хочется поставить плюс.
7. Эту благодарность передали по адресу.
8. Спасибо перевели в репу.
9. Мелочь в балансе, но по делу.
10. Этот плюс не за красивые логи.
11. Плюс дошёл без повторной отправки.
12. Перевод без комиссии банка.
13. На этот раз хватило короткого «спасибо».
14. Репа пришла. Письмо в бухгалтерию не нужно.
15. У этой благодарности есть квитанция.
16. Этот плюс можно пустить в дело.
17. Подхватить чужую задачу в нужный момент — отдельный навык.
18. Спасибо, что помогли довести дело до конца.
19. С такой помощью можно возвращаться к работе.
20. В нужную минуту нашлась пара свободных рук.
21. Спасибо, что не пришлось разбираться в одиночку.
22. Спасибо, что нашлось на это время.
23. После такой подсказки снова понятно, за что браться.
24. Хорошо, когда на просьбу о помощи отвечают делом.
25. Теперь понятно, где искать причину.
26. Спасибо за ответ, который не пришлось расшифровывать.
27. Эту мысль теперь можно объяснить следующему человеку.
28. Из объяснения понятно, что проверить сначала.
29. Спасибо, что показали ход мысли, а не только результат.
30. Пример сработал лучше длинной инструкции.
31. Теперь понятно, почему первый способ не подошёл.
32. Можно закрыть лишние вкладки: ответ нашёлся здесь.
33. Хорошо, что это заметили до релиза.
34. Спасибо за замечание, по которому понятно, что исправить.
35. Спасибо, что внимательно прошлись по коду.
36. Один вопрос в разборе сэкономил ещё один круг правок.
37. Спасибо, что проверили и случай за пределами примера.
38. Хорошо, что нашлась конкретная строка для правки.
39. После такого разбора проще оставить код следующему человеку.
40. Спасибо за конкретику: понятно, что оставить и что переделать.

#### Excuses - spin prizes (12)

1. У меня локально работает.
2. Я думал, это черновик.
3. Это согласовали на созвоне, записи нет.
4. Я как раз собирался это проверить.
5. В документации был другой понедельник.
6. Я нажал "сохранить". Дальше началась самостоятельная жизнь файла.
7. У меня открыта правильная вкладка. Просто не та.
8. Я не опоздал. Я пришёл к повторному обсуждению.
9. Сначала надо выяснить, кто записался добровольцем. Надеюсь, не я.
10. Я отвечу, как только вспомню вопрос.
11. Последний раз я видел это в рабочем состоянии.
12. Я оставил это до утра. Утро пришло без предупреждения.

#### Incident events - conditional copy (10)

1. Lobby opened: Дежурный открыл инцидент. Нужны ещё участники.
2. Insufficient quorum: Трое не набрались. Репа за запуск снова доступна.
3. Incident started: Смена началась. Сначала посмотрите, что сломалось.
4. Clue revealed: В логах появилась новая строка. Проверьте её перед голосованием.
5. Vote replaced: Голос изменён. В разборе останутся оба выбора.
6. Vote expired: Этот ход уже закрыт. Откройте карточку, чтобы посмотреть итог.
7. Tied vote: Голоса разделились. Сработал порядок, указанный в карточке.
8. Restored after index recovery: Поиск снова видит документы. Помогло восстановление индекса.
9. Unresolved: Ходы закончились, а сервис не восстановился. В разборе видно, что изменилось после каждого решения.
10. Retrospective ready: В разборе есть все принятые действия и итог каждого хода.

#### Farm events - conditional copy (10)

1. Planting: Семена в земле. Время сбора указано на грядке.
2. Harvest: Зерно уже в амбаре. Его можно продать или пустить на муку.
3. Mill complete: Мельница освободилась. Мука у того, кто запустил помол.
4. Oven complete: Хлеб готов. Его можно выставить на продажу.
5. Build ready: Стройка закончилась. Можно запускать первую работу.
6. Listing partly filled: Часть партии купили. Остаток всё ещё на рынке.
7. Listing expired: Объявление снято. Непроданный товар вернулся в амбар.
8. Lease accepted: Место забронировано. Последняя работа должна закончиться до конца аренды.
9. Lease too short: До конца аренды осталось меньше двух часов. Выберите другую мельницу или печь.
10. Bakery milestone: Общая пекарня открылась. Кооператив принимает дополнительную партию хлеба.

## 9. Acceptance matrix

| Requirement | Observable acceptance |
|---|---|
| R1 | Migrate twice from a real-format v1 fixture; balances do not double. Verify member and admin payout equality, duplicate/social limit behavior, purchase without title change, tie order and permissions. |
| R2 | Race two writes for the same action, restart with a pending deadline, and replay a settled request; observe one classed result, one set of ledger entries and deterministic recovery. |
| R3 | Spin, equip, tag by handle and by user entity after emoji; confirm one attributed no-ping reply. Test disable, restart, ambiguous send, stale button, insufficient funds, self-tag, edit and lookalike input. |
| R4 | One sponsor finances a full free round, one lobby expires without a charge; replay and reorder inputs. Open retrospective and compare accepted vote changes and applied result against the recorded turns. |
| R5 | Complete plant -> harvest -> sale -> rented mill -> flour -> rented oven -> bread -> co-op delivery with three players. Race fills/lease accepts, restart mid-job, verify no duplicated stock or money, and reject cross-chat and forged Mini App actions. |
| R6 | Verify every inventory row remains available or is explicitly removed; current-state links resolve and new text remains distinguishable from current behavior. |
| R7-R8 | Review rendered Russian for every new screen and error; count 40 praise entries and 32 event/prize entries; check unique entries, state-specific correctness, safe interpolation and no third-party pings. |
| R9 | In a test chat invoke each removed command and press an old button; none starts a game. `/help`, non-game inline results, stats and social reputation still work. |

## 10. Decision records and references

- **One database transaction, not a service per game.** The existing [bbolt](https://github.com/etcd-io/bbolt#transactions) model provides a local write boundary; launching a second service to settle money would require cross-service reconciliation. Correct settlement outranks animation delivery.
- **Fixed prices, explicit escrow and finite capacity, not an auction or passive yield.** The first release needs inventory available for immediate purchase and predictable job intervals. The auction/order-matching and interest rules are out of scope. The player-to-player price may differ from the co-op contract; only the co-op budget guarantees a buyer. Initial balance values above are proposed, not economy measurements.
- **Chronological and blameless game retrospective.** Use the timeline, impact and root-cause pattern from [Google SRE](https://sre.google/sre-book/postmortem-culture/); retain only accepted game controls, not chat messages.
- **Chat controls plus an authenticated farm client.** [Telegram Mini Apps](https://core.telegram.org/bots/webapps#direct-link-mini-apps) provide chat context but do not read the conversation or replace the server's authorization. Telegram-native controls remain usable without opening the app. A Mini App is not an inline HTML widget.
- **Document structure.** Scope, interfaces, requirements, constraints and verification use an [ISO/IEC/IEEE 29148-oriented SRS](https://www.iso.org/standard/72089.html) in pragmatic EARS form. This is not a claim of full standards conformance.

Source register: R1 reputation and titles; R2 shared engine, separate modules; R3 paid spins and mention replies; R4 sponsor and retrospective; R5 shared farm, trade and rental; R6 English TZ including current features; R7 edited Russian; R8 additional praise and event phrases; R9 deletion of all legacy games. R1-R9 refer to the owner request. Baseline claims refer to the linked existing specs and the runtime entry points [`routes.go`](internal/bot/routes.go), [`inline.go`](internal/bot/inline.go) and [`reputation_repo.go`](internal/storage/reputation_repo.go).

Launch assumptions to verify: the owner has an HTTPS endpoint available for the farm; a test group can receive bot mention updates; an anonymized baseline distribution is available for title calibration. Failure to satisfy the HTTPS assumption prevents Mini App launch, not chat controls; it does not waive Mini App acceptance. Final prices and title thresholds in this document are proposed launch values and need playtesting, not data migrations.
