# matrix-slackhooks

Webhooks for Matrix in the Slack incoming-webhook format. One webhook URL per
room; many different sources post to the same URL, each appearing as its own
Matrix user. The display name and avatar are taken from the payload.

Written in Go on top of [mautrix](https://maunium.net/go/mautrix), state is
kept in SQLite ([modernc.org/sqlite](https://modernc.org/sqlite), no CGO).

## Features

- Slack incoming webhook payload format, so existing tools with "Slack
  webhook" support work without changes. turt2live/matrix-appservice-webhooks
  fields (`displayName`, `avatar_url`, `format`) are accepted as aliases.
- One Matrix puppet user per source name, with display name and avatar.
- Slack mrkdwn (`*bold*`, `_italic_`, `~strike~`, code, `<url|label>` links)
  converted to Matrix HTML messages.

## Limitations

- Rooms must **not** be encrypted (E2EE is out of scope).
- Slack `attachments`/`blocks` are only minimally supported: each
  attachment's `fallback` (or `text`) is appended as an extra line.

## Building

```
go build -o slackhooks .
```

## Configuration

Copy `config.yaml.example` to `config.yaml` and adjust homeserver URL, server
name, tokens and the public base URL. The config path defaults to
`$SLACKHOOKS_CONFIG` if set, else `config.yaml`. Any setting can also be
overridden with an environment variable:
`SLACKHOOKS_HOMESERVER_URL`, `SLACKHOOKS_SERVER_NAME`, `SLACKHOOKS_AS_TOKEN`,
`SLACKHOOKS_HS_TOKEN`, `SLACKHOOKS_AS_ADDRESS`, `SLACKHOOKS_APPSERVICE_URL`,
`SLACKHOOKS_WEBHOOK_ADDRESS`, `SLACKHOOKS_PUBLIC_BASE_URL`, `SLACKHOOKS_DB`,
`SLACKHOOKS_BOT_LOCALPART`, `SLACKHOOKS_BOT_DISPLAYNAME`,
`SLACKHOOKS_USER_PREFIX`, `SLACKHOOKS_DEFAULT_MSGTYPE`,
`SLACKHOOKS_ALLOWED_ROOMS` (comma-separated list of room IDs/aliases).
`SLACKHOOKS_AS_TOKEN_FILE` / `SLACKHOOKS_HS_TOKEN_FILE` read the tokens from
the named files (Docker/Swarm secrets) and take precedence over the plain
`SLACKHOOKS_AS_TOKEN` / `SLACKHOOKS_HS_TOKEN` env vars.

## Registration

Generate the appservice registration file and give it to your homeserver
(e.g. add its path to `app_service_config_files` in Synapse's config):

```
slackhooks -config config.yaml generate-registration -out registration.yaml
```

If `as_token`/`hs_token` are empty in the config they are randomly generated,
printed at the end of the command output, and must be copied into the config.
The registration reserves the exclusive user namespace `@_slackhook_*` (plus
the bot user) and sets `rate_limited: false`.

## Running

```
slackhooks -config config.yaml start
```

The appservice (Matrix transaction endpoint) and the webhook endpoint share
one listener by default; set `webhook_address` to serve the webhooks on a
separate address.

## Managing webhooks

Webhooks are managed from the command line against the SQLite database. The
CLI can run while the service is running (the database is in WAL mode with a
busy timeout, so concurrent access is safe — handy with
`docker exec … slackhooks add-hook`):

```
slackhooks -config config.yaml add-hook -label "CI" '!roomid:localhost'
slackhooks -config config.yaml add-hook '#my-room:localhost'   # aliases are resolved too
slackhooks -config config.yaml list-hooks                       # all webhooks
slackhooks -config config.yaml list-hooks '!roomid:localhost'   # just one room
slackhooks -config config.yaml remove-hook abc12345             # by token or unique prefix
slackhooks -config config.yaml backup slackhooks-2026.db        # safe snapshot while running
```

`add-hook` takes a room ID (`!localpart:server`) or an alias
(`#localpart:server`, resolved through the homeserver when tokens are set). It
prints a URL like `https://hooks.example.com/hooks/<token>` and a reminder to
invite the bot. `remove-hook` needs the full token or a prefix that matches
exactly one webhook; on ambiguity it lists the candidates.

## Room access control

The bot only joins rooms it is meant to be in. A room is **allowed** when it
has at least one webhook, or when it is listed in `allowed_rooms` (config) /
`SLACKHOOKS_ALLOWED_ROOMS` (env, comma-separated) as a room ID or alias.

Because rooms with a hook are allowed automatically, the normal flow is:
**create the hook first, then invite the bot** — it joins on the invite. Inviting
the bot to a room with neither a hook nor an `allowed_rooms` entry is rejected
(the bot leaves the invite with a reason). The bot warns in the log at startup
about any room it is already joined to that is not allowed; it never leaves
those automatically. Puppets are still only invited to rooms that have hooks.

Make sure the bot has permission to invite users, because each puppet joins
the room via a bot invite.

## Database

State lives in a single SQLite file (`db` config option). The schema is
versioned and migrated automatically on startup; you never need to wipe the
database to upgrade. A database written by a newer binary is refused rather
than downgraded. Before migrating a database that already holds data, slackhooks
writes a safety snapshot next to the original
(`<db>.bak-v<from>-<timestamp>`) and logs its path. Use `slackhooks backup
<path>` anytime for an on-demand snapshot; it is safe to run while the service
is up and does not require the `sqlite3` binary.

## Payload format

Send a JSON POST (or `application/x-www-form-urlencoded` with the JSON in a
`payload=` field) to the webhook URL:

```json
{
  "text": "Hello *world*! <https://example.com|click here>",
  "username": "Build Bot",
  "icon_url": "https://example.com/bot.png",
  "icon_emoji": ":ghost:",
  "attachments": [{"fallback": "optional fallback text"}]
}
```

| Field | Meaning |
|---|---|
| `text` | Message text (required, or use attachments). Slack mrkdwn is converted to Matrix HTML. |
| `username` / `displayName` | Source name; becomes the Matrix puppet's display name and is used for its username (`@_slackhook_<slug>`). Falls back to the hook label, then `webhook`. |
| `icon_url` / `avatar_url` | Avatar image URL; downloaded (max 2 MB, images only), uploaded to the media repo and set as the puppet's avatar. Cached per URL. Downloads refuse loopback/private/link-local/multicast targets (SSRF guard, also for redirects) and follow at most 3 redirects. |
| `icon_emoji` | `:shortcode:`; rendered as a unicode emoji (small built-in map) prepended to the text when no avatar URL is given. |
| `format` | Set to `"html"` to pass `text` through as HTML (turt2live compat). |

Responses match Slack: `ok` (200) on success, 400 for a bad payload, 404 for
an unknown token. If the homeserver does not answer within 25s the request
fails with 503 ("Matrix homeserver unavailable").

## Docker / Swarm

A multi-stage `Dockerfile` builds a static binary (`CGO_ENABLED=0`) and puts
it on a small Alpine runtime image running as uid 10001. The image defaults
to `SLACKHOOKS_CONFIG=/data/config.yaml`, `SLACKHOOKS_DB=/data/slackhooks.db`
and listens on port 29329; `/data` is a volume owned by the container user, so
a fresh named volume picks up the right ownership. The config file is
optional: with no file present everything comes from `SLACKHOOKS_*`
environment variables, and tokens can be mounted as files with
`SLACKHOOKS_AS_TOKEN_FILE` / `SLACKHOOKS_HS_TOKEN_FILE` (Docker/Swarm
secrets). A built-in `slackhooks healthcheck` subcommand backs the image's
`HEALTHCHECK` (no curl/wget in the image, port read from the config).

Build and push (uses the `dockerctl` script from the dotfiles; needs
`REGISTRY_HOST`, image name `slackhooks` from the justfile):

```
just publish           # = dockerctl build push amd64, tagged git describe --tags --always --dirty
just build             # build only, optionally: just build arm64
```

Without `dockerctl` the plain equivalent is:

```
docker build --build-arg APP_VERSION=$(git describe --tags --always --dirty) -t slackhooks .
```

Other justfile recipes: `just test` (vet + gofmt + tests), `just bin-linux`
(cross-compiled static binary), `just run …` (`go run .` with the same
ldflags), `just docker-run …` (local smoke test of the image with a
`slackhooks-dev` volume and `--env-file .env`).

### Deploying

`deploy/stack.yml` is an example Swarm stack. Generate the appservice
registration and tokens once and create the Swarm secrets (see the comments
on top of that file):

```
docker run --rm \
    -e SLACKHOOKS_SERVER_NAME=example.com \
    -e SLACKHOOKS_HOMESERVER_URL=https://matrix.example.com \
    "$SLACKHOOKS_IMAGE" generate-registration
```

Hand the printed registration to Synapse, store the two tokens as external
secrets, then `docker stack deploy -c deploy/stack.yml slackhooks`. Three
details matter: replicas are pinned to 1 with `order: stop-first` (SQLite
plus one registration must never have two processes on it), the `/data`
volume must be local (WAL on NFS breaks) and the service is therefore pinned
to one node with a placement constraint — label that node once before
deploying:

```
docker node update --label-add slackhooks.data=true <node>
```

(Without the pin, Swarm rescheduling the task to another node would start it
on a fresh empty local volume and every hook would be silently lost.) Only
the webhook port is published for the reverse proxy; the appservice port
stays on the internal overlay network shared with Synapse.

### Moving an existing install into Swarm

If you already run slackhooks outside Docker:

- Reuse the existing `as_token`/`hs_token` from your current config as the
  contents of the two Swarm secrets. Generating new tokens instead also
  means replacing the registration file on Synapse.
- Update the `url` in Synapse's existing registration file to the in-Swarm
  appservice address (`http://slackhooks_slackhooks:29329`), make sure
  Synapse is attached to the `matrix` overlay network, and restart Synapse.
- Copy the existing `slackhooks.db` into the `slackhooks_data` volume before
  the first start, owned by `10001:10001`. Stop the old process first, and
  copy any `-wal`/`-shm` files along with it — or better, take a consistent
  snapshot from the old install with `slackhooks backup` and copy that in as
  `slackhooks.db`. Using a throwaway container on the labelled node:

  ```
  docker run --rm -v slackhooks_data:/data -v $PWD:/src alpine \
      sh -c 'cp /src/slackhooks.db /data/ && chown 10001:10001 /data/slackhooks.db'
  ```

On first start the container migrates the database and writes a
`.bak-v0-…` safety copy next to it.

### Managing hooks on the running service

The CLI can run against the live database while the service keeps running.
`docker exec` does not use the image's `ENTRYPOINT`, so pass the binary name.
In Swarm the container is named `<stack>_slackhooks.1.<id>`, so run this on
the node where the task is scheduled:

```
docker exec -it $(docker ps -qf name=<stack>_slackhooks) slackhooks add-hook -label CI '!room:server'
docker exec -it $(docker ps -qf name=<stack>_slackhooks) slackhooks list-hooks
docker exec $(docker ps -qf name=<stack>_slackhooks) slackhooks backup /data/backup.db
```

No `-config` flag needed: `SLACKHOOKS_CONFIG` and the DB path come from the
image environment. Back up with `slackhooks backup` (a consistent snapshot via
`VACUUM INTO`), not `cp` of the live database, which would miss the WAL file.

## Development

Run the tests:

```
go test ./...
```

For manual testing a local Synapse can be started with
[`dev/docker-compose.yaml`](dev/docker-compose.yaml). The Synapse image does
**not** generate its config by itself on first run, so generate it once
before starting, then bring it up:

```
docker compose -f dev/docker-compose.yaml run --rm synapse generate
docker compose -f dev/docker-compose.yaml up -d
```

Create a human user you can log in with:

```
docker compose -f dev/docker-compose.yaml exec synapse \
    register_new_matrix_user -c /data/homeserver.yaml http://localhost:8008
```

For the remaining steps use a dev `config.yaml` (a copy of
`config.yaml.example`) with `server_name: localhost` and
`homeserver_url: http://localhost:8008` (the compose defaults), and with

```yaml
appservice_url: http://host.docker.internal:29329
```

From inside the Synapse container `localhost` is the container itself, so
the appservice URL must point at the host; the compose file maps
`host.docker.internal` to the host gateway, so this also works on Linux.

Generate the registration straight into the Synapse data directory (the
tokens it prints must be copied into the dev config):

```
slackhooks -config config.yaml generate-registration -out dev/synapse/registration.yaml
```

Reference it in `dev/synapse/homeserver.yaml` (the file appears inside the
container as `/data/registration.yaml`) and restart Synapse:

```yaml
app_service_config_files:
  - /data/registration.yaml
```

```
docker compose -f dev/docker-compose.yaml restart synapse
```

Start slackhooks on the host (`slackhooks -config config.yaml start`, it
listens on port 29329), log into the Synapse web client as the human user and
create a room. Create a hook for that room **first**, then invite the bot user
`@slackhooks:localhost` — a room with a hook is allowed, so the bot joins the
invite automatically. (The bot also needs permission to invite users, because
each puppet joins the room via a bot invite.) Post with curl:

```
slackhooks -config config.yaml add-hook '!roomid:localhost'
curl -d '{"text":"hi","username":"Alice","icon_url":"https://example.com/a.png"}' \
     -H 'Content-Type: application/json' \
     http://localhost:29329/hooks/<token>
```

Two posts with different `username`/`icon_url` produce two distinct users with
the correct names and avatars in the room; re-posting does not re-upload the
avatar or re-set the profile.
