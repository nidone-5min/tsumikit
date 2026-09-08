package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	twitchDeviceURL        = "https://id.twitch.tv/oauth2/device"
	twitchTokenURL         = "https://id.twitch.tv/oauth2/token"
	twitchValidateURL      = "https://id.twitch.tv/oauth2/validate"
	twitchUsersURL         = "https://api.twitch.tv/helix/users"
	twitchSubscriptionsURL = "https://api.twitch.tv/helix/eventsub/subscriptions"
	twitchEventSubURL      = "wss://eventsub.wss.twitch.tv/ws?keepalive_timeout_seconds=30"
	twitchChatScope        = "user:read:chat"
	twitchMaxResponseSize  = 1024 * 1024
)

var (
	twitchClientIDPattern = regexp.MustCompile(`^[a-z0-9]{20,64}$`)
	twitchLoginPattern    = regexp.MustCompile(`^[A-Za-z0-9_]{1,25}$`)
	errTwitchAuthPending  = errors.New("twitch authorization pending")
	errTwitchSlowDown     = errors.New("twitch authorization polling too quickly")
	errTwitchAuthRevoked  = errors.New("twitch authorization revoked")
	errTwitchSubRevoked   = errors.New("twitch EventSub subscription was revoked")
)

type twitchDeviceAuthorization struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type twitchTokenSet struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    int      `json:"expires_in"`
	Scopes       []string `json:"scope"`
	TokenType    string   `json:"token_type"`
}

type twitchTokenValidation struct {
	ClientID  string   `json:"client_id"`
	Login     string   `json:"login"`
	UserID    string   `json:"user_id"`
	Scopes    []string `json:"scopes"`
	ExpiresIn int      `json:"expires_in"`
}

type twitchStreamSignal struct {
	Ready       bool
	Reconnected bool
	Event       *TwitchEvent
}

type twitchAPI interface {
	startDeviceAuth(context.Context, string) (twitchDeviceAuthorization, error)
	pollDeviceToken(context.Context, string, string) (twitchTokenSet, error)
	validateToken(context.Context, string) (twitchTokenValidation, error)
	lookupUserID(context.Context, string, string, string) (string, error)
	stream(context.Context, string, string, string, string, func(twitchStreamSignal) error) error
}

type twitchHTTPError struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (e *twitchHTTPError) Error() string {
	return fmt.Sprintf("twitch API returned status %d", e.StatusCode)
}

type twitchStreamError struct {
	err       error
	retryable bool
}

func (e *twitchStreamError) Error() string { return e.err.Error() }
func (e *twitchStreamError) Unwrap() error { return e.err }

type twitchWebSocket interface {
	Read(time.Duration) ([]byte, error)
	Close() error
}

type twitchNetworkAPI struct {
	client *http.Client
	dial   func(context.Context, string) (twitchWebSocket, error)
}

