// Cove's app: files, links, calendar, trash and settings, on one page (the address after # says which).
"use strict";

const S = {
  me: null,
  officeExts: new Set(),
  view: localStorage.getItem("cove.view") || "list",
  path: "/",
  items: [],
  sel: new Set(),
  lastClicked: null,
  share: null,     // the people-share being browsed (null: your own files)
  favs: new Set(), // starred, as "share|path"
  shareInfo: null, // {name, who, edit}
  edit: true,      // may change things here
};
// sq and shareQ add which share a request is about (nothing for your own files).
const sq = () => (S.share ? "&share=" + S.share : "");
const shareQ = () => (S.share ? "share=" + S.share : "");
const $ = id => document.getElementById(id);
const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

const files = {
  content: p => "/api/files/content?path=" + encPath(p) + sq(),
  download: p => "/api/files/content?download=1&path=" + encPath(p) + sq(),
  zip: p => "/api/files/zip?path=" + encPath(p) + sq(),
  thumb: (p, w) => "/api/files/thumb?w=" + (w || 320) + "&path=" + encPath(p) + sq(),
};

const uploader = new Uploader({
  start: (file, path, job) => api("POST", "/api/uploads" + (job.share ? "?share=" + job.share : ""), { path, size: file.size, mod: file.lastModified }),
  chunk: (id, offset, blob, onProgress) => xhrSend("PUT", `/api/uploads/${id}?offset=${offset}`, blob, onProgress).then(d => d.received),
  status: id => api("GET", "/api/uploads/" + id),
  finish: (id, job) => api("POST", `/api/uploads/${id}/finish` + (job.share ? "?share=" + job.share : ""), {}),
  cancel: id => api("DELETE", "/api/uploads/" + id),
}, job => {
  if (parentOf(job.result ? job.result.path : job.path) === S.path && (job.share || null) === S.share) refreshSoon();
});

let refreshTimer;
function refreshSoon() { clearTimeout(refreshTimer); refreshTimer = setTimeout(() => route(), 400); }

// --- shell

async function boot() {
  try { S.me = await api("GET", "/api/me"); } catch (e) { return; }
  if (S.me.office) api("GET", "/api/office").then(d => { S.officeExts = new Set(d.exts); }).catch(() => {});
  drawShell();
  addEventListener("hashchange", route);
  route();
  startLive();
  OFFQ.flush();
}

// the menu, in groups ("-label" starts one); kids don't see money, records or public links
const NAV = [["home", "home", "Home"], ["fridge", "pin", "Fridge"], ["chat", "mail", "Chat"], ["ask", "search", "Ask Cove"],
  ["-Files"], ["files", "folder", "My files"], ["photos", "image", "Photos"], ["recent", "clock", "Recent"], ["shared", "inbox", "Shared with me"], ["links", "share", "Shared links", "adult"],
  ["-Plans"], ["calendar", "calendar", "Calendar"], ["tasks", "check", "Tasks"], ["meals", "text", "Meals"], ["chores", "star", "Chores"], ["trips", "place", "Trips"],
  ["-Home"], ["contacts", "user", "Contacts"], ["notes", "edit", "Notes"], ["journal", "text", "Journal"], ["wishes", "star", "Wish lists"], ["health", "clock", "Health"], ["pantry", "inbox", "Pantry"],
  ["records", "key", "Home records", "adult"], ["budget", "sheet", "Budget", "adult"],
  ["-"], ["kiosk", "grid", "Kitchen display"], ["activity", "restore", "Activity"], ["trash", "trash", "Trash"], ["settings", "settings", "Settings"], ["admin", "lock", "Admin"]];

function drawShell() {
  const u = S.me.user;
  const name = u.display || u.name;
  $("side").replaceChildren(
    el("div", { class: "brand" }, el("img", { src: "/assets/icon.svg", alt: "" }), "Cove"),
    el("button", { class: "btn primary newbtn", onclick: e => globalNew(e.currentTarget) }, icon("plus"), "New"),
    el("nav", { class: "navlist" }, NAV.filter(([id, , , who]) => (id !== "admin" || S.me.user.admin) && !(who === "adult" && S.me.user.kid) && !(id === "ask" && !S.me.ai)).map(([id, ic, label]) =>
      id.startsWith("-") ? el("div", { class: "navgroup", text: id.slice(1) }) :
        el("button", { class: "nav", "data-nav": id, onclick: () => { document.body.classList.remove("nav-open"); location.hash = id === "files" ? "#/files/" : "#/" + id; } }, icon(ic), label,
          id === "chat" ? el("span", { class: "nbadge hide" }) : null))),
    el("div", { class: "grow" }),
    el("div", { class: "me" }, el("div", { class: "avatar", text: name.slice(0, 1).toUpperCase() }),
      el("div", { class: "who" }, el("div", { text: name }), el("div", { class: "muted", style: { fontSize: "12px" }, text: u.email || u.name })),
      S.me.devMode ? null : el("form", { method: "post", action: "/logout" }, el("button", { class: "btn ghost icon", title: "Sign out", "aria-label": "Sign out" }, icon("logout")))));
  const q = el("input", { type: "search", placeholder: "Search files, documents, events, people…", "aria-label": "Search", onkeydown: e => { if (e.key === "Enter" && q.value.trim()) location.hash = "#/search/" + encodeURIComponent(q.value.trim()); } });
  $("top").replaceChildren(
    el("button", { class: "btn ghost icon menu", onclick: () => document.body.classList.toggle("nav-open"), "aria-label": "Menu" }, icon("menu")),
    el("div", { class: "search" }, icon("search"), q), el("div", { class: "grow" }),
    el("button", { class: "btn ghost icon bell", id: "bell", "aria-label": "Notifications", onclick: e => notificationsPanel(e.currentTarget) }, icon("bell"), el("span", { class: "badge hide", id: "badge" })));
  api("GET", "/api/settings").then(st => { if (st.tz !== tz) api("PUT", "/api/settings", { ...st, tz }); }).catch(() => {}); // reminders go out in this time zone
  pollNotifications();
  chatBadge();
  setInterval(pollNotifications, 60000);
  document.addEventListener("click", e => { if (document.body.classList.contains("nav-open") && !$("side").contains(e.target) && !e.target.closest(".menu")) document.body.classList.remove("nav-open"); });
}

function route() {
  const h = location.hash || "#/home";
  const [, section, ...rest] = h.split("/");
  document.querySelectorAll("[data-nav]").forEach(b => b.classList.toggle("on", b.dataset.nav === (section === "search" ? "files" : section)));
  const c = $("content");
  c.onscroll = null;
  if (section !== "kiosk") document.body.classList.remove("kiosk");
  if (section !== "shared" || !rest[0]) { S.share = null; S.shareInfo = null; }
  switch (section) {
    case "shared": {
      if (!rest[0]) return showSharedWithMe(c);
      const id = rest[0];
      if (S.share !== id) { S.share = id; S.shareInfo = null; S.sel.clear(); }
      return showFiles(c, "/" + rest.slice(1).map(decodeURIComponent).filter(Boolean).join("/"));
    }
    case "home": return showHome(c);
    case "fridge": return showFridge(c);
    case "meals": return showMeals(c);
    case "chores": return showChores(c);
    case "photos": return showPhotos(c, rest);
    case "records": return showRecords(c);
    case "budget": return showBudget(c);
    case "trips": return showTrips(c, rest);
    case "chat": return showChat(c, rest);
    case "ask": return showAsk(c);
    case "wishes": return showWishes(c);
    case "health": return showHealth(c);
    case "pantry": return showPantry(c);
    case "journal": return showJournal(c);
    case "kiosk": return showKiosk(c);
    case "activity": return showActivity(c);
    case "duplicates": return showDuplicates(c);
    case "notes": return showNotes(c, rest.length ? "/" + rest.map(decodeURIComponent).join("/") : "");
    case "admin": return showAdmin(c);
    case "recent": return showRecent(c);
    case "tasks": return showTasks(c);
    case "contacts": return showContacts(c);
    case "links": return showLinks(c);
    case "calendar": return showCalendar(c);
    case "trash": return showTrash(c);
    case "settings": return showSettings(c);
    case "search": return showSearch(c, decodeURIComponent(rest.join("/")));
    default: {
      const p = "/" + rest.map(decodeURIComponent).filter(Boolean).join("/");
      return showFiles(c, p);
    }
  }
}

function goFolder(p, share) {
  if (share === undefined) share = S.share;
  const base = share ? "#/shared/" + share : "#/files";
  location.hash = base + (p === "/" ? "/" : p.split("/").map(encodeURIComponent).join("/"));
}

// --- files

async function showFiles(c, path) {
  if (path !== S.path) { S.sel.clear(); S.lastClicked = null; }
  S.path = path;
  let data;
  try {
    if (S.share && !S.shareInfo) {
      const mine = await api("GET", "/api/shared-with-me");
      const sh = mine.find(x => String(x.id) === String(S.share));
      if (!sh) throw new Error("That isn't shared with you any more.");
      S.shareInfo = sh;
    }
    data = await api("GET", "/api/files?path=" + encPath(path) + sq());
  } catch (e) {
    c.replaceChildren(el("div", { class: "empty" }, icon("folder"), el("h3", { text: "This folder isn't here" }), el("p", { text: e.message }),
      el("button", { class: "btn", onclick: () => goFolder("/", null) }, "Go to your files")));
    return;
  }
  S.edit = data.edit !== false;
  try { S.favs = new Set((await api("GET", "/api/favorites")).map(f => (f.share || "") + "|" + f.path)); } catch (_) { /* no stars */ }
  S.items = data.items.map(it => ({ ...it, path: joinPath(path, it.name) }));
  for (const p of [...S.sel]) if (!S.items.some(it => it.path === p)) S.sel.delete(p);
  drawFiles(c);
}

function drawFiles(c) {
  const path = S.path;
  const crumbs = el("div", { class: "crumbs" });
  if (S.share) crumbs.append(el("button", { text: "Shared with me", onclick: () => { location.hash = "#/shared"; } }), el("span", { class: "sep" }, icon("right")));
  crumbs.append(el("button", { text: S.share ? S.shareInfo.name : "My files", onclick: () => goFolder("/") }));
  let acc = "";
  for (const part of path.split("/").filter(Boolean)) {
    acc += "/" + part;
    const p = acc;
    crumbs.append(el("span", { class: "sep" }, icon("right")), el("button", { text: part, onclick: () => goFolder(p) }));
  }
  const fileInput = el("input", { type: "file", multiple: true, class: "hide", onchange: e => { uploadFiles([...e.target.files].map(f => ({ file: f, rel: f.name }))); e.target.value = ""; } });
  const dirInput = el("input", { type: "file", multiple: true, webkitdirectory: true, class: "hide", onchange: e => { uploadFiles([...e.target.files].map(f => ({ file: f, rel: f.webkitRelativePath || f.name }))); e.target.value = ""; } });
  const newMenu = e => popMenu(e.currentTarget, [
    { label: "Folder", icon: "folder", action: newFolder },
    "-",
    { label: "Word document", icon: "doc", action: () => newDoc("document") },
    { label: "Excel spreadsheet", icon: "sheet", action: () => newDoc("spreadsheet") },
    { label: "PowerPoint presentation", icon: "slides", action: () => newDoc("presentation") },
    { label: "From a template…", icon: "copy", action: () => templateDialog(["document", "spreadsheet"], t => t && newDoc(t.kind, t)) },
    "-", { label: "Text file", icon: "text", action: newText },
    S.share ? null : { label: "Find duplicate files", icon: "search", action: () => { location.hash = "#/duplicates"; } },
  ]);
  const shared = S.share ? el("span", { class: "pill" }, icon(S.edit ? "edit" : "lock"), (S.edit ? "You can edit · " : "View only · ") + "from " + S.shareInfo.who) : null;
  const bar = el("div", { class: "bar" }, crumbs, shared, el("div", { class: "grow" }), fileInput, dirInput,
    S.edit ? el("button", { class: "btn", onclick: newMenu }, icon("plus"), el("span", { class: "lbl", text: "New" })) : null,
    !S.edit ? null : el("button", { class: "btn primary", onclick: e => popMenu(e.currentTarget, [
      { label: "Upload files", icon: "file", action: () => fileInput.click() },
      { label: "Upload a folder", icon: "folder", action: () => dirInput.click() }]) }, icon("upload"), el("span", { class: "lbl", text: "Upload" })),
    el("div", { class: "seg" },
      el("button", { class: S.view === "list" ? "on" : "", title: "List", onclick: () => setView("list") }, icon("list")),
      el("button", { class: S.view === "grid" ? "on" : "", title: "Thumbnails", onclick: () => setView("grid") }, icon("grid"))));

  const parts = [bar];
  if (S.sel.size) parts.push(selectionBar());
  if (!S.items.length) {
    parts.push(el("div", { class: "card empty" }, icon("upload"), el("h3", { text: "Nothing here yet" }),
      el("p", { text: S.edit ? "Drop files or folders anywhere on this page, or use Upload." : "" })));
  } else if (S.view === "grid") {
    parts.push(el("div", { class: "grid" }, S.items.map(tile)));
  } else {
    const allSel = S.items.length && S.items.every(it => S.sel.has(it.path));
    parts.push(el("div", { class: "card list" },
      el("div", { class: "lrow head" },
        el("input", { type: "checkbox", class: "cb", checked: allSel, "aria-label": "Select all", onchange: e => { S.sel = e.target.checked ? new Set(S.items.map(i => i.path)) : new Set(); drawFiles(c); } }),
        el("div", { text: "Name" }), el("div", { text: "Size" }), el("div", { text: "Modified" }), el("div")),
      S.items.map(listRow)));
  }
  c.replaceChildren(...parts);
  wireDrop(c);
}

function canDrop() { return location.hash.startsWith("#/files") || (location.hash.startsWith("#/shared/") && S.edit); }

function setView(v) { S.view = v; localStorage.setItem("cove.view", v); drawFiles($("content")); }

function itemEvents(node, it) {
  node.draggable = true;
  node.ondragstart = e => { e.dataTransfer.setData("application/x-cove", JSON.stringify(S.sel.has(it.path) ? [...S.sel] : [it.path])); e.dataTransfer.effectAllowed = "move"; };
  if (it.dir) {
    node.ondragover = e => { if (e.dataTransfer.types.includes("application/x-cove")) { e.preventDefault(); node.classList.add("drop-target"); } };
    node.ondragleave = () => node.classList.remove("drop-target");
    node.ondrop = e => {
      const raw = e.dataTransfer.getData("application/x-cove");
      if (!raw) return;
      e.preventDefault(); e.stopPropagation(); node.classList.remove("drop-target");
      moveMany(JSON.parse(raw).filter(p => p !== it.path), it.path);
    };
  }
  node.onclick = e => {
    if (e.target.closest(".cb, .more")) return;
    if (e.metaKey || e.ctrlKey) return toggleSel(it.path);
    if (e.shiftKey && S.lastClicked) {
      const a = S.items.findIndex(i => i.path === S.lastClicked), b = S.items.findIndex(i => i.path === it.path);
      for (const i of S.items.slice(Math.min(a, b), Math.max(a, b) + 1)) S.sel.add(i.path);
      return drawFiles($("content"));
    }
    if (S.sel.size) return toggleSel(it.path);
    openItem(it);
  };
  node.oncontextmenu = e => { e.preventDefault(); itemMenu(e, it); };
}

function toggleSel(p) {
  S.sel.has(p) ? S.sel.delete(p) : S.sel.add(p);
  S.lastClicked = p;
  drawFiles($("content"));
}

function checkbox(it) {
  return el("input", { type: "checkbox", class: "cb", checked: S.sel.has(it.path), "aria-label": "Select " + it.name, onclick: e => { e.stopPropagation(); toggleSel(it.path); } });
}

function favKey(p) { return (S.share || "") + "|" + p; }

async function toggleStar(it) {
  const on = S.favs.has(favKey(it.path));
  try {
    if (on) await api("DELETE", "/api/favorites?path=" + encPath(it.path) + sq());
    else await api("POST", "/api/favorites?" + shareQ(), { path: it.path });
    toast(on ? "Unstarred" : "Starred: it's on your home page");
    route();
  } catch (e) { fail(e); }
}

function listRow(it) {
  const thumb = it.kind === "image" && !["svg", "heic", "heif", "tif", "tiff", "ico", "avif", "bmp"].includes(extOf(it.name))
    ? el("img", { src: files.thumb(it.path, 80), loading: "lazy", alt: "", onerror: e => e.target.replaceWith(kindIcon("image")) }) : kindIcon(it.kind);
  const row = el("div", { class: "lrow" + (S.sel.has(it.path) ? " sel" : "") },
    checkbox(it),
    el("div", { class: "name" }, thumb, el("span", { text: it.name, title: it.name }), S.favs.has(favKey(it.path)) ? el("span", { class: "star" }, icon("star")) : null),
    el("div", { class: "size", text: it.dir ? "" : fmtSize(it.size) }),
    el("div", { class: "date", text: fmtDate(it.mod) }),
    el("button", { class: "btn ghost icon more", "aria-label": "More", onclick: e => { e.stopPropagation(); itemMenu(e.currentTarget, it); } }, icon("more")));
  itemEvents(row, it);
  return row;
}

function tile(it) {
  const hasThumb = it.kind === "image" && !["svg", "heic", "heif", "tif", "tiff", "ico", "avif", "bmp"].includes(extOf(it.name));
  const t = el("div", { class: "tile" + (S.sel.has(it.path) ? " sel" : "") },
    checkbox(it),
    el("div", { class: "thumb" }, hasThumb ? el("img", { src: files.thumb(it.path, 360), loading: "lazy", alt: "", onerror: e => e.target.replaceWith(kindIcon("image")) }) : kindIcon(it.kind)),
    el("div", { class: "cap", title: it.name }, it.name, el("small", { text: it.dir ? "Folder" : fmtSize(it.size) })));
  itemEvents(t, it);
  return t;
}

function selectionBar() {
  const sel = S.items.filter(it => S.sel.has(it.path));
  return el("div", { class: "selbar" },
    el("strong", { text: sel.length + " selected" }),
    el("button", { class: "btn", onclick: () => sel.forEach(downloadItem) }, icon("download"), el("span", { class: "lbl", text: "Download" })),
    sel.length === 1 && !S.share ? el("button", { class: "btn", onclick: () => shareDialog(sel[0]) }, icon("share"), el("span", { class: "lbl", text: "Share" })) : null,
    !S.edit ? null : el("button", { class: "btn", onclick: () => pickFolder("Move " + (sel.length === 1 ? sel[0].name : sel.length + " items"), dest => moveMany(sel.map(i => i.path), dest)) }, icon("move"), el("span", { class: "lbl", text: "Move" })),
    S.edit ? el("button", { class: "btn danger", onclick: () => deleteMany(sel) }, icon("trash"), el("span", { class: "lbl", text: "Delete" })) : null,
    el("button", { class: "btn ghost icon", title: "Clear selection", onclick: () => { S.sel.clear(); drawFiles($("content")); } }, icon("close")));
}

function itemMenu(at, it) {
  const office = !it.dir && S.officeExts.has(extOf(it.name));
  popMenu(at, [
    { label: it.dir ? "Open" : office ? "Open in Office" : "Open", icon: "open", action: () => openItem(it) },
    { label: it.dir ? "Download as zip" : "Download", icon: "download", action: () => downloadItem(it) },
    { label: "Comments", icon: "text", action: () => commentsDialog(it) },
    !it.dir && extOf(it.name) === "zip" && S.edit ? { label: "Unzip here", icon: "archive", action: async () => {
      try { const r = await api("POST", "/api/files/unzip?" + shareQ(), { path: it.path }); toast(`Unpacked ${r.files} files`); goFolder(r.path); } catch (e) { fail(e); }
    } } : null,
    it.dir ? { label: "Size", icon: "archive", action: async () => {
      try { const r = await api("GET", "/api/files/info?path=" + encPath(it.path) + sq()); toast(`${it.name}: ${fmtSize(r.size)} in ${r.files} files and ${r.folders} folders`); } catch (e) { fail(e); }
    } } : null,
    S.share ? null : { label: "Share", icon: "share", action: () => shareDialog(it) },
    { label: S.favs.has(favKey(it.path)) ? "Unstar" : "Star", icon: "star", action: () => toggleStar(it) },
    it.dir ? null : { label: "Version history", icon: "clock", action: () => versionsDialog(it, S.edit) },
    S.edit ? "-" : null,
    S.edit ? { label: "Rename", icon: "edit", action: () => renameItem(it) } : null,
    S.edit ? { label: "Move to…", icon: "move", action: () => pickFolder("Move " + it.name, dest => moveMany([it.path], dest)) } : null,
    S.edit ? "-" : null,
    S.edit ? { label: "Delete", icon: "trash", danger: true, action: () => deleteMany([it]) } : null,
  ]);
}

function downloadItem(it) {
  const a = el("a", { href: it.dir ? files.zip(it.path) : files.download(it.path), download: "" });
  document.body.append(a); a.click(); a.remove();
}

