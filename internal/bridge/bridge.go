package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/joenas/matrix-slackhooks/internal/config"
	"github.com/joenas/matrix-slackhooks/internal/slackmd"
	"github.com/joenas/matrix-slackhooks/internal/store"
	"github.com/joenas/matrix-slackhooks/internal/webhook"
)

const maxAvatarSize = 2 << 20 // 2 MB

type Bridge struct {
	Cfg         *config.Config
	DB          *store.DB
	AS          *appservice.AppService
	Log         zerolog.Logger
	MediaClient *http.Client
}

func New(cfg *config.Config, db *store.DB, as *appservice.AppService, log zerolog.Logger) *Bridge {
	return &Bridge{
		Cfg:         cfg,
		DB:          db,
		AS:          as,
		Log:         log,
		MediaClient: newAvatarClient(),
	}
}

// newAvatarClient builds the HTTP client used for avatar downloads. It
// refuses to connect to loopback, private, link-local, unspecified or
// multicast addresses (checked per dial, so redirects and DNS rebinding are
// covered too) and follows at most 3 redirects.
func newAvatarClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("unexpected dial address %q: %w", address, err)
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return fmt.Errorf("unexpected dial address %q: %w", address, err)
			}
			if isLocalIP(ip) {
				return fmt.Errorf("refusing avatar download connection to %s", ip)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("avatar download: too many redirects")
			}
			return nil
		},
	}
}

