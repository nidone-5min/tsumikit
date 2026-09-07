package main

import (
	"context"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

const youtubeTokenTimeout = 20 * time.Second

// scopedYouTubeTokens shares refreshed credentials while each request retains
// its own cancellation. The gate also makes waiting for another refresh cancellable.
type scopedYouTubeTokens struct {
	session context.Context
	config  *oauth2.Config
	token   *oauth2.Token
	gate    chan struct{}
}

func newScopedYouTubeTokens(ctx context.Context, config *oauth2.Config, token *oauth2.Token) *scopedYouTubeTokens {
	return &scopedYouTubeTokens{session: ctx, config: config, token: token, gate: make(chan struct{}, 1)}
}

func (s *scopedYouTubeTokens) Token() (*oauth2.Token, error) { return s.withContext(s.session).Token() }
func (s *scopedYouTubeTokens) withContext(ctx context.Context) oauth2.TokenSource {
	return &scopedYouTubeTokenRequest{source: s, ctx: ctx}
}

type scopedYouTubeTokenRequest struct {
	source *scopedYouTubeTokens
	ctx    context.Context
}

func (r *scopedYouTubeTokenRequest) Token() (*oauth2.Token, error) {
	s := r.source
	ctx, cancel := context.WithTimeout(r.ctx, youtubeTokenTimeout)
	defer cancel()
	stop := context.AfterFunc(s.session, cancel)
	defer stop()
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.gate }()
	if err := s.session.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	token, err := s.config.TokenSource(ctx, s.token).Token()
	if err != nil {
		return nil, err
	}
	s.token = token
	return token, nil
}

func youtubeOAuthHTTPContext(ctx context.Context) context.Context {
	client := &http.Client{Timeout: youtubeTokenTimeout}
	return context.WithValue(ctx, oauth2.HTTPClient, client)
}