function openItem(it) {
  if (it.dir) return goFolder(it.path);
  const ext = extOf(it.name);
  if (S.officeExts.has(ext)) return openOffice(it.path);
  if (it.kind === "audio") {
    const list = S.items.filter(i => i.kind === "audio");
    return player.play(list, list.indexOf(it));
  }
  const list = S.items.filter(i => !i.dir && i.kind !== "audio" && !S.officeExts.has(extOf(i.name)));
  openViewer({
    items: list, index: list.indexOf(it),
    url: i => files.content(i.path), thumb: (i, w) => files.thumb(i.path, w), download: i => files.download(i.path),
    save: S.edit ? async (i, text) => { await xhrSend("PUT", "/api/files/content?conflict=replace&path=" + encPath(i.path) + sq(), new Blob([text])); refreshSoon(); } : null,
    buttons: (i, close) => [S.me.ai && ["text", "pdf", "doc", "code", "sheet", "slides"].includes(i.kind) && !S.share ? el("button", { class: "btn", onclick: () => askAbout(i) }, icon("search"), el("span", { class: "lbl", text: "Ask Cove" })) : null,
      el("button", { class: "btn", onclick: () => commentsDialog(i) }, icon("text"), el("span", { class: "lbl", text: "Comments" }))].filter(Boolean).concat(S.share ? [] : [el("button", { class: "btn", onclick: () => { close(); shareDialog(i); } }, icon("share"), el("span", { class: "lbl", text: "Share" }))]),
  });
}

function uploadFiles(list) {
  if (!list.length) return;
  if (!S.edit) return toast("This is shared with you to view only.", true);
  uploader.add(list.map(({ file, rel }) => ({ file, path: joinPath(S.path, rel), share: S.share })));
}

let dragDepth = 0;
function wireDrop(c) {
  if (c.dataset.wired) return;
  c.dataset.wired = "1";
  const zone = el("div", { class: "dropzone hide" }, el("div", {}, icon("upload"), "Drop to upload here"));
  document.body.append(zone);
  const external = e => e.dataTransfer && [...e.dataTransfer.types].includes("Files");
  c.addEventListener("dragenter", e => { if (!external(e) || !canDrop()) return; dragDepth++; zone.classList.remove("hide"); });
  c.addEventListener("dragleave", e => { if (!external(e)) return; if (--dragDepth <= 0) { dragDepth = 0; zone.classList.add("hide"); } });
  c.addEventListener("dragover", e => { if (external(e) && canDrop()) e.preventDefault(); });
  c.addEventListener("drop", async e => {
    if (!external(e) || !canDrop()) return;
    e.preventDefault(); dragDepth = 0; zone.classList.add("hide");
    try { uploadFiles(await filesFromDrop(e.dataTransfer)); } catch (err) { fail(err); }
  });
}

async function newFolder() {
  const name = await ask("New folder", "Name", "New folder", "Create");
  if (!name) return;
  try { await api("POST", "/api/files/mkdir?" + shareQ(), { path: joinPath(S.path, name) }); route(); } catch (e) { fail(e); }
}

// newDoc makes a Word, Excel or PowerPoint file (blank, or from a template) where you are, and opens it.
async function newDoc(kind, template) {
  const label = template ? template.name : { document: "Document", spreadsheet: "Spreadsheet", presentation: "Presentation" }[kind];
  const what = { document: "Word document", spreadsheet: "Excel spreadsheet", presentation: "PowerPoint presentation" }[kind];
  const name = await ask("New " + what, "Name", label, "Create");
  if (!name) return;
  const inFiles = /^#\/(files|shared\/)/.test(location.hash);
  if (!inFiles) { S.share = null; S.path = "/"; }
  try {
    const r = await api("POST", "/api/files/new?" + shareQ(), { path: S.path, kind, name, template: template ? template.id : "" });
    if (!inFiles) goFolder(parentOf(r.path), null); else await route();
    if (S.officeExts.size) openOffice(r.path);
    else toast("Made " + baseName(r.path) + ". Opening it needs the office (Collabora) to be running.");
  } catch (e) { fail(e); }
}

// globalNew is the sidebar's New button: anything Cove makes, from anywhere.
function globalNew(anchor) {
  const goto = (hash, then) => { location.hash = hash; if (then) setTimeout(then, 450); };
  popMenu(anchor, [
    { label: "Word document", icon: "doc", action: () => newDoc("document") },
    { label: "Excel spreadsheet", icon: "sheet", action: () => newDoc("spreadsheet") },
    { label: "PowerPoint presentation", icon: "slides", action: () => newDoc("presentation") },
    { label: "From a template…", icon: "copy", action: () => templateDialog(["document", "spreadsheet"], t => t && newDoc(t.kind, t)) },
    "-",
    { label: "Note", icon: "text", action: () => newNote() },
    { label: "Event", icon: "calendar", action: () => goto("#/calendar", () => eventDialog(null, (() => { const d = new Date(); d.setMinutes(0, 0, 0); d.setHours(d.getHours() + 1); return d; })(), $("content"))) },
    { label: "Task", icon: "check", action: () => goto("#/tasks", () => { const i = document.querySelector(".add-task input"); i && i.focus(); }) },
    { label: "On the fridge", icon: "pin", action: () => goto("#/fridge", () => fridgeDialog(null, "note", $("content"))) },
    { label: "Scan a document", icon: "pdf", action: () => scanDialog(location.hash.startsWith("#/files") ? S.path : "/Scans") },
    { label: "Recipe", icon: "text", action: () => goto("#/meals", () => recipeDialog(null, $("content"))) },
    S.me.user.kid ? null : { label: "Spending", icon: "sheet", action: () => goto("#/budget", () => { people().then(() => expenseDialog(null, $("content"))); }) },
    { label: "Trip", icon: "place", action: () => goto("#/trips", () => { people().then(() => tripDialog(null, $("content"))); }) },
    "-",
    { label: "Folder", icon: "folder", action: () => goto("#/files/", newFolder) },
    { label: "Upload files", icon: "upload", action: () => goto(location.hash.startsWith("#/files") ? location.hash : "#/files/", () => { const f = document.querySelector('.bar input[type=file]:not([webkitdirectory])'); f && f.click(); }) },
  ]);
}

async function newNote() {
  templateDialog(["note"], async t => {
    const title = await ask(t ? "New " + t.name.toLowerCase() : "New note", "Title", t ? t.name : "", "Create");
    if (title === null) return;
    try { const r = await api("POST", "/api/notes", { title, template: t ? t.id : "" }); location.hash = "#/notes" + r.path.split("/").map(encodeURIComponent).join("/"); } catch (e) { fail(e); }
  });
}

async function newText() {
  let name = await ask("New text file", "Name", "Notes.md", "Create");
  if (!name) return;
  if (!name.includes(".")) name += ".txt";
  try { await xhrSend("PUT", "/api/files/content?path=" + encPath(joinPath(S.path, name)) + sq(), new Blob([""])); route(); } catch (e) { fail(e); }
}

async function renameItem(it) {
  const name = await ask("Rename", "New name", it.name, "Rename");
  if (!name || name === it.name) return;
  try { await api("POST", "/api/files/move?" + shareQ(), { from: it.path, to: joinPath(parentOf(it.path), name) }); route(); } catch (e) { fail(e); }
}

async function moveMany(paths, dest) {
  let moved = 0;
  for (const p of paths) {
    if (parentOf(p) === dest) continue;
    try { await api("POST", "/api/files/move?" + shareQ(), { from: p, to: joinPath(dest, baseName(p)) }); moved++; } catch (e) { fail(e); }
  }
  if (moved) toast(`Moved ${moved} item${moved > 1 ? "s" : ""}`);
  S.sel.clear();
  route();
}

async function deleteMany(items) {
  const what = items.length === 1 ? `“${items[0].name}”` : items.length + " items";
  if (!(await confirmBox("Move to trash?", `${what} will be kept in the trash for 30 days.`, "Move to trash", true))) return;
  try { await api("POST", "/api/files/delete?" + shareQ(), { paths: items.map(i => i.path) }); toast(S.share ? "Moved to " + S.shareInfo.who + "'s trash" : "Moved to trash"); } catch (e) { fail(e); }
  S.sel.clear();
  route();
}

// pickFolder lets the user browse to a folder, then calls done with it.
function pickFolder(title, done) {
  let at = S.path;
  const listBox = el("div", { class: "card list", style: { maxHeight: "320px", overflow: "auto", boxShadow: "none" } });
  const where = el("div", { class: "crumbs", style: { fontSize: "14px", marginBottom: "8px" } });
  async function load() {
    where.replaceChildren(el("button", { text: S.share ? S.shareInfo.name : "My files", type: "button", onclick: () => { at = "/"; load(); } }));
    let acc = "";
    for (const part of at.split("/").filter(Boolean)) { acc += "/" + part; const p = acc; where.append(el("span", { class: "sep" }, icon("right")), el("button", { type: "button", text: part, onclick: () => { at = p; load(); } })); }
    try {
      const d = await api("GET", "/api/files?path=" + encPath(at) + sq());
      const dirs = d.items.filter(i => i.dir);
      listBox.replaceChildren(...(dirs.length ? dirs.map(i => el("div", { class: "lrow", style: { gridTemplateColumns: "1fr" }, onclick: () => { at = joinPath(at, i.name); load(); } },
        el("div", { class: "name" }, kindIcon("folder"), el("span", { text: i.name })))) : [el("div", { class: "empty", style: { padding: "26px" }, text: "No folders inside" })]));
    } catch (e) { fail(e); }
  }
  dialog({ title, wide: true, body: el("div", {}, where, listBox), buttons: [
    { label: "New folder", action: async () => { const n = await ask("New folder", "Name", "New folder", "Create"); if (n) { try { await api("POST", "/api/files/mkdir?" + shareQ(), { path: joinPath(at, n) }); at = joinPath(at, n); } catch (e) { fail(e); } } load(); return false; } },
    "grow", { label: "Cancel" }, { label: "Move here", primary: true, action: () => done(at) }] });
  load();
}

// --- share links

async function shareDialog(it) {
  const office = !it.dir && S.officeExts.has(extOf(it.name));
  // People in the household
  const peopleList = el("div", { class: "items-list", style: { margin: "0 -14px 6px" } });
  const who = el("select", { class: "input" });
  const canEdit = el("input", { type: "checkbox", checked: true });
  let everyone = [];
  async function loadPeople() {
    try {
      const [users, shares] = await Promise.all([api("GET", "/api/users"), api("GET", "/api/user-shares?path=" + encPath(it.path))]);
      everyone = users;
      const shared = new Set(shares.map(x => x.user));
      const others = users.filter(u => !shared.has(u.name));
      who.replaceChildren(...others.map(u => el("option", { value: u.name, text: u.display || u.name })));
      addRow.classList.toggle("hide", !others.length);
      nobody.classList.toggle("hide", users.length > 0);
      peopleList.replaceChildren(...shares.map(x => el("div", {}, el("div", { class: "avatar", text: x.who.slice(0, 1).toUpperCase() }),
        el("div", { class: "grow" }, el("div", { text: x.who })),
        el("select", { class: "input", style: { width: "auto" }, onchange: async e => {
          if (e.target.value === "remove") await api("DELETE", "/api/user-shares/" + x.id).catch(fail);
          else await api("POST", "/api/user-shares", { path: it.path, user: x.user, edit: e.target.value === "edit" }).catch(fail);
          loadPeople();
        } }, el("option", { value: "edit", text: "Can edit", selected: x.edit }), el("option", { value: "view", text: "Can view", selected: !x.edit }), el("option", { value: "remove", text: "Remove" })))));
    } catch (e) { fail(e); }
  }
  const addRow = el("div", { class: "row", style: { alignItems: "center" } }, who, el("label", { class: "check", style: { margin: 0, whiteSpace: "nowrap" } }, canEdit, "Can edit"),
    el("button", { class: "btn", type: "button", onclick: () => addPerson() }, "Share"));
  const nobody = el("p", { class: "muted hide", style: { margin: 0, fontSize: "13px" }, text: "Nobody else has signed in to Cove yet. Once they have, you can share with them here." });
  const addPerson = async () => {
    if (!who.value) return;
    try { await api("POST", "/api/user-shares", { path: it.path, user: who.value, edit: canEdit.checked }); toast("Shared"); loadPeople(); } catch (e) { fail(e); }
  };
  const peopleBox = el("div", { class: "card section", style: { boxShadow: "none", padding: "14px", marginBottom: "14px" } },
    el("h3", { text: "People" }),
    el("p", { class: "muted", style: { margin: "0 0 10px", fontSize: "13px" }, text: "It shows up under “Shared with me” for them." + (office ? " Everyone who can edit works on the document together, live." : "") }),
    peopleList, addRow, nobody);

  // Links anyone can open
  const existing = el("div");
  const modes = it.dir ? [["view", "View and download", "People with the link see the files."],
    ["upload", "View and add files", "They can also upload into this folder."],
    ["drop", "Only add files (drop box)", "They can upload, but can't see what's inside."]]
    : office ? [["view", "View", "They can read and download it."], ["edit", "Edit", "They can edit it in the browser, with no account."]] : [];
  let mode = "view";
  const choiceBox = el("div", { class: "choices" });
  const drawChoices = () => choiceBox.replaceChildren(...modes.map(([id, label, help]) => el("label", { class: "choice" + (mode === id ? " on" : "") },
    el("input", { type: "radio", name: "mode", checked: mode === id, onchange: () => { mode = id; drawChoices(); } }), el("div", {}, label, el("small", { text: help })))));
  drawChoices();
  const pw = el("input", { class: "input", type: "text", placeholder: "No password", autocomplete: "off" });
  const exp = el("select", { class: "input" }, [["0", "Never"], ["1", "In a day"], ["7", "In a week"], ["30", "In a month"], ["90", "In 3 months"]].map(([v, l]) => el("option", { value: v, text: l })));
  async function loadExisting() {
    try {
      const list = await api("GET", "/api/shares?path=" + encPath(it.path));
      existing.replaceChildren(...list.map(sh => el("div", { style: { marginBottom: "12px" } }, copyField(sh.url),
        el("div", { class: "row", style: { marginTop: "6px", alignItems: "center", flexWrap: "wrap", gap: "6px" } }, linkPills(sh), el("div", { style: { flex: 1 } }),
          el("button", { class: "btn ghost", type: "button", onclick: () => qrDialog(sh) }, icon("qr"), "QR"),
          el("button", { class: "btn ghost danger", type: "button", onclick: async () => { await api("DELETE", "/api/shares/" + sh.token).catch(fail); loadExisting(); } }, "Stop sharing")))));
    } catch (e) { fail(e); }
  }
  const linkBox = el("div", { class: "card section", style: { boxShadow: "none", padding: "14px", margin: 0 } },
    el("h3", { text: "Link" }), el("p", { class: "muted", style: { margin: "0 0 10px", fontSize: "13px" }, text: "Anyone with the link can open it, no account needed." }),
    existing, choiceBox,
    el("div", { class: "row" }, el("div", { class: "field" }, el("label", { text: "Password (optional)" }), pw), el("div", { class: "field" }, el("label", { text: "Link stops working" }), exp)),
    el("button", { class: "btn", type: "button", onclick: async () => {
      try {
        const sh = await api("POST", "/api/shares", { path: it.path, password: pw.value, expiresDays: +exp.value, mode });
        copyText(sh.url); pw.value = ""; loadExisting();
      } catch (e) { fail(e); }
    } }, icon("share"), "Create link"));
  peopleBox.append(groupShareBox("file", it.path));
  dialog({ title: "Share “" + it.name + "”", wide: true, body: el("div", {}, peopleBox, linkBox), buttons: [{ label: "Done", primary: true }] });
  loadPeople();
  loadExisting();
}

function linkPills(sh) {
  return [
    sh.downloads ? el("span", { class: "pill" }, icon("download"), sh.downloads + (sh.downloads === 1 ? " download" : " downloads")) : null,
    sh.mode === "edit" ? el("span", { class: "pill" }, icon("edit"), "Can edit") : sh.mode === "drop" ? el("span", { class: "pill" }, icon("inbox"), "Drop box") : sh.mode === "upload" ? el("span", { class: "pill" }, icon("upload"), "Can add files") : el("span", { class: "pill" }, icon("download"), "View"),
    sh.hasPassword ? el("span", { class: "pill" }, icon("lock"), "Password") : null,
    sh.expires ? el("span", { class: "pill" }, icon("clock"), (sh.expires * 1000 < Date.now() ? "Expired " : "Until ") + new Date(sh.expires * 1000).toLocaleDateString()) : null,
  ];
}

function qrDialog(sh) {
  dialog({ title: "Scan to open “" + sh.name + "”", body: el("div", { style: { textAlign: "center" } },
    el("img", { src: "/api/shares/" + sh.token + "/qr", alt: "QR code for the link", style: { width: "260px", height: "260px", imageRendering: "pixelated", borderRadius: "10px", background: "#fff", padding: "8px" } }),
    el("p", { class: "muted mono", style: { wordBreak: "break-all" }, text: sh.url })),
  buttons: [{ label: "Copy link", action: () => { copyText(sh.url); return false; } }, { label: "Done", primary: true }] });
}

async function showLinks(c) {
  c.replaceChildren(el("div", { class: "page-title", text: "Shared links" }));
  let list;
  try { list = await api("GET", "/api/shares"); } catch (e) { return fail(e); }
  if (!list.length) {
    c.append(el("div", { class: "card empty" }, icon("share"), el("h3", { text: "No links yet" }), el("p", { text: "Share a file or folder from its ⋯ menu to make a link anyone can open." })));
    return;
  }
  c.append(el("div", { class: "card items-list" }, list.map(sh => el("div", {},
    kindIcon(sh.dir ? "folder" : "file"),
    el("div", { class: "grow" }, el("div", { text: sh.name, style: { fontWeight: 600 } }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: sh.path }),
      el("div", { class: "row", style: { gap: "6px", marginTop: "5px", flexWrap: "wrap" } }, linkPills(sh))),
    el("button", { class: "btn", onclick: () => copyText(sh.url) }, icon("copy"), el("span", { class: "lbl", text: "Copy link" })),
    el("button", { class: "btn ghost icon", title: "QR code", onclick: () => qrDialog(sh) }, icon("qr")),
    el("button", { class: "btn ghost icon", title: "Show in Files", onclick: () => goFolder(parentOf(sh.path)) }, icon("folder")),
    el("button", { class: "btn ghost icon danger", title: "Stop sharing", onclick: async () => { await api("DELETE", "/api/shares/" + sh.token).catch(fail); showLinks(c); } }, icon("trash"))))));
}

// --- recent and shared with me

async function showRecent(c) {
  c.replaceChildren(el("div", { class: "page-title", text: "Recent" }));
  let list;
  try { list = await api("GET", "/api/recent"); } catch (e) { return fail(e); }
  if (!list.length) { c.append(el("div", { class: "card empty" }, icon("clock"), el("h3", { text: "Nothing yet" }), el("p", { text: "Documents and notes you open show up here." }))); return; }
  c.append(el("div", { class: "card list" }, list.map(it => el("div", { class: "lrow", style: { gridTemplateColumns: "1fr 150px 150px" }, onclick: () => {
    S.share = it.share ? String(it.share) : null; S.shareInfo = null;
    if (S.officeExts.has(extOf(it.name))) openOffice(it.path);
    else goFolder(parentOf(it.path));
  } }, el("div", { class: "name" }, kindIcon(it.kind), el("div", { style: { minWidth: 0 } }, el("div", { text: it.name }), el("div", { class: "muted", style: { fontSize: "12px" }, text: (it.share ? it.owner + " · " : "") + parentOf(it.path) }))),
    el("div", { class: "date", text: "Opened " + fmtDate(it.opened) }), el("div", { class: "date", text: "Changed " + fmtDate(it.mod) })))));
}

async function showSharedWithMe(c) {
  c.replaceChildren(el("div", { class: "page-title", text: "Shared with me" }));
  let list;
  try { list = await api("GET", "/api/shared-with-me"); } catch (e) { return fail(e); }
  if (!list.length) { c.append(el("div", { class: "card empty" }, icon("inbox"), el("h3", { text: "Nothing shared with you yet" }), el("p", { text: "When someone in the household shares a file or folder with you, it shows up here." }))); return; }
  c.append(el("div", { class: "card items-list" }, list.map(sh => el("div", { style: { cursor: "pointer" }, onclick: () => {
    if (sh.dir) return goFolder("/", String(sh.id));
    S.share = String(sh.id); S.shareInfo = sh;
    if (S.officeExts.has(extOf(sh.name))) return openOffice("/");
    const it = { name: sh.name, kind: sh.kind, path: "/", size: 0 };
    openViewer({ items: [it], index: 0, url: () => files.content("/"), thumb: (i, w) => files.thumb("/", w), download: () => files.download("/") });
  } }, kindIcon(sh.kind),
    el("div", { class: "grow" }, el("div", { text: sh.name, style: { fontWeight: 600 } }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: "From " + sh.who + " · " + fmtDate(sh.created * 1000) })),
    el("span", { class: "pill" }, icon(sh.edit ? "edit" : "lock"), sh.edit ? "Can edit" : "View only"),
    el("button", { class: "btn ghost icon", title: "Remove from my list", onclick: async e => {
      e.stopPropagation();
      if (!(await confirmBox("Remove “" + sh.name + "”?", "It stays with " + sh.who + "; you just won't see it here.", "Remove", true))) return;
      await api("DELETE", "/api/user-shares/" + sh.id).catch(fail); showSharedWithMe(c);
    } }, icon("close"))))));
}

