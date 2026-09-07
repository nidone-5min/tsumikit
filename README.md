# tsumikit

YouTube Live / Twitch のイベントを受け取り、OBS Browser Source で演出を実行するデスクトップアプリです。

現在は Phase 0 の技術検証段階です。PR-006 ではGoogle Desktop OAuth（PKCE）とYouTube Liveの低遅延コメント受信を確認するPoCを追加しています。

## 採用バージョン

- Wails CLI / Go module: `v2.15.0`
- Go: `1.25.0` 以上
- Node.js: `24`
- UI: React + TypeScript + Vite + Tailwind CSS

Wailsは、実装開始時点でGitHub上の正式な安定リリースである `v2.15.0` に固定しています。

## Windowsでの開発

Windowsを開発・QAの優先環境とします。PowerShellで次を準備してください。

1. Go、Node.js 24、WebView2 Runtimeをインストールします。
2. Wails CLIを固定バージョンでインストールします。

   ```powershell
   go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0
   wails doctor
   ```

3. リポジトリをcloneし、フロントエンド依存関係を復元します。

   ```powershell
   cd frontend
   npm ci
   cd ..
   ```

4. 開発モードで起動します。

   ```powershell
   wails dev
   ```

`wails doctor` が不足している依存関係を示した場合は、案内に従って導入してください。WebView2 Runtimeがない環境ではアプリ画面を表示できません。

## macOSでの開発

Go、Node.js 24、Xcode Command Line Toolsを準備したうえで、Windowsと同じWails CLIを導入します。

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0
wails doctor
cd frontend
npm ci
cd ..
wails dev
```

## OBS表示PoC

アプリの起動時に、IPv4ループバックアドレス `127.0.0.1` のポート `18500` でオーバーレイサーバーを起動します。OBSのBrowser Sourceへ、アプリに表示される次のURLを設定してください。

```text
http://127.0.0.1:18500/overlay/<起動ごとに生成されるCapability>
```

OBSが接続されるとアプリ上の接続数が増え、テストメッセージを送信できます。ポートが使用中の場合は、アプリ画面で `1024` から `65535` の別のポートへ変更して起動してください。

サーバーは同じ端末からのみアクセスでき、LANには公開されません。OBS用URLには起動ごとに暗号学的乱数から生成するCapabilityが含まれ、停止または再起動すると以前のURLは無効になります。URLを知っているローカルプロセスはオーバーレイへ接続できるため、URLを第三者へ共有したり、ログや配信画面へ映したりしないでください。

HTTPリクエストのHostとWebSocket接続のOriginは、表示されたループバックURLと完全に一致する場合だけ許可されます。WebSocket接続はping／pongで監視され、応答しなくなったBrowser Sourceは接続数から自動的に除外されます。

### Overlay SDKとCEF診断

オーバーレイはCapability配下の固定SDKを読み込み、`ready()`で受信準備を通知した後にテストイベントを`onTrigger()`で受信し、演出後に`complete(event.id)`を返します。未知のSDKバージョン、未許可フィールド、現在の接続へ割り当てていないevent IDは受理しません。

HTML応答にはCSP、`sandbox`、Permissions-Policyと追加のセキュリティヘッダーを付与します。OBS上の診断表示で、透過背景、画像、音声／動画の自動再生、外部fetch、外部WebSocket、worker、popup、外部navigation、WebRTCの拒否結果を確認できます。

SDKとオーバーレイHTMLはこのPoC専用です。ZIPパッケージ向けの公開SDK契約と追加APIは後続PRで実装します。

## Trayとアプリ終了

ウィンドウの閉じるボタンを押すと、アプリは終了せずWindowsではシステムトレイ、macOSではメニューバーへ常駐します。「tsumikitを表示」でウィンドウを再表示し、「終了」でアプリを終了できます。OBS Browser Sourceが接続中の場合は、接続を切断する前に確認ダイアログを表示します。

同じ利用者セッションでtsumikitをもう一度起動すると、新しいプロセスは終了し、既存のウィンドウが前面に表示されます。2回目の起動に渡されたコマンドライン引数と作業ディレクトリは処理しません。

TrayはWails v2の外部イベントループへ`fyne.io/systray`を統合するPhase 0実装です。Trayの初期化に失敗した環境では、ウィンドウを閉じると通常どおりアプリを終了し、画面を再表示できない状態で常駐しません。

## YouTube Liveコメント受信PoC

このPoCを試すには、Google Cloud ConsoleでYouTube Data API v3を有効にし、アプリケーションの種類が「デスクトップ アプリ」のOAuth 2.0クライアントを作成してください。クライアントシークレットは使用しません。

1. アプリ画面へDesktop OAuthクライアントID（末尾が`.apps.googleusercontent.com`）を入力し、「Google認証」を押します。
2. OSの既定ブラウザで、読み取り専用のYouTube権限を許可します。
3. 配信中のYouTube Live URLを入力し、「受信開始」を押します。

対応URLは`https://www.youtube.com/watch?v=...`、`https://www.youtube.com/live/...`、`https://youtu.be/...`です。アプリは`videos.list`の`liveStreamingDetails.activeLiveChatId`を解決した後、`liveChatMessages.streamList`でコメントを受信します。

最初のAPIレスポンスに含まれる直近の履歴は画面確認だけに使い、演出対象にはしません。2回目以降のレスポンスだけをリアルタイムイベントとして扱い、切断からの再開時は最後に受け取った`nextPageToken`をメモリ内で使用します。

OAuthコールバックは`127.0.0.1`のランダムな空きポートだけで一時的に待ち受け、5分以内に成功・拒否・タイムアウトのいずれかで停止します。`state`とPKCE verifierは認証要求ごとに生成し、認証コードは一度だけ受理します。アクセストークンと更新トークン、クライアントID、配信URL、受信コメントは永続化やログ出力を行いません。認証トークンはセッション中だけ保持するため、アプリを再起動した場合は再認証が必要です。

## 品質チェック

変更を送る前に、次を実行してください。

```powershell
cd frontend
npm run check
npm run build
cd ..
gofmt -w (git ls-files '*.go')
go test ./...
```

CIでは静的チェックとテストが成功した後に、Windows向けWailsビルドを行います。

## プロダクションビルド

```powershell
wails build -clean
```

成果物は `build/bin` に生成されます。
