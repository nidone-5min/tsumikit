package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type fakeTwitchAPI struct {
	startDeviceAuthFunc func(context.Context, string) (twitchDeviceAuthorization, error)
	pollDeviceTokenFunc func(context.Context, string, string) (twitchTokenSet, error)
	validateTokenFunc   func(context.Context, string) (twitchTokenValidation, error)
	lookupUserIDFunc    func(context.Context, string, string, string) (string, error)
	streamFunc          func(context.Context, string, string, string, string, func(twitchStreamSignal) error) error
}

func (f *fakeTwitchAPI) startDeviceAuth(ctx context.Context, clientID string) (twitchDeviceAuthorization, error) {
	return f.startDeviceAuthFunc(ctx, clientID)
}

func (f *fakeTwitchAPI) pollDeviceToken(ctx context.Context, clientID, deviceCode string) (twitchTokenSet, error) {
	return f.pollDeviceTokenFunc(ctx, clientID, deviceCode)
}

func (f *fakeTwitchAPI) validateToken(ctx context.Context, token string) (twitchTokenValidation, error) {
	return f.validateTokenFunc(ctx, token)
}

func (f *fakeTwitchAPI) lookupUserID(ctx context.Context, clientID, token, login string) (string, error) {
	return f.lookupUserIDFunc(ctx, clientID, token, login)
}

func (f *fakeTwitchAPI) stream(ctx context.Context, clientID, token, broadcasterID, userID string, receive func(twitchStreamSignal) error) error {
	return f.streamFunc(ctx, clientID, token, broadcasterID, userID, receive)
}

func validTwitchValidation(clientID string) twitchTokenValidation {
	return twitchTokenValidation{ClientID: clientID, UserID: "user-id", Login: "viewer", Scopes: []string{twitchChatScope}, ExpiresIn: 3600}
}

func waitForTwitchStatus(t *testing.T, service *TwitchService, matches func(TwitchStatus) bool) TwitchStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		if matches(status) {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	status := service.Status()
	t.Fatalf("timed out waiting for Twitch status: %#v", status)
	return status
}

