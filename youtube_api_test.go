package main

import (
	"net/http"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestParseYouTubeVideoID(t *testing.T) {
	t.Parallel()
	want := "dQw4w9WgXcQ"
	for _, rawURL := range []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com/live/dQw4w9WgXcQ?feature=share",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ&list=ignored",
		"https://youtu.be/dQw4w9WgXcQ?t=1",
	} {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			if got, err := parseYouTubeVideoID(rawURL); err != nil || got != want {
				t.Fatalf("parseYouTubeVideoID() = %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestParseYouTubeVideoIDRejectsUntrustedURLs(t *testing.T) {
	t.Parallel()
	for _, rawURL := range []string{
		"http://youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com.evil.example/watch?v=dQw4w9WgXcQ",
		"https://youtube.com:8443/watch?v=dQw4w9WgXcQ",
		"https://user@youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com/watch?v=../../secret",
		"dQw4w9WgXcQ",
	} {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			if _, err := parseYouTubeVideoID(rawURL); err == nil {
				t.Fatalf("parseYouTubeVideoID(%q) returned nil error", rawURL)
			}
		})
	}
}

func TestRetryableYouTubeErrorHonoursRetryAfter(t *testing.T) {
	t.Parallel()
	err := &googleapi.Error{
		Code:   http.StatusForbidden,
		Header: http.Header{"Retry-After": []string{"12"}},
		Errors: []googleapi.ErrorItem{{Reason: "rateLimitExceeded"}},
	}
	retryable, delay := retryableYouTubeError(err)
	if !retryable || delay != 12*time.Second {
		t.Fatalf("retryableYouTubeError() = %v, %v; want true, 12s", retryable, delay)
	}
}

func TestYouTubeGRPCDescriptorDecodesStreamResponse(t *testing.T) {
	t.Parallel()
	descriptors, err := youtubeGRPCDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	if got := descriptors.response.Fields().ByName("next_page_token").Number(); got != 100602 {
		t.Fatalf("next_page_token field number = %d, want 100602", got)
	}

	response := dynamicpb.NewMessage(descriptors.response)
	setDynamicString(response, "next_page_token", "resume-token")
	items := response.Mutable(descriptors.response.Fields().ByName("items")).List()
	itemValue := items.NewElement()
	item := itemValue.Message()
	setReflectString(item, "id", "message-id")
	snippetField := descriptors.message.Fields().ByName("snippet")
	snippet := item.Mutable(snippetField).Message()
	setReflectString(snippet, "display_message", "hello")
	setReflectString(snippet, "published_at", "2026-09-07T00:00:00Z")
	typeField := descriptors.snippet.Fields().ByName("type")
	snippet.Set(typeField, protoreflect.ValueOfInt32(1))
	authorField := descriptors.message.Fields().ByName("author_details")
	author := item.Mutable(authorField).Message()
	setReflectString(author, "display_name", "viewer")
	items.Append(itemValue)

	page := youtubePageFromGRPC(response, descriptors)
	if page.nextPageToken != "resume-token" || len(page.messages) != 1 {
		t.Fatalf("decoded page = %#v", page)
	}
	message := page.messages[0]
	if message.id != "message-id" || message.author != "viewer" || message.text != "hello" || message.messageType != "textMessageEvent" {
		t.Fatalf("decoded message = %#v", message)
	}
}

func setReflectString(message protoreflect.Message, name protoreflect.Name, value string) {
	field := message.Descriptor().Fields().ByName(name)
	message.Set(field, protoreflect.ValueOfString(value))
}
