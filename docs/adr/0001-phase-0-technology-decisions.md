# ADR-0001: Phase 0の技術選定とPhase 1への移行判断

- Status: Accepted with release gates
- Date: 2026-09-08
- Scope: PR-001〜PR-007で実装したPhase 0 PoC

## Context

tsumikitは、YouTube LiveまたはTwitchからイベントを受け取り、同じ端末のOBS Browser Sourceへ演出を配信するデスクトップアプリである。配布先ごとに認証情報、配信アカウント、OBS構成が異なるため、開発者の単一環境だけで成立する構成にはできない。

Phase 0では、次の技術的な不確実性を小さなPoCで検証した。

- WailsのUIライフサイクルと外部Trayイベントループを共存させられるか
- localhost HTTP／WebSocketをOBS Browser Sourceから利用できるか
- Capability URL、接続lease、CSPでオーバーレイの権限を制限できるか
- YouTube LiveとTwitchの公開デスクトップクライアント向け認証・コメント受信・再接続を実装できるか

このADRはPhase 1以降で使う技術を固定し、Phase 0で確認できたこと、確認できていないこと、再設計条件を記録する。

## Decision

現行構成のままPhase 1へ進む。Wails v3やElectronなどへの移行、Trayの自作、YouTube／Twitch用の別SDKへの置換は現時点では行わない。

ただし、これは製品版の互換性や外部サービスの審査完了を意味しない。後述するOBS実機試験、Windowsライフサイクル試験、OAuth公開準備をリリースゲートとして残す。ゲートを満たせない場合は、影響する部分だけを再設計する。

## 採用技術とバージョン

| 領域                 | 採用                                                           | 固定方法／方針                                                                                |
| -------------------- | -------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| デスクトップシェル   | Wails v2.15.0                                                  | `go.mod`とCIのCLI導入バージョンを一致させる                                                   |
| アプリケーションコア | Go 1.25.0以上                                                  | `go.mod`を最低バージョンとし、CIは`go-version-file`を使用する                                 |
| UI                   | React 19 + TypeScript + Vite 7                                 | 直接依存の許容範囲は`package.json`、再現可能な解決版は`package-lock.json`と`npm ci`で固定する |
| スタイル             | Tailwind CSS 4                                                 | Viteプラグインを使用し、解決版はlockfileで固定する                                            |
| localhost配信        | Go `net/http` + gorilla/websocket v1.5.3                       | IPv4ループバック限定。WebSocket実装をYouTube／Twitch／OBS接続で共用しない                     |
| Tray                 | fyne.io/systray v1.12.2                                        | Wailsの外部イベントループと統合する                                                           |
| YouTube              | google.golang.org/api v0.296.0、gRPC v1.83.2、x/oauth2 v0.36.0 | 公式API定義と標準OAuth部品を利用する                                                          |
| Twitch               | Twitch Helix／IdentityのHTTP API + gorilla/websocket v1.5.3    | Twitch固有の非公式SDKは導入しない                                                             |

Node.jsは24系を開発・CIの基準とする。フロントエンドの正確な推移的依存バージョンは`frontend/package-lock.json`を正とし、依存更新は別PRで脆弱性検査とビルド確認を行う。

## Trayライブラリ

`fyne.io/systray` v1.12.2を継続採用する。

選定理由:

- Windows TrayとmacOSメニューバーの両方を1つのGo APIで扱える。
- `RunWithExternalLoop`を使い、Wailsと同じネイティブUIスレッド上で初期化できる。
- Phase 0で再表示、終了、シングルインスタンス通知、終了時の接続解放を既存のWailsライフサイクルへ統合できた。
- Wails bindings生成時だけTrayを起動しないbuild tagを設け、生成処理とネイティブイベントループを分離できた。

制約:

