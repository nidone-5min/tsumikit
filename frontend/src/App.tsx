function App() {
  return (
    <main className="grid min-h-screen place-items-center bg-[#101217] bg-[radial-gradient(circle_at_20%_10%,rgba(50,56,72,0.48),transparent_35%)] p-8 font-sans text-[#f6f7f9]">
      <section
        className="w-full max-w-[560px] rounded-[20px] border border-[#2f3440] bg-[#191c23] p-8 text-left shadow-[0_24px_80px_rgba(0,0,0,0.32)] sm:p-12"
        aria-labelledby="app-title"
      >
        <p className="mb-2 text-[13px] font-bold tracking-[0.12em] text-[#9ba4b5]">
          配信演出ツール
        </p>
        <h1
          id="app-title"
          className="m-0 text-[clamp(44px,9vw,72px)] font-bold tracking-[-0.05em] text-[#f6f7f9]"
        >
          tsumikit
        </h1>
        <p className="mt-8 mb-3 flex items-center gap-2.5 font-bold text-[#dce2ea]">
          <span
            className="h-2.5 w-2.5 rounded-full bg-[#58d68d] shadow-[0_0_16px_rgba(88,214,141,0.72)]"
            aria-hidden="true"
          />
          アプリケーション基盤を準備しました
        </p>
        <p className="m-0 leading-7 text-[#9ba4b5]">
          配信接続とオーバーレイ機能は、今後のフェーズで追加されます。
        </p>
      </section>
    </main>
  );
}

export default App;
