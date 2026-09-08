package main

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"sync"
	"time"
)

const (
	twitchStateDisconnected = "disconnected"
	twitchStateAuthorizing  = "authorizing"
	twitchStateAuthorized   = "authorized"
	twitchStateConnecting   = "connecting"
	twitchStateConnected    = "connected"
	twitchStateReconnecting = "reconnecting"
	twitchStateError        = "error"
	maxTwitchStreamRetries  = 5
	maxSeenTwitchMessages   = 5000
	maxTwitchRetryDelay     = time.Minute
	defaultTwitchValidation = time.Hour
)

// TwitchEvent is a live EventSub chat message emitted to the frontend.
type TwitchEvent struct {
	ID          string `json:"-"`
	Author      string `json:"author"`
	AuthorLogin string `json:"authorLogin"`
	Message     string `json:"message"`
	OccurredAt  string `json:"occurredAt"`
}

// TwitchStatus deliberately excludes access tokens, refresh tokens, device codes,
// user IDs, and broadcaster IDs. UserCode is the short-lived code intended for display.
type TwitchStatus struct {
	State           string       `json:"state"`
	Authenticated   bool         `json:"authenticated"`
	Connected       bool         `json:"connected"`
	ChannelLogin    string       `json:"channelLogin"`
	UserCode        string       `json:"userCode"`
	VerificationURI string       `json:"verificationUri"`
	CodeExpiresAt   string       `json:"codeExpiresAt"`
	Messages        int          `json:"messages"`
	Reconnects      int          `json:"reconnects"`
	LastEvent       *TwitchEvent `json:"lastEvent,omitempty"`
	Error           string       `json:"error"`
}

type twitchServiceDependencies struct {
	api                twitchAPI
	openURL            func(context.Context, string)
	emit               func(TwitchEvent)
	wait               func(context.Context, time.Duration) error
	jitter             func(time.Duration) time.Duration
	validationInterval time.Duration
}

// TwitchService owns one user's in-memory Twitch authorization and EventSub connection.
type TwitchService struct {
	mu               sync.Mutex
	deps             twitchServiceDependencies
	status           TwitchStatus
	clientID         string
	token            twitchTokenSet
	userID           string
	authCancel       context.CancelFunc
	streamCancel     context.CancelFunc
	validationCancel context.CancelFunc
	authRun          uint64
	streamRun        uint64
}

func newTwitchService(deps twitchServiceDependencies) *TwitchService {
	if deps.api == nil {
		deps.api = newTwitchNetworkAPI()
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
			return delay * time.Duration(192+int(value[0])/2) / 256
		}
	}
	if deps.validationInterval <= 0 {
		deps.validationInterval = defaultTwitchValidation
	}
	return &TwitchService{deps: deps, status: TwitchStatus{State: twitchStateDisconnected}}
}

func (s *TwitchService) Status() TwitchStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.status
	if status.LastEvent != nil {
		last := *status.LastEvent
		status.LastEvent = &last
	}
	return status
}

