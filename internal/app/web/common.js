// Cove: what the app and the public link page share. Text always goes into the page as text, never as HTML.
"use strict";

const ICONS = {
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  file: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>',
  image: '<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="10" r="2"/><path d="m21 16-5-5-9 9"/>',
  video: '<rect x="3" y="5" width="13" height="14" rx="2"/><path d="m16 10 5-3v10l-5-3"/>',
  audio: '<path d="M9 18V6l11-2v12"/><circle cx="6" cy="18" r="3"/><circle cx="17" cy="16" r="3"/>',
  pdf: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h4"/>',
  doc: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M8 12h8M8 15h8M8 18h5"/>',
  sheet: '<rect x="4" y="3" width="16" height="18" rx="2"/><path d="M4 9h16M4 15h16M10 3v18"/>',
  slides: '<rect x="3" y="4" width="18" height="12" rx="2"/><path d="M12 16v4M8 20h8"/>',
  archive: '<rect x="3" y="4" width="18" height="5" rx="1"/><path d="M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9M10 13h4"/>',
  text: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h6"/>',
  code: '<path d="m8 8-4 4 4 4M16 8l4 4-4 4M14 5l-4 14"/>',
  upload: '<path d="M12 16V4M7 9l5-5 5 5M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/>',
  download: '<path d="M12 4v12M7 11l5 5 5-5M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  share: '<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/>',
  trash: '<path d="M4 7h16M10 11v6M14 11v6M5 7l1 12a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/>',
  calendar: '<rect x="3" y="5" width="18" height="16" rx="2"/><path d="M3 10h18M8 3v4M16 3v4"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
  more: '<circle cx="12" cy="5" r="1.2"/><circle cx="12" cy="12" r="1.2"/><circle cx="12" cy="19" r="1.2"/>',
  close: '<path d="M6 6l12 12M18 6 6 18"/>',
  left: '<path d="m15 6-6 6 6 6"/>',
  right: '<path d="m9 6 6 6-6 6"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>',
  grid: '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>',
  edit: '<path d="M12 20h9M16.5 3.5a2.1 2.1 0 1 1 3 3L7 19l-4 1 1-4z"/>',
  move: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M10 13h6M13 10l3 3-3 3"/>',
  restore: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/>',
  play: '<path d="M7 4v16l13-8z"/>',
  pause: '<path d="M7 4h4v16H7zM14 4h4v16h-4z"/>',
  prev: '<path d="M19 20 9 12l10-8zM5 4v16"/>',
  next: '<path d="m5 4 10 8-10 8zM19 4v16"/>',
  menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>',
  open: '<path d="M14 3h7v7M10 14 21 3M19 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h5"/>',
  lock: '<rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  check: '<path d="m5 12 5 5 9-10"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="m10.8 12.2 8.2-8.2M17 6l2 2M14 9l2 2"/>',
  inbox: '<path d="M22 12h-6l-2 3h-4l-2-3H2"/><path d="M5.5 5h13l3.5 7v6a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-6z"/>',
  minus: '<path d="M5 12h14"/>',
  home: '<path d="M3 11 12 4l9 7"/><path d="M5 10v10h5v-6h4v6h5V10"/>',
  bell: '<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.9 1.9 0 0 0 3.4 0"/>',
  star: '<path d="m12 3 2.8 5.7 6.2.9-4.5 4.4 1 6.2L12 17.3 6.5 20.2l1-6.2L3 9.6l6.2-.9z"/>',
  qr: '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><path d="M14 14h3v3h-3zM20 14v.01M14 20h.01M17 20h4v-3"/>',
  pin: '<path d="M12 17v5M9 3h6l-1 7 4 3H6l4-3z"/>',
  place: '<path d="M12 21s-7-6.2-7-11a7 7 0 0 1 14 0c0 4.8-7 11-7 11z"/><circle cx="12" cy="10" r="2.5"/>',
  phone: '<path d="M5 4h4l2 5-2.5 1.5a11 11 0 0 0 5 5L15 13l5 2v4a2 2 0 0 1-2 2A16 16 0 0 1 3 6a2 2 0 0 1 2-2"/>',
  mail: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/>',
  user: '<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>',
  video: '<rect x="3" y="6" width="13" height="12" rx="2"/><path d="m16 10 5-3v10l-5-3"/>',
  moon: '<path d="M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  refresh: '<path d="M21 12a9 9 0 0 1-15.5 6.2L3 16M3 12a9 9 0 0 1 15.5-6.2L21 8M21 3v5h-5M3 21v-5h5"/>',
};

