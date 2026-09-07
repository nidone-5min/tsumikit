package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func startTestOverlayServer(t *testing.T) *OverlayServer {
	t.Helper()

	server := NewOverlayServer()
	if err := server.Start(0); err != nil {
		t.Fatalf("Start(0): %v", err)
	}
	t.Cleanup(func() {
		if err := server.Stop(context.Background()); err != nil {
			t.Errorf("Stop(): %v", err)
		}
	})
	return server
}

func dialOverlay(t *testing.T, server *OverlayServer) *websocket.Conn {
	t.Helper()

	status := server.Status()
	websocketURL := strings.Replace(status.URL, "http://", "ws://", 1)
	websocketURL = strings.Replace(websocketURL, overlayPathPrefix, websocketPathPrefix, 1)
	headers := http.Header{"Origin": []string{overlayOrigin(t, status.URL)}}
	connection, response, err := websocket.DefaultDialer.Dial(websocketURL, headers)
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatalf("Dial(%q): %v", websocketURL, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func markOverlayReady(t *testing.T, server *OverlayServer, connection *websocket.Conn) {
	t.Helper()
	if err := connection.WriteJSON(clientMessage{Type: "ready", SDKVersion: overlaySDKVersion}); err != nil {
		t.Fatalf("write ready message: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.mu.Lock()
		ready := false
		for client := range server.clients {
			ready = ready || client.ready
		}
		server.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("overlay client did not become ready")
}

func overlayOrigin(t *testing.T, overlayURL string) string {
	t.Helper()
	parsed, err := url.Parse(overlayURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", overlayURL, err)
	}
	return parsed.Scheme + "://" + parsed.Host
}

func newTestHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
}

func waitForConnections(t *testing.T, server *OverlayServer, count int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if server.Status().Connections == count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connections = %d, want %d", server.Status().Connections, count)
}

func TestOverlayServerBindsOnlyIPv4Loopback(t *testing.T) {
	server := startTestOverlayServer(t)

	server.mu.Lock()
	address := server.listener.Addr().(*net.TCPAddr)
	server.mu.Unlock()
	if !address.IP.IsLoopback() || address.IP.To4() == nil {
		t.Fatalf("listener address = %v, want IPv4 loopback", address)
	}
}

func TestOverlayServerServesOverlayHTML(t *testing.T) {
	server := startTestOverlayServer(t)
	client := newTestHTTPClient()

	response, err := client.Get(server.Status().URL)
	if err != nil {
		t.Fatalf("GET overlay: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	if got := response.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	if got := response.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := response.Header.Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin", got)
	}
	if got := response.Header.Get("Permissions-Policy"); !strings.Contains(got, "camera=()") || !strings.Contains(got, "autoplay=(self)") {
		t.Errorf("Permissions-Policy = %q, want denied sensitive features and same-origin autoplay", got)
	}
	csp := response.Header.Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'none'",
		"connect-src 'self' ws://127.0.0.1:",
		"worker-src 'none'",
		"webrtc 'block'",
		"frame-ancestors 'none'",
		"sandbox allow-scripts allow-same-origin",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("Content-Security-Policy = %q, missing %q", csp, directive)
		}
	}
	if !strings.Contains(string(body), "tsumikit overlay") {
		t.Error("overlay HTML does not contain its title")
	}
	if !strings.Contains(string(body), `<script src="./sdk.js"></script>`) {
		t.Error("overlay HTML does not load the fixed SDK asset")
	}
}

func TestOverlayServerServesCapabilityScopedSDK(t *testing.T) {
	server := startTestOverlayServer(t)
	client := newTestHTTPClient()

	response, err := client.Get(server.Status().URL + "sdk.js")
	if err != nil {
		t.Fatalf("GET sdk.js: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatalf("read sdk.js: %v", readErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("Content-Type = %q, want text/javascript", got)
	}
	if !strings.Contains(string(body), "OverlaySDK") {
		t.Error("SDK asset does not define OverlaySDK")
	}

	status := server.Status()
	wrongURL := fmt.Sprintf("http://127.0.0.1:%d%swrong-capability/sdk.js", status.Port, overlayPathPrefix)
	wrongResponse, err := client.Get(wrongURL)
	if err != nil {
		t.Fatalf("GET wrong SDK URL: %v", err)
	}
	_ = wrongResponse.Body.Close()
	if wrongResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong SDK status = %d, want %d", wrongResponse.StatusCode, http.StatusNotFound)
	}
}

func TestOverlayServerRequiresCapability(t *testing.T) {
	server := startTestOverlayServer(t)
	status := server.Status()
	client := newTestHTTPClient()

	for _, path := range []string{"/overlay", overlayPathPrefix + "wrong-capability"} {
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", status.Port, path))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want %d", path, response.StatusCode, http.StatusNotFound)
		}
	}
}

