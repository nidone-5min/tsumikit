import { useEffect, useRef, useState } from 'react';
import {
  BeginYouTubeAuth,
  ConnectYouTube,
  DisconnectYouTube,
  GetOverlayStatus,
  GetYouTubeStatus,
  SendTestEvent,
  SignOutYouTube,
  StartOverlayServer,
  StopOverlayServer,
} from '../wailsjs/go/main/App';
import { main } from '../wailsjs/go/models';

const defaultPort = 18500;

function errorMessage(error: unknown): string {
  if (typeof error === 'string') return error;
  if (error instanceof Error) return error.message;
  return '操作に失敗しました。もう一度お試しください。';
}

function App() {
  const [status, setStatus] = useState<main.OverlayStatus>();
  const [port, setPort] = useState(defaultPort);
  const [message, setMessage] = useState('tsumikitからテストイベントを送信');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [youtubeStatus, setYouTubeStatus] = useState<main.YouTubeStatus>();
  const [clientID, setClientID] = useState('');
  const [streamURL, setStreamURL] = useState('');
  const [youtubeNotice, setYouTubeNotice] = useState('');
  const [youtubeBusy, setYouTubeBusy] = useState(false);
  const [lastYouTubeEvent, setLastYouTubeEvent] = useState<main.YouTubeEvent>();
  const youtubeRequest = useRef(0);
  const youtubeOperation = useRef(false);

  const refreshStatus = async () => {
    try {
      setStatus(await GetOverlayStatus());
    } catch (error) {
      setNotice(errorMessage(error));
    }
  };

  useEffect(() => {
    void refreshStatus();
    const timer = window.setInterval(() => void refreshStatus(), 1000);
    return () => window.clearInterval(timer);
  }, []);

  const refreshYouTubeStatus = async (force = false) => {
    if (youtubeOperation.current && !force) return;
    const request = ++youtubeRequest.current;
    try {
      const next = await GetYouTubeStatus();
      if (request !== youtubeRequest.current) return;
      setYouTubeStatus(next);
      setLastYouTubeEvent(next.lastEvent);
    } catch (error) {
      if (request !== youtubeRequest.current) return;
      setYouTubeNotice(errorMessage(error));
    }
  };

  useEffect(() => {
    void refreshYouTubeStatus();
    const timer = window.setInterval(() => void refreshYouTubeStatus(), 1000);
    return () => {
      youtubeRequest.current++;
      window.clearInterval(timer);
    };
  }, []);

  const startServer = async () => {
    setBusy(true);
    setNotice('');
    try {
      await StartOverlayServer(port);
      setNotice('オーバーレイサーバーを起動しました。');
      await refreshStatus();
    } catch (error) {
      setNotice(errorMessage(error));
      await refreshStatus();
    } finally {
      setBusy(false);
    }
  };

  const stopServer = async () => {
    setBusy(true);
    setNotice('');
    try {
      await StopOverlayServer();
      setNotice('オーバーレイサーバーを停止しました。');
      await refreshStatus();
    } catch (error) {
      setNotice(errorMessage(error));
    } finally {
      setBusy(false);
    }
  };

  const copyURL = async () => {
    if (!status?.url) return;
    try {
      await navigator.clipboard.writeText(status.url);
      setNotice('OBS用URLをコピーしました。');
    } catch {
      setNotice('URLをコピーできませんでした。手動でコピーしてください。');
    }
  };

  const sendTestEvent = async () => {
    setBusy(true);
    setNotice('');
    try {
      await SendTestEvent(message);
      setNotice('テストイベントを送信しました。');
    } catch (error) {
      setNotice(errorMessage(error));
      await refreshStatus();
    } finally {
      setBusy(false);
    }
  };

  const beginYouTubeAuth = async () => {
    youtubeOperation.current = true;
    youtubeRequest.current++;
    setLastYouTubeEvent(undefined);
    setYouTubeBusy(true);
    setYouTubeNotice('');
    try {
      await BeginYouTubeAuth(clientID);
      setYouTubeNotice(
        '既定のブラウザでGoogle認証を完了してください（5分でタイムアウトします）。',
      );
      await refreshYouTubeStatus(true);
    } catch (error) {
      setYouTubeNotice(errorMessage(error));
    } finally {
      youtubeOperation.current = false;
      setYouTubeBusy(false);
    }
  };

  const connectYouTube = async () => {
    youtubeOperation.current = true;
    youtubeRequest.current++;
    setYouTubeBusy(true);
    setYouTubeNotice('');
    setLastYouTubeEvent(undefined);
    try {
      await ConnectYouTube(streamURL);
      setYouTubeNotice('YouTube Liveのコメント受信を開始しました。');
      await refreshYouTubeStatus(true);
    } catch (error) {
      setYouTubeNotice(errorMessage(error));
      await refreshYouTubeStatus(true);
    } finally {
      youtubeOperation.current = false;
      setYouTubeBusy(false);
    }
  };

  const disconnectYouTube = async () => {
    await endYouTubeSession(false);
  };

  const signOutYouTube = async () => {
    await endYouTubeSession(true);
  };

  const endYouTubeSession = async (signOut: boolean) => {
    youtubeOperation.current = true;
    youtubeRequest.current++;
    setYouTubeBusy(true);
    setLastYouTubeEvent(undefined);
    try {
      if (signOut) await SignOutYouTube();
      else await DisconnectYouTube();
      setYouTubeNotice(
        signOut
          ? 'セッション内のGoogle認証情報を消去しました。'
          : 'YouTube Liveから切断しました。',
      );
      await refreshYouTubeStatus(true);
    } catch (error) {
      setYouTubeNotice(errorMessage(error));
    } finally {
      youtubeOperation.current = false;
      setYouTubeBusy(false);
    }
  };

  const running = status?.running ?? false;
  const connected = (status?.connections ?? 0) > 0;

  return (
    <main className="min-h-screen bg-[#101217] bg-[radial-gradient(circle_at_20%_10%,rgba(50,56,72,0.48),transparent_35%)] p-6 font-sans text-[#f6f7f9] sm:p-10">
      <section className="mx-auto w-full max-w-3xl rounded-[20px] border border-[#2f3440] bg-[#191c23] p-6 shadow-[0_24px_80px_rgba(0,0,0,0.32)] sm:p-10">
        <p className="mb-2 text-[13px] font-bold tracking-[0.12em] text-[#9ba4b5]">
          OBS表示PoC
        </p>
        <h1 className="m-0 text-5xl font-bold tracking-[-0.05em]">tsumikit</h1>

        <div className="mt-8 flex items-center gap-3 rounded-xl border border-[#303642] bg-[#12151a] p-4">
          <span
            className={`h-3 w-3 rounded-full ${running ? 'bg-[#58d68d] shadow-[0_0_16px_rgba(88,214,141,0.72)]' : 'bg-[#727987]'}`}
            aria-hidden="true"
          />
          <div>
            <p className="font-bold">
              {running ? 'サーバー起動中' : 'サーバー停止中'}
            </p>
            <p className="text-sm text-[#9ba4b5]">
              OBS接続数: {status?.connections ?? 0}
            </p>
          </div>
        </div>

        <div className="mt-6 grid gap-3 sm:grid-cols-[1fr_auto_auto]">
          <label className="grid gap-2 text-sm font-bold text-[#cdd3dd]">
            ポート番号
            <input
              className="rounded-lg border border-[#3b4250] bg-[#101217] px-4 py-3 text-base text-white disabled:opacity-60"
              type="number"
              min={1024}
              max={65535}
              value={port}
              disabled={running || busy}
              onChange={(event) => setPort(Number(event.target.value))}
            />
          </label>
          <button
            className="self-end rounded-lg bg-[#58d68d] px-5 py-3 font-bold text-[#0d2115] disabled:cursor-not-allowed disabled:opacity-50"
            type="button"
            disabled={running || busy || port < 1024 || port > 65535}
            onClick={() => void startServer()}
          >
            起動
          </button>
          <button
            className="self-end rounded-lg border border-[#505866] px-5 py-3 font-bold disabled:cursor-not-allowed disabled:opacity-50"
            type="button"
            disabled={!running || busy}
            onClick={() => void stopServer()}
          >
            停止
          </button>
        </div>

        <div className="mt-6">
          <label className="mb-2 block text-sm font-bold text-[#cdd3dd]">
            OBS Browser Source URL
          </label>
          <div className="flex gap-3">
            <input
              className="min-w-0 flex-1 rounded-lg border border-[#3b4250] bg-[#101217] px-4 py-3 text-[#dce2ea]"
              value={status?.url ?? ''}
              readOnly
              placeholder="サーバーを起動すると表示されます"
            />
            <button
              className="rounded-lg border border-[#505866] px-5 py-3 font-bold disabled:opacity-50"
              type="button"
              disabled={!status?.url}
              onClick={() => void copyURL()}
            >
              コピー
            </button>
          </div>
        </div>

        <div className="mt-6 border-t border-[#303642] pt-6">
          <label
            className="mb-2 block text-sm font-bold text-[#cdd3dd]"
            htmlFor="test-message"
          >
            テストメッセージ
          </label>
          <div className="flex gap-3">
            <input
              id="test-message"
              className="min-w-0 flex-1 rounded-lg border border-[#3b4250] bg-[#101217] px-4 py-3 text-white"
              maxLength={200}
              value={message}
              onChange={(event) => setMessage(event.target.value)}
            />
            <button
              className="rounded-lg bg-[#667eea] px-5 py-3 font-bold disabled:cursor-not-allowed disabled:opacity-50"
              type="button"
              disabled={!running || !connected || busy}
              onClick={() => void sendTestEvent()}
            >
              送信
            </button>
          </div>
          {!connected && running && (
            <p className="mt-2 text-sm text-[#9ba4b5]">
              OBSでURLを開くと送信できます。
            </p>
          )}
        </div>

        {(notice || status?.error) && (
          <p
            className="mt-6 rounded-lg bg-[#242a34] p-3 text-sm text-[#dce2ea]"
            aria-live="polite"
          >
            {notice || status?.error}
          </p>
        )}

        <div className="mt-10 border-t border-[#303642] pt-8">
          <p className="mb-2 text-[13px] font-bold tracking-[0.12em] text-[#9ba4b5]">
            YOUTUBE API POC
          </p>
          <h2 className="text-2xl font-bold">ライブコメント受信</h2>
          <p className="mt-2 text-sm leading-6 text-[#9ba4b5]">
            Google Desktop OAuthクライアントIDと配信URLを使い、公式の streamList
            APIへ接続します。認証情報はアプリ終了時に破棄されます。
          </p>

          <div className="mt-5 flex items-center gap-3 rounded-xl border border-[#303642] bg-[#12151a] p-4">
            <span
              className={`h-3 w-3 rounded-full ${youtubeStatus?.connected ? 'bg-[#ff5f57] shadow-[0_0_16px_rgba(255,95,87,0.62)]' : youtubeStatus?.authenticated ? 'bg-[#58d68d]' : 'bg-[#727987]'}`}
              aria-hidden="true"
            />
            <div>
              <p className="font-bold">
                {youtubeStatus?.connected
                  ? 'コメント受信中'
                  : youtubeStatus?.state === 'authorizing'
                    ? 'Google認証待ち'
                    : youtubeStatus?.authenticated
                      ? 'Google認証済み'
                      : '未認証'}
              </p>
              <p className="text-sm text-[#9ba4b5]">
                履歴 {youtubeStatus?.initialMessages ?? 0}件 / リアルタイム{' '}
                {youtubeStatus?.realtimeMessages ?? 0}件
              </p>
            </div>
          </div>

          <label className="mt-5 grid gap-2 text-sm font-bold text-[#cdd3dd]">
            Google Desktop OAuthクライアントID
            <input
              className="rounded-lg border border-[#3b4250] bg-[#101217] px-4 py-3 text-base text-white disabled:opacity-60"
              value={clientID}
              disabled={youtubeBusy || youtubeStatus?.connected}
              autoComplete="off"
              spellCheck={false}
              placeholder="….apps.googleusercontent.com"
              onChange={(event) => setClientID(event.target.value)}
            />
          </label>
          <div className="mt-3 flex gap-3">
            <button
              className="rounded-lg bg-[#e8eaed] px-5 py-3 font-bold text-[#202124] disabled:cursor-not-allowed disabled:opacity-50"
              type="button"
              disabled={youtubeBusy || youtubeStatus?.connected || !clientID}
              onClick={() => void beginYouTubeAuth()}
            >
              Google認証
            </button>
            <button
              className="rounded-lg border border-[#505866] px-5 py-3 font-bold disabled:cursor-not-allowed disabled:opacity-50"
              type="button"
              disabled={youtubeBusy || !youtubeStatus?.authenticated}
              onClick={() => void signOutYouTube()}
            >
              認証を解除
            </button>
          </div>

          <label className="mt-6 grid gap-2 text-sm font-bold text-[#cdd3dd]">
            YouTube Live配信URL
            <input
              className="rounded-lg border border-[#3b4250] bg-[#101217] px-4 py-3 text-base text-white disabled:opacity-60"
              type="url"
              value={streamURL}
              disabled={youtubeBusy || youtubeStatus?.connected}
              placeholder="https://www.youtube.com/watch?v=…"
              onChange={(event) => setStreamURL(event.target.value)}
            />
          </label>
          <div className="mt-3 flex gap-3">
            <button
              className="rounded-lg bg-[#ff5f57] px-5 py-3 font-bold text-white disabled:cursor-not-allowed disabled:opacity-50"
              type="button"
              disabled={
                youtubeBusy ||
                youtubeStatus?.connected ||
                !youtubeStatus?.authenticated ||
                !streamURL
              }
              onClick={() => void connectYouTube()}
            >
              受信開始
            </button>
            <button
              className="rounded-lg border border-[#505866] px-5 py-3 font-bold disabled:cursor-not-allowed disabled:opacity-50"
              type="button"
              disabled={youtubeBusy || !youtubeStatus?.connected}
              onClick={() => void disconnectYouTube()}
            >
              切断
            </button>
          </div>

          {lastYouTubeEvent && (
            <div className="mt-6 rounded-xl border border-[#303642] bg-[#12151a] p-4">
              <div className="flex items-center justify-between gap-3">
                <p className="min-w-0 truncate font-bold">
                  {lastYouTubeEvent.author || 'YouTubeユーザー'}
                </p>
                <span className="shrink-0 rounded-full bg-[#242a34] px-3 py-1 text-xs text-[#cdd3dd]">
                  {lastYouTubeEvent.replayed
                    ? '初回履歴（演出対象外）'
                    : 'リアルタイム'}
                </span>
              </div>
              <p className="mt-2 break-words text-[#dce2ea]">
                {lastYouTubeEvent.message ||
                  `イベント: ${lastYouTubeEvent.type}`}
              </p>
            </div>
          )}

          {(youtubeNotice || youtubeStatus?.error) && (
            <p
              className="mt-6 rounded-lg bg-[#242a34] p-3 text-sm text-[#dce2ea]"
              aria-live="polite"
            >
              {youtubeStatus?.error || youtubeNotice}
            </p>
          )}
        </div>
      </section>
    </main>
  );
}

export default App;