function icon(name, cls) {
  const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  s.setAttribute("viewBox", "0 0 24 24");
  s.setAttribute("class", "i" + (cls ? " " + cls : ""));
  s.innerHTML = ICONS[name] || ICONS.file; // fixed strings above, never page data
  return s;
}

// el("div", {class: "x", onclick: f}, "text", child…)
function el(tag, props, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === "class") e.className = v;
    else if (k === "text") e.textContent = v;
    else if (k === "style" && typeof v === "object") for (const [p, val] of Object.entries(v)) p.startsWith("--") ? e.style.setProperty(p, val) : (e.style[p] = val);
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (k in e && (typeof v !== "string" || k === "value")) e[k] = v; // a textarea's value is only a property
    else e.setAttribute(k, v === true ? "" : v);
  }
  for (const k of kids.flat(Infinity)) {
    if (k == null || k === false) continue;
    e.append(k instanceof Node ? k : document.createTextNode(String(k)));
  }
  return e;
}

function kindIcon(kind) {
  return el("div", { class: "ficon k-" + (kind || "file") }, icon(ICONS[kind] ? kind : "file"));
}

function fmtSize(n) {
  if (n == null) return "";
  if (n < 1024) return n + " B";
  const u = ["KB", "MB", "GB", "TB"];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return (n >= 100 ? Math.round(n) : n.toFixed(1)) + " " + u[i];
}

function fmtDate(ms) {
  const d = new Date(ms), now = new Date();
  const sameDay = d.toDateString() === now.toDateString();
  if (sameDay) return "Today, " + d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  const opts = { month: "short", day: "numeric" };
  if (d.getFullYear() !== now.getFullYear()) opts.year = "numeric";
  return d.toLocaleDateString([], opts);
}

