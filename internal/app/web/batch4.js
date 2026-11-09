// Cove: the shopping list by aisle, dinner ideas, Siri and Android shortcuts, the babysitter page, school, the
// family video call, bringing files back from the off-site backup, and the week on paper.
"use strict";

// --- shopping

const SHOP = { store: false };

async function showShopping(c) {
  let s;
  try { s = await api("GET", "/api/shopping"); } catch (e) { return fail(e); }
  const save = async items => {
    try { await api("PUT", "/api/fridge/" + s.id, { kind: "list", text: "Shopping", color: "green", items: items.map(({ text, done, by }) => ({ text, done, by })) }); } catch (e) { fail(e); }
  };
  const all = s.items.map(i => ({ ...i }));
  const add = async text => {
    if (!text.trim()) return;
    try { await api("POST", "/api/shopping", { items: [text.trim()] }); } catch (e) { return fail(e); }
    showShopping(c);
  };
  const box = input("", { placeholder: "Add something…", onkeydown: e => { if (e.key === "Enter") { add(e.target.value); e.target.value = ""; } } });
  const groups = [];
  for (const it of all) {
    if (!groups.length || groups[groups.length - 1][0] !== it.aisle) groups.push([it.aisle, []]);
    groups[groups.length - 1][1].push(it);
  }
  const left = all.filter(i => !i.done).length, got = all.length - left;
  put(c, el("div", { class: "shopping" + (SHOP.store ? " store" : "") },
    pageHead("Shopping", left ? `${left} to get${got ? `, ${got} in the cart` : ""}` : "Nothing to get",
      btn(SHOP.store ? "Done with store mode" : "Store mode", "check", () => { SHOP.store = !SHOP.store; showShopping(c); }),
      got ? btn("Done shopping", "check", async () => {
        try { const r = await api("POST", "/api/shopping/done"); toast(`Cleared ${r.bought}${r.stocked ? `, ${r.stocked} put in the pantry` : ""}`); SHOP.store = false; showShopping(c); } catch (e) { fail(e); }
      }, "primary") : null),
    SHOP.store ? null : el("div", { class: "row", style: { marginBottom: "10px" } }, box),
    !SHOP.store && s.usual.length ? el("div", {}, el("div", { class: "label", text: "The usual" }), el("div", { class: "chips", style: { padding: "0 0 12px" } }, s.usual.map(u => el("button", { class: "chip", onclick: () => add(u) }, "+ " + u)))) : null,
    all.length ? el("div", {}, groups.map(([aisle, items]) => el("div", { class: "aisle" }, el("h3", { class: "sec", text: aisle }),
      el("div", { class: "card chores" }, items.map(it => el("label", { class: "chore shop" + (it.done ? " done" : "") },
        el("input", { type: "checkbox", checked: it.done, onchange: async e => { all[all.findIndex(x => x.index === it.index)].done = e.target.checked; await save(all.sort((a, b) => a.index - b.index)); showShopping(c); } }),
        el("span", { class: "grow ctitle", text: it.text }), it.done && it.by ? el("small", { class: "muted", text: it.by }) : null)))))) :
      emptyCard("check", "The list is empty", "Add things here, from a recipe, or by saying “Hey Siri, ask Cove to add milk”.")));
  if (!SHOP.store) setTimeout(() => box.focus(), 50);
}

// --- dinner ideas