func TestTwitchDeviceAuthorizationUsesServerIntervalAndValidatesToken(t *testing.T) {
	t.Parallel()
	clientID := "abcdefghijklmnopqrstuv12345678"
	var mu sync.Mutex
	waits := make([]time.Duration, 0, 2)
	polls := 0
	api := &fakeTwitchAPI{
		startDeviceAuthFunc: func(context.Context, string) (twitchDeviceAuthorization, error) {
			return twitchDeviceAuthorization{DeviceCode: "private-device", UserCode: "ABCDEFGH", VerificationURI: "https://www.twitch.tv/activate", ExpiresIn: 600, Interval: 7}, nil
		},
		pollDeviceTokenFunc: func(context.Context, string, string) (twitchTokenSet, error) {
			polls++
			if polls == 1 {
				return twitchTokenSet{}, errTwitchAuthPending
			}
			return twitchTokenSet{AccessToken: "private-access", RefreshToken: "private-refresh", TokenType: "bearer"}, nil
		},
		validateTokenFunc: func(context.Context, string) (twitchTokenValidation, error) {
			return validTwitchValidation(clientID), nil
		},
	}
	service := newTwitchService(twitchServiceDependencies{
		api: api,
		wait: func(_ context.Context, delay time.Duration) error {
			mu.Lock()
			waits = append(waits, delay)
			mu.Unlock()
			return nil
		},
	})
	defer service.Shutdown()
	if err := service.BeginAuth(context.Background(), clientID); err != nil {
		t.Fatal(err)
	}
	status := waitForTwitchStatus(t, service, func(status TwitchStatus) bool { return status.Authenticated })
	if status.UserCode != "" || status.VerificationURI != "" {
		t.Fatalf("authorized status retained device authorization data: %#v", status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 2 {
		t.Fatalf("wait calls = %d, want 2", len(waits))
	}
}

func TestTwitchSignOutCannotBeOverwrittenByDelayedDeviceRequest(t *testing.T) {
	t.Parallel()
	clientID := "abcdefghijklmnopqrstuv12345678"
	started := make(chan struct{})
	release := make(chan struct{})
	opened := make(chan struct{}, 1)
	api := &fakeTwitchAPI{
		startDeviceAuthFunc: func(context.Context, string) (twitchDeviceAuthorization, error) {
			close(started)
			<-release
			return twitchDeviceAuthorization{DeviceCode: "private-device", UserCode: "CODE", VerificationURI: "https://www.twitch.tv/activate", ExpiresIn: 600, Interval: 5}, nil
		},
	}
	service := newTwitchService(twitchServiceDependencies{api: api, openURL: func(context.Context, string) { opened <- struct{}{} }})
	done := make(chan error, 1)
	go func() { done <- service.BeginAuth(context.Background(), clientID) }()
	<-started
	service.SignOut()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("BeginAuth() error = %v, want cancellation", err)
	}
	status := service.Status()
	if status.State != twitchStateDisconnected || status.Authenticated || status.UserCode != "" {
		t.Fatalf("delayed authorization restored state: %#v", status)
	}
	select {
	case <-opened:
		t.Fatal("stale authorization opened the browser")
	default:
	}
}

func TestTwitchHourlyValidationInvalidatesSessionAndStopsStream(t *testing.T) {
	t.Parallel()
	clientID := "abcdefghijklmnopqrstuv12345678"
	validationCalls := 0
	streamCancelled := make(chan struct{})
	api := &fakeTwitchAPI{
		startDeviceAuthFunc: func(context.Context, string) (twitchDeviceAuthorization, error) {
			return twitchDeviceAuthorization{DeviceCode: "device", UserCode: "CODE", VerificationURI: "https://www.twitch.tv/activate", ExpiresIn: 600, Interval: 1}, nil
		},
		pollDeviceTokenFunc: func(context.Context, string, string) (twitchTokenSet, error) {
			return twitchTokenSet{AccessToken: "token", TokenType: "bearer"}, nil
		},
		validateTokenFunc: func(context.Context, string) (twitchTokenValidation, error) {
			validationCalls++
			if validationCalls <= 2 {
				return validTwitchValidation(clientID), nil
			}
			return twitchTokenValidation{}, &twitchHTTPError{StatusCode: http.StatusUnauthorized}
		},
		lookupUserIDFunc: func(context.Context, string, string, string) (string, error) { return "channel-id", nil },
		streamFunc: func(ctx context.Context, _, _, _, _ string, receive func(twitchStreamSignal) error) error {
			if err := receive(twitchStreamSignal{Ready: true}); err != nil {
				return err
			}
			<-ctx.Done()
			close(streamCancelled)
			return ctx.Err()
		},
	}
	service := newTwitchService(twitchServiceDependencies{
		api:                api,
		wait:               func(context.Context, time.Duration) error { return nil },
		validationInterval: 5 * time.Millisecond,
	})
	defer service.Shutdown()
	if err := service.BeginAuth(context.Background(), clientID); err != nil {
		t.Fatal(err)
	}
	waitForTwitchStatus(t, service, func(status TwitchStatus) bool { return status.Authenticated })
	if err := service.Connect(context.Background(), "https://twitch.tv/channel"); err != nil {
		t.Fatal(err)
	}
	status := waitForTwitchStatus(t, service, func(status TwitchStatus) bool { return !status.Authenticated && status.Error != "" })
	if status.Connected {
		t.Fatal("invalidated session remained connected")
	}
	select {
	case <-streamCancelled:
	case <-time.After(time.Second):
		t.Fatal("stream was not cancelled after token invalidation")
	}
}

func TestTwitchStreamDeduplicatesMessagesAcrossReconnect(t *testing.T) {
	t.Parallel()
	clientID := "abcdefghijklmnopqrstuv12345678"
	events := make(chan TwitchEvent, 2)
	api := &fakeTwitchAPI{
		validateTokenFunc: func(context.Context, string) (twitchTokenValidation, error) {
			return validTwitchValidation(clientID), nil
		},
		lookupUserIDFunc: func(context.Context, string, string, string) (string, error) { return "channel-id", nil },
		streamFunc: func(ctx context.Context, _, _, _, _ string, receive func(twitchStreamSignal) error) error {
			if err := receive(twitchStreamSignal{Ready: true}); err != nil {
				return err
			}
			event := TwitchEvent{ID: "same-message", Author: "Viewer", Message: "hello"}
			if err := receive(twitchStreamSignal{Event: &event}); err != nil {
				return err
			}
			if err := receive(twitchStreamSignal{Event: &event}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	service := newTwitchService(twitchServiceDependencies{api: api, emit: func(event TwitchEvent) { events <- event }})
	service.clientID = clientID
	service.token = twitchTokenSet{AccessToken: "token"}
	service.userID = "user-id"
	service.authRun = 1
	service.status = TwitchStatus{State: twitchStateAuthorized, Authenticated: true}
	defer service.Shutdown()
	if err := service.Connect(context.Background(), "https://twitch.tv/channel"); err != nil {
		t.Fatal(err)
	}
	status := waitForTwitchStatus(t, service, func(status TwitchStatus) bool { return status.Messages == 1 })
	if status.LastEvent == nil || status.LastEvent.Message != "hello" {
		t.Fatalf("last event = %#v", status.LastEvent)
	}
	select {
	case <-events:
	default:
		t.Fatal("message was not emitted")
	}
	select {
	case duplicate := <-events:
		t.Fatalf("duplicate was emitted: %#v", duplicate)
	default:
	}
}

func TestTwitchStreamPreservesArrivalOrderWhenTimestampsReverse(t *testing.T) {
	t.Parallel()
	clientID := "abcdefghijklmnopqrstuv12345678"
	events := make(chan TwitchEvent, 2)
	api := &fakeTwitchAPI{
		validateTokenFunc: func(context.Context, string) (twitchTokenValidation, error) {
			return validTwitchValidation(clientID), nil
		},
		lookupUserIDFunc: func(context.Context, string, string, string) (string, error) { return "channel-id", nil },
		streamFunc: func(ctx context.Context, _, _, _, _ string, receive func(twitchStreamSignal) error) error {
			if err := receive(twitchStreamSignal{Ready: true}); err != nil {
				return err
			}
			for _, event := range []TwitchEvent{
				{ID: "received-first", Message: "first", OccurredAt: "2026-09-07T00:00:02Z"},
				{ID: "received-second", Message: "second", OccurredAt: "2026-09-07T00:00:01Z"},
			} {
				if err := receive(twitchStreamSignal{Event: &event}); err != nil {
					return err
				}
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	service := newTwitchService(twitchServiceDependencies{api: api, emit: func(event TwitchEvent) { events <- event }})
	service.clientID = clientID
	service.token = twitchTokenSet{AccessToken: "token"}
	service.userID = "user-id"
	service.authRun = 1
	service.status = TwitchStatus{State: twitchStateAuthorized, Authenticated: true}
	defer service.Shutdown()
	if err := service.Connect(context.Background(), "https://twitch.tv/channel"); err != nil {
		t.Fatal(err)
	}
	first := <-events
	second := <-events
	if first.ID != "received-first" || second.ID != "received-second" {
		t.Fatalf("arrival order changed: %q then %q", first.ID, second.ID)
	}
}

func TestTwitchUnexpectedDisconnectRetriesWithBackoff(t *testing.T) {
	t.Parallel()
	clientID := "abcdefghijklmnopqrstuv12345678"
	streamCalls := 0
	waits := make(chan time.Duration, 1)
	api := &fakeTwitchAPI{
		validateTokenFunc: func(context.Context, string) (twitchTokenValidation, error) {
			return validTwitchValidation(clientID), nil
		},
		lookupUserIDFunc: func(context.Context, string, string, string) (string, error) { return "channel-id", nil },
		streamFunc: func(ctx context.Context, _, _, _, _ string, receive func(twitchStreamSignal) error) error {
			streamCalls++
			if streamCalls == 1 {
				return &twitchStreamError{err: errors.New("network lost"), retryable: true}
			}
			if err := receive(twitchStreamSignal{Ready: true}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	service := newTwitchService(twitchServiceDependencies{
		api:    api,
		jitter: func(delay time.Duration) time.Duration { return delay },
		wait: func(_ context.Context, delay time.Duration) error {
			waits <- delay
			return nil
		},
	})
	service.clientID = clientID
	service.token = twitchTokenSet{AccessToken: "token"}
	service.userID = "user-id"
	service.authRun = 1
	service.status = TwitchStatus{State: twitchStateAuthorized, Authenticated: true}
	defer service.Shutdown()
	if err := service.Connect(context.Background(), "https://twitch.tv/channel"); err != nil {
		t.Fatal(err)
	}
	status := waitForTwitchStatus(t, service, func(status TwitchStatus) bool { return status.Connected })
	if status.Reconnects != 1 {
		t.Fatalf("reconnects = %d, want 1", status.Reconnects)
	}
	if delay := <-waits; delay != time.Second {
		t.Fatalf("retry delay = %v, want 1s", delay)
	}
}

func TestTwitchAuthorizationRevocationClearsTokenAndStopsConnection(t *testing.T) {
	t.Parallel()
	clientID := "abcdefghijklmnopqrstuv12345678"
	api := &fakeTwitchAPI{
		validateTokenFunc: func(context.Context, string) (twitchTokenValidation, error) {
			return validTwitchValidation(clientID), nil
		},
		lookupUserIDFunc: func(context.Context, string, string, string) (string, error) { return "channel-id", nil },
		streamFunc: func(_ context.Context, _, _, _, _ string, receive func(twitchStreamSignal) error) error {
			if err := receive(twitchStreamSignal{Ready: true}); err != nil {
				return err
			}
			return &twitchStreamError{err: errTwitchAuthRevoked, retryable: false}
		},
	}
	service := newTwitchService(twitchServiceDependencies{api: api})
	service.clientID = clientID
	service.token = twitchTokenSet{AccessToken: "token"}
	service.userID = "user-id"
	service.authRun = 1
	service.status = TwitchStatus{State: twitchStateAuthorized, Authenticated: true}
	defer service.Shutdown()
	if err := service.Connect(context.Background(), "https://twitch.tv/channel"); err != nil {
		t.Fatal(err)
	}
	status := waitForTwitchStatus(t, service, func(status TwitchStatus) bool { return !status.Authenticated && status.Error != "" })
	if status.Connected {
		t.Fatal("revoked authorization remained connected")
	}
}

func TestTwitchLookupUnauthorizedClearsToken(t *testing.T) {
	t.Parallel()
	clientID := "abcdefghijklmnopqrstuv12345678"
	api := &fakeTwitchAPI{
		validateTokenFunc: func(context.Context, string) (twitchTokenValidation, error) {
			return validTwitchValidation(clientID), nil
		},
		lookupUserIDFunc: func(context.Context, string, string, string) (string, error) {
			return "", &twitchHTTPError{StatusCode: http.StatusUnauthorized}
		},
	}
	service := newTwitchService(twitchServiceDependencies{api: api})
	service.clientID = clientID
	service.token = twitchTokenSet{AccessToken: "token"}
	service.userID = "user-id"
	service.authRun = 1
	service.status = TwitchStatus{State: twitchStateAuthorized, Authenticated: true}
	defer service.Shutdown()
	if err := service.Connect(context.Background(), "https://twitch.tv/channel"); err == nil {
		t.Fatal("Connect() returned nil for unauthorized lookup")
	}
	if status := service.Status(); status.Authenticated {
		t.Fatalf("unauthorized lookup retained authentication: %#v", status)
	}
}