// isLocalIP reports whether an IP address is one that avatar downloads must
// never reach: loopback, unspecified, private, link-local or multicast
// (including the IPv4-in-IPv6 forms).
func isLocalIP(ip netip.Addr) bool {
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

func (b *Bridge) QueryAlias(alias id.RoomAlias) bool {
	return false
}

func (b *Bridge) QueryUser(userID id.UserID) bool {
	return userID == b.AS.BotMXID() || b.isPuppet(userID)
}

// isPuppet reports whether the user ID belongs to one of this appservice's
// puppet users: a user on our own server whose localpart starts with the
// configured namespace prefix.
func (b *Bridge) isPuppet(userID id.UserID) bool {
	localpart, serverName, err := userID.Parse()
	if err != nil || serverName != b.Cfg.ServerName {
		return false
	}
	return strings.HasPrefix(localpart, b.Cfg.UserPrefix)
}

// Send turns a webhook payload into a Matrix message using a puppet user
// named after the payload's source.
func (b *Bridge) Send(ctx context.Context, hook *store.Hook, payload *webhook.Payload) error {
	log := b.Log.With().Str("room_id", hook.RoomID.String()).Logger()
	name := payload.SourceName()
	if name == "" {
		name = strings.TrimSpace(hook.Label)
	}
	if name == "" {
		name = "webhook"
	}
	localpart := webhook.Localpart(b.Cfg.UserPrefix, name)
	userID := id.NewUserID(localpart, b.Cfg.ServerName)
	intent := b.AS.Intent(userID)
	if intent == nil {
		return fmt.Errorf("failed to create intent for %s", userID)
	} else if err := intent.EnsureRegistered(ctx); err != nil {
		return fmt.Errorf("ensure puppet %s registered: %w", userID, err)
	}

	puppet, err := b.DB.GetPuppet(localpart)
	if err != nil {
		return fmt.Errorf("get puppet: %w", err)
	} else if puppet == nil {
		puppet = &store.Puppet{Localpart: localpart}
	}

	dirty := false
	if puppet.DisplayName != name {
		if err = intent.SetDisplayName(ctx, name); err != nil {
			log.Warn().Err(err).Msg("Failed to set puppet display name")
		} else {
			puppet.DisplayName = name
			dirty = true
		}
	}
	avatarSource := payload.SourceAvatarURL()
	if avatarSource != "" && avatarSource != puppet.AvatarSourceURL {
		mxc, avatarErr := b.resolveAvatar(ctx, intent, avatarSource)
		if avatarErr != nil {
			log.Warn().Err(avatarErr).Str("avatar_url", avatarSource).Msg("Failed to resolve avatar")
		} else if mxc.String() != puppet.AvatarMXC {
			if err = intent.SetAvatarURL(ctx, mxc); err != nil {
				log.Warn().Err(err).Msg("Failed to set puppet avatar")
			} else {
				puppet.AvatarMXC = mxc.String()
				puppet.AvatarSourceURL = avatarSource
				dirty = true
			}
		}
	}
	if dirty {
		if err = b.DB.UpsertPuppet(puppet); err != nil {
			log.Warn().Err(err).Msg("Failed to update puppet record")
		}
	}

	return b.sendMessage(ctx, log, intent, hook.RoomID, b.renderMessage(payload))
}

func (b *Bridge) renderMessage(payload *webhook.Payload) *event.MessageEventContent {
	text := payload.Text
	if payload.IconEmoji != "" && payload.SourceAvatarURL() == "" && text != "" {
		text = webhook.LookupEmoji(payload.IconEmoji) + " " + text
	}
	lines := make([]string, 0, 1+len(payload.Attachments))
	if text != "" {
		lines = append(lines, text)
	}
	for _, attachment := range payload.Attachments {
		line := strings.TrimSpace(attachment.Fallback)
		if line == "" {
			line = strings.TrimSpace(attachment.Text)
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	body := strings.Join(lines, "\n")

	content := &event.MessageEventContent{
		MsgType: b.Cfg.MsgType(),
		Body:    body,
	}
	if payload.Format == "html" {
		content.Body = slackmd.StripHTML(body)
		if content.Body == "" {
			content.Body = body
		}
		content.Format = event.FormatHTML
		content.FormattedBody = body
	} else if plain, formatted := slackmd.SlackToHTML(body); formatted != plain {
		content.Body = plain
		content.Format = event.FormatHTML
		content.FormattedBody = formatted
	}
	return content
}

func (b *Bridge) sendMessage(ctx context.Context, log zerolog.Logger, intent *appservice.IntentAPI, roomID id.RoomID, content *event.MessageEventContent) error {
	_, err := intent.SendMessageEvent(ctx, roomID, event.EventMessage, content)
	if err == nil || !errors.Is(err, mautrix.MForbidden) {
		return err
	}
	// The membership cache said we were in the room, but the homeserver
	// disagrees (e.g. the puppet got kicked while we weren't looking).
	log.Warn().Err(err).Msg("Sending forbidden, dropping membership cache and retrying join")
	_ = b.DB.DeleteMembership(intent.UserID, roomID)
	if joinErr := intent.EnsureJoined(ctx, roomID, appservice.EnsureJoinedParams{IgnoreCache: true}); joinErr != nil {
		return joinErr
	}
	_, err = intent.SendMessageEvent(ctx, roomID, event.EventMessage, content)
	return err
}

func (b *Bridge) resolveAvatar(ctx context.Context, intent *appservice.IntentAPI, sourceURL string) (id.ContentURI, error) {
	if mxc, ok := b.DB.GetAvatar(sourceURL); ok {
		uri, err := id.ContentURIString(mxc).Parse()
		if err == nil {
			return uri, nil
		}
	}
	data, contentType, err := b.downloadAvatar(ctx, sourceURL)
	if err != nil {
		return id.ContentURI{}, err
	}
	resp, err := intent.UploadMedia(ctx, mautrix.ReqUploadMedia{
		ContentBytes: data,
		ContentType:  contentType,
		FileName:     avatarFileName(sourceURL, contentType),
	})
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("upload avatar: %w", err)
	}
	if err = b.DB.PutAvatar(sourceURL, resp.ContentURI.String()); err != nil {
		b.Log.Warn().Err(err).Msg("Failed to cache avatar")
	}
	return resp.ContentURI, nil
}

func (b *Bridge) downloadAvatar(ctx context.Context, avatarURL string) ([]byte, string, error) {
	parsed, err := url.Parse(avatarURL)
	if err != nil {
		return nil, "", fmt.Errorf("invalid avatar url: %w", err)
	} else if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", fmt.Errorf("unsupported avatar url scheme %q", parsed.Scheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, avatarURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := b.MediaClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download avatar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download avatar: unexpected status %d", resp.StatusCode)
	}
	contentType, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("avatar is not an image (content type %q)", contentType)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAvatarSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("read avatar: %w", err)
	} else if len(data) == 0 {
		return nil, "", fmt.Errorf("avatar is empty")
	} else if len(data) > maxAvatarSize {
		return nil, "", fmt.Errorf("avatar is too large (over %d bytes)", maxAvatarSize)
	}
	return data, contentType, nil
}

func avatarFileName(sourceURL, contentType string) string {
	name := path.Base(strings.Split(sourceURL, "?")[0])
	if name == "" || name == "." || name == "/" || !strings.Contains(name, ".") {
		switch contentType {
		case "image/png":
			name = "avatar.png"
		case "image/gif":
			name = "avatar.gif"
		case "image/webp":
			name = "avatar.webp"
		case "image/svg+xml":
			name = "avatar.svg"
		default:
			name = "avatar.jpg"
		}
	}
	return name
}

// HandleRoomMember auto-joins the bot and puppets when invited to rooms.
func (b *Bridge) HandleRoomMember(ctx context.Context, evt *event.Event) {
	member, ok := evt.Content.Parsed.(*event.MemberEventContent)
	if !ok || member.Membership != event.MembershipInvite || evt.StateKey == nil {
		return
	}
	target := id.UserID(*evt.StateKey)
	if target == b.AS.BotMXID() {
		b.handleBotInvite(ctx, evt)
		return
	}
	if !b.isPuppet(target) {
		return
	}
	hasHooks, err := b.DB.HasHooksForRoom(evt.RoomID)
	if err != nil {
		b.Log.Warn().Err(err).Msg("Failed to check hooks for room")
		return
	} else if !hasHooks {
		return
	}
	intent := b.AS.Intent(target)
	if intent == nil {
		return
	}
	if err = intent.EnsureJoined(ctx, evt.RoomID); err != nil {
		b.Log.Warn().Err(err).Str("user_id", target.String()).Str("room_id", evt.RoomID.String()).
			Msg("Failed to join puppet to room")
	}
}

