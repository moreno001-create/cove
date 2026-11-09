// Cove's family pages: wish lists, health, the pantry, the journal, the kitchen display, the home page's connected
// cards (Home Assistant, solar, weather, Jellyfin, memories), and the admin's connections.
"use strict";

// --- wish lists

const WISH = { who: null };

async function showWishes(c) {
  let list;
  try { [list] = await Promise.all([HH.list("wish"), people()]); } catch (e) { return fail(e); }
  const me = S.me.user.name;
  WISH.who = WISH.who || me;
  const tabs = el("div", { class: "chips", style: { padding: 0, marginBottom: "12px" } }, PEOPLE.map(p => el("button", { class: "chip" + (WISH.who === p.name ? " on" : ""), onclick: () => { WISH.who = p.name; showWishes(c); } }, p.name === me ? "My list" : p.display || p.name)));
  const shown = list.filter(w => (w.for || w.owner) === WISH.who);
  const mine = WISH.who === me;
  put(c, pageHead("Wish lists", mine ? "What you'd like. Nobody tells you who's getting what." : "Tick “I'll get it” so nobody buys it twice. " + nameOf(WISH.who) + " doesn't see that, or ideas you add.",
    btn(mine ? "Add a wish" : "Add a gift idea", "plus", () => wishDialog(null, WISH.who, c), "primary")),
    tabs,
    shown.length ? el("div", { class: "card items-list" }, shown.map(w => el("div", {},
      el("div", { class: "ficon k-file" }, icon(w.owner !== w.for ? "star" : "pin")),
      el("div", { class: "grow", style: { cursor: "pointer" }, onclick: () => wishDialog(w, WISH.who, c) },
        el("div", { style: { fontWeight: 600 } }, w.title, w.owner !== w.for ? el("span", { class: "pill", style: { marginLeft: "6px" }, text: "idea from " + nameOf(w.owner) }) : null),
        el("div", { class: "muted", style: { fontSize: "12.5px" } }, [w.price ? money(w.price) : "", w.notes].filter(Boolean).join(" · "),
          w.link ? el("a", { href: w.link, target: "_blank", rel: "noopener", style: { marginLeft: "6px" }, text: "link", onclick: e => e.stopPropagation() }) : null)),
      mine ? null : w.claimedBy ? el("span", { class: "pill" + (w.claimedBy === me ? " money" : ""), text: w.claimedBy === me ? "You're getting it" : nameOf(w.claimedBy) + " is getting it" }) : null,
      mine ? null : (!w.claimedBy || w.claimedBy === me) ? btn(w.claimedBy ? "Not anymore" : "I'll get it", null, async () => {
        try { await HH.save("wish", { ...w, claimedBy: w.claimedBy ? "" : me }); showWishes(c); } catch (e) { fail(e); }
      }) : null))) : emptyCard("star", mine ? "Your list is empty" : nameOf(WISH.who) + "'s list is empty", mine ? "Add things you'd like for birthdays and holidays." : "Add a gift idea: they won't see it."));
}

function wishDialog(w, forWho, c) {
  w = w || { for: forWho };
  const title = input(w.title, { placeholder: "LEGO set, a book, socks…" }), link = input(w.link, { placeholder: "https://…" });
  const price = input(w.price || "", { type: "number", step: "0.01" }), notes = textarea(w.notes, { placeholder: "Size, color…" });
  dialog({ title: w.id ? "Wish" : forWho === S.me.user.name ? "Add a wish" : "Gift idea for " + nameOf(forWho), body: el("div", {}, field("What", title), field("Link", link), el("div", { class: "row" }, field("Price", price)), field("Notes", notes)),
    buttons: [w.id && (w.owner === S.me.user.name) ? { label: "Remove", danger: true, action: async () => { await HH.del("wish", w.id).catch(fail); showWishes(c); } } : null, "grow", { label: "Cancel" },
      { label: "Save", primary: true, action: async () => { await HH.save("wish", { ...w, title: title.value, link: link.value, price: parseFloat(price.value) || 0, notes: notes.value }); showWishes(c); } }].filter(Boolean) });
}

// --- health: today's doses, medicines, records

