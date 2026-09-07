package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

type fakeYouTubeAPI struct {
	activeID  string
	activeErr error
	streamFn  func(context.Context, string, func(youtubePage) error) error
}

func TestYouTubeDeliveryFinishesBeforeSignOut(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var signedOut, late atomic.Bool
	s := authenticatedYouTubeService(nil, func(YouTubeEvent) { close(entered); <-release }, func(YouTubeEvent) {
		if signedOut.Load() {
			late.Store(true)
		}
	})
	s.status.Connected = true
	go func() { s.recordEvent(0, YouTubeEvent{TriggerEligible: true}); close(done) }()
	<-entered
	stopped := make(chan struct{})
	go func() { s.SignOut(); signedOut.Store(true); close(stopped) }()
	select {
	case <-stopped:
		close(release)
		t.Fatal("sign-out returned during delivery")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-done
	<-stopped
	if late.Load() {
		t.Fatal("old trigger ran after sign-out")
	}
	s.recordEvent(0, YouTubeEvent{TriggerEligible: true})
	if s.Status().LastEvent != nil {
		t.Fatal("stale event restored after sign-out")
	}
}

func TestYouTubeRetryLimitSurvivesEmptyResponses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	api := &fakeYouTubeAPI{streamFn: func(_ context.Context, _ string, receive func(youtubePage) error) error {
		calls++
		if calls > 10 {
			cancel()
			return context.Canceled
		}
		if err := receive(youtubePage{nextPageToken: "same-token"}); err != nil {
			return err
		}
		return errors.New("connection lost")
	}}
	s := authenticatedYouTubeService(api, nil, nil)
	s.consumeStream(ctx, 0, s.tokenSource, "chat", "video")
	if calls != maxYouTubeStreamRetries+1 {
		t.Fatalf("connections = %d, want %d", calls, maxYouTubeStreamRetries+1)
	}
}

func (f *fakeYouTubeAPI) activeLiveChatID(context.Context, oauth2.TokenSource, string) (string, error) {
	return f.activeID, f.activeErr
}

func (f *fakeYouTubeAPI) stream(ctx context.Context, _ oauth2.TokenSource, _ string, token string, receive func(youtubePage) error) error {
	return f.streamFn(ctx, token, receive)
}

func authenticatedYouTubeService(api youtubeAPI, emit, trigger func(YouTubeEvent)) *YouTubeService {
	service := newYouTubeService(youtubeServiceDependencies{
		api:     api,
		emit:    emit,
		trigger: trigger,
		jitter:  func(delay time.Duration) time.Duration { return delay },
		wait:    func(context.Context, time.Duration) error { return nil },
	})
	service.tokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test", TokenType: "Bearer"})
	service.status = YouTubeStatus{State: youtubeStateAuthorized, Authenticated: true}
	return service
}

func TestYouTubeServiceExcludesInitialHistoryFromTriggers(t *testing.T) {
	t.Parallel()
	emitted := make(chan YouTubeEvent, 3)
	triggered := make(chan YouTubeEvent, 1)
	api := &fakeYouTubeAPI{activeID: "live-chat", streamFn: func(_ context.Context, _ string, receive func(youtubePage) error) error {
		if err := receive(youtubePage{
			nextPageToken: "page-1",
			messages: []youtubeMessage{
				{id: "history-1", author: "A", text: "old 1", messageType: "textMessageEvent"},
				{id: "history-2", author: "B", text: "old 2", messageType: "textMessageEvent"},
			},
		}); err != nil {
			return err
		}
		if err := receive(youtubePage{
			nextPageToken: "page-2",
			messages: []youtubeMessage{
				{id: "history-2", author: "B", text: "duplicate", messageType: "textMessageEvent"},
				{id: "realtime-1", author: "C", text: "new", messageType: "textMessageEvent"},
			},
		}); err != nil {
			return err
		}
		return errYouTubeStreamEnded
	}}
	service := authenticatedYouTubeService(api, func(event YouTubeEvent) { emitted <- event }, func(event YouTubeEvent) { triggered <- event })

	if err := service.Connect(context.Background(), "https://youtu.be/dQw4w9WgXcQ"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		select {
		case event := <-emitted:
			if i < 2 && (!event.Replayed || event.TriggerEligible) {
				t.Fatalf("history event %#v was trigger eligible", event)
			}
			if i == 2 && (event.Replayed || !event.TriggerEligible) {
				t.Fatalf("realtime event %#v was not trigger eligible", event)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for event")
		}
	}
	select {
	case event := <-triggered:
		if event.Message != "new" {
			t.Fatalf("trigger event = %#v, want only realtime message", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for realtime trigger")
	}
	select {
	case extra := <-triggered:
		t.Fatalf("unexpected trigger for history: %#v", extra)
	default:
	}
}

func TestYouTubeServiceReconnectsFromLastPageToken(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var tokens []string
	done := make(chan struct{})
	api := &fakeYouTubeAPI{activeID: "live-chat", streamFn: func(_ context.Context, token string, receive func(youtubePage) error) error {
		mu.Lock()
		tokens = append(tokens, token)
		call := len(tokens)
		mu.Unlock()
		if call == 1 {
			if err := receive(youtubePage{nextPageToken: "resume-token"}); err != nil {
				return err
			}
			return &googleapi.Error{Code: http.StatusServiceUnavailable}
		}
		close(done)
		return errYouTubeStreamEnded
	}}
	service := authenticatedYouTubeService(api, nil, nil)
	if err := service.Connect(context.Background(), "https://youtube.com/watch?v=dQw4w9WgXcQ"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tokens) != 2 || tokens[0] != "" || tokens[1] != "resume-token" {
		t.Fatalf("stream tokens = %#v, want [\"\" \"resume-token\"]", tokens)
	}
}

func TestYouTubeServiceDisconnectCancelsStream(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	api := &fakeYouTubeAPI{activeID: "live-chat", streamFn: func(ctx context.Context, _ string, _ func(youtubePage) error) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}}
	service := authenticatedYouTubeService(api, nil, nil)
	if err := service.Connect(context.Background(), "https://youtube.com/live/dQw4w9WgXcQ"); err != nil {
		t.Fatal(err)
	}
	<-started
	service.Disconnect()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("stream context was not cancelled")
	}
	status := service.Status()
	if status.Connected || status.State != youtubeStateAuthorized {
		t.Fatalf("status after disconnect = %#v", status)
	}
}

func TestYouTubeServiceReturnsSafeLookupError(t *testing.T) {
	t.Parallel()
	api := &fakeYouTubeAPI{activeErr: errors.New("token=secret-value")}
	service := authenticatedYouTubeService(api, nil, nil)
	err := service.Connect(context.Background(), "https://youtube.com/watch?v=dQw4w9WgXcQ")
	if err == nil || err.Error() == "token=secret-value" {
		t.Fatalf("Connect() error = %v, want redacted user-facing error", err)
	}
}