async function dinnerIdeas(c) {
  const from = ymd(MEALS.week) < ymd(new Date()) ? ymd(new Date()) : ymd(MEALS.week), to = ymd(addDays(MEALS.week, 6));
  toast("Thinking about dinners…");
  let r;
  try { r = await api("POST", "/api/meals/suggest", { from, to }); } catch (e) { return fail(e); }
  if (!r.ideas.length) return toast("Every dinner this week is planned already");
  const picks = r.ideas.map(i => ({ i, on: el("input", { type: "checkbox", checked: true }) }));
  dialog({ title: "Dinner ideas", wide: true, body: el("div", {},
    el("p", { class: "muted", style: { marginTop: 0 }, text: "From your recipes and what's in the pantry. Untick what you don't want." }),
    el("div", { class: "card items-list", style: { boxShadow: "none" } }, picks.map(({ i, on }) => el("label", { class: "row", style: { alignItems: "flex-start", cursor: "pointer" } }, on,
      el("div", { class: "grow" }, el("b", { text: `${parseYmd(i.date).toLocaleDateString([], { weekday: "long" })}: ${i.dinner}` }),
        el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [i.why, i.missing.length ? "to buy: " + i.missing.join(", ") : ""].filter(Boolean).join(" · ") })))))),
  buttons: [{ label: "Cancel" }, { label: "Plan them", primary: true, action: async () => {
    const chosen = picks.filter(p => p.on.checked).map(p => p.i);
    for (const i of chosen) await HH.save("meal", { date: i.date, slot: "dinner", recipe: i.recipeId || 0, text: i.recipeId ? "" : i.dinner });
    const missing = [...new Set(chosen.flatMap(i => i.missing))];
    if (missing.length) await api("POST", "/api/shopping", { items: missing });
    toast(`Planned ${chosen.length} dinners${missing.length ? `, ${missing.length} things on the shopping list` : ""}`);
    showMeals(c);
  } }] });
}

// --- the week on paper

async function showPrint(c) {
  const start = mondayOf(new Date()), days = [...Array(7)].map((_, i) => addDays(start, i));
  const [evs, meals, recipes, chores] = await Promise.all([
    api("GET", `/api/events?from=${ymd(start)}&to=${ymd(addDays(start, 7))}&tz=${encodeURIComponent(tz)}`).catch(() => []),
    HH.list("meal", `from=${ymd(start)}&to=${ymd(addDays(start, 6))}`).catch(() => []), HH.list("recipe").catch(() => []), api("GET", "/api/chores").catch(() => null), people()]);
  const rname = Object.fromEntries(recipes.map(r => [r.id, r.title]));
  put(c, el("div", { class: "printweek" },
    el("div", { class: "noprint bar" }, el("div", { class: "page-title", style: { margin: 0 }, text: "The week on paper" }), el("div", { class: "grow" }), btn("Print", "download", () => print(), "primary")),
    el("h1", { text: "Week of " + start.toLocaleDateString([], { month: "long", day: "numeric" }) }),
    el("table", {}, el("thead", {}, el("tr", {}, el("th", { text: "" }), ...days.map(d => el("th", { text: d.toLocaleDateString([], { weekday: "short", day: "numeric" }) })))),
      el("tbody", {},
        el("tr", {}, el("th", { text: "Plans" }), ...days.map(d => el("td", {}, evs.filter(o => (o.allDay ? o.start : ymd(new Date(o.start))) === ymd(d)).map(o =>
          el("div", { text: (o.allDay ? "" : new Date(o.start).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }) + " ") + o.title }))))),
        el("tr", {}, el("th", { text: "Dinner" }), ...days.map(d => el("td", {}, meals.filter(m => m.date === ymd(d) && m.slot === "dinner").map(m => el("div", { text: m.recipe ? rname[m.recipe] || m.text : m.text }))))),
        el("tr", {}, el("th", { text: "Chores" }), ...days.map(d => el("td", {}, (chores ? chores.chores : []).filter(x => choreOnDay(x.chore, d)).map(x =>
          el("div", { text: "☐ " + x.title + (x.chore.who && x.chore.who.length && !x.chore.rotate ? " (" + x.chore.who.map(nameOf).join(", ") + ")" : "") })))))))));
}
function choreOnDay(ch, d) {
  if (ch.paused) return false;
  const [k, n] = (ch.repeat || "").split(":");
  if (k === "daily" || k === "once") return true;
  if (k === "weekdays") return d.getDay() >= 1 && d.getDay() <= 5;
  if (k === "weekly") return d.getDay() === +n;
  if (k === "monthly") return d.getDate() === +n;
  return false;
}

// --- Siri and Android shortcuts