- Wails v2の組み込み機能ではないため、WailsとOSの更新時にイベントループの回帰試験が必要である。
- Tray初期化に失敗した場合は常駐せず、ウィンドウを閉じて通常終了する安全側の動作を維持する。
- Windowsでの長時間常駐、Explorer再起動後のTray再登録、シャットダウン中の多重操作はリリース前に実機確認する。

次のいずれかが再現する場合はTray実装を再評価する。

- Windowsのサポート対象環境で再表示または終了が安定しない
- Wails更新により外部イベントループとの統合が維持できない
- macOSでアプリ終了時のハングまたはメニューバー項目の残留が解消できない

## OBS／CEF対応範囲

Phase 1の開発基準を、公式配布版OBS Studio 32.2.xに同梱されるBrowser Sourceとする。優先対象はWindows x64、互換維持対象はmacOS Apple Siliconである。外部配布のobs-browser、改造版OBS、OBS 31以前、ベータ／RC版はサポート対象に含めない。

2026-08-07のローカル環境ログでは、次の環境を確認した。

| 項目          | 確認値                      |
| ------------- | --------------------------- |
| OS            | macOS 26.5.1、Apple Silicon |
| OBS Studio    | 32.2.1                      |
| obs-browser   | 2.26.9                      |
| CEF／Chromium | 127.0.6533.120              |

このログはBrowser Sourceの実行環境を特定する証拠であり、tsumikitの全診断項目が合格した証拠ではない。製品版で「対応」と表示するには、各対象OSで以下を同じOBSシーンから確認し、OBS、obs-browser、CEF、OSのバージョンと結果を保存する。

- localhostのHTML、画像、透過背景、WebSocketが動作する
- 音声／動画がユーザー操作なしで再生できる
- `ready`、`trigger`、`complete`とlease切断が動作する
- 外部fetch、外部WebSocket、worker、popup、WebRTCが拒否され、自己navigationの影響が許容範囲内に封じられる
- OBS再起動、Browser Source更新、シーン切り替え後に接続が回復する

リリース時は、Windows x64の最新32.2.xパッチを必須合格環境とする。macOS Apple Siliconは同じ32.2.xの試験合格後にサポートへ含める。OBSまたは同梱CEFのメジャー更新は自動的に対応扱いにせず、同じ試験を再実行する。

## CSPと例外

オーバーレイは信頼できないJavaScriptをOBS内で実行し得るため、外部通信と親アプリへの到達を既定拒否する。Phase 0で採用したCSPをPhase 1の基準とする。

```text
default-src 'none';
script-src 'self' 'unsafe-inline';
style-src 'self' 'unsafe-inline';
connect-src 'self' ws://127.0.0.1:{configuredPort};
img-src 'self' data: blob:;
media-src 'self' data: blob:;
font-src 'self' data:;
worker-src 'none';
webrtc 'block';
child-src 'none';
frame-src 'none';
frame-ancestors 'none';
object-src 'none';
manifest-src 'none';
base-uri 'none';
form-action 'none';
sandbox allow-scripts allow-same-origin;
```

例外と理由:

| 例外                         | 理由                                                              | 制限                                                                               |
| ---------------------------- | ----------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `script-src 'unsafe-inline'` | 単一HTMLで配布される演出スクリプトを許可する                      | `unsafe-eval`は許可しない。外部scriptは読み込めない                                |
| `style-src 'unsafe-inline'`  | 演出固有のstyle属性と単一HTML内CSSを許可する                      | 外部stylesheetは読み込めない                                                       |
| `sandbox allow-scripts`      | 演出とOverlay SDKを実行する                                       | form、popup、downloadの権限は付与しない。文書自身のnavigationは別途制御が必要      |
| `sandbox allow-same-origin`  | Capability配下のSDK、asset、WebSocketを同一オリジンとして利用する | localhostの公開ルートをオーバーレイ専用に限定し、管理APIとWails bindingsを置かない |
| `data:`／`blob:`             | 埋め込み画像、フォント、音声、動画を許可する                      | scriptとconnectには許可しない                                                      |
| localhost WebSocket          | Overlay SDKのイベント通信に必要                                   | 実ポート、IPv4ループバック、Capability、完全一致Originを要求する                   |
| `autoplay=(self)`            | OBS演出の音声／動画再生に必要                                     | camera、microphone、display captureなどは拒否する                                  |

