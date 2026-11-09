// Cove's helpers: receipts into spending, sorting mail, recipes from a link, the rewards shop, maintenance, the
// guest Wi-Fi card, bringing in photos, the welcome tour, and the menu (grouped, folding, pages hidden on request).
"use strict";

// --- a receipt photo becomes spending

function snapReceipt() {
  const pick = el("input", { type: "file", accept: "image/*,application/pdf", capture: "environment", class: "hide", onchange: async e => {
    const f = e.target.files[0];
    pick.remove();
    if (!f) return;
    toast("Reading the receipt…");
    try {
      const r = await xhrSend("POST", "/api/receipts", f);
      await people();
      if (!r.read) toast("Couldn't read the receipt's words: fill it in yourself (the photo is kept)", true);
      expenseDialog({ title: r.store, amount: r.amount, date: r.date, category: r.category, paidBy: S.me.user.name, split: [], receipt: r.path }, location.hash.startsWith("#/budget") ? $("content") : null);
    } catch (err) { fail(err); }
  } });
  document.body.append(pick);
  pick.click();
}

// --- sorting a scanned letter or bill

async function sortLetter(p) {
  toast("Reading the letter…");
  let r;
  try { r = await api("POST", "/api/mail/sort", { path: p }); } catch (e) { return fail(e); }
  const g = r.suggestion;
  await people();
  const folder = input(g.folder), name = input(g.name || baseName(p).replace(/\.[^.]+$/, ""));
  const move = el("input", { type: "checkbox", checked: true });
  const bill = el("input", { type: "checkbox", checked: g.kind === "bill" && g.amount > 0 && !!g.due });
  const cal = el("input", { type: "checkbox", checked: !!g.due });
  dialog({ title: g.title || "Sort this letter", wide: true, body: el("div", {},
    el("div", { class: "row", style: { gap: "6px", flexWrap: "wrap", marginBottom: "8px" } }, el("span", { class: "pill", text: g.kind || "letter" }), g.from ? el("span", { class: "pill", text: "From " + g.from }) : null,
      g.due ? el("span", { class: "pill warn", text: "By " + parseYmd(g.due).toLocaleDateString([], { dateStyle: "medium" }) }) : null, g.amount ? el("span", { class: "pill", text: money(g.amount) }) : null),
    el("p", { text: g.summary }),
    el("label", { class: "check" }, move, "File it"), el("div", { class: "row" }, field("Folder", folder), field("Name", name)),
    g.amount > 0 && g.due ? el("label", { class: "check" }, bill, `Add a bill: ${g.from || g.title}, ${money(g.amount)} due ${g.due}`) : null,
    g.due ? el("label", { class: "check" }, cal, `Put “${g.title}” on my calendar on ${g.due}`) : null,
    el("p", { class: "muted", style: { fontSize: "12px" }, text: "Read by your home AI: check it before you rely on it." })),
  buttons: [{ label: "Cancel" }, { label: "Do it", primary: true, action: async () => {
    const done = [];
    if (move.checked) { const m = await api("POST", "/api/mail/file", { path: p, folder: folder.value, name: name.value }); done.push("filed in " + parentOf(m.path)); }
    if (bill.checked && g.amount > 0 && g.due) { await HH.save("bill", { name: g.from || g.title, amount: g.amount, repeat: "once", date: g.due, category: "Bills", payer: S.me.user.name }); done.push("bill added"); }
    if (cal.checked && g.due) {
      const cals = await api("GET", "/api/calendars?kind=events");
      const k = cals.find(x => x.owner === S.me.user.name && !x.subscription) || cals[0];
      await api("POST", "/api/events", { cal: k.id, title: g.title || "Mail", allDay: true, start: g.due, end: ymd(addDays(parseYmd(g.due), 1)), tz, notes: g.summary });
      done.push("on your calendar");
    }
    toast(done.length ? "Done: " + done.join(", ") : "Nothing to do");
    if (location.hash.startsWith("#/files")) refreshSoon();
  } }] });
}

// --- recipes from a link

