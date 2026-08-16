# BidloBot - PRD

Status: DRAFT
<!-- On user approval replace with: Status: FROZEN 2026-08-15 -->
<!-- Later edits need user permission; each lands as:
     Amended: 2026-09-01 - <one-line reason> -->

## Problem

IT-community supergroups on Telegram run on a mix of engagement, content sharing,
and gatekeeping, and their admins do not want to moderate any of it by hand. BidloBot
covers that ground as a group-management plus content tool: it measures activity, runs
games, tracks reputation and referral links, cleans up shared external posts, summarizes
conversations for admins, and gates new joiners, all in one always-on service. The
moderation era is deliberately over (all moderation surfaces removed 2026-06): the
product is a read/game/content tool, installable only by its owner, cheap to run as a
single long-polling process with an embedded database.

## Concept

An always-on Telegram bot that makes IT supergroups livelier and cleaner with zero
moderation work. It measures activity, runs lightweight games, and keeps durable per-chat
reputation and referral catalogs. External posts - YouTube links with tracking params,
TikTok videos, X/Twitter posts - are re-shared as attributed, self-contained messages and
the originals removed. Admins can request an LLM digest of the recent conversation. New
members pass an opt-in math captcha with a welcome animation before they can post. The
bot is installed only by its owner and exposes no moderation commands anywhere, in group,
DM, or inline.

## Scope

In:
- Statistics - message counters per user, top contributors, activity reports, retroactive
  monthly nominations.
- Mini-games - dice, battle, code-quiz, native poll, 8ball, guess, hangman, duel,
  IT-trivia; callable inline or via slash commands.
- Reputation - durable per-chat praise/roast economy (`/praise` `/roast` `/rep` `/reptop`).
- Referral catalog - chat-scoped registry of referral links (`/refs` `/refreg` `/refreport`).
- YouTube `si=` sanitizer - strips the share-tracking param (delete + attributed repost).
- TikTok video repost - downloads a TikTok video, reposts it attributed, deletes the original.
- X/Twitter post repost - a status URL becomes ONE message: tweet text as caption, every
  photo/video as native Telegram media, canonical link to the original, via the public
  FixTweet API; the original is deleted only after a successful send. No renderer sidecar.
- Chat summarization - admin-only `/summarize [N]` (alias `/итог`): an LLM condenses the
  last N messages the bot heard.
- New-member captcha + admission gate - owner-only installation (`BOT_OWNER_ID`), opt-in
  math captcha with welcome animation.
- Deferred retry queue - failed TikTok exports / summarize calls are queued per user and
  retried via `/flush` (48h TTL).
- Command surfaces - DM: `/help`, `/start`, owner-only `/chats`; supergroup slash set
  (stats, games, reputation, referral, summarize, flush); read-only inline launcher. No
  moderation verbs.

Out (non-goals):
- DM moderation console (`/warn /warns /mute /unmute /ban /unban`, DM `/cleanup`,
  `/import`) - removed 2026-06, must not return.
- Public moderation (group slash verbs, inline moderation) - removed 2026-05, must not
  return.
- Inactive-cleanup campaigns (`/cleanup <period>` -> gracekick tags) - no command seeds a
  campaign; the daily tick stays idle.
- History import (Telegram Desktop export) - not wired; membership and monthly stats are
  live-observed/live-fed only.
- Bio/profile registration FSM - archived (branch `archive/profiles-bio`, tag
  `v0-bio-archive`).
- Permanently dropped (no reintroduction without an explicit ask): YouTube Summary (LLM
  summarization of external videos), Inline Query DSL parser, salary field, zen-lang
  config, i18n switching, bot-managed admin list.

## Success criteria

- Build and tests are green: `go test -race ./...` passes.
- Deployment model holds: single Go binary + embedded bbolt; docker-compose.yml declares
  exactly one service (`bot`); long polling; container-loopback healthcheck on /health;
  256 MB memory limit; non-root with tini as PID 1; data in the `bidlobot-data` volume.
  The xshot renderer sidecar exists neither in compose nor in the tree.
- X-post contract: one repost message per status URL (caption, native media, canonical
  link, via the public FixTweet API); the original is deleted only after a successful send
  and kept with a decline note on failure; at most one expansion in flight at a time.
- TikTok contract: download -> attributed repost -> delete original.
- YouTube contract: delete + attributed repost without `si=`.
- Summarize contract: admin-only, always-on provider.
- Admission contract: only `BOT_OWNER_ID` may add the bot; captcha is opt-in via
  `CAPTCHA_ENABLED`, wrong or missing answer is kicked.
- Deferred queue contract: `/flush` retries the caller's own failed jobs within a 48h
  window.
- Performance targets: update processing < 100 ms (p95); memory < 100 MB at 50 active
  chats (container limit 256 MB); outgoing <= 15 msg/min/chat.
