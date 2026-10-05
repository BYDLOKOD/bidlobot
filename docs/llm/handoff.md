# Handoff - 2026-10-05 (YouTube sanitizer copy rework merged from PR #3)

## 1. State (what is true right now)

- **Committed and pushed.** `master` = the merge of PR #3
  (`fix(youtube): preserve post content when removing share tracking`
  by gudkovWay, two commits) plus two commits added on top:
  `test(youtube): lock caption split, entity offsets, preflight and
  rollback` and `docs(youtube): rewrite the sanitizer spec for the
  copy flow`. GitHub marks PR #3 merged (fast-forward, no merge
  commit).
- **What changed.**
  - `internal/bot/youtube_sanitizer.go`: repost-with-header flow
    replaced by copy-then-delete; preflight rejects album items, paid
    media, inline keyboards, spoilers, automatic forwards, exotic
    reply contexts and unsupported kinds; sender exclusions unchanged.
  - `internal/bot/youtube_entities.go` (new): byte-span `si` deletion
    (no `net/url` re-encoding), UTF-16 entity remapping, `text_link`
    URL rewrite, fail-closed validation.
  - `internal/bot/youtube_caption.go` (new): >1024-unit captions
    split into <=4096-unit text parts after a caption-less
    `copyMessages`; partial failure rolls back only the bot's messages.
  - `internal/shared/tgclient/client.go`: `CopyMessage` /
    `CopyMessages` wrappers (rate budget + migration rewrite).
  - Tests: three new files lock caption splitting, entity offsets
    (astral included), preflight, rollback and query-span edges.
  - Docs: `55_youtube_sanitizer.md` rewritten, `00_index.md`,
    `60_architecture.md`, devlog 14, this handoff.
- **Verified.** `go build`, `go vet`, `gofmt`, `go test ./...` and the
  full suite under `-race` green on the merged master;
  `docs/llm/validate.sh` exits 0. The risky math (astral offsets,
  hidden-link rewrite, caption splitting) was additionally probed
  with throwaway tests before the permanent ones were written.
- **NOT deployed.** Production still runs the previous build; deploy
  via `.omp/skills/bidlobot-deploy/scripts/deploy.sh --skip-push`
  after the usual owner OK.

## 2. Negatives (what does NOT exist / is not proven)

- No attribution on the copy (owner-approved): the bot's copy names no
  author; a manually forwarded channel post loses its forward header.
- Albums with a tracked link pass through untouched (owner-approved).
- `edited_message` is not re-sanitized (as before).
- The sanitizer still sends synchronously inside the update handler;
  acceptable because a server-side copy is one cheap API call.
- Long-caption splitting (>1024 units) has no live proof yet: Telegram
  Premium captions are the only trigger and none hit the test chats.
- 28 stale specs by `validate.sh`: all pre-existing, none in files
  whose content drifted (the 50_telegram entry covers the two new
  tgclient wrappers, which change nothing the spec describes).

## 3. Queue

- Deploy the new master (owner gate), then watch one real `si=` link:
  text post, media post and a channel forward each once.
- PR #3 author was answered in the PR thread in the house style
  (English, factual, closes with the spec pointer). Keep that style
  for future external PRs.

## 4. Read order

1. `docs/llm/55_youtube_sanitizer.md` - the copy flow, preflight,
   splitting rules, gaps.
2. `docs/llm/devlog/14_youtube_copy_preserve.md` - what the audit
   verified and what was added on top.
3. `internal/bot/youtube_entities.go` then `youtube_caption.go` - the
   span editor and the splitter.
4. `docs/llm/60_architecture.md` ("Failure handling") for where the
   sanitizer sits in the failure matrix.

## 5. Smoke test (run before touching anything)

```sh
go build ./... && go vet ./... && gofmt -l internal/ cmd/
go test ./...                 # expect all ok
bash docs/llm/validate.sh     # expect 0 errors
```

## 6. Agent errors

- The `edit` tool wrote doc edits into the MAIN repository working
  tree when given paths inside the /tmp git worktree; the mistake was
  caught by `git diff` before commit and the files were moved to the
  branch. Verify the location of every edit made through a linked
  worktree path.
