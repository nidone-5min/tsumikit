package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	youtubeReadonlyScope = "https://www.googleapis.com/auth/youtube.readonly"
	oauthCallbackPath    = "/oauth/google/callback"
	oauthTimeout         = 5 * time.Minute
)

type oauthCallbackResult struct {
	code string
	err  error
}

func validateGoogleClientID(clientID string) error {
	if len(clientID) < 20 || len(clientID) > 256 || !strings.HasSuffix(clientID, ".apps.googleusercontent.com") {
		return errors.New("Google Desktop OAuthのクライアントIDを入力してください")
	}
	if strings.TrimSpace(clientID) != clientID || strings.ContainsAny(clientID, "\r\n\t /\\") {
		return errors.New("Google OAuthクライアントIDの形式が正しくありません")
	}
	return nil
}

func newOAuthState() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func googleOAuthConfig(clientID, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:    clientID,
		Endpoint:    google.Endpoint,
		RedirectURL: redirectURL,
		Scopes:      []string{youtubeReadonlyScope},
	}
}

func newOAuthCallbackHandler(expectedHost, expectedState string, results chan<- oauthCallbackResult) http.Handler {
	var accepted sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet || r.Host != expectedHost || r.URL.Path != oauthCallbackPath || r.URL.EscapedPath() != oauthCallbackPath {
			http.Error(w, "invalid callback", http.StatusBadRequest)
			return
		}

		state := r.URL.Query().Get("state")
		if len(state) != len(expectedState) || subtle.ConstantTimeCompare([]byte(state), []byte(expectedState)) != 1 {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}

		delivered := false
		callbackFailed := false
		accepted.Do(func() {
			delivered = true
			if oauthError := r.URL.Query().Get("error"); oauthError != "" {
				callbackFailed = true
				results <- oauthCallbackResult{err: errors.New("Google認証がキャンセルされました")}
				return
			}
			code := r.URL.Query().Get("code")
			if code == "" || len(code) > 4096 || strings.ContainsAny(code, "\r\n") {
				callbackFailed = true
				results <- oauthCallbackResult{err: errors.New("Google認証コードを受け取れませんでした")}
				return
			}
			results <- oauthCallbackResult{code: code}
		})
		if !delivered {
			http.Error(w, "callback already used", http.StatusConflict)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if callbackFailed {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8><title>tsumikit</title><style>body{font-family:system-ui;padding:3rem;background:#101217;color:#f6f7f9}</style><h1>認証を完了できませんでした</h1><p>このタブを閉じてtsumikitからもう一度お試しください。</p>"))
			return
		}
		_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8><title>tsumikit</title><style>body{font-family:system-ui;padding:3rem;background:#101217;color:#f6f7f9}</style><h1>認証結果を受け取りました</h1><p>このタブを閉じてtsumikitへ戻ってください。</p>"))
	})
}

func startOAuthListener() (net.Listener, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("Google認証用のローカル接続を開始できませんでした: %w", err)
	}
	return listener, nil
}

func shutdownOAuthServer(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}
