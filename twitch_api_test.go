package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateTwitchClientID(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "valid", value: "abcdefghijklmnopqrstuv12345678"},
		{name: "too short", value: "short", wantErr: true},
		{name: "uppercase", value: "ABCDEFGHIJKLMNOPQRSTUV12345678", wantErr: true},
		{name: "whitespace", value: " abcdefghijklmnopqrstuv12345678", wantErr: true},
		{name: "separator", value: "abcdefghijklmnopqrstuv-1234567", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := validateTwitchClientID(test.value); (err != nil) != test.wantErr {
				t.Fatalf("validateTwitchClientID(%q) error = %v, wantErr %v", test.value, err, test.wantErr)
			}
		})
	}
}

func TestParseTwitchChannelLogin(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "www", value: "https://www.twitch.tv/TwitchDev", want: "twitchdev"},
		{name: "mobile trailing slash", value: "https://m.twitch.tv/some_channel/", want: "some_channel"},
		{name: "foreign host", value: "https://twitch.tv.example/channel", wantErr: true},
		{name: "credentials", value: "https://user@twitch.tv/channel", wantErr: true},
		{name: "extra path", value: "https://twitch.tv/channel/videos", wantErr: true},
		{name: "query", value: "https://twitch.tv/channel?token=secret", wantErr: true},
		{name: "port", value: "https://twitch.tv:8443/channel", wantErr: true},
		{name: "http", value: "http://twitch.tv/channel", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseTwitchChannelLogin(test.value)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("parseTwitchChannelLogin(%q) = %q, %v; want %q, wantErr %v", test.value, got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestValidateTwitchEventSubURL(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value   string
		wantErr bool
	}{
		{value: "wss://eventsub.wss.twitch.tv/ws?session=abc"},
		{value: "wss://eventsub.wss.twitch.tv:443/ws?session=abc"},
		{value: "ws://eventsub.wss.twitch.tv/ws", wantErr: true},
		{value: "wss://eventsub.wss.twitch.tv.example/ws", wantErr: true},
		{value: "wss://user@eventsub.wss.twitch.tv/ws", wantErr: true},
		{value: "wss://eventsub.wss.twitch.tv/other", wantErr: true},
	} {
		if err := validateTwitchEventSubURL(test.value); (err != nil) != test.wantErr {
			t.Errorf("validateTwitchEventSubURL(%q) error = %v, wantErr %v", test.value, err, test.wantErr)
		}
	}
}

type twitchRoundTripFunc func(*http.Request) (*http.Response, error)

func (f twitchRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func twitchJSONResponse(status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestStartDeviceAuthUsesPublicClientWithoutSecret(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: twitchRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != twitchDeviceURL {
			t.Fatalf("URL = %q", request.URL)
		}
		body, _ := io.ReadAll(request.Body)
		values := string(body)
		if !strings.Contains(values, "client_id=client") || !strings.Contains(values, "scopes=user%3Aread%3Achat") {
			t.Fatalf("form = %q", values)
		}
		if strings.Contains(strings.ToLower(values), "secret") {
			t.Fatalf("form contains a client secret field: %q", values)
		}
		return twitchJSONResponse(http.StatusOK, `{"device_code":"device-code","user_code":"ABCDEFGH","verification_uri":"https://www.twitch.tv/activate?public=true","expires_in":600,"interval":5}`, nil), nil
	})}
	api := &twitchNetworkAPI{client: client}
	device, err := api.startDeviceAuth(context.Background(), "client")
	if err != nil || device.UserCode != "ABCDEFGH" {
		t.Fatalf("startDeviceAuth() = %#v, %v", device, err)
	}
}

func TestPollDeviceTokenRecognizesPending(t *testing.T) {
	t.Parallel()
	api := &twitchNetworkAPI{client: &http.Client{Transport: twitchRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return twitchJSONResponse(http.StatusBadRequest, `{"status":400,"message":"authorization_pending"}`, nil), nil
	})}}
	_, err := api.pollDeviceToken(context.Background(), "client", "device")
	if !errors.Is(err, errTwitchAuthPending) {
		t.Fatalf("pollDeviceToken() error = %v, want pending", err)
	}
}

func TestTwitchRetryDelayUsesLongestServerInstruction(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	header := make(http.Header)
	header.Set("Ratelimit-Reset", "1788782420") // 20 seconds after now.
	header.Set("Retry-After", "30")
	if got := twitchRetryDelayAt(header, now); got != 30*time.Second {
		t.Fatalf("delay = %v, want 30s", got)
	}
	header.Set("Retry-After", now.Add(45*time.Second).Format(http.TimeFormat))
	if got := twitchRetryDelayAt(header, now); got != 45*time.Second {
		t.Fatalf("HTTP-date delay = %v, want 45s", got)
	}
}

