// Cove: cooking mode, savings goals, carpools and rides, the year in review, and the insurance list.
"use strict";

// --- cooking mode: one step at a time, big, with timers, and the screen stays on

function cookMode(r) {
  const steps = (r.steps || "").split(/\n+/).map(s => s.replace(/^\s*(\d+[.)]|[-*•])\s*/, "").trim()).filter(Boolean);
  if (!steps.length) return toast("This recipe has no steps written down", true);
  let i = 0, lock = null;
  const timers = el("div", { class: "cooktimers" });
  const stage = el("div", { class: "cookstep" });
  const counter = el("div", { class: "cookcount" });
  const draw = () => {
    counter.textContent = `Step ${i + 1} of ${steps.length}`;
    const text = steps[i];
    const found = [...text.matchAll(/(\d+)\s*(?:-|to)?\s*(\d+)?\s*(minutes?|mins?|hours?|hrs?|seconds?|secs?)\b/gi)];
    put(stage, el("p", { text }), found.length ? el("div", { class: "row", style: { gap: "8px", flexWrap: "wrap", justifyContent: "center" } }, found.map(m => {
      const n = +(m[2] || m[1]), unit = m[3].toLowerCase()[0];
      const secs = unit === "h" ? n * 3600 : unit === "s" ? n : n * 60;
      return btn(`⏱ ${m[0]}`, null, () => startTimer(m[0], secs));
    })) : null);
    prev.disabled = i === 0;
    put(next, i === steps.length - 1 ? "Done" : "Next ›");
  };
  const startTimer = (label, secs) => {
    const end = Date.now() + secs * 1000;
    const out = el("b");
    const row = el("div", { class: "cooktimer" }, el("span", { text: label }), out, el("button", { class: "btn ghost icon", onclick: () => { clearInterval(t); row.remove(); } }, icon("close")));
    timers.append(row);
    const t = setInterval(() => {
      const left = Math.max(0, Math.round((end - Date.now()) / 1000));
      out.textContent = `${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;
      if (!left) {
        clearInterval(t);
        row.classList.add("ring");
        try { const a = new AudioContext(), o = a.createOscillator(); o.connect(a.destination); o.frequency.value = 880; o.start(); setTimeout(() => o.stop(), 1500); } catch (_) {}
        if (navigator.vibrate) navigator.vibrate([400, 200, 400]);
        toast("⏱ " + label + " is up!");
      }
    }, 250);
  };
  const prev = el("button", { class: "btn", onclick: () => { if (i > 0) { i--; draw(); } } }, "‹ Back");
  const next = el("button", { class: "btn primary", onclick: () => { if (i < steps.length - 1) { i++; draw(); } else close(); } });
  const close = () => { view.remove(); document.removeEventListener("keydown", key); lock && lock.release && lock.release(); };
  const key = e => { if (e.key === "ArrowRight" || e.key === " ") next.click(); if (e.key === "ArrowLeft") prev.click(); if (e.key === "Escape") close(); };
  const view = el("div", { class: "cook" },
    el("div", { class: "cookbar" }, el("b", { text: r.title }), counter, el("div", { class: "grow" }),
      el("button", { class: "btn", onclick: () => { const box = el("ul", {}, (r.ingredients || []).map(x => el("li", { text: x }))); dialog({ title: "Ingredients", body: box, buttons: [{ label: "Close", primary: true }] }); } }, "Ingredients"),
      el("button", { class: "btn ghost icon", onclick: close, "aria-label": "Close" }, icon("close"))),
    stage, timers, el("div", { class: "cooknav" }, prev, next));
  document.body.append(view);
  document.addEventListener("keydown", key);
  let touchX = null;
  stage.addEventListener("touchstart", e => { touchX = e.touches[0].clientX; }, { passive: true });
  stage.addEventListener("touchend", e => { if (touchX == null) return; const dx = e.changedTouches[0].clientX - touchX; if (Math.abs(dx) > 60) (dx < 0 ? next : prev).click(); touchX = null; });
  if (navigator.wakeLock) navigator.wakeLock.request("screen").then(l => { lock = l; }).catch(() => {});
  draw();
}

// --- savings goals (on the chores page)

async function goalsSection(c) {
  let goals;
  try { [goals] = await Promise.all([HH.list("goal"), people()]); } catch (e) { return null; }
  const kid = isKid(), me = S.me.user.name;
  const mine = kid ? goals.filter(g => g.kid === me) : goals;
  return el("div", {},
    el("h3", { class: "sec", text: "Saving up" }),
    mine.length ? el("div", { class: "rgrid rewards" }, mine.map(g => {
      const pct = Math.min(100, Math.round(100 * g.saved / g.target));
      return el("div", { class: "card goal" }, el("div", { class: "row", style: { alignItems: "center" } }, el("div", { class: "remoji", text: g.emoji || "🎯" }),
          el("div", { class: "grow" }, el("b", { text: g.title }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: `${nameOf(g.kid)} · ${cents(g.saved)} of ${cents(g.target)}${pct >= 100 ? " 🎉" : ""}` })),
          el("button", { class: "btn ghost icon", onclick: () => goalDialog(g, c), "aria-label": "Edit" }, icon("edit"))),
        el("div", { class: "usage big" }, el("div", { style: { width: pct + "%" } })),
        kid ? null : el("div", { class: "row", style: { marginTop: "8px", gap: "6px" } }, ...[100, 500, 1000].map(v => btn("+" + cents(v), null, async () => { await api("POST", `/api/goals/${g.id}/add`, { cents: v }).catch(fail); showChores(c); })),
          btn("Other", null, async () => { const a = await ask("Put toward " + g.title, "Amount ($)", "", "Add"); if (a) { await api("POST", `/api/goals/${g.id}/add`, { cents: Math.round(parseFloat(a) * 100) }).catch(fail); showChores(c); } }, "ghost")));
    })) : el("p", { class: "muted", text: kid ? "Saving up for something? Set a goal and watch it fill up." : "Kids can save allowance toward goals: a bike, a game, a trip." }),
    btn("New goal", "plus", () => goalDialog(null, c), "ghost"));
}

function goalDialog(g, c) {
  g = g || { kid: isKid() ? S.me.user.name : (PEOPLE.find(p => p.name !== S.me.user.name) || PEOPLE[0]).name, emoji: "🎯" };
  const title = input(g.title, { placeholder: "New bike" }), target = input(g.target ? (g.target / 100).toFixed(2) : "", { type: "number", step: "0.01", placeholder: "120.00" });
  const emoji = input(g.emoji, { style: { width: "80px" } }), kid = select(PEOPLE.map(p => [p.name, p.display || p.name]), g.kid, { disabled: isKid() });
  dialog({ title: g.id ? g.title : "New savings goal", body: el("div", {}, field("Saving for", title), el("div", { class: "row" }, field("It costs ($)", target), field("Emoji", emoji), field("Whose", kid))),
    buttons: [g.id && (!isKid() || g.owner === S.me.user.name) ? { label: "Remove", danger: true, action: async () => { await HH.del("goal", g.id).catch(fail); showChores(c); } } : null, "grow", { label: "Cancel" },
      { label: "Save", primary: true, action: async () => { await HH.save("goal", { ...g, title: title.value, target: Math.round(parseFloat(target.value) * 100) || 0, emoji: emoji.value, kid: kid.value }); showChores(c); } }].filter(Boolean) });
}

// --- carpools and rides

async function showRides(c) {
  let list;
  try { [list] = await Promise.all([HH.list("ride"), people()]); } catch (e) { return fail(e); }
  const today = ymd(new Date());
  const upcoming = list.filter(x => x.date >= today).sort((a, b) => (a.date + a.time).localeCompare(b.date + b.time));
  const byDay = {};
  for (const x of upcoming) (byDay[x.date] = byDay[x.date] || []).push(x);
  put(c, pageHead("Rides", "Who's driving whom: the driver is reminded the evening before and an hour before.", btn("Add a ride", "plus", () => rideDialog(null, c), "primary")),
    upcoming.length ? Object.entries(byDay).map(([d, rides]) => el("div", {}, el("h3", { class: "sec", text: d === today ? "Today" : parseYmd(d).toLocaleDateString([], { weekday: "long", month: "short", day: "numeric" }) }),
      el("div", { class: "card items-list" }, rides.map(x => el("div", { style: { cursor: "pointer" }, onclick: () => rideDialog(x, c) },
        el("div", { class: "ficon k-file" }, icon("place")),
        el("div", { class: "grow" }, el("div", { style: { fontWeight: 600 }, text: (x.time ? fmtTime(x.time) + " · " : "") + x.what }),
          el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [x.riders.length ? x.riders.join(", ") : "", x.to ? "to " + x.to : "", x.back && x.back !== "none" && x.back !== x.driver ? "home: " + nameOf(x.back) : x.back === "none" ? "no ride home" : ""].filter(Boolean).join(" · ") })),
        el("span", { class: "pill" + (x.driver === S.me.user.name ? " money" : ""), text: x.driver ? "🚗 " + nameOf(x.driver) : "Needs a driver" })))))) :
      emptyCard("place", "No rides coming up", "Put down practices, lessons and playdates: who drives there, who drives home."));
}
const fmtTime = t => { const [h, m] = t.split(":").map(Number); return new Date(2000, 0, 1, h, m).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }); };

function rideDialog(x, c) {
  x = x || { date: ymd(new Date()), time: "", riders: [], driver: S.me.user.name };
  const what = input(x.what, { placeholder: "Soccer practice" }), date = input(x.date, { type: "date" }), time = input(x.time, { type: "time" });
  const driver = input(x.driver ? nameOf(x.driver) : "", { list: "drivers", placeholder: "Who drives there" }), back = input(x.back === "none" ? "No ride home" : x.back ? nameOf(x.back) : "", { list: "drivers", placeholder: "Who drives home (empty: the same)" });
  const riders = input((x.riders || []).join(", "), { placeholder: "Sam, Leo" }), from = input(x.from, { placeholder: "Home" }), to = input(x.to, { placeholder: "Westside Park" }), notes = textarea(x.notes);
  const asUser = v => { const p = PEOPLE.find(p => (p.display || p.name).toLowerCase() === v.trim().toLowerCase() || p.name === v.trim()); return p ? p.name : v.trim(); };
  dialog({ title: x.id ? x.what : "New ride", body: el("div", {}, field("What", what), el("div", { class: "row" }, field("Day", date), field("Time", time)),
    el("div", { class: "row" }, field("Driving there", driver), field("Driving home", back)), field("Who's riding", riders), el("div", { class: "row" }, field("From", from), field("To", to)), field("Notes", notes),
    el("datalist", { id: "drivers" }, [...PEOPLE.map(p => p.display || p.name), "No ride home"].map(v => el("option", { value: v })))),
  buttons: [x.id ? { label: "Delete", danger: true, action: async () => { await HH.del("ride", x.id).catch(fail); showRides(c); } } : null, "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => {
      const b = back.value.trim().toLowerCase() === "no ride home" ? "none" : asUser(back.value);
      await HH.save("ride", { ...x, what: what.value, date: date.value, time: time.value, driver: asUser(driver.value), back: b, riders: riders.value.split(",").map(s => s.trim()).filter(Boolean), from: from.value, to: to.value, notes: notes.value });
      showRides(c);
    } }].filter(Boolean) });
}

// --- the year in review

async function showYear(c, rest) {
  const year = +(rest && rest[0]) || (new Date().getMonth() >= 10 ? new Date().getFullYear() : new Date().getFullYear() - 1);
  put(c, el("p", { class: "muted", text: "Gathering the year…" }));
  const [photos, trips, journal, chores, meals, recipes] = await Promise.all([api("GET", "/api/photos").catch(() => []), HH.list("trip").catch(() => []),
    HH.list("journal").catch(() => []), api("GET", "/api/chores").catch(() => null), HH.list("meal", `from=${year}-01-01&to=${year}-12-31`).catch(() => []), HH.list("recipe").catch(() => []), people()]);
  const inYear = photos.filter(p => new Date(p.taken * 1000).getFullYear() === year && p.kind === "image");
  // a few from each month, spread out, preferring described ones
  const picks = [];
  for (let m = 0; m < 12; m++) {
    const month = inYear.filter(p => new Date(p.taken * 1000).getMonth() === m);
    const step = Math.max(1, Math.floor(month.length / 3));
    for (let i = 0; i < month.length && picks.filter(p => new Date(p.taken * 1000).getMonth() === m).length < 3; i += step) picks.push(month[i]);
  }
  const yTrips = trips.filter(t => t.start.startsWith(String(year)));
  const yJournal = journal.filter(j => j.date.startsWith(String(year)));
  const counts = {};
  for (const m of meals) { const k = m.recipe ? (recipes.find(r => r.id === m.recipe) || {}).title || m.text : m.text; if (k) counts[k] = (counts[k] || 0) + 1; }
  const favorite = Object.entries(counts).sort((a, b) => b[1] - a[1])[0];
  const champ = chores && chores.people.slice().sort((a, b) => b.earned - a.earned)[0];
  const stat = (n, label) => el("div", { class: "card stat" }, el("b", { text: n }), el("small", { text: label }));
  put(c, el("div", { class: "year" },
    el("div", { class: "bar noprint" }, btn("‹ " + (year - 1), null, () => { location.hash = "#/year/" + (year - 1); }, "ghost"), el("div", { class: "grow" }),
      year < new Date().getFullYear() ? btn((year + 1) + " ›", null, () => { location.hash = "#/year/" + (year + 1); }, "ghost") : null, btn("Print", "download", () => print())),
    el("h1", { class: "yearhead", text: `Our ${year}` }),
    el("div", { class: "stats" }, stat(inYear.length.toLocaleString(), "photos"), stat(yTrips.length, yTrips.length === 1 ? "trip" : "trips"),
      stat(meals.length, "meals planned"), stat(yJournal.length, "journal days (yours)")),
    picks.length ? el("div", {}, el("h2", { text: "The year in pictures" }), el("div", { class: "yeargrid" }, picks.map((p, i) => el("figure", { onclick: () => openViewer({ items: picks, index: i, url: x => files.content(x.path), thumb: (x, w) => files.thumb(x.path, w), download: x => files.download(x.path) }) },
      el("img", { src: files.thumb(p.path, 480), loading: "lazy", alt: p.caption || "" }), el("figcaption", { text: new Date(p.taken * 1000).toLocaleDateString([], { month: "long" }) }))))) : null,
    yTrips.length ? el("div", {}, el("h2", { text: "Where we went" }), el("ul", {}, yTrips.map(t => el("li", {}, el("b", { text: t.name }), ` · ${t.where || ""} · ${fmtRange(t.start, t.end)}`)))) : null,
    favorite ? el("p", {}, el("b", { text: "Most-made dinner: " }), `${favorite[0]} (${favorite[1]} times)`) : null,
    champ && champ.earned ? el("p", {}, el("b", { text: "Chore champion: " }), champ.who) : null,
    yJournal.length ? el("div", {}, el("h2", { text: "From your journal" }), el("p", { class: "muted", style: { fontSize: "12.5px" }, text: "Only you see these." }),
      yJournal.slice(-6).map(j => el("blockquote", {}, el("b", { text: parseYmd(j.date).toLocaleDateString([], { month: "long", day: "numeric" }) + ": " }), firstLineOf(j.text).replace(/[*_`#>]/g, "")))) : null));
}

// --- the insurance list: everything in Home records, with prices and receipts, to print or save as PDF

async function showInventory(c) {
  let list;
  try { [list] = await Promise.all([HH.list("record"), people()]); } catch (e) { return fail(e); }
  const things = list.filter(r => r.category !== "document");
  const total = things.reduce((a, r) => a + (r.price || 0), 0);
  const photoOf = r => (r.files || []).find(p => /\.(jpe?g|png|heic|webp)$/i.test(p));
  put(c, el("div", { class: "inventory" },
    el("div", { class: "bar noprint" }, el("div", { class: "page-title", style: { margin: 0 }, text: "Insurance list" }), el("div", { class: "grow" }), btn("Print or save as PDF", "download", () => print(), "primary")),
    el("h1", { text: "Home inventory" }),
    el("p", { text: `${things.length} item${things.length === 1 ? "" : "s"} · total ${money(total)} · as of ${new Date().toLocaleDateString([], { dateStyle: "long" })}` }),
    el("table", {}, el("thead", {}, el("tr", {}, ["", "Item", "Brand and model", "Serial", "Bought", "Price", "Receipt"].map(h => el("th", { text: h })))),
      el("tbody", {}, things.map(r => el("tr", {},
        el("td", {}, photoOf(r) && r.owner === S.me.user.name ? el("img", { src: files.thumb(photoOf(r), 160), alt: "" }) : null),
        el("td", {}, el("b", { text: r.name }), r.where ? el("div", { class: "muted", text: r.where }) : null),
        el("td", { text: [r.brand, r.model].filter(Boolean).join(" ") }), el("td", { text: r.serial || "" }),
        el("td", { text: r.bought || "" }), el("td", { text: r.price ? money(r.price) : "" }),
        el("td", { text: (r.files || []).length ? `${r.files.length} file${r.files.length > 1 ? "s" : ""}` : "" }))))),
    el("p", { class: "muted noprint", style: { fontSize: "12.5px" }, text: "Add things in Home records, with a photo and the receipt attached. Documents and IDs aren't on this list. Keep a copy somewhere outside the house." })));
}