async function showHealth(c) {
  let today, meds, recs;
  try { [today, meds, recs] = await Promise.all([api("GET", "/api/meds/today"), HH.list("med"), HH.list("health"), people()]); } catch (e) { return fail(e); }
  const whoName = n => PEOPLE.some(p => p.name === n) ? nameOf(n) : n || "—";
  const soon = ymd(addDays(new Date(), 30)), now = ymd(new Date());
  put(c, pageHead("Health", "Medicines with reminders, and when checkups, vaccines and the vet are due.", btn("Record", "plus", () => healthDialog(null, c)), btn("Medicine", "plus", () => medDialog(null, c), "primary")),
    el("h3", { class: "sec", text: "Today" }),
    today.doses.length ? el("div", { class: "card chores" }, today.doses.map(d => el("div", { class: "chore" + (d.taken ? " done" : "") },
      el("input", { type: "checkbox", checked: d.taken, onclick: async e => { try { await api("POST", `/api/meds/${d.med}/taken`, { day: today.day, time: d.time, taken: e.target.checked }); } catch (err) { fail(err); } showHealth(c); } }),
      el("div", { class: "grow" }, el("div", { class: "ctitle", text: `${d.time} · ${d.name}${d.dose ? " (" + d.dose + ")" : ""}` }),
        el("div", { class: "muted", style: { fontSize: "12.5px" }, text: whoName(d.who) + (d.taken ? " · given by " + d.by : "") }))))) :
      el("p", { class: "muted", text: "No doses today." }),
    el("h3", { class: "sec", text: "Medicines" }),
    meds.length ? el("div", { class: "card items-list" }, meds.map(m => el("div", { style: { cursor: "pointer", opacity: m.paused || (m.end && m.end < now) ? 0.55 : 1 }, onclick: () => medDialog(m, c) },
      el("div", { class: "ficon k-file" }, icon("clock")), el("div", { class: "grow" }, el("div", { style: { fontWeight: 600 }, text: m.name + (m.dose ? " · " + m.dose : "") }),
        el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [whoName(m.who), (m.times || []).join(", "), m.days, m.end ? "until " + m.end : ""].filter(Boolean).join(" · ") }))))) :
      emptyCard("clock", "No medicines", "Add one with its times: Cove reminds whoever it's for (and you), and anyone can tick a dose given."),
    el("h3", { class: "sec", text: "Checkups, vaccines, the vet" }),
    recs.length ? el("div", { class: "card items-list" }, recs.sort((a, b) => (a.next || "9999").localeCompare(b.next || "9999")).map(h => el("div", { style: { cursor: "pointer" }, onclick: () => healthDialog(h, c) },
      el("div", { class: "ficon k-file" }, icon(h.type === "vet" ? "star" : "check")),
      el("div", { class: "grow" }, el("div", { style: { fontWeight: 600 }, text: `${whoName(h.who)}: ${h.title}` }),
        el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [h.date && "last " + h.date, h.provider].filter(Boolean).join(" · ") })),
      h.next ? el("span", { class: "pill" + (h.next < now ? " gone" : h.next <= soon ? " warn" : ""), text: "due " + parseYmd(h.next).toLocaleDateString([], { dateStyle: "medium" }) }) : null))) :
      emptyCard("check", "Nothing yet", "Write down vaccines, checkups and vet visits with when they're due next: Cove reminds you two weeks before."));
}

function whoSelect(value) {
  const sel = select([["", "—"], ...PEOPLE.map(p => [p.name, p.display || p.name]), ["*other", "A pet or someone else…"]], PEOPLE.some(p => p.name === value) || !value ? value : "*other");
  const other = input(PEOPLE.some(p => p.name === value) ? "" : value, { placeholder: "Rex the dog" });
  const box = el("div", {}, sel, other);
  const draw = () => { other.style.display = sel.value === "*other" ? "" : "none"; };
  sel.onchange = draw; draw();
  box.value = () => sel.value === "*other" ? other.value.trim() : sel.value;
  return box;
}

function medDialog(m, c) {
  m = m || { times: ["08:00"], days: "daily" };
  const who = whoSelect(m.who), name = input(m.name, { placeholder: "Amoxicillin" }), dose = input(m.dose, { placeholder: "5 ml" });
  const times = input((m.times || []).join(", "), { placeholder: "08:00, 20:00" });
  const days = select([["daily", "Every day"], ["weekdays", "Weekdays"], ["mon,wed,fri", "Mon, Wed, Fri"], ["sun", "Sundays"]], m.days);
  const start = input(m.start, { type: "date" }), end = input(m.end, { type: "date" }), notes = textarea(m.notes);
  const paused = el("input", { type: "checkbox", checked: m.paused }), priv = el("input", { type: "checkbox", checked: m.private });
  dialog({ title: m.id ? m.name : "New medicine", body: el("div", {}, el("div", { class: "row" }, field("For", who), field("Medicine", name), field("Dose", dose)),
    el("div", { class: "row" }, field("Times (24-hour)", times), field("Days", days)), el("div", { class: "row" }, field("From", start), field("Until", end)),
    field("Notes", notes), el("label", { class: "check" }, paused, "Paused"), el("label", { class: "check" }, priv, "Private (only you see it)")),
  buttons: [m.id ? { label: "Delete", danger: true, action: async () => { await HH.del("med", m.id).catch(fail); showHealth(c); } } : null, "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => {
      await HH.save("med", { ...m, who: who.value(), name: name.value, dose: dose.value, times: times.value.split(/[ ,]+/).filter(Boolean).map(t => t.length === 4 ? "0" + t : t), days: days.value,
        start: start.value, end: end.value, notes: notes.value, paused: paused.checked, private: priv.checked });
      showHealth(c);
    } }].filter(Boolean) });
}

function healthDialog(h, c) {
  h = h || { type: "checkup" };
  const who = whoSelect(h.who), type = select([["checkup", "Checkup"], ["vaccine", "Vaccine"], ["dentist", "Dentist"], ["eyes", "Eyes"], ["vet", "Vet"], ["other", "Other"]], h.type);
  const title = input(h.title, { placeholder: "Flu shot, yearly physical…" }), date = input(h.date, { type: "date" }), next = input(h.next, { type: "date" });
  const provider = input(h.provider, { placeholder: "Dr. Lee, Westside Vet" }), notes = textarea(h.notes), priv = el("input", { type: "checkbox", checked: h.private });
  dialog({ title: h.id ? h.title : "New health record", body: el("div", {}, el("div", { class: "row" }, field("Who", who), field("Kind", type)), field("What", title),
    el("div", { class: "row" }, field("When", date), field("Due again", next)), field("Doctor or clinic", provider), field("Notes", notes), el("label", { class: "check" }, priv, "Private (only you see it)")),
  buttons: [h.id ? { label: "Delete", danger: true, action: async () => { await HH.del("health", h.id).catch(fail); showHealth(c); } } : null, "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => {
      await HH.save("health", { ...h, who: who.value(), type: type.value, title: title.value, date: date.value, next: next.value, provider: provider.value, notes: notes.value, private: priv.checked });
      showHealth(c);
    } }].filter(Boolean) });
}

