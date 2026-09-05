# tsumikit

YouTube Live / Twitch のイベントを受け取り、OBS Browser Source で演出を実行するデスクトップアプリです。

現在は Phase 0 の技術検証段階です。PR-001 では Wails v2、React、TypeScript、Vite、Go の最小構成と、継続的に品質を確認するための開発基盤を用意しています。

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