func TestOverlayServerRejectsUnexpectedHost(t *testing.T) {
	server := startTestOverlayServer(t)
	request, err := http.NewRequest(http.MethodGet, server.Status().URL, nil)
	if err != nil {
		t.Fatalf("NewRequest(): %v", err)
	}
	request.Host = "attacker.example"
	response, err := newTestHTTPClient().Do(request)
	if err != nil {
		t.Fatalf("Do(): %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
}

func TestOverlayServerRejectsUnexpectedWebSocketOrigin(t *testing.T) {
	server := startTestOverlayServer(t)
	status := server.Status()
	websocketURL := strings.Replace(status.URL, "http://", "ws://", 1)
	websocketURL = strings.Replace(websocketURL, overlayPathPrefix, websocketPathPrefix, 1)

	for _, origin := range []string{"", "https://attacker.example"} {
		headers := http.Header{}
		if origin != "" {
			headers.Set("Origin", origin)
		}
		connection, response, err := websocket.DefaultDialer.Dial(websocketURL, headers)
		if connection != nil {
			_ = connection.Close()
		}
		if err == nil {
			t.Fatalf("Dial() succeeded with origin %q", origin)
		}
		if response == nil || response.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %q response = %#v, want status %d", origin, response, http.StatusForbidden)
		}
		_ = response.Body.Close()
	}
}

func TestOverlayServerRejectsWrongWebSocketCapability(t *testing.T) {
	server := startTestOverlayServer(t)
	status := server.Status()
	websocketURL := fmt.Sprintf("ws://127.0.0.1:%d%swrong-capability", status.Port, websocketPathPrefix)
	headers := http.Header{"Origin": []string{overlayOrigin(t, status.URL)}}
	connection, response, err := websocket.DefaultDialer.Dial(websocketURL, headers)
	if connection != nil {
		_ = connection.Close()
	}
	if err == nil {
		t.Fatal("Dial() succeeded with an invalid capability")
	}
	if response == nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("response = %#v, want status %d", response, http.StatusNotFound)
	}
	_ = response.Body.Close()
}