// --- the pantry

async function showPantry(c) {
  let list;
  try { list = await HH.list("pantry"); } catch (e) { return fail(e); }
  const today = ymd(new Date()), soon = ymd(addDays(new Date(), 3));
  const by = {};
  for (const p of list) (by[p.where || "Pantry"] = by[p.where || "Pantry"] || []).push(p);
  const low = list.filter(p => p.qty <= p.min);
  put(c, pageHead("Pantry", "What's in the cupboards. Using the last one puts it on the shopping list.",
    low.length ? btn(`Shop for ${low.length} low`, "check", async () => { try { const r = await api("POST", "/api/shopping", { items: low.map(p => p.name) }); toast(r.added ? `${r.added} added to the shopping list` : "They're on the list already"); } catch (e) { fail(e); } }) : null,
    btn("Scan", "qr", () => barcodeScan(c)), btn("Add", "plus", () => pantryDialog(null, c), "primary")),
    ...(list.length ? Object.keys(by).sort().flatMap(k => [el("h3", { class: "sec", text: k }), el("div", { class: "card items-list" }, by[k].sort((a, b) => a.name.localeCompare(b.name)).map(p => el("div", {},
      el("div", { class: "grow", style: { cursor: "pointer" }, onclick: () => pantryDialog(p, c) }, el("div", { style: { fontWeight: 600 } }, p.name, p.brand ? el("span", { class: "muted", style: { fontWeight: 400 }, text: " · " + p.brand }) : null),
        el("div", { class: "muted", style: { fontSize: "12.5px", color: p.expires && p.expires <= soon ? "var(--danger)" : "" }, text: p.expires ? (p.expires < today ? "went off " : "use by ") + parseYmd(p.expires).toLocaleDateString([], { month: "short", day: "numeric" }) : "" })),
      p.qty <= p.min ? el("span", { class: "pill warn", text: "low" }) : null,
      el("div", { class: "qty" }, el("button", { class: "btn ghost icon", onclick: () => pantryUse(p, -1, c), "aria-label": "Used one" }, icon("minus")),
        el("b", { text: `${+p.qty.toFixed(2)}${p.unit ? " " + p.unit : ""}` }),
        el("button", { class: "btn ghost icon", onclick: () => pantryUse(p, 1, c), "aria-label": "Add one" }, icon("plus"))))))]) :
      [emptyCard("inbox", "The pantry is empty", "Scan barcodes or type what you have; set how few is “low”.")]));
}

async function pantryUse(p, d, c) {
  try { const r = await api("POST", `/api/pantry/${p.id}/use`, { delta: d }); if (r.listed) toast(p.name + " is low: it's on the shopping list"); } catch (e) { fail(e); }
  showPantry(c);
}

function pantryDialog(p, c, prefill) {
  p = p || { qty: 1, min: 0, where: "Pantry", ...(prefill || {}) };
  const name = input(p.name), brand = input(p.brand), qty = input(p.qty, { type: "number", step: "any", min: 0 }), unit = input(p.unit, { placeholder: "cans, lbs, boxes" });
  const min = input(p.min, { type: "number", step: "any", min: 0 }), where = input(p.where, { list: "pantry-where" }), expires = input(p.expires, { type: "date" }), code = input(p.barcode, { inputmode: "numeric" });
  dialog({ title: p.id ? p.name : "Add to the pantry", body: el("div", {}, el("div", { class: "row" }, field("What", name), field("Brand", brand)),
    el("div", { class: "row" }, field("How many", qty), field("Unit", unit), field("Low at", min, "At or below this, it goes on the shopping list")),
    el("div", { class: "row" }, field("Where", where), field("Use by", expires)), field("Barcode", code),
    el("datalist", { id: "pantry-where" }, ["Pantry", "Fridge", "Freezer", "Garage", "Bathroom"].map(v => el("option", { value: v })))),
  buttons: [p.id ? { label: "Remove", danger: true, action: async () => { await HH.del("pantry", p.id).catch(fail); showPantry(c); } } : null, "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => {
      await HH.save("pantry", { ...p, name: name.value, brand: brand.value, qty: parseFloat(qty.value) || 0, unit: unit.value, min: parseFloat(min.value) || 0, where: where.value, expires: expires.value, barcode: code.value });
      showPantry(c);
    } }].filter(Boolean) });
}