function fmtDuration(s) {
  if (!isFinite(s)) return "0:00";
  s = Math.max(0, Math.round(s));
  const h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60, sec = String(s % 60).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${sec}` : `${m}:${sec}`;
}

function encPath(p) { return encodeURIComponent(p); }
function joinPath(dir, name) { return (dir === "/" ? "" : dir) + "/" + name; }
function parentOf(p) { const i = p.lastIndexOf("/"); return i <= 0 ? "/" : p.slice(0, i); }
function baseName(p) { return p.slice(p.lastIndexOf("/") + 1); }
function extOf(name) { const i = name.lastIndexOf("."); return i > 0 ? name.slice(i + 1).toLowerCase() : ""; }

// coveHome is the household being looked at (for people in more than one), sent with every request.
function coveHome() { try { return localStorage.getItem("cove.home") || ""; } catch (_) { return ""; } }

async function api(method, url, body) {
  const opts = { method, headers: { "X-Cove": "1", "X-Cove-Household": coveHome() } };
  if (body !== undefined) { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
  let r;
  try { r = await fetch(url, opts); }
  catch (e) {
    // offline: changes to tasks and the fridge wait on this device (see OFFQ in app.js)
    if (method !== "GET" && /^\/api\/(tasks|fridge)/.test(url) && typeof OFFQ !== "undefined") {
      OFFQ.add({ method, url, body: opts.body, home: coveHome() });
      toast("Saved on this device; it syncs when you're back online");
      return {};
    }
    throw new Error("Cove can't be reached. Are you online?");
  }
  const ct = r.headers.get("content-type") || "";
  const data = ct.includes("json") ? await r.json() : await r.text();
  if (r.status === 401 && url.startsWith("/api/")) { location.href = "/login"; throw new Error("You're signed out."); }
  if (!r.ok) { const e = new Error((data && data.error) || r.statusText); e.status = r.status; e.data = data; throw e; }
  return data;
}

// --- toasts, dialogs, menus

function toast(msg, isError) {
  let box = document.querySelector(".toasts");
  if (!box) { box = el("div", { class: "toasts" }); document.body.append(box); }
  const t = el("div", { class: "toast" + (isError ? " err" : ""), text: msg });
  box.append(t);
  setTimeout(() => t.remove(), isError ? 6000 : 3000);
}
function fail(e) { toast(e.message || String(e), true); }

function dialog({ title, body, buttons = [], wide, onClose }) {
  const close = () => { veil.remove(); document.removeEventListener("keydown", key); onClose && onClose(); };
  const key = e => { if (e.key === "Escape") close(); };
  const foot = el("footer");
  for (const b of buttons) {
    if (b === "grow") { foot.append(el("div", { class: "grow" })); continue; }
    const btn = el("button", { class: "btn" + (b.primary ? " primary" : "") + (b.danger ? " danger" : ""), type: b.primary ? "submit" : "button", text: b.label });
    if (!b.primary) btn.onclick = async () => { if (!b.action || (await b.action(close)) !== false) close(); };
    foot.append(btn);
  }
  const form = el("form", { class: "dialog card" + (wide ? " wide" : "") },
    el("header", {}, el("h2", { text: title }), el("button", { class: "btn ghost icon", type: "button", onclick: close, "aria-label": "Close" }, icon("close"))),
    el("div", { class: "body" }, body), foot);
  form.onsubmit = async e => {
    e.preventDefault();
    const p = buttons.find(b => b.primary);
    if (!p) return;
    const btn = form.querySelector("button[type=submit]");
    btn.disabled = true;
    try { if (!p.action || (await p.action(close)) !== false) close(); }
    catch (err) { fail(err); }
    finally { btn.disabled = false; }
  };
  const veil = el("div", { class: "veil", onmousedown: e => { if (e.target === veil) close(); } }, form);
  document.body.append(veil);
  document.addEventListener("keydown", key);
  setTimeout(() => { const f = form.querySelector("input:not([type=checkbox]):not([type=radio]), textarea, select"); f && f.focus(); f && f.select && f.select(); }, 30);
  return { close, form };
}

function ask(title, label, value, okLabel) {
  return new Promise(resolve => {
    const input = el("input", { class: "input", value: value || "" });
    let done = false;
    dialog({ title, body: el("div", { class: "field" }, el("label", { text: label }), input),
      buttons: [{ label: "Cancel" }, { label: okLabel || "OK", primary: true, action: () => { done = true; resolve(input.value.trim()); } }],
      onClose: () => { if (!done) resolve(null); } });
  });
}

function confirmBox(title, text, okLabel, danger) {
  return new Promise(resolve => {
    let done = false;
    dialog({ title, body: el("p", { class: "muted", text }),
      buttons: [{ label: "Cancel" }, { label: okLabel, primary: true, danger, action: () => { done = true; resolve(true); } }],
      onClose: () => { if (!done) resolve(false); } });
  });
}

function popMenu(at, items) {
  document.querySelectorAll(".menu-pop").forEach(m => m.remove());
  const m = el("div", { class: "menu-pop" });
  for (const it of items) {
    if (!it) continue;
    if (it === "-") { m.append(el("hr")); continue; }
    m.append(el("button", { class: it.danger ? "danger" : "", onclick: () => { m.remove(); it.action(); } }, icon(it.icon || "file"), it.label));
  }
  document.body.append(m);
  let x, y;
  if (at instanceof Event) { x = at.clientX; y = at.clientY; }
  else { const r = at.getBoundingClientRect(); x = r.right - m.offsetWidth; y = r.bottom + 4; }
  x = Math.max(8, Math.min(x, innerWidth - m.offsetWidth - 8));
  y = Math.max(8, Math.min(y, innerHeight - m.offsetHeight - 8));
  m.style.left = x + "px"; m.style.top = y + "px";
  setTimeout(() => {
    const away = e => { if (!m.contains(e.target)) { m.remove(); document.removeEventListener("mousedown", away); } };
    document.addEventListener("mousedown", away);
  });
  return m;
}

// --- Markdown, safely: everything is escaped first, then a few marks become tags.

function esc(s) { return s.replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])); }

function inlineMd(s) {
  return s
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/(^|[^*])\*([^*]+)\*/g, "$1<em>$2</em>")
    .replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
}

function markdown(text) {
  const lines = esc(text).split("\n");
  let html = "", list = null, code = false, para = [];
  const flush = () => { if (para.length) { html += "<p>" + inlineMd(para.join(" ")) + "</p>"; para = []; } };
  const endList = () => { if (list) { html += "</" + list + ">"; list = null; } };
  for (const line of lines) {
    if (line.startsWith("```")) { flush(); endList(); html += code ? "</code></pre>" : "<pre><code>"; code = !code; continue; }
    if (code) { html += line + "\n"; continue; }
    let m;
    if ((m = line.match(/^(#{1,6})\s+(.*)/))) { flush(); endList(); const n = m[1].length; html += `<h${n}>${inlineMd(m[2])}</h${n}>`; }
    else if ((m = line.match(/^\s*[-*+]\s+(.*)/))) { flush(); if (list !== "ul") { endList(); html += "<ul>"; list = "ul"; } html += "<li>" + inlineMd(m[1]) + "</li>"; }
    else if ((m = line.match(/^\s*\d+[.)]\s+(.*)/))) { flush(); if (list !== "ol") { endList(); html += "<ol>"; list = "ol"; } html += "<li>" + inlineMd(m[1]) + "</li>"; }
    else if ((m = line.match(/^&gt;\s?(.*)/))) { flush(); endList(); html += "<blockquote>" + inlineMd(m[1]) + "</blockquote>"; }
    else if (/^\s*(---|\*\*\*)\s*$/.test(line)) { flush(); endList(); html += "<hr>"; }
    else if (!line.trim()) { flush(); endList(); }
    else para.push(line);
  }
  flush(); endList();
  if (code) html += "</code></pre>";
  const div = el("div", { class: "md" });
  div.innerHTML = html; // built only from escaped text
  return div;
}

// --- the viewer: photos (arrow keys flip through), video, music, PDFs, text and Markdown (editable)

const TEXT_KINDS = ["text", "code"];
const EDITABLE = ["txt", "md", "markdown", "log", "csv", "json", "yaml", "yml", "toml", "ini", "conf", "cfg", "xml", "html", "css", "js", "ts", "py", "go", "sh", "sql", "rs", "rb", "java", "c", "h", "cpp", "swift", "kt"];

function openViewer(o) {
  // o: { items, index, url(it), thumb(it, w), download(it), save?(it, text), buttons?(it) }
  let i = o.index;
  const viewable = o.items;
  const stage = el("div", { class: "stage" });
  const title = el("div", { class: "title" });
  const extra = el("div", { class: "row", style: { alignItems: "center" } });
  const close = () => { v.remove(); document.removeEventListener("keydown", key); };
  const go = d => { const n = i + d; if (n >= 0 && n < viewable.length) { i = n; show(); } };
  const key = e => {
    if (e.target.tagName === "TEXTAREA") return;
    if (e.key === "Escape") close();
    if (e.key === "ArrowLeft") go(-1);
    if (e.key === "ArrowRight") go(1);
  };
  const prev = el("button", { class: "nav-arrow prev", onclick: () => go(-1), "aria-label": "Previous" }, icon("left"));
  const next = el("button", { class: "nav-arrow next", onclick: () => go(1), "aria-label": "Next" }, icon("right"));
  const v = el("div", { class: "viewer" },
    el("div", { class: "vbar" }, title, extra,
      el("a", { class: "btn", href: "#", onclick: e => { e.preventDefault(); location.href = o.download(viewable[i]); } }, icon("download"), el("span", { class: "lbl", text: "Download" })),
      el("button", { class: "btn icon", onclick: close, "aria-label": "Close" }, icon("close"))),
    stage);
  let touchX = null;
  stage.addEventListener("touchstart", e => { touchX = e.touches[0].clientX; }, { passive: true });
  stage.addEventListener("touchend", e => { if (touchX == null) return; const dx = e.changedTouches[0].clientX - touchX; if (Math.abs(dx) > 60) go(dx < 0 ? 1 : -1); touchX = null; });

  async function show() {
    const it = viewable[i];
    title.textContent = it.name;
    extra.replaceChildren(...(o.buttons ? o.buttons(it, close) : []));
    stage.replaceChildren();
    prev.style.visibility = i > 0 ? "" : "hidden";
    next.style.visibility = i < viewable.length - 1 ? "" : "hidden";
    if (viewable.length > 1) stage.append(prev, next);
    const url = o.url(it);
    if (it.kind === "image") {
      const ext = extOf(it.name);
      const img = el("img", { src: ["jpg", "jpeg", "png", "webp"].includes(ext) && o.thumb ? o.thumb(it, 1600) : url, alt: it.name });
      img.onerror = () => { if (img.src !== new URL(url, location.href).href) img.src = url; };
      stage.append(img);
      // the next photo loads meanwhile
      const n = viewable[i + 1];
      if (n && n.kind === "image" && o.thumb) new Image().src = o.thumb(n, 1600);
    } else if (it.kind === "video") {
      stage.append(el("video", { src: url, controls: true, autoplay: true, playsInline: true }));
    } else if (it.kind === "audio") {
      stage.append(el("div", { class: "nothumb" }, kindIcon("audio"), el("audio", { src: url, controls: true, autoplay: true })));
    } else if (it.kind === "pdf") {
      stage.append(el("iframe", { src: url, title: it.name }));
    } else if (TEXT_KINDS.includes(it.kind) || ["md", "markdown", "csv"].includes(extOf(it.name))) {
      const doc = el("div", { class: "doc" }, el("p", { class: "muted", text: "Loading…" }));
      stage.append(doc);
      try {
        const r = await fetch(url);
        if (!r.ok) throw new Error("It couldn't be opened.");
        const text = await r.text();
        const render = () => {
          const isMd = ["md", "markdown"].includes(extOf(it.name));
          doc.replaceChildren(isMd ? markdown(text) : el("pre", { class: "plain", text }));
        };
        render();
        if (o.save && EDITABLE.includes(extOf(it.name)) && it.size < 2e6) {
          extra.prepend(el("button", { class: "btn", onclick: e => {
            const ta = el("textarea", { value: text, spellcheck: false });
            doc.replaceChildren(ta);
            ta.focus();
            e.currentTarget.replaceWith(el("button", { class: "btn", style: { background: "var(--accent)", color: "#fff" }, onclick: async () => {
              try { await o.save(it, ta.value); toast("Saved"); show(); } catch (err) { fail(err); }
            } }, icon("check"), "Save"));
          } }, icon("edit"), el("span", { class: "lbl", text: "Edit" })));
        }
      } catch (e) { doc.replaceChildren(el("p", { text: e.message })); }
    } else {
      stage.append(el("div", { class: "nothumb" }, kindIcon(it.kind), el("div", { text: it.name }),
        el("p", { class: "muted", text: fmtSize(it.size) + " · this kind of file doesn't open in the browser" }),
        el("a", { class: "btn primary", href: o.download(it) }, icon("download"), "Download")));
    }
  }
  document.body.append(v);
  document.addEventListener("keydown", key);
  show();
  return { close };
}

// --- uploads, like copyparty: big files go in 8 MB pieces, three files at a time; a dropped piece is retried, and a
// reloaded page resumes what had arrived.

class Uploader {
  // send: { start(file, path) → {id, received}, chunk(id, offset, blob, onProgress) → received, finish(id) → result }
  // or send.put(file, path, onProgress) for links that take whole files.
  constructor(send, onDone) {
    this.send = send; this.onDone = onDone;
    this.queue = []; this.active = 0; this.panel = null;
  }
  add(entries) {
    for (const { file, path, share } of entries) {
      const job = { file, path, share, sent: 0, state: "waiting", started: 0 };
      this.queue.push(job);
    }
    this.render();
    this.pump();
  }
  pump() {
    while (this.active < 3) {
      const job = this.queue.find(j => j.state === "waiting");
      if (!job) break;
      this.active++;
      job.state = "sending"; job.started = performance.now();
      this.run(job).then(() => { job.state = "done"; job.sent = job.file.size; },
        e => { if (job.state !== "cancelled") { job.state = "failed"; job.error = e.message || String(e); } })
        .finally(() => { this.active--; this.render(); this.pump(); if (job.state === "done" && this.onDone) this.onDone(job); this.maybeFinish(); });
    }
    this.render();
  }
  async run(job) {
    const s = this.send;
    if (s.put) { await s.put(job.file, job.path, n => { job.sent = n; this.tick(job); }); return; }
    const CHUNK = 8 << 20;
    const up = await s.start(job.file, job.path, job);
    job.id = up.id; job.sent = up.received;
    let tries = 0;
    while (job.sent < job.file.size) {
      if (job.state === "cancelled") throw new Error("cancelled");
      const end = Math.min(job.file.size, job.sent + CHUNK), base = job.sent;
      try {
        job.sent = await s.chunk(job.id, base, job.file.slice(base, end), n => { job.sent = base + n; this.tick(job); });
        tries = 0;
      } catch (e) {
        if (e.received != null) { job.sent = e.received; continue; } // the server says where to carry on
        if (++tries > 8) throw e;
        await new Promise(r => setTimeout(r, Math.min(30000, 1000 * 2 ** tries)));
        try { job.sent = (await s.status(job.id)).received; } catch (_) { /* try again */ }
      }
      this.tick(job);
    }
    job.result = await s.finish(job.id, job);
  }
  cancel(job) {
    if (job.state === "waiting" || job.state === "sending") {
      job.state = "cancelled";
      job.xhr && job.xhr.abort();
      if (job.id && this.send.cancel) this.send.cancel(job.id).catch(() => {});
    }
    this.queue = this.queue.filter(j => j !== job);
    this.render();
    this.maybeFinish();
  }
  retry(job) { job.state = "waiting"; job.error = null; this.pump(); }
  maybeFinish() {
    if (this.queue.length && this.queue.every(j => j.state === "done")) {
      clearTimeout(this.hideTimer);
      this.hideTimer = setTimeout(() => { if (this.queue.every(j => j.state === "done")) { this.queue = []; this.render(); } }, 4000);
    }
  }
  tick(job) {
    const now = performance.now();
    if (now - (this.lastDraw || 0) > 150) { this.lastDraw = now; this.render(); }
  }
  render() {
    if (!this.queue.length) { this.panel && this.panel.remove(); this.panel = null; return; }
    if (!this.panel) {
      this.panel = el("div", { class: "uploads card" });
      document.body.append(this.panel);
    }
    const total = this.queue.reduce((a, j) => a + j.file.size, 0);
    const sent = this.queue.reduce((a, j) => a + (j.state === "done" ? j.file.size : j.sent), 0);
    const left = this.queue.filter(j => j.state === "waiting" || j.state === "sending").length;
    const failed = this.queue.filter(j => j.state === "failed").length;
    const sending = this.queue.filter(j => j.state === "sending");
    const speed = sending.reduce((a, j) => a + j.sent / Math.max(0.5, (performance.now() - j.started) / 1000), 0);
    const sum = left ? `${left} left · ${fmtSize(sent)} of ${fmtSize(total)}${speed > 0 ? " · " + fmtSize(speed) + "/s" : ""}`
      : failed ? `${failed} didn't upload` : "All uploaded";
    const min = this.panel.classList.contains("min");
    this.panel.replaceChildren(
      el("header", {}, icon(left ? "upload" : failed ? "close" : "check"),
        el("div", { class: "sum" }, el("b", { text: left ? "Uploading" : failed ? "Some uploads failed" : "Done" }), el("small", { text: sum })),
        el("button", { class: "btn ghost icon", onclick: () => { this.panel.classList.toggle("min"); }, "aria-label": "Minimize" }, icon(min ? "plus" : "minus")),
        el("button", { class: "btn ghost icon", "aria-label": "Close", onclick: () => { this.queue.filter(j => j.state !== "done").forEach(j => this.cancel(j)); this.queue = []; this.render(); } }, icon("close"))),
      el("div", { class: "items" }, this.queue.map(j => {
        const pct = j.file.size ? Math.round(100 * (j.state === "done" ? 1 : j.sent / j.file.size)) : (j.state === "done" ? 100 : 0);
        const status = j.state === "failed" ? j.error : j.state === "done" ? fmtSize(j.file.size) : j.state === "waiting" ? "Waiting" : pct + "%";
        return el("div", { class: "up " + j.state },
          el("div", { class: "t" }, el("span", { class: "n", text: j.path.replace(/^\//, ""), title: j.path }), el("span", { class: "s", text: status }),
            j.state === "failed" ? el("button", { onclick: () => this.retry(j), title: "Try again" }, icon("refresh")) : null,
            j.state !== "done" ? el("button", { onclick: () => this.cancel(j), title: "Cancel" }, icon("close")) : null),
          el("div", { class: "progress" }, el("div", { style: { width: pct + "%" } })));
      })));
    if (min) this.panel.classList.add("min");
  }
}

// xhrSend sends a body with progress; it rejects with .status, .received (when the server says how much arrived).
function xhrSend(method, url, body, onProgress, headers) {
  return new Promise((resolve, reject) => {
    const x = new XMLHttpRequest();
    x.open(method, url);
    x.setRequestHeader("X-Cove", "1");
    x.setRequestHeader("X-Cove-Household", coveHome());
    for (const [k, v] of Object.entries(headers || {})) x.setRequestHeader(k, v);
    if (onProgress) x.upload.onprogress = e => onProgress(e.loaded);
    x.onload = () => {
      let data = null;
      try { data = JSON.parse(x.responseText); } catch (_) { /* not JSON */ }
      if (x.status >= 200 && x.status < 300) resolve(data);
      else { const e = new Error((data && data.error) || "The server answered " + x.status); e.status = x.status; if (data && data.received != null) e.received = data.received; reject(e); }
    };
    x.onerror = () => reject(new Error("The connection dropped."));
    x.send(body);
  });
}

// filesFromDrop lists dropped files, going into dropped folders.
async function filesFromDrop(dt) {
  const out = [];
  const entries = [...dt.items].map(i => i.webkitGetAsEntry && i.webkitGetAsEntry()).filter(Boolean);
  if (!entries.length) return [...dt.files].map(f => ({ file: f, rel: f.name }));
  const walk = async (entry, prefix) => {
    if (entry.isFile) {
      const file = await new Promise((res, rej) => entry.file(res, rej));
      out.push({ file, rel: prefix + file.name });
    } else if (entry.isDirectory) {
      const reader = entry.createReader();
      let batch;
      do {
        batch = await new Promise((res, rej) => reader.readEntries(res, rej));
        for (const e of batch) await walk(e, prefix + entry.name + "/");
      } while (batch.length);
    }
  };
  for (const e of entries) await walk(e, "");
  return out;
}

function copyText(text) {
  navigator.clipboard.writeText(text).then(() => toast("Copied"), () => toast("Copy it from the box", true));
}

function copyField(value) {
  const input = el("input", { class: "input mono", value, readOnly: true, onfocus: e => e.target.select() });
  return el("div", { class: "copy" }, input, el("button", { class: "btn", type: "button", onclick: () => copyText(value) }, icon("copy"), "Copy"));
}

// --- documents in Collabora Online, full screen. o: { name, open() → {url, token, ttl, write}, onVersions?, onClose? }

async function openDocument(o) {
  let r;
  try { r = await o.open(); } catch (e) { return fail(e); }
  const frameName = "office" + Date.now();
  const frame = el("iframe", { name: frameName, title: o.name, allow: "clipboard-read; clipboard-write; fullscreen" });
  const status = el("span", { class: "muted", style: { fontSize: "12.5px" }, text: r.write ? "" : "View only" });
  const close = () => { removeEventListener("message", onMsg); box.remove(); o.onClose && o.onClose(); };
  const kinds = { docx: "doc", doc: "doc", odt: "doc", rtf: "doc", xlsx: "sheet", xls: "sheet", ods: "sheet", csv: "sheet", pptx: "slides", ppt: "slides", odp: "slides" };
  const box = el("div", { class: "office" },
    el("div", { class: "obar" }, kindIcon(kinds[extOf(o.name)] || "doc"), el("div", { class: "title", text: o.name }), status,
      o.onVersions && r.write ? el("button", { class: "btn ghost", onclick: () => o.onVersions() }, icon("clock"), el("span", { class: "lbl", text: "Versions" })) : null,
      el("button", { class: "btn", onclick: close }, icon("close"), "Close")),
    frame);
  const office = new URL(r.url, location.href).origin;
  if (office === location.origin) { // the built-in office shares Cove's storage: skip its first-visit tour
    try { for (const app of ["text", "spreadsheet", "presentation", "drawing"]) localStorage.setItem(app + ".onboardingTourSeen", "true"); } catch (_) { /* private window */ }
  }
  const post = (id, values) => frame.contentWindow && frame.contentWindow.postMessage(JSON.stringify({ MessageId: id, SendTime: Date.now(), Values: values || {} }), office);
  const onMsg = e => {
    if (e.origin !== office || e.source !== frame.contentWindow) return;
    let m;
    try { m = typeof e.data === "string" ? JSON.parse(e.data) : e.data; } catch (_) { return; }
    const v = m.Values || {};
    switch (m.MessageId) {
      case "App_LoadingStatus":
        if (v.Status === "Frame_Ready") post("Host_PostmessageReady");
        if (v.Status === "Document_Loaded" && r.write) status.textContent = "All changes saved";
        break;
      case "Doc_ModifiedStatus":
        if (r.write) status.textContent = v.Modified ? "Saving soon…" : "All changes saved";
        break;
      case "Action_Save_Resp":
        if (r.write) status.textContent = v.success === false ? "Couldn't save: " + (v.errorMsg || "try again") : "All changes saved";
        break;
      case "UI_FileVersions":
        o.onVersions && o.onVersions();
        break;
      case "UI_Close":
        close();
        break;
    }
  };
  addEventListener("message", onMsg);
  document.body.append(box);
  const form = el("form", { method: "post", action: r.url, target: frameName, class: "hide" },
    el("input", { type: "hidden", name: "access_token", value: r.token }),
    el("input", { type: "hidden", name: "access_token_ttl", value: String(r.ttl) }));
  box.append(form);
  form.submit();
  return { close };
}

// Installed as an app (Add to Home Screen), Cove needs a service worker.
if ("serviceWorker" in navigator && location.pathname === "/") navigator.serviceWorker.register("/sw.js").catch(() => {});
