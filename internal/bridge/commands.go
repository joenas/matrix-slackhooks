package bridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/joenas/matrix-slackhooks/internal/store"
)

// maxCommandAge is the age beyond which a message event is too old to be
// treated as a bot command, so a homeserver replaying queued transactions
// after downtime can't trigger old commands.
const maxCommandAge = 10 * time.Minute

// ParsedCommand holds a parsed bot command. Args is the raw argument string
// after the subcommand (trimmed); for "new" and "remove" it is the label or
// query respectively, and may contain spaces.
type ParsedCommand struct {
	Subcommand string
	Args       string
}

// parseCommand parses a message body as a bot command. Returns nil if the
// body does not start with the configured prefix (exactly, or followed by a
// space). This is a pure function with no Matrix or database calls.
func parseCommand(body, prefix string) *ParsedCommand {
	body = strings.TrimSpace(body)
	if body != prefix && !strings.HasPrefix(body, prefix+" ") {
		return nil
	}
	rest := strings.TrimSpace(strings.TrimPrefix(body, prefix))
	if rest == "" {
		return &ParsedCommand{Subcommand: ""}
	}
	parts := strings.SplitN(rest, " ", 2)
	cmd := &ParsedCommand{Subcommand: strings.ToLower(parts[0])}
	if len(parts) > 1 {
		cmd.Args = strings.TrimSpace(parts[1])
	}
	return cmd
}

// canRunCommand is the pure permission decision: the sender is an admin, or
// their power level meets the configured threshold. Setting the threshold
// above 100 (e.g. 101) effectively means "admins only", because no normal user
// can reach that level.
func canRunCommand(isAdmin bool, userPowerLevel, requiredLevel int) bool {
	return isAdmin || userPowerLevel >= requiredLevel
}

// matchHooksForRemoval finds hooks matching the query within the given list,
// scoped to roomID. A match is an exact label (case-insensitive) or a token
// prefix of at least 4 characters. Hooks in other rooms are never matched.
// This is a pure function with no Matrix or database calls.
func matchHooksForRemoval(hooks []store.Hook, roomID id.RoomID, query string) []store.Hook {
	var matches []store.Hook
	for i := range hooks {
		h := &hooks[i]
		if h.RoomID != roomID {
			continue
		}
		if strings.EqualFold(h.Label, query) {
			matches = append(matches, *h)
			continue
		}
		if len(query) >= MinRemovePrefix && strings.HasPrefix(h.Token, query) {
			matches = append(matches, *h)
		}
	}
	return matches
}

// MinRemovePrefix is the minimum number of characters required when matching
// by token prefix (chat commands and CLI alike), so a very short or empty
// string can't match every hook.
const MinRemovePrefix = 4

// tokenShortPrefix returns an 8-character prefix of a token for display in
// bot command replies. The full token is never shown in a room.
func tokenShortPrefix(token string) string {
	if len(token) > 8 {
		return token[:8]
	}
	return token
}

// HandleBotMessage processes m.room.message events addressed to the bot as
// commands. Commands are prefixed with the configured command_prefix
// (default "!hook"). Only m.text messages are handled; edits, old events,
// the bot's own messages and puppet messages are ignored.
func (b *Bridge) HandleBotMessage(ctx context.Context, evt *event.Event) {
	if evt.Sender == b.AS.BotMXID() {
		return
	}
	if b.isPuppet(evt.Sender) {
		return
	}
	msg, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok || msg.MsgType != event.MsgText {
		return
	}
	if rel := msg.OptionalGetRelatesTo(); rel != nil && rel.Type == event.RelReplace {
		return
	}
	if age := time.Since(time.UnixMilli(evt.Timestamp)); age > maxCommandAge {
		return
	}
	cmd := parseCommand(msg.Body, b.Cfg.CommandPrefix)
	if cmd == nil {
		return
	}

	if !b.senderCanRunCommands(ctx, evt.Sender, evt.RoomID) {
		b.replyNotice(ctx, evt, "You don't have permission to run hook commands here.")
		return
	}

	switch cmd.Subcommand {
	case "new":
		b.cmdNewHook(ctx, evt, cmd.Args)
	case "list":
		b.cmdListHooks(ctx, evt)
	case "remove":
		b.cmdRemoveHook(ctx, evt, cmd.Args)
	case "help":
		b.replyNotice(ctx, evt, helpText(b.Cfg.CommandPrefix))
	case "":
		b.replyNotice(ctx, evt, helpText(b.Cfg.CommandPrefix))
	default:
		b.replyNotice(ctx, evt, "Unknown subcommand. Try "+b.Cfg.CommandPrefix+" help")
	}
}

// senderCanRunCommands fetches the sender's power level from the homeserver
// and decides whether they may run commands (admin, or power level >=
// command_power_level). Commands are rare, so fetching power levels on each
// command is acceptable.
func (b *Bridge) senderCanRunCommands(ctx context.Context, sender id.UserID, roomID id.RoomID) bool {
	if b.Cfg.IsAdmin(sender) {
		return true
	}
	pl := &event.PowerLevelsEventContent{}
	if err := b.AS.BotIntent().StateEvent(ctx, roomID, event.StatePowerLevels, "", pl); err != nil {
		b.Log.Warn().Err(err).Str("room_id", roomID.String()).Msg("Failed to fetch power levels for command permission")
		return false
	}
	return canRunCommand(false, pl.GetUserLevel(sender), b.Cfg.CommandPowerLevel)
}

