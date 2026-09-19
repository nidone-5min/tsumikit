package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func validPlatformEvent() PlatformEvent {
	occurredAt := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	return PlatformEvent{
		ID:         "message-1",
		Type:       EventTypeComment,
		OccurredAt: occurredAt,
		Stream:     StreamInfo{ID: "stream-1", ChannelID: "channel-1", ChannelName: "配信者"},
		Author: &AuthorInfo{
			ID:          "author-1",
			DisplayName: "視聴者",
			Login:       "viewer",
			ImageURL:    "/media/author-1",
			Badges:      []AuthorBadge{{ID: "moderator-1", Name: "moderator", ImageURL: "/media/badge-1"}},
			IsModerator: true,
		},
		Message: &MessageInfo{
			Text:        "hello Kappa",
			PublishedAt: occurredAt,
			Fragments: []MessageFragment{
				{Type: MessageFragmentText, Text: "hello "},
				{Type: MessageFragmentEmote, Text: "Kappa", Emote: &EmoteInfo{ID: "25", DisplayName: "Kappa", ImageURL: "/media/emote-25"}},
			},
		},
		Payload:       map[string]any{"kind": "comment"},
		PlatformExtra: map[string]any{"membershipLevel": "member"},
	}
}

func TestPlatformAdapterNormalizesAndCopiesEvent(t *testing.T) {
	receivedAt := time.Date(2026, time.September, 8, 3, 0, 1, 0, time.UTC)
	adapter, err := newPlatformAdapter(PlatformYouTube, func() time.Time { return receivedAt }, "membershipLevel")
	if err != nil {
		t.Fatalf("newPlatformAdapter(): %v", err)
	}
	input := validPlatformEvent()
	event, err := adapter.Normalize(input)
	if err != nil {
		t.Fatalf("Normalize(): %v", err)
	}
	if event.SchemaVersion != OverlayEventSchemaVersion || event.Platform != PlatformYouTube || !event.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("normalized envelope = %#v", event)
	}
	if event.OccurredAt.Location() != time.UTC || event.Message.PublishedAt.Location() != time.UTC {
		t.Fatal("timestamps were not normalized to UTC")
	}

	input.Author.Badges[0].Name = "changed"
	input.Message.Fragments[1].Emote.DisplayName = "changed"
	input.Payload.(map[string]any)["kind"] = "changed"
	input.PlatformExtra["membershipLevel"] = "changed"
	if event.Author.Badges[0].Name != "moderator" || event.Message.Fragments[1].Emote.DisplayName != "Kappa" {
		t.Fatal("nested author or message data aliases the platform input")
	}
	if event.Payload.(map[string]any)["kind"] != "comment" || event.PlatformExtra["membershipLevel"] != "member" {
		t.Fatal("payload or platformExtra aliases the platform input")
	}
}

func TestPlatformAdapterEmitsEmptyArraysInsteadOfNull(t *testing.T) {
	adapter, err := NewPlatformAdapter(PlatformYouTube)
	if err != nil {
		t.Fatalf("NewPlatformAdapter(): %v", err)
	}
	for _, empty := range []bool{false, true} {
		input := validPlatformEvent()
		input.PlatformExtra = nil
		input.Author.Badges = nil
		input.Message.Fragments = nil
		if empty {
			input.Author.Badges = []AuthorBadge{}
			input.Message.Fragments = []MessageFragment{}
		}
		event, err := adapter.Normalize(input)
		if err != nil {
			t.Fatalf("Normalize(empty=%t): %v", empty, err)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("json.Marshal(): %v", err)
		}
		if strings.Contains(string(encoded), `"badges":null`) || strings.Contains(string(encoded), `"fragments":null`) {
			t.Fatalf("normalized arrays must not be null: %s", encoded)
		}
	}
}

func TestPlatformAdapterRejectsUnknownExtraAndNonJSONPayload(t *testing.T) {
	adapter, err := NewPlatformAdapter(PlatformTwitch, "allowed")
	if err != nil {
		t.Fatalf("NewPlatformAdapter(): %v", err)
	}
	input := validPlatformEvent()
	input.PlatformExtra = map[string]any{"token": "must-not-pass"}
	if _, err := adapter.Normalize(input); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("Normalize() error = %v, want allowlist rejection", err)
	}
	input.PlatformExtra = nil
	input.Payload = math.Inf(1)
	if _, err := adapter.Normalize(input); err == nil || !strings.Contains(err.Error(), "invalid payload") {
		t.Fatalf("Normalize() error = %v, want JSON rejection", err)
	}
}

func TestPlatformAdapterRejectsInvalidConfiguration(t *testing.T) {
	for _, platform := range []Platform{"", "unknown"} {
		if _, err := NewPlatformAdapter(platform); err == nil {
			t.Errorf("NewPlatformAdapter(%q) succeeded", platform)
		}
	}
	if _, err := NewPlatformAdapter(PlatformYouTube, "bad field"); err == nil {
		t.Fatal("NewPlatformAdapter() accepted an invalid extra field")
	}
	if adapter, err := NewPlatformAdapter(PlatformTest); err != nil || adapter.Platform() != PlatformTest {
		t.Fatalf("NewPlatformAdapter(test) = (%v, %v)", adapter, err)
	}
}