`webrtc 'block'`は未対応CEFで無視され得るため、単独の防御とはみなさない。WebRTC拒否の実機診断に失敗したOBS／CEFはサポート対象から外す。CSPを緩和する変更は、脅威、代替策、影響するOBS範囲を新しいADRへ記録する。

現在の診断には限界がある。popupの生成失敗をnavigation拒否の結果としても表示しているが、sandboxは文書自身の`location`変更まで禁止する保証にならない。また、`example.invalid`へのfetch／WebSocket失敗だけでは、CSPによる拒否とDNS／ネットワーク障害を区別できない。Phase 1では自己navigationを独立した安全なfixtureで検証し、外部通信は到達可能な管理下エンドポイントと`securitypolicyviolation`の観測を組み合わせて判定する。改善前の診断表示だけをサポート判定の証拠にしない。

レスポンスには`no-store`、`nosniff`、`no-referrer`、`DENY`、same-origin resource policy、機密機能を拒否するPermissions Policyも付与する。Capabilityは認証情報として扱い、ログ、クエリ、Cookie、Local Storageへ複製しない。

## YouTube／Twitchのライブラリ選定

### YouTube

YouTube Data APIの公式Goクライアントと、`liveChatMessages.streamList`に必要な公式protobuf descriptor／gRPCクライアントを採用する。OAuthは`golang.org/x/oauth2`でAuthorization Code Flow + PKCE S256を実装し、OSの既定ブラウザと`127.0.0.1`の一時callbackだけを使う。

この構成を選ぶ理由は、公式API定義に追従でき、初回履歴とリアルタイムレスポンスを区別しながらストリーミングをキャンセルできるためである。書き込みscope、client secret、埋め込みWebView認証は使用しない。

未解決事項は、公開前のGoogle OAuth確認、YouTube API Servicesの監査要否、利用者数に応じた共有クォータである。Phase 1では利用者／接続単位の状態と再試行を維持し、全利用者を共有するプロセス内グローバル制御を導入しない。

### Twitch

Identity APIとHelixはGo標準HTTPクライアント、EventSubは`gorilla/websocket`を使う。公式仕様に対する薄い内部adapterを維持し、非公式Twitch SDKは採用しない。

この構成を選ぶ理由は、Device Code Grant、`/validate`、REST rate limit、EventSubのWelcome／keepalive／reconnectを明示的に制御でき、不要なscopeやclient secretを導入せずに済むためである。EventSubのmessage IDを使った重複排除と、接続単位の上限付き再試行を維持する。

Phase 0ではトークンをメモリ内だけに保持する。Phase 1以降で永続化する場合は、OS資格情報ストアとrefresh tokenのアトミックな差し替えを実装するまで平文保存を行わない。

## Phase 0の結果

| 項目                           | 結果         | Phase 1以降への引き継ぎ                                                  |
| ------------------------------ | ------------ | ------------------------------------------------------------------------ |
| Wails／ReactアプリとWindows CI | 続行         | Windowsを優先環境とする                                                  |
| localhost HTTP／WebSocket      | 続行         | 管理APIを同じ公開ルートへ追加しない                                      |
| Capability／lease              | 続行         | パッケージ／effect単位へ拡張し、永続Capabilityは資格情報ストアへ保存する |
| Overlay SDK／CSP               | 条件付き続行 | OBS 32.2.x実機マトリクスをリリースゲートにする                           |
| Tray／シングルインスタンス     | 条件付き続行 | Windows長時間・Explorer再起動・終了競合を実機確認する                    |
| YouTube Live API               | 続行         | OAuth公開準備、クォータ測定、資格情報ストアが必要                        |
| Twitch API                     | 続行         | refresh tokenローテーションと資格情報ストアが必要                        |