// --- versions

async function versionsDialog(it, canRestore) {
  const box = el("div", { class: "items-list", style: { margin: "0 -20px" } }, el("div", { class: "muted", text: "Loading…" }));
  const dlg = dialog({ title: "Versions of “" + it.name + "”", wide: true, body: el("div", {},
    el("p", { class: "muted", style: { fontSize: "13px" }, text: "Cove keeps the earlier version whenever a file is replaced: at most one every 10 minutes while a document is being edited, the newest 30." }), box),
    buttons: [{ label: "Close", primary: true }] });
  try {
    const list = await api("GET", "/api/versions?path=" + encPath(it.path) + sq());
    if (!list.length) { box.replaceChildren(el("div", { class: "muted", text: "No earlier versions yet." })); return; }
    box.replaceChildren(...list.map(v => el("div", {}, icon("clock"),
      el("div", { class: "grow" }, el("div", { text: new Date(v.mod).toLocaleString([], { dateStyle: "medium", timeStyle: "short" }), style: { fontWeight: 600 } }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: fmtSize(v.size) })),
      el("a", { class: "btn", href: "/api/versions/content?path=" + encPath(it.path) + "&id=" + v.id + sq() }, icon("download"), el("span", { class: "lbl", text: "Download" })),
      canRestore ? el("button", { class: "btn", onclick: async () => {
        if (!(await confirmBox("Restore this version?", "What's there now is kept as a version too.", "Restore"))) return;
        try {
          await api("POST", "/api/versions/restore?" + shareQ(), { path: it.path, id: v.id });
          toast("Restored");
          dlg.close();
          document.querySelectorAll(".office").forEach(o => o.querySelector(".obar .btn:last-child").click());
          route();
        } catch (e) { fail(e); }
      } }, icon("restore"), el("span", { class: "lbl", text: "Restore" })) : null)));
  } catch (e) { box.replaceChildren(el("div", { text: e.message })); }
}

// --- search

async function showSearch(c, q) {
  c.replaceChildren(el("div", { class: "page-title", text: "Search: " + q }), el("p", { class: "muted", text: "Searching…" }));
  let r;
  try { r = await api("GET", "/api/search?q=" + encodeURIComponent(q) + "&tz=" + encodeURIComponent(tz)); } catch (e) { return fail(e); }
  const sections = [];
  const snippet = s => {
    const p = el("div", { class: "muted snip" });
    s.split("\x01").forEach((part, i) => {
      if (i === 0) { p.append(part); return; }
      const [hit, rest] = part.split("\x02");
      p.append(el("mark", { text: hit }), rest || "");
    });
    return p;
  };
  if (r.files.length) sections.push(el("h3", { class: "sec", text: "Files" }), el("div", { class: "card list" }, r.files.map(h => el("div", { class: "lrow", style: { gridTemplateColumns: "1fr 110px" }, onclick: () => {
    if (h.dir) return goFolder(h.path, null);
    S.share = null; S.path = parentOf(h.path);
    S.items = [{ ...h }];
    openItem({ ...h });
  } }, el("div", { class: "name" }, kindIcon(h.kind), el("div", { style: { minWidth: 0 } }, el("div", { text: h.name }), h.snippet ? snippet(h.snippet) : el("div", { class: "muted", style: { fontSize: "12px" }, text: parentOf(h.path) }))),
    el("div", { class: "size", text: h.dir ? "" : fmtSize(h.size) })))));
  if (r.related && r.related.length) sections.push(el("h3", { class: "sec", text: "Related (by meaning)" }), el("div", { class: "card list" }, r.related.map(h => el("div", { class: "lrow", style: { gridTemplateColumns: "1fr 110px" }, onclick: () => {
    S.share = null; S.path = parentOf(h.path); S.items = [{ ...h }]; openItem({ ...h });
  } }, el("div", { class: "name" }, kindIcon(h.kind), el("div", { style: { minWidth: 0 } }, el("div", { text: h.name }), el("div", { class: "muted snip", text: h.snippet || parentOf(h.path) }))),
    el("div", { class: "size", text: fmtSize(h.size) })))));
  if (r.events.length) sections.push(el("h3", { class: "sec", text: "Events" }), el("div", { class: "card items-list" }, r.events.map(ev => el("div", { style: { cursor: "pointer" }, onclick: () => {
    const d = ev.allDay ? parseYmd(ev.start) : new Date(ev.start);
    CAL.focus = new Date(d.getFullYear(), d.getMonth(), d.getDate()); CAL.month = new Date(d.getFullYear(), d.getMonth(), 1);
    location.hash = "#/calendar";
  } }, icon("calendar"), el("div", { class: "grow" }, el("div", { text: ev.title, style: { fontWeight: 600 } }),
    el("div", { class: "muted", style: { fontSize: "12.5px" }, text: (ev.allDay ? parseYmd(ev.start).toLocaleDateString([], { dateStyle: "medium" }) : new Date(ev.start).toLocaleString([], { dateStyle: "medium", timeStyle: "short" })) + (ev.where ? " · " + ev.where : "") }))))));
  if (r.tasks.length) sections.push(el("h3", { class: "sec", text: "Tasks" }), el("div", { class: "card items-list" }, r.tasks.map(t => el("div", { style: { cursor: "pointer" }, onclick: () => { TASKS.list = t.list; location.hash = "#/tasks"; } },
    icon(t.done ? "check" : "clock"), el("div", { class: "grow" }, el("div", { text: t.title, style: { fontWeight: 600, textDecoration: t.done ? "line-through" : "" } }), t.notes ? el("div", { class: "muted", style: { fontSize: "12.5px" }, text: t.notes }) : null)))));
  if (r.contacts.length) sections.push(el("h3", { class: "sec", text: "People" }), el("div", { class: "card items-list" }, r.contacts.map(p => el("div", { style: { cursor: "pointer" }, onclick: () => { CONTACTS.sel = p.id; location.hash = "#/contacts"; } },
    avatarOf(p, 32), el("div", { class: "grow" }, el("div", { text: p.name, style: { fontWeight: 600 } }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [p.org, ...(p.emails || []).map(e => e.value), ...(p.phones || []).map(x => x.value)].filter(Boolean).join(" · ") }))))));
  c.replaceChildren(el("div", { class: "page-title", text: "Search: " + q }), ...(sections.length ? sections : [el("div", { class: "card empty" }, icon("search"), el("h3", { text: "Nothing found" }),
    el("p", { text: "Search looks at names, the words inside documents, events, tasks and contacts." }))]));
}

// --- trash

async function showTrash(c) {
  let list, items;
  try { [list, items] = await Promise.all([api("GET", "/api/trash"), api("GET", "/api/trash/items")]); } catch (e) { return fail(e); }
  const head = el("div", { class: "bar" }, el("div", { class: "page-title", style: { margin: 0 }, text: "Trash" }), el("div", { class: "grow" }),
    list.length || items.length ? el("button", { class: "btn danger", onclick: async () => {
      if (!(await confirmBox("Empty the trash?", "Everything in it is deleted for good.", "Empty trash", true))) return;
      await api("POST", "/api/trash/delete", { all: true }).catch(fail);
      await api("POST", "/api/trash/items/delete", { all: true }).catch(fail);
      showTrash(c);
    } }, icon("trash"), "Empty trash") : null);
  if (!list.length && !items.length) { c.replaceChildren(head, el("div", { class: "card empty" }, icon("trash"), el("h3", { text: "The trash is empty" }), el("p", { text: "Deleted files, events, tasks and contacts stay here for 30 days." }))); return; }
  const kindIc = { event: "calendar", task: "check", contact: "user" };
  c.replaceChildren(...[head, el("p", { class: "muted", style: { marginTop: 0 }, text: "Deleted files, events, tasks and contacts stay here for 30 days, even when they were deleted on a phone." }),
    list.length ? el("h3", { class: "sec", text: "Files" }) : null,
    list.length ? el("div", { class: "card items-list" }, list.map(t => el("div", {}, kindIcon(t.kind),
      el("div", { class: "grow" }, el("div", { text: t.name, style: { fontWeight: 600 } }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: `${t.path} · ${fmtSize(t.size)} · deleted ${fmtDate(t.deleted * 1000)}` })),
      el("button", { class: "btn", onclick: async () => { try { await api("POST", "/api/trash/restore", { ids: [t.id] }); toast("Restored to " + parentOf(t.path)); showTrash(c); } catch (e) { fail(e); } } }, icon("restore"), el("span", { class: "lbl", text: "Restore" })),
      el("button", { class: "btn ghost icon danger", title: "Delete for good", onclick: async () => {
        if (!(await confirmBox("Delete for good?", `“${t.name}” can't be brought back.`, "Delete", true))) return;
        await api("POST", "/api/trash/delete", { ids: [t.id] }).catch(fail); showTrash(c);
      } }, icon("trash"))))) : null,
    items.length ? el("h3", { class: "sec", text: "Events, tasks and contacts" }) : null,
    items.length ? el("div", { class: "card items-list" }, items.map(t => el("div", {}, el("div", { class: "ficon k-file" }, icon(kindIc[t.kind] || "file")),
      el("div", { class: "grow" }, el("div", { text: t.title || "(no title)", style: { fontWeight: 600 } }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: `${t.kind[0].toUpperCase() + t.kind.slice(1)}${t.where ? " in " + t.where : ""} · deleted ${fmtDate(t.deleted * 1000)}` })),
      el("button", { class: "btn", onclick: async () => { try { await api("POST", "/api/trash/items/restore", { id: t.id }); toast("Restored"); showTrash(c); } catch (e) { fail(e); } } }, icon("restore"), el("span", { class: "lbl", text: "Restore" })),
      el("button", { class: "btn ghost icon danger", title: "Delete for good", onclick: async () => {
        await api("POST", "/api/trash/items/delete", { ids: [t.id] }).catch(fail); showTrash(c);
      } }, icon("trash"))))) : null].filter(Boolean));
}

// --- settings: account and sync apps

function notificationSettings() {
  const topic = el("input", { class: "input", placeholder: "https://ntfy." + location.hostname.split(".").slice(1).join(".") + "/cove-" + S.me.user.name });
  const browser = el("input", { type: "checkbox", checked: localStorage.getItem("cove.browsernotes") === "1" && "Notification" in window && Notification.permission === "granted" });
  browser.onchange = async () => {
    if (browser.checked && "Notification" in window) {
      const p = await Notification.requestPermission();
      if (p !== "granted") { browser.checked = false; toast("The browser didn't allow notifications.", true); return; }
    }
    localStorage.setItem("cove.browsernotes", browser.checked ? "1" : "0");
  };
  api("GET", "/api/settings").then(st => { topic.value = st.ntfy || ""; }).catch(() => {});
  return el("div", { class: "card section" }, el("h3", { text: "Notifications" }),
    el("p", { text: "Cove tells you when someone shares with you, changes a shared calendar or list, edits your document, or invites you, and reminds you of events and tasks. It shows them under the bell." }),
    el("label", { class: "check" }, browser, "Also show them as browser notifications while Cove is open"),
    el("div", { class: "row", style: { alignItems: "center", marginBottom: "14px", flexWrap: "wrap" } },
      el("button", { class: "btn primary", onclick: async () => { try { await enablePush(); toast("This device gets Cove's notifications now"); } catch (e) { fail(e); } } }, icon("bell"), "Get notifications on this device"),
      el("button", { class: "btn ghost", onclick: async () => { await disablePush().catch(() => {}); toast("Turned off on this device"); } }, "Turn off here"),
      el("span", { class: "muted", style: { fontSize: "12.5px" }, text: "Works on phones too: on an iPhone, first add Cove to the home screen and open it from there." })),
    el("div", { class: "field" }, el("label", { text: "On your phone, with ntfy (the address of a topic only you know)" }),
      el("div", { class: "copy" }, topic,
        el("button", { class: "btn", onclick: async () => {
          try { await api("PUT", "/api/settings", { ...(await api("GET", "/api/settings")), ntfy: topic.value, tz }); toast(topic.value ? "Saved" : "Phone notifications off"); } catch (e) { fail(e); }
        } }, "Save"),
        el("button", { class: "btn", onclick: async () => {
          try { await api("PUT", "/api/settings", { ...(await api("GET", "/api/settings")), ntfy: topic.value, tz }); await api("POST", "/api/settings/test-ntfy"); toast("Sent: look at your phone"); } catch (e) { fail(e); }
        } }, "Test"))),
    el("p", { class: "muted", style: { fontSize: "12.5px", margin: 0 }, text: "Install the ntfy app (or ntfy from Homeport's catalog for your own server), subscribe to the same topic, and paste its address here." }));
}

async function showSettings(c) {
  const u = S.me.user;
  const base = S.me.url;
  const host = base.replace(/^https?:\/\//, "");
  const pwList = el("div", { class: "items-list" });
  async function loadPasswords() {
    const list = await api("GET", "/api/app-passwords").catch(e => { fail(e); return []; });
    pwList.replaceChildren(...(list.length ? list.map(a => el("div", {}, icon("key"),
      el("div", { class: "grow" }, el("div", { text: a.name, style: { fontWeight: 600 } }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: "Made " + fmtDate(a.created * 1000) + " · " + (a.lastUsed ? "last used " + fmtDate(a.lastUsed * 1000) : "not used yet") })),
      el("button", { class: "btn danger", onclick: async () => {
        if (!(await confirmBox("Revoke “" + a.name + "”?", "Apps using it stop syncing until they get a new one.", "Revoke", true))) return;
        await api("DELETE", "/api/app-passwords/" + a.id).catch(fail); loadPasswords();
      } }, "Revoke"))) : [el("div", { class: "muted", text: "No app passwords yet." })]));
  }
  const newPassword = async () => {
    const name = await ask("New app password", "Which device or app is it for?", "iPhone", "Create");
    if (!name) return;
    try {
      const r = await api("POST", "/api/app-passwords", { name });
      dialog({ title: "App password for " + name, body: el("div", {},
        el("p", { class: "muted", text: "Type this into the app as the password. You'll only see it once." }),
        el("div", { class: "secret", text: r.password }),
        el("div", { class: "field" }, el("label", { text: "User name" }), copyField(r.user)),
        el("div", { class: "field" }, el("label", { text: "Server" }), copyField(base))),
        buttons: [{ label: "Copy password", action: () => { copyText(r.password); return false; } }, { label: "Done", primary: true }] });
      loadPasswords();
    } catch (e) { fail(e); }
  };
  const how = (title, steps) => el("div", {}, el("h4", { text: title }), el("ol", {}, steps.map(s => el("li", { text: s }))));
  c.replaceChildren(
    el("div", { class: "page-title", text: "Settings" }),
    el("div", { class: "card section" }, el("h3", { text: "Account" }),
      el("p", { text: S.me.devMode ? "Cove is in try-out mode (no sign-in)." : "You sign in to Cove with your Homeport account." }),
      el("div", { class: "row", style: { alignItems: "center" } }, el("div", { class: "avatar", text: (u.display || u.name).slice(0, 1).toUpperCase() }),
        el("div", { style: { flex: 1 } }, el("div", { text: u.display || u.name, style: { fontWeight: 600 } }), el("div", { class: "muted", text: (u.email ? u.email + " · " : "") + "user name " + u.name })),
        el("a", { class: "btn", href: "/api/export" }, icon("download"), "Download everything"),
        S.me.devMode ? null : el("form", { method: "post", action: "/logout" }, el("button", { class: "btn" }, icon("logout"), "Sign out")))),
    notificationSettings(),
    (() => { const b = el("div"); api("GET", "/api/settings").then(st => put(b, briefingSettings(st, x => api("PUT", "/api/settings", x)))).catch(() => {}); return b; })(),
    el("div", { class: "card section" }, el("div", { class: "row", style: { alignItems: "flex-start" } },
      el("div", { style: { flex: 1 } }, el("h3", { text: "Sync apps" }), el("p", { text: "Calendar apps, Finder, Windows and file-sync apps sign in with your user name and an app password, one per device. Revoking one stops just that device." })),
      el("button", { class: "btn primary", onclick: newPassword }, icon("plus"), "New app password")), pwList),
    el("div", { class: "card section" }, el("h3", { text: "Connect your devices" }),
      el("p", { text: "Use your user name (" + u.name + ") and an app password from above." }),
      el("div", { class: "field" }, el("label", { text: "Files (WebDAV)" }), copyField(base + "/dav/files/")),
      el("div", { class: "field" }, el("label", { text: "Calendars and tasks (CalDAV)" }), copyField(base + "/dav/cal/")),
      el("div", { class: "field" }, el("label", { text: "Contacts (CardDAV)" }), copyField(base + "/dav/card/")),
      el("div", { class: "howto" },
        how("iPhone and iPad: calendars and Reminders", ["Settings → Apps → Calendar → Calendar Accounts → Add Account → Other.", "Add CalDAV Account. Server: " + host + ".", "Your user name and an app password. Your task lists show up in Reminders."]),
        how("iPhone and iPad: contacts", ["Settings → Apps → Contacts → Contacts Accounts → Add Account → Other.", "Add CardDAV Account. Server: " + host + ".", "Your user name and an app password."]),
        how("Cove as an app", ["iPhone: open Cove in Safari → Share → Add to Home Screen.", "Android: Chrome menu → Install app. Then “Share → Cove” saves photos, files and links into “Shared to Cove”."]),
        how("Mac calendar", ["Calendar → Settings → Accounts → +.", "Other CalDAV Account, account type Manual.", "Server address: " + host + ", then your user name and app password."]),
        how("Android", ["Install DAVx⁵ (free on F-Droid), and jtx Board or Tasks.org for tasks.", "Add account → Login with URL and user name: " + base + ".", "Pick the calendars, task lists and address book to sync."]),
        how("Files on a Mac", ["In Finder: Go → Connect to Server (⌘K).", base + "/dav/files/", "Your user name and an app password."]),
        how("Files on Windows", ["File Explorer → This PC → Map network drive.", "Folder: " + base + "/dav/files/", "Your user name and an app password."]),
        how("Folders kept in sync", ["Any WebDAV sync app works: rclone (bisync), FolderSync on Android, or Mountain Duck.", "Server " + base + "/dav/files/ with your user name and an app password."]))),
    el("p", { class: "muted", style: { fontSize: "12px" }, text: "Cove " + S.me.version }));
  loadPasswords();
}

// --- calendar: month, week, day and list views; drag events to move them (or their bottom edge to resize) in week
// and day views

const CAL = {
  focus: new Date(new Date().getFullYear(), new Date().getMonth(), new Date().getDate()),
  get month() { return new Date(this.focus.getFullYear(), this.focus.getMonth(), 1); },
  set month(d) { this.focus = new Date(d); },
  mode: localStorage.getItem("cove.calmode") || (innerWidth < 820 ? "agenda" : "month"), // phones start with the list
  hidden: new Set(JSON.parse(localStorage.getItem("cove.calhidden") || "[]")),
  cals: [],
};
const COLORS = ["#3b82f6", "#10b981", "#f59e0b", "#ef4444", "#8b5cf6", "#ec4899", "#14b8a6", "#64748b"];
const HOUR = 48; // pixels per hour in week and day views

function ymd(d) { return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`; }
function parseYmd(s) { const [y, m, d] = s.split("-").map(Number); return new Date(y, m - 1, d); }
function addDays(d, n) { const x = new Date(d); x.setDate(x.getDate() + n); return x; }
function hm(d) { return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`; }
function timeLabel(d) { return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }); }
function evStart(o) { return o.allDay ? parseYmd(o.start) : new Date(o.start); }
function evEnd(o) { return o.allDay ? parseYmd(o.end) : new Date(o.end); }
function weekStart(d) { return addDays(new Date(d.getFullYear(), d.getMonth(), d.getDate()), -d.getDay()); }

