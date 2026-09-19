package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	OverlayEventSchemaVersion = "1"
	MaxOverlayEventJSONBytes  = 64 * 1024
	maxEventIdentifierLength  = 256
	maxEventTextLength        = 10_000
	maxEventFragments         = 1_000
	maxPlatformExtraFields    = 32
	eventStringSchemaPattern  = `^[^\u0000-\u0020\u007F-\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000\uFEFF](?:[^\u0000-\u001F\u007F-\u009F\u2028\u2029]*[^\u0000-\u0020\u007F-\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000\uFEFF])?$`
)

//go:embed schemas/overlay-event-v1.schema.json
var overlayEventSchemaV1 []byte

// Platform identifies the source of an event. Test is a synthetic source and
// never grants access to a platform connection.
type Platform string

const (
	PlatformYouTube Platform = "youtube"
	PlatformTwitch  Platform = "twitch"
	PlatformTest    Platform = "test"
)

// EventType is the stable event name exposed to trigger and overlay code.
type EventType string

const (
	EventTypeComment             EventType = "comment"
	EventTypePaidMessage         EventType = "paid_message"
	EventTypePaidSticker         EventType = "paid_sticker"
	EventTypeBits                EventType = "bits"
	EventTypeMembership          EventType = "membership"
	EventTypeMembershipGift      EventType = "membership_gift"
	EventTypeRaid                EventType = "raid"
	EventTypeFollow              EventType = "follow"
	EventTypeChannelPoints       EventType = "channel_points"
	EventTypeCommentDeleted      EventType = "comment_deleted"
	EventTypeUserCommentsCleared EventType = "user_comments_cleared"
	EventTypeChatCleared         EventType = "chat_cleared"
	EventTypeStreamStarted       EventType = "stream_started"
	EventTypeStreamEnded         EventType = "stream_ended"
)

var (
	validPlatforms = map[Platform]struct{}{
		PlatformYouTube: {},
		PlatformTwitch:  {},
		PlatformTest:    {},
	}
	validEventTypes = map[EventType]struct{}{
		EventTypeComment:             {},
		EventTypePaidMessage:         {},
		EventTypePaidSticker:         {},
		EventTypeBits:                {},
		EventTypeMembership:          {},
		EventTypeMembershipGift:      {},
		EventTypeRaid:                {},
		EventTypeFollow:              {},
		EventTypeChannelPoints:       {},
		EventTypeCommentDeleted:      {},
		EventTypeUserCommentsCleared: {},
		EventTypeChatCleared:         {},
		EventTypeStreamStarted:       {},
		EventTypeStreamEnded:         {},
	}
	extraFieldPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
)

// StreamInfo identifies the public broadcast context without exposing the
// original URL or authenticated account.
type StreamInfo struct {
	ID          string `json:"id"`
	ChannelID   string `json:"channelId,omitempty"`
	ChannelName string `json:"channelName,omitempty"`
}

type AuthorBadge struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ImageURL string `json:"imageUrl,omitempty"`
}

type AuthorInfo struct {
	ID                   string        `json:"id"`
	DisplayName          string        `json:"displayName"`
	Login                string        `json:"login,omitempty"`
	ImageURL             string        `json:"imageUrl,omitempty"`
	Badges               []AuthorBadge `json:"badges"`
	IsBroadcaster        bool          `json:"isBroadcaster"`
	IsModerator          bool          `json:"isModerator"`
	IsMemberOrSubscriber bool          `json:"isMemberOrSubscriber"`
	Color                string        `json:"color,omitempty"`
}

type MessageFragmentType string

const (
	MessageFragmentText  MessageFragmentType = "text"
	MessageFragmentEmoji MessageFragmentType = "emoji"
	MessageFragmentEmote MessageFragmentType = "emote"
)

type EmoteInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	ImageURL    string `json:"imageUrl"`
}

type MessageFragment struct {
	Type  MessageFragmentType `json:"type"`
	Text  string              `json:"text"`
	Emote *EmoteInfo          `json:"emote,omitempty"`
}

