package main

import (
	"context"
	"golang.org/x/oauth2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestYouTubeRefreshCancellation(t *testing.T) {
	for _, mode := range []string{"disconnect", "signout", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Read the form so the server can observe the client's cancellation.
				_ = r.ParseForm()
				entered <- struct{}{}
				<-r.Context().Done()
			}))
			defer server.Close()
			session, cancelSession := context.WithCancel(context.Background())
			defer cancelSession()
			request, cancelRequest := context.WithCancel(session)
			defer cancelRequest()
			if mode == "deadline" {
				var cancel context.CancelFunc
				request, cancel = context.WithTimeout(session, 100*time.Millisecond)
				defer cancel()
			}
			config := &oauth2.Config{ClientID: "test", Endpoint: oauth2.Endpoint{TokenURL: server.URL, AuthStyle: oauth2.AuthStyleInParams}}
			source := newScopedYouTubeTokens(session, config, &oauth2.Token{AccessToken: "expired", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Hour)})
			s := newYouTubeService(youtubeServiceDependencies{})
			s.tokenSource = source
			s.tokenCancel = cancelSession
			s.streamCancel = cancelRequest
			done := make(chan error, 1)
			go func() { _, err := source.withContext(request).Token(); done <- err }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("refresh did not start")
			}
			switch mode {
			case "disconnect":
				s.Disconnect()
			case "signout":
				s.SignOut()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("refresh succeeded after cancellation")
				}
			case <-time.After(time.Second):
				t.Fatal("refresh was not cancelled")
			}
			if mode == "disconnect" && session.Err() != nil {
				t.Fatal("disconnect invalidated authentication session")
			}
		})
	}
}

func TestYouTubeRefreshIsSharedAcrossConnections(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer server.Close()
	ctx := context.Background()
	config := &oauth2.Config{ClientID: "test", Endpoint: oauth2.Endpoint{TokenURL: server.URL, AuthStyle: oauth2.AuthStyleInParams}}
	source := newScopedYouTubeTokens(ctx, config, &oauth2.Token{RefreshToken: "refresh"})
	for i := 0; i < 2; i++ {
		token, err := source.withContext(ctx).Token()
		if err != nil || token.AccessToken != "new" {
			t.Fatalf("refresh failed: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("refresh count = %d", calls)
	}
}

func TestYouTubeRefreshWaitCanBeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := newScopedYouTubeTokens(context.Background(), &oauth2.Config{}, nil)
	source.gate <- struct{}{}
	cancel()
	done := make(chan error, 1)
	go func() { _, err := source.withContext(ctx).Token(); done <- err }()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled request blocked behind another refresh")
	}
	<-source.gate
}