func TestOverlayEventValidationRejectsInvalidBoundaries(t *testing.T) {
	adapter, err := NewPlatformAdapter(PlatformYouTube, "membershipLevel")
	if err != nil {
		t.Fatalf("NewPlatformAdapter(): %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*PlatformEvent)
	}{
		{name: "missing id", mutate: func(event *PlatformEvent) { event.ID = "" }},
		{name: "unknown type", mutate: func(event *PlatformEvent) { event.Type = "unknown" }},
		{name: "missing occurred time", mutate: func(event *PlatformEvent) { event.OccurredAt = time.Time{} }},
		{name: "missing stream", mutate: func(event *PlatformEvent) { event.Stream.ID = "" }},
		{name: "remote author image", mutate: func(event *PlatformEvent) { event.Author.ImageURL = "https://example.com/avatar.png" }},
		{name: "network path author image", mutate: func(event *PlatformEvent) { event.Author.ImageURL = `/\\evil.example/avatar.png` }},
		{name: "control character author image", mutate: func(event *PlatformEvent) { event.Author.ImageURL = "/\t/evil.example/avatar.png" }},
		{name: "control character in identifier", mutate: func(event *PlatformEvent) { event.ID = "message\n1" }},
		{name: "leading unicode whitespace", mutate: func(event *PlatformEvent) { event.Author.DisplayName = "\u3000viewer" }},
		{name: "trailing byte order mark", mutate: func(event *PlatformEvent) { event.Stream.ChannelName = "channel\ufeff" }},
		{name: "missing emote image", mutate: func(event *PlatformEvent) { event.Message.Fragments[1].Emote.ImageURL = "" }},
		{name: "empty local media path", mutate: func(event *PlatformEvent) { event.Author.ImageURL = "/" }},
		{name: "emote metadata on text", mutate: func(event *PlatformEvent) {
			event.Message.Fragments[0].Emote = &EmoteInfo{ID: "1", DisplayName: "bad", ImageURL: "/media/1"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validPlatformEvent()
			test.mutate(&input)
			if _, err := adapter.Normalize(input); err == nil {
				t.Fatal("Normalize() succeeded")
			}
		})
	}
}

func TestOverlayEventValidationRejectsNullCollections(t *testing.T) {
	adapter, err := NewPlatformAdapter(PlatformYouTube, "membershipLevel")
	if err != nil {
		t.Fatalf("NewPlatformAdapter(): %v", err)
	}
	event, err := adapter.Normalize(validPlatformEvent())
	if err != nil {
		t.Fatalf("Normalize(): %v", err)
	}
	event.Author.Badges = nil
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "badges must be an array") {
		t.Fatalf("Validate() error = %v, want null badges rejection", err)
	}
	event, err = adapter.Normalize(validPlatformEvent())
	if err != nil {
		t.Fatalf("Normalize(): %v", err)
	}
	event.Message.Fragments = nil
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "fragments must be an array") {
		t.Fatalf("Validate() error = %v, want null fragments rejection", err)
	}
}

func TestOverlayEventValidationEnforcesEncodedSize(t *testing.T) {
	adapter, err := NewPlatformAdapter(PlatformTwitch)
	if err != nil {
		t.Fatalf("NewPlatformAdapter(): %v", err)
	}
	input := validPlatformEvent()
	input.PlatformExtra = nil
	input.Payload = strings.Repeat("x", MaxOverlayEventJSONBytes)
	if _, err := adapter.Normalize(input); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Normalize() error = %v, want size rejection", err)
	}
}

func TestOverlayEventJSONAndSchemaVersion(t *testing.T) {
	adapter, err := NewPlatformAdapter(PlatformYouTube, "membershipLevel")
	if err != nil {
		t.Fatalf("NewPlatformAdapter(): %v", err)
	}
	event, err := adapter.Normalize(validPlatformEvent())
	if err != nil {
		t.Fatalf("Normalize(): %v", err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("json.Unmarshal(event): %v", err)
	}
	if document["schemaVersion"] != OverlayEventSchemaVersion || document["platform"] != string(PlatformYouTube) {
		t.Fatalf("event JSON = %s", encoded)
	}

	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(OverlayEventSchema(), &schema); err != nil {
		t.Fatalf("json.Unmarshal(schema): %v", err)
	}
	var schemaVersion struct {
		Const string `json:"const"`
	}
	if err := json.Unmarshal(schema.Properties["schemaVersion"], &schemaVersion); err != nil {
		t.Fatalf("json.Unmarshal(schemaVersion): %v", err)
	}
	if schemaVersion.Const != OverlayEventSchemaVersion {
		t.Fatalf("schema version = %q", schemaVersion.Const)
	}
	var identifierSchema struct {
		Pattern string `json:"pattern"`
	}
	var fullSchema struct {
		Definitions map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(OverlayEventSchema(), &fullSchema); err != nil {
		t.Fatalf("json.Unmarshal(schema definitions): %v", err)
	}
	if err := json.Unmarshal(fullSchema.Definitions["identifier"], &identifierSchema); err != nil {
		t.Fatalf("json.Unmarshal(identifier schema): %v", err)
	}
	if identifierSchema.Pattern != eventStringSchemaPattern {
		t.Errorf("identifier pattern = %q, want shared contract pattern %q", identifierSchema.Pattern, eventStringSchemaPattern)
	}
	copyOfSchema := OverlayEventSchema()
	copyOfSchema[0] = 'x'
	if OverlayEventSchema()[0] != '{' {
		t.Fatal("OverlayEventSchema returned mutable shared storage")
	}
}