async function showCalendar(c) {
  try { CAL.cals = await api("GET", "/api/calendars?kind=events"); } catch (e) { return fail(e); }
  const color = id => (CAL.cals.find(x => x.id === id) || {}).color || COLORS[0];
  const mode = CAL.mode;
  let from, to, title;
  if (mode === "month") {
    from = weekStart(CAL.month); to = addDays(from, 41);
    title = CAL.month.toLocaleDateString([], { month: "long", year: "numeric" });
  } else if (mode === "week") {
    from = weekStart(CAL.focus); to = addDays(from, 6);
    const end = addDays(from, 6);
    title = from.getMonth() === end.getMonth() ? from.toLocaleDateString([], { month: "long", year: "numeric" })
      : from.toLocaleDateString([], { month: "short" }) + " – " + end.toLocaleDateString([], { month: "short", year: "numeric" });
  } else if (mode === "day") {
    from = CAL.focus; to = CAL.focus;
    title = CAL.focus.toLocaleDateString([], { weekday: "long", month: "long", day: "numeric" });
  } else {
    from = new Date(); from.setHours(0, 0, 0, 0); to = addDays(from, 90);
    title = "Coming up";
  }
  let events = [];
  try { events = await api("GET", `/api/events?from=${ymd(from)}&to=${ymd(to)}&tz=${encodeURIComponent(tz)}`); } catch (e) { fail(e); }
  events = events.filter(o => !CAL.hidden.has(o.cal));

  const step = n => {
    const f = CAL.focus;
    CAL.focus = mode === "month" ? new Date(f.getFullYear(), f.getMonth() + n, 1) : addDays(f, n * (mode === "week" ? 7 : 1));
    showCalendar(c);
  };
  const bar = el("div", { class: "bar" },
    el("div", { class: "page-title", style: { margin: 0, minWidth: "170px" }, text: title }),
    mode !== "agenda" ? [el("button", { class: "btn icon", onclick: () => step(-1), "aria-label": "Back" }, icon("left")),
      el("button", { class: "btn", onclick: () => { const d = new Date(); CAL.focus = new Date(d.getFullYear(), d.getMonth(), d.getDate()); showCalendar(c); } }, "Today"),
      el("button", { class: "btn icon", onclick: () => step(1), "aria-label": "Next" }, icon("right"))] : null,
    el("div", { class: "grow" }),
    el("div", { class: "seg" }, [["month", "Month"], ["week", "Week"], ["day", "Day"], ["agenda", "List"]].map(([m, l]) => el("button", { class: mode === m ? "on" : "", style: { padding: "6px 11px" }, onclick: () => { CAL.mode = m; localStorage.setItem("cove.calmode", m); showCalendar(c); } }, l))),
    el("button", { class: "btn primary", onclick: () => {
      const d = new Date(Math.max(Date.now(), +CAL.focus)); d.setMinutes(0, 0, 0); d.setHours(d.getHours() + 1); // the next whole hour
      eventDialog(null, d, c);
    } }, icon("plus"), el("span", { class: "lbl", text: "Event" })));

  const toggle = k => { CAL.hidden.has(k.id) ? CAL.hidden.delete(k.id) : CAL.hidden.add(k.id); localStorage.setItem("cove.calhidden", JSON.stringify([...CAL.hidden])); showCalendar(c); };
  const side = el("div", { class: "cal-side" },
    el("h4", {}, el("span", { text: "Calendars" }), el("button", { class: "btn ghost icon", title: "New calendar or subscription", onclick: e => popMenu(e.currentTarget, [
      { label: "New calendar", icon: "calendar", action: () => calendarDialog(null, c) },
      { label: "Subscribe to a calendar…", icon: "share", action: () => subscribeDialog(null, c) }]) }, icon("plus"))),
    CAL.cals.map(k => el("div", { class: "calrow" + (CAL.hidden.has(k.id) ? " off" : ""), style: { "--c": k.color }, onclick: () => toggle(k) },
      el("div", { class: "dot" }), el("span", { text: k.name, title: k.shared ? "From " + k.who : k.name }),
      k.shared ? el("span", { class: "pill", style: { padding: "0 6px" }, text: k.who }) : null,
      k.subscription ? el("button", { class: "edit", title: "Subscription", onclick: e => { e.stopPropagation(); subscribeDialog(k.subscription, c); } }, icon("edit")) :
      k.virtual ? null : el("button", { class: "edit", title: k.shared ? "Shared with you" : "Edit, share, import", onclick: e => { e.stopPropagation(); calendarDialog(k, c); } }, icon(k.shared ? "more" : "edit")))),
    el("p", { class: "muted", style: { fontSize: "12.5px", marginTop: "16px" } }, "See these on your phone: ", el("a", { href: "#/settings", text: "Settings → Connect your devices" }), "."));

  let main;
  if (mode === "month") main = monthView(events, from, color, c);
  else if (mode === "week" || mode === "day") main = timeView(events, from, mode === "week" ? 7 : 1, color, c);
  else main = agendaView(events, from, color, c);
  c.replaceChildren(bar, el("div", { class: "cal" }, side, main));
  const body = c.querySelector(".tbody");
  if (body) body.scrollTop = Math.max(0, (Math.min(new Date().getHours(), 17) - 1.5) * HOUR);
}

function monthView(events, from, color, c) {
  const byDay = new Map();
  for (const o of events) {
    const s = evStart(o), e = evEnd(o);
    let d = new Date(s.getFullYear(), s.getMonth(), s.getDate());
    const last = o.allDay ? addDays(e, -1) : (e > s ? new Date(e - 1) : s);
    for (let n = 0; d <= last && n < 42; n++, d = addDays(d, 1)) {
      const k = ymd(d);
      if (!byDay.has(k)) byDay.set(k, []);
      byDay.get(k).push(o);
    }
  }
  const today = ymd(new Date());
  const weeks = [];
  for (let w = 0; w < 6; w++) {
    const days = [];
    for (let i = 0; i < 7; i++) {
      const d = addDays(from, w * 7 + i), k = ymd(d);
      const list = (byDay.get(k) || []).sort((a, b) => (b.allDay - a.allDay) || (evStart(a) - evStart(b)));
      days.push(el("div", { class: "day" + (d.getMonth() !== CAL.month.getMonth() ? " other" : "") + (k === today ? " today" : ""), onclick: () => { const t = new Date(d); t.setHours(9); eventDialog(null, t, c); } },
        el("div", { class: "num", text: d.getDate(), onclick: e => { e.stopPropagation(); CAL.focus = d; CAL.mode = "day"; showCalendar(c); } }),
        list.slice(0, 4).map(o => chip(o, color(o.cal), c)),
        list.length > 4 ? el("div", { class: "more", text: `+${list.length - 4} more`, onclick: e => { e.stopPropagation(); dayDialog(d, list, color, c); } }) : null));
    }
    weeks.push(el("div", { class: "week" }, days));
  }
  const dows = [...Array(7)].map((_, i) => addDays(from, i).toLocaleDateString([], { weekday: "short" }));
  return el("div", { class: "card month" }, el("div", { class: "dows" }, dows.map(d => el("div", { text: d }))), el("div", { class: "weeks" }, weeks));
}

function agendaView(events, from, color, c) {
  const groups = new Map();
  for (const o of events) {
    const k = ymd(evStart(o) < from ? from : evStart(o));
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k).push(o);
  }
  return groups.size ? el("div", { class: "card agenda" }, [...groups.entries()].sort().map(([k, list]) => {
    const d = parseYmd(k);
    return el("div", { class: "aday" }, el("div", { class: "d" }, d.toLocaleDateString([], { weekday: "long" }), el("small", { text: d.toLocaleDateString([], { month: "long", day: "numeric" }) })),
      el("div", {}, list.map(o => el("div", { class: "aev", style: { "--c": color(o.cal) }, onclick: () => eventDialog(o, null, c) }, el("div", { class: "dot" }),
        el("div", { class: "tm", text: o.allDay ? "All day" : timeLabel(evStart(o)) + " – " + timeLabel(evEnd(o)) }),
        el("div", {}, el("div", { text: o.title || "(no title)" }), o.invite ? el("span", { class: "pill", text: inviteLabel(o.invite) }) : o.location ? el("div", { class: "muted", style: { fontSize: "12.5px" }, text: o.location }) : null)))));
  })) : el("div", { class: "card empty" }, icon("calendar"), el("h3", { text: "Nothing in the next three months" }));
}

function inviteLabel(inv) {
  return { "NEEDS-ACTION": "Invitation from " + inv.organizer, ACCEPTED: "Going · " + inv.organizer, TENTATIVE: "Maybe · " + inv.organizer }[inv.status] || inv.organizer;
}

// timeView is the week (days = 7) or day view: all-day events on top, the rest placed by time, side by side when
// they overlap.
function timeView(events, from, days, color, c) {
  const cols = [...Array(days)].map((_, i) => addDays(from, i));
  const today = ymd(new Date());
  const head = el("div", { class: "thead", style: { gridTemplateColumns: `56px repeat(${days}, 1fr)` } }, el("div"),
    cols.map(d => el("div", { class: "tday" + (ymd(d) === today ? " today" : ""), onclick: () => { CAL.focus = d; CAL.mode = "day"; showCalendar(c); } },
      el("span", { text: d.toLocaleDateString([], { weekday: "short" }) }), el("b", { text: d.getDate() }))));
  const allday = el("div", { class: "tallday", style: { gridTemplateColumns: `56px repeat(${days}, 1fr)` } }, el("div", { class: "muted", text: "all day" }),
    cols.map(d => el("div", {}, events.filter(o => o.allDay && evStart(o) <= d && evEnd(o) > d).map(o => chip(o, color(o.cal), c)))));
  const grid = el("div", { class: "tgrid", style: { gridTemplateColumns: `56px repeat(${days}, 1fr)`, height: 24 * HOUR + "px" } });
  grid.append(el("div", { class: "hours" }, [...Array(24)].map((_, h) => el("div", { style: { height: HOUR + "px" }, text: h ? new Date(2000, 0, 1, h).toLocaleTimeString([], { hour: "numeric" }) : "" }))));
  cols.forEach((d, i) => {
    const col = el("div", { class: "tcol" + (ymd(d) === today ? " today" : "") });
    col.addEventListener("click", e => {
      if (e.target !== col) return;
      const mins = Math.floor((e.offsetY / HOUR) * 2) * 30;
      eventDialog(null, new Date(d.getFullYear(), d.getMonth(), d.getDate(), 0, mins), c);
    });
    const dayEnd = addDays(d, 1);
    const list = events.filter(o => !o.allDay && evStart(o) < dayEnd && evEnd(o) > d).sort((a, b) => evStart(a) - evStart(b));
    // lay overlapping events side by side
    const placed = [];
    for (const o of list) {
      const s = Math.max(evStart(o), d), e = Math.min(Math.max(evEnd(o), +evStart(o) + 15 * 60e3), dayEnd);
      let lane = 0;
      while (placed.some(p => p.lane === lane && p.s < e && p.e > s)) lane++;
      placed.push({ o, s, e, lane });
    }
    for (const p of placed) {
      const lanes = 1 + Math.max(...placed.filter(q => q.s < p.e && q.e > p.s).map(q => q.lane));
      col.append(timedEvent(p, d, lanes, color(p.o.cal), c, cols));
    }
    if (ymd(d) === today) {
      const now = new Date();
      col.append(el("div", { class: "now", style: { top: (now.getHours() + now.getMinutes() / 60) * HOUR + "px" } }));
    }
    grid.append(col);
  });
  return el("div", { class: "card tview" }, head, allday, el("div", { class: "tbody" }, grid));
}

function timedEvent(p, day, lanes, color, c, cols) {
  const o = p.o;
  const top = (p.s - day) / 3600e3 * HOUR, height = Math.max(18, (p.e - p.s) / 3600e3 * HOUR - 2);
  const box = el("div", { class: "tev" + (o.readOnly ? " ro" : "") + (o.invite && o.invite.status === "NEEDS-ACTION" ? " invite" : ""),
    style: { top: top + "px", height: height + "px", left: `calc(${100 * p.lane / lanes}% + 2px)`, width: `calc(${100 / lanes}% - 4px)`, "--c": color } },
  el("div", { class: "tt", text: o.title || "(no title)" }), el("div", { class: "tm", text: timeLabel(evStart(o)) + " – " + timeLabel(evEnd(o)) }),
  o.readOnly ? null : el("div", { class: "grip" }));
  // drag to move (a quarter hour at a time, across days in the week view), drag the bottom edge to resize
  box.addEventListener("pointerdown", e => {
    if (e.button !== 0) return;
    e.preventDefault();
    const resize = e.target.classList.contains("grip");
    const x0 = e.clientX, y0 = e.clientY, colW = box.parentElement.offsetWidth;
    let dMin = 0, dDay = 0, moved = false;
    box.setPointerCapture(e.pointerId);
    const move = ev => {
      if (o.readOnly) return;
      dMin = Math.round(((ev.clientY - y0) / HOUR) * 4) * 15;
      dDay = resize ? 0 : Math.max(-cols.indexOf(cols.find(x => ymd(x) === ymd(day))), Math.min(cols.length - 1 - cols.findIndex(x => ymd(x) === ymd(day)), Math.round((ev.clientX - x0) / colW)));
      moved = moved || Math.abs(ev.clientY - y0) > 4 || Math.abs(ev.clientX - x0) > 4;
      if (resize) box.style.height = Math.max(18, height + dMin / 60 * HOUR) + "px";
      else box.style.transform = `translate(${dDay * colW}px, ${dMin / 60 * HOUR}px)`;
      box.classList.toggle("dragging", moved);
    };
    const up = async () => {
      box.removeEventListener("pointermove", move);
      box.removeEventListener("pointerup", up);
      if (!moved) return eventDialog(o, null, c);
      if (o.readOnly || (!dMin && !dDay)) return showCalendar(c);
      let s = new Date(+evStart(o) + (resize ? 0 : dMin * 60e3)), en = new Date(+evEnd(o) + dMin * 60e3);
      if (!resize) { s = addDays(s, dDay); en = addDays(en, dDay); }
      if (en <= s) en = new Date(+s + 15 * 60e3);
      let scope = "all";
      if (o.repeat && o.instance) { scope = await whichTimes("Move"); if (!scope) return showCalendar(c); }
      await moveEvent(o, s, en, scope).catch(fail);
      showCalendar(c);
    };
    box.addEventListener("pointermove", move);
    box.addEventListener("pointerup", up);
  });
  return box;
}

// moveEvent gives an event new times; for a repeating one, the whole series shifts by as much.
async function moveEvent(o, s, e, scope) {
  let start = s, end = e;
  if (scope === "this") {
    const [k, name] = o.id.split("/");
    return api("PUT", `/api/events/${encodeURIComponent(k)}/${encodeURIComponent(name)}`,
      { cal: o.cal, title: o.title, location: o.location, notes: o.notes, allDay: false, start: s.toISOString(), end: e.toISOString(), tz, scope: "this", instance: o.instance });
  }
  if (o.repeat && o.seriesStart) {
    const shift = s - evStart(o), ss = new Date(o.seriesStart), se = new Date(o.seriesEnd);
    start = new Date(+ss + shift); end = new Date(+se + shift + ((e - s) - (evEnd(o) - evStart(o))));
  }
  const [k, name] = o.id.split("/");
  await api("PUT", `/api/events/${encodeURIComponent(k)}/${encodeURIComponent(name)}`,
    { cal: o.cal, title: o.title, location: o.location, notes: o.notes, allDay: false, start: start.toISOString(), end: end.toISOString(), repeat: o.repeat, tz });
}

function chip(o, color, c) {
  const s = evStart(o);
  return el("div", { class: "ev " + (o.allDay ? "allday" : "timed") + (o.invite && o.invite.status === "NEEDS-ACTION" ? " invite" : ""), style: { "--c": color }, title: o.title,
    onclick: e => { e.stopPropagation(); eventDialog(o, null, c); } },
  o.allDay ? null : el("span", { class: "tm", text: timeLabel(s).replace(":00", "").replace(" ", "").toLowerCase() }), el("span", { class: "tt", text: o.title || "(no title)" }));
}

function dayDialog(d, list, color, c) {
  const dlg = dialog({ title: d.toLocaleDateString([], { weekday: "long", month: "long", day: "numeric" }),
    body: el("div", { class: "agenda" }, list.map(o => el("div", { class: "aev", style: { "--c": color(o.cal) }, onclick: () => { dlg.close(); eventDialog(o, null, c); } }, el("div", { class: "dot" }),
      el("div", { class: "tm", text: o.allDay ? "All day" : timeLabel(evStart(o)) }), el("div", { text: o.title || "(no title)" })))),
    buttons: [{ label: "Close" }] });
}

const REMINDERS = [[-1, "No reminder"], [0, "At the start"], [5, "5 minutes before"], [10, "10 minutes before"], [15, "15 minutes before"], [30, "30 minutes before"], [60, "1 hour before"], [120, "2 hours before"], [1440, "1 day before"], [2880, "2 days before"]];

async function eventDialog(o, at, c) {
  const editing = !!o;
  if (editing && o.invite) return inviteDialog(o, c);
  const s = editing ? evStart(o) : new Date(at); // a repeating event shows this time; "every time" shifts the series as much
  if (!editing && s.getHours() === 0 && s.getMinutes() === 0) s.setHours(9);
  const e = editing ? evEnd(o) : new Date(s.getTime() + 3600e3);
  const title = el("input", { class: "input", value: editing ? o.title : "", placeholder: "Title" });
  const writable = CAL.cals.filter(k => k.edit && !k.virtual);
  const cal = el("select", { class: "input" }, writable.concat(editing && !writable.some(k => k.id === o.cal) ? CAL.cals.filter(k => k.id === o.cal) : [])
    .map(k => el("option", { value: k.id, text: k.shared ? `${k.name} (${k.who})` : k.name, selected: editing ? k.id === o.cal : false })));
  const allDay = el("input", { type: "checkbox", checked: editing && o.allDay });
  const sd = el("input", { class: "input", type: "date", value: ymd(s) });
  const st = el("input", { class: "input", type: "time", value: hm(s), step: 300 });
  const ed = el("input", { class: "input", type: "date", value: ymd(editing && o.allDay ? addDays(e, -1) : e) });
  const et = el("input", { class: "input", type: "time", value: hm(e), step: 300 });
  const repeats = [["", "Doesn't repeat"], ["DAILY", "Every day"], ["WEEKLY", "Every week"], ["MONTHLY", "Every month"], ["YEARLY", "Every year"]];
  if (editing && o.repeat === "custom") repeats.push(["custom", "Custom (set on another device)"]);
  const repeat = el("select", { class: "input" }, repeats.map(([v, l]) => el("option", { value: v, text: l, selected: editing && o.repeat === v })));
  const current = editing ? ((o.reminders || [])[0] ?? -1) : +(localStorage.getItem("cove.remind") ?? -1);
  const remind = el("select", { class: "input" }, REMINDERS.concat(REMINDERS.some(([m]) => m === current) ? [] : [[current, current + " minutes before"]])
    .map(([m, l]) => el("option", { value: m, text: l, selected: m === current })));
  const loc = el("input", { class: "input", value: editing ? o.location : "", placeholder: "Add a place" });
  const notes = el("textarea", { class: "input", placeholder: "Notes" }, editing ? o.notes : "");
  const sync = () => [st, et].forEach(t => t.classList.toggle("hide", allDay.checked));
  allDay.onchange = sync;
  sd.onchange = () => { if (ed.value < sd.value) ed.value = sd.value; };
  sync();
  // inviting people in the household
  const guestBox = el("div", { class: "guests" });
  let invited = new Set(), people = [];
  let busy = {};
  const checkBusy = async () => {
    if (!people.length || allDay.checked) return;
    const a = new Date(`${sd.value}T${st.value || "09:00"}`), b = new Date(`${ed.value}T${et.value || "10:00"}`);
    try {
      const fb = await api("GET", `/api/freebusy?users=${people.map(p => encodeURIComponent(p.name)).join(",")}&from=${a.toISOString()}&to=${b.toISOString()}`);
      busy = Object.fromEntries(Object.entries(fb).map(([u, spans]) => [u, spans.some(x => !(editing && x.start === o.start && u === S.me.user.name))]));
      drawGuests(lastGuests);
    } catch (_) { /* no free/busy */ }
  };
  let lastGuests = [];
  [sd, st, ed, et].forEach(x => x.addEventListener("change", checkBusy));
  const drawGuests = guests => {
    lastGuests = guests;
    const status = Object.fromEntries((guests || []).map(g => [g.user, g.status]));
    guestBox.replaceChildren(...(people.length ? people.map(p => el("label", { class: "check guest" },
      el("input", { type: "checkbox", checked: invited.has(p.name), onchange: ev => { ev.target.checked ? invited.add(p.name) : invited.delete(p.name); } }),
      p.display || p.name, status[p.name] ? el("span", { class: "pill", text: { "NEEDS-ACTION": "not answered", ACCEPTED: "going", TENTATIVE: "maybe", DECLINED: "not going" }[status[p.name]] }) : null,
      busy[p.name] ? el("span", { class: "pill late", text: "busy then" }) : busy[p.name] === false ? el("span", { class: "pill", text: "free" }) : null))
      : [el("span", { class: "muted", style: { fontSize: "13px" }, text: "Nobody else has signed in to Cove yet." })]));
  };
  const readOnly = editing && o.readOnly;
  const body = el("div", {},
    el("div", { class: "field" }, title),
    el("label", { class: "check" }, allDay, "All day"),
    el("div", { class: "row" }, el("div", { class: "field" }, el("label", { text: "Starts" }), el("div", { class: "row" }, sd, st))),
    el("div", { class: "row" }, el("div", { class: "field" }, el("label", { text: "Ends" }), el("div", { class: "row" }, ed, et))),
    el("div", { class: "row" }, el("div", { class: "field" }, el("label", { text: "Repeat" }), repeat), el("div", { class: "field" }, el("label", { text: "Reminder" }), remind)),
    el("div", { class: "row" }, el("div", { class: "field" }, el("label", { text: "Calendar" }), cal)),
    el("div", { class: "field" }, el("label", { text: "Place" }), loc),
    el("div", { class: "field" }, el("label", { text: "Invite" }), guestBox),
    el("div", { class: "field" }, el("label", { text: "Notes" }), notes),
    editing && o.repeat ? el("p", { class: "muted", style: { fontSize: "12.5px" }, text: "Changes apply to every time it repeats." }) : null);
  if (readOnly) {
    body.querySelectorAll("input, select, textarea").forEach(x => { x.disabled = true; });
    guestBox.parentElement.remove();
    body.prepend(el("p", { class: "muted", style: { marginTop: 0 }, text: o.cal === "~birthdays" ? "From your contacts." : "This calendar is shared with you to view." }));
    dialog({ title: o.title || "Event", body, buttons: [{ label: "Close", primary: true }] });
    return;
  }
  const save = async () => {
    const input = { cal: cal.value, title: title.value, location: loc.value, notes: notes.value, allDay: allDay.checked, repeat: repeat.value, tz,
      reminder: +remind.value, invite: [...invited] };
    localStorage.setItem("cove.remind", remind.value);
    if (allDay.checked) {
      input.start = sd.value;
      input.end = ymd(addDays(parseYmd(ed.value < sd.value ? sd.value : ed.value), 1));
    } else {
      const a = new Date(`${sd.value}T${st.value || "09:00"}`), b = new Date(`${ed.value}T${et.value || "10:00"}`);
      input.start = a.toISOString();
      input.end = (b > a ? b : new Date(a.getTime() + 3600e3)).toISOString();
    }
    if (editing && o.repeat && o.instance) {
      const scope = await whichTimes("Change");
      if (!scope) return false;
      if (scope === "this") { input.scope = "this"; input.instance = o.instance; }
      else { // shift the whole series by how much this time moved
        const shift = (input.allDay ? parseYmd(input.start) : new Date(input.start)) - evStart(o);
        const len = (input.allDay ? parseYmd(input.end) : new Date(input.end)) - (input.allDay ? parseYmd(input.start) : new Date(input.start));
        const ss = new Date(+(o.allDay ? parseYmd(o.seriesStart) : new Date(o.seriesStart)) + shift);
        input.start = input.allDay ? ymd(ss) : ss.toISOString();
        input.end = input.allDay ? ymd(new Date(+ss + len)) : new Date(+ss + len).toISOString();
      }
    }
    if (editing) {
      const [k, name] = o.id.split("/");
      await api("PUT", `/api/events/${encodeURIComponent(k)}/${encodeURIComponent(name)}`, input);
    } else await api("POST", "/api/events", input);
    showCalendar(c);
  };
  dialog({ title: editing ? "Edit event" : "New event", body, wide: true, buttons: [
    editing ? { label: "Delete", danger: true, action: async () => {
      const [k, name] = o.id.split("/");
      let q = "";
      if (o.repeat && o.instance) {
        const scope = await whichTimes("Delete");
        if (!scope) return false;
        if (scope === "this") q = "?scope=this&instance=" + encodeURIComponent(o.instance);
      } else if (!(await confirmBox("Delete this event?", "It goes to the trash for 30 days.", "Delete", true))) return false;
      try { await api("DELETE", `/api/events/${encodeURIComponent(k)}/${encodeURIComponent(name)}${q}`); showCalendar(c); } catch (err) { fail(err); }
    } } : null,
    "grow", { label: "Cancel" }, { label: "Save", primary: true, action: save }].filter(Boolean) });
  try {
    people = await api("GET", "/api/users");
    let guests = [];
    if (editing) { const [k, name] = o.id.split("/"); guests = await api("GET", `/api/events/${encodeURIComponent(k)}/${encodeURIComponent(name)}/guests`); }
    invited = new Set(guests.map(g => g.user));
    drawGuests(guests);
    checkBusy();
  } catch (_) { drawGuests([]); }
}