自動テストは、ループバック限定、Host／Origin／Capability拒否、lease、入力上限、OAuthのstate／PKCE／timeout、初回履歴抑止、重複イベント、順序逆転、再接続、失効、キャンセル、リソース解放を対象としている。外部サービスの実配信、OBS描画、OS固有Tray操作は自動テストの代替にならない。

## Consequences

### Positive

- Phase 1は共通イベントモデル、永続化、資格情報ストアへ集中できる。
- Wails UIと信頼できないOBSコンテンツのプロセス／オリジン境界を維持できる。
- YouTubeとTwitchの接続状態、再試行、認証情報を利用者／接続単位で分離する設計を継続できる。
- 外部SDKを増やさず、API仕様差分を小さなadapterへ閉じ込められる。

### Negative

- OBS同梱CEFの更新ごとにセキュリティ機能とメディア動作の回帰試験が必要になる。
- `unsafe-inline`と`allow-same-origin`を完全には除去できず、localhost公開面を狭く保つ必要がある。
- Wailsと外部Trayライブラリの組み合わせにはOS固有の保守負担が残る。
- Googleの審査・クォータとTwitchの仕様変更はアプリだけでは制御できない。

## Release gates and unresolved risks

次の項目はPhase 1の開始を妨げないが、該当機能を製品版として公開する前に解決する。

1. 外部通信のCSP違反とネットワーク障害を区別し、popupと自己navigationを別々に判定する診断へ改善する。
2. Windows x64のOBS 32.2.xで改善後のCSP診断、透過、音声／動画、WebSocketを実機合格させる。
3. macOS Apple Siliconをサポート表示する前に同じOBS診断を合格させる。
4. WindowsでTray常駐、Explorer再起動、二重起動、終了中の接続解放を長時間試験する。
5. macOSのローカルWailsビルドで確認されたSDKリンカー問題を、再現条件と対応Xcode／SDKの組み合わせまで切り分ける。
6. Google OAuth公開要件、YouTubeクォータ、Twitchアプリ登録条件を公開前に再確認する。
7. OAuth tokenと永続Capabilityを保存する前に、Windows Credential Manager／macOS Keychain adapterを導入する。
8. ZIP由来コードに対する容量・CPU・GPU枯渇はCSPでは防げないため、インポート確認、無効化、上限を実装する。

## Reconsideration triggers

以下のいずれかに該当した場合、このADRを置き換える。

- サポート対象のWindowsまたはOBSでWails、Tray、Browser Sourceの主要動作が安定しない
- 必要な演出を保ったまま外部通信を拒否できない
- Wails v2がサポート終了し、セキュリティ修正を受け取れない
- YouTubeまたはTwitchの公開クライアント方式で必要なイベントを取得できない
- 配布規模でAPIクォータまたはレート制限を安全に分離できない

## References

- [OBS Studio releases](https://github.com/obsproject/obs-studio/releases)
- [obs-browser](https://github.com/obsproject/obs-browser)
- [Wails v2 documentation](https://wails.io/docs/introduction/)
- [YouTube LiveChatMessages](https://developers.google.com/youtube/v3/live/docs/liveChatMessages)
- [YouTube liveChatMessages.streamList](https://developers.google.com/youtube/v3/live/docs/liveChatMessages/streamList)
- [YouTube OAuth for installed apps](https://developers.google.com/identity/protocols/oauth2/native-app)
- [Twitch Device Code Grant](https://dev.twitch.tv/docs/authentication/getting-tokens-oauth/)
- [Twitch EventSub WebSocket](https://dev.twitch.tv/docs/eventsub/handling-websocket-events)
- [Twitch token validation](https://dev.twitch.tv/docs/authentication/validate-tokens/)
- [Content Security Policy Level 3](https://www.w3.org/TR/CSP3/)
