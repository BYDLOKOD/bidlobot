---
name: bidlobot-deploy
description: "Deploy bidlobot to the homelab k8s cluster (gitops branch + Flux), check status, view logs, rollback. Use when user asks to deploy/push/update/restart/release/status the bidlobot Telegram bot."
---

# BidloBot Deploy

Production runs in the `bidlobot` namespace of the homelab k3s cluster
(context `arsolitt@homelab`), reconciled by Flux from the orphan `gitops`
branch of this repository. The VM100 docker-compose deploy and its scripts
(`deploy.sh`, `status.sh`) were removed 2026-10-17 with the host they served.

## HARD GATE

1. `go test ./...` must be GREEN; `gofmt -l` on changed files must be EMPTY.
2. **Pushing to `master` IS a production deploy**: CI runs the
   publish chain (test -> image -> smoke + sign -> update-manifest),
   bumps `newTag` + `digest` on the `gitops` branch, and Flux rolls
   the pod within ~6 min (build time + 5m source interval + 1m
   reconcile). Ask the user for explicit OK before pushing - the
   Recreate rollout drops in-flight messages for ~2 s while the
   long-poll reconnects.
3. Never commit code you did not write: `git status --porcelain` before
   `git add`, stage only your files.

## Deploy path

```
push to master
  -> publish.yml test job: go vet + go test gate the build
  -> publish.yml image job: ghcr.io/bydlokod/bidlobot:<sha7> (linux/amd64)
  -> publish.yml smoke job: boot the pushed digest (--check-config + fail-fast auth gate)
  -> publish.yml sign job: cosign keyless OIDC over the digest
  -> publish.yml update-manifest job: sed newTag + digest in deploy/kustomization.yaml on gitops
  -> Flux GitRepository `bidlobot` (branch gitops, ns flux-system; reconcile on push once a webhook Receiver is wired, otherwise manual/gated by the source interval)
  -> Recreate rollout of the single getUpdates poller
```

Wiring (GitRepository + owner Kustomization) lives in the homelab gitops
repo, `clusters/homelab/{sources,apps}/bidlobot.yaml`. Manifests and the
bootstrap/backup runbooks live in `deploy/` on the `gitops` branch.

## Manual pin (rollback or hotpin)

On the `gitops` branch, set BOTH `newTag` and `digest` in
`deploy/kustomization.yaml` to the target revision and push - with
`digest:` present the digest wins, so a `newTag`-only edit changes
nothing that runs. Nodes cache images (`IfNotPresent`), so pinning
back to a previously pulled digest is instant. Tags are immutable
(`master-<sha7>`, see `README.md`), never overwrite one;
find an older digest with `docker buildx imagetools inspect
ghcr.io/bydlokod/bidlobot:<tag>`.

Code rollback: `git revert` on `master` and push - CI rebuilds, and
the chain rewrites `newTag` + `digest` itself.

## Status

```fish
set ctx arsolitt@homelab
kubectl --context $ctx -n bidlobot get deploy,pods
kubectl --context $ctx -n bidlobot get externalsecret bidlobot-secrets
kubectl --context $ctx -n bidlobot logs deploy/bidlobot --since 10m
flux --context $ctx get sources git -A
flux --context $ctx get kustomizations -A
kubectl --context $ctx -n bidlobot exec deploy/bidlobot -- \
    wget -qO- http://127.0.0.1:8080/livez
```

Pod health detail (`/health` poll-freshness reason strings, `/version`
build info): same exec pattern with `/health` and `/version`. Kubelet
probes use `/livez` only - see `docs/llm/70_deployment.md`.

## Secrets

Values live in the Bitwarden Secrets Manager project `homelab`
(`bidlobot-tg-bot-token`, `bidlobot-bot-owner-id`,
`bidlobot-deepseek-api-key`), pulled by the ExternalSecret
`bidlobot-secrets` through the `bitwarden` ClusterSecretStore; nothing
secret is committed anywhere. To rotate: change the value in Bitwarden,
then force a sync and restart (envFrom is read at container start):

```fish
kubectl --context arsolitt@homelab -n bidlobot annotate externalsecret \
    bidlobot-secrets force-sync=$(date +%s) --overwrite
kubectl --context arsolitt@homelab -n bidlobot rollout status deploy/bidlobot \
    --timeout=3m || kubectl --context arsolitt@homelab -n bidlobot rollout restart deploy/bidlobot
```

## Failure modes

| Symptom | Cause | Fix |
|---|---|---|
| ExternalSecret `Ready=False` (ProviderNotFound) | Bitwarden secret missing/renamed | create it in the project, check `kubectl describe es` events |
| Pod `Pending` | PVC/node (single `local-retain` volume) | check events; volume is node-pinned |
| Summarize fails `No API key found for deepseek` | `DEEPSEEK_API_KEY` missing from the Secret | Bitwarden value + rotation recipe above |
| getUpdates 409 conflict | two pollers on one token | never scale past `replicas: 1`; stop any local run using the prod token |
| Rollout stuck on new tag | image build failed, or smoke/sign failed before `update-manifest` | check the publish workflow run; pin `newTag` + `digest` back |