type fakeTwitchSocket struct {
	mu       sync.Mutex
	messages [][]byte
	closed   bool
}

func (s *fakeTwitchSocket) Read(time.Duration) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return nil, errors.New("socket ended")
	}
	message := s.messages[0]
	s.messages = s.messages[1:]
	return message, nil
}

func (s *fakeTwitchSocket) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func twitchMessage(t *testing.T, messageType string, payload any) []byte {
	t.Helper()
	value := map[string]any{
		"metadata": map[string]any{
			"message_id":        messageType + "-id",
			"message_type":      messageType,
			"message_timestamp": "2026-09-07T00:00:00Z",
		},
		"payload": payload,
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func twitchChatMessage(t *testing.T, id, text string) []byte {
	t.Helper()
	value := map[string]any{
		"metadata": map[string]any{
			"message_id":           id,
			"message_type":         "notification",
			"message_timestamp":    "2026-09-07T00:00:00Z",
			"subscription_type":    "channel.chat.message",
			"subscription_version": "1",
		},
		"payload": map[string]any{"event": map[string]any{
			"chatter_user_name": "Viewer",
			"message":           map[string]any{"text": text},
		}},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func twitchRevocationMessage(t *testing.T, status string) []byte {
	t.Helper()
	return twitchMessage(t, "revocation", map[string]any{
		"subscription": map[string]any{"status": status},
	})
}

type delayedTwitchSocket struct {
	release  <-chan struct{}
	welcome  []byte
	closed   chan struct{}
	closeOne sync.Once
}

func (s *delayedTwitchSocket) Read(time.Duration) ([]byte, error) {
	select {
	case <-s.release:
		if s.welcome == nil {
			return nil, errors.New("socket ended")
		}
		welcome := s.welcome
		s.welcome = nil
		return welcome, nil
	case <-s.closed:
		return nil, errors.New("socket closed")
	}
}

func (s *delayedTwitchSocket) Close() error {
	s.closeOne.Do(func() { close(s.closed) })
	return nil
}

func TestTwitchStreamHandsOffReconnectBeforeClosingOldSocket(t *testing.T) {
	t.Parallel()
	reconnectURL := "wss://eventsub.wss.twitch.tv/ws?session=reconnect"
	oldSocket := &fakeTwitchSocket{messages: [][]byte{
		twitchMessage(t, "session_welcome", map[string]any{"session": map[string]any{"id": "first", "keepalive_timeout_seconds": 10}}),
		twitchMessage(t, "session_reconnect", map[string]any{"session": map[string]any{"reconnect_url": reconnectURL}}),
	}}
	newSocket := &fakeTwitchSocket{messages: [][]byte{
		twitchMessage(t, "session_welcome", map[string]any{"session": map[string]any{"id": "second", "keepalive_timeout_seconds": 10}}),
	}}
	var dialCount int
	api := &twitchNetworkAPI{
		client: &http.Client{Transport: twitchRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != twitchSubscriptionsURL {
				t.Fatalf("unexpected request URL %q", request.URL)
			}
			return twitchJSONResponse(http.StatusAccepted, `{}`, nil), nil
		})},
		dial: func(_ context.Context, endpoint string) (twitchWebSocket, error) {
			dialCount++
			if dialCount == 1 {
				return oldSocket, nil
			}
			oldSocket.mu.Lock()
			closedEarly := oldSocket.closed
			oldSocket.mu.Unlock()
			if closedEarly {
				t.Fatal("old socket closed before reconnect welcome")
			}
			if endpoint != reconnectURL {
				t.Fatalf("reconnect endpoint = %q", endpoint)
			}
			return newSocket, nil
		},
	}
	stop := errors.New("stop after reconnect")
	err := api.stream(context.Background(), "client", "token", "broadcaster", "user", func(signal twitchStreamSignal) error {
		if signal.Reconnected {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("stream() error = %v, want stop", err)
	}
	oldSocket.mu.Lock()
	defer oldSocket.mu.Unlock()
	if !oldSocket.closed {
		t.Fatal("old socket was not closed after reconnect welcome")
	}
}

func TestTwitchStreamDeliversOldSocketMessageWhileWaitingForReconnectWelcome(t *testing.T) {
	t.Parallel()
	reconnectURL := "wss://eventsub.wss.twitch.tv/ws?session=reconnect"
	oldSocket := &fakeTwitchSocket{messages: [][]byte{
		twitchMessage(t, "session_welcome", map[string]any{"session": map[string]any{"id": "first", "keepalive_timeout_seconds": 10}}),
		twitchMessage(t, "session_reconnect", map[string]any{"session": map[string]any{"reconnect_url": reconnectURL}}),
		twitchChatMessage(t, "during-handoff", "still delivered"),
	}}
	releaseWelcome := make(chan struct{})
	newSocket := &delayedTwitchSocket{
		release: releaseWelcome,
		welcome: twitchMessage(t, "session_welcome", map[string]any{"session": map[string]any{"id": "second", "keepalive_timeout_seconds": 10}}),
		closed:  make(chan struct{}),
	}
	dialCount := 0
	api := &twitchNetworkAPI{
		client: &http.Client{Transport: twitchRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return twitchJSONResponse(http.StatusAccepted, `{}`, nil), nil
		})},
		dial: func(context.Context, string) (twitchWebSocket, error) {
			dialCount++
			if dialCount == 1 {
				return oldSocket, nil
			}
			return newSocket, nil
		},
	}
	eventReceived := make(chan struct{})
	stop := errors.New("stop")
	done := make(chan error, 1)
	go func() {
		done <- api.stream(context.Background(), "client", "token", "broadcaster", "user", func(signal twitchStreamSignal) error {
			if signal.Event != nil && signal.Event.ID == "during-handoff" {
				close(eventReceived)
			}
			if signal.Reconnected {
				return stop
			}
			return nil
		})
	}()
	select {
	case <-eventReceived:
	case <-time.After(time.Second):
		t.Fatal("old socket message was not delivered while new Welcome was pending")
	}
	close(releaseWelcome)
	if err := <-done; !errors.Is(err, stop) {
		t.Fatalf("stream() error = %v, want stop", err)
	}
}

func TestTwitchHandoffDeliveryErrorStopsReaderWithQueuedMessages(t *testing.T) {
	t.Parallel()
	reconnectURL := "wss://eventsub.wss.twitch.tv/ws?session=reconnect"
	oldSocket := &fakeTwitchSocket{messages: [][]byte{
		twitchMessage(t, "session_welcome", map[string]any{"session": map[string]any{"id": "first", "keepalive_timeout_seconds": 10}}),
		twitchMessage(t, "session_reconnect", map[string]any{"session": map[string]any{"reconnect_url": reconnectURL}}),
		twitchChatMessage(t, "first", "delivery fails"),
		twitchChatMessage(t, "second", "must not strand reader"),
	}}
	newSocket := &fakeTwitchSocket{messages: [][]byte{
		twitchMessage(t, "session_welcome", map[string]any{"session": map[string]any{"id": "second", "keepalive_timeout_seconds": 10}}),
	}}
	dialCount := 0
	api := &twitchNetworkAPI{
		client: &http.Client{Transport: twitchRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return twitchJSONResponse(http.StatusAccepted, `{}`, nil), nil
		})},
		dial: func(context.Context, string) (twitchWebSocket, error) {
			dialCount++
			if dialCount == 1 {
				return oldSocket, nil
			}
			return newSocket, nil
		},
	}
	deliveryErr := errors.New("delivery stopped")
	done := make(chan error, 1)
	go func() {
		done <- api.stream(context.Background(), "client", "token", "broadcaster", "user", func(signal twitchStreamSignal) error {
			if signal.Event != nil {
				return deliveryErr
			}
			return nil
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, deliveryErr) {
			t.Fatalf("stream() error = %v, want delivery error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after handoff delivery error")
	}
}

func TestTwitchHandoffPropagatesAuthorizationRevocation(t *testing.T) {
	t.Parallel()
	reconnectURL := "wss://eventsub.wss.twitch.tv/ws?session=reconnect"
	oldSocket := &fakeTwitchSocket{messages: [][]byte{
		twitchMessage(t, "session_welcome", map[string]any{"session": map[string]any{"id": "first", "keepalive_timeout_seconds": 10}}),
		twitchMessage(t, "session_reconnect", map[string]any{"session": map[string]any{"reconnect_url": reconnectURL}}),
		twitchRevocationMessage(t, "authorization_revoked"),
	}}
	newSocket := &fakeTwitchSocket{messages: [][]byte{
		twitchMessage(t, "session_welcome", map[string]any{"session": map[string]any{"id": "second", "keepalive_timeout_seconds": 10}}),
	}}
	dialCount := 0
	api := &twitchNetworkAPI{
		client: &http.Client{Transport: twitchRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return twitchJSONResponse(http.StatusAccepted, `{}`, nil), nil
		})},
		dial: func(context.Context, string) (twitchWebSocket, error) {
			dialCount++
			if dialCount == 1 {
				return oldSocket, nil
			}
			return newSocket, nil
		},
	}
	err := api.stream(context.Background(), "client", "token", "broadcaster", "user", func(twitchStreamSignal) error { return nil })
	if !errors.Is(err, errTwitchAuthRevoked) {
		t.Fatalf("stream() error = %v, want authorization revoked", err)
	}
}
