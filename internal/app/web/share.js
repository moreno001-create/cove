// A link someone shared: a file to view and download, a folder to browse (and maybe add to), or a drop box.
"use strict";

const token = location.pathname.split("/")[2];
const base = "/s/" + encodeURIComponent(token);
const L = {
  info: p => base + "/info?path=" + encPath(p),
  content: p => base + "/content?path=" + encPath(p),
  download: p => base + "/content?download=1&path=" + encPath(p),
  thumb: (p, w) => base + "/thumb?w=" + (w || 320) + "&path=" + encPath(p),
  zip: p => base + "/zip?path=" + encPath(p),
};
let sub = decodeURIComponent((location.hash || "#/").slice(1)) || "/";
let info = null;
let officeExts = new Set();
const page = document.getElementById("page");

async function load() {
  try {
    const r = await fetch(L.info(sub));
    info = await r.json();
    if (!r.ok) throw new Error(info.error || "This link doesn't work.");
  } catch (e) {
    page.replaceChildren(header("Cove"), el("div", { class: "card empty" }, icon("share"), el("h3", { text: "This link doesn't work" }), el("p", { text: e.message })));
    return;
  }
  if (info.needsPassword) return passwordForm();
  if (info.mode !== "drop" && !officeExts.size) {
    try { const r = await fetch(base + "/office"); if (r.ok) officeExts = new Set((await r.json()).exts); } catch (_) { /* no documents */ }
  }
  if (info.mode === "drop") return dropBox();
  if (!info.dir) return singleFile();
  folder();
}

function header(title, subtitle, ...actions) {
  return el("div", { class: "head" }, el("img", { src: "/assets/icon.svg", alt: "" }),
    el("div", { style: { flex: 1, minWidth: 0 } }, el("h1", { text: title }), subtitle ? el("small", { text: subtitle }) : null), ...actions);
}

function passwordForm() {
  const pw = el("input", { class: "input", type: "password", placeholder: "Password", autocomplete: "current-password" });
  const form = el("form", { class: "card section", style: { maxWidth: "420px", margin: "40px auto" } },
    el("h3", {}, "“" + info.name + "” has a password"), el("p", { text: (info.owner || "Someone") + " shared it with a password. Ask them for it." }),
    el("div", { class: "field" }, pw), el("button", { class: "btn primary" }, icon("lock"), "Open"));
  form.onsubmit = async e => {
    e.preventDefault();
    const r = await fetch(base + "/unlock", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ password: pw.value }) });
    if (r.ok) load(); else { toast("That isn't the password.", true); pw.select(); }
  };
  page.replaceChildren(header("Cove"), form);
  pw.focus();
}

// openDoc opens a document from the link in Collabora; with an edit link, the guest's name is what the others see.
async function openDoc(p, name) {
  let guest = "";
  if (info.mode === "edit") {
    guest = localStorage.getItem("cove.guestname") || "";
    if (!guest) {
      guest = await ask("Your name", "Shown to the others editing", "", "Open");
      if (guest === null) return;
      localStorage.setItem("cove.guestname", guest);
    }
  }
  openDocument({ name, open: () => fetch(base + "/office", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path: p, name: guest }) })
    .then(async r => { const d = await r.json(); if (!r.ok) throw new Error(d.error || "It couldn't be opened."); return d; }) });
}

function singleFile() {
  const it = { ...info.item, path: "/" };
  if (officeExts.has(extOf(it.name))) {
    const edit = info.mode === "edit";
    page.replaceChildren(
      header(it.name, `${fmtSize(it.size)} · shared by ${info.owner}`, el("a", { class: "btn", href: L.download("/") }, icon("download"), el("span", { class: "lbl", text: "Download" }))),
      el("div", { class: "card empty" }, kindIcon(it.kind), el("h3", { text: it.name }),
        el("p", { text: edit ? info.owner + " shared this to edit together. Changes save as you go." : info.owner + " shared this to read." }),
        el("button", { class: "btn primary", onclick: () => openDoc("/", it.name) }, icon(edit ? "edit" : "open"), edit ? "Edit document" : "Open document")));
    return;
  }
  const preview = el("div", { class: "card", style: { overflow: "hidden", display: "grid", placeItems: "center", minHeight: "300px", background: "var(--sunk)" } });
  if (it.kind === "image") preview.append(el("img", { src: L.content("/"), alt: it.name, style: { maxWidth: "100%", maxHeight: "75vh", display: "block" } }));
  else if (it.kind === "video") preview.append(el("video", { src: L.content("/"), controls: true, playsInline: true, style: { maxWidth: "100%", maxHeight: "75vh" } }));
  else if (it.kind === "audio") preview.append(el("div", { style: { padding: "40px", textAlign: "center" } }, kindIcon("audio"), el("audio", { src: L.content("/"), controls: true, style: { marginTop: "16px" } })));
  else if (it.kind === "pdf") preview.append(el("iframe", { src: L.content("/"), title: it.name, style: { width: "100%", height: "78vh", border: 0 } }));
  else preview.append(el("div", { class: "empty" }, kindIcon(it.kind), el("h3", { text: it.name }), el("p", { text: fmtSize(it.size) })));
  page.replaceChildren(
    header(it.name, `${fmtSize(it.size)} · shared by ${info.owner}`, el("a", { class: "btn primary", href: L.download("/") }, icon("download"), el("span", { class: "lbl", text: "Download" }))),
    preview);
}

let view = localStorage.getItem("cove.shareview") || "grid";

