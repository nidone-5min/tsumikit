import { useEffect, useState } from 'react';
import {
  GetOverlayStatus,
  SendTestEvent,
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
      </section>
    </main>
  );
}

export default App;