// barcodeScan reads a barcode with the camera (where the browser can), else asks for the number.
async function barcodeScan(c) {
  const found = async code => {
    try {
      const r = await api("GET", "/api/pantry/lookup?code=" + encodeURIComponent(code));
      if (r.id) { const all = await HH.list("pantry"); const p = all.find(x => x.id === r.id); if (p) { await pantryUse(p, 1, c); toast("One more " + p.name); return; } }
      pantryDialog(null, c, { name: r.name || "", brand: r.brand || "", barcode: code });
    } catch (e) { fail(e); pantryDialog(null, c, { barcode: code }); }
  };
  if (!("BarcodeDetector" in window) || !navigator.mediaDevices) {
    const code = await ask("Barcode", "This browser can't read barcodes with the camera: type the number under the bars", "", "Look up");
    if (code) found(code);
    return;
  }
  const video = el("video", { playsinline: true, muted: true, style: { width: "100%", borderRadius: "8px", background: "#000" } });
  let stream, stop = false;
  const d = dialog({ title: "Point the camera at a barcode", body: el("div", {}, video), buttons: [{ label: "Type it instead", action: async () => { stop = true; const code = await ask("Barcode", "The number under the bars", "", "Look up"); if (code) found(code); } }, { label: "Cancel" }],
    onClose: () => { stop = true; stream && stream.getTracks().forEach(t => t.stop()); } });
  try {
    stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: "environment" } });
    video.srcObject = stream; await video.play();
    const det = new BarcodeDetector({ formats: ["ean_13", "ean_8", "upc_a", "upc_e", "code_128"] });
    while (!stop) {
      const codes = await det.detect(video).catch(() => []);
      if (codes.length) { stop = true; d.close(); found(codes[0].rawValue); return; }
      await new Promise(r => setTimeout(r, 250));
    }
  } catch (e) { fail(e); d.close(); }
}

// --- the family journal and memories

async function showJournal(c) {
  let list, mem;
  try { [list, mem] = await Promise.all([HH.list("journal"), api("GET", "/api/memories").catch(() => ({ photos: [], journal: [] }))]); } catch (e) { return fail(e); }
  list.sort((a, b) => b.date.localeCompare(a.date) || b.created - a.created);
  put(c, pageHead("My journal", "Just for you: nobody else in the family sees it.", btn("Write", "edit", () => journalDialog(null, c), "primary")),
    memoriesCard(mem),
    list.length ? el("div", { class: "journal" }, list.map(j => el("div", { class: "card jentry" },
      el("div", { class: "jhead" }, el("b", { text: parseYmd(j.date).toLocaleDateString([], { weekday: "long", month: "long", day: "numeric", year: "numeric" }) }),
        el("div", { class: "grow" }), btn("", "edit", () => journalDialog(j, c), "ghost icon")),
      j.text ? el("div", { class: "md" }, markdown(j.text)) : null,
      (j.photos || []).length ? el("div", { class: "pgrid", style: { marginTop: "8px" } }, j.photos.map((p, i) => el("div", { class: "ph", onclick: () => openViewer({
        items: j.photos.map(x => ({ name: baseName(x), path: x, kind: "image" })), index: i, url: x => `/api/journal/${j.id}/photo?path=${encodeURIComponent(x.path)}`,
        thumb: (x, w) => `/api/journal/${j.id}/photo?w=${w}&path=${encodeURIComponent(x.path)}`, download: x => `/api/journal/${j.id}/photo?path=${encodeURIComponent(x.path)}` }) },
        el("img", { src: `/api/journal/${j.id}/photo?w=320&path=${encodeURIComponent(p)}`, loading: "lazy", alt: "" })))) : null))) :
      emptyCard("lock", "Nothing written yet", "A line a day, just for you: what happened, what someone said, a photo. In a year, “on this day” brings it back."));
}

function memoriesCard(mem) {
  if (!mem || (!mem.photos.length && !mem.journal.length)) return null;
  return el("div", { class: "card hcard", style: { marginBottom: "16px" } }, el("div", { class: "hhead" }, icon("clock"), el("h3", { text: "On this day" })),
    mem.photos.length ? el("div", { class: "pgrid" }, mem.photos.slice(0, 12).map((p, i) => el("div", { class: "ph", onclick: () => openViewer({ items: mem.photos, index: i, url: x => files.content(x.path), thumb: (x, w) => files.thumb(x.path, w), download: x => files.download(x.path) }) },
      el("img", { src: files.thumb(p.path, 320), loading: "lazy", alt: "" }), el("span", { class: "by", text: p.years === 1 ? "1 year ago" : p.years + " years ago" })))) : null,
    ...mem.journal.map(j => el("p", {}, el("b", { text: j.date.slice(0, 4) + ": " }), firstLineOf(j.text))));
}
const firstLineOf = s => (s || "").split("\n")[0].slice(0, 200);

function journalDialog(j, c) {
  j = j || { date: ymd(new Date()), photos: [] };
  const date = input(j.date, { type: "date" }), text = textarea(j.text, { placeholder: "What happened today?", style: { minHeight: "160px" } });
  let photos = [...(j.photos || [])];
  const strip = el("div", { class: "scanstrip" });
  const draw = () => put(strip, photos.map(p => el("div", { class: "spage" }, el("img", { src: files.thumb(p, 160), alt: "" }), el("button", { class: "x", type: "button", onclick: () => { photos = photos.filter(x => x !== p); draw(); } }, icon("close")))),
    el("button", { class: "spage add", type: "button", onclick: () => up.click() }, icon("plus"), "Photo"));
  const up = el("input", { type: "file", accept: "image/*", multiple: true, class: "hide", onchange: async e => {
    for (const f of e.target.files) {
      try { const r = await xhrSend("PUT", "/api/files/content?path=" + encPath(`/Journal/${date.value.slice(0, 4)}/${f.name}`), f); photos.push(r.path || `/Journal/${date.value.slice(0, 4)}/${f.name}`); } catch (err) { fail(err); }
    }
    draw();
  } });
  draw();
  dialog({ title: j.id ? "My journal" : "Write in your journal", wide: true, body: el("div", {}, field("Day", date), field("", text), up, strip),
    buttons: [j.id ? { label: "Delete", danger: true, action: async () => { await HH.del("journal", j.id).catch(fail); showJournal(c); } } : null, "grow", { label: "Cancel" },
      { label: "Save", primary: true, action: async () => { await HH.save("journal", { ...j, date: date.value, text: text.value, photos }); showJournal(c); } }].filter(Boolean) });
}

