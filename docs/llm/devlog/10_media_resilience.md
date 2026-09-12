# Devlog 10 - 2026-09-12: media export and transport resilience

## Trigger

Three failure classes in the production logs (VM100, `docker logs
bidlobot`, 2026-08-26..2026-09-12):

1. The process died once with SIGSEGV inside telego/go-json. telego
   clones every update through a JSON opcode-set cache that is written
   unsynchronized in builds without `-race`, while telegohandler runs
   each update on its own goroutine.
2. TikTok video export failed 36 times (24 DNS, 7 TLS-EOF, 5
   content-level). The download path fetched TikTok web pages, and the
   ISP filters TikTok web by TLS SNI.
3. Every Telegram transport error was dropped unretried: the retry
   policy only classified `*telegoapi.Error`, and callers ran without
   deadlines (`getChatAdministrators` ran 181s on 2026-09-04).

## Change

- **Panic containment.** `App.recoverMiddleware` registered as the
  outermost handler in `Run` (before `healthMiddleware`), plus
  `shared.Go(log, name, fn)` as a panic-safe goroutine launcher.
  Converted the six fire-and-forget spawns: flush, tiktok,
  tiktok-comment, tiktok-shortlink, xpost, captcha-welcome.
- **Transport retry.** `retry.classify` gained `kindTransport` (any
  error that is neither a Telegram API error nor a caller
  cancellation), `MaxTransportAttempts = 3`, ladder 1/2/4s, and
  `Policy.AttemptTimeout` bounding each attempt. `tgclient` now carries
  two budgets: control calls 20s per attempt, media sends 240s with at
  most two transport attempts.
- **No unbounded calls.** `telego.NewBot` gets a `fasthttp.Client` with
  `ReadTimeout: 65s` / `WriteTimeout: 240s` (telego's default client has
  neither, and its caller applies the context deadline only when the
  context has one); the long poll sets `Timeout: 30` explicitly.
- **TikTok source.** New `internal/bot/tiktok_source.go`: the tikwm
  mirror resolves the link and streams the MP4 from a
  `*.tiktokcdn-us.com` host, sharing the comment fetcher's pacer.
  `downloadTikTok` is now mirror-first with `ytdlpDownload` as the
  fallback; both paths failing produces one error naming each
  (`mirror: ...; yt-dlp: ...`). The yt-dlp path moved to a 45s deadline
  per attempt (was: one 60s deadline shared by three attempts), and its
  captured output is truncated to 400 runes before logging. Photo posts
  (`errPhotoPost`) decline instead of queueing a job no retry can
  complete.
- **Host phases** (independent of the code): the `bot` compose service
  pins its resolvers (`192.168.0.1, 1.1.1.1, 8.8.8.8`) so lookups stop
  traversing the host's stalling `127.0.0.53` stub; `deploy/backup.sh`
  is installed in the root crontab with an explicit
  `BIDLOBOT_COMPOSE_DIR` (the script defaults to `/opt/bidlobot`, the
  checkout is `/home/veschin/bidlobot`).

## Evidence

- Mirror probe from the production egress, three runs:
  `200 1067861` each time for `https://vt.tiktok.com/ZSq5d4Rxh`.
- Photo posts carry a non-empty `data.images` array in the tikwm
  envelope (checked against a real `/photo/` post), and their
  `data.play` points at a music host that serves no MP4 - hence the
  image-carrying-post branch in `mediaFailure`, not only the empty-play
  branch.
- `TestRecoverMiddlewareKeepsProcessing` aborts the test binary when the
  middleware registration is removed (`panic: boom` escaping
  `telegohandler.(*Context).Next` on `sync.(*WaitGroup).Go.func1.1`) and
  passes with it.
- `go test -race ./...` green across all packages; `gofmt -l` clean;
  `go build ./...` and `go vet ./...` clean.

## Follow-up: monthstats boundary race (separate commit)

Found by running the suite for this change: `TestBufferLiveTrackStartPersistedOnce`
failed 8 of 12 runs. It is not a store race - three layers disagreed:

- `MonthStatsRepo.SetLiveTrackStart` keeps the MINIMUM ts (a later write
  is a no-op, an earlier one lands).
- The package's in-memory test double refused every write after the
  first, so it could not express the ordering the flush produces.
- `Buffer.flush` skipped chats already marked `liveStartDone`, and the
  eager first-`Add` persist set that flag - so once the first-seen ts was
  stored, no flush could lower the boundary to the earliest live message
  the chat had actually produced.

The gate is the defect: `30_stats.md` defines `LiveTrackStart` as the
earliest live message ts, because the (unwired) importer skips
`ts >= LiveTrackStart`, and a boundary that sits too late leaves those
messages reachable by both paths. Fix: the flush now persists the running
minimum for every chat with a boundary and lets the store's min setter
decide whether a write lands (a repeat is one no-op transaction), and the
`liveStartDone` map is gone. The double now mirrors the repo, the eager
persist logs its failure instead of dropping it, and the flaky test was
replaced by `TestBufferLiveTrackStartTracksEarliest`, which forces the
eager write to land before the flush and therefore asserts the same
contract deterministically. A later-boundary case in the bbolt repo test
was corrected (the comment said "first write wins"; the code lowers) and
gained coverage for the lowering direction.

Verified: 25/25 green with `-race` after the fix (8/12 failing before),
and the new test fails deterministically against the pre-fix `buffer.go`.

## Notes

- The old `TestNonAPIErrorNotRetried` encoded the retired contract (a
  plain error is never retried). It was replaced by the transport-ladder
  tests rather than re-pinned.
- Residual risk: the mirror download reuses the shared 30s client
  (`tiktokCommentHTTPClient`), so a CDN transfer of a very large video
  can exceed the client timeout and fall back to yt-dlp. Measured
  throughput was ~1 MiB in 1.1-1.5s, which leaves the 30s bound
  comfortable for the sizes seen in production.
