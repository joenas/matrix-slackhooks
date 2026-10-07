package store

import (
	"context"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// StateStore implements appservice.StateStore on top of SQLite for the
// membership and registration caches, falling back to the in-memory store
// from mautrix for the rest of the room state.
type StateStore struct {
	*mautrix.MemoryStateStore
	db *DB
}

func NewStateStore(db *DB) *StateStore {
	mem, _ := mautrix.NewMemoryStateStore().(*mautrix.MemoryStateStore)
	return &StateStore{
		MemoryStateStore: mem,
		db:               db,
	}
}

// The membership cache is keyed by full user ID (@localpart:server), so
// users with the same localpart on different servers never collide.

func (s *StateStore) IsMembership(ctx context.Context, roomID id.RoomID, userID id.UserID, allowed ...event.Membership) bool {
	membership, err := s.db.GetMembership(userID, roomID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).
			Str("room_id", roomID.String()).
			Str("user_id", userID.String()).
			Msg("Failed to look up membership cache")
		return false
	}
	for _, allowedMembership := range allowed {
		if string(allowedMembership) == membership {
			return true
		}
	}
	return false
}

func (s *StateStore) IsInRoom(ctx context.Context, roomID id.RoomID, userID id.UserID) bool {
	return s.IsMembership(ctx, roomID, userID, event.MembershipJoin)
}

func (s *StateStore) IsInvited(ctx context.Context, roomID id.RoomID, userID id.UserID) bool {
	return s.IsMembership(ctx, roomID, userID, event.MembershipJoin, event.MembershipInvite)
}

func (s *StateStore) SetMembership(ctx context.Context, roomID id.RoomID, userID id.UserID, membership event.Membership) error {
	return s.db.SetMembership(userID, roomID, string(membership))
}

func (s *StateStore) SetMember(ctx context.Context, roomID id.RoomID, userID id.UserID, member *event.MemberEventContent) error {
	if member != nil {
		if err := s.db.SetMembership(userID, roomID, string(member.Membership)); err != nil {
			return err
		}
	}
	return s.MemoryStateStore.SetMember(ctx, roomID, userID, member)
}

func (s *StateStore) ForgetMembership(ctx context.Context, roomID id.RoomID, userID id.UserID) error {
	return s.db.DeleteMembership(userID, roomID)
}