// --- the home page's connected cards

async function homeExtras(box) {
  const cards = [];
  const [dev, watch, mem, meds] = await Promise.all([
    S.me.home ? api("GET", "/api/devices").catch(() => null) : null,
    S.me.jellyfin ? api("GET", "/api/watching").catch(() => null) : null,
    api("GET", "/api/memories").catch(() => null),
    api("GET", "/api/meds/today").catch(() => null)]);
  if (dev) {
    const shown = dev.devices.filter(d => d.home);
    if (dev.weather || dev.energy || shown.length || (dev.people || []).length) cards.push(el("div", { class: "card hcard" }, el("div", { class: "hhead" }, icon("home"), el("h3", { text: "At home" }), el("div", { class: "grow" }), dev.weather ? weatherBadge(dev.weather) : null),
      dev.people && dev.people.length ? el("div", { class: "people" }, dev.people.map(p => el("span", { class: "who" + (p.state === "home" ? " in" : ""), title: p.since ? "since " + fmtDate(p.since * 1000) : "" },
        el("span", { class: "dot" }), p.name, el("small", { text: p.state === "home" ? "home" : p.state === "not_home" ? "away" : p.state })))) : null,
      dev.energy ? energyRow(dev.energy) : null,
      shown.length ? el("div", { class: "devices" }, shown.map(d => deviceButton(d, () => homeExtras(box)))) : null));
  }
  if (meds && meds.doses.some(d => !d.taken)) cards.push(el("div", { class: "card hcard" }, el("div", { class: "hhead" }, icon("clock"), el("h3", { text: "Medicines today" }), el("div", { class: "grow" }), el("a", { href: "#/health", class: "muted", text: "All" })),
    el("div", { class: "flist" }, meds.doses.map(d => el("label", { class: d.taken ? "done" : "" }, el("input", { type: "checkbox", checked: d.taken, onclick: async e => {
      await api("POST", `/api/meds/${d.med}/taken`, { day: meds.day, time: d.time, taken: e.target.checked }).catch(fail);
    } }), el("span", { text: `${d.time} ${d.name}${d.who ? " · " + (PEOPLE ? nameOf(d.who) : d.who) : ""}` }))))));
  if (watch && (watch.resume.length || watch.latest.length)) {
    const row = (items) => el("div", { class: "posters" }, items.map(i => el("a", { class: "poster", href: `${watch.url}/web/#/details?id=${encodeURIComponent(i.Id)}`, target: "_blank", rel: "noopener", title: i.Series ? i.Series + " · " + i.Name : i.Name },
      el("img", { src: "/api/watching/image/" + encodeURIComponent(i.Id), loading: "lazy", alt: "" }), i.progress ? el("div", { class: "prog" }, el("div", { style: { width: i.progress + "%" } })) : null,
      el("span", { text: i.Series || i.Name }))));
    cards.push(el("div", { class: "card hcard wide" }, el("div", { class: "hhead" }, icon("play"), el("h3", { text: watch.resume.length ? "Continue watching" : "New on Jellyfin" }), el("div", { class: "grow" }), el("a", { href: watch.url, target: "_blank", rel: "noopener", class: "muted", text: "Jellyfin" })),
      row(watch.resume.length ? watch.resume : watch.latest), watch.resume.length && watch.latest.length ? el("div", { class: "muted", style: { fontSize: "12.5px", margin: "10px 0 6px" }, text: "Newly added" }) : null,
      watch.resume.length && watch.latest.length ? row(watch.latest.slice(0, 8)) : null));
  }
  const m = memoriesCard(mem);
  if (m) cards.push(m);
  put(box, cards);
}

function weatherBadge(w) {
  const ic = { sunny: "☀️", "clear-night": "🌙", partlycloudy: "⛅", cloudy: "☁️", rainy: "🌧", pouring: "🌧", snowy: "❄️", lightning: "⛈", "lightning-rainy": "⛈", fog: "🌫", windy: "💨" }[w.state] || "🌡";
  return el("span", { class: "weather", text: `${ic} ${Math.round(w.temp)}${w.unit || "°"}` });
}

function energyRow(e) {
  const kw = w => w == null ? "—" : Math.abs(w) >= 1000 ? (w / 1000).toFixed(1) + " kW" : Math.round(w) + " W";
  const cell = (label, value, cls) => value == null ? null : el("div", { class: "estat " + (cls || "") }, el("small", { text: label }), el("b", { text: value }));
  return el("div", { class: "energy" },
    cell("☀️ Solar now", e.solar != null ? kw(e.solar) : null, "sun"),
    cell("Made today", e.today != null ? e.today.toFixed(1) + " kWh" : null),
    cell("🔋 Battery", e.battery != null ? Math.round(e.battery) + "%" : null),
    cell("🏠 Using", e.load != null ? kw(e.load) : null),
    cell(e.grid != null && e.grid < 0 ? "⚡ To the grid" : "⚡ From the grid", e.grid != null ? kw(Math.abs(e.grid)) : null));
}

