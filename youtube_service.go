package main

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const (
	youtubeStateDisconnected = "disconnected"
	youtubeStateAuthorizing  = "authorizing"
	youtubeStateAuthorized   = "authorized"
	youtubeStateConnecting   = "connecting"
	youtubeStateConnected    = "connected"
	youtubeStateError        = "error"
	maxYouTubeStreamRetries  = 5
	maxSeenYouTubeMessages   = 5000
	maxYouTubeRetryDelay     = time.Minute
)

var errYouTubeStreamEnded = errors.New("youtube stream ended")

// YouTubeEvent is emitted to the frontend. Replayed events are display-only
// history and must never be passed to a trigger implementation.
type YouTubeEvent struct {
	Author          string `json:"author"`
	Message         string `json:"message"`
	Type            string `json:"type"`
	PublishedAt     string `json:"publishedAt"`
	Replayed        bool   `json:"replayed"`
	TriggerEligible bool   `json:"triggerEligible"`
}

// YouTubeStatus contains no credentials or channel identifiers.
type YouTubeStatus struct {
	State            string        `json:"state"`
	Authenticated    bool          `json:"authenticated"`
	Connected        bool          `json:"connected"`
	VideoID          string        `json:"videoId"`
	InitialMessages  int           `json:"initialMessages"`
	RealtimeMessages int           `json:"realtimeMessages"`
	LastEvent        *YouTubeEvent `json:"lastEvent,omitempty"`
	Error            string        `json:"error"`
}

type youtubeServiceDependencies struct {
	api         youtubeAPI
	openURL     func(context.Context, string)
	emit        func(YouTubeEvent)
	trigger     func(YouTubeEvent)
	authTimeout time.Duration
	wait        func(context.Context, time.Duration) error
	jitter      func(time.Duration) time.Duration
}

// YouTubeService owns one user's in-memory OAuth session and chat connection.
type YouTubeService struct {
	mu           sync.Mutex
	deps         youtubeServiceDependencies
	status       YouTubeStatus
	tokenSource  oauth2.TokenSource
	authCancel   context.CancelFunc
	streamCancel context.CancelFunc
	tokenCancel  context.CancelFunc
	authRun      uint64
	streamRun    uint64
}

func newYouTubeService(deps youtubeServiceDependencies) *YouTubeService {
	if deps.api == nil {
		deps.api = googleYouTubeAPI{}
	}
	if deps.authTimeout <= 0 {
		deps.authTimeout = oauthTimeout
	}
	if deps.wait == nil {
		deps.wait = func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	if deps.jitter == nil {
		deps.jitter = func(delay time.Duration) time.Duration {
			var value [1]byte
			if _, err := rand.Read(value[:]); err != nil {
				return delay
			}
			// Apply 75%-125% jitter to avoid synchronised reconnects.
			return delay * time.Duration(192+int(value[0])/2) / 256
		}
	}
	return &YouTubeService{deps: deps, status: YouTubeStatus{State: youtubeStateDisconnected}}
}

func (s *YouTubeService) Status() YouTubeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.status
	if status.LastEvent != nil {
		last := *status.LastEvent
		status.LastEvent = &last
	}
	return status
}

func (s *YouTubeService) BeginAuth(baseCtx context.Context, clientID string) error {
	if err := validateGoogleClientID(clientID); err != nil {
		return err
	}
	state, err := newOAuthState()
	if err != nil {
		return errors.New("安全なGoogle認証を開始できませんでした")
	}
	verifier := oauth2.GenerateVerifier()
	listener, err := startOAuthListener()
	if err != nil {
		return err
	}
	host := listener.Addr().String()
	redirectURL := "http://" + host + oauthCallbackPath
	config := googleOAuthConfig(clientID, redirectURL)
	results := make(chan oauthCallbackResult, 1)
	server := &http.Server{
		Handler:           newOAuthCallbackHandler(host, state, results),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    8 * 1024,
	}

	authCtx, authCancel := context.WithTimeout(baseCtx, s.deps.authTimeout)
	s.mu.Lock()
	s.cancelLocked()
	s.authRun++
	s.streamRun++
	run := s.authRun
	s.authCancel = authCancel
	s.tokenSource = nil
	s.status = YouTubeStatus{State: youtubeStateAuthorizing}
	s.mu.Unlock()

	go func() { _ = server.Serve(listener) }()
	go func() {
		defer authCancel()
		s.finishAuth(baseCtx, authCtx, run, config, verifier, server, results)
	}()
	authURL := config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier))
	if s.deps.openURL != nil {
		s.deps.openURL(baseCtx, authURL)
	}
	return nil
}