// whichTimes asks whether a change to a repeating event is for this time or every time ("this", "all", or null).
function whichTimes(verb) {
  return new Promise(resolve => {
    let done = false;
    const pick = v => () => { done = true; resolve(v); };
    dialog({ title: verb + " a repeating event", body: el("p", { class: "muted", text: `${verb} only this time, or every time it repeats?` }),
      buttons: [{ label: "Cancel" }, "grow", { label: "Every time", action: pick("all") }, { label: "Only this time", primary: true, action: pick("this") }],
      onClose: () => { if (!done) resolve(null); } });
  });
}

function inviteDialog(o, c) {
  const s = evStart(o), e = evEnd(o);
  const when = o.allDay ? s.toLocaleDateString([], { dateStyle: "full" }) : s.toLocaleString([], { dateStyle: "full", timeStyle: "short" }) + " – " + timeLabel(e);
  const answer = status => async () => {
    try { await api("POST", "/api/invites/" + o.invite.id, { status }); toast({ ACCEPTED: "You're going", TENTATIVE: "Maybe", DECLINED: "Declined" }[status]); showCalendar(c); } catch (err) { fail(err); }
  };
  dialog({ title: o.title || "Invitation", body: el("div", {},
    el("p", { class: "muted", style: { marginTop: 0 }, text: `${o.invite.organizer} invited you.` }),
    el("div", { class: "cline" }, icon("clock"), el("div", { text: when })),
    o.location ? el("div", { class: "cline" }, icon("place"), el("div", { text: o.location })) : null,
    o.notes ? el("div", { class: "cline" }, icon("text"), el("div", { text: o.notes, style: { whiteSpace: "pre-line" } })) : null,
    el("p", { class: "muted", style: { fontSize: "12.5px" }, text: "Your answer: " + ({ "NEEDS-ACTION": "not yet", ACCEPTED: "going", TENTATIVE: "maybe" }[o.invite.status] || o.invite.status) })),
  buttons: [{ label: "Can't go", danger: true, action: answer("DECLINED") }, "grow", { label: "Maybe", action: answer("TENTATIVE") }, { label: "Going", primary: true, action: answer("ACCEPTED") }] });
}

function importInto(k, what, after) {
  const input = el("input", { type: "file", accept: ".ics,text/calendar", class: "hide", onchange: async e => {
    const f = e.target.files[0];
    if (!f) return;
    try {
      const r = await xhrSend("POST", `/api/calendars/${encodeURIComponent(k.id)}/import`, f);
      toast(`${r.added} added, ${r.updated} updated${r.unchanged ? `, ${r.unchanged} already there` : ""}${r.skipped ? `, ${r.skipped} skipped` : ""}`);
      document.querySelectorAll(".veil").forEach(v => v.remove());
      after();
    } catch (err) { fail(err); }
  } });
  document.body.append(input);
  input.click();
}

function calendarDialog(k, c) { listDialog(k, "events", () => showCalendar(c)); }

// listDialog makes or edits a calendar or task list (kind events or tasks), and shares it with the household.
function listDialog(k, kind, after) {
  const what = kind === "tasks" ? "list" : "calendar";
  const transfer = k ? el("div", { class: "row", style: { gap: "8px", margin: "4px 0 6px", flexWrap: "wrap" } },
    k.edit ? el("button", { class: "btn", type: "button", onclick: () => importInto(k, what, after) }, icon("upload"), "Import .ics") : null,
    el("a", { class: "btn", href: `/api/calendars/${encodeURIComponent(k.id)}/export` }, icon("download"), "Export .ics")) : null;
  if (k && k.shared) {
    dialog({ title: k.name, body: el("div", {}, el("p", { class: "muted", text: `${k.who} shares this ${what} with you${k.edit ? ", and you can change it" : " to view"}.` }), transfer),
      buttons: [{ label: `Remove from my ${what}s`, danger: true, action: async () => {
        try { await api("DELETE", "/api/calendars/" + encodeURIComponent(k.id)); after(); } catch (e) { fail(e); }
      } }, "grow", { label: "Close", primary: true }] });
    return;
  }
  let color = k ? k.color : COLORS[Math.floor(Math.random() * COLORS.length)];
  const name = el("input", { class: "input", value: k ? k.name : "", placeholder: kind === "tasks" ? "Groceries, Chores…" : "Family, Work…" });
  const sw = el("div", { class: "swatches" });
  const draw = () => sw.replaceChildren(...COLORS.map(col => el("button", { type: "button", class: col === color ? "on" : "", style: { background: col }, "aria-label": col, onclick: () => { color = col; draw(); } })));
  draw();
  const body = el("div", {}, el("div", { class: "field" }, el("label", { text: "Name" }), name), el("div", { class: "label", style: { marginBottom: "6px" }, text: "Color" }), sw);
  if (k) body.append(el("div", { class: "label", style: { margin: "4px 0 2px" }, text: "Move in or out" }),
    el("p", { class: "muted", style: { margin: "0 0 6px", fontSize: "13px" }, text: `Import an .ics export from iCloud, Google or Outlook (importing again doesn't make copies), or save this ${what} as one.` }), transfer);
  if (k) {
    const people = el("div", { class: "items-list", style: { margin: "0 -14px 8px" } });
    const who = el("select", { class: "input" });
    const canEdit = el("input", { type: "checkbox", checked: true });
    const nobody = el("p", { class: "muted hide", style: { margin: 0, fontSize: "13px" }, text: "Nobody else has signed in to Cove yet." });
    const addRow = el("div", { class: "row", style: { alignItems: "center" } }, who, el("label", { class: "check", style: { margin: 0, whiteSpace: "nowrap" } }, canEdit, "Can edit"),
      el("button", { class: "btn", type: "button", onclick: async () => {
        if (!who.value) return;
        try { await api("POST", `/api/calendars/${k.id}/shares`, { user: who.value, edit: canEdit.checked }); load(); } catch (e) { fail(e); }
      } }, "Share"));
    async function load() {
      try {
        const [users, shares] = await Promise.all([api("GET", "/api/users"), api("GET", `/api/calendars/${k.id}/shares`)]);
        const shared = new Set(shares.map(x => x.user));
        const others = users.filter(u => !shared.has(u.name));
        who.replaceChildren(...(others.length > 1 ? [el("option", { value: "*", text: "Everyone in the household" })] : []), ...others.map(u => el("option", { value: u.name, text: u.display || u.name })));
        addRow.classList.toggle("hide", !others.length);
        nobody.classList.toggle("hide", users.length > 0);
        people.replaceChildren(...shares.map(x => el("div", {}, el("div", { class: "avatar", text: x.who.slice(0, 1).toUpperCase() }), el("div", { class: "grow", text: x.who }),
          el("select", { class: "input", style: { width: "auto" }, onchange: async e => {
            if (e.target.value === "remove") await api("DELETE", "/api/calendar-shares/" + x.id).catch(fail);
            else await api("POST", `/api/calendars/${k.id}/shares`, { user: x.user, edit: e.target.value === "edit" }).catch(fail);
            load();
          } }, el("option", { value: "edit", text: "Can edit", selected: x.edit }), el("option", { value: "view", text: "Can view", selected: !x.edit }), el("option", { value: "remove", text: "Remove" })))));
      } catch (e) { fail(e); }
    }
    body.append(groupShareBox("calendar", k.id));
    body.append(el("div", { class: "label", style: { margin: "6px 0 6px" }, text: "Shared with" }),
      el("p", { class: "muted", style: { margin: "0 0 8px", fontSize: "13px" }, text: `They see it in Cove and on their phones, beside their own ${what}s.` }), people, addRow, nobody);
    load();
  }
  dialog({ title: k ? `Edit ${what}` : `New ${what}`, body,
    buttons: [
      k ? { label: "Delete", danger: true, action: async () => {
        if (!(await confirmBox("Delete “" + k.name + "”?", `Everything in it is deleted too, on all devices, for everyone it's shared with.`, "Delete", true))) return false;
        try { await api("DELETE", "/api/calendars/" + k.id); after(); } catch (e) { fail(e); }
      } } : null,
      "grow", { label: "Cancel" },
      { label: k ? "Save" : "Create", primary: true, action: async () => {
        if (k) await api("PUT", "/api/calendars/" + k.id, { name: name.value, color });
        else await api("POST", "/api/calendars", { name: name.value, color, kind });
        after();
      } }].filter(Boolean) });
}

// --- tasks: lists on the left (yours and shared), the list's tasks on the right; Reminders on phones sync them

const TASKS = { list: localStorage.getItem("cove.tasklist") || "", showDone: false, lists: [] };

async function showTasks(c) {
  try { TASKS.lists = await api("GET", "/api/calendars?kind=tasks"); } catch (e) { return fail(e); }
  const me = S.me.user.name;
  const mineList = { id: "~mine", name: "Assigned to me", color: "#8b5cf6", edit: false, virtual: true };
  if (TASKS.list !== "~mine" && !TASKS.lists.some(l => l.id === TASKS.list)) TASKS.list = TASKS.lists[0] ? TASKS.lists[0].id : "";
  const list = TASKS.list === "~mine" ? mineList : TASKS.lists.find(l => l.id === TASKS.list);
  let tasks = [];
  try {
    tasks = TASKS.list === "~mine" ? (await api("GET", "/api/tasks?tz=" + encodeURIComponent(tz))).filter(t => t.assignee === me)
      : await api("GET", "/api/tasks?list=" + encodeURIComponent(TASKS.list) + "&tz=" + encodeURIComponent(tz));
  } catch (e) { fail(e); }
  // steps go under their task
  const steps = {};
  for (const t of tasks) if (t.parent && tasks.some(p => p.uid === t.parent)) (steps[t.parent] = steps[t.parent] || []).push(t);
  tasks = tasks.filter(t => !(t.parent && tasks.some(p => p.uid === t.parent)));
  const todo = tasks.filter(t => !t.done), done = tasks.filter(t => t.done);
  const pick = id => { TASKS.list = id; localStorage.setItem("cove.tasklist", id); showTasks(c); };
  const side = el("div", { class: "cal-side tasks-side" },
    el("h4", {}, el("span", { text: "Lists" }), el("button", { class: "btn ghost icon", title: "New list", onclick: () => listDialog(null, "tasks", () => showTasks(c)) }, icon("plus"))),
    el("div", { class: "calrow" + (TASKS.list === "~mine" ? " on" : ""), style: { "--c": mineList.color }, onclick: () => pick("~mine") }, el("div", { class: "dot" }), el("span", { text: mineList.name })),
    TASKS.lists.map(l => el("div", { class: "calrow" + (l.id === TASKS.list ? " on" : ""), style: { "--c": l.color }, onclick: () => pick(l.id) },
      el("div", { class: "dot" }), el("span", { text: l.name }), l.shared ? el("span", { class: "pill", style: { padding: "0 6px" }, text: l.who }) : null,
      el("button", { class: "edit", title: "Edit and share", onclick: e => { e.stopPropagation(); listDialog(l, "tasks", () => showTasks(c)); } }, icon(l.shared ? "more" : "edit")))));
  const picker = el("select", { class: "input tasks-picker", onchange: e => pick(e.target.value) }, [mineList, ...TASKS.lists].map(l => el("option", { value: l.id, text: l.shared ? `${l.name} (${l.who})` : l.name, selected: l.id === TASKS.list })));
  const canEdit = list && list.edit;
  TASKS.steps = steps;
  const add = el("input", { class: "input", placeholder: "Add a task, then press Enter", disabled: !canEdit,
    onkeydown: async e => {
      if (e.key !== "Enter" || !add.value.trim()) return;
      const title = add.value.trim();
      add.value = "";
      try { await api("POST", "/api/tasks", { list: TASKS.list, title, tz }); showTasks(c).then(() => { const n = c.querySelector(".add-task input"); n && n.focus(); }); } catch (err) { fail(err); }
    } });
  const row = (t, depth = 0) => {
    const due = t.due ? (t.due.length === 10 ? parseYmd(t.due) : new Date(t.due)) : null;
    const today = new Date(); today.setHours(0, 0, 0, 0);
    const late = due && !t.done && due < today;
    const dueText = due ? (ymd(due) === ymd(today) ? "Today" : ymd(due) === ymd(addDays(today, 1)) ? "Tomorrow" : due.toLocaleDateString([], { month: "short", day: "numeric" }))
      + (t.due.length > 10 ? " " + timeLabel(due) : "") : "";
    const kids = (steps[t.uid] || []).filter(k => TASKS.showDone || !k.done || t.done);
    const who = t.assignee ? (t.assignee === me ? "Me" : t.assignee) : "";
    return [el("div", { class: "task" + (t.done ? " done" : "") + (depth ? " step" : ""), onclick: () => taskDialog(t, c) },
      el("input", { type: "checkbox", class: "cb round", checked: t.done, disabled: t.readOnly, "aria-label": "Done", onclick: async e => {
        e.stopPropagation();
        const [l, n] = t.id.split("/");
        try { await api("PUT", `/api/tasks/${encodeURIComponent(l)}/${encodeURIComponent(n)}`, { ...t, due: t.due, done: e.target.checked, tz }); showTasks(c); } catch (err) { fail(err); }
      } }),
      el("div", { class: "grow" }, el("div", { class: "tt", text: t.title || "(no title)" }),
        t.notes ? el("div", { class: "muted tn", text: t.notes }) : null),
      t.repeat ? el("span", { class: "pill", title: "Repeats" }, icon("refresh")) : null,
      who ? el("span", { class: "pill" }, icon("user"), who) : null,
      steps[t.uid] ? el("span", { class: "pill", text: `${steps[t.uid].filter(k => k.done).length}/${steps[t.uid].length}` }) : null,
      dueText ? el("span", { class: "pill" + (late ? " late" : "") }, icon("clock"), dueText) : null), ...kids.map(k => row(k, depth + 1))];
  };
  const main = el("div", {},
    el("div", { class: "bar" }, el("div", { class: "page-title", style: { margin: 0 }, text: list ? list.name : "Tasks" }), picker,
      list && list.shared ? el("span", { class: "pill" }, icon(list.edit ? "edit" : "lock"), (list.edit ? "Shared by " : "View only · ") + list.who) : null),
    el("div", { class: "card add-task" }, icon("plus"), add),
    el("div", { class: "card items-list tasks" }, todo.length ? todo.map(row) : [el("div", { class: "muted", text: canEdit ? "Nothing to do. Add something above." : "Nothing to do." })]),
    done.length ? el("button", { class: "btn ghost", style: { marginTop: "10px" }, onclick: () => { TASKS.showDone = !TASKS.showDone; showTasks(c); } }, icon(TASKS.showDone ? "minus" : "plus"), `${TASKS.showDone ? "Hide" : "Show"} ${done.length} done`) : null,
    TASKS.showDone && done.length ? el("div", { class: "card items-list tasks", style: { marginTop: "8px" } }, done.map(row)) : null);
  c.replaceChildren(el("div", { class: "cal" }, side, main));
}