func (s *TwitchService) BeginAuth(baseCtx context.Context, clientID string) error {
	if err := validateTwitchClientID(clientID); err != nil {
		return err
	}
	requestCtx, requestCancel := context.WithTimeout(baseCtx, 20*time.Second)
	s.mu.Lock()
	s.cancelLocked()
	s.authRun++
	s.streamRun++
	run := s.authRun
	s.authCancel = requestCancel
	s.clientID = clientID
	s.token = twitchTokenSet{}
	s.userID = ""
	s.status = TwitchStatus{State: twitchStateAuthorizing}
	s.mu.Unlock()

	device, err := s.deps.api.startDeviceAuth(requestCtx, clientID)
	requestErr := requestCtx.Err()
	requestCancel()
	if err != nil {
		if errors.Is(requestErr, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(requestErr, context.DeadlineExceeded) {
			message := "Twitchとの通信がタイムアウトしました。もう一度お試しください。"
			s.setAuthError(run, message)
			return errors.New(message)
		}
		message := friendlyTwitchError(err)
		s.setAuthError(run, message)
		return errors.New(message)
	}
	authCtx, authCancel := context.WithTimeout(baseCtx, time.Duration(device.ExpiresIn)*time.Second)

	s.mu.Lock()
	if run != s.authRun || baseCtx.Err() != nil {
		s.mu.Unlock()
		authCancel()
		return context.Canceled
	}
	s.authCancel = authCancel
	s.status = TwitchStatus{
		State:           twitchStateAuthorizing,
		UserCode:        device.UserCode,
		VerificationURI: device.VerificationURI,
		CodeExpiresAt:   time.Now().Add(time.Duration(device.ExpiresIn) * time.Second).UTC().Format(time.RFC3339),
	}
	s.mu.Unlock()

	go s.pollAuthorization(baseCtx, authCtx, run, clientID, device)
	if s.deps.openURL != nil {
		s.deps.openURL(baseCtx, device.VerificationURI)
	}
	return nil
}

func (s *TwitchService) pollAuthorization(baseCtx, authCtx context.Context, run uint64, clientID string, device twitchDeviceAuthorization) {
	defer func() {
		s.mu.Lock()
		if run == s.authRun {
			cancel := s.authCancel
			s.authCancel = nil
			s.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			return
		}
		s.mu.Unlock()
	}()
	interval := time.Duration(device.Interval) * time.Second
	for {
		if err := s.deps.wait(authCtx, interval); err != nil {
			if errors.Is(authCtx.Err(), context.DeadlineExceeded) {
				s.setAuthError(run, "Twitch認証がタイムアウトしました。もう一度お試しください。")
			}
			return
		}
		requestCtx, cancel := context.WithTimeout(authCtx, 20*time.Second)
		token, err := s.deps.api.pollDeviceToken(requestCtx, clientID, device.DeviceCode)
		cancel()
		if errors.Is(err, errTwitchAuthPending) {
			continue
		}
		if errors.Is(err, errTwitchSlowDown) {
			interval += 5 * time.Second
			continue
		}
		if err != nil {
			s.setAuthError(run, friendlyTwitchError(err))
			return
		}
		validationCtx, validationCancel := context.WithTimeout(authCtx, 20*time.Second)
		validation, err := s.deps.api.validateToken(validationCtx, token.AccessToken)
		validationCancel()
		if err != nil || !validTwitchAuthorization(validation, clientID) {
			s.setAuthError(run, "Twitch認証を検証できませんでした。もう一度お試しください。")
			return
		}

		sessionCtx, sessionCancel := context.WithCancel(baseCtx)
		s.mu.Lock()
		if run != s.authRun || authCtx.Err() != nil {
			s.mu.Unlock()
			sessionCancel()
			return
		}
		s.token = token
		s.userID = validation.UserID
		s.validationCancel = sessionCancel
		s.status = TwitchStatus{State: twitchStateAuthorized, Authenticated: true}
		s.mu.Unlock()
		go s.validateHourly(sessionCtx, run, clientID)
		return
	}
}

func validTwitchAuthorization(validation twitchTokenValidation, clientID string) bool {
	return validation.ClientID == clientID && validation.UserID != "" && validation.ExpiresIn > 0 && hasTwitchScope(validation.Scopes, twitchChatScope)
}

func (s *TwitchService) validateHourly(ctx context.Context, run uint64, clientID string) {
	ticker := time.NewTicker(s.deps.validationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		if run != s.authRun || s.token.AccessToken == "" {
			s.mu.Unlock()
			return
		}
		accessToken := s.token.AccessToken
		s.mu.Unlock()
		requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		validation, err := s.deps.api.validateToken(requestCtx, accessToken)
		cancel()
		if err != nil {
			var apiErr *twitchHTTPError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
				s.invalidateAuthorization(run, "Twitch認証が失効しました。もう一度認証してください。")
				return
			}
			s.setValidationWarning(run, friendlyTwitchError(err))
			continue
		}
		if !validTwitchAuthorization(validation, clientID) {
			s.invalidateAuthorization(run, "Twitch認証が失効したか、必要な権限がありません。もう一度認証してください。")
			return
		}
		s.clearValidationWarning(run)
	}
}

