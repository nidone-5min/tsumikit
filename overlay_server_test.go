package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
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
	url := strings.Replace(status.URL, "http://", "ws://", 1)
	url = strings.TrimSuffix(url, overlayPath) + websocketPath
	connection, response, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatalf("Dial(%q): %v", url, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
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
	client := &http.Client{Timeout: 2 * time.Second}

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
	if !strings.Contains(string(body), "tsumikit overlay") {
		t.Error("overlay HTML does not contain its title")
	}
}

func TestOverlayServerBroadcastsTestEvent(t *testing.T) {
	server := startTestOverlayServer(t)
	connection := dialOverlay(t, server)
	waitForConnections(t, server, 1)

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
	var event TestEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("Unmarshal(): %v", err)
	}
	if event.Type != "test" || event.Message != "hello OBS" || event.SentAt.IsZero() {
		t.Fatalf("event = %#v", event)
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
