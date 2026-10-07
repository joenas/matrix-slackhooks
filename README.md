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
- Bot management commands are not implemented yet.
- Slack `attachments`/`blocks` are only minimally supported: each
  attachment's `fallback` (or `text`) is appended as an extra line.

## Building

```
go build -o slackhooks .
```

## Configuration

Copy `config.yaml.example` to `config.yaml` and adjust homeserver URL, server
name, tokens and the public base URL. Any setting can also be overridden with
an environment variable:
`SLACKHOOKS_HOMESERVER_URL`, `SLACKHOOKS_SERVER_NAME`, `SLACKHOOKS_AS_TOKEN`,
`SLACKHOOKS_HS_TOKEN`, `SLACKHOOKS_AS_ADDRESS`, `SLACKHOOKS_APPSERVICE_URL`,
`SLACKHOOKS_WEBHOOK_ADDRESS`, `SLACKHOOKS_PUBLIC_BASE_URL`, `SLACKHOOKS_DB`,
`SLACKHOOKS_BOT_LOCALPART`, `SLACKHOOKS_USER_PREFIX`,
`SLACKHOOKS_DEFAULT_MSGTYPE`.

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

## Creating webhook URLs

Webhooks are currently created by inserting rows into the `hooks` table, e.g.
with the built-in command:

```
slackhooks -config config.yaml add-hook -label "CI" '!roomid:localhost'
```

It prints a URL like `https://hooks.example.com/hooks/<token>`.

Before the first message arrives, invite the bot user (`@slackhooks:<server>`)
to the room — it joins automatically — and make sure the bot has permission to
invite users, because each puppet joins the room via a bot invite.

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
an unknown token.

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
listens on port 29329), log into the Synapse web client as the human user,
create a room and **invite the bot user `@slackhooks:localhost`** to it — it
joins automatically. The bot also needs permission to invite users, because
each puppet joins the room via a bot invite. Then create a hook for the room
and post with curl:

```
slackhooks -config config.yaml add-hook '!roomid:localhost'
curl -d '{"text":"hi","username":"Alice","icon_url":"https://example.com/a.png"}' \
     -H 'Content-Type: application/json' \
     http://localhost:29329/hooks/<token>
```

Two posts with different `username`/`icon_url` produce two distinct users with
the correct names and avatars in the room; re-posting does not re-upload the
avatar or re-set the profile.