function shortcutSettings() {
  const url = location.origin + "/shortcut";
  return el("div", { class: "card section" }, el("h3", { text: "Siri and shortcuts" }),
    el("p", { text: "Talk to Cove without opening it: “Hey Siri, Cove” → “add milk and eggs”, “what's for dinner?”, “put up a note: back at 6”." }),
    el("ol", { class: "steps" },
      el("li", { text: "Make an app password above (Sync apps), named “Siri”." }),
      el("li", {}, "On the iPhone, open Shortcuts → + → name it “Cove”, then add these actions: ", el("b", { text: "Dictate Text" }), " → ",
        el("b", { text: "Get Contents of URL" }), " (Method POST, Request Body: File = Dictated Text, Headers: Authorization = Basic … see below) → ", el("b", { text: "Speak Text" }), "."),
      el("li", {}, "The address: ", copyField(url)),
      el("li", { text: "For the Authorization header, the Shortcuts app's “Base64 Encode” of “your-user-name:the-app-password”, written as “Basic ” and that text. Or use Get Contents of URL's own Basic authentication if it offers it." }),
      el("li", { text: "On Android, the HTTP Shortcuts app does the same: POST the spoken text to that address with Basic authentication, and read the answer aloud." })),
    el("p", { class: "muted", style: { fontSize: "12.5px" }, text: "It needs Ask Cove switched on (Admin → AI). Anything Cove does is the same as you asking in the app, with your permissions." }));
}

// --- the babysitter page

async function showSitter(c) {
  let rows, links;
  try { [rows, links] = await Promise.all([HH.list("sitter"), api("GET", "/api/sitter-links")]); } catch (e) { return fail(e); }
  const x = rows[rows.length - 1] || { contacts: [{ name: S.me.user.display || S.me.user.name, phone: "", who: "" }], kids: [], wifi: true };
  const kid = isKid();
  const contacts = el("div"), kidsBox = el("div");
  const contactRow = p => el("div", { class: "row srow" }, input(p.name, { placeholder: "Name" }), input(p.who, { placeholder: "Mom, neighbor…" }), input(p.phone, { placeholder: "Phone", type: "tel" }),
    el("button", { class: "btn ghost icon", type: "button", onclick: e => e.currentTarget.parentElement.remove() }, icon("close")));
  const kidRow = k => el("div", { class: "card kidcard" }, el("div", { class: "row" }, input(k.name, { placeholder: "Name" }), input(k.age, { placeholder: "Age" }), input(k.bedtime, { placeholder: "Bedtime" })),
    input(k.allergies, { placeholder: "Allergies (none?)" }), textarea(k.notes, { placeholder: "Comforter, favorite show, how to get them to sleep…" }),
    el("button", { class: "btn ghost", type: "button", onclick: e => e.currentTarget.parentElement.remove() }, "Remove"));
  (x.contacts || []).forEach(p => contacts.append(contactRow(p)));
  (x.kids || []).forEach(k => kidsBox.append(kidRow(k)));
  const address = input(x.address, { placeholder: "Street, city" }), doctor = textarea(x.doctor, { placeholder: "Dr. Lee, 555-0101; nearest urgent care…" });
  const rules = textarea(x.rules, { placeholder: "Bedtime routine, screen time, snacks, pets…" }), notes = textarea(x.notes, { placeholder: "Where the first-aid kit is, the breaker box, spare key…" });
  const wifi = el("input", { type: "checkbox", checked: x.wifi });
  const save = async () => {
    const vals = el => [...el.querySelectorAll("input, textarea")].map(i => i.value);
    const o = { ...x, address: address.value, doctor: doctor.value, rules: rules.value, notes: notes.value, wifi: wifi.checked,
      contacts: [...contacts.children].map(r => { const [name, who, phone] = vals(r); return { name, who, phone }; }),
      kids: [...kidsBox.children].map(r => { const [name, age, bedtime, allergies, notes] = vals(r); return { name, age, bedtime, allergies, notes }; }) };
    await HH.save("sitter", o);
  };
  const hours = select([[4, "4 hours"], [12, "12 hours"], [24, "1 day"], [72, "3 days"], [168, "1 week"]], 12);
  const label = input("", { placeholder: "For whom? (Saturday night, Emma…)" });
  put(c, pageHead("Babysitter page", "One page for the sitter: who to call, the kids, house rules, the Wi-Fi. Only what's here, and only until the link runs out."),
    el("div", { class: "card section" }, el("h3", { text: "Send it to the sitter" }),
      kid ? el("p", { class: "muted", text: "A grown-up makes the link." }) : el("div", { class: "row", style: { alignItems: "flex-end", flexWrap: "wrap" } }, field("Label", label), field("Works for", hours),
        btn("Make a link", "share", async () => {
          try { await save(); const r = await api("POST", "/api/sitter-links", { hours: +hours.value, label: label.value }); await navigator.clipboard?.writeText(r.url).catch(() => {}); toast("Link copied: send it to the sitter"); showSitter(c); } catch (e) { fail(e); }
        }, "primary")),
      links.length ? el("div", { class: "items-list", style: { marginTop: "10px" } }, links.map(l => el("div", {}, icon("share"),
        el("div", { class: "grow" }, el("b", { text: l.label || "Sitter link" }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: "works until " + fmtDate(l.expires * 1000) })),
        btn("Copy", "copy", () => { navigator.clipboard?.writeText(l.url); toast("Copied"); }), btn("Open", "open", () => window.open(l.url, "_blank")),
        kid ? null : btn("End", null, async () => { await api("DELETE", "/api/sitter-links/" + l.token).catch(fail); showSitter(c); }, "danger")))) : null),
    el("div", { class: "card section" }, field("Address", address), el("div", { class: "label", text: "Who to call" }), contacts,
      kid ? null : btn("Add someone", "plus", () => contacts.append(contactRow({})), "ghost"),
      el("div", { class: "label", style: { marginTop: "14px" }, text: "The kids" }), kidsBox, kid ? null : btn("Add a child", "plus", () => kidsBox.append(kidRow({})), "ghost"),
      field("House rules and bedtime", rules), field("Doctor and urgent care", doctor), field("Good to know", notes),
      el("label", { class: "check" }, wifi, "Show the guest Wi-Fi (from the fridge's Wi-Fi card)"),
      kid ? null : btn("Save", null, async () => { try { await save(); toast("Saved"); } catch (e) { fail(e); } }, "primary")));
}