func (s *TwitchService) Connect(baseCtx context.Context, rawURL string) error {
	channelLogin, err := parseTwitchChannelLogin(rawURL)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.token.AccessToken == "" || s.userID == "" {
		s.mu.Unlock()
		return errors.New("先にTwitch認証を完了してください")
	}
	if s.streamCancel != nil {
		s.streamCancel()
	}
	streamCtx, streamCancel := context.WithCancel(baseCtx)
	s.streamRun++
	run := s.streamRun
	authRun := s.authRun
	clientID := s.clientID
	accessToken := s.token.AccessToken
	userID := s.userID
	s.streamCancel = streamCancel
	s.status = TwitchStatus{State: twitchStateConnecting, Authenticated: true, ChannelLogin: channelLogin}
	s.mu.Unlock()

	validationCtx, validationCancel := context.WithTimeout(streamCtx, 20*time.Second)
	validation, err := s.deps.api.validateToken(validationCtx, accessToken)
	validationCancel()
	if errors.Is(err, context.Canceled) || streamCtx.Err() != nil {
		streamCancel()
		return context.Canceled
	}
	s.mu.Lock()
	stale := run != s.streamRun || authRun != s.authRun
	s.mu.Unlock()
	if stale {
		streamCancel()
		return context.Canceled
	}
	if err != nil {
		streamCancel()
		var apiErr *twitchHTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			message := friendlyTwitchError(err)
			s.invalidateAuthorization(authRun, message)
			return errors.New(message)
		}
		message := friendlyTwitchError(err)
		s.setStreamError(run, channelLogin, message)
		return errors.New(message)
	}
	if !validTwitchAuthorization(validation, clientID) || validation.UserID != userID {
		streamCancel()
		message := "Twitch認証が失効したか、必要な権限がありません。もう一度認証してください。"
		s.invalidateAuthorization(authRun, message)
		return errors.New(message)
	}
	lookupCtx, lookupCancel := context.WithTimeout(streamCtx, 20*time.Second)
	broadcasterID, err := s.deps.api.lookupUserID(lookupCtx, clientID, accessToken, channelLogin)
	lookupCancel()
	if errors.Is(err, context.Canceled) || streamCtx.Err() != nil {
		streamCancel()
		return context.Canceled
	}
	s.mu.Lock()
	stale = run != s.streamRun || authRun != s.authRun
	s.mu.Unlock()
	if stale {
		streamCancel()
		return context.Canceled
	}
	if err != nil {
		streamCancel()
		message := friendlyTwitchError(err)
		var apiErr *twitchHTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			s.invalidateAuthorization(authRun, message)
			return errors.New(message)
		}
		s.setStreamError(run, channelLogin, message)
		return errors.New(message)
	}

	go s.consumeStream(streamCtx, run, authRun, clientID, accessToken, broadcasterID, userID, channelLogin)
	return nil
}

func (s *TwitchService) consumeStream(ctx context.Context, run, authRun uint64, clientID, accessToken, broadcasterID, userID, channelLogin string) {
	seen := make(map[string]struct{})
	seenOrder := make([]string, 0, maxSeenTwitchMessages)
	retries := 0
	for {
		err := s.deps.api.stream(ctx, clientID, accessToken, broadcasterID, userID, func(signal twitchStreamSignal) error {
			if signal.Ready {
				s.markConnected(run, channelLogin, signal.Reconnected || retries > 0)
			}
			if signal.Event == nil {
				return nil
			}
			if _, duplicate := seen[signal.Event.ID]; duplicate {
				return nil
			}
			seen[signal.Event.ID] = struct{}{}
			seenOrder = append(seenOrder, signal.Event.ID)
			if len(seenOrder) > maxSeenTwitchMessages {
				delete(seen, seenOrder[0])
				seenOrder = seenOrder[1:]
			}
			return s.recordEvent(run, *signal.Event)
		})
		if ctx.Err() != nil {
			return
		}
		var apiErr *twitchHTTPError
		if errors.Is(err, errTwitchAuthRevoked) || (errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized) {
			s.invalidateAuthorization(authRun, "Twitch認証が失効しました。もう一度認証してください。")
			return
		}
		var streamErr *twitchStreamError
		if !errors.As(err, &streamErr) || !streamErr.retryable || retries >= maxTwitchStreamRetries {
			s.setStreamError(run, channelLogin, friendlyTwitchError(err))
			return
		}
		retries++
		delay := time.Second << (retries - 1)
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		delay = s.deps.jitter(delay)
		if errors.As(err, &apiErr) && apiErr.RetryAfter > delay {
			delay = apiErr.RetryAfter
		}
		if delay > maxTwitchRetryDelay {
			s.setStreamError(run, channelLogin, "Twitchから長い待機時間が指定されました。時間をおいて再接続してください。")
			return
		}
		s.markReconnecting(run, channelLogin)
		if err := s.deps.wait(ctx, delay); err != nil {
			return
		}
	}
}

