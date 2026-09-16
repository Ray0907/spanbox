# Durability with Litestream result

Implemented and verified with Litestream 0.5.17, installed using:

```sh
brew install benbjohnson/litestream/litestream
```

Litestream 0.5 changed the plan's configuration sketch in two places: `replicas` is now the singular `replica`, and the 168-hour retention setting is `snapshot.retention` rather than a replica-level `retention`. `sync-interval`, `endpoint`, `access-key-id`, and `secret-access-key` remain valid replica keys.

End-to-end verification output:

```text
$ ./deploy/litestream/verify.sh
PASS
```

The installed Docker CLI does not include the Compose plugin (`docker compose` reports `unknown command`), and the Docker daemon is unavailable, so `deploy/litestream/docker-compose.yml` was not executed or validated with Compose.