type MessageInfo struct {
	Text        string            `json:"text"`
	Fragments   []MessageFragment `json:"fragments"`
	PublishedAt time.Time         `json:"publishedAt"`
	Deleted     bool              `json:"deleted"`
}

// OverlayEvent is the versioned event envelope shared by platform adapters,
// the trigger engine, and the Overlay SDK.
type OverlayEvent struct {
	SchemaVersion string         `json:"schemaVersion"`
	ID            string         `json:"id"`
	Platform      Platform       `json:"platform"`
	Type          EventType      `json:"type"`
	OccurredAt    time.Time      `json:"occurredAt"`
	ReceivedAt    time.Time      `json:"receivedAt"`
	Replayed      bool           `json:"replayed"`
	Test          bool           `json:"test"`
	Stream        StreamInfo     `json:"stream"`
	Author        *AuthorInfo    `json:"author,omitempty"`
	Message       *MessageInfo   `json:"message,omitempty"`
	Payload       any            `json:"payload"`
	PlatformExtra map[string]any `json:"platformExtra,omitempty"`
}

// PlatformEvent contains already selected platform fields. Raw API responses,
// credentials, cookies, and headers must never be placed in this value.
type PlatformEvent struct {
	ID            string
	Type          EventType
	OccurredAt    time.Time
	ReceivedAt    time.Time
	Replayed      bool
	Test          bool
	Stream        StreamInfo
	Author        *AuthorInfo
	Message       *MessageInfo
	Payload       any
	PlatformExtra map[string]any
}

// PlatformAdapter is the trust boundary between a platform-specific client
// and the stable event contract.
type PlatformAdapter interface {
	Platform() Platform
	Normalize(PlatformEvent) (OverlayEvent, error)
}

type allowlistPlatformAdapter struct {
	platform      Platform
	allowedExtras map[string]struct{}
	now           func() time.Time
}

// NewPlatformAdapter creates an adapter that rejects platformExtra fields not
// explicitly selected by the caller.
func NewPlatformAdapter(platform Platform, allowedExtraFields ...string) (PlatformAdapter, error) {
	return newPlatformAdapter(platform, time.Now, allowedExtraFields...)
}

func newPlatformAdapter(platform Platform, now func() time.Time, allowedExtraFields ...string) (PlatformAdapter, error) {
	if _, ok := validPlatforms[platform]; !ok {
		return nil, fmt.Errorf("unsupported platform %q", platform)
	}
	if now == nil {
		return nil, errors.New("adapter clock is required")
	}
	allowed := make(map[string]struct{}, len(allowedExtraFields))
	for _, field := range allowedExtraFields {
		if !extraFieldPattern.MatchString(field) {
			return nil, fmt.Errorf("invalid platformExtra field %q", field)
		}
		allowed[field] = struct{}{}
	}
	if len(allowed) > maxPlatformExtraFields {
		return nil, fmt.Errorf("platformExtra allowlist exceeds %d fields", maxPlatformExtraFields)
	}
	return &allowlistPlatformAdapter{platform: platform, allowedExtras: allowed, now: now}, nil
}

func (a *allowlistPlatformAdapter) Platform() Platform {
	return a.platform
}

func (a *allowlistPlatformAdapter) Normalize(input PlatformEvent) (OverlayEvent, error) {
	for field := range input.PlatformExtra {
		if _, ok := a.allowedExtras[field]; !ok {
			return OverlayEvent{}, fmt.Errorf("platformExtra field %q is not allowed", field)
		}
	}
	payload, err := cloneJSONValue(input.Payload)
	if err != nil {
		return OverlayEvent{}, fmt.Errorf("invalid payload: %w", err)
	}
	extra, err := cloneJSONObject(input.PlatformExtra)
	if err != nil {
		return OverlayEvent{}, fmt.Errorf("invalid platformExtra: %w", err)
	}
	receivedAt := input.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = a.now()
	}
	event := OverlayEvent{
		SchemaVersion: OverlayEventSchemaVersion,
		ID:            input.ID,
		Platform:      a.platform,
		Type:          input.Type,
		OccurredAt:    input.OccurredAt.UTC(),
		ReceivedAt:    receivedAt.UTC(),
		Replayed:      input.Replayed,
		Test:          input.Test,
		Stream:        input.Stream,
		Author:        cloneAuthor(input.Author),
		Message:       cloneMessage(input.Message),
		Payload:       payload,
		PlatformExtra: extra,
	}
	if err := event.Validate(); err != nil {
		return OverlayEvent{}, err
	}
	return event, nil
}