// --- school

const SCHOOL = { kid: null };

async function showSchool(c) {
  let list;
  try { [list] = await Promise.all([HH.list("homework"), people()]); } catch (e) { return fail(e); }
  const kids = PEOPLE.filter(p => list.some(h => h.kid === p.name) || p.name === S.me.user.name || true);
  SCHOOL.kid = SCHOOL.kid || (isKid() ? S.me.user.name : (list[0] && list[0].kid) || kids[0].name);
  const mine = list.filter(h => h.kid === SCHOOL.kid).sort((a, b) => (a.done - b.done) || (a.due || "9").localeCompare(b.due || "9"));
  const today = ymd(new Date()), tomorrow = ymd(addDays(new Date(), 1));
  const folder = `/School/${nameOf(SCHOOL.kid)}`;
  const up = el("input", { type: "file", multiple: true, class: "hide", onchange: async e => {
    for (const f of e.target.files) { try { await xhrSend("PUT", "/api/files/content?path=" + encPath(`${folder}/Report cards/${f.name}`), f); } catch (err) { fail(err); } }
    toast("Saved in " + folder + "/Report cards");
  } });
  put(c, pageHead("School", null, up, btn("Report card", "upload", () => up.click()), btn("School calendar", "calendar", () => { location.hash = "#/calendar"; setTimeout(() => subscribeDialog(null, $("content")), 400); }),
    btn("Homework", "plus", () => homeworkDialog(null, c), "primary")),
    el("div", { class: "chips", style: { padding: "0 0 12px" } }, kids.map(p => el("button", { class: "chip" + (SCHOOL.kid === p.name ? " on" : ""), onclick: () => { SCHOOL.kid = p.name; showSchool(c); } }, p.display || p.name))),
    mine.length ? el("div", { class: "card chores" }, mine.map(h => el("div", { class: "chore" + (h.done ? " done" : "") },
      el("input", { type: "checkbox", checked: h.done, onchange: async e => { await HH.save("homework", { ...h, done: e.target.checked }).catch(fail); showSchool(c); } }),
      el("div", { class: "grow", style: { cursor: "pointer" }, onclick: () => homeworkDialog(h, c) }, el("div", { class: "ctitle", text: (h.subject ? h.subject + ": " : "") + h.title }),
        h.notes ? el("div", { class: "muted", style: { fontSize: "12.5px" }, text: h.notes }) : null),
      h.due && !h.done ? el("span", { class: "pill" + (h.due < today ? " gone" : h.due <= tomorrow ? " warn" : ""), text: h.due === today ? "due today" : h.due === tomorrow ? "due tomorrow" : "due " + parseYmd(h.due).toLocaleDateString([], { weekday: "short", month: "short", day: "numeric" }) }) : null))) :
      emptyCard("text", "No homework", "Write down homework and projects with their due dates: Cove reminds the day before at 5."),
    el("p", { class: "muted", style: { fontSize: "12.5px" } }, "Report cards and school papers go in ", el("a", { href: "#", text: folder, onclick: e => { e.preventDefault(); goFolder(folder, null); } }), "."));
}