async function recipeFromLink(c) {
  const url = await ask("Recipe from a web page", "The recipe's address", "", "Bring it in");
  if (!url) return;
  toast("Reading the page…");
  try { const r = await api("POST", "/api/recipes/import", { url }); recipeDialog(r, c); } catch (e) { fail(e); }
}

// --- the rewards shop (on the chores page)

async function rewardsSection(c) {
  let shop;
  try { shop = await api("GET", "/api/rewards"); } catch (e) { return null; }
  const me = S.me.user.name, kid = isKid();
  const waiting = shop.claims.filter(x => x.status === "waiting");
  return el("div", {},
    el("h3", { class: "sec", text: `Rewards shop · ${shop.mine} points to spend` }),
    shop.rewards.length ? el("div", { class: "rgrid rewards" }, shop.rewards.map(rw => el("div", { class: "card reward" },
      el("div", { class: "remoji", text: rw.emoji || "🎁" }), el("div", { class: "grow" }, el("b", { text: rw.title }), el("div", { class: "muted", text: rw.points + " points" })),
      el("button", { class: "btn" + (shop.mine >= rw.points ? " primary" : ""), disabled: shop.mine < rw.points, onclick: async () => {
        try { await api("POST", `/api/rewards/${rw.id}/claim`); toast("Asked! A grown-up will say yes or no."); showChores(c); } catch (e) { fail(e); }
      } }, "Get it"),
      kid ? null : el("button", { class: "btn ghost icon", onclick: () => rewardDialog(rw, c), "aria-label": "Edit" }, icon("edit"))))) :
      el("p", { class: "muted", text: kid ? "No rewards yet: ask a grown-up to add some." : "Add rewards kids can spend chore points on: screen time, picking dinner, a later bedtime." }),
    kid ? null : btn("Add a reward", "plus", () => rewardDialog(null, c), "ghost"),
    waiting.length && !kid ? el("div", { class: "card items-list", style: { marginTop: "10px" } }, waiting.map(x => el("div", {},
      el("div", { class: "grow" }, el("b", { text: x.who }), ` wants ${x.title} (${x.points} points)`),
      btn("Yes", "check", async () => { await api("POST", `/api/reward-claims/${x.id}`, { yes: true }).catch(fail); showChores(c); }, "primary"),
      btn("Not now", null, async () => { await api("POST", `/api/reward-claims/${x.id}`, { yes: false }).catch(fail); showChores(c); })))) : null,
    shop.claims.filter(x => x.user === me || !kid).length ? el("div", { class: "muted", style: { fontSize: "12.5px", marginTop: "8px" } },
      shop.claims.filter(x => x.status !== "waiting").slice(0, 5).map(x => `${x.who}: ${x.title} ${x.status === "yes" ? "✓" : "✗"}`).join(" · ")) : null);
}

function rewardDialog(rw, c) {
  rw = rw || { points: 10, emoji: "🎁" };
  const title = input(rw.title, { placeholder: "30 minutes of screen time" }), points = input(rw.points, { type: "number", min: 1 }), emoji = input(rw.emoji, { style: { width: "80px" } });
  dialog({ title: rw.id ? "Reward" : "New reward", body: el("div", {}, field("Reward", title), el("div", { class: "row" }, field("Points", points), field("Emoji", emoji))),
    buttons: [rw.id ? { label: "Remove", danger: true, action: async () => { await HH.del("reward", rw.id).catch(fail); showChores(c); } } : null, "grow", { label: "Cancel" },
      { label: "Save", primary: true, action: async () => { await HH.save("reward", { ...rw, title: title.value, points: +points.value, emoji: emoji.value }); showChores(c); } }].filter(Boolean) });
}

// --- home and car maintenance

const AREAS = [["home", "🏠 Home"], ["car", "🚗 Car"], ["yard", "🌳 Yard"], ["other", "🔧 Other"]];

