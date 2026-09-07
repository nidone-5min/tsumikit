package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	overlayPathPrefix     = "/overlay/"
	websocketPathPrefix   = "/ws/"
	serverShutdownTimeout = 3 * time.Second
	clientWriteTimeout    = 2 * time.Second
	clientReadLimit       = 64 * 1024
	clientPingInterval    = 15 * time.Second
	clientLeaseTimeout    = 45 * time.Second
	eventCompletionLimit  = 30 * time.Second
	capabilityBytes       = 32
	eventIDBytes          = 16
	overlaySDKVersion     = "1"
)

var (
	errOverlayNotRunning = errors.New("オーバーレイサーバーは停止しています")
	errNoOverlayClients  = errors.New("接続中のOBS Browser Sourceがありません")
	errNoReadyClients    = errors.New("接続中のOBS Browser Sourceがまだ準備完了していません")
	errOverlayBusy       = errors.New("前のテスト演出がまだ完了していません")
)

//go:embed overlay/index.html overlay/sdk.js
var overlayFiles embed.FS

// OverlayStatus is returned to the Wails frontend.
type OverlayStatus struct {
	Running     bool   `json:"running"`
	Port        int    `json:"port"`
	URL         string `json:"url"`
	Connections int    `json:"connections"`
	Error       string `json:"error"`
}