func (b *Bridge) cmdNewHook(ctx context.Context, evt *event.Event, label string) {
	hook := &store.Hook{
		Token:     store.NewToken(),
		RoomID:    evt.RoomID,
		Label:     label,
		CreatedBy: evt.Sender.String(),
		CreatedAt: time.Now(),
	}
	if err := b.DB.InsertHook(hook); err != nil {
		b.Log.Warn().Err(err).Msg("Failed to insert hook")
		b.replyNotice(ctx, evt, "Failed to create the hook.")
		return
	}

	hookURL := b.Cfg.WebhookBaseURL() + "/hooks/" + hook.Token
	if err := b.sendDMWithURL(ctx, evt.Sender, evt.RoomID, label, hookURL); err != nil {
		b.Log.Warn().Err(err).Msg("Failed to DM the hook URL; rolling back")
		if delErr := b.DB.DeleteHook(hook.Token); delErr != nil {
			b.Log.Error().Err(delErr).Msg("Failed to delete hook after DM failure")
		}
		b.replyNotice(ctx, evt, "Could not send you the hook URL by DM, so no hook was created. Make sure the bot can DM you.")
		return
	}

	prefix := tokenShortPrefix(hook.Token)
	if label != "" {
		b.replyNotice(ctx, evt, fmt.Sprintf("Hook created: %s (token %s…). I sent you the URL by DM.", label, prefix))
	} else {
		b.replyNotice(ctx, evt, fmt.Sprintf("Hook created (token %s…). I sent you the URL by DM.", prefix))
	}
}

func (b *Bridge) cmdListHooks(ctx context.Context, evt *event.Event) {
	hooks, err := b.DB.ListHooks(evt.RoomID)
	if err != nil {
		b.Log.Warn().Err(err).Msg("Failed to list hooks")
		b.replyNotice(ctx, evt, "Failed to list hooks.")
		return
	}
	if len(hooks) == 0 {
		b.replyNotice(ctx, evt, "No hooks in this room.")
		return
	}
	var lines []string
	for _, h := range hooks {
		date := h.CreatedAt.UTC().Format("2006-01-02")
		prefix := tokenShortPrefix(h.Token)
		if h.Label != "" {
			lines = append(lines, fmt.Sprintf("• %s — token %s… — by %s — %s", h.Label, prefix, h.CreatedBy, date))
		} else {
			lines = append(lines, fmt.Sprintf("• token %s… — by %s — %s", prefix, h.CreatedBy, date))
		}
	}
	b.replyNotice(ctx, evt, strings.Join(lines, "\n"))
}

func (b *Bridge) cmdRemoveHook(ctx context.Context, evt *event.Event, query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		b.replyNotice(ctx, evt, "Usage: "+b.Cfg.CommandPrefix+" remove <label|token-prefix>")
		return
	}
	hooks, err := b.DB.ListHooks(evt.RoomID)
	if err != nil {
		b.Log.Warn().Err(err).Msg("Failed to list hooks for remove")
		b.replyNotice(ctx, evt, "Failed to look up hooks.")
		return
	}
	matches := matchHooksForRemoval(hooks, evt.RoomID, query)
	if len(matches) == 0 {
		b.replyNotice(ctx, evt, "No matching hook in this room.")
		return
	}
	if len(matches) > 1 {
		var lines []string
		lines = append(lines, fmt.Sprintf("%d hooks match; use a more specific label or longer prefix:", len(matches)))
		for _, h := range matches {
			lines = append(lines, fmt.Sprintf("  %s (token %s…)", h.Label, tokenShortPrefix(h.Token)))
		}
		b.replyNotice(ctx, evt, strings.Join(lines, "\n"))
		return
	}
	hook := matches[0]
	if err = b.DB.DeleteHook(hook.Token); err != nil {
		b.Log.Warn().Err(err).Msg("Failed to delete hook")
		b.replyNotice(ctx, evt, "Failed to remove the hook.")
		return
	}
	if hook.Label != "" {
		b.replyNotice(ctx, evt, fmt.Sprintf("Removed hook %s (token %s…).", hook.Label, tokenShortPrefix(hook.Token)))
	} else {
		b.replyNotice(ctx, evt, fmt.Sprintf("Removed hook (token %s…).", tokenShortPrefix(hook.Token)))
	}
}

// replyNotice sends an m.notice reply to the given event in the same room.
func (b *Bridge) replyNotice(ctx context.Context, original *event.Event, text string) {
	content := &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    text,
	}
	content.SetReply(original)
	if _, err := b.AS.BotIntent().SendMessageEvent(ctx, original.RoomID, event.EventMessage, content); err != nil {
		b.Log.Warn().Err(err).Msg("Failed to send bot reply")
	}
}

func helpText(prefix string) string {
	return fmt.Sprintf(`Commands (all operate on hooks in this room only):
  %s new [label]    Create a hook; the URL is sent by DM
  %s list           List hooks (label, token prefix, creator)
  %s remove <label|token-prefix>  Remove a hook
  %s help           Show this help`, prefix, prefix, prefix, prefix)
}