function taskDialog(t, c) {
  const title = el("input", { class: "input", value: t.title });
  const hasTime = t.due && t.due.length > 10;
  const d = t.due ? (t.due.length === 10 ? parseYmd(t.due) : new Date(t.due)) : null;
  const dueDate = el("input", { class: "input", type: "date", value: d ? ymd(d) : "" });
  const dueTime = el("input", { class: "input", type: "time", value: hasTime ? hm(d) : "", step: 300 });
  const listSel = el("select", { class: "input" }, TASKS.lists.filter(l => l.edit || l.id === t.list).map(l => el("option", { value: l.id, text: l.shared ? `${l.name} (${l.who})` : l.name, selected: l.id === t.list })));
  const prio = el("select", { class: "input" }, [["0", "None"], ["1", "High"], ["5", "Medium"], ["9", "Low"]].map(([v, l]) => el("option", { value: v, text: l, selected: String(t.priority || 0) === v || (v === "1" && t.priority > 0 && t.priority < 5) || (v === "9" && t.priority > 5) })));
  const repeats = [["", "Doesn't repeat"], ["DAILY", "Every day"], ["WEEKLY", "Every week"], ["MONTHLY", "Every month"], ["YEARLY", "Every year"]];
  if (t.repeat === "custom") repeats.push(["custom", "Custom (set on another device)"]);
  const repeat = el("select", { class: "input" }, repeats.map(([v, l]) => el("option", { value: v, text: l, selected: (t.repeat || "") === v })));
  const assignee = el("select", { class: "input" }, el("option", { value: "", text: "Nobody" }));
  api("GET", `/api/calendars/${encodeURIComponent(t.list)}/members`).then(ms => {
    ms.forEach(m => assignee.append(el("option", { value: m.name, text: m.name === S.me.user.name ? "Me" : (m.display || m.name), selected: m.name === t.assignee })));
    if (t.assignee && !ms.some(m => m.name === t.assignee)) assignee.append(el("option", { value: t.assignee, text: t.assignee, selected: true }));
  }).catch(() => {});
  const notes = el("textarea", { class: "input", placeholder: "Notes" }, t.notes);
  const stepsBox = el("div", { class: "steps" });
  const drawSteps = () => stepsBox.replaceChildren(...((TASKS.steps || {})[t.uid] || []).map(k => el("label", { class: "check", style: { margin: "0 0 4px" } },
    el("input", { type: "checkbox", checked: k.done, onchange: async e => {
      const [kl, kn] = k.id.split("/");
      await api("PUT", `/api/tasks/${encodeURIComponent(kl)}/${encodeURIComponent(kn)}`, { ...k, done: e.target.checked, tz }).catch(fail);
      k.done = e.target.checked;
    } }), el("span", { text: k.title, style: { textDecoration: k.done ? "line-through" : "" } }))));
  drawSteps();
  const body = el("div", {}, el("div", { class: "field" }, title),
    el("div", { class: "row" }, el("div", { class: "field" }, el("label", { text: "Due" }), el("div", { class: "row" }, dueDate, dueTime))),
    el("div", { class: "row" }, el("div", { class: "field" }, el("label", { text: "Repeat" }), repeat), el("div", { class: "field" }, el("label", { text: "For" }), assignee)),
    el("div", { class: "row" }, el("div", { class: "field" }, el("label", { text: "List" }), listSel), el("div", { class: "field" }, el("label", { text: "Priority" }), prio)),
    t.parent ? null : el("div", { class: "field" }, el("label", { text: "Steps" }), stepsBox,
      t.readOnly ? null : el("button", { class: "btn ghost", type: "button", style: { alignSelf: "flex-start" }, onclick: async () => {
        const st = await ask("Add a step", "Step", "", "Add");
        if (!st) return;
        try { await api("POST", "/api/tasks", { list: t.list, title: st, parent: t.uid, tz }); await showTasks(c); drawSteps(); } catch (e) { fail(e); }
      } }, icon("plus"), "Add a step")),
    el("div", { class: "field" }, el("label", { text: "Notes" }), notes),
    t.repeat ? el("p", { class: "muted", style: { fontSize: "12.5px" }, text: "Ticking it off moves it to the next time it's due." }) : null);
  const [l, n] = t.id.split("/");
  const url = `/api/tasks/${encodeURIComponent(l)}/${encodeURIComponent(n)}`;
  if (t.readOnly) {
    body.querySelectorAll("input, select, textarea").forEach(x => { x.disabled = true; });
    dialog({ title: t.title, body, buttons: [{ label: "Close", primary: true }] });
    return;
  }
  dialog({ title: t.parent ? "Step" : "Task", wide: true, body, buttons: [
    { label: "Delete", danger: true, action: async () => { try { await api("DELETE", url); showTasks(c); } catch (e) { fail(e); } } },
    "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => {
      let due = "";
      if (dueDate.value) due = dueTime.value ? new Date(`${dueDate.value}T${dueTime.value}`).toISOString() : dueDate.value;
      await api("PUT", url, { list: listSel.value, title: title.value, notes: notes.value, due, done: t.done, priority: +prio.value, tz, repeat: repeat.value, assignee: assignee.value });
      showTasks(c);
    } }] });
}

// --- contacts: a searchable list, and the selected contact's card (editable)

const CONTACTS = { all: [], sel: null, q: "", group: "" };

async function showContacts(c) {
  try { CONTACTS.all = await api("GET", "/api/contacts"); } catch (e) { return fail(e); }
  drawContacts(c);
}

function drawContacts(c) {
  const q = CONTACTS.q.toLowerCase();
  const groups = [...new Set(CONTACTS.all.flatMap(p => p.groups || []))].sort();
  const shown = CONTACTS.all.filter(p => (!CONTACTS.group || (p.groups || []).includes(CONTACTS.group)) &&
    (!q || [p.name, p.org, ...p.emails.map(e => e.value), ...p.phones.map(x => x.value)].join(" ").toLowerCase().includes(q)));
  const sel = CONTACTS.all.find(p => p.id === CONTACTS.sel);
  const search = el("input", { class: "input", type: "search", placeholder: "Search contacts", value: CONTACTS.q,
    oninput: e => { CONTACTS.q = e.target.value; const pos = e.target.selectionStart; drawContacts(c); const n = c.querySelector(".contacts-list input"); n.focus(); n.setSelectionRange(pos, pos); } });
  let letter = "";
  const items = [];
  for (const p of shown) {
    const L = (p.given || p.name || "#").slice(0, 1).toUpperCase();
    if (L !== letter) { letter = L; items.push(el("div", { class: "letter", text: L })); }
    items.push(el("div", { class: "person" + (p.id === CONTACTS.sel ? " on" : ""), onclick: () => { CONTACTS.sel = p.id; drawContacts(c); } },
      avatarOf(p, 34), el("div", { class: "grow" }, el("div", { text: p.name }), p.org ? el("div", { class: "muted", style: { fontSize: "12px" }, text: p.org }) : null)));
  }
  const list = el("div", { class: "card contacts-list" + (sel ? " has-sel" : "") },
    el("div", { class: "row", style: { padding: "10px", gap: "8px", alignItems: "center" } }, search,
      el("button", { class: "btn primary icon", title: "New contact", onclick: () => contactDialog(null, c) }, icon("plus")),
      el("button", { class: "btn ghost icon", title: "Import or export", onclick: e => popMenu(e.currentTarget, [
        { label: "Import .vcf (iCloud, Google…)", icon: "upload", action: () => {
          const input = el("input", { type: "file", accept: ".vcf,text/vcard", class: "hide", onchange: async ev => {
            const f = ev.target.files[0];
            if (!f) return;
            try { const r = await xhrSend("POST", "/api/contacts/import", f); toast(`${r.added} added, ${r.updated} updated${r.unchanged ? `, ${r.unchanged} already there` : ""}`); showContacts(c); } catch (err) { fail(err); }
          } });
          document.body.append(input); input.click();
        } },
        { label: "Export all as .vcf", icon: "download", action: () => { location.href = "/api/contacts/export"; } },
        { label: "Find duplicates", icon: "search", action: () => duplicateContacts(c) }]) }, icon("more"))),
    groups.length ? el("div", { class: "chips" }, ["", ...groups].map(g => el("button", { class: "chip" + (CONTACTS.group === g ? " on" : ""), onclick: () => { CONTACTS.group = g; drawContacts(c); } }, g || "Everyone"))) : null,
    el("div", { class: "people" }, items.length ? items : [el("div", { class: "muted", style: { padding: "20px", textAlign: "center" }, text: CONTACTS.all.length ? "No one matches." : "No contacts yet. Add one, or sync from a phone." })]));
  const card = sel ? contactCard(sel, c) : el("div", { class: "card empty contact-card" }, icon("inbox"), el("h3", { text: `${CONTACTS.all.length} contact${CONTACTS.all.length === 1 ? "" : "s"}` }),
    el("p", { text: "They sync with your phone's contacts: see Settings → Connect your devices." }));
  c.replaceChildren(el("div", { class: "contacts" + (sel ? " showing" : "") }, list, card));
}

function avatarOf(p, size) {
  if (p.photo) return el("img", { class: "avatar", src: p.photo, alt: "", style: { width: size + "px", height: size + "px", objectFit: "cover" } });
  const initials = ((p.given || p.name || "?").slice(0, 1) + (p.family || "").slice(0, 1)).toUpperCase();
  return el("div", { class: "avatar", text: initials, style: { width: size + "px", height: size + "px", fontSize: Math.round(size / 2.6) + "px" } });
}

function contactCard(p, c) {
  const line = (ic, label, value, href) => el("div", { class: "cline" }, icon(ic), el("div", { class: "grow" },
    el("div", { class: "muted", style: { fontSize: "12px" }, text: label }), href ? el("a", { href, text: value }) : el("div", { text: value, style: { whiteSpace: "pre-line" } })));
  return el("div", { class: "card contact-card" },
    el("div", { class: "row", style: { alignItems: "center", marginBottom: "18px" } },
      el("button", { class: "btn ghost icon back", onclick: () => { CONTACTS.sel = null; drawContacts(c); }, "aria-label": "Back" }, icon("left")),
      avatarOf(p, 64), el("div", { class: "grow" }, el("h2", { style: { margin: 0, fontSize: "20px" }, text: p.name }),
        p.title || p.org ? el("div", { class: "muted", text: [p.title, p.org].filter(Boolean).join(", ") }) : null),
      el("button", { class: "btn", onclick: () => contactDialog(p, c) }, icon("edit"), el("span", { class: "lbl", text: "Edit" }))),
    p.phones.map(x => line("phone", x.label || "phone", x.value, "tel:" + x.value.replace(/[^+\d]/g, ""))),
    p.emails.map(x => line("mail", x.label || "email", x.value, "mailto:" + x.value)),
    p.address ? line("place", "address", p.address) : null,
    p.birthday ? line("calendar", "birthday", fmtBirthday(p.birthday)) : null,
    p.groups && p.groups.length ? line("user", "groups", p.groups.join(", ")) : null,
    p.notes ? line("text", "notes", p.notes) : null);
}

async function duplicateContacts(c) {
  let groups;
  try { groups = await api("GET", "/api/contacts/duplicates"); } catch (e) { return fail(e); }
  const dlg = dialog({ title: "Duplicate contacts", wide: true, body: groups.length ? el("div", {},
    el("p", { class: "muted", style: { marginTop: 0, fontSize: "13px" }, text: "These look like the same person (same name, email or phone). Merging keeps every phone, email and group, and puts the extra copies in the trash." }),
    ...groups.map(g => el("div", { class: "card items-list", style: { marginBottom: "10px", boxShadow: "none" } },
      ...g.map(p => el("div", {}, avatarOf(p, 30), el("div", { class: "grow" }, el("div", { text: p.name, style: { fontWeight: 600 } }),
        el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [...p.emails.map(e => e.value), ...p.phones.map(x => x.value)].join(" · ") })))),
      el("div", { style: { justifyContent: "flex-end" } }, el("button", { class: "btn primary", onclick: async e => {
        try { await api("POST", "/api/contacts/merge", { ids: g.map(p => p.id) }); e.target.closest(".card").remove(); toast("Merged"); showContacts(c); } catch (err) { fail(err); }
      } }, "Merge into one"))))) : el("p", { text: "No duplicates found." }),
  buttons: [{ label: "Done", primary: true }] });
}

function fmtBirthday(b) {
  const m = b.match(/^(\d{4}|--)-?(\d{2})-?(\d{2})/);
  if (!m) return b;
  const d = new Date(m[1] === "--" ? 2000 : +m[1], +m[2] - 1, +m[3]);
  return d.toLocaleDateString([], m[1] === "--" ? { month: "long", day: "numeric" } : { year: "numeric", month: "long", day: "numeric" });
}

function contactDialog(p, c) {
  const f = (label, value, attrs) => { const i = el("input", { class: "input", value: value || "", ...attrs }); return [i, el("div", { class: "field" }, el("label", { text: label }), i)]; };
  const [given, gf] = f("First name", p && p.given), [family, ff] = f("Last name", p && p.family);
  const [org, of] = f("Company", p && p.org), [title, tf] = f("Job title", p && p.title);
  const [grp, grpf] = f("Groups (separated by commas)", p && p.groups ? p.groups.join(", ") : "", { placeholder: "Family, School, Work" });
  const [bday, bf] = f("Birthday", p && p.birthday && /^\d{4}-?\d{2}-?\d{2}$/.test(p.birthday) ? p.birthday.replace(/^(\d{4})-?(\d{2})-?(\d{2})$/, "$1-$2-$3") : "", { type: "date" });
  const multi = (items, kind, labels) => {
    const box = el("div");
    const add = (it) => box.append(el("div", { class: "row multi", style: { marginBottom: "6px", alignItems: "center" } },
      el("select", { class: "input", style: { width: "110px", flex: "none" } }, labels.map(l => el("option", { value: l, text: l || "—", selected: (it.label || "") === l }))),
      el("input", { class: "input", value: it.value || "", type: kind === "email" ? "email" : "tel", placeholder: kind === "email" ? "name@example.com" : "+1 555 0100" }),
      el("button", { class: "btn ghost icon", type: "button", onclick: e => e.currentTarget.parentElement.remove(), "aria-label": "Remove" }, icon("close"))));
    (items.length ? items : [{}]).forEach(add);
    const wrap = el("div", { class: "field" }, el("label", { text: kind === "email" ? "Email" : "Phone" }), box,
      el("button", { class: "btn ghost", type: "button", style: { alignSelf: "flex-start" }, onclick: () => add({}) }, icon("plus"), "Add " + (kind === "email" ? "email" : "phone")));
    wrap.values = () => [...box.querySelectorAll(".multi")].map(r => ({ label: r.querySelector("select").value, value: r.querySelector("input").value })).filter(x => x.value.trim());
    return wrap;
  };
  const phones = multi(p ? p.phones : [], "phone", ["mobile", "home", "work", "other", ""]);
  const emails = multi(p ? p.emails : [], "email", ["home", "work", "other", ""]);
  const address = el("textarea", { class: "input", placeholder: "Street\nCity\nState\nCountry" }, p ? p.address : "");
  const notes = el("textarea", { class: "input", placeholder: "Notes" }, p ? p.notes : "");
  const body = el("div", {}, el("div", { class: "row" }, gf, ff), el("div", { class: "row" }, of, tf), phones, emails,
    el("div", { class: "field" }, el("label", { text: "Address" }), address), el("div", { class: "row" }, bf, grpf), el("div", { class: "field" }, el("label", { text: "Notes" }), notes));
  const [book, name] = p ? p.id.split("/") : [];
  dialog({ title: p ? "Edit contact" : "New contact", wide: true, body, buttons: [
    p ? { label: "Delete", danger: true, action: async () => {
      if (!(await confirmBox("Delete " + p.name + "?", "It's deleted from your phones too.", "Delete", true))) return false;
      try { await api("DELETE", `/api/contacts/${encodeURIComponent(book)}/${encodeURIComponent(name)}`); CONTACTS.sel = null; showContacts(c); } catch (e) { fail(e); }
    } } : null,
    "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => {
      const data = { given: given.value, family: family.value, org: org.value, title: title.value, phones: phones.values(), emails: emails.values(),
        address: address.value, birthday: bday.value, notes: notes.value, groups: grp.value.split(",").map(x => x.trim()).filter(Boolean), name: p && !given.value && !family.value ? p.name : "" };
      const saved = p ? await api("PUT", `/api/contacts/${encodeURIComponent(book)}/${encodeURIComponent(name)}`, data) : await api("POST", "/api/contacts", data);
      CONTACTS.sel = saved.id;
      showContacts(c);
    } }].filter(Boolean) });
}

// --- home: today and this week, tasks due, recent documents, what was shared lately, starred

