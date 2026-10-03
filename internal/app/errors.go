package app

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalidCredentials      = errors.New("invalid email or password")
	ErrAccountLocked           = errors.New("account is temporarily locked")
	ErrUserNotFound            = errors.New("user not found")
	ErrTokenNotFound           = errors.New("token not found")
	ErrTokenRevoked            = errors.New("token has been revoked")
	ErrTokenExpired            = errors.New("token has expired")
	ErrTokenInvalid            = errors.New("token is invalid")
	ErrTokenUsed               = errors.New("token has already been used")
	ErrEmailAlreadyVerified    = errors.New("email is already verified")
	ErrForbidden               = errors.New("access denied")
	ErrRoomNotFound            = errors.New("room not found")
	ErrRoomNotJoinable         = errors.New("room is not waiting")
	ErrAlreadyParticipant      = errors.New("user is already a participant")
	ErrRoomFull                = errors.New("room is full")
	ErrNotHost                 = errors.New("caller is not the host")
	ErrParticipantNotFound     = errors.New("participant not found")
	ErrParticipantReady        = errors.New("participant is ready")
	ErrNotEnoughParticipants   = errors.New("not enough participants")
	ErrNotAllReady             = errors.New("not all participants are ready")
	ErrInvalidRaceType         = errors.New("invalid race type")
	ErrInvalidStatusTransition = errors.New("invalid room status transition")
	ErrAlreadyFriends          = errors.New("users are already friends")
	ErrRequestPending          = errors.New("friend request is already pending")
	ErrCooldownActive          = errors.New("friend request cooldown is active")
	ErrNoSuchRequest           = errors.New("pending friend request not found")
	ErrNotRecipient            = errors.New("only the recipient may decide a friend request")
)

// RateLimitedError is returned when an operation is attempted too frequently.
// RetryAt is the earliest time the caller may try again.
type RateLimitedError struct {
	RetryAt time.Time
}

func (e RateLimitedError) Error() string {
	return fmt.Sprintf("rate limited: retry after %s", e.RetryAt.Format(time.RFC3339))
}
