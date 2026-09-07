package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestOAuthAuthorizationURLUsesPKCES256AndNoSecret(t *testing.T) {
	t.Parallel()
	config := googleOAuthConfig("123456789-example.apps.googleusercontent.com", "http://127.0.0.1:49152"+oauthCallbackPath)
	verifier := oauth2.GenerateVerifier()
	authURL := config.AuthCodeURL("state-value", oauth2.S256ChallengeOption(verifier))
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		t.Fatalf("authorization URL does not use PKCE S256: %s", authURL)
	}
	if query.Get("state") != "state-value" {
		t.Fatalf("state = %q, want state-value", query.Get("state"))
	}
	if query.Get("scope") != youtubeReadonlyScope {
		t.Fatalf("scope = %q, want read-only YouTube scope", query.Get("scope"))
	}
	if strings.Contains(authURL, "client_secret") || strings.Contains(authURL, verifier) {
		t.Fatal("authorization URL exposed a client secret or PKCE verifier")
	}
}

func TestOAuthCallbackValidatesHostPathStateAndSingleUse(t *testing.T) {
	t.Parallel()
	results := make(chan oauthCallbackResult, 1)
	handler := newOAuthCallbackHandler("127.0.0.1:49152", "expected-state", results)

	tests := []struct {
		name   string
		target string
		host   string
	}{
		{name: "wrong host", target: oauthCallbackPath + "?state=expected-state&code=code", host: "localhost:49152"},
		{name: "wrong path", target: "/other?state=expected-state&code=code", host: "127.0.0.1:49152"},
		{name: "wrong state", target: oauthCallbackPath + "?state=wrong&code=code", host: "127.0.0.1:49152"},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, test.target, nil)
		request.Host = test.host
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", test.name, recorder.Code)
		}
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, oauthCallbackPath+"?state=expected-state&code=one-time-code", nil)
	request.Host = "127.0.0.1:49152"
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid callback status = %d, want 200", recorder.Code)
	}
	if result := <-results; result.err != nil || result.code != "one-time-code" {
		t.Fatalf("callback result = %#v", result)
	}
	if strings.Contains(recorder.Body.String(), "one-time-code") {
		t.Fatal("callback response exposed the authorization code")
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request)
	if second.Code != http.StatusConflict {
		t.Fatalf("second callback status = %d, want 409", second.Code)
	}
}

func TestOAuthCallbackReportsDenialWithoutEchoingProviderInput(t *testing.T) {
	t.Parallel()
	results := make(chan oauthCallbackResult, 1)
	handler := newOAuthCallbackHandler("127.0.0.1:49152", "state", results)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, oauthCallbackPath+"?state=state&error=access_denied&error_description=sensitive", nil)
	request.Host = "127.0.0.1:49152"
	handler.ServeHTTP(recorder, request)
	result := <-results
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("denied callback status = %d, want 400", recorder.Code)
	}
	if result.err == nil || strings.Contains(result.err.Error(), "sensitive") || strings.Contains(recorder.Body.String(), "sensitive") {
		t.Fatalf("denial result exposed provider input: %v", result.err)
	}
}

func TestYouTubeAuthTimeoutClosesLoopbackListener(t *testing.T) {
	opened := make(chan string, 1)
	service := newYouTubeService(youtubeServiceDependencies{
		authTimeout: 30 * time.Millisecond,
		openURL: func(_ context.Context, target string) {
			opened <- target
		},
	})
	if err := service.BeginAuth(context.Background(), "123456789-example.apps.googleusercontent.com"); err != nil {
		t.Fatal(err)
	}
	authURL := <-opened
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	redirectURL, err := url.Parse(parsed.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	if redirectURL.Hostname() != "127.0.0.1" || redirectURL.Path != oauthCallbackPath {
		t.Fatalf("redirect URL = %q, want IPv4 loopback callback", redirectURL.String())
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if service.Status().State == youtubeStateError {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status := service.Status(); status.State != youtubeStateError || !strings.Contains(status.Error, "タイムアウト") {
		t.Fatalf("status after timeout = %#v", status)
	}

	for time.Now().Before(deadline) {
		connection, dialErr := net.DialTimeout("tcp4", redirectURL.Host, 20*time.Millisecond)
		if dialErr != nil {
			return
		}
		_ = connection.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("OAuth loopback listener remained open after timeout")
}
