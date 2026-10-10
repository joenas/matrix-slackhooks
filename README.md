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
`SLACKHOOKS_ALLOWED_ROOMS` (comma-separated list of room IDs/aliases),
`SLACKHOOKS_ADMINS` (comma-separated list of user IDs/patterns),
`SLACKHOOKS_COMMAND_POWER_LEVEL`, `SLACKHOOKS_COMMAND_PREFIX`.
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

Webhooks are managed from the command line against the SQLite database, or
from inside Matrix via bot commands. The CLI can run while the service is
running (the database is in WAL mode with a busy timeout, so concurrent access
is safe — handy with `docker exec … slackhooks add-hook`):

```
slackhooks -config config.yaml add-hook -label "CI" '!roomid:localhost'
slackhooks -config config.yaml add-hook '#my-room:localhost'   # aliases are resolved too
slackhooks -config config.yaml list-hooks                       # all webhooks
slackhooks -config config.yaml list-hooks '!roomid:localhost'   # just one room
slackhooks -config config.yaml remove-hook abc12345             # by token or unique prefix (min 4 chars)
slackhooks -config config.yaml backup slackhooks-2026.db        # safe snapshot while running
```

`add-hook` takes a room ID (`!localpart:server`) or an alias
(`#localpart:server`, resolved through the homeserver when tokens are set). It
prints a URL like `https://hooks.example.com/hooks/<token>` and a reminder to
invite the bot. `remove-hook` needs the full token or a prefix at least 4
characters that matches exactly one webhook; on ambiguity it lists the
candidates.

### Bot commands

In any room the bot is in, use `!hook help` to list commands. The prefix
(`!hook` by default) is configurable via `command_prefix` or
`SLACKHOOKS_COMMAND_PREFIX`.

| Command | Description |
|---|---|
| `!hook new [label]` | Create a hook for this room. The full URL is sent to you by direct message. In the room, only the label and token prefix are shown. |
| `!hook list` | List hooks in this room: label, 8-character token prefix, creator and creation date. |
| `!hook remove <label\|token-prefix>` | Remove a hook by exact label (case-insensitive) or token prefix (min 4 chars). Ambiguous matches are listed without deleting. |
| `!hook help` | Show this help. |

**Permissions:**

- **Admins** (listed in `admins` config / `SLACKHOOKS_ADMINS`) may run commands
  in any room the bot is in, and may invite the bot to any room (including rooms
  without a hook that are not in `allowed_rooms`). The `@*:example.com` pattern
  matches every user on that server.
- **Non-admins** need a power level at or above `command_power_level` (default
  `50`) in the room. Setting `command_power_level: 101` effectively means
  "admins only", because no normal user can reach that level.

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
  appservice address (`http://slackhooks:29329`, the network alias from the
  stack file), make sure Synapse is attached to the `matrix` overlay network,
  and restart Synapse. Don't use the Swarm service name
  `slackhooks_slackhooks`: Synapse rejects hostnames containing `_`.
- Copy the existing `slackhooks.db` into the stack's data volume before the
  first start, owned by `10001:10001`. `docker stack deploy` prefixes volume
  names with the stack name, so with the stack named `slackhooks` the volume
  is `slackhooks_slackhooks_data`; Swarm reuses it if it already exists on the
  node. Stop the old process first, and
  copy any `-wal`/`-shm` files along with it — or better, take a consistent
  snapshot from the old install with `slackhooks backup` and copy that in as
  `slackhooks.db`. Using a throwaway container on the labelled node:

  ```
  docker run --rm -v slackhooks_slackhooks_data:/data -v $PWD:/src alpine \
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

## Security

- **Hook URLs are bearer secrets.** Anyone who knows the URL can post messages
  to the room. The bot only sends full URLs by DM, never in a room. The
  `list-hooks` command and `!hook list` show only an 8-character token prefix;
  the CLI shows the full URL but only on the terminal of the operator who ran
  the command. Token logs are truncated to 6 characters.
- **Command permissions.** By default users need power level 50 or above to run
  bot commands. Set `command_power_level: 101` to restrict commands to admins
  only. Admins are configured in the `admins` config option; they can run
  commands in any room and invite the bot anywhere.
- **Encrypted rooms.** The bot does not support E2EE. Messages sent to the bot
  via bot commands in encrypted rooms are not seen by the bot, so "new",
  "list", "remove" won't work there.
- **SSRF guard for avatar downloads.** The avatar download client refuses
  connections to loopback, private, link-local, multicast, unspecified,
  CGNAT (`100.64.0.0/10`), `0.0.0.0/8`, `192.0.0.0/24`, `198.18.0.0/15` and
  NAT64 (`64:ff9b::/96`) addresses, including IPv4-in-IPv6 forms and
  redirect targets.

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