func newTwitchNetworkAPI() *twitchNetworkAPI {
	api := &twitchNetworkAPI{client: &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	api.dial = api.dialWebSocket
	return api
}

func validateTwitchClientID(clientID string) error {
	if strings.TrimSpace(clientID) != clientID || !twitchClientIDPattern.MatchString(clientID) {
		return errors.New("Twitch公開クライアントのClient IDを入力してください")
	}
	return nil
}

func parseTwitchChannelLogin(rawURL string) (string, error) {
	if len(rawURL) > 2048 {
		return "", errors.New("Twitch配信URLが長すぎます")
	}
	parsed, err := url.ParseRequestURI(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("httpsのTwitch配信URLを入力してください")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return "", errors.New("Twitch配信URLのポート番号が正しくありません")
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "twitch.tv", "www.twitch.tv", "m.twitch.tv":
	default:
		return "", errors.New("twitch.tvの配信URLを入力してください")
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 1 {
		return "", errors.New("Twitch配信URLからチャンネル名を取得できませんでした")
	}
	login, err := url.PathUnescape(segments[0])
	if err != nil || !twitchLoginPattern.MatchString(login) {
		return "", errors.New("Twitch配信URLからチャンネル名を取得できませんでした")
	}
	return strings.ToLower(login), nil
}

func (a *twitchNetworkAPI) startDeviceAuth(ctx context.Context, clientID string) (twitchDeviceAuthorization, error) {
	values := url.Values{"client_id": {clientID}, "scopes": {twitchChatScope}}
	var response twitchDeviceAuthorization
	if err := a.formRequest(ctx, twitchDeviceURL, values, &response); err != nil {
		return response, err
	}
	if len(response.DeviceCode) < 8 || len(response.DeviceCode) > 512 || len(response.UserCode) < 4 || len(response.UserCode) > 64 || response.ExpiresIn < 60 || response.ExpiresIn > 3600 || response.Interval < 1 || response.Interval > 60 {
		return response, errors.New("twitch device authorization response is incomplete")
	}
	if err := validateTwitchVerificationURL(response.VerificationURI); err != nil {
		return response, err
	}
	return response, nil
}

func (a *twitchNetworkAPI) pollDeviceToken(ctx context.Context, clientID, deviceCode string) (twitchTokenSet, error) {
	values := url.Values{
		"client_id":   {clientID},
		"scopes":      {twitchChatScope},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	var response twitchTokenSet
	err := a.formRequest(ctx, twitchTokenURL, values, &response)
	var apiErr *twitchHTTPError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
		switch strings.ToLower(apiErr.Message) {
		case "authorization_pending":
			return response, errTwitchAuthPending
		case "slow_down":
			return response, errTwitchSlowDown
		}
	}
	if err != nil {
		return response, err
	}
	if response.AccessToken == "" || !strings.EqualFold(response.TokenType, "bearer") {
		return response, errors.New("twitch token response is incomplete")
	}
	return response, nil
}

func (a *twitchNetworkAPI) validateToken(ctx context.Context, accessToken string) (twitchTokenValidation, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, twitchValidateURL, nil)
	if err != nil {
		return twitchTokenValidation{}, err
	}
	request.Header.Set("Authorization", "OAuth "+accessToken)
	var response twitchTokenValidation
	if err := a.request(request, &response); err != nil {
		return response, err
	}
	if response.ClientID == "" || response.UserID == "" || response.ExpiresIn <= 0 {
		return response, errors.New("twitch token validation response is incomplete")
	}
	return response, nil
}

func (a *twitchNetworkAPI) lookupUserID(ctx context.Context, clientID, accessToken, login string) (string, error) {
	endpoint, _ := url.Parse(twitchUsersURL)
	query := endpoint.Query()
	query.Set("login", login)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	setTwitchAPIHeaders(request, clientID, accessToken)
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := a.request(request, &response); err != nil {
		return "", err
	}
	if len(response.Data) != 1 || response.Data[0].ID == "" {
		return "", errors.New("twitch channel not found")
	}
	return response.Data[0].ID, nil
}

func (a *twitchNetworkAPI) createChatSubscription(ctx context.Context, clientID, accessToken, sessionID, broadcasterID, userID string) error {
	payload := struct {
		Type      string            `json:"type"`
		Version   string            `json:"version"`
		Condition map[string]string `json:"condition"`
		Transport map[string]string `json:"transport"`
	}{
		Type:    "channel.chat.message",
		Version: "1",
		Condition: map[string]string{
			"broadcaster_user_id": broadcasterID,
			"user_id":             userID,
		},
		Transport: map[string]string{"method": "websocket", "session_id": sessionID},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, twitchSubscriptionsURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	setTwitchAPIHeaders(request, clientID, accessToken)
	request.Header.Set("Content-Type", "application/json")
	return a.request(request, nil)
}

func setTwitchAPIHeaders(request *http.Request, clientID, accessToken string) {
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Client-Id", clientID)
}

func (a *twitchNetworkAPI) formRequest(ctx context.Context, endpoint string, values url.Values, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return a.request(request, target)
}

func (a *twitchNetworkAPI) request(request *http.Request, target any) error {
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, twitchMaxResponseSize+1))
	if err != nil {
		return err
	}
	if len(body) > twitchMaxResponseSize {
		return errors.New("twitch API response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &problem)
		return &twitchHTTPError{
			StatusCode: response.StatusCode,
			Message:    problem.Message,
			RetryAfter: twitchRetryDelay(response.Header),
		}
	}
	if target == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return errors.New("twitch API response is invalid")
	}
	return nil
}

func twitchRetryDelay(header http.Header) time.Duration {
	return twitchRetryDelayAt(header, time.Now())
}

func twitchRetryDelayAt(header http.Header, now time.Time) time.Duration {
	var delay time.Duration
	value := header.Get("Ratelimit-Reset")
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		delay = time.Unix(seconds, 0).Sub(now)
		if delay < 0 {
			delay = 0
		}
	}
	retryAfter := header.Get("Retry-After")
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
		if candidate := time.Duration(seconds) * time.Second; candidate > delay {
			delay = candidate
		}
	} else if parsed, err := http.ParseTime(retryAfter); err == nil {
		if candidate := parsed.Sub(now); candidate > delay {
			delay = candidate
		}
	}
	return delay
}

func validateTwitchVerificationURL(rawURL string) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return errors.New("twitch verification URL is invalid")
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "twitch.tv", "www.twitch.tv":
		return nil
	default:
		return errors.New("twitch verification URL is invalid")
	}
}

