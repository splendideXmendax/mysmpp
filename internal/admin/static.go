package admin

const styleCSS = `:root {
  --primary: #0f766e;
  --danger: #b91c1c;
  --bg: #f5f7fb;
  --border: #dbe3ef;
  --text: #1f2937;
  --muted: #6b7280;
  font-family: -apple-system, "Segoe UI", Arial, sans-serif;
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--text); }
.layout { display: grid; grid-template-columns: 210px minmax(0, 1fr); min-height: 100vh; }
.sidebar { background: #0f172a; color: white; padding: 18px; display: flex; flex-direction: column; }
.sidebar h1 { margin: 0 0 24px; font-size: 18px; letter-spacing: 0; }
.sidebar nav { display: flex; flex-direction: column; flex: 1; gap: 2px; }
.sidebar nav a { color: #cbd5e1; padding: 8px 10px; border-radius: 4px; text-decoration: none; font-size: 14px; }
.sidebar nav a.active { background: var(--primary); color: white; }
.sidebar nav a:hover:not(.active) { background: #1e293b; color: white; }
.logout button { background: none; border: 1px solid #475569; color: #cbd5e1; padding: 7px 10px; border-radius: 4px; width: 100%; cursor: pointer; }
main { padding: 24px; max-width: 1120px; }
.page-header { display: flex; justify-content: space-between; align-items: center; gap: 12px; margin-bottom: 16px; }
.page-header h2 { margin: 0; font-size: 18px; letter-spacing: 0; }
.btn { display: inline-block; padding: 8px 14px; border: 1px solid var(--border); background: white; border-radius: 4px; text-decoration: none; color: var(--text); font-size: 14px; cursor: pointer; }
.btn.primary { background: var(--primary); color: white; border-color: var(--primary); }
.btn:hover { opacity: .9; }
table { width: 100%; background: white; border: 1px solid var(--border); border-radius: 6px; border-collapse: collapse; overflow: hidden; }
th, td { padding: 10px 14px; text-align: left; border-bottom: 1px solid var(--border); font-size: 14px; vertical-align: top; }
thead th { background: #f9fafb; font-weight: 600; color: var(--muted); }
tbody tr:last-child td { border-bottom: 0; }
.empty { text-align: center; color: var(--muted); padding: 32px; }
.link-danger { color: var(--danger); background: none; border: 0; padding: 0; cursor: pointer; font-size: 14px; }
.form { background: white; border: 1px solid var(--border); border-radius: 6px; padding: 20px; max-width: 680px; }
.field { margin-bottom: 16px; }
.field label { display: block; font-size: 13px; font-weight: 600; margin-bottom: 6px; color: var(--text); }
.field input, .field select, .field textarea { width: 100%; padding: 8px 10px; border: 1px solid var(--border); border-radius: 4px; font: 14px Consolas, "Courier New", monospace; }
.field textarea { resize: vertical; min-height: 140px; }
.field input[readonly] { background: #f3f4f6; }
.field small { display: block; margin-top: 4px; color: var(--muted); font-size: 12px; }
.actions { display: flex; gap: 8px; margin-top: 24px; }
.flash { background: #fef3c7; border: 1px solid #fcd34d; padding: 10px 14px; border-radius: 4px; margin-bottom: 16px; font-size: 14px; }
.error { background: #fee2e2; border: 1px solid #fecaca; color: #991b1b; padding: 10px 14px; border-radius: 4px; margin-bottom: 16px; font-size: 14px; }
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 12px; }
.stat { background: white; border: 1px solid var(--border); border-radius: 6px; padding: 16px; }
.stat strong { display: block; font-size: 24px; margin-bottom: 4px; }
.muted { color: var(--muted); }
.login { min-height: 100vh; display: grid; place-items: center; padding: 18px; }
.login form { background: white; border: 1px solid var(--border); border-radius: 6px; padding: 24px; width: min(380px, 100%); }
.login h1 { margin: 0 0 18px; font-size: 20px; }
@media (max-width: 768px) {
  .layout { grid-template-columns: 1fr; }
  .sidebar { position: static; }
  .sidebar nav { flex-direction: row; flex-wrap: wrap; }
  main { padding: 12px; }
  .page-header { align-items: flex-start; flex-direction: column; }
}`
