# Deploy and Back Up

## Platforms

Release binaries: Linux amd64/arm64, macOS amd64/arm64, Windows amd64.

CI exercises Ubuntu amd64, an Arch Linux amd64 container, Windows amd64, and macOS 15 arm64/amd64 with Go vet, race-enabled tests, and builds. Linux arm64 is a published cross-build target, not runtime-tested by CI.

Windows: CI proves process termination and data round trips, not graceful shutdown.

## Docker

```sh
mkdir -m 700 data
docker run -p 4318:4318 -v ./data:/data -e AUTH_TOKEN=$(openssl rand -hex 32) ghcr.io/ray0907/spanbox
```

## Single writer

Never run two spanbox instances against the same database file.

## Durability with Litestream

[Litestream](https://litestream.io/) replicates `spanbox.db` continuously, with a recovery point of about its 1-second sync interval, and needs no spanbox changes.

1. Install Litestream 0.5.x. On macOS: `brew install benbjohnson/litestream/litestream`.
2. Copy `deploy/litestream/.env.example` to `deploy/litestream/.env`.
3. Use `deploy/litestream/docker-compose.yml`.

On a fresh volume, restore before starting spanbox:

```sh
cd deploy/litestream
docker compose run --rm litestream restore -if-replica-exists -o /data/spanbox.db /data/spanbox.db
```

Restore first because spanbox creates an empty database otherwise, which Litestream could replicate over the good replica. `-if-replica-exists` makes a first deployment safe when no backup exists. Automated deployments should enforce restore-before-spanbox with an init container and `depends_on` ordering.

## Health checks

`GET /healthz` and `GET /api/public/health` need no token. Give startup probes time for [schema migration](retention.md#upgrades).

## Arch Linux

The `spanbox-bin` PKGBUILD in `deploy/aur` is published to the AUR only when the `AUR_SSH_PRIVATE_KEY` secret is configured. It is not published at the moment; install from the release binary.