func (s *YouTubeService) finishAuth(baseCtx, authCtx context.Context, run uint64, config *oauth2.Config, verifier string, server *http.Server, results <-chan oauthCallbackResult) {
	defer shutdownOAuthServer(server)
	var result oauthCallbackResult
	select {
	case <-authCtx.Done():
		if errors.Is(authCtx.Err(), context.DeadlineExceeded) {
			s.setAuthError(run, "Google認証がタイムアウトしました。もう一度お試しください。")
		}
		return
	case result = <-results:
	}
	if result.err != nil {
		s.setAuthError(run, result.err.Error())
		return
	}
	token, err := config.Exchange(youtubeOAuthHTTPContext(authCtx), result.code, oauth2.VerifierOption(verifier))
	if err != nil || token == nil || !token.Valid() {
		s.setAuthError(run, "Google認証を完了できませんでした。もう一度お試しください。")
		return
	}
	sessionCtx, sessionCancel := context.WithCancel(baseCtx)
	tokenSource := newScopedYouTubeTokens(youtubeOAuthHTTPContext(sessionCtx), config, token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if run != s.authRun || authCtx.Err() != nil {
		sessionCancel()
		return
	}
	s.tokenSource = tokenSource
	s.tokenCancel = sessionCancel
	s.authCancel = nil
	s.status = YouTubeStatus{State: youtubeStateAuthorized, Authenticated: true}
}

func (s *YouTubeService) setAuthError(run uint64, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run != s.authRun {
		return
	}
	s.authCancel = nil
	s.tokenSource = nil
	s.status = YouTubeStatus{State: youtubeStateError, Error: message}
}

func (s *YouTubeService) Connect(baseCtx context.Context, rawURL string) error {
	videoID, err := parseYouTubeVideoID(rawURL)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.tokenSource == nil {
		s.mu.Unlock()
		return errors.New("先にGoogle認証を完了してください")
	}
	if s.streamCancel != nil {
		s.streamCancel()
	}
	streamCtx, streamCancel := context.WithCancel(baseCtx)
	s.streamRun++
	run := s.streamRun
	tokenSource := s.tokenSource
	if scoped, ok := tokenSource.(*scopedYouTubeTokens); ok {
		tokenSource = scoped.withContext(streamCtx)
	}
	s.streamCancel = streamCancel
	s.status = YouTubeStatus{State: youtubeStateConnecting, Authenticated: true, VideoID: videoID}
	s.mu.Unlock()

	lookupCtx, lookupCancel := context.WithTimeout(streamCtx, 20*time.Second)
	lookupSource := tokenSource
	if scoped, ok := tokenSource.(*scopedYouTubeTokenRequest); ok {
		lookupSource = scoped.source.withContext(lookupCtx)
	}
	liveChatID, err := s.deps.api.activeLiveChatID(lookupCtx, lookupSource, videoID)
	lookupCancel()
	if err != nil {
		streamCancel()
		if errors.Is(err, context.Canceled) {
			return err
		}
		message := friendlyYouTubeError(err)
		s.setStreamError(run, videoID, message)
		return errors.New(message)
	}

	s.mu.Lock()
	if run != s.streamRun || streamCtx.Err() != nil {
		s.mu.Unlock()
		return context.Canceled
	}
	s.status.State = youtubeStateConnected
	s.status.Connected = true
	s.mu.Unlock()
	go func() {
		defer streamCancel()
		s.consumeStream(streamCtx, run, tokenSource, liveChatID, videoID)
	}()
	return nil
}

func (s *YouTubeService) consumeStream(ctx context.Context, run uint64, tokenSource oauth2.TokenSource, liveChatID, videoID string) {
	firstResponse := true
	pageToken := ""
	retries := 0
	seen := make(map[string]struct{})
	seenOrder := make([]string, 0, maxSeenYouTubeMessages)

	for {
		err := s.deps.api.stream(ctx, tokenSource, liveChatID, pageToken, func(page youtubePage) error {
			isHistory := firstResponse
			firstResponse = false
			if page.nextPageToken != "" {
				pageToken = page.nextPageToken
			}
			for _, message := range page.messages {
				if _, duplicate := seen[message.id]; duplicate {
					continue
				}
				seen[message.id] = struct{}{}
				seenOrder = append(seenOrder, message.id)
				if len(seenOrder) > maxSeenYouTubeMessages {
					delete(seen, seenOrder[0])
					seenOrder = seenOrder[1:]
				}
				event := YouTubeEvent{
					Author:          message.author,
					Message:         message.text,
					Type:            message.messageType,
					PublishedAt:     message.publishedAt,
					Replayed:        isHistory,
					TriggerEligible: !isHistory,
				}
				s.recordEvent(run, event)
			}
			if page.offline || page.nextPageToken == "" {
				return errYouTubeStreamEnded
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errYouTubeStreamEnded) || err == nil {
			s.setStreamError(run, videoID, "YouTube Liveは終了しています。")
			return
		}
		retryable, retryAfter := retryableYouTubeError(err)
		if !retryable || retries >= maxYouTubeStreamRetries {
			s.setStreamError(run, videoID, friendlyYouTubeError(err))
			return
		}
		retries++
		delay := time.Second << (retries - 1)
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		delay = s.deps.jitter(delay)
		if retryAfter > delay {
			delay = retryAfter
		}
		if delay > maxYouTubeRetryDelay {
			s.setStreamError(run, videoID, "YouTubeから長い待機時間が指定されました。時間をおいて再接続してください。")
			return
		}
		if err := s.deps.wait(ctx, delay); err != nil {
			return
		}
	}
}

func (s *YouTubeService) recordEvent(run uint64, event YouTubeEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run != s.streamRun || !s.status.Connected {
		return
	}
	if event.Replayed {
		s.status.InitialMessages++
	} else {
		s.status.RealtimeMessages++
	}
	copy := event
	s.status.LastEvent = &copy
	emit := s.deps.emit
	trigger := s.deps.trigger
	// Callbacks must be bounded and must not re-enter the service. Holding the
	// lock ensures disconnect/sign-out cannot return before delivery completes.
	if emit != nil {
		emit(event)
	}
	if event.TriggerEligible && trigger != nil {
		trigger(event)
	}
}

func (s *YouTubeService) setStreamError(run uint64, videoID, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run != s.streamRun {
		return
	}
	s.streamCancel = nil
	s.status.State = youtubeStateError
	s.status.Connected = false
	s.status.Authenticated = s.tokenSource != nil
	s.status.VideoID = videoID
	s.status.Error = message
}

func (s *YouTubeService) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streamCancel != nil {
		s.streamCancel()
		s.streamCancel = nil
	}
	s.streamRun++
	s.status = YouTubeStatus{State: youtubeStateDisconnected, Authenticated: s.tokenSource != nil}
	if s.tokenSource != nil {
		s.status.State = youtubeStateAuthorized
	}
}

func (s *YouTubeService) SignOut() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelLocked()
	s.authRun++
	s.streamRun++
	s.tokenSource = nil
	s.status = YouTubeStatus{State: youtubeStateDisconnected}
}

func (s *YouTubeService) Shutdown() {
	s.SignOut()
}

func (s *YouTubeService) cancelLocked() {
	if s.tokenCancel != nil {
		s.tokenCancel()
		s.tokenCancel = nil
	}
	if s.authCancel != nil {
		s.authCancel()
		s.authCancel = nil
	}
	if s.streamCancel != nil {
		s.streamCancel()
		s.streamCancel = nil
	}
}