function deviceButton(d, after) {
  const on = ["on", "open", "unlocked", "playing", "home"].includes(d.state);
  const label = d.domain === "lock" ? (d.state === "locked" ? "Locked" : "Unlocked") : d.domain === "cover" ? (d.state === "open" ? "Open" : d.state === "closed" ? "Closed" : d.state) :
    d.category === "sensor" ? d.state + (d.unit || "") : on ? "On" : "Off";
  return el("button", { class: "device" + (on ? " on" : "") + (d.category === "sensor" ? " sensor" : ""), disabled: d.category === "sensor" || !d.can, onclick: async () => {
    let action = "toggle";
    if (d.domain === "lock") action = d.state === "locked" ? "unlock" : "lock";
    if (d.domain === "cover") action = d.state === "open" ? "close" : "open";
    if (d.careful && !(await confirmBox(`${action[0].toUpperCase() + action.slice(1)} ${d.name}?`, "", action[0].toUpperCase() + action.slice(1)))) return;
    try { await api("POST", "/api/devices", { id: d.id, action }); setTimeout(after, 700); } catch (e) { fail(e); }
  } }, el("small", { text: d.name }), el("b", { text: label }));
}

// --- the kitchen display: a tablet on the wall

const KIOSK = { timer: null, album: localStorage.getItem("cove.kioskAlbum") || "" };

async function showKiosk(c) {
  document.body.classList.add("kiosk");
  const exit = () => { document.body.classList.remove("kiosk"); clearInterval(KIOSK.timer); clearInterval(KIOSK.clock); location.hash = "#/home"; };
  const clock = el("div", { class: "kclock" });
  const tick = () => { const d = new Date(); put(clock, el("b", { text: d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }) }), el("span", { text: d.toLocaleDateString([], { weekday: "long", month: "long", day: "numeric" }) })); };
  tick(); clearInterval(KIOSK.clock); KIOSK.clock = setInterval(tick, 10000);
  const slide = el("div", { class: "kslide" });
  const cols = el("div", { class: "kcols" });
  put(c, el("div", { class: "kiosk-page" }, el("div", { class: "khead" }, clock, el("div", { class: "grow" }), el("div", { class: "kweather" }),
    btn("", "settings", () => kioskSettings(c), "ghost icon"), btn("", "close", exit, "ghost icon")), cols, slide));
  const draw = async () => {
    const today = ymd(new Date()), end = ymd(addDays(new Date(), 7));
    const [h, chores, meals, recipes, dev] = await Promise.all([api("GET", "/api/home?tz=" + encodeURIComponent(tz)), api("GET", "/api/chores").catch(() => null),
      HH.list("meal", `from=${today}&to=${end}`).catch(() => []), HH.list("recipe").catch(() => []), S.me.home ? api("GET", "/api/devices").catch(() => null) : null]);
    const rname = Object.fromEntries(recipes.map(r => [r.id, r.title]));
    const byDay = {};
    for (const o of h.events) { const d = (o.allDay ? o.start : ymd(new Date(o.start))); (byDay[d] = byDay[d] || []).push(o); }
    const days = [...Array(5)].map((_, i) => ymd(addDays(new Date(), i)));
    put(cols,
      el("div", { class: "kcol" }, el("h3", { text: "This week" }), ...days.map(d => el("div", { class: "kday" }, el("div", { class: "kdate", text: d === today ? "Today" : parseYmd(d).toLocaleDateString([], { weekday: "long" }) }),
        ...(byDay[d] || []).map(o => el("div", { class: "kev" }, el("span", { text: o.allDay ? "All day" : new Date(o.start).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }) }), el("b", { text: o.title }))),
        ...meals.filter(m => m.date === d && m.slot === "dinner").map(m => el("div", { class: "kev meal" }, el("span", { text: "Dinner" }), el("b", { text: m.recipe ? rname[m.recipe] || m.text : m.text })))))),
      el("div", { class: "kcol" },
        chores ? el("h3", { text: "Chores today" }) : null,
        chores ? el("div", { class: "kchores" }, chores.chores.filter(x => x.dueToday).map(x => el("button", { class: "kchore" + (x.doneBy ? " done" : ""), onclick: async () => {
          try { if (x.doneBy) await api("DELETE", `/api/chores/done/${x.doneId}`); else await api("POST", `/api/chores/${x.id}/done`, {}); } catch (e) { fail(e); }
          draw();
        } }, el("b", { text: x.title }), el("span", { text: x.doneBy ? "✓ " + nameOf(x.doneBy) : (x.for.length ? x.for.map(nameOf).join(", ") : "Anyone") })))) : null,
        dev && dev.devices.some(d => d.home) ? el("h3", { text: "Home" }) : null,
        dev ? el("div", { class: "devices" }, dev.devices.filter(d => d.home).map(d => deviceButton(d, draw))) : null,
        dev && dev.energy ? energyRow(dev.energy) : null),
      el("div", { class: "kcol" }, el("h3", { text: "On the fridge" }), el("div", { class: "fridge small" }, (h.fridge || []).map(n => fridgeCard(n, null, true)))));
    const kw = c.querySelector(".kweather");
    if (kw) put(kw, dev && dev.weather ? weatherBadge(dev.weather) : null);
  };
  await people();
  draw();
  clearInterval(KIOSK.timer);
  KIOSK.timer = setInterval(() => { if (!location.hash.startsWith("#/kiosk")) { clearInterval(KIOSK.timer); document.body.classList.remove("kiosk"); return; } draw(); }, 5 * 60000);
  // the slideshow
  if (KIOSK.album) {
    try {
      const r = await api("GET", "/api/albums/" + KIOSK.album);
      const pics = r.items.filter(i => i.kind === "image");
      let i = 0;
      const show = () => { if (!pics.length || !document.body.classList.contains("kiosk")) return; put(slide, el("img", { src: albumPhotoURL(KIOSK.album, pics[i++ % pics.length], 1600), alt: "" })); };
      show();
      clearInterval(KIOSK.slides); KIOSK.slides = setInterval(show, 20000);
    } catch (_) {}
  }
  if (document.documentElement.requestFullscreen && !document.fullscreenElement) document.documentElement.requestFullscreen().catch(() => {});
  if (navigator.wakeLock) navigator.wakeLock.request("screen").catch(() => {});
}