function homeworkDialog(h, c) {
  h = h || { kid: SCHOOL.kid, due: ymd(addDays(new Date(), 1)) };
  const kid = select(PEOPLE.map(p => [p.name, p.display || p.name]), h.kid), subject = input(h.subject, { placeholder: "Math", list: "subjects" });
  const title = input(h.title, { placeholder: "Fractions worksheet" }), due = input(h.due, { type: "date" }), notes = textarea(h.notes);
  dialog({ title: h.id ? "Homework" : "New homework", body: el("div", {}, el("div", { class: "row" }, field("For", kid), field("Subject", subject)), field("What", title), field("Due", due), field("Notes", notes),
    el("datalist", { id: "subjects" }, ["Math", "Reading", "Writing", "Science", "History", "Spanish", "French", "Art", "Music", "Project"].map(v => el("option", { value: v })))),
  buttons: [h.id ? { label: "Delete", danger: true, action: async () => { await HH.del("homework", h.id).catch(fail); showSchool(c); } } : null, "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => { await HH.save("homework", { ...h, kid: kid.value, subject: subject.value, title: title.value, due: due.value, notes: notes.value }); SCHOOL.kid = kid.value; showSchool(c); } }].filter(Boolean) });
}

// --- the family video call

async function callFamily() {
  if (!(await confirmBox("Call the family?", "Everyone gets a notification to join the video call.", "Call"))) return;
  const win = window.open("about:blank", "_blank");
  try { const r = await api("POST", "/api/call"); if (win) win.location = r.link; else location.href = r.link; } catch (e) { if (win) win.close(); fail(e); }
}

function callAdmin() {
  const box = el("div", { class: "card section" }, el("h3", { text: "Family video call" }));
  api("GET", "/api/call").then(r => {
    const url = input(r.url, { placeholder: "https://mirotalk.home.example.com" });
    put(box, el("h3", { text: "Family video call" }), el("p", { class: "muted", style: { fontSize: "13px" }, text: "“Call the family” in Chat opens a video call everyone gets a notification for. Homeport's MiroTalk (or any address that takes /join?room=…)." }),
      field("Video call address", url), btn("Save", null, async () => { try { await api("PUT", "/api/admin/call", { url: url.value }); S.me = await api("GET", "/api/me"); toast("Saved"); } catch (e) { fail(e); } }, "primary"));
  }).catch(() => {});
  return box;
}

// --- bringing a file back from the off-site backup

async function restoreDialog(it) {
  let snaps;
  try { snaps = await api("GET", "/api/backups/snapshots"); } catch (e) { return fail(e); }
  if (!snaps.length) return toast("There's no off-site backup yet", true);
  const sel = select(snaps.map(s => [s.id, new Date(s.time).toLocaleString([], { dateStyle: "full", timeStyle: "short" })]), snaps[0].id);
  dialog({ title: "Bring back “" + it.name + "”", body: el("div", {}, field("From the backup of", sel),
    el("p", { class: "muted", style: { fontSize: "12.5px" }, text: "The copy from then is put next to this one, named “… (from … backup)”. Nothing is replaced." })),
    buttons: [{ label: "Cancel" }, { label: "Bring it back", primary: true, action: async () => { toast("Fetching from the backup… this can take a few minutes"); const r = await api("POST", "/api/backups/restore", { snapshot: sel.value, path: it.path }); toast("Brought back: " + r.path); refreshSoon(); } }] });
}
