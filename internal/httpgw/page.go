package httpgw

const configPageHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>mysmpp 配置</title>
  <style>
    :root {
      color-scheme: light;
      font-family: "Segoe UI", Arial, sans-serif;
      background: #f5f7fb;
      color: #1f2937;
    }
    body { margin: 0; }
    header {
      background: #0f766e;
      color: white;
      padding: 18px 24px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
    }
    h1 { margin: 0; font-size: 20px; font-weight: 650; letter-spacing: 0; }
    main { max-width: 1180px; margin: 0 auto; padding: 18px; }
    .toolbar {
      display: flex;
      gap: 10px;
      align-items: center;
      justify-content: flex-end;
      margin-bottom: 12px;
      min-height: 42px;
    }
    button {
      border: 0;
      border-radius: 6px;
      background: #0f766e;
      color: white;
      padding: 10px 14px;
      font-size: 14px;
      cursor: pointer;
    }
    button.secondary { background: #475569; }
    button:disabled { opacity: .55; cursor: wait; }
    .status { margin-right: auto; color: #475569; font-size: 14px; }
    .grid {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 14px;
    }
    section {
      background: white;
      border: 1px solid #dbe3ef;
      border-radius: 8px;
      padding: 14px;
      min-width: 0;
    }
    section.wide { grid-column: 1 / -1; }
    h2 { margin: 0 0 10px; font-size: 15px; color: #0f172a; letter-spacing: 0; }
    textarea {
      width: 100%;
      min-height: 180px;
      box-sizing: border-box;
      border: 1px solid #cbd5e1;
      border-radius: 6px;
      padding: 10px;
      resize: vertical;
      font: 13px/1.45 Consolas, "Courier New", monospace;
      color: #111827;
      background: #fbfdff;
    }
    .wide textarea { min-height: 260px; }
    @media (max-width: 840px) {
      header { align-items: flex-start; flex-direction: column; }
      .grid { grid-template-columns: 1fr; }
      main { padding: 12px; }
      .toolbar { flex-wrap: wrap; justify-content: stretch; }
      .status { width: 100%; }
      button { flex: 1; }
    }
  </style>
</head>
<body>
  <header>
    <h1>mysmpp 配置中心</h1>
    <div>HTTP / SMPP / 路由 / 上游下游规则</div>
  </header>
  <main>
    <div class="toolbar">
      <span id="status" class="status">正在加载配置...</span>
      <button class="secondary" id="reload">重新加载</button>
      <button id="save">保存配置</button>
    </div>
    <div class="grid">
      <section>
        <h2>服务与 SMPP</h2>
        <textarea id="service"></textarea>
      </section>
      <section>
        <h2>存储</h2>
        <textarea id="storage"></textarea>
      </section>
      <section>
        <h2>路由规则</h2>
        <textarea id="routes"></textarea>
      </section>
      <section>
        <h2>上游供应商</h2>
        <textarea id="providers"></textarea>
      </section>
      <section>
        <h2>下游入站 HTTP</h2>
        <textarea id="inbound"></textarea>
      </section>
      <section>
        <h2>上游出站 HTTP</h2>
        <textarea id="outbound"></textarea>
      </section>
      <section class="wide">
        <h2>完整配置</h2>
        <textarea id="full"></textarea>
      </section>
    </div>
  </main>
  <script>
    const ids = ["service", "storage", "routes", "providers", "inbound", "outbound", "full"];
    let current = null;
    const statusEl = document.getElementById("status");
    const pretty = value => JSON.stringify(value, null, 2);
    const parse = id => JSON.parse(document.getElementById(id).value || "null");
    const setBusy = busy => {
      document.getElementById("save").disabled = busy;
      document.getElementById("reload").disabled = busy;
    };
    function render(cfg) {
      current = cfg;
      document.getElementById("service").value = pretty({ server: cfg.server, smpp: cfg.smpp });
      document.getElementById("storage").value = pretty(cfg.storage || {});
      document.getElementById("routes").value = pretty(cfg.routes || []);
      document.getElementById("providers").value = pretty(cfg.providers || []);
      document.getElementById("inbound").value = pretty(cfg.inbound || []);
      document.getElementById("outbound").value = pretty(cfg.outbound || []);
      document.getElementById("full").value = pretty(cfg);
    }
    async function loadConfig() {
      setBusy(true);
      statusEl.textContent = "正在加载配置...";
      try {
        const res = await fetch("/v1/config");
        if (!res.ok) throw new Error(await res.text());
        render(await res.json());
        statusEl.textContent = "配置已加载";
      } catch (err) {
        statusEl.textContent = "加载失败: " + err.message;
      } finally {
        setBusy(false);
      }
    }
    async function saveConfig() {
      setBusy(true);
      statusEl.textContent = "正在保存...";
      try {
        let cfg;
        const fullText = document.getElementById("full").value.trim();
        if (fullText) {
          cfg = JSON.parse(fullText);
        } else {
          const service = parse("service");
          cfg = {
            server: service.server,
            smpp: service.smpp,
            storage: parse("storage"),
            routes: parse("routes"),
            providers: parse("providers"),
            inbound: parse("inbound"),
            outbound: parse("outbound")
          };
        }
        const res = await fetch("/v1/config", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(cfg)
        });
        if (!res.ok) throw new Error(await res.text());
        render(await res.json());
        statusEl.textContent = "保存成功，运行时配置已生效";
      } catch (err) {
        statusEl.textContent = "保存失败: " + err.message;
      } finally {
        setBusy(false);
      }
    }
    ids.forEach(id => {
      document.getElementById(id).addEventListener("input", () => {
        if (id !== "full") document.getElementById("full").value = "";
      });
    });
    document.getElementById("reload").addEventListener("click", loadConfig);
    document.getElementById("save").addEventListener("click", saveConfig);
    loadConfig();
  </script>
</body>
</html>`