func validateTwitchEventSubURL(rawURL string) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Scheme != "wss" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("twitch reconnect URL is invalid")
	}
	if !strings.EqualFold(parsed.Hostname(), "eventsub.wss.twitch.tv") || (parsed.Port() != "" && parsed.Port() != "443") || parsed.Path != "/ws" {
		return errors.New("twitch reconnect URL is invalid")
	}
	return nil
}

type twitchEventSubMessage struct {
	Metadata struct {
		MessageID        string `json:"message_id"`
		MessageType      string `json:"message_type"`
		MessageTimestamp string `json:"message_timestamp"`
		SubscriptionType string `json:"subscription_type"`
	} `json:"metadata"`
	Payload struct {
		Session struct {
			ID                      string  `json:"id"`
			KeepaliveTimeoutSeconds int     `json:"keepalive_timeout_seconds"`
			ReconnectURL            *string `json:"reconnect_url"`
		} `json:"session"`
		Event struct {
			ChatterUserName  string `json:"chatter_user_name"`
			ChatterUserLogin string `json:"chatter_user_login"`
			Message          struct {
				Text string `json:"text"`
			} `json:"message"`
		} `json:"event"`
		Subscription struct {
			Status string `json:"status"`
		} `json:"subscription"`
	} `json:"payload"`
}

func decodeTwitchMessage(data []byte) (twitchEventSubMessage, error) {
	var message twitchEventSubMessage
	if len(data) == 0 || len(data) > twitchMaxResponseSize || json.Unmarshal(data, &message) != nil || message.Metadata.MessageType == "" {
		return message, errors.New("twitch EventSub message is invalid")
	}
	return message, nil
}

func (a *twitchNetworkAPI) stream(ctx context.Context, clientID, accessToken, broadcasterID, userID string, receive func(twitchStreamSignal) error) error {
	connection, welcome, err := a.connect(ctx, twitchEventSubURL)
	if err != nil {
		return &twitchStreamError{err: err, retryable: true}
	}
	defer func() { _ = connection.Close() }()

	subscribeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	err = a.createChatSubscription(subscribeCtx, clientID, accessToken, welcome.Payload.Session.ID, broadcasterID, userID)
	cancel()
	if err != nil {
		return &twitchStreamError{err: err, retryable: retryableTwitchHTTPError(err)}
	}
	if err := receive(twitchStreamSignal{Ready: true}); err != nil {
		return err
	}
	keepalive := twitchKeepaliveTimeout(welcome.Payload.Session.KeepaliveTimeoutSeconds)

	for {
		data, err := connection.Read(keepalive)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &twitchStreamError{err: err, retryable: true}
		}
		message, err := decodeTwitchMessage(data)
		if err != nil {
			return &twitchStreamError{err: err, retryable: false}
		}
		switch message.Metadata.MessageType {
		case "session_keepalive":
		case "notification":
			if message.Metadata.SubscriptionType != "channel.chat.message" || message.Metadata.MessageID == "" {
				continue
			}
			event := TwitchEvent{
				ID:          message.Metadata.MessageID,
				Author:      message.Payload.Event.ChatterUserName,
				AuthorLogin: message.Payload.Event.ChatterUserLogin,
				Message:     message.Payload.Event.Message.Text,
				OccurredAt:  message.Metadata.MessageTimestamp,
			}
			if err := receive(twitchStreamSignal{Event: &event}); err != nil {
				return err
			}
		case "session_reconnect":
			if message.Payload.Session.ReconnectURL == nil {
				return &twitchStreamError{err: errors.New("twitch reconnect URL is missing"), retryable: true}
			}
			next, nextWelcome, err := a.handoff(ctx, connection, *message.Payload.Session.ReconnectURL, receive)
			if err != nil {
				retryable := !errors.Is(err, errTwitchAuthRevoked) && !errors.Is(err, errTwitchSubRevoked)
				return &twitchStreamError{err: err, retryable: retryable}
			}
			connection = next
			keepalive = twitchKeepaliveTimeout(nextWelcome.Payload.Session.KeepaliveTimeoutSeconds)
			if err := receive(twitchStreamSignal{Ready: true, Reconnected: true}); err != nil {
				return err
			}
		case "revocation":
			if message.Payload.Subscription.Status == "authorization_revoked" {
				return &twitchStreamError{err: errTwitchAuthRevoked, retryable: false}
			}
			return &twitchStreamError{err: errTwitchSubRevoked, retryable: false}
		}
	}
}

type twitchHandoffResult struct {
	connection twitchWebSocket
	welcome    twitchEventSubMessage
	err        error
}

