# TODO - xpost v2 + docs v3 migration (started 2026-08-16)

- [x] Rework internal/bot/xpost.go: single-message repost via FixTweet
      API, repost-then-delete, no renderer sidecar
  Accept: xpost pipeline tests all pass
  Evidence: go test ./internal/bot/ -run 'ProcessXPost' -count=1 2026-08-16 - 10/10 PASS (decision table, variant selection, caption budget, album assembly, fallbacks, decline paths); earlier narrow filter missed the Process* tests, full suite caught an allowlist port-key bug, fixed and re-verified
- [x] Add SendMediaGroup to tgclient + youtubeMediaSender + test fakes
  Accept: go build ./... clean
  Evidence: go build ./... 2026-08-16 - no output, exit 0
- [x] Delete xshot sidecar (dir + compose service + depends_on)
  Accept: no xshot references outside immutable history
  Evidence: grep 2026-08-16 - only the 57_xpost.md history note and compose header mention remain
- [x] Migrate docs/llm to v3 (PRD, ROADMAP, frontmatter, TODO, validate.sh)
  Accept: docs/llm/validate.sh exits 0
  Evidence: ./validate.sh 2026-08-16 - "validate: 0 error(s), 0 warn(s), 49 stale spec(s)"; 49 STALE hits recorded as ROADMAP E8 actualization queue
- [ ] Owner approves PRD freeze
  Accept: PRD.md says "Status: FROZEN 2026-08-16" (10_scope.md already deleted, absorbed)
- [ ] Owner approves xpost caption format (user-facing)
  Accept: explicit OK in chat for the "sender / author / text / canonical url" caption layout
- [ ] Deploy xpost v2 to VM100
  Accept: scripts/deploy.sh run with explicit owner OK; smoke: X link in prod chat -> one message, original deleted