func (s *TwitchService) markConnected(run uint64, channelLogin string, reconnected bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run != s.streamRun {
		return
	}
	s.status.State = twitchStateConnected
	s.status.Connected = true
	s.status.Authenticated = true
	s.status.ChannelLogin = channelLogin
	s.status.Error = ""
	if reconnected {
		s.status.Reconnects++
	}
}

func (s *TwitchService) markReconnecting(run uint64, channelLogin string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run != s.streamRun {
		return
	}
	s.status.State = twitchStateReconnecting
	s.status.Connected = false
	s.status.ChannelLogin = channelLogin
}

func (s *TwitchService) recordEvent(run uint64, event TwitchEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run != s.streamRun || !s.status.Connected {
		return context.Canceled
	}
	s.status.Messages++
	copy := event
	s.status.LastEvent = &copy
	if s.deps.emit != nil {
		s.deps.emit(event)
	}
	return nil
}

func (s *TwitchService) setAuthError(run uint64, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run != s.authRun {
		return
	}
	s.token = twitchTokenSet{}
	s.userID = ""
	s.status = TwitchStatus{State: twitchStateError, Error: message}
}

func (s *TwitchService) setStreamError(run uint64, channelLogin, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run != s.streamRun {
		return
	}
	s.streamCancel = nil
	s.status.State = twitchStateError
	s.status.Connected = false
	s.status.Authenticated = s.token.AccessToken != ""
	s.status.ChannelLogin = channelLogin
	s.status.Error = message
}

func (s *TwitchService) invalidateAuthorization(run uint64, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run != s.authRun {
		return
	}
	if s.streamCancel != nil {
		s.streamCancel()
		s.streamCancel = nil
	}
	if s.validationCancel != nil {
		s.validationCancel()
		s.validationCancel = nil
	}
	s.streamRun++
	s.token = twitchTokenSet{}
	s.userID = ""
	s.status = TwitchStatus{State: twitchStateError, Error: message}
}

func (s *TwitchService) setValidationWarning(run uint64, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run == s.authRun && s.token.AccessToken != "" {
		s.status.Error = message
	}
}

func (s *TwitchService) clearValidationWarning(run uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run == s.authRun && s.token.AccessToken != "" {
		s.status.Error = ""
	}
}

func (s *TwitchService) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streamCancel != nil {
		s.streamCancel()
		s.streamCancel = nil
	}
	s.streamRun++
	s.status = TwitchStatus{State: twitchStateDisconnected, Authenticated: s.token.AccessToken != ""}
	if s.token.AccessToken != "" {
		s.status.State = twitchStateAuthorized
	}
}

func (s *TwitchService) SignOut() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelLocked()
	s.authRun++
	s.streamRun++
	s.clientID = ""
	s.token = twitchTokenSet{}
	s.userID = ""
	s.status = TwitchStatus{State: twitchStateDisconnected}
}

func (s *TwitchService) Shutdown() { s.SignOut() }

func (s *TwitchService) cancelLocked() {
	if s.authCancel != nil {
		s.authCancel()
		s.authCancel = nil
	}
	if s.streamCancel != nil {
		s.streamCancel()
		s.streamCancel = nil
	}
	if s.validationCancel != nil {
		s.validationCancel()
		s.validationCancel = nil
	}
}

func friendlyTwitchError(err error) string {
	if err == nil || errors.Is(err, context.Canceled) {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Twitchとの通信がタイムアウトしました。もう一度お試しください。"
	}
	if err.Error() == "twitch channel not found" {
		return "指定したTwitchチャンネルが見つかりませんでした。URLを確認してください。"
	}
	if err.Error() == "twitch EventSub subscription was revoked" {
		return "Twitchのイベント購読が解除されました。認証と権限を確認してください。"
	}
	var apiErr *twitchHTTPError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return "Twitch認証が失効しました。もう一度認証してください。"
		case http.StatusForbidden:
			return "Twitchコメントを読む権限がありません。認証をやり直してください。"
		case http.StatusTooManyRequests:
			return "Twitch APIの利用上限に達しました。時間をおいてお試しください。"
		}
	}
	return "Twitchとの接続に失敗しました。通信状態を確認してもう一度お試しください。"
}