func TestOverlayServerRotatesCapabilityAfterRestart(t *testing.T) {
	server := startTestOverlayServer(t)
	oldURL := server.Status().URL
	port := server.Status().Port
	if err := server.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(): %v", err)
	}
	if err := server.Start(port); err != nil {
		t.Fatalf("Start(%d): %v", port, err)
	}
	newURL := server.Status().URL
	if newURL == oldURL {
		t.Fatal("capability URL did not rotate after restart")
	}

	response, err := newTestHTTPClient().Get(oldURL)
	if err != nil {
		t.Fatalf("GET old URL: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("old URL status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
}

func TestOverlayServerExpiresClientWithoutPong(t *testing.T) {
	server := NewOverlayServer()
	server.pingEvery = 20 * time.Millisecond
	server.leaseFor = 80 * time.Millisecond
	if err := server.Start(0); err != nil {
		t.Fatalf("Start(0): %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	connection := dialOverlay(t, server)
	waitForConnections(t, server, 1)

	// The client deliberately does not read, so it never processes ping frames or sends pong frames.
	waitForConnections(t, server, 0)
	_ = connection.Close()
}

func TestOverlayServerRenewsLeaseOnPong(t *testing.T) {
	server := NewOverlayServer()
	server.pingEvery = 20 * time.Millisecond
	server.leaseFor = 120 * time.Millisecond
	if err := server.Start(0); err != nil {
		t.Fatalf("Start(0): %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	connection := dialOverlay(t, server)
	waitForConnections(t, server, 1)

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				return
			}
		}
	}()
	time.Sleep(350 * time.Millisecond)
	if got := server.Status().Connections; got != 1 {
		t.Fatalf("connections = %d, want 1 while pong responses renew the lease", got)
	}
	_ = connection.Close()
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client reader did not stop")
	}
}

func TestOverlayServerBroadcastsTestEvent(t *testing.T) {
	server := startTestOverlayServer(t)
	connection := dialOverlay(t, server)
	waitForConnections(t, server, 1)
	markOverlayReady(t, server, connection)

	if err := server.SendTestEvent("hello OBS"); err != nil {
		t.Fatalf("SendTestEvent(): %v", err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline(): %v", err)
	}
	_, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage(): %v", err)
	}
	var message triggerMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("Unmarshal(): %v", err)
	}
	if message.Type != "trigger" || message.Event.ID == "" || message.Event.Type != "test" ||
		message.Event.Message != "hello OBS" || message.Event.SentAt.IsZero() {
		t.Fatalf("message = %#v", message)
	}

	if err := connection.WriteJSON(clientMessage{Type: "complete", EventID: message.Event.ID}); err != nil {
		t.Fatalf("write complete message: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.mu.Lock()
		activeID := ""
		for client := range server.clients {
			activeID = client.activeID
		}
		server.mu.Unlock()
		if activeID == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("complete message did not clear the active event")
}

func TestOverlayServerRequiresReadyBeforeTrigger(t *testing.T) {
	server := startTestOverlayServer(t)
	_ = dialOverlay(t, server)
	waitForConnections(t, server, 1)

	if err := server.SendTestEvent("not ready"); !errors.Is(err, errNoReadyClients) {
		t.Fatalf("SendTestEvent() error = %v, want %v", err, errNoReadyClients)
	}
}

func TestOverlayServerRejectsInvalidSDKMessages(t *testing.T) {
	server := startTestOverlayServer(t)
	connection := dialOverlay(t, server)
	waitForConnections(t, server, 1)

	if err := connection.WriteJSON(map[string]any{
		"type": "ready", "sdkVersion": overlaySDKVersion, "unexpected": true,
	}); err != nil {
		t.Fatalf("write invalid SDK message: %v", err)
	}
	waitForConnections(t, server, 0)
}

func TestOverlayServerIgnoresCompletionForAnotherEvent(t *testing.T) {
	server := startTestOverlayServer(t)
	connection := dialOverlay(t, server)
	waitForConnections(t, server, 1)
	markOverlayReady(t, server, connection)
	if err := server.SendTestEvent("active event"); err != nil {
		t.Fatalf("SendTestEvent(): %v", err)
	}

	if err := connection.WriteJSON(clientMessage{Type: "complete", EventID: "another-event"}); err != nil {
		t.Fatalf("write complete message: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	server.mu.Lock()
	activeID := ""
	for client := range server.clients {
		activeID = client.activeID
	}
	server.mu.Unlock()
	if activeID == "" {
		t.Fatal("completion for another event cleared the active event")
	}
}

func TestOverlayServerRejectsOverlappingTrigger(t *testing.T) {
	server := startTestOverlayServer(t)
	connection := dialOverlay(t, server)
	waitForConnections(t, server, 1)
	markOverlayReady(t, server, connection)
	if err := server.SendTestEvent("first"); err != nil {
		t.Fatalf("first SendTestEvent(): %v", err)
	}
	if err := server.SendTestEvent("second"); !errors.Is(err, errOverlayBusy) {
		t.Fatalf("second SendTestEvent() error = %v, want %v", err, errOverlayBusy)
	}
}

func TestOverlayServerExpiresIncompleteTrigger(t *testing.T) {
	server := NewOverlayServer()
	server.eventFor = 20 * time.Millisecond
	if err := server.Start(0); err != nil {
		t.Fatalf("Start(0): %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	connection := dialOverlay(t, server)
	waitForConnections(t, server, 1)
	markOverlayReady(t, server, connection)
	if err := server.SendTestEvent("first"); err != nil {
		t.Fatalf("first SendTestEvent(): %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := server.SendTestEvent("after timeout"); err != nil {
		t.Fatalf("SendTestEvent() after timeout: %v", err)
	}
}

func TestOverlayServerReportsPortConflict(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(): %v", err)
	}
	defer listener.Close()

	server := NewOverlayServer()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := server.Start(port); err == nil {
		t.Fatal("Start() returned nil for an occupied port")
	}
	status := server.Status()
	if status.Running || !strings.Contains(status.Error, "ほかのポート") {
		t.Fatalf("status = %#v", status)
	}
}

func TestOverlayServerStopReleasesPort(t *testing.T) {
	server := startTestOverlayServer(t)
	port := server.Status().Port
	if err := server.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(): %v", err)
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("port %d was not released: %v", port, err)
	}
	_ = listener.Close()
}

func TestOverlayServerStopDisconnectsClients(t *testing.T) {
	server := startTestOverlayServer(t)
	connection := dialOverlay(t, server)
	waitForConnections(t, server, 1)

	if err := server.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(): %v", err)
	}
	if server.Status().Connections != 0 {
		t.Fatalf("connections = %d, want 0", server.Status().Connections)
	}
	if err := connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline(): %v", err)
	}
	if _, _, err := connection.ReadMessage(); err == nil {
		t.Fatal("ReadMessage() returned nil after server stopped")
	}
}

func TestOverlayServerSendRequiresConnectedClient(t *testing.T) {
	server := startTestOverlayServer(t)
	if err := server.SendTestEvent("test"); err == nil {
		t.Fatal("SendTestEvent() returned nil without clients")
	}
}

func TestOverlayServerRejectsLongTestMessage(t *testing.T) {
	server := startTestOverlayServer(t)
	if err := server.SendTestEvent(strings.Repeat("あ", 201)); err == nil {
		t.Fatal("SendTestEvent() returned nil for a message over 200 characters")
	}
}