// handleBotInvite joins the bot to allowed rooms and rejects invites to
// everything else, so the bot can only end up in rooms it is meant to serve.
func (b *Bridge) handleBotInvite(ctx context.Context, evt *event.Event) {
	allowed, err := b.roomAllowedFor(ctx, evt.RoomID)
	if err != nil {
		b.Log.Warn().Err(err).Str("room_id", evt.RoomID.String()).
			Msg("Failed to evaluate room allowlist; not joining")
		return
	}
	if allowed {
		if _, err = b.AS.BotIntent().JoinRoomByID(ctx, evt.RoomID); err != nil {
			b.Log.Warn().Err(err).Str("room_id", evt.RoomID.String()).Msg("Failed to join invite as bot")
		}
		return
	}
	b.Log.Info().Str("room_id", evt.RoomID.String()).Str("inviter", evt.Sender.String()).
		Msg("Rejecting bot invite to a room that is not allowed")
	if _, err = b.AS.BotIntent().LeaveRoom(ctx, evt.RoomID,
		&mautrix.ReqLeave{Reason: "This room is not allowed for slackhooks"}); err != nil {
		b.Log.Warn().Err(err).Str("room_id", evt.RoomID.String()).Msg("Failed to reject invite as bot")
	}
}

// roomAllowedFor reports whether the bot may join roomID: it has a webhook, or
// it is in the configured allowlist (aliases resolved against the homeserver).
func (b *Bridge) roomAllowedFor(ctx context.Context, roomID id.RoomID) (bool, error) {
	hasHooks, err := b.DB.HasHooksForRoom(roomID)
	if err != nil {
		return false, err
	}
	resolve := func(alias id.RoomAlias) (id.RoomID, bool) {
		resp, err := b.AS.BotIntent().ResolveAlias(ctx, alias)
		if err != nil {
			b.Log.Warn().Err(err).Str("alias", alias.String()).Msg("Failed to resolve allowed room alias")
			return "", false
		}
		return resp.RoomID, true
	}
	return roomAllowed(roomID, hasHooks, b.Cfg.AllowedRooms, resolve), nil
}

// roomAllowed is the pure allowlist decision. A room is allowed when it has at
// least one webhook, or when it is listed in allowedRooms by room ID or by an
// alias that resolveAlias maps back to it. resolveAlias may be nil to disable
// alias resolution (e.g. no homeserver). It performs no Matrix or database
// calls so the rule is directly testable.
func roomAllowed(roomID id.RoomID, hasHooks bool, allowedRooms []string, resolveAlias func(id.RoomAlias) (id.RoomID, bool)) bool {
	if hasHooks {
		return true
	}
	for _, entry := range allowedRooms {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, "#") {
			if resolveAlias == nil {
				continue
			}
			if resolved, ok := resolveAlias(id.RoomAlias(entry)); ok && resolved == roomID {
				return true
			}
			continue
		}
		if id.RoomID(entry) == roomID {
			return true
		}
	}
	return false
}

// WarnDisallowedJoinedRooms logs a warning for every room the bot is joined to
// that is no longer allowed (no webhook and not in allowed_rooms). It only
// logs; leaving rooms is left to the operator.
func (b *Bridge) WarnDisallowedJoinedRooms(ctx context.Context) {
	resp, err := b.AS.BotIntent().JoinedRooms(ctx)
	if err != nil {
		b.Log.Warn().Err(err).Msg("Failed to list joined rooms for the allowlist check")
		return
	}
	for _, roomID := range resp.JoinedRooms {
		allowed, err := b.roomAllowedFor(ctx, roomID)
		if err != nil {
			b.Log.Warn().Err(err).Str("room_id", roomID.String()).Msg("Failed to check room allowlist")
			continue
		}
		if !allowed {
			b.Log.Warn().Str("room_id", roomID.String()).
				Msg("Bot is joined to a room that is not allowed (no webhook, not in allowed_rooms)")
		}
	}
}

// HandleBotMessage ignores messages sent to the bot. Management is done
// entirely through the CLI; bot commands are intentionally not supported.
func (b *Bridge) HandleBotMessage(ctx context.Context, evt *event.Event) {
	if evt.Sender == b.AS.BotMXID() {
		return
	}
	b.Log.Debug().Str("sender", evt.Sender.String()).Str("room_id", evt.RoomID.String()).
		Msg("Ignoring message to bot (management is done with the CLI; bot commands are not supported)")
}
