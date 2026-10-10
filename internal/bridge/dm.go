package bridge

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// sendDMWithURL sends the full hook URL to the user by DM. It reuses a stored
// DM room if the bot is still joined, the user is joined or invited, and the
// room is not encrypted; otherwise it creates a new unencrypted DM room and
// stores it. Works for users on other homeservers too.
func (b *Bridge) sendDMWithURL(ctx context.Context, userID id.UserID, sourceRoom id.RoomID, label, hookURL string) error {
	roomID, err := b.ensureDM(ctx, userID)
	if err != nil {
		return fmt.Errorf("ensure DM: %w", err)
	}
	text := fmt.Sprintf("Webhook URL for room %s", sourceRoom)
	if label != "" {
		text += fmt.Sprintf(" (label: %s)", label)
	}
	text += ":\n" + hookURL
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    text,
	}
	if _, err = b.AS.BotIntent().SendMessageEvent(ctx, roomID, event.EventMessage, content); err != nil {
		return fmt.Errorf("send DM: %w", err)
	}
	return nil
}

// ensureDM returns a DM room ID for the user, reusing a stored one if still
// valid, or creating a new unencrypted DM room.
func (b *Bridge) ensureDM(ctx context.Context, userID id.UserID) (id.RoomID, error) {
	existing, err := b.DB.GetDM(userID)
	if err != nil {
		return "", fmt.Errorf("get DM: %w", err)
	}
	if existing != "" && b.dmValid(ctx, existing, userID) {
		return existing, nil
	}
	roomID, err := b.createDM(ctx, userID)
	if err != nil {
		return "", err
	}
	if err = b.DB.SetDM(userID, roomID); err != nil {
		return "", fmt.Errorf("store DM: %w", err)
	}
	return roomID, nil
}

// dmValid reports whether the stored DM room is still usable: the bot is
// joined, the user is joined or invited, and the room is not encrypted.
func (b *Bridge) dmValid(ctx context.Context, roomID id.RoomID, userID id.UserID) bool {
	bot := b.AS.BotIntent()
	resp, err := bot.JoinedRooms(ctx)
	if err != nil {
		b.Log.Warn().Err(err).Msg("Failed to list joined rooms for DM check")
		return false
	}
	botJoined := false
	for _, r := range resp.JoinedRooms {
		if r == roomID {
			botJoined = true
			break
		}
	}
	if !botJoined {
		return false
	}
	if !b.AS.StateStore.IsInRoom(ctx, roomID, userID) && !b.AS.StateStore.IsInvited(ctx, roomID, userID) {
		return false
	}
	enc := &event.EncryptionEventContent{}
	if err := bot.StateEvent(ctx, roomID, event.StateEncryption, "", enc); err == nil && enc.Algorithm != "" {
		return false
	}
	return true
}

// createDM creates a new unencrypted DM room with the user and returns its
// room ID.
func (b *Bridge) createDM(ctx context.Context, userID id.UserID) (id.RoomID, error) {
	resp, err := b.AS.BotIntent().CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Preset:   "trusted_private_chat",
		IsDirect: true,
		Invite:   []id.UserID{userID},
	})
	if err != nil {
		return "", fmt.Errorf("create DM room: %w", err)
	}
	return resp.RoomID, nil
}