// Validate enforces the stable contract before an event crosses into shared
// queues or the Overlay SDK.
func (e OverlayEvent) Validate() error {
	if e.SchemaVersion != OverlayEventSchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %q", e.SchemaVersion)
	}
	if err := validateIdentifier("event id", e.ID); err != nil {
		return err
	}
	if _, ok := validPlatforms[e.Platform]; !ok {
		return fmt.Errorf("unsupported platform %q", e.Platform)
	}
	if _, ok := validEventTypes[e.Type]; !ok {
		return fmt.Errorf("unsupported event type %q", e.Type)
	}
	if e.OccurredAt.IsZero() || e.ReceivedAt.IsZero() {
		return errors.New("occurredAt and receivedAt are required")
	}
	if err := validateIdentifier("stream id", e.Stream.ID); err != nil {
		return err
	}
	if err := validateOptionalString("channel id", e.Stream.ChannelID, maxEventIdentifierLength); err != nil {
		return err
	}
	if err := validateOptionalString("channel name", e.Stream.ChannelName, maxEventIdentifierLength); err != nil {
		return err
	}
	if err := validateAuthor(e.Author); err != nil {
		return err
	}
	if err := validateMessage(e.Message); err != nil {
		return err
	}
	if len(e.PlatformExtra) > maxPlatformExtraFields {
		return fmt.Errorf("platformExtra exceeds %d fields", maxPlatformExtraFields)
	}
	for field := range e.PlatformExtra {
		if !extraFieldPattern.MatchString(field) {
			return fmt.Errorf("invalid platformExtra field %q", field)
		}
	}
	encoded, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("event is not JSON compatible: %w", err)
	}
	if len(encoded) > MaxOverlayEventJSONBytes {
		return fmt.Errorf("event exceeds %d bytes", MaxOverlayEventJSONBytes)
	}
	return nil
}

// OverlayEventSchema returns a copy so callers cannot mutate the embedded
// schema used by another connection.
func OverlayEventSchema() []byte {
	return bytes.Clone(overlayEventSchemaV1)
}

func validateAuthor(author *AuthorInfo) error {
	if author == nil {
		return nil
	}
	if err := validateIdentifier("author id", author.ID); err != nil {
		return err
	}
	if err := validateRequiredString("author display name", author.DisplayName, maxEventIdentifierLength); err != nil {
		return err
	}
	if err := validateOptionalString("author login", author.Login, maxEventIdentifierLength); err != nil {
		return err
	}
	if err := validateLocalMediaURL("author image", author.ImageURL); err != nil {
		return err
	}
	if author.Badges == nil {
		return errors.New("author badges must be an array")
	}
	if len(author.Badges) > 100 {
		return errors.New("author badges exceed 100 items")
	}
	for _, badge := range author.Badges {
		if err := validateIdentifier("badge id", badge.ID); err != nil {
			return err
		}
		if err := validateRequiredString("badge name", badge.Name, maxEventIdentifierLength); err != nil {
			return err
		}
		if err := validateLocalMediaURL("badge image", badge.ImageURL); err != nil {
			return err
		}
	}
	return validateOptionalString("author color", author.Color, 64)
}

