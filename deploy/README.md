# bidlobot

This branch (`gitops`) carries only `deploy/` — the Kubernetes payload
Flux reconciles into the homelab cluster. Everything else about the bot
lives on `master`.

Single-container Go Telegram bot (telego, long-polling `getUpdates`, no inbound traffic) whose
state is an embedded bbolt database at `/var/lib/bidlobot/bidlobot.db` on the `bidlobot-data`
PVC. tini is PID 1; the container runs as uid/gid 65532 with a writable rootfs. The image's own
healthcheck hits `/health` on port 8080 — the container's only port, since the bot has no
Service and no HTTPRoute. This Deployment's kubelet probes target the static `/livez` instead:
`/health` is dependency-aware (503 when no successful Telegram poll happened for 5 minutes —
Telegram outage or a wedged poll loop) and restarting the pod cannot fix either. Before the
fork build `master-ed97cc3-f0963759`, `/health` was freshness-gated on *chat* activity and
probing it caused a kubelet restart loop every ~6.5 min (2026-10-11); the fork re-keyed `/health`
to a poll-loop heartbeat and added `/livez` as the proper kubelet target.

## Deploy

Pushing to the fork's `master` is the whole deploy. The publish workflow
(`.github/workflows/publish.yml` on `master`) builds and pushes
`ghcr.io/bydlokod/bidlobot:<tag>` (linux/amd64, GHCR), then its
`update-manifest` job rewrites `newTag` in `deploy/kustomization.yaml`
on this branch and pushes `chore(deploy): bump image tag to <tag>`.
Flux picks the commit up through the `bidlobot` GitRepository
(`https://github.com/BYDLOKOD/bidlobot.git`, `ref: {branch: gitops}`,
interval 1h, push-triggered via a webhook Receiver) and the owner Kustomization (`path: deploy`, interval 1m,
`prune: false`, `wait: true`); both live in the homelab gitops repo at
`clusters/homelab/{sources,apps}/bidlobot.yaml`. The kustomize
`images:` transformer pins the actual tag — never edit the Deployment
image by hand. Secrets never enter this repo: the ExternalSecret in
`externalsecret.yaml` pulls `TG_BOT_TOKEN`, `BOT_OWNER_ID` and
`DEEPSEEK_API_KEY` from the Bitwarden Secrets Manager project `homelab`
via the `bitwarden` ClusterSecretStore and materializes them as the
`bidlobot-secrets` Secret the Deployment `envFrom`s.

## Image provenance

`ghcr.io/bydlokod/bidlobot` is built by the publish workflow in the
public `BYDLOKOD/bidlobot` fork, from the fork's own tree (upstream
`BYDLOKOD/bidlobot` base plus local commits). Upstream publishes no
image (its CI builds the Dockerfile with `push: false`). Tags are
`<upstream7>-f<fork7>` computed by the workflow and immutable per
build — they must be, since this Deployment pulls with `IfNotPresent`
and a reused tag would strand the node on a cached layer. The running
tag is pinned by `newTag` in `kustomization.yaml`; the
`update-manifest` job bumps it automatically after every successful
build. Upstream bumps rebase the fork's local commits onto the new
upstream base and update `UPSTREAM_SHA` in the workflow.

## Bootstrap: restore the bbolt seed

`replicas` stays 0 until the seed database is on the volume: Telegram allows exactly one
`getUpdates` poller per token and bbolt holds an exclusive flock on the file, so a second
replica — or a restore over a running one — would fight the live process over the same bot.
Never restore over a mounted database: scale the Deployment to 0 first.

Copy the seed in with a helper pod on the same volume:

```fish
set ctx arsolitt@homelab
kubectl --context $ctx -n bidlobot run bidlobot-restore --image=busybox:1.37.0 --restart=Never \
    --overrides='{"spec":{"containers":[{"name":"restore","image":"busybox:1.37.0","command":["sleep","3600"],"volumeMounts":[{"name":"data","mountPath":"/data"}]}],"volumes":[{"name":"data","persistentVolumeClaim":{"claimName":"bidlobot-data"}}]}}'
kubectl --context $ctx -n bidlobot wait pod/bidlobot-restore --for=condition=Ready --timeout=2m
kubectl --context $ctx -n bidlobot cp ./bidlobot.db bidlobot-restore:/data/bidlobot.db
kubectl --context $ctx -n bidlobot exec bidlobot-restore -- chown 65532:65532 /data/bidlobot.db
kubectl --context $ctx -n bidlobot delete pod bidlobot-restore
```

`./bidlobot.db` is the seed taken from the bot's previous deployment; the destination name must
stay `bidlobot.db`, and the `chown` is what lets the non-root container open the file. Then flip
`replicas: 0 -> 1` in `deployment.yaml` in the follow-up commit.

## Backup

bbolt holds the exclusive flock for the process lifetime, so there is no online file copy: a
`kubectl cp` of a live database blocks or tears. To snapshot, scale the Deployment to 0, mount
the volume with the same helper-pod recipe, copy the file out, then scale back. The PVC uses the
`local-retain` StorageClass, so deleting the claim keeps the underlying PV instead of wiping the
database.