async function showUpkeep(c) {
  let list;
  try { [list] = await Promise.all([HH.list("upkeep"), people()]); } catch (e) { return fail(e); }
  const today = ymd(new Date()), soon = ymd(addDays(new Date(), 14));
  const due = x => {
    const h = x.history || [];
    if (!h.length) return { day: "", miles: 0 };
    const last = h[0];
    return { day: x.everyDays ? ymd(addDays(parseYmd(last.date), x.everyDays)) : "", miles: x.everyMiles && last.miles ? last.miles + x.everyMiles : 0 };
  };
  const sorted = list.map(x => ({ x, d: due(x) })).sort((a, b) => (a.d.day || "9999").localeCompare(b.d.day || "9999"));
  put(c, pageHead("Maintenance", "Filters, batteries, oil changes: when they're due, and when they were last done.", btn("Add", "plus", () => upkeepDialog(null, c), "primary")),
    ...(sorted.length ? AREAS.filter(([k]) => sorted.some(s => (s.x.area || "other") === k)).flatMap(([k, label]) => [el("h3", { class: "sec", text: label }),
      el("div", { class: "card items-list" }, sorted.filter(s => (s.x.area || "other") === k).map(({ x, d }) => el("div", {},
        el("div", { class: "grow", style: { cursor: "pointer" }, onclick: () => upkeepDialog(x, c) }, el("div", { style: { fontWeight: 600 }, text: x.title }),
          el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [x.everyDays ? everyText(x.everyDays) : "", x.everyMiles ? `every ${x.everyMiles.toLocaleString()} miles` : "",
            (x.history || []).length ? "last done " + parseYmd(x.history[0].date).toLocaleDateString([], { dateStyle: "medium" }) + (x.history[0].miles ? ` at ${x.history[0].miles.toLocaleString()} mi` : "") : "never done yet"].filter(Boolean).join(" · ") })),
        d.day ? el("span", { class: "pill" + (d.day < today ? " gone" : d.day <= soon ? " warn" : ""), text: (d.day < today ? "overdue · " : "due ") + parseYmd(d.day).toLocaleDateString([], { month: "short", day: "numeric" }) }) : null,
        d.miles ? el("span", { class: "pill" + (x.miles && x.miles >= d.miles ? " gone" : ""), text: `at ${d.miles.toLocaleString()} mi` }) : null,
        btn("Done", "check", () => upkeepDone(x, c)))))]) : [emptyCard("settings", "Nothing yet", "Add the furnace filter, smoke alarm batteries, the gutters, the car's oil: Cove reminds you when each is due.")]));
}
const everyText = d => d % 365 === 0 ? (d === 365 ? "every year" : `every ${d / 365} years`) : d % 30 === 0 ? (d === 30 ? "every month" : `every ${d / 30} months`) : d % 7 === 0 ? `every ${d / 7} weeks` : `every ${d} days`;

function upkeepDone(x, c) {
  const date = input(ymd(new Date()), { type: "date" }), miles = input(x.everyMiles ? (x.miles || "") : "", { type: "number", placeholder: "Odometer" }), note = input("", { placeholder: "Brand of filter, who did it, cost…" });
  dialog({ title: "Done: " + x.title, body: el("div", {}, el("div", { class: "row" }, field("When", date), x.everyMiles ? field("Miles", miles) : null), field("Note", note)),
    buttons: [{ label: "Cancel" }, { label: "Save", primary: true, action: async () => {
      const m = +miles.value || 0;
      await HH.save("upkeep", { ...x, miles: Math.max(x.miles || 0, m), history: [{ date: date.value, miles: m, note: note.value, by: S.me.user.display || S.me.user.name }, ...(x.history || [])] });
      showUpkeep(c);
    } }] });
}