// handoff keeps consuming the old socket until the replacement sends Welcome.
func (a *twitchNetworkAPI) handoff(ctx context.Context, old twitchWebSocket, endpoint string, receive func(twitchStreamSignal) error) (twitchWebSocket, twitchEventSubMessage, error) {
	results := make(chan twitchHandoffResult, 1)
	go func() {
		connection, welcome, err := a.connect(ctx, endpoint)
		results <- twitchHandoffResult{connection: connection, welcome: welcome, err: err}
	}()
	readerCtx, readerCancel := context.WithCancel(ctx)
	defer readerCancel()
	messages := make(chan []byte)
	go func() {
		defer close(messages)
		for {
			data, err := old.Read(35 * time.Second)
			if err != nil {
				return
			}
			select {
			case messages <- data:
			case <-readerCtx.Done():
				return
			}
		}
	}()

	for {
		select {
		case data, ok := <-messages:
			if !ok {
				readerCancel()
				result := <-results
				_ = old.Close()
				return result.connection, result.welcome, result.err
			}
			if err := deliverTwitchNotification(data, receive); err != nil {
				_ = old.Close()
				readerCancel()
				for range messages {
				}
				result := <-results
				if result.connection != nil {
					_ = result.connection.Close()
				}
				return nil, twitchEventSubMessage{}, err
			}
		case result := <-results:
			// Welcome is the handoff boundary. Stop the old reader, then drain
			// everything it had already read before switching connections.
			_ = old.Close()
			var deliveryErr error
			for data := range messages {
				if deliveryErr == nil {
					deliveryErr = deliverTwitchNotification(data, receive)
				}
			}
			readerCancel()
			if deliveryErr != nil {
				if result.connection != nil {
					_ = result.connection.Close()
				}
				return nil, twitchEventSubMessage{}, deliveryErr
			}
			return result.connection, result.welcome, result.err
		}
	}
}

func deliverTwitchNotification(data []byte, receive func(twitchStreamSignal) error) error {
	message, err := decodeTwitchMessage(data)
	if err != nil {
		return err
	}
	if message.Metadata.MessageType == "revocation" {
		if message.Payload.Subscription.Status == "authorization_revoked" {
			return errTwitchAuthRevoked
		}
		return errTwitchSubRevoked
	}
	if message.Metadata.MessageType != "notification" || message.Metadata.SubscriptionType != "channel.chat.message" || message.Metadata.MessageID == "" {
		return nil
	}
	return receive(twitchStreamSignal{Event: &TwitchEvent{
		ID:          message.Metadata.MessageID,
		Author:      message.Payload.Event.ChatterUserName,
		AuthorLogin: message.Payload.Event.ChatterUserLogin,
		Message:     message.Payload.Event.Message.Text,
		OccurredAt:  message.Metadata.MessageTimestamp,
	}})
}

func (a *twitchNetworkAPI) connect(ctx context.Context, endpoint string) (twitchWebSocket, twitchEventSubMessage, error) {
	if err := validateTwitchEventSubURL(endpoint); err != nil {
		return nil, twitchEventSubMessage{}, err
	}
	connection, err := a.dial(ctx, endpoint)
	if err != nil {
		return nil, twitchEventSubMessage{}, err
	}
	data, err := connection.Read(15 * time.Second)
	if err != nil {
		_ = connection.Close()
		return nil, twitchEventSubMessage{}, err
	}
	welcome, err := decodeTwitchMessage(data)
	if err != nil || welcome.Metadata.MessageType != "session_welcome" || welcome.Payload.Session.ID == "" {
		_ = connection.Close()
		return nil, twitchEventSubMessage{}, errors.New("twitch EventSub welcome message is invalid")
	}
	return connection, welcome, nil
}

func twitchKeepaliveTimeout(seconds int) time.Duration {
	if seconds < 10 || seconds > 600 {
		seconds = 30
	}
	return time.Duration(seconds+5) * time.Second
}

type gorillaTwitchWebSocket struct {
	connection *websocket.Conn
	done       chan struct{}
	once       sync.Once
}

func (a *twitchNetworkAPI) dialWebSocket(ctx context.Context, endpoint string) (twitchWebSocket, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	connection, response, err := dialer.DialContext(ctx, endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	connection.SetReadLimit(twitchMaxResponseSize)
	socket := &gorillaTwitchWebSocket{connection: connection, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = socket.Close()
		case <-socket.done:
		}
	}()
	return socket, nil
}

func (s *gorillaTwitchWebSocket) Read(timeout time.Duration) ([]byte, error) {
	if err := s.connection.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	_, data, err := s.connection.ReadMessage()
	return data, err
}

func (s *gorillaTwitchWebSocket) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.connection.Close()
	})
	return err
}

func hasTwitchScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

func retryableTwitchHTTPError(err error) bool {
	var apiErr *twitchHTTPError
	if !errors.As(err, &apiErr) {
		return true
	}
	switch apiErr.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}
