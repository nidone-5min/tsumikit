package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	overlayPath           = "/overlay"
	websocketPath         = "/ws"
	serverShutdownTimeout = 3 * time.Second
	clientWriteTimeout    = 2 * time.Second
	clientReadLimit       = 64 * 1024
)

var (
	errOverlayNotRunning = errors.New("オーバーレイサーバーは停止しています")
	errNoOverlayClients  = errors.New("接続中のOBS Browser Sourceがありません")
)

//go:embed overlay/index.html
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
	Type    string    `json:"type"`
	Message string    `json:"message"`
	SentAt  time.Time `json:"sentAt"`
}

type overlayClient struct {
	connection *websocket.Conn
	writeMu    sync.Mutex
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
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	_ = c.connection.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
	_ = c.connection.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server stopped"),
		time.Now().Add(clientWriteTimeout),
	)
	_ = c.connection.Close()
}

// OverlayServer owns the loopback HTTP server and connected Browser Sources.
type OverlayServer struct {
	mu         sync.Mutex
	httpServer *http.Server
	listener   net.Listener
	clients    map[*overlayClient]struct{}
	port       int
	lastError  string
}

// NewOverlayServer constructs an idle overlay server.
func NewOverlayServer() *OverlayServer {
	return &OverlayServer{clients: make(map[*overlayClient]struct{})}
}

// Start starts the server on IPv4 loopback only.
func (s *OverlayServer) Start(port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.httpServer != nil {
		return errors.New("オーバーレイサーバーはすでに起動しています")
	}

	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		friendlyErr := fmt.Errorf("ポート%dを使用できません。ほかのポートを指定してください: %w", port, err)
		s.lastError = friendlyErr.Error()
		return friendlyErr
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	mux.HandleFunc(overlayPath, s.serveOverlay)
	mux.HandleFunc(websocketPath, func(w http.ResponseWriter, r *http.Request) {
		s.serveWebSocket(server, w, r)
	})

	s.listener = listener
	s.httpServer = server
	s.port = actualPort
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
		status.URL = fmt.Sprintf("http://127.0.0.1:%d%s", status.Port, overlayPath)
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

	event, err := json.Marshal(TestEvent{Type: "test", Message: message, SentAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("テストイベントを作成できませんでした: %w", err)
	}

	s.mu.Lock()
	if s.httpServer == nil {
		s.mu.Unlock()
		return errOverlayNotRunning
	}
	clients := make([]*overlayClient, 0, len(s.clients))
	for client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()
	if len(clients) == 0 {
		return errNoOverlayClients
	}

	var firstErr error
	for _, client := range clients {
		if err := client.write(event); err != nil {
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

func (s *OverlayServer) serveOverlay(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != overlayPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	content, err := overlayFiles.ReadFile("overlay/index.html")
	if err != nil {
		http.Error(w, "overlay unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	_, _ = w.Write(content)
}

var websocketUpgrader = websocket.Upgrader{
	EnableCompression: false,
}

func (s *OverlayServer) serveWebSocket(owner *http.Server, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	connection, err := websocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &overlayClient{connection: connection}
	connection.SetReadLimit(clientReadLimit)
	if !s.addClient(owner, client) {
		client.close()
		return
	}
	defer func() {
		s.removeClient(client)
		_ = connection.Close()
	}()

	for {
		if _, _, err := connection.ReadMessage(); err != nil {
			return
		}
	}
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