func validateMessage(message *MessageInfo) error {
	if message == nil {
		return nil
	}
	if utf8.RuneCountInString(message.Text) > maxEventTextLength {
		return fmt.Errorf("message text exceeds %d characters", maxEventTextLength)
	}
	if message.PublishedAt.IsZero() {
		return errors.New("message publishedAt is required")
	}
	if message.Fragments == nil {
		return errors.New("message fragments must be an array")
	}
	if len(message.Fragments) > maxEventFragments {
		return fmt.Errorf("message fragments exceed %d items", maxEventFragments)
	}
	for _, fragment := range message.Fragments {
		if fragment.Type != MessageFragmentText && fragment.Type != MessageFragmentEmoji && fragment.Type != MessageFragmentEmote {
			return fmt.Errorf("unsupported message fragment type %q", fragment.Type)
		}
		if utf8.RuneCountInString(fragment.Text) > maxEventTextLength {
			return fmt.Errorf("message fragment exceeds %d characters", maxEventTextLength)
		}
		if fragment.Type == MessageFragmentEmote && fragment.Emote == nil {
			return errors.New("emote fragment requires emote metadata")
		}
		if fragment.Type != MessageFragmentEmote && fragment.Emote != nil {
			return errors.New("only emote fragments may include emote metadata")
		}
		if fragment.Emote != nil {
			if err := validateIdentifier("emote id", fragment.Emote.ID); err != nil {
				return err
			}
			if err := validateRequiredString("emote display name", fragment.Emote.DisplayName, maxEventIdentifierLength); err != nil {
				return err
			}
			if fragment.Emote.ImageURL == "" {
				return errors.New("emote image is required")
			}
			if err := validateLocalMediaURL("emote image", fragment.Emote.ImageURL); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateIdentifier(name, value string) error {
	return validateRequiredString(name, value, maxEventIdentifierLength)
}

func validateRequiredString(name, value string, limit int) error {
	if value == "" || !utf8.ValidString(value) {
		return fmt.Errorf("%s is required and must be valid UTF-8", name)
	}
	if strings.IndexFunc(value, func(character rune) bool {
		return isEventStringControl(character)
	}) >= 0 {
		return fmt.Errorf("%s must not contain control or line-separator characters", name)
	}
	first, _ := utf8.DecodeRuneInString(value)
	last, _ := utf8.DecodeLastRuneInString(value)
	if unicode.IsSpace(first) || unicode.IsSpace(last) || first == '\ufeff' || last == '\ufeff' {
		return fmt.Errorf("%s must not have surrounding whitespace", name)
	}
	if utf8.RuneCountInString(value) > limit {
		return fmt.Errorf("%s exceeds %d characters", name, limit)
	}
	return nil
}

func validateOptionalString(name, value string, limit int) error {
	if value == "" {
		return nil
	}
	return validateRequiredString(name, value, limit)
}

func validateLocalMediaURL(name, value string) error {
	if value == "" {
		return nil
	}
	if value == "/" || utf8.RuneCountInString(value) > 2_048 || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\?#") || strings.IndexFunc(value, func(character rune) bool {
		return character <= 0x1f || character == 0x7f
	}) >= 0 {
		return fmt.Errorf("%s must be a local absolute path without backslash, control character, query, or fragment", name)
	}
	return nil
}

func isEventStringControl(character rune) bool {
	return character <= 0x1f || (character >= 0x7f && character <= 0x9f) || character == '\u2028' || character == '\u2029'
}

func cloneAuthor(author *AuthorInfo) *AuthorInfo {
	if author == nil {
		return nil
	}
	clone := *author
	clone.Badges = make([]AuthorBadge, len(author.Badges))
	copy(clone.Badges, author.Badges)
	return &clone
}

func cloneMessage(message *MessageInfo) *MessageInfo {
	if message == nil {
		return nil
	}
	clone := *message
	clone.Fragments = make([]MessageFragment, len(message.Fragments))
	copy(clone.Fragments, message.Fragments)
	for index := range clone.Fragments {
		if clone.Fragments[index].Emote != nil {
			emote := *clone.Fragments[index].Emote
			clone.Fragments[index].Emote = &emote
		}
	}
	clone.PublishedAt = clone.PublishedAt.UTC()
	return &clone
}

func cloneJSONObject(value map[string]any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	clone := make(map[string]any, len(value))
	for key, item := range value {
		copied, err := cloneJSONValue(item)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
		clone[key] = copied
	}
	return clone, nil
}

func cloneJSONValue(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var clone any
	if err := decoder.Decode(&clone); err != nil {
		return nil, err
	}
	return clone, nil
}