function upkeepDialog(x, c) {
  x = x || { area: "home", everyDays: 90, history: [] };
  const title = input(x.title, { placeholder: "Change the furnace filter" }), area = select(AREAS, x.area);
  const every = select([[0, "—"], [30, "Every month"], [60, "Every 2 months"], [90, "Every 3 months"], [180, "Every 6 months"], [365, "Every year"], [730, "Every 2 years"]], x.everyDays);
  const miles = input(x.everyMiles || "", { type: "number", placeholder: "5000" }), notes = textarea(x.notes);
  dialog({ title: x.id ? x.title : "New maintenance", body: el("div", {}, field("What", title), el("div", { class: "row" }, field("Where", area), field("How often", every), field("Or every … miles", miles)), field("Notes", notes),
    (x.history || []).length ? el("div", {}, el("div", { class: "label", text: "Done before" }), el("div", { class: "muted", style: { fontSize: "12.5px" } }, x.history.slice(0, 8).map(h => el("div", { text: `${h.date}${h.miles ? " · " + h.miles.toLocaleString() + " mi" : ""}${h.by ? " · " + h.by : ""}${h.note ? " · " + h.note : ""}` })))) : null),
  buttons: [x.id ? { label: "Delete", danger: true, action: async () => { await HH.del("upkeep", x.id).catch(fail); showUpkeep(c); } } : null, "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => { await HH.save("upkeep", { ...x, title: title.value, area: area.value, everyDays: +every.value, everyMiles: +miles.value || 0, notes: notes.value }); showUpkeep(c); } }].filter(Boolean) });
}

// --- the guest Wi-Fi card

function wifiDialog(c) {
  const ssid = input("", { placeholder: "Network name" }), pass = input("", { placeholder: "Password", autocomplete: "off" });
  dialog({ title: "Guest Wi-Fi on the fridge", body: el("div", {}, field("Network", ssid), field("Password", pass),
    el("p", { class: "muted", style: { fontSize: "12.5px" }, text: "Guests scan the code with their phone's camera to join. Everyone signed in to Cove can see the password: use your guest network." })),
    buttons: [{ label: "Cancel" }, { label: "Put it up", primary: true, action: async () => { await api("POST", "/api/fridge", { kind: "wifi", text: ssid.value, items: [{ text: pass.value }], color: "white", pinned: true }); showFridge(c); } }] });
}

function wifiCard(n, card) {
  card.append(el("div", { class: "wifi" }, el("div", { class: "muted", text: "📶 Guest Wi-Fi" }), el("b", { text: n.text }),
    el("img", { src: `/api/fridge/${n.id}/wifi`, alt: "Scan to join " + n.text }),
    n.items[0] ? el("div", { class: "mono", text: n.items[0].text }) : null));
}

// --- bringing in photos from Google Takeout or iCloud

async function importPhotoZip(p) {
  if (!(await confirmBox("Bring in these photos?", "Cove unpacks the photos and videos into Photos, by the date each was taken (Google's info files are used for dates and places). It runs in the background: you'll get a notification when it's done. The zip stays where it is.", "Bring them in"))) return;
  try { await api("POST", "/api/photos/import", { path: p }); toast("Started: you'll get a notification when it's done"); } catch (e) { fail(e); }
}

// --- the welcome tour

function welcomeTour(force) {
  let seen = false;
  try { seen = localStorage.getItem("cove.welcomed." + S.me.user.name) === "1"; } catch (_) {}
  if (seen && !force) return;
  const done = () => { try { localStorage.setItem("cove.welcomed." + S.me.user.name, "1"); } catch (_) {} };
  const ios = /iPhone|iPad/.test(navigator.userAgent), android = /Android/.test(navigator.userAgent);
  const steps = [
    { title: `Welcome to Cove, ${S.me.user.display || S.me.user.name}!`, body: el("div", {},
      el("p", { text: "Cove is your family's own cloud: files and photos, documents, the calendar, chores, meals and the fridge, all at home, on your own server." }),
      el("p", { class: "muted", text: "A few quick steps get the most out of it. You can skip any of them." }),
      el("div", { class: "label", text: "Light or dark?" }), (() => { const b = el("div", { class: "seg" }); const d = () => put(b, [["auto", "Automatic"], ["light", "Light"], ["dark", "Dark"]].map(([v, l]) => el("button", { class: coveTheme.get() === v ? "on" : "", style: { padding: "7px 14px" }, onclick: () => { coveTheme.set(v); d(); } }, l))); d(); return b; })()) },
    { title: "Put Cove on your home screen", body: el("div", {},
      el("p", { text: ios ? "In Safari, tap the Share button (the square with an arrow), then “Add to Home Screen”. Open Cove from there from now on: it works like an app, and can send notifications." :
        android ? "In Chrome, tap ⋮ then “Add to Home screen” (or “Install app”). Cove then opens like an app, and shared photos and files can go straight to it." :
        "On your phone, open this same address and add it to the home screen (Safari: Share → Add to Home Screen; Chrome: ⋮ → Install app)." })) },
    { title: "Get notifications", body: el("div", {}, el("p", { text: "Reminders, invitations, chores, the morning briefing and chat messages can reach this device." }),
      btn("Turn on notifications here", "bell", async () => { try { await enablePush(); toast("Notifications are on"); } catch (e) { fail(e); } }, "primary")) },
    { title: "Your calendar and contacts on your phone", body: el("div", {},
      el("p", { text: "Cove's calendars, tasks and contacts can sync with your phone's own Calendar, Reminders and Contacts apps." }),
      el("p", { class: "muted", text: "In Settings → Sync apps, make an app password, then add the account on your phone (iPhone: Settings → Calendar → Accounts → Add Account → Other → CalDAV)." }),
      btn("Open Settings after the tour", "settings", () => { done(); location.hash = "#/settings"; }, "ghost")) },
    { title: "Bring in what you have", body: el("div", {},
      el("p", {}, el("b", { text: "Photos: " }), "from Google Photos (takeout.google.com) or iCloud (privacy.apple.com), download the .zip, upload it to My files, then use “Bring in photos” on it."),
      el("p", {}, el("b", { text: "Contacts: " }), "export a .vcf from Google Contacts or iCloud, then Contacts → ⋯ → Import."),
      el("p", {}, el("b", { text: "Calendars: " }), "export .ics files, or subscribe to a calendar's link in Calendar → ⋯.")) },
  ];
  let i = 0;
  const show = () => {
    const s = steps[i];
    dialog({ title: s.title, wide: true, body: el("div", {}, s.body, el("div", { class: "muted", style: { fontSize: "12px", marginTop: "12px" }, text: `${i + 1} of ${steps.length}` })),
      onClose: () => {},
      buttons: [{ label: "Skip the tour", action: () => done() }, "grow", i > 0 ? { label: "Back", action: () => { i--; setTimeout(show, 0); } } : null,
        { label: i === steps.length - 1 ? "Done" : "Next", primary: true, action: () => { if (i === steps.length - 1) { done(); return; } i++; setTimeout(show, 0); } }].filter(Boolean) });
  };
  show();
}

// --- the menu: groups that fold, and pages each person can hide

const MENU = {
  top: [["home", "home", "Home"], ["ask", "search", "Ask Cove"], ["chat", "mail", "Chat"], ["fridge", "pin", "Fridge"]],
  groups: [
    ["Files", [["files", "folder", "My files"], ["photos", "image", "Photos"], ["journal", "lock", "My journal"], ["recent", "clock", "Recent"], ["shared", "inbox", "Shared with me"], ["links", "share", "Shared links", "adult"]]],
    ["Plans", [["calendar", "calendar", "Calendar"], ["tasks", "check", "Tasks"], ["meals", "text", "Meals"], ["shopping", "check", "Shopping"], ["chores", "star", "Chores"], ["trips", "place", "Trips"]]],
    ["Family", [["contacts", "user", "Contacts"], ["notes", "edit", "Notes"], ["school", "text", "School"], ["wishes", "star", "Wish lists"], ["health", "clock", "Health"], ["sitter", "user", "Babysitter"]]],
    ["House", [["pantry", "inbox", "Pantry"], ["upkeep", "settings", "Maintenance"], ["records", "key", "Home records", "adult"], ["budget", "sheet", "Budget", "adult"]]],
  ],
  bottom: [["kiosk", "grid", "Kitchen display"], ["activity", "restore", "Activity"], ["trash", "trash", "Trash"], ["settings", "settings", "Settings"], ["admin", "lock", "Admin", "admin"]],
};
const menuPref = (k, d) => { try { return JSON.parse(localStorage.getItem("cove.menu." + k) || "null") ?? d; } catch (_) { return d; } };
const setMenuPref = (k, v) => { try { localStorage.setItem("cove.menu." + k, JSON.stringify(v)); } catch (_) {} };

// GUEST_PAGES are what family guests (grandparents) see.
const GUEST_PAGES = ["home", "calendar", "photos", "fridge", "chat", "settings"];

function menuAllowed([id, , , who]) {
  if (S.me.user.guest && !S.me.user.admin && !GUEST_PAGES.includes(id)) return false;
  if (who === "adult" && S.me.user.kid) return false;
  if (who === "admin" && !S.me.user.admin) return false;
  if (id === "ask" && !S.me.ai) return false;
  return !menuPref("hidden", []).includes(id);
}

function navButton([id, ic, label], compact) {
  const go = () => { document.body.classList.remove("nav-open"); location.hash = id === "files" ? "#/files/" : "#/" + id; };
  return compact ? el("button", { class: "nav mini", "data-nav": id, title: label, "aria-label": label, onclick: go }, icon(ic)) :
    el("button", { class: "nav", "data-nav": id, onclick: go }, icon(ic), label, id === "chat" ? el("span", { class: "nbadge hide" }) : null);
}

function buildMenu() {
  const open = menuPref("open", { Files: true, Plans: true, Family: false, House: false });
  const section = (location.hash.split("/")[1] || "home");
  return el("nav", { class: "navlist" },
    ...MENU.top.filter(menuAllowed).map(n => navButton(n)),
    ...MENU.groups.map(([name, items]) => {
      const shown = items.filter(menuAllowed);
      if (!shown.length) return null;
      const here = shown.some(([id]) => id === section);
      const isOpen = open[name] || here;
      const body = el("div", { class: "navitems" + (isOpen ? "" : " hide") }, shown.map(n => navButton(n)));
      const head = el("button", { class: "navgroup fold" + (isOpen ? " open" : ""), onclick: () => {
        const o = !head.classList.contains("open");
        head.classList.toggle("open", o); body.classList.toggle("hide", !o);
        setMenuPref("open", { ...menuPref("open", {}), [name]: o });
      } }, el("span", { text: name }), icon("right"));
      return el("div", { class: "navsec" }, head, body);
    }));
}

function menuFoot() {
  return el("div", { class: "navfoot" }, MENU.bottom.filter(menuAllowed).map(n => navButton(n, true)));
}

function menuSettings() {
  const hidden = new Set(menuPref("hidden", []));
  const all = [...MENU.top, ...MENU.groups.flatMap(g => g[1]), ...MENU.bottom].filter(([id, , , who]) => id !== "home" && id !== "settings" &&
    !(who === "adult" && S.me.user.kid) && !(who === "admin" && !S.me.user.admin));
  return el("div", { class: "card section" }, el("h3", { text: "Menu" }),
    el("p", { class: "muted", text: "Hide the pages you don't use (on this device). They're still there: switch them back on here." }),
    el("div", { class: "chips", style: { padding: 0 } }, all.map(([id, ic, label]) => el("button", { class: "chip" + (hidden.has(id) ? "" : " on"), onclick: e => {
      hidden.has(id) ? hidden.delete(id) : hidden.add(id);
      e.currentTarget.classList.toggle("on", !hidden.has(id));
      setMenuPref("hidden", [...hidden]);
      drawShell(); route();
    } }, label))),
    el("div", { style: { marginTop: "12px" } }, btn("Show the welcome tour again", "star", () => welcomeTour(true), "ghost")));
}

// menuFollow opens the group holding the page being shown (when you go there from a link or the home page).
function menuFollow(section) {
  const b = document.querySelector(`.navsec [data-nav="${section}"]`);
  if (!b) return;
  const sec = b.closest(".navsec");
  sec.querySelector(".navitems").classList.remove("hide");
  sec.querySelector(".navgroup").classList.add("open");
}