// TestEvent is sent to connected overlay clients.
type TestEvent struct {
	ID      string    `json:"id"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
	SentAt  time.Time `json:"sentAt"`
}

type triggerMessage struct {
	Type  string    `json:"type"`
	Event TestEvent `json:"event"`
}

type clientMessage struct {
	Type       string `json:"type"`
	SDKVersion string `json:"sdkVersion,omitempty"`
	EventID    string `json:"eventId,omitempty"`
}

type overlayClient struct {
	connection  *websocket.Conn
	writeMu     sync.Mutex
	done        chan struct{}
	closeOnce   sync.Once
	ready       bool
	activeID    string
	activeUntil time.Time
}

func (c *overlayClient) write(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if err := c.connection.SetWriteDeadline(time.Now().Add(clientWriteTimeout)); err != nil {
		return err
	}
	return c.connection.WriteMessage(websocket.TextMessage, payload)
}

func (c *overlayClient) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.writeMu.Lock()
		defer c.writeMu.Unlock()

		_ = c.connection.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server stopped"),
			time.Now().Add(clientWriteTimeout),
		)
		_ = c.connection.Close()
	})
}

func (c *overlayClient) ping() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.connection.WriteControl(
		websocket.PingMessage,
		nil,
		time.Now().Add(clientWriteTimeout),
	)
}

// OverlayServer owns the loopback HTTP server and connected Browser Sources.
type OverlayServer struct {
	mu         sync.Mutex
	httpServer *http.Server
	listener   net.Listener
	clients    map[*overlayClient]struct{}
	port       int
	capability string
	lastError  string
	pingEvery  time.Duration
	leaseFor   time.Duration
	eventFor   time.Duration
}

// NewOverlayServer constructs an idle overlay server.
func NewOverlayServer() *OverlayServer {
	return &OverlayServer{
		clients:   make(map[*overlayClient]struct{}),
		pingEvery: clientPingInterval,
		leaseFor:  clientLeaseTimeout,
		eventFor:  eventCompletionLimit,
	}
}

// Start starts the server on IPv4 loopback only.
func (s *OverlayServer) Start(port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.httpServer != nil {
		return errors.New("オーバーレイサーバーはすでに起動しています")
	}
	capability, err := newCapability()
	if err != nil {
		friendlyErr := fmt.Errorf("安全なOBS用URLを作成できませんでした: %w", err)
		s.lastError = friendlyErr.Error()
		return friendlyErr
	}

	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		friendlyErr := fmt.Errorf("ポート%dを使用できません。ほかのポートを指定してください: %w", port, err)
		s.lastError = friendlyErr.Error()
		return friendlyErr
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port
	authority := fmt.Sprintf("127.0.0.1:%d", actualPort)
	mux := http.NewServeMux()
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	mux.HandleFunc(overlayPathPrefix, func(w http.ResponseWriter, r *http.Request) {
		s.serveOverlay(authority, capability, w, r)
	})
	mux.HandleFunc(strings.TrimSuffix(overlayPathPrefix, "/"), http.NotFound)
	mux.HandleFunc(websocketPathPrefix, func(w http.ResponseWriter, r *http.Request) {
		s.serveWebSocket(server, authority, capability, w, r)
	})
	mux.HandleFunc(strings.TrimSuffix(websocketPathPrefix, "/"), http.NotFound)

	s.listener = listener
	s.httpServer = server
	s.port = actualPort
	s.capability = capability
	s.lastError = ""
	go s.serve(server, listener)
	return nil
}

func (s *OverlayServer) serve(server *http.Server, listener net.Listener) {
	err := server.Serve(listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return
	}

	var clients []*overlayClient
	s.mu.Lock()
	if s.httpServer == server {
		s.lastError = fmt.Sprintf("オーバーレイサーバーが停止しました: %v", err)
		s.httpServer = nil
		s.listener = nil
		s.port = 0
		s.capability = ""
		clients = make([]*overlayClient, 0, len(s.clients))
		for client := range s.clients {
			clients = append(clients, client)
		}
		s.clients = make(map[*overlayClient]struct{})
	}
	s.mu.Unlock()
	for _, client := range clients {
		client.close()
	}
}

// Stop disconnects clients and shuts the server down.
func (s *OverlayServer) Stop(parent context.Context) error {
	s.mu.Lock()
	server := s.httpServer
	listener := s.listener
	clients := make([]*overlayClient, 0, len(s.clients))
	for client := range s.clients {
		clients = append(clients, client)
	}
	s.httpServer = nil
	s.listener = nil
	s.port = 0
	s.capability = ""
	s.clients = make(map[*overlayClient]struct{})
	if listener != nil {
		_ = listener.Close()
	}
	s.mu.Unlock()

	if server == nil {
		return nil
	}

	for _, client := range clients {
		client.close()
	}
	ctx, cancel := context.WithTimeout(parent, serverShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil &&
		!errors.Is(err, http.ErrServerClosed) &&
		!errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("オーバーレイサーバーを停止できませんでした: %w", err)
	}
	return nil
}

// Status returns a consistent snapshot of server state.
func (s *OverlayServer) Status() OverlayStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	status := OverlayStatus{
		Running:     s.httpServer != nil,
		Port:        s.port,
		Connections: len(s.clients),
		Error:       s.lastError,
	}
	if status.Running {
		status.URL = fmt.Sprintf("http://127.0.0.1:%d%s%s/", status.Port, overlayPathPrefix, s.capability)
	}
	return status
}

// SendTestEvent broadcasts a bounded test message to every connected client.
func (s *OverlayServer) SendTestEvent(message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "tsumikitからテストイベントを受信しました"
	}
	if len([]rune(message)) > 200 {
		return errors.New("テストメッセージは200文字以内で入力してください")
	}

	eventID, err := newEventID()
	if err != nil {
		return fmt.Errorf("テストイベントIDを作成できませんでした: %w", err)
	}
	event := TestEvent{ID: eventID, Type: "test", Message: message, SentAt: time.Now().UTC()}
	payload, err := json.Marshal(triggerMessage{Type: "trigger", Event: event})
	if err != nil {
		return fmt.Errorf("テストイベントを作成できませんでした: %w", err)
	}

	s.mu.Lock()
	if s.httpServer == nil {
		s.mu.Unlock()
		return errOverlayNotRunning
	}
	clients := make([]*overlayClient, 0, len(s.clients))
	now := time.Now()
	for client := range s.clients {
		if client.activeID != "" && !now.Before(client.activeUntil) {
			client.activeID = ""
			client.activeUntil = time.Time{}
		}
		if client.ready && client.activeID != "" {
			s.mu.Unlock()
			return errOverlayBusy
		}
	}
	for client := range s.clients {
		if client.ready {
			client.activeID = eventID
			client.activeUntil = now.Add(s.eventFor)
			clients = append(clients, client)
		}
	}
	totalClients := len(s.clients)
	s.mu.Unlock()
	if totalClients == 0 {
		return errNoOverlayClients
	}
	if len(clients) == 0 {
		return errNoReadyClients
	}

	var firstErr error
	for _, client := range clients {
		if err := client.write(payload); err != nil {
			s.removeClient(client)
			_ = client.connection.Close()
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return fmt.Errorf("一部のOBS Browser Sourceへ送信できませんでした: %w", firstErr)
	}
	return nil
}

func (s *OverlayServer) serveOverlay(authority, capability string, w http.ResponseWriter, r *http.Request) {
	setOverlaySecurityHeaders(w, authority)
	if !validAuthority(r, authority) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	asset, ok := overlayAsset(r.URL.Path, capability)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	filename := "overlay/index.html"
	contentType := "text/html; charset=utf-8"
	if asset == "sdk.js" {
		filename = "overlay/sdk.js"
		contentType = "text/javascript; charset=utf-8"
	}
	content, err := overlayFiles.ReadFile(filename)
	if err != nil {
		http.Error(w, "overlay unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(content)
}

func setOverlaySecurityHeaders(w http.ResponseWriter, authority string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("X-DNS-Prefetch-Control", "off")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), display-capture=(), payment=(), usb=(), serial=(), hid=(), clipboard-read=(), clipboard-write=(), fullscreen=(self), autoplay=(self)")
	w.Header().Set("Content-Security-Policy", fmt.Sprintf("default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws://%s; img-src 'self' data: blob:; media-src 'self' data: blob:; font-src 'self' data:; worker-src 'none'; webrtc 'block'; child-src 'none'; frame-src 'none'; frame-ancestors 'none'; object-src 'none'; manifest-src 'none'; base-uri 'none'; form-action 'none'; sandbox allow-scripts allow-same-origin", authority))
}

var websocketUpgrader = websocket.Upgrader{
	EnableCompression: false,
}

func (s *OverlayServer) serveWebSocket(owner *http.Server, authority, capability string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !validAuthority(r, authority) || !validOrigin(r.Header.Get("Origin"), authority) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !validCapabilityPath(r.URL.Path, websocketPathPrefix, capability) {
		http.NotFound(w, r)
		return
	}

	connection, err := websocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &overlayClient{connection: connection, done: make(chan struct{})}
	connection.SetReadLimit(clientReadLimit)
	if err := connection.SetReadDeadline(time.Now().Add(s.leaseFor)); err != nil {
		_ = connection.Close()
		return
	}
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(s.leaseFor))
	})
	if !s.addClient(owner, client) {
		client.close()
		return
	}
	defer func() {
		s.removeClient(client)
		client.close()
	}()
	go s.keepAlive(client)

	for {
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage || !s.handleClientMessage(client, payload) {
			return
		}
	}
}

func (s *OverlayServer) handleClientMessage(client *overlayClient, payload []byte) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return false
	}
	typeValue, ok := raw["type"]
	if !ok {
		return false
	}
	var messageType string
	if err := json.Unmarshal(typeValue, &messageType); err != nil {
		return false
	}

	var message clientMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return false
	}
	switch messageType {
	case "ready":
		if !onlyFields(raw, "type", "sdkVersion") || message.SDKVersion != overlaySDKVersion {
			return false
		}
		s.mu.Lock()
		if _, exists := s.clients[client]; exists {
			client.ready = true
		}
		s.mu.Unlock()
		return true
	case "complete":
		if !onlyFields(raw, "type", "eventId") || message.EventID == "" || len(message.EventID) > 128 {
			return false
		}
		s.mu.Lock()
		if !time.Now().Before(client.activeUntil) {
			client.activeID = ""
			client.activeUntil = time.Time{}
		} else if subtle.ConstantTimeCompare([]byte(client.activeID), []byte(message.EventID)) == 1 {
			client.activeID = ""
			client.activeUntil = time.Time{}
		}
		s.mu.Unlock()
		return true
	default:
		return false
	}
}

func onlyFields(raw map[string]json.RawMessage, allowed ...string) bool {
	if len(raw) != len(allowed) {
		return false
	}
	for _, field := range allowed {
		if _, ok := raw[field]; !ok {
			return false
		}
	}
	return true
}

func (s *OverlayServer) keepAlive(client *overlayClient) {
	ticker := time.NewTicker(s.pingEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := client.ping(); err != nil {
				_ = client.connection.Close()
				return
			}
		case <-client.done:
			return
		}
	}
}

func newCapability() (string, error) {
	random := make([]byte, capabilityBytes)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func newEventID() (string, error) {
	random := make([]byte, eventIDBytes)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func validAuthority(r *http.Request, authority string) bool {
	return r.Host == authority
}

func validCapabilityPath(path, prefix, capability string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	provided := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/")
	return subtle.ConstantTimeCompare([]byte(provided), []byte(capability)) == 1
}

func overlayAsset(path, capability string) (string, bool) {
	if !strings.HasPrefix(path, overlayPathPrefix) {
		return "", false
	}
	remainder := strings.TrimPrefix(path, overlayPathPrefix)
	provided, asset, found := strings.Cut(remainder, "/")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(capability)) != 1 {
		return "", false
	}
	if !found || asset == "" {
		return "index.html", true
	}
	if asset == "sdk.js" {
		return asset, true
	}
	return "", false
}

func validOrigin(origin, authority string) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" && parsed.Host == authority && parsed.User == nil &&
		parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func (s *OverlayServer) addClient(owner *http.Server, client *overlayClient) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpServer != owner {
		return false
	}
	s.clients[client] = struct{}{}
	return true
}

func (s *OverlayServer) removeClient(client *overlayClient) {
	s.mu.Lock()
	delete(s.clients, client)
	s.mu.Unlock()
}

func (s *OverlayServer) setLastError(err error) {
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
}