async function showHome(c) {
  let h;
  try { h = await api("GET", "/api/home?tz=" + encodeURIComponent(tz)); } catch (e) { return fail(e); }
  const name = (S.me.user.display || S.me.user.name).split(" ")[0];
  const today = ymd(new Date()), tomorrow = ymd(addDays(new Date(), 1));
  const dayLabel = d => {
    const k = ymd(d);
    if (k === today) return "Today";
    if (k === tomorrow) return "Tomorrow";
    if (k < today) return "Overdue · " + d.toLocaleDateString([], { month: "short", day: "numeric" });
    return d.toLocaleDateString([], { weekday: "long" });
  };
  const card = (title, ic, more, body) => el("div", { class: "card hcard" }, el("div", { class: "hhead" }, icon(ic), el("h3", { text: title }), el("div", { class: "grow" }), more), body);
  const link = (text, hash) => el("a", { href: hash, class: "muted", style: { fontSize: "13px" }, text });
  const empty = t => el("div", { class: "muted hempty", text: t });
  const events = h.events.length ? el("div", { class: "hlist" }, h.events.map(o => {
    const s = evStart(o);
    return el("div", { class: "hrow", onclick: () => { CAL.focus = new Date(s.getFullYear(), s.getMonth(), s.getDate()); location.hash = "#/calendar"; } },
      el("div", { class: "when" }, el("b", { text: dayLabel(s) }), el("span", { text: o.allDay ? "All day" : timeLabel(s) })),
      el("div", { class: "grow" }, el("div", { text: o.title || "(no title)" }), o.invite ? el("span", { class: "pill", text: "Invitation from " + o.invite.organizer }) : o.location ? el("div", { class: "muted", style: { fontSize: "12.5px" }, text: o.location }) : null));
  })) : empty("Nothing in the next week.");
  const tasks = h.tasks.length ? el("div", { class: "hlist" }, h.tasks.map(t => {
    const due = t.due.length === 10 ? parseYmd(t.due) : new Date(t.due);
    const late = due < new Date(new Date().toDateString());
    return el("div", { class: "hrow" },
      el("input", { type: "checkbox", class: "cb round", "aria-label": "Done", onclick: async e => {
        const [l, n] = t.id.split("/");
        try { await api("PUT", `/api/tasks/${encodeURIComponent(l)}/${encodeURIComponent(n)}`, { ...t, done: true, tz }); toast("Done: " + t.title); showHome(c); } catch (err) { fail(err); }
      } }),
      el("div", { class: "grow", text: t.title }), el("span", { class: "pill" + (late ? " late" : ""), text: dayLabel(due) }));
  })) : empty("Nothing due this week.");
  const fileRow = (it, onclick, sub) => el("div", { class: "hrow", onclick }, kindIcon(it.kind), el("div", { class: "grow" }, el("div", { text: it.name }), sub ? el("div", { class: "muted", style: { fontSize: "12.5px" }, text: sub }) : null));
  const openEntry = it => {
    S.share = it.share ? String(it.share) : null; S.shareInfo = null;
    if (it.dir) return goFolder(it.path, S.share);
    if (S.officeExts.has(extOf(it.name))) return openOffice(it.path);
    goFolder(parentOf(it.path), S.share);
  };
  const recent = h.recent.length ? el("div", { class: "hlist" }, h.recent.map(it => fileRow(it, () => openEntry(it), "Opened " + fmtDate(it.opened)))) : empty("Documents and notes you open show up here.");
  const shared = h.shared.length ? el("div", { class: "hlist" }, h.shared.map(sh => fileRow(sh, () => goFolder("/", String(sh.id)), "From " + sh.who + " · " + fmtDate(sh.created * 1000)))) : empty("Nothing shared with you yet.");
  const favs = h.favorites.length ? el("div", { class: "hfavs" }, h.favorites.map(f => el("button", { class: "fav", onclick: () => openEntry(f) }, kindIcon(f.kind), el("span", { text: f.name })))) : empty("Star files and folders from their ⋯ menu to keep them here.");
  c.replaceChildren(...[
    el("div", { class: "hello" }, el("h1", { text: `${h.greeting}, ${name}` }), el("p", { class: "muted", text: new Date().toLocaleDateString([], { weekday: "long", month: "long", day: "numeric" }) + (h.unread ? ` · ${h.unread} new notification${h.unread > 1 ? "s" : ""}` : "") })),
    S.me.ai ? el("form", { class: "askbar card", onsubmit: e => { e.preventDefault(); const v = e.target.q.value.trim(); if (!v) return; location.hash = "#/ask"; setTimeout(() => { const t = document.querySelector(".askcard textarea"); if (t) { t.value = v; t.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" })); } }, 300); } },
      icon("search"), el("input", { name: "q", placeholder: "Ask Cove anything: when's the dentist? what's for dinner?" }), el("button", { class: "btn primary", text: "Ask" })) : null,
    el("div", { class: "quick" },
      el("button", { class: "qbtn", onclick: () => newDoc("document") }, kindIcon("doc"), "Word"),
      el("button", { class: "qbtn", onclick: () => newDoc("spreadsheet") }, kindIcon("sheet"), "Excel"),
      el("button", { class: "qbtn", onclick: () => newDoc("presentation") }, kindIcon("slides"), "PowerPoint"),
      el("button", { class: "qbtn", onclick: () => newNote() }, kindIcon("text"), "Note"),
      el("button", { class: "qbtn", onclick: () => scanDialog("/Scans") }, kindIcon("pdf"), "Scan"),
      el("button", { class: "qbtn", onclick: () => { location.hash = "#/fridge"; setTimeout(() => fridgeDialog(null, "note", $("content")), 400); } }, kindIcon("image"), "Fridge note")),
    h.fridge && h.fridge.length ? el("div", { class: "card hcard", style: { marginBottom: "16px" } }, el("div", { class: "hhead" }, icon("pin"), el("h3", { text: "On the fridge" }), el("div", { class: "grow" }), link("All", "#/fridge")),
      el("div", { class: "fridge small" }, h.fridge.map(n => fridgeCard(n, null, true)))) : null,
    (() => { const b = el("div", { class: "hgrid extras" }); homeExtras(b); return b; })(),
    el("div", { class: "hgrid" },
      card("Coming up", "calendar", link("Calendar", "#/calendar"), events),
      card("Due soon", "check", link("Tasks", "#/tasks"), tasks),
      card("Starred", "star", null, favs),
      card("Recent", "clock", link("All", "#/recent"), recent),
      card("Shared with you", "inbox", link("All", "#/shared"), shared))].filter(Boolean));
}

// --- notifications: the bell, and browser notifications while Cove is open

const NOTES_SEEN = { last: +(localStorage.getItem("cove.lastnote") || 0) };

async function pollNotifications() {
  let r;
  try { r = await api("GET", "/api/notifications"); } catch (_) { return; }
  const badge = $("badge");
  if (badge) { badge.textContent = r.unread > 9 ? "9+" : r.unread; badge.classList.toggle("hide", !r.unread); }
  const fresh = r.items.filter(n => !n.read && n.created > NOTES_SEEN.last);
  if (fresh.length) {
    NOTES_SEEN.last = Math.max(...fresh.map(n => n.created));
    try { localStorage.setItem("cove.lastnote", NOTES_SEEN.last); } catch (_) { /* private window */ }
    if (localStorage.getItem("cove.browsernotes") === "1" && "Notification" in window && Notification.permission === "granted" && document.hidden) {
      for (const n of fresh.slice(0, 3)) {
        const note = new Notification("Cove", { body: n.text, icon: "/assets/icon-192.png", tag: "cove-" + n.id });
        note.onclick = () => { window.focus(); if (n.link) location.hash = n.link.slice(1); };
      }
    }
  }
  return r;
}

async function notificationsPanel(anchor) {
  const r = await pollNotifications();
  if (!r) return;
  document.querySelectorAll(".menu-pop").forEach(m => m.remove());
  const m = el("div", { class: "menu-pop notes-pop" },
    el("div", { class: "row", style: { padding: "6px 10px 8px", alignItems: "center" } }, el("b", { style: { flex: 1 }, text: "Notifications" }),
      el("a", { href: "#/settings", class: "muted", style: { fontSize: "12.5px" }, onclick: () => m.remove(), text: "Settings" })),
    ...(r.items.length ? r.items.slice(0, 30).map(n => el("button", { class: n.read ? "" : "unread", onclick: () => { m.remove(); if (n.link) location.hash = n.link.slice(1); } },
      el("div", { style: { flex: 1, minWidth: 0 } }, el("div", { text: n.text, style: { whiteSpace: "normal" } }), el("div", { class: "muted", style: { fontSize: "11.5px" }, text: fmtDate(n.created * 1000) })))) :
      [el("div", { class: "muted", style: { padding: "18px 12px", textAlign: "center" }, text: "Nothing yet. Shares, changes to shared calendars, invitations and reminders show up here." })]));
  document.body.append(m);
  const rect = anchor.getBoundingClientRect();
  m.style.top = rect.bottom + 6 + "px";
  m.style.left = Math.max(8, Math.min(rect.right - m.offsetWidth, document.documentElement.clientWidth - m.offsetWidth - 8)) + "px";
  setTimeout(() => {
    const away = e => { if (!m.contains(e.target)) { m.remove(); document.removeEventListener("mousedown", away); } };
    document.addEventListener("mousedown", away);
  });
  if (r.unread) { await api("POST", "/api/notifications/read").catch(() => {}); $("badge").classList.add("hide"); }
}

// --- notes: Markdown files in "Notes", with checklists you can tick and pins

async function showNotes(c, open) {
  if (open) return noteEditor(c, open);
  let list;
  try { list = await api("GET", "/api/notes"); } catch (e) { return fail(e); }
  const q = el("input", { class: "input", type: "search", placeholder: "Filter notes", style: { maxWidth: "260px" }, oninput: () => draw() });
  const grid = el("div", { class: "notes-grid" });
  const draw = () => {
    const f = q.value.toLowerCase();
    const shown = list.filter(n => !f || (n.title + " " + n.excerpt).toLowerCase().includes(f));
    grid.replaceChildren(...(shown.length ? shown.map(n => el("div", { class: "card note-card", onclick: () => { location.hash = "#/notes" + n.path.split("/").map(encodeURIComponent).join("/"); } },
      el("div", { class: "row", style: { alignItems: "flex-start", gap: "6px" } }, el("h3", { text: n.title }),
        el("button", { class: "btn ghost icon pinbtn" + (n.pinned ? " on" : ""), title: n.pinned ? "Unpin" : "Pin", onclick: async e => {
          e.stopPropagation(); await api("POST", "/api/notes/pin", { path: n.path, pinned: !n.pinned }).catch(fail); showNotes(c);
        } }, icon("pin"))),
      el("p", { text: n.excerpt || "Empty note" }),
      el("div", { class: "row", style: { gap: "6px", marginTop: "auto", alignItems: "center" } },
        n.total ? el("span", { class: "pill" }, icon("check"), `${n.done}/${n.total}`) : null,
        el("span", { class: "muted", style: { fontSize: "12px" }, text: fmtDate(n.mod) })))) :
      [el("div", { class: "card empty", style: { gridColumn: "1/-1" } }, icon("text"), el("h3", { text: list.length ? "No notes match" : "No notes yet" }),
        el("p", { text: "Notes are Markdown files in your Notes folder. Start a checklist with “- [ ] ”." }))]));
  };
  draw();
  c.replaceChildren(el("div", { class: "bar" }, el("div", { class: "page-title", style: { margin: 0 }, text: "Notes" }), el("div", { class: "grow" }), q,
    el("button", { class: "btn primary", onclick: () => newNote() }, icon("plus"), el("span", { class: "lbl", text: "Note" }))), grid);
}

async function noteEditor(c, path) {
  let text;
  try { const r = await fetch("/api/files/content?path=" + encPath(path)); if (!r.ok) throw new Error("That note isn't there any more."); text = await r.text(); } catch (e) { return fail(e); }
  let saved = text, timer = null, mode = localStorage.getItem("cove.notemode") || "preview";
  const status = el("span", { class: "muted", style: { fontSize: "12.5px" } });
  const save = async () => {
    if (text === saved) return;
    status.textContent = "Saving…";
    const url = "/api/files/content?conflict=replace&path=" + encPath(path);
    try { await xhrSend("PUT", url, new Blob([text])); saved = text; status.textContent = "Saved"; }
    catch (e) {
      if (!navigator.onLine || /connection/.test(e.message)) {
        OFFQ.add({ method: "PUT", url, body: text, type: "text/markdown" }); saved = text; status.textContent = "Saved on this device; it syncs when you're online";
      } else status.textContent = "Not saved: " + e.message;
    }
  };
  const soon = () => { clearTimeout(timer); status.textContent = "Editing…"; timer = setTimeout(save, 800); };
  const ta = el("textarea", { class: "note-text", spellcheck: true, placeholder: "Write in Markdown. “- [ ] ” starts a checklist item.", oninput: e => { text = e.target.value; soon(); } });
  ta.value = text;
  const preview = el("div", { class: "note-preview" });
  const drawPreview = () => {
    const md = markdown(text.replace(/^(\s*[-*])\s\[( |x|X)\]\s/gm, (m, b, x, off) => `${b} ${x.trim() ? "☑" : "☐"}⁠ `));
    // tick boxes: clicking one flips that "- [ ]" in the text
    let i = 0;
    md.querySelectorAll("li").forEach(li => {
      const t = li.textContent;
      if (!t.startsWith("☐") && !t.startsWith("☑")) return;
      const idx = i++;
      const box = el("input", { type: "checkbox", class: "cb", checked: t.startsWith("☑"), onclick: () => {
        let n = -1;
        text = text.replace(/^(\s*[-*]\s)\[( |x|X)\]/gm, (m, pre, x) => (++n === idx ? pre + (x.trim() ? "[ ]" : "[x]") : m));
        ta.value = text; soon(); drawPreview();
      } });
      li.classList.add("check-item" + (t.startsWith("☑") ? "" : ""));
      if (t.startsWith("☑")) li.classList.add("done");
      li.firstChild.textContent = li.firstChild.textContent.replace(/^[☐☑]⁠?\s?/, "");
      li.prepend(box);
    });
    preview.replaceChildren(md);
  };
  const body = el("div", { class: "note-body " + mode });
  const setMode = m => { mode = m; localStorage.setItem("cove.notemode", m); body.className = "note-body " + m; if (m !== "edit") drawPreview(); segs.forEach(b => b.classList.toggle("on", b.dataset.m === m)); if (m === "edit") ta.focus(); };
  const segs = [["edit", "Write"], ["split", "Both"], ["preview", "Read"]].map(([m, l]) => el("button", { "data-m": m, class: mode === m ? "on" : "", style: { padding: "6px 12px" }, onclick: () => setMode(m) }, l));
  body.append(ta, preview);
  drawPreview();
  c.replaceChildren(el("div", { class: "bar" },
    el("button", { class: "btn ghost icon", onclick: async () => { await save(); location.hash = "#/notes"; }, "aria-label": "All notes" }, icon("left")),
    el("div", { class: "page-title", style: { margin: 0 }, text: baseName(path).replace(/\.(md|markdown|txt)$/i, "") }), status, el("div", { class: "grow" }),
    el("div", { class: "seg" }, segs),
    el("button", { class: "btn ghost icon danger", title: "Delete note", onclick: async () => {
      if (!(await confirmBox("Delete this note?", "It goes to the trash for 30 days.", "Delete", true))) return;
      clearTimeout(timer);
      try { await api("POST", "/api/files/delete", { paths: [path] }); location.hash = "#/notes"; } catch (e) { fail(e); }
    } }, icon("trash"))), body);
  addEventListener("hashchange", function leave() { removeEventListener("hashchange", leave); clearTimeout(timer); save(); });
}

// --- admin: space per person and limits, every link and share

async function showAdmin(c) {
  if (!S.me.user.admin) { location.hash = "#/home"; return; }
  let users, all;
  try { [users, all] = await Promise.all([api("GET", "/api/admin/users"), api("GET", "/api/admin/shares")]); } catch (e) { return fail(e); }
  const total = users.reduce((a, u) => a + u.used, 0);
  const limit = u => el("select", { class: "input", style: { width: "auto" }, onchange: async e => {
    try { await api("PUT", "/api/admin/users/" + encodeURIComponent(u.name), { quota: +e.target.value }); toast("Limit set for " + (u.display || u.name)); showAdmin(c); } catch (err) { fail(err); }
  } }, [[0, "No limit"], [10, "10 GB"], [50, "50 GB"], [100, "100 GB"], [250, "250 GB"], [500, "500 GB"], [1000, "1 TB"]].map(([gb, l]) =>
    el("option", { value: gb * 2 ** 30, text: l, selected: u.quota === gb * 2 ** 30 })), u.quota && ![10, 50, 100, 250, 500, 1000].some(g => g * 2 ** 30 === u.quota) ? el("option", { value: u.quota, text: fmtSize(u.quota), selected: true }) : null);
  c.replaceChildren(
    el("div", { class: "page-title", text: "Admin" }),
    el("div", { class: "card section" }, el("h3", { text: "People and space" }), el("p", { text: `${users.length} people use Cove, ${fmtSize(total)} in all (files, earlier versions and trash).` }),
      el("div", { class: "items-list", style: { margin: "0 -20px -18px" } }, users.map(u => el("div", {},
        el("div", { class: "avatar", text: (u.display || u.name).slice(0, 1).toUpperCase() }),
        el("div", { class: "grow" }, el("div", { style: { fontWeight: 600 } }, u.display || u.name, u.admin ? el("span", { class: "pill", style: { marginLeft: "6px" }, text: "admin" }) : null),
          el("div", { class: "muted", style: { fontSize: "12.5px" }, text: `${u.name}${u.email ? " · " + u.email : ""} · ${u.files} files · last seen ${u.seen ? fmtDate(u.seen * 1000) : "never"}` }),
          el("div", { class: "usage" }, el("div", { style: { width: (u.quota ? Math.min(100, 100 * u.used / u.quota) : Math.min(100, 100 * u.used / Math.max(total, 1))) + "%", background: u.quota && u.used > u.quota * 0.9 ? "var(--danger)" : "" } }))),
        el("div", { style: { textAlign: "right", fontSize: "13px" } }, el("div", { text: fmtSize(u.used) + (u.quota ? " of " + fmtSize(u.quota) : "") }), limit(u),
          u.admin ? null : el("label", { class: "check", style: { justifyContent: "flex-end", margin: "6px 0 0" } }, el("input", { type: "checkbox", checked: u.kid, onchange: async e => {
            try { await api("PUT", `/api/admin/users/${encodeURIComponent(u.name)}/kid`, { kid: e.target.checked }); toast(e.target.checked ? (u.display || u.name) + " has a kid's account now" : "A regular account again"); } catch (err) { fail(err); }
          } }), "Kid's account")))))),
    aiAdmin(),
    connectAdmin(),
    el("div", { class: "card section" }, el("h3", { text: "Links" }), el("p", { text: all.links.length ? "Every link anyone made. Ending one stops it working." : "Nobody has made a link yet." }),
      all.links.length ? el("div", { class: "items-list", style: { margin: "0 -20px -18px" } }, all.links.map(sh => el("div", {}, kindIcon(sh.dir ? "folder" : "file"),
        el("div", { class: "grow" }, el("div", { style: { fontWeight: 600 }, text: sh.name }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: `${sh.owner} · ${sh.path} · ${sh.downloads} download${sh.downloads === 1 ? "" : "s"} · made ${fmtDate(sh.created * 1000)}` }),
          el("div", { class: "row", style: { gap: "6px", marginTop: "4px", flexWrap: "wrap" } }, linkPills(sh))),
        el("button", { class: "btn danger", onclick: async () => { if (await confirmBox("End this link?", "It stops working for everyone who has it.", "End link", true)) { await api("DELETE", "/api/admin/links/" + sh.token).catch(fail); showAdmin(c); } } }, "End")))) : null),
    el("div", { class: "card section" }, el("h3", { text: "Shared between people" }), el("p", { text: all.people.length ? "Files, folders, calendars and lists shared inside the household." : "Nothing is shared between people yet." }),
      all.people.length ? el("div", { class: "items-list", style: { margin: "0 -20px -18px" } }, all.people.map(p => el("div", {}, icon(p.kind === "calendar" ? "calendar" : "folder"),
        el("div", { class: "grow" }, el("div", { style: { fontWeight: 600 }, text: p.what }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: `${p.owner} → ${p.with} · ${p.edit ? "can edit" : "can view"}` })),
        el("button", { class: "btn danger", onclick: async () => { await api("DELETE", `/api/admin/shares/${p.kind}/${p.id}`).catch(fail); showAdmin(c); } }, "End")))) : null));
}

// --- live updates: what others change shows up without a reload

let liveTimer;
function startLive() {
  if (!window.EventSource) return;
  const es = new EventSource("/api/live");
  es.onmessage = e => onLive(e.data);
}
function onLive(topic) {
  if (topic === "notifications") { pollNotifications(); return; }
  const parts = location.hash.split("/"), sec = parts[1] || "home";
  if (topic === "chat") chatBadge();
  const views = { files: ["files", "shared", "recent", "home", "notes", "photos"], calendar: ["calendar", "home"], tasks: ["tasks", "home"], contacts: ["contacts"], fridge: ["fridge", "home"],
    household: ["meals", "chores", "records", "budget", "trips", "wishes", "health", "pantry", "journal", "kiosk"], chat: ["chat"], photos: ["photos"] };
  if (!(views[topic] || []).includes(sec)) return;
  // never pull the page out from under someone typing or looking at something
  if (topic === "chat" && sec === "chat" && CHAT.room) { // a new message: redraw the conversation, keeping what's being typed
    const pane = document.querySelector(".chatpane"), t = pane && pane.querySelector("textarea");
    const draft = t ? t.value : "";
    if (pane) drawRoom(pane, CHAT.room, $("content")).then(() => { const n = pane.querySelector("textarea"); if (n && draft) { n.value = draft; n.focus(); } });
    return;
  }
  if (document.querySelector(".veil, .viewer, .office, .menu-pop") || (sec === "notes" && parts.length > 2) || document.activeElement && /INPUT|TEXTAREA/.test(document.activeElement.tagName)) return;
  clearTimeout(liveTimer);
  liveTimer = setTimeout(route, 300);
}

// --- notifications on this device (Web Push; on an iPhone, Cove has to be on the home screen first)

function urlB64(s) {
  const pad = "=".repeat((4 - s.length % 4) % 4);
  const raw = atob((s + pad).replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from([...raw].map(c => c.charCodeAt(0)));
}
async function enablePush() {
  if (!("serviceWorker" in navigator) || !("PushManager" in window)) {
    throw new Error(/iPhone|iPad/.test(navigator.userAgent) ? "On an iPhone, add Cove to the home screen first (Share → Add to Home Screen), then turn this on from there." : "This browser can't receive notifications.");
  }
  if ((await Notification.requestPermission()) !== "granted") throw new Error("Notifications weren't allowed.");
  const reg = await navigator.serviceWorker.ready;
  const { key } = await api("GET", "/api/push/key");
  const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlB64(key) });
  await api("POST", "/api/push/subscribe", sub.toJSON());
}
async function disablePush() {
  const reg = await navigator.serviceWorker.ready;
  const sub = await reg.pushManager.getSubscription();
  if (sub) { await api("POST", "/api/push/unsubscribe", { endpoint: sub.endpoint }).catch(() => {}); await sub.unsubscribe(); }
}

// --- the fridge: the household's notice board

const STICKY = { yellow: "#fff3a6", pink: "#ffd3e2", blue: "#cfe6ff", green: "#d4f5d0", orange: "#ffdcb5", purple: "#e6d9ff", white: "#ffffff" };

function aiAdmin() {
  const box = el("div", { class: "card section" }, el("h3", { text: "AI (Ask Cove and smart search)" }), el("p", { class: "muted", text: "Loading…" }));
  api("GET", "/api/admin/ai").then(st => {
    const url = el("input", { class: "input", value: st.config.url || "", placeholder: "http://host.docker.internal:1234/v1 (LM Studio) or http://host.docker.internal:11434/v1 (Ollama)" });
    const model = el("select", { class: "input" }), embed = el("select", { class: "input" });
    const fill = (ids) => {
      model.replaceChildren(el("option", { value: "", text: "— Off —" }), ...ids.filter(i => !/embed/i.test(i)).map(i => el("option", { value: i, text: i, selected: i === st.config.model })));
      embed.replaceChildren(el("option", { value: "", text: "— Off —" }), ...ids.filter(i => /embed/i.test(i)).map(i => el("option", { value: i, text: i, selected: i === st.config.embed })));
    };
    fill([st.config.model, st.config.embed].filter(Boolean));
    const load = async () => { try { const r = await api("GET", "/api/admin/ai/models?url=" + encodeURIComponent(url.value)); fill(r.models); toast(`${r.models.length} models found`); } catch (e) { fail(e); } };
    if (st.config.url) load();
    box.replaceChildren(el("h3", { text: "AI (Ask Cove and smart search)" }),
      el("p", { text: "Cove uses an AI server on your home network (LM Studio or Ollama). Questions and documents never leave your network." }),
      el("div", { class: "row", style: { alignItems: "flex-end" } }, el("div", { class: "field grow" }, el("label", { text: "Server address" }), url), el("button", { class: "btn", style: { marginBottom: "14px" }, onclick: load }, "Find models")),
      el("div", { class: "row" }, el("div", { class: "field" }, el("label", { text: "Model for answers" }), model), el("div", { class: "field" }, el("label", { text: "Model for smart search (embeddings)" }), embed)),
      el("p", { class: "muted", style: { fontSize: "12.5px" }, text: `Smart search has read ${st.embedded} of ${st.documents} documents. ${st.ocr ? "Words in scans and photos are read with Tesseract." : "Reading words in scans and photos needs Tesseract in Cove's image."}` }),
      el("button", { class: "btn primary", onclick: async () => {
        try { await api("PUT", "/api/admin/ai", { url: url.value, model: model.value, embed: embed.value }); S.me = await api("GET", "/api/me"); drawShell(); route(); toast("Saved"); } catch (e) { fail(e); }
      } }, "Save"));
  }).catch(e => box.replaceChildren(el("p", { text: e.message })));
  return box;
}

async function showFridge(c) {
  let notes;
  try { [notes] = await Promise.all([api("GET", "/api/fridge"), people()]); } catch (e) { return fail(e); }
  const photo = el("input", { type: "file", accept: "image/*", class: "hide", onchange: async e => {
    const f = e.target.files[0];
    if (!f) return;
    const caption = await ask("Photo on the fridge", "Caption (optional)", "", "Put it up");
    if (caption === null) return;
    try { await xhrSend("POST", "/api/fridge/photo?caption=" + encodeURIComponent(caption), f); showFridge(c); } catch (err) { fail(err); }
  } });
  c.replaceChildren(
    el("div", { class: "bar" }, el("div", { class: "page-title", style: { margin: 0 }, text: "Fridge" }),
      el("span", { class: "muted", style: { fontSize: "13px" }, text: "Everyone in the household sees this." }), el("div", { class: "grow" }), photo,
      el("button", { class: "btn", onclick: () => pollDialog(c) }, icon("star"), el("span", { class: "lbl", text: "Poll" })),
      el("button", { class: "btn", onclick: () => countdownDialog(c) }, icon("clock"), el("span", { class: "lbl", text: "Countdown" })),
      el("button", { class: "btn", onclick: () => fridgeDialog(null, "list", c) }, icon("check"), el("span", { class: "lbl", text: "List" })),
      el("button", { class: "btn", onclick: () => photo.click() }, icon("image"), el("span", { class: "lbl", text: "Photo" })),
      el("button", { class: "btn primary", onclick: () => fridgeDialog(null, "note", c) }, icon("plus"), el("span", { class: "lbl", text: "Note" }))),
    notes.length ? el("div", { class: "fridge" }, notes.map(n => fridgeCard(n, c, false))) :
      el("div", { class: "card empty" }, icon("home"), el("h3", { text: "The fridge is empty" }), el("p", { text: "Put up a note, a list or a photo for everyone at home to see." })));
}

function fridgeCard(n, c, small) {
  const tilt = ((n.id * 37) % 7 - 3) * 0.6;
  const meta = el("div", { class: "fmeta" }, (n.pinned ? "📌 " : "") + n.who + " · " + fmtDate(n.created * 1000) + (n.expires ? " · until " + new Date(n.expires * 1000).toLocaleDateString([], { month: "short", day: "numeric" }) : ""));
  const card = el("div", { class: "fnote " + n.kind, style: { "--bg": STICKY[n.color] || STICKY.yellow, transform: `rotate(${tilt}deg)` } });
  if (n.kind === "photo") {
    card.append(el("img", { src: `/api/fridge/${n.id}/photo?w=${small ? 360 : 720}`, alt: n.text, loading: "lazy" }), n.text ? el("div", { class: "caption", text: n.text }) : null);
  } else {
    if (n.text) card.append(el("div", { class: "ftext", text: n.text }));
    if (n.kind === "list") card.append(el("div", { class: "flist" }, n.items.map((it, i) => el("label", { class: it.done ? "done" : "" },
      el("input", { type: "checkbox", checked: it.done, onclick: async e => {
        e.stopPropagation();
        const items = n.items.map((x, j) => j === i ? { ...x, done: e.target.checked } : x);
        try { await api("PUT", "/api/fridge/" + n.id, { kind: n.kind, text: n.text, color: n.color, items, pinned: n.pinned }); n.items = items; } catch (err) { fail(err); }
        if (c) showFridge(c);
      } }), el("span", { text: it.text }), it.done && it.by ? el("small", { text: it.by }) : null))));
    fridgeExtra(n, c, card);
  }
  card.append(meta);
  if (!small) card.onclick = e => { if (!e.target.closest("input, label, .popt")) fridgeMenu(e, n, c); };
  else card.onclick = () => { location.hash = "#/fridge"; };
  return card;
}

function fridgeMenu(e, n, c) {
  popMenu(e, [
    n.kind === "note" || n.kind === "list" ? { label: n.mine ? "Edit" : "Add to it", icon: "edit", action: () => fridgeDialog(n, n.kind, c) } : null,
    n.mine ? { label: n.pinned ? "Unpin" : "Pin to the top", icon: "pin", action: async () => { await api("PUT", "/api/fridge/" + n.id, { ...n, pinned: !n.pinned }).catch(fail); showFridge(c); } } : null,
    n.kind === "photo" ? { label: "Open the photo", icon: "image", action: () => window.open(`/api/fridge/${n.id}/photo?w=1600`, "_blank") } : null,
    n.mine ? "-" : null,
    n.mine ? { label: "Take it down", icon: "trash", danger: true, action: async () => { await api("DELETE", "/api/fridge/" + n.id).catch(fail); showFridge(c); } } : null,
  ]);
}

function fridgeDialog(n, kind, c) {
  let color = n ? n.color : "yellow";
  const text = el("textarea", { class: "input", placeholder: kind === "list" ? "What's the list for? (optional)" : "Write a note for everyone…", style: { minHeight: kind === "list" ? "60px" : "140px" }, disabled: n && !n.mine }, n ? n.text : "");
  const items = el("div", { class: "fitems" });
  const addItem = (it = { text: "", done: false }) => {
    const row = el("div", { class: "row", style: { alignItems: "center", marginBottom: "6px" } },
      el("input", { type: "checkbox", checked: it.done }), el("input", { class: "input", value: it.text, placeholder: "An item", onkeydown: e => { if (e.key === "Enter") { e.preventDefault(); addItem(); } } }),
      el("button", { class: "btn ghost icon", type: "button", onclick: () => row.remove() }, icon("close")));
    row.dataset.by = it.by || "";
    items.append(row);
    setTimeout(() => row.querySelector(".input").focus(), 10);
  };
  if (kind === "list") (n && n.items.length ? n.items : [{ text: "", done: false }]).forEach(addItem);
  const sw = el("div", { class: "swatches" });
  const draw = () => sw.replaceChildren(...Object.entries(STICKY).map(([k, v]) => el("button", { type: "button", class: k === color ? "on" : "", style: { background: v, borderColor: "var(--line)" }, "aria-label": k, onclick: () => { color = k; draw(); } })));
  draw();
  const until = el("select", { class: "input" }, [["0", n && n.expires ? "Keep its date" : "Stays up"], ["1", "Take down after a day"], ["3", "After 3 days"], ["7", "After a week"], ["30", "After a month"], ["-1", "Stays up (no end)"]]
    .filter(([v]) => n || v !== "-1").map(([v, l]) => el("option", { value: v, text: l })));
  const pinned = el("input", { type: "checkbox", checked: n ? n.pinned : false });
  const mine = !n || n.mine;
  dialog({ title: n ? (mine ? "Edit" : "Add to the list") : kind === "list" ? "New list on the fridge" : "New note on the fridge", body: el("div", {},
    el("div", { class: "field" }, text),
    kind === "list" ? el("div", { class: "field" }, el("label", { text: "Items" }), items, el("button", { class: "btn ghost", type: "button", style: { alignSelf: "flex-start" }, onclick: () => addItem() }, icon("plus"), "Add an item")) : null,
    mine ? el("div", { class: "label", style: { marginBottom: "6px" }, text: "Color" }) : null, mine ? sw : null,
    mine ? el("div", { class: "row" }, el("div", { class: "field" }, el("label", { text: "Comes down" }), until), el("label", { class: "check", style: { marginBottom: "22px" } }, pinned, "Pin to the top")) : null),
  buttons: [{ label: "Cancel" }, { label: n ? "Save" : "Put it up", primary: true, action: async () => {
    const list = [...items.querySelectorAll(".row")].map(r => ({ text: r.querySelector(".input").value, done: r.querySelector("input[type=checkbox]").checked, by: r.dataset.by }));
    const body = { kind, text: text.value, color, items: list, pinned: pinned.checked, expiresDays: +until.value };
    if (n) await api("PUT", "/api/fridge/" + n.id, body); else await api("POST", "/api/fridge", body);
    showFridge(c);
  } }] });
}

// --- activity: what changed in your things, who did it, and undo

async function showActivity(c) {
  let list;
  try { list = await api("GET", "/api/activity"); } catch (e) { return fail(e); }
  const me = S.me.user.name;
  c.replaceChildren(el("div", { class: "page-title", text: "Activity" }),
    el("p", { class: "muted", style: { marginTop: 0 }, text: "Changes to your files, calendars, tasks and contacts, by you and the people you share with. Most can be undone for a week." }),
    list.length ? el("div", { class: "card items-list" }, list.map(a => el("div", { style: { opacity: a.undone ? 0.55 : 1 } },
      el("div", { class: "avatar", text: a.who.slice(0, 1).toUpperCase() }),
      el("div", { class: "grow" }, el("div", {}, el("b", { text: a.actor === me ? "You" : a.who }), " " + a.text), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: fmtDate(a.created * 1000) + (a.undone ? " · undone" : "") })),
      a.canUndo ? el("button", { class: "btn", onclick: async () => {
        try { await api("POST", `/api/activity/${a.id}/undo`); toast("Undone"); showActivity(c); } catch (e) { fail(e); }
      } }, icon("restore"), el("span", { class: "lbl", text: "Undo" })) : null))) :
      el("div", { class: "card empty" }, icon("clock"), el("h3", { text: "Nothing yet" })));
}