function folder() {
  const items = info.items.map(it => ({ ...it, path: joinPath(sub, it.name) }));
  const crumbs = el("div", { class: "crumbs" }, el("button", { text: info.name, onclick: () => go("/") }));
  let acc = "";
  for (const part of sub.split("/").filter(Boolean)) { acc += "/" + part; const p = acc; crumbs.append(el("span", { class: "sep" }, icon("right")), el("button", { text: part, onclick: () => go(p) })); }
  const fileInput = el("input", { type: "file", multiple: true, class: "hide", onchange: e => { send([...e.target.files].map(f => ({ file: f, rel: f.name }))); e.target.value = ""; } });
  const open = it => {
    if (it.dir) return go(it.path);
    if (officeExts.has(extOf(it.name))) return openDoc(it.path, it.name);
    const list = items.filter(i => !i.dir);
    openViewer({ items: list, index: list.indexOf(it), url: i => L.content(i.path), thumb: (i, w) => L.thumb(i.path, w), download: i => L.download(i.path) });
  };
  const thumbOk = it => it.kind === "image" && !["svg", "heic", "heif", "tif", "tiff", "ico", "avif", "bmp"].includes(extOf(it.name));
  const body = !items.length ? el("div", { class: "card empty" }, icon("folder"), el("h3", { text: "This folder is empty" }))
    : view === "grid" ? el("div", { class: "grid" }, items.map(it => el("div", { class: "tile", onclick: () => open(it) },
      el("div", { class: "thumb" }, thumbOk(it) ? el("img", { src: L.thumb(it.path, 360), loading: "lazy", alt: "" }) : kindIcon(it.kind)),
      el("div", { class: "cap", title: it.name }, it.name, el("small", { text: it.dir ? "Folder" : fmtSize(it.size) })))))
      : el("div", { class: "card list" }, items.map(it => el("div", { class: "lrow", style: { gridTemplateColumns: "1fr 110px 150px 40px" }, onclick: () => open(it) },
        el("div", { class: "name" }, kindIcon(it.kind), el("span", { text: it.name })), el("div", { class: "size", text: it.dir ? "" : fmtSize(it.size) }),
        el("div", { class: "date", text: fmtDate(it.mod) }),
        it.dir ? el("div") : el("a", { class: "btn ghost icon", href: L.download(it.path), onclick: e => e.stopPropagation(), "aria-label": "Download" }, icon("download")))));
  page.replaceChildren(
    header(info.name, "Shared by " + info.owner + (info.mode === "upload" ? " · you can add files" : ""),
      fileInput, info.mode === "upload" ? el("button", { class: "btn", onclick: () => fileInput.click() }, icon("upload"), el("span", { class: "lbl", text: "Add files" })) : null,
      el("a", { class: "btn primary", href: L.zip(sub) }, icon("download"), el("span", { class: "lbl", text: "Download all" }))),
    el("div", { class: "bar" }, crumbs, el("div", { class: "grow" }), el("div", { class: "seg" },
      el("button", { class: view === "list" ? "on" : "", onclick: () => { view = "list"; localStorage.setItem("cove.shareview", view); folder(); } }, icon("list")),
      el("button", { class: view === "grid" ? "on" : "", onclick: () => { view = "grid"; localStorage.setItem("cove.shareview", view); folder(); } }, icon("grid")))),
    body);
  if (info.mode === "upload") wireDrop(page);
}

function go(p) { sub = p; location.hash = p === "/" ? "" : "#" + p; load(); }
addEventListener("hashchange", () => { const s = decodeURIComponent(location.hash.slice(1)) || "/"; if (s !== sub) { sub = s; load(); } });

// Uploads through a link send whole files; nothing already there is replaced.
const uploader = new Uploader({
  put: (file, path, onProgress) => xhrSend("PUT", base + "/upload?path=" + encPath(parentOf(path)) + "&name=" + encodeURIComponent(baseName(path)), file, onProgress),
}, () => { if (info && info.mode === "upload") load(); else sent++; drawSent(); });
let sent = 0;
const sentLine = el("p", { class: "muted", style: { textAlign: "center" } });
function drawSent() { sentLine.textContent = sent ? `${sent} file${sent > 1 ? "s" : ""} sent. Thank you!` : ""; }

function send(list) {
  // folders dropped into a link arrive as their files, side by side
  uploader.add(list.map(({ file }) => ({ file, path: joinPath(sub, file.name) })));
}

function dropBox() {
  const input = el("input", { type: "file", multiple: true, class: "hide", onchange: e => { send([...e.target.files].map(f => ({ file: f }))); e.target.value = ""; } });
  const box = el("div", { class: "dropbox", onclick: () => input.click() }, icon("upload"),
    el("h3", { text: "Drop files here" }), el("p", { class: "muted", text: "or click to choose them. Only " + info.owner + " can see what's sent." }), input);
  box.addEventListener("dragover", e => { e.preventDefault(); box.classList.add("over"); });
  box.addEventListener("dragleave", () => box.classList.remove("over"));
  box.addEventListener("drop", async e => { e.preventDefault(); box.classList.remove("over"); send(await filesFromDrop(e.dataTransfer)); });
  page.replaceChildren(header("Send files to " + info.owner, "“" + info.name + "” · a drop box"), box, sentLine);
}

function wireDrop(target) {
  if (target.dataset.wired) return;
  target.dataset.wired = "1";
  document.addEventListener("dragover", e => { if ([...e.dataTransfer.types].includes("Files")) e.preventDefault(); });
  document.addEventListener("drop", async e => { e.preventDefault(); if (info && info.mode === "upload") send(await filesFromDrop(e.dataTransfer)); });
}

load();