async function kioskSettings(c) {
  const albums = await api("GET", "/api/albums").catch(() => []);
  const sel = select([["", "No slideshow"], ...albums.map(a => [a.id, a.name])], KIOSK.album);
  dialog({ title: "Kitchen display", body: el("div", {}, field("Photo slideshow from", sel),
    el("p", { class: "muted", style: { fontSize: "12.5px" }, text: "Open Cove on the tablet, sign in, then go to Kitchen display. It refreshes itself; tap a chore to tick it." })),
    buttons: [{ label: "Cancel" }, { label: "Save", primary: true, action: () => { KIOSK.album = sel.value; try { localStorage.setItem("cove.kioskAlbum", sel.value); } catch (_) {} showKiosk(c); } }] });
}

// --- Ask Cove: speaking a question

function micButton(target, then) {
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!SR) return null;
  const b = el("button", { class: "btn ghost icon mic", type: "button", title: "Ask by voice (the browser's speech recognition may use Apple's or Google's servers)", "aria-label": "Speak" }, icon("audio"));
  b.onclick = () => {
    const r = new SR();
    r.lang = navigator.language || "en-US"; r.interimResults = true;
    b.classList.add("on");
    r.onresult = e => { target.value = [...e.results].map(x => x[0].transcript).join(""); if (e.results[e.results.length - 1].isFinal) then(); };
    r.onend = () => b.classList.remove("on");
    r.onerror = () => b.classList.remove("on");
    r.start();
  };
  return b;
}

// --- admin: connections