// --- duplicate files

async function showDuplicates(c) {
  c.replaceChildren(el("div", { class: "page-title", text: "Duplicate files" }), el("p", { class: "muted", text: "Comparing files… this can take a minute the first time." }));
  let groups;
  try { groups = await api("GET", "/api/files/duplicates"); } catch (e) { return fail(e); }
  const wasted = groups.reduce((a, g) => a + g.size * (g.files.length - 1), 0);
  c.replaceChildren(el("div", { class: "page-title", text: "Duplicate files" }),
    el("p", { class: "muted", style: { marginTop: 0 }, text: groups.length ? `${groups.length} sets of identical files. Removing the extra copies frees ${fmtSize(wasted)}.` : "No duplicates: every file is different." }),
    ...groups.map(g => el("div", { class: "card items-list", style: { marginBottom: "12px" } }, g.files.map(f => el("div", {}, kindIcon(f.kind),
      el("div", { class: "grow" }, el("div", { text: f.name, style: { fontWeight: 600 } }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: parentOf(f.path) + " · " + fmtSize(g.size) })),
      el("button", { class: "btn ghost icon", title: "Show in Files", onclick: () => goFolder(parentOf(f.path), null) }, icon("folder")),
      el("button", { class: "btn ghost icon danger", title: "Move to trash", onclick: async () => { await api("POST", "/api/files/delete", { paths: [f.path] }).catch(fail); showDuplicates(c); } }, icon("trash")))))));
}

// --- comments on a file or folder

async function commentsDialog(it) {
  const box = el("div", { class: "comments" });
  const text = el("textarea", { class: "input", placeholder: "Write a comment. @name tells that person." });
  const load = async () => {
    try {
      const list = await api("GET", "/api/comments?path=" + encPath(it.path) + sq());
      box.replaceChildren(...(list.length ? list.map(cm => el("div", { class: "cmt" }, el("div", { class: "avatar", text: cm.who.slice(0, 1).toUpperCase() }),
        el("div", { class: "grow" }, el("div", {}, el("b", { text: cm.who }), el("span", { class: "muted", style: { fontSize: "12px", marginLeft: "6px" }, text: fmtDate(cm.created * 1000) })), el("div", { text: cm.text, style: { whiteSpace: "pre-wrap" } })),
        cm.mine || !S.share ? el("button", { class: "btn ghost icon", title: "Delete", onclick: async () => { await api("DELETE", "/api/comments/" + cm.id).catch(fail); load(); } }, icon("close")) : null))
        : [el("p", { class: "muted", text: "No comments yet." })]));
    } catch (e) { fail(e); }
  };
  let people = [];
  api("GET", "/api/users").then(u => { people = u; hint.textContent = people.length ? "Mention: " + people.map(p => "@" + p.name).join(" ") : ""; }).catch(() => {});
  const hint = el("div", { class: "muted", style: { fontSize: "12px", marginTop: "4px" } });
  dialog({ title: "Comments on “" + it.name + "”", wide: true, body: el("div", {}, box, el("div", { class: "field", style: { marginTop: "12px" } }, text, hint)),
    buttons: [{ label: "Close" }, { label: "Comment", primary: true, action: async () => {
      if (!text.value.trim()) return false;
      await api("POST", "/api/comments?" + shareQ(), { path: it.path, text: text.value });
      text.value = ""; load(); return false;
    } }] });
  load();
}

// --- templates for new documents and notes

async function templateDialog(kinds, onPick) {
  let list;
  try { list = await api("GET", "/api/templates"); } catch (e) { return fail(e); }
  list = list.filter(t => kinds.includes(t.kind));
  const ic = { document: "doc", spreadsheet: "sheet", note: "text" };
  const dlg = dialog({ title: "Start from a template", wide: true, body: el("div", { class: "templates" },
    kinds.includes("note") ? el("button", { class: "tpl", onclick: () => { dlg.close(); onPick(null); } }, kindIcon("text"), el("b", { text: "Blank note" }), el("small", { text: "Start from nothing" })) : null,
    list.map(t => el("button", { class: "tpl", onclick: () => { dlg.close(); onPick(t); } }, kindIcon(ic[t.kind]), el("b", { text: t.name }), el("small", { text: t.desc })))),
  buttons: [{ label: "Cancel" }] });
}

// --- subscribed calendars

function subscribeDialog(sub, c) {
  let color = sub ? sub.color : "#64748b";
  const name = el("input", { class: "input", value: sub ? sub.name : "", placeholder: "Holidays, School, Team…" });
  const link = el("input", { class: "input", value: sub ? sub.url : "", placeholder: "https://… or webcal://… (.ics)", disabled: !!sub });
  const sw = el("div", { class: "swatches" });
  const draw = () => sw.replaceChildren(...COLORS.map(col => el("button", { type: "button", class: col === color ? "on" : "", style: { background: col }, "aria-label": col, onclick: () => { color = col; draw(); } })));
  draw();
  dialog({ title: sub ? "Subscribed calendar" : "Subscribe to a calendar", body: el("div", {},
    el("p", { class: "muted", style: { marginTop: 0, fontSize: "13px" }, text: "Follow a calendar from another site: public holidays, a school, a sports team, your work calendar's .ics address. Cove checks it every few hours; it's read-only here." }),
    el("div", { class: "field" }, el("label", { text: "Its address" }), link), el("div", { class: "field" }, el("label", { text: "Name" }), name),
    el("div", { class: "label", style: { marginBottom: "6px" }, text: "Color" }), sw,
    sub ? el("p", { class: "muted", style: { fontSize: "12.5px" }, text: (sub.error ? "Last check failed: " + sub.error : "Last checked " + fmtDate(sub.fetched * 1000)) }) : null),
  buttons: [
    sub ? { label: "Unsubscribe", danger: true, action: async () => { await api("DELETE", "/api/subscriptions/" + sub.id).catch(fail); showCalendar(c); } } : null,
    sub ? { label: "Check now", action: async () => { try { await api("POST", `/api/subscriptions/${sub.id}/refresh`); toast("Up to date"); showCalendar(c); } catch (e) { fail(e); } } } : null,
    "grow", { label: "Cancel" },
    { label: sub ? "Save" : "Subscribe", primary: true, action: async () => {
      if (sub) await api("PUT", "/api/subscriptions/" + sub.id, { name: name.value, color });
      else await api("POST", "/api/subscriptions", { name: name.value, url: link.value, color });
      showCalendar(c);
    } }].filter(Boolean) });
}

// groupShareBox shares a file (kind "file", ref its path) or a calendar ("calendar", its id) with a Homeport group.
function groupShareBox(kind, ref) {
  const box = el("div", { class: "hide" });
  (async () => {
    try {
      const [groups, shares] = await Promise.all([api("GET", "/api/groups"), api("GET", `/api/group-shares?kind=${kind}&ref=${encodeURIComponent(ref)}`)]);
      if (!groups.length) return;
      box.classList.remove("hide");
      const have = new Set(shares.map(s => s.group));
      const sel = el("select", { class: "input" }, groups.filter(g => !have.has(g.name)).map(g => el("option", { value: g.name, text: `${g.name} (${g.members.length})` })));
      const edit = el("input", { type: "checkbox", checked: true });
      box.replaceChildren(el("div", { class: "label", style: { margin: "10px 0 6px" }, text: "Groups (from Homeport): everyone in them, including people who join later" }),
        ...shares.map(s => el("div", { class: "row", style: { alignItems: "center", marginBottom: "6px" } }, icon("user"), el("div", { class: "grow", text: s.group }), el("span", { class: "pill", text: s.edit ? "can edit" : "can view" }),
          el("button", { class: "btn ghost danger", type: "button", onclick: async () => { await api("DELETE", "/api/group-shares/" + s.id).catch(fail); box.replaceWith(groupShareBox(kind, ref)); } }, "Remove"))),
        sel.options.length ? el("div", { class: "row", style: { alignItems: "center" } }, sel, el("label", { class: "check", style: { margin: 0, whiteSpace: "nowrap" } }, edit, "Can edit"),
          el("button", { class: "btn", type: "button", onclick: async () => {
            try { await api("POST", "/api/group-shares", { kind, ref, group: sel.value, edit: edit.checked }); toast("Shared with " + sel.value); box.replaceWith(groupShareBox(kind, ref)); } catch (e) { fail(e); }
          } }, "Share")) : null);
    } catch (_) { /* no groups */ }
  })();
  return box;
}

// --- offline: changes to tasks and notes made without a connection wait here and go when it's back

const OFFQ = {
  list() { try { return JSON.parse(localStorage.getItem("cove.offline") || "[]"); } catch (_) { return []; } },
  add(req) { const l = OFFQ.list(); l.push(req); localStorage.setItem("cove.offline", JSON.stringify(l)); },
  async flush() {
    const l = OFFQ.list();
    if (!l.length || !navigator.onLine) return;
    localStorage.setItem("cove.offline", "[]");
    let sent = 0;
    for (const r of l) {
      try { const res = await fetch(r.url, { method: r.method, headers: { "X-Cove": "1", "Content-Type": r.type || "application/json" }, body: r.body }); if (res.ok) sent++; }
      catch (_) { OFFQ.add(r); }
    }
    if (sent) { toast(`Synced ${sent} change${sent > 1 ? "s" : ""} made offline`); route(); }
  },
};
addEventListener("online", () => OFFQ.flush());

// --- music player: the audio files in the folder, one after another

const player = {
  audio: new Audio(), list: [], i: 0, bar: null,
  play(list, i) {
    this.list = list; this.i = i;
    this.audio.src = files.content(list[i].path);
    this.audio.play().catch(() => {});
    if ("mediaSession" in navigator) {
      navigator.mediaSession.metadata = new MediaMetadata({ title: list[i].name.replace(/\.[^.]+$/, ""), album: baseName(parentOf(list[i].path)) || "Cove" });
      navigator.mediaSession.setActionHandler("previoustrack", () => this.step(-1));
      navigator.mediaSession.setActionHandler("nexttrack", () => this.step(1));
    }
    this.draw();
  },
  step(d) { const n = this.i + d; if (n >= 0 && n < this.list.length) this.play(this.list, n); },
  stop() { this.audio.pause(); this.audio.removeAttribute("src"); this.list = []; this.bar && this.bar.remove(); this.bar = null; document.body.classList.remove("playing"); },
  draw() {
    if (!this.list.length) return;
    document.body.classList.add("playing");
    if (!this.bar) { this.bar = el("div", { class: "player card" }); document.body.append(this.bar); }
    const a = this.audio, it = this.list[this.i];
    const seek = el("input", { type: "range", min: 0, max: 1000, value: a.duration ? 1000 * a.currentTime / a.duration : 0, "aria-label": "Position",
      oninput: e => { if (a.duration) a.currentTime = a.duration * e.target.value / 1000; } });
    const time = el("span", { text: fmtDuration(a.currentTime) + " / " + fmtDuration(a.duration) });
    this.bar.replaceChildren(
      el("button", { class: "btn ghost icon", onclick: () => this.step(-1), disabled: this.i === 0, "aria-label": "Previous" }, icon("prev")),
      el("button", { class: "btn primary icon", onclick: () => { a.paused ? a.play() : a.pause(); }, "aria-label": a.paused ? "Play" : "Pause" }, icon(a.paused ? "play" : "pause")),
      el("button", { class: "btn ghost icon", onclick: () => this.step(1), disabled: this.i >= this.list.length - 1, "aria-label": "Next" }, icon("next")),
      el("div", { class: "info" }, el("div", { class: "n", text: it.name.replace(/\.[^.]+$/, "") }), el("div", { class: "seek" }, seek, time)),
      el("span", { class: "muted", style: { fontSize: "12px" }, text: `${this.i + 1} of ${this.list.length}` }),
      el("button", { class: "btn ghost icon", onclick: () => this.stop(), "aria-label": "Close player" }, icon("close")));
  },
};
player.audio.addEventListener("ended", () => player.i < player.list.length - 1 ? player.step(1) : player.draw());
for (const ev of ["play", "pause", "loadedmetadata"]) player.audio.addEventListener(ev, () => player.draw());
player.audio.addEventListener("timeupdate", () => {
  if (!player.bar) return;
  const a = player.audio, seek = player.bar.querySelector("input[type=range]"), time = player.bar.querySelector(".seek span");
  if (seek && document.activeElement !== seek && a.duration) seek.value = 1000 * a.currentTime / a.duration;
  if (time) time.textContent = fmtDuration(a.currentTime) + " / " + fmtDuration(a.duration);
});

// --- documents in Collabora Online

function openOffice(path) {
  openDocument({
    name: baseName(path),
    open: () => api("POST", "/api/office/open?" + shareQ(), { path }),
    onVersions: () => versionsDialog({ name: baseName(path), path, kind: "doc" }, true),
    onClose: () => route(),
  });
}

boot();
