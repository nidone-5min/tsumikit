# tsumikit

YouTube Live / Twitch のイベントを受け取り、OBS Browser Source で演出を実行するデスクトップアプリです。

現在は Phase 0 の技術検証段階です。PR-002 ではローカルHTTP／WebSocketサーバーを起動し、OBS Browser Sourceへテストイベントを表示するPoCを追加しています。

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
http://127.0.0.1:18500/overlay
```

OBSが接続されるとアプリ上の接続数が増え、テストメッセージを送信できます。ポートが使用中の場合は、アプリ画面で `1024` から `65535` の別のポートへ変更して起動してください。

サーバーは同じ端末からのみアクセスでき、LANには公開されません。現段階では接続URLを保護するCapability認証は未実装です。PR-003で追加するまでは、検証用途に限定し、URLを共有したり常用したりしないでください。

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