function connectAdmin() {
  const box = el("div", { class: "card section" }, el("h3", { text: "Home connections" }), el("p", { class: "muted", text: "Loading…" }));
  Promise.all([api("GET", "/api/admin/ha"), api("GET", "/api/admin/jellyfin"), api("GET", "/api/admin/offsite")]).then(([ha, jf, off]) => {
    // Home Assistant
    const hurl = input(ha.config.url, { placeholder: "http://host.docker.internal:8123" });
    const htok = input("", { type: "password", placeholder: ha.tokenSet ? "Saved (type a new one to change it)" : "A long-lived access token from your Home Assistant profile", autocomplete: "off" });
    let ents = ha.config.entities || [];
    const entBox = el("div", { class: "items-list", style: { maxHeight: "300px", overflow: "auto" } });
    const maps = {};
    const mapBox = el("div", { class: "row", style: { flexWrap: "wrap" } });
    const drawMaps = all => put(mapBox, [["solar", "Solar power now"], ["today", "Solar today (kWh)"], ["battery", "Battery %"], ["load", "House use now"], ["grid", "Grid now (+ from, − to)"], ["weather", "Weather"]].map(([k, l]) => {
      maps[k] = select([["", "—"], ...all.filter(e => k === "weather" ? e.id.startsWith("weather.") : e.id.startsWith("sensor.")).map(e => [e.id, `${e.name || e.id} (${e.state}${e.unit ? " " + e.unit : ""})`])], ha.config[k] || "");
      return field(l, maps[k]);
    }));
    const drawEnts = all => {
      const pickable = all.filter(e => /^(light|switch|fan|lock|cover|input_boolean|scene|script|sensor|binary_sensor|climate|media_player)\./.test(e.id));
      put(entBox, pickable.map(e => {
        const cur = ents.find(x => x.id === e.id);
        const show = el("input", { type: "checkbox", checked: !!cur, onchange: () => { if (show.checked) ents.push({ id: e.id, label: "", home: true }); else ents = ents.filter(x => x.id !== e.id); } });
        const home = el("input", { type: "checkbox", checked: cur ? cur.home : true, onchange: () => { const x = ents.find(y => y.id === e.id); if (x) x.home = home.checked; } });
        return el("div", {}, show, el("div", { class: "grow" }, el("div", { text: e.name || e.id }), el("div", { class: "muted", style: { fontSize: "12px" }, text: `${e.id} · ${e.state}${e.unit ? " " + e.unit : ""}` })),
          el("label", { class: "check", style: { margin: 0, fontSize: "12.5px" } }, home, "Home page"));
      }));
    };
    const loadEnts = async () => { try { const all = await api("GET", "/api/admin/ha/entities"); drawEnts(all); drawMaps(all); } catch (e) { put(entBox, el("p", { class: "muted", text: e.message })); } };
    if (ha.tokenSet && ha.config.url) loadEnts();
    // Jellyfin
    const jurl = input(jf.config.url, { placeholder: "http://host.docker.internal:8096" }), jpub = input(jf.config.public, { placeholder: "https://jellyfin.example.com (where people open it)" });
    const jkey = input("", { type: "password", placeholder: jf.keySet ? "Saved (type a new one to change it)" : "An API key from Jellyfin's Dashboard → API Keys", autocomplete: "off" });
    // off-site
    const orepo = input(off.config.repo, { placeholder: "s3:s3.us-west-002.backblazeb2.com/my-bucket/cove, sftp:user@host:/backups/cove, rest:https://…" });
    const opass = input("", { type: "password", placeholder: off.passwordSet ? "Saved (type a new one to change it; keep a copy somewhere safe)" : "A password for the backup (keep a copy somewhere safe!)", autocomplete: "off" });
    const oenv = textarea("", { placeholder: off.envKeys.length ? "Saved: " + off.envKeys.join(", ") + " (type new lines to replace)" : "AWS_ACCESS_KEY_ID=…\nAWS_SECRET_ACCESS_KEY=…", style: { minHeight: "70px", fontFamily: "monospace" } });
    const ohour = select([...Array(24)].map((_, h) => [h, new Date(2000, 0, 1, h).toLocaleTimeString([], { hour: "numeric" })]), off.config.hour);
    const st = off.status;
    put(box, el("h3", { text: "Home Assistant" }),
      el("p", { class: "muted", style: { fontSize: "13px" }, text: "Lights, locks, the garage door and sensors on the home page and the kitchen display, and Ask Cove can switch them. Kids can't open locks or doors." }),
      el("div", { class: "row" }, field("Address", hurl), field("Token", htok)),
      btn("Save and load devices", "refresh", async () => { try { await api("PUT", "/api/admin/ha", { ...ha.config, url: hurl.value, token: htok.value, entities: ents }); ha.tokenSet = true; await loadEnts(); toast("Connected"); } catch (e) { fail(e); } }),
      el("div", { class: "label", style: { marginTop: "14px" }, text: "What Cove shows" }), entBox,
      el("div", { class: "label", style: { marginTop: "14px" }, text: "Solar and weather (from Home Assistant's EG4 or other integration)" }), mapBox,
      btn("Save", null, async () => {
        const cfg = { url: hurl.value, token: htok.value, entities: ents };
        for (const k in maps) cfg[k] = maps[k].value;
        try { await api("PUT", "/api/admin/ha", cfg); S.me = await api("GET", "/api/me"); toast("Saved"); } catch (e) { fail(e); }
      }, "primary"),
      el("hr"), el("h3", { text: "Jellyfin" }), el("p", { class: "muted", style: { fontSize: "13px" }, text: "“Continue watching” on the home page, for people whose Jellyfin name matches their Cove name." }),
      el("div", { class: "row" }, field("Address Cove uses", jurl), field("Address people open", jpub)), field("API key", jkey),
      btn("Save", null, async () => { try { await api("PUT", "/api/admin/jellyfin", { url: jurl.value, public: jpub.value, key: jkey.value }); S.me = await api("GET", "/api/me"); toast("Saved"); } catch (e) { fail(e); } }, "primary"),
      el("hr"), el("h3", { text: "Off-site backup" }),
      el("p", { class: "muted", style: { fontSize: "13px" }, text: off.restic ? "Every night, Cove's files and database snapshots go to storage outside your home (Backblaze B2, S3, another computer), encrypted with restic. Keeps 14 days, 8 weeks and 12 months." : "restic isn't in this Cove image." }),
      field("Where (restic repository)", orepo), field("Backup password", opass), field("Keys for the storage (one per line)", oenv), field("Each night at", ohour),
      el("p", { class: "muted", style: { fontSize: "12.5px" }, text: st.last ? `Last backup ${fmtDate(st.last * 1000)}: ${st.ok ? `worked · ${st.count} kept · ${st.size}` : st.error}` : "No backup yet." }),
      el("div", { class: "row" }, btn("Save", null, async () => { try { await api("PUT", "/api/admin/offsite", { repo: orepo.value, password: opass.value, env: oenv.value, hour: +ohour.value }); toast("Saved"); } catch (e) { fail(e); } }, "primary"),
        btn("Back up now", "upload", async () => { try { await api("POST", "/api/admin/offsite/run"); toast("Started: you'll get a notification when it's done"); } catch (e) { fail(e); } }),
        btn("Check it can be read back", "check", async () => { try { await api("POST", "/api/admin/offsite/check"); toast("Checking: you'll get a notification when it's done (it also runs on the 1st of each month)"); } catch (e) { fail(e); } })));
  }).catch(e => put(box, el("p", { text: e.message })));
  return box;
}

// --- settings: the morning briefing

function briefingSettings(st, save) {
  const on = el("input", { type: "checkbox", checked: !!st.briefing });
  const at = input(st.briefing || "07:00", { type: "time", style: { width: "auto" } });
  const preview = el("pre", { class: "brief hide" });
  const store = async () => { try { await save({ ...st, briefing: on.checked ? at.value : "" }); toast(on.checked ? "Briefing at " + at.value : "Briefing off"); } catch (e) { fail(e); } };
  on.onchange = store; at.onchange = () => on.checked && store();
  return el("div", { class: "card section" }, el("h3", { text: "Morning briefing" }),
    el("p", { class: "muted", text: "One notification each morning: the weather, today's events, dinner, your chores, medicines, bills due and countdowns." }),
    el("div", { class: "row", style: { alignItems: "center" } }, el("label", { class: "check", style: { margin: 0 } }, on, "Send it at"), at,
      btn("Show today's", null, async () => { try { const r = await api("GET", "/api/briefing"); preview.textContent = r.text; preview.classList.remove("hide"); } catch (e) { fail(e); } }, "ghost")),
    preview);
}
