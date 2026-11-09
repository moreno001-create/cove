// Cove's household pages: meals, chores, photos, scanning, home records, budget, trips, chat and Ask Cove.
"use strict";

// put fills an element, leaving out what's missing (null, false, nested arrays flattened).
const put = (target, ...kids) => target.replaceChildren(...kids.flat(9).filter(x => x != null && x !== false));

const HH = {
  list: (kind, q) => api("GET", `/api/hh/${kind}` + (q ? "?" + q : "")),
  save: (kind, o) => o.id ? api("PUT", `/api/hh/${kind}/${o.id}`, o) : api("POST", `/api/hh/${kind}`, o),
  del: (kind, id) => api("DELETE", `/api/hh/${kind}/${id}`),
};
let PEOPLE = null; // everyone at home: [{name, display}]
async function people() {
  if (!PEOPLE) {
    const others = await api("GET", "/api/users").catch(() => []);
    PEOPLE = [{ name: S.me.user.name, display: S.me.user.display || S.me.user.name }, ...others];
  }
  return PEOPLE;
}
const nameOf = n => { const p = (PEOPLE || []).find(x => x.name === n); return p ? (p.display || p.name) : n; };
const isKid = () => !!(S.me && S.me.user.kid);
const money = n => (n < 0 ? "−$" : "$") + Math.abs(n).toLocaleString([], { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const cents = c => money(c / 100);

function pageHead(title, sub, ...buttons) {
  return el("div", { class: "bar" }, el("div", { class: "page-title", style: { margin: 0 }, text: title }),
    sub ? el("span", { class: "muted", style: { fontSize: "13px" }, text: sub }) : null, el("div", { class: "grow" }), ...buttons);
}
function btn(label, ic, onclick, cls) {
  return el("button", { class: "btn" + (cls ? " " + cls : ""), type: "button", onclick }, ic ? icon(ic) : null, el("span", { class: "lbl", text: label }));
}
function field(label, input, hint) {
  return el("div", { class: "field" }, el("label", { text: label }), input, hint ? el("div", { class: "muted", style: { fontSize: "12px", marginTop: "4px" }, text: hint }) : null);
}
function input(value, attrs) { return el("input", { class: "input", value: value == null ? "" : value, ...(attrs || {}) }); }
function textarea(value, attrs) { return el("textarea", { class: "input", ...(attrs || {}) }, value || ""); }
function select(options, value, attrs) {
  return el("select", { class: "input", ...(attrs || {}) }, options.map(([v, l]) => el("option", { value: v, text: l, selected: String(v) === String(value) })));
}
function emptyCard(ic, title, text, ...more) {
  return el("div", { class: "card empty" }, icon(ic), el("h3", { text: title }), text ? el("p", { text }) : null, ...more);
}
function personChecks(list, chosen) {
  const box = el("div", { class: "chips", style: { padding: 0 } });
  const set = new Set(chosen || []);
  const draw = () => put(box, ...list.map(p => el("button", { type: "button", class: "chip" + (set.has(p.name) ? " on" : ""), onclick: () => { set.has(p.name) ? set.delete(p.name) : set.add(p.name); draw(); } }, p.display || p.name)));
  draw();
  box.value = () => [...set];
  return box;
}
const mondayOf = d => { const x = new Date(d.getFullYear(), d.getMonth(), d.getDate()); x.setDate(x.getDate() - ((x.getDay() + 6) % 7)); return x; };
const DOW = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

// --- meals: the week's plan and the recipe box

const MEALS = { week: mondayOf(new Date()), tab: "plan" };
const SLOTS = [["breakfast", "Breakfast"], ["lunch", "Lunch"], ["dinner", "Dinner"]];

async function showMeals(c) {
  const from = ymd(MEALS.week), to = ymd(addDays(MEALS.week, 6));
  let meals, recipes;
  try { [meals, recipes] = await Promise.all([HH.list("meal", `from=${from}&to=${to}`), HH.list("recipe")]); } catch (e) { return fail(e); }
  const byId = Object.fromEntries(recipes.map(r => [r.id, r]));
  const tabs = el("div", { class: "seg" }, ["plan", "recipes"].map(t => el("button", { class: MEALS.tab === t ? "on" : "", onclick: () => { MEALS.tab = t; showMeals(c); } }, t === "plan" ? "This week" : `Recipes (${recipes.length})`)));
  if (MEALS.tab === "recipes") {
    put(c, pageHead("Meals", null, tabs, btn("New recipe", "plus", () => recipeDialog(null, c), "primary")),
      recipes.length ? el("div", { class: "rgrid" }, recipes.sort((a, b) => a.title.localeCompare(b.title)).map(r => el("div", { class: "card rcard", onclick: () => recipeView(r, c) },
        el("h3", { text: r.title }),
        el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [r.minutes ? r.minutes + " min" : "", r.serves ? "serves " + r.serves : "", (r.ingredients || []).length + " ingredients"].filter(Boolean).join(" · ") }),
        (r.tags || []).length ? el("div", { class: "row", style: { gap: "4px", flexWrap: "wrap", marginTop: "8px" } }, r.tags.map(t => el("span", { class: "pill", text: t }))) : null))) :
        emptyCard("text", "No recipes yet", "Write down the family favorites: ingredients go to the shopping list in one tap."));
    return;
  }
  const days = [...Array(7)].map((_, i) => addDays(MEALS.week, i));
  const today = ymd(new Date());
  const cell = (d, slot) => {
    const here = meals.filter(m => m.date === ymd(d) && m.slot === slot);
    return el("div", { class: "mcell", onclick: e => { if (!e.target.closest(".meal")) mealDialog(null, ymd(d), slot, recipes, c); } },
      here.map(m => el("div", { class: "meal", onclick: () => mealDialog(m, m.date, m.slot, recipes, c) },
        el("div", { text: m.recipe && byId[m.recipe] ? byId[m.recipe].title : m.text }), m.cook ? el("small", { text: "🧑‍🍳 " + nameOf(m.cook) }) : null)),
      here.length ? null : el("span", { class: "add", text: "+" }));
  };
  await people();
  put(c, 
    pageHead("Meals", null, tabs,
      btn("", "left", () => { MEALS.week = addDays(MEALS.week, -7); showMeals(c); }, "ghost icon"),
      el("span", { style: { fontWeight: 600, fontSize: "14px" }, text: MEALS.week.toLocaleDateString([], { month: "short", day: "numeric" }) + " – " + addDays(MEALS.week, 6).toLocaleDateString([], { month: "short", day: "numeric" }) }),
      btn("", "right", () => { MEALS.week = addDays(MEALS.week, 7); showMeals(c); }, "ghost icon"),
      btn("Shopping list", "check", async () => {
        try { const r = await api("POST", "/api/meals/shopping", { from, to }); toast(r.added ? `${r.added} things added to the fridge's shopping list` : "Everything's on the list already"); } catch (e) { fail(e); }
      }, "primary")),
    el("div", { class: "card mweek" },
      el("div", { class: "mhead" }), ...days.map(d => el("div", { class: "mhead" + (ymd(d) === today ? " today" : "") }, el("b", { text: DOW[d.getDay()].slice(0, 3) }), " " + d.getDate())),
      ...SLOTS.flatMap(([slot, label]) => [el("div", { class: "mslot", text: label }), ...days.map(d => cell(d, slot))])));
}

function mealDialog(m, date, slot, recipes, c) {
  const rec = select([["", "— Not a recipe —"], ...recipes.sort((a, b) => a.title.localeCompare(b.title)).map(r => [r.id, r.title])], m ? m.recipe || "" : "");
  const text = input(m ? m.text : "", { placeholder: "Leftovers, pizza night, eating out…" });
  const cook = select([["", "Anyone"], ...PEOPLE.map(p => [p.name, p.display || p.name])], m ? m.cook : "");
  const day = input(date, { type: "date" });
  const sl = select(SLOTS.concat([["snack", "Snack"]]), slot);
  dialog({ title: m ? "Edit meal" : "Plan a meal", body: el("div", {}, el("div", { class: "row" }, field("Day", day), field("Meal", sl)),
    field("Recipe", rec), field("Or write what it is", text), field("Who's cooking", cook)),
  buttons: [m ? { label: "Remove", danger: true, action: async () => { await HH.del("meal", m.id).catch(fail); showMeals(c); } } : null, "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => {
      await HH.save("meal", { id: m && m.id, date: day.value, slot: sl.value, recipe: +rec.value || 0, text: text.value, cook: cook.value });
      showMeals(c);
    } }].filter(Boolean) });
}

function recipeView(r, c) {
  dialog({ title: r.title, wide: true, body: el("div", { class: "recipe" },
    el("div", { class: "muted", text: [r.minutes ? r.minutes + " minutes" : "", r.serves ? "Serves " + r.serves : "", "by " + r.who].filter(Boolean).join(" · ") }),
    r.link ? el("p", {}, el("a", { href: r.link, target: "_blank", rel: "noopener", text: r.link })) : null,
    el("h4", { text: "Ingredients" }), el("ul", {}, (r.ingredients || []).map(i => el("li", { text: i }))),
    r.steps ? el("h4", { text: "Steps" }) : null, r.steps ? el("div", { class: "md" }, markdown(r.steps)) : null),
  buttons: [{ label: "Delete", danger: true, action: async () => { if (await confirmBox("Delete this recipe?", "Meals planned with it keep its name.", "Delete", true)) { await HH.del("recipe", r.id).catch(fail); showMeals(c); } else return false; } },
    { label: "Edit", action: () => setTimeout(() => recipeDialog(r, c), 50) }, "grow",
    { label: "Ingredients to the shopping list", action: async () => { try { const x = await api("POST", "/api/meals/shopping", { recipes: [r.id] }); toast(`${x.added} added to the shopping list`); } catch (e) { fail(e); } return false; } },
    { label: "Done", primary: true }] });
}

function recipeDialog(r, c) {
  const title = input(r ? r.title : "", { placeholder: "Grandma's lasagna" });
  const serves = input(r ? r.serves || "" : "", { type: "number", min: 0 });
  const minutes = input(r ? r.minutes || "" : "", { type: "number", min: 0 });
  const ingr = textarea(r ? (r.ingredients || []).join("\n") : "", { placeholder: "One per line:\n2 cups flour\n3 eggs", style: { minHeight: "140px" } });
  const steps = textarea(r ? r.steps : "", { placeholder: "How to make it (Markdown works)", style: { minHeight: "140px" } });
  const tags = input(r ? (r.tags || []).join(", ") : "", { placeholder: "Quick, Vegetarian, Kids love it" });
  const link = input(r ? r.link : "", { placeholder: "https://… (optional)" });
  dialog({ title: r ? "Edit recipe" : "New recipe", wide: true, body: el("div", {}, field("Name", title), el("div", { class: "row" }, field("Serves", serves), field("Minutes", minutes)),
    el("div", { class: "row" }, field("Ingredients", ingr), field("Steps", steps)), field("Tags", tags), field("Where it's from", link)),
  buttons: [{ label: "Cancel" }, { label: "Save", primary: true, action: async () => {
    await HH.save("recipe", { id: r && r.id, title: title.value, serves: +serves.value || 0, minutes: +minutes.value || 0, ingredients: ingr.value.split("\n"), steps: steps.value,
      tags: tags.value.split(","), link: link.value });
    MEALS.tab = "recipes"; showMeals(c);
  } }] });
}

// --- chores: today's list, the scoreboard, allowance

async function showChores(c) {
  let b;
  try { [b] = await Promise.all([api("GET", "/api/chores"), people()]); } catch (e) { return fail(e); }
  const me = S.me.user.name;
  const today = b.chores.filter(x => x.dueToday);
  const later = b.chores.filter(x => !x.dueToday);
  const repeatText = r => r === "daily" ? "Every day" : r === "weekdays" ? "Weekdays" : r === "once" ? "Once" : r.startsWith("weekly:") ? "Every " + DOW[+r.split(":")[1]] : r.startsWith("monthly:") ? "Monthly on the " + r.split(":")[1] : r;
  const row = ch => el("div", { class: "chore" + (ch.doneBy ? " done" : "") },
    el("input", { type: "checkbox", checked: !!ch.doneBy, disabled: isKid() && ch.doneBy && ch.doneBy !== me, onclick: async e => {
      try {
        if (e.target.checked) await api("POST", `/api/chores/${ch.id}/done`, {});
        else await api("DELETE", `/api/chores/done/${ch.doneId}`);
      } catch (err) { fail(err); }
      showChores(c);
    } }),
    el("div", { class: "grow" }, el("div", { class: "ctitle", text: ch.title }),
      el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [ch.doneBy ? "✓ " + nameOf(ch.doneBy) : (ch.for.length ? ch.for.map(nameOf).join(", ") : "Anyone"), repeatText(ch.repeat)].join(" · ") })),
    ch.points ? el("span", { class: "pill", text: "+" + ch.points }) : null, ch.cents ? el("span", { class: "pill money", text: cents(ch.cents) }) : null,
    isKid() ? null : el("button", { class: "btn ghost icon", onclick: () => choreDialog(ch, c), "aria-label": "Edit" }, icon("edit")));
  const kids = b.people.filter(p => p.kid || p.earned || p.week);
  put(c, 
    pageHead("Chores", null, isKid() ? null : btn("New chore", "plus", () => choreDialog(null, c), "primary")),
    el("div", { class: "scores" }, (kids.length ? kids : b.people).map(p => el("div", { class: "card score" + (p.name === me ? " me" : "") },
      el("div", { class: "avatar", text: (p.who || p.name).slice(0, 1).toUpperCase() }),
      el("div", { class: "grow" }, el("div", { style: { fontWeight: 650 }, text: p.who }),
        el("div", { class: "muted", style: { fontSize: "12.5px" }, text: `${p.week} points this week${p.streak > 1 ? ` · 🔥 ${p.streak} days` : ""}` }),
        p.earned || p.paid ? el("div", { style: { fontSize: "13px", marginTop: "2px" } }, "Allowance owed: ", el("b", { text: cents(p.balance) })) : null),
      !isKid() && p.balance > 0 ? btn("Pay", null, async () => {
        const amt = await ask("Pay " + p.who, "Amount ($)", (p.balance / 100).toFixed(2), "Paid");
        if (amt === null) return;
        try { await api("POST", "/api/allowance", { user: p.name, cents: Math.round(parseFloat(amt) * 100) }); showChores(c); } catch (e) { fail(e); }
      }) : null))),
    el("h3", { class: "sec", text: "Today" }),
    today.length ? el("div", { class: "card chores" }, today.map(row)) : emptyCard("check", "Nothing today", isKid() ? "Enjoy!" : "Add chores, who does them and how often; turns can rotate."),
    later.length ? el("h3", { class: "sec", text: "Other days" }) : null, later.length ? el("div", { class: "card chores" }, later.map(row)) : null,
    b.recent.length ? el("h3", { class: "sec", text: "Done lately" }) : null,
    b.recent.length ? el("div", { class: "card items-list" }, b.recent.slice(0, 12).map(r => el("div", {}, icon("check"), el("div", { class: "grow" }, el("b", { text: r.who }), " " + r.chore),
      el("span", { class: "muted", style: { fontSize: "12.5px" }, text: fmtDate(r.at * 1000) + (r.points ? ` · +${r.points}` : "") })))) : null);
}

function choreDialog(ch, c) {
  const x = ch ? ch.chore : { repeat: "daily", who: [], points: 1, cents: 0 };
  const title = input(x.title, { placeholder: "Empty the dishwasher" });
  const who = personChecks(PEOPLE, x.who);
  const rotate = el("input", { type: "checkbox", checked: x.rotate });
  const [kind, n] = (x.repeat || "daily").split(":");
  const rep = select([["daily", "Every day"], ["weekdays", "Weekdays"], ["weekly", "Once a week"], ["monthly", "Once a month"], ["once", "Just once"]], kind);
  const dow = select(DOW.map((d, i) => [i, d]), kind === "weekly" ? n : 6);
  const dom = input(kind === "monthly" ? n : 1, { type: "number", min: 1, max: 31 });
  const dayBox = el("div");
  const drawDay = () => put(dayBox, rep.value === "weekly" ? field("On", dow) : rep.value === "monthly" ? field("Day of the month", dom) : el("span"));
  rep.onchange = drawDay; drawDay();
  const points = input(x.points || 0, { type: "number", min: 0 });
  const allowance = input(x.cents ? (x.cents / 100).toFixed(2) : "", { type: "number", min: 0, step: "0.25", placeholder: "0.00" });
  const paused = el("input", { type: "checkbox", checked: x.paused });
  dialog({ title: ch ? "Edit chore" : "New chore", body: el("div", {}, field("Chore", title), field("Who (none: anyone)", who),
    el("label", { class: "check" }, rotate, "Take turns (by day for daily chores, by week otherwise)"),
    el("div", { class: "row" }, field("How often", rep), dayBox),
    el("div", { class: "row" }, field("Points", points), field("Allowance ($)", allowance)),
    ch ? el("label", { class: "check" }, paused, "Paused (on holiday)") : null),
  buttons: [ch ? { label: "Delete", danger: true, action: async () => { await HH.del("chore", ch.id).catch(fail); showChores(c); } } : null, "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => {
      const repeat = rep.value === "weekly" ? "weekly:" + dow.value : rep.value === "monthly" ? "monthly:" + dom.value : rep.value;
      await HH.save("chore", { id: ch && ch.id, title: title.value, who: who.value(), rotate: rotate.checked, repeat, points: +points.value || 0,
        cents: Math.round((parseFloat(allowance.value) || 0) * 100), paused: paused.checked });
      showChores(c);
    } }].filter(Boolean) });
}

// --- photos: the timeline, albums, the map, and backing up a phone

const PHOTOS = { tab: "timeline", list: null, sel: new Set() };

async function showPhotos(c, rest) {
  if (rest && rest[0] === "album" && rest[1]) return showAlbum(c, rest[1]);
  const tabs = el("div", { class: "seg" }, [["timeline", "All photos"], ["albums", "Albums"], ["map", "Map"]].map(([t, l]) =>
    el("button", { class: PHOTOS.tab === t ? "on" : "", onclick: () => { PHOTOS.tab = t; showPhotos(c); } }, l)));
  const backup = el("input", { type: "file", multiple: true, accept: "image/*,video/*", class: "hide", onchange: e => backUpPhotos([...e.target.files], c) });
  const head = pageHead("Photos", null, tabs, backup, btn("Back up this phone", "upload", () => backup.click(), "primary"));
  if (PHOTOS.tab === "albums") {
    let albums;
    try { albums = await api("GET", "/api/albums"); } catch (e) { return fail(e); }
    put(c, head, el("div", { class: "bar", style: { marginTop: 0 } }, el("div", { class: "grow" }), btn("New album", "plus", () => albumDialog(null, c))),
      albums.length ? el("div", { class: "agrid" }, albums.map(a => el("div", { class: "card album", onclick: () => { location.hash = "#/photos/album/" + a.id; } },
        a.cover ? el("img", { src: albumPhotoURL(a.id, a.cover, 480), loading: "lazy", alt: "" }) : el("div", { class: "acover" }, icon("image")),
        el("div", { class: "ainfo" }, el("b", { text: a.name }), el("div", { class: "muted", style: { fontSize: "12.5px" }, text: `${a.count} photo${a.count === 1 ? "" : "s"}${a.shared ? " · shared with everyone" : ""}${a.owner !== S.me.user.name ? " · " + a.who : ""}` }))))) :
        emptyCard("image", "No albums yet", "Make an album for a trip or a birthday. A shared album takes everyone's photos."));
    return;
  }
  put(c, head, el("p", { class: "muted", text: "Looking through your files…" }));
  try { PHOTOS.list = await api("GET", "/api/photos"); } catch (e) { return fail(e); }
  const list = PHOTOS.list;
  if (!list.length) { put(c, head, emptyCard("image", "No photos yet", "Back up your phone's camera roll, or upload pictures to any folder: they all show here, by the day they were taken.")); return; }
  if (PHOTOS.tab === "map") {
    const placed = list.filter(p => p.lat || p.lon);
    put(c, head, placed.length ? el("div", { class: "card", style: { padding: 0, overflow: "hidden" } }, photoMap(placed)) :
      emptyCard("place", "No places yet", "Photos taken with location turned on show up on the map."),
      el("p", { class: "muted", style: { fontSize: "12px" }, text: "Map © OpenStreetMap contributors. The map pictures come from openstreetmap.org; your photos stay in Cove." }));
    return;
  }
  PHOTOS.sel.clear();
  const selBar = el("div", { class: "selbar hide" });
  const drawSel = () => {
    selBar.classList.toggle("hide", !PHOTOS.sel.size);
    put(selBar, el("b", { text: `${PHOTOS.sel.size} selected` }), el("div", { class: "grow" }),
      btn("Add to album", "plus", () => pickAlbum([...PHOTOS.sel])), btn("Clear", "close", () => { PHOTOS.sel.clear(); c.querySelectorAll(".ph.sel").forEach(x => x.classList.remove("sel")); drawSel(); }, "ghost"));
  };
  const groups = [];
  for (const p of list) {
    const k = new Date(p.taken * 1000).toLocaleDateString([], { month: "long", year: "numeric" });
    if (!groups.length || groups[groups.length - 1][0] !== k) groups.push([k, []]);
    groups[groups.length - 1][1].push(p);
  }
  const open = p => {
    const items = list.map(x => ({ ...x, path: x.path }));
    openViewer({ items, index: list.indexOf(p), url: i => files.content(i.path), thumb: (i, w) => files.thumb(i.path, w), download: i => files.download(i.path),
      buttons: (i) => [el("button", { class: "btn", onclick: () => pickAlbum([i.path]) }, icon("plus"), el("span", { class: "lbl", text: "Album" })),
        el("span", { class: "muted", style: { fontSize: "13px" }, text: new Date(i.taken * 1000).toLocaleString([], { dateStyle: "medium", timeStyle: "short" }) })] });
  };
  let shown = 0;
  const wrap = el("div");
  const more = () => { // a few hundred at a time
    let n = 0;
    while (shown < groups.length && n < 400) {
      const [k, ps] = groups[shown++];
      n += ps.length;
      wrap.append(el("h3", { class: "sec", text: k }), el("div", { class: "pgrid" }, ps.map(p => {
        const t = el("div", { class: "ph", onclick: e => {
          if (PHOTOS.sel.size || e.shiftKey || e.metaKey) { PHOTOS.sel.has(p.path) ? PHOTOS.sel.delete(p.path) : PHOTOS.sel.add(p.path); t.classList.toggle("sel"); drawSel(); return; }
          open(p);
        } }, p.kind === "image" ? el("img", { src: files.thumb(p.path, 320), loading: "lazy", alt: p.name }) : el("div", { class: "vid" }, icon("play")),
          el("button", { class: "tick", "aria-label": "Select", onclick: e => { e.stopPropagation(); PHOTOS.sel.has(p.path) ? PHOTOS.sel.delete(p.path) : PHOTOS.sel.add(p.path); t.classList.toggle("sel"); drawSel(); } }, icon("check")));
        return t;
      })));
    }
  };
  more();
  put(c, head, selBar, wrap);
  c.onscroll = () => { if (c.scrollTop + c.clientHeight > c.scrollHeight - 600 && shown < groups.length) more(); };
}

// backUpPhotos uploads what isn't in Cove yet into Photos/YEAR/MONTH.
async function backUpPhotos(list, c) {
  if (!list.length) return;
  let have;
  try { have = await api("POST", "/api/photos/have", { files: list.map(f => ({ name: f.name, size: f.size })) }); } catch (e) { return fail(e); }
  const todo = list.filter((f, i) => !have[i]);
  if (!todo.length) return toast("All of them are backed up already");
  uploader.add(todo.map(f => {
    const d = new Date(f.lastModified || Date.now());
    return { file: f, path: `/Photos/${d.getFullYear()}/${String(d.getMonth() + 1).padStart(2, "0")}/${f.name}`, share: null };
  }));
  toast(`Backing up ${todo.length} (${list.length - todo.length} were there already)`);
}

const albumPhotoURL = (id, it, w) => `/api/albums/${id}/photo?owner=${encodeURIComponent(it.owner)}&path=${encodeURIComponent(it.path)}` + (w ? "&w=" + w : "");

function albumDialog(a, c) {
  const name = input(a ? a.name : "", { placeholder: "Summer 2026" });
  const shared = el("input", { type: "checkbox", checked: a ? a.shared : true });
  dialog({ title: a ? "Album" : "New album", body: el("div", {}, field("Name", name), el("label", { class: "check" }, shared, "Shared: everyone at home sees it and can add their photos")),
    buttons: [a ? { label: "Delete album", danger: true, action: async () => { if (!(await confirmBox("Delete this album?", "The photos stay in everyone's files.", "Delete", true))) return false; await api("DELETE", "/api/albums/" + a.id).catch(fail); location.hash = "#/photos"; } } : null, "grow",
      { label: "Cancel" }, { label: "Save", primary: true, action: async () => {
        const r = a ? await api("PUT", "/api/albums/" + a.id, { name: name.value, shared: shared.checked }) : await api("POST", "/api/albums", { name: name.value, shared: shared.checked });
        if (a) showAlbum(c, a.id); else { PHOTOS.tab = "albums"; location.hash = "#/photos/album/" + r.id; }
      } }].filter(Boolean) });
}

async function pickAlbum(paths) {
  let albums;
  try { albums = (await api("GET", "/api/albums")).filter(a => a.shared || a.owner === S.me.user.name); } catch (e) { return fail(e); }
  const add = async id => { try { const r = await api("POST", `/api/albums/${id}/items`, { paths }); toast(`${r.added} added`); } catch (e) { fail(e); } };
  const d = dialog({ title: "Add to an album", body: el("div", { class: "items-list card", style: { boxShadow: "none" } },
    albums.map(a => el("div", { style: { cursor: "pointer" }, onclick: async () => { d.close(); await add(a.id); } }, icon("image"), el("div", { class: "grow", text: a.name }), el("span", { class: "muted", text: a.count + "" }))),
    el("div", { style: { cursor: "pointer" }, onclick: async () => {
      d.close();
      const name = await ask("New album", "Name", "", "Create");
      if (!name) return;
      try { const r = await api("POST", "/api/albums", { name, shared: true }); await add(r.id); } catch (e) { fail(e); }
    } }, icon("plus"), el("div", { class: "grow", text: "New shared album…" }))), buttons: [{ label: "Cancel" }] });
}

async function showAlbum(c, id) {
  let r;
  try { r = await api("GET", "/api/albums/" + id); } catch (e) { return fail(e); }
  const a = r.album;
  const items = r.items.map(it => ({ ...it, name: it.name }));
  put(c, 
    pageHead(a.name, `${items.length} photos${a.shared ? " · shared" : ""}`, btn("All albums", "left", () => { PHOTOS.tab = "albums"; location.hash = "#/photos"; }, "ghost"),
      a.shared || a.owner === S.me.user.name ? btn("Add my photos", "plus", () => { PHOTOS.tab = "timeline"; location.hash = "#/photos"; toast("Select photos (the ✓ on each), then Add to album"); }) : null,
      a.mine ? btn("", "settings", () => albumDialog(a, c), "ghost icon") : null),
    items.length ? el("div", { class: "pgrid" }, items.map((it, i) => el("div", { class: "ph", onclick: () => openViewer({ items, index: i, url: x => albumPhotoURL(a.id, x), thumb: (x, w) => albumPhotoURL(a.id, x, w),
      download: x => albumPhotoURL(a.id, x) + "&download=1",
      buttons: (x, close) => (a.mine || x.owner === S.me.user.name) ? [el("button", { class: "btn", onclick: async () => {
        await api("DELETE", `/api/albums/${a.id}/items?owner=${encodeURIComponent(x.owner)}&path=${encodeURIComponent(x.path)}`).catch(fail); close(); showAlbum(c, id);
      } }, icon("minus"), el("span", { class: "lbl", text: "Remove from album" }))] : [] }) },
      it.kind === "image" ? el("img", { src: albumPhotoURL(a.id, it, 320), loading: "lazy", alt: it.name }) : el("div", { class: "vid" }, icon("play")),
      it.owner !== S.me.user.name ? el("span", { class: "by", text: nameOf(it.owner) }) : null))) :
      emptyCard("image", "This album is empty", "Add photos from All photos: tick them, then Add to album."));
  people();
}

// photoMap is a small map (OpenStreetMap tiles) with the photos' places; dragging pans, the wheel or buttons zoom.
function photoMap(list) {
  const box = el("div", { class: "pmap" });
  const tiles = el("div", { class: "tiles" });
  const marks = el("div", { class: "marks" });
  box.append(tiles, marks,
    el("div", { class: "zoom" }, el("button", { onclick: () => setZoom(z + 1) }, "+"), el("button", { onclick: () => setZoom(z - 1) }, "−")));
  const proj = (lat, lon, zz) => {
    const s = 256 * 2 ** zz, r = Math.PI / 180;
    return [(lon + 180) / 360 * s, (1 - Math.log(Math.tan(lat * r) + 1 / Math.cos(lat * r)) / Math.PI) / 2 * s];
  };
  // start: fit all
  const lats = list.map(p => p.lat), lons = list.map(p => p.lon);
  const cLat = (Math.min(...lats) + Math.max(...lats)) / 2, cLon = (Math.min(...lons) + Math.max(...lons)) / 2;
  let z = 2;
  for (let zz = 16; zz >= 2; zz--) {
    const [x1, y1] = proj(Math.max(...lats), Math.min(...lons), zz), [x2, y2] = proj(Math.min(...lats), Math.max(...lons), zz);
    if (x2 - x1 < 700 && y2 - y1 < 420) { z = zz; break; }
  }
  let [cx, cy] = proj(cLat, cLon, z);
  const draw = () => {
    const W = box.clientWidth || 800, H = box.clientHeight || 480;
    const x0 = cx - W / 2, y0 = cy - H / 2, n = 2 ** z;
    const ts = [];
    for (let tx = Math.floor(x0 / 256); tx * 256 < x0 + W; tx++) for (let ty = Math.floor(y0 / 256); ty * 256 < y0 + H; ty++) {
      if (ty < 0 || ty >= n) continue;
      ts.push(el("img", { src: `https://tile.openstreetmap.org/${z}/${((tx % n) + n) % n}/${ty}.png`, alt: "", draggable: false, style: { left: (tx * 256 - x0) + "px", top: (ty * 256 - y0) + "px" } }));
    }
    put(tiles, ...ts);
    // group photos that land within 40 px of each other
    const groups = [];
    for (const p of list) {
      const [px, py] = proj(p.lat, p.lon, z);
      const g = groups.find(g => Math.abs(g.x - px) < 40 && Math.abs(g.y - py) < 40);
      if (g) g.ps.push(p); else groups.push({ x: px, y: py, ps: [p] });
    }
    put(marks, ...groups.filter(g => g.x > x0 - 40 && g.x < x0 + W + 40 && g.y > y0 - 40 && g.y < y0 + H + 40).map(g => el("button", { class: "mark", style: { left: (g.x - x0) + "px", top: (g.y - y0) + "px" },
      onclick: () => openViewer({ items: g.ps, index: 0, url: i => files.content(i.path), thumb: (i, w) => files.thumb(i.path, w), download: i => files.download(i.path) }) },
      g.ps[0].kind === "image" ? el("img", { src: files.thumb(g.ps[0].path, 120), alt: "" }) : icon("play"), g.ps.length > 1 ? el("span", { text: g.ps.length }) : null)));
  };
  const setZoom = nz => {
    nz = Math.max(1, Math.min(18, nz));
    if (nz === z) return;
    const f = 2 ** (nz - z); cx *= f; cy *= f; z = nz; draw();
  };
  let drag = null;
  box.addEventListener("pointerdown", e => { if (e.target.closest("button")) return; drag = [e.clientX, e.clientY]; box.setPointerCapture(e.pointerId); });
  box.addEventListener("pointermove", e => { if (!drag) return; cx -= e.clientX - drag[0]; cy -= e.clientY - drag[1]; drag = [e.clientX, e.clientY]; draw(); });
  box.addEventListener("pointerup", () => { drag = null; });
  box.addEventListener("wheel", e => { e.preventDefault(); setZoom(z + (e.deltaY < 0 ? 1 : -1)); }, { passive: false });
  setTimeout(draw, 0);
  return box;
}

// --- scanning: photograph pages, straighten them, save a PDF

function scanDialog(folder) {
  const pages = []; // {img, corners: [[x,y]*4] in image pixels, filter}
  const strip = el("div", { class: "scanstrip" });
  const cam = el("input", { type: "file", accept: "image/*", capture: "environment", multiple: true, class: "hide", onchange: async e => {
    for (const f of e.target.files) {
      const img = await loadImage(f);
      const w = img.naturalWidth, h = img.naturalHeight, m = 0.04;
      const pg = { img, corners: [[w * m, h * m], [w * (1 - m), h * m], [w * (1 - m), h * (1 - m)], [w * m, h * (1 - m)]], filter: "doc" };
      pages.push(pg);
    }
    cam.value = "";
    drawStrip();
    if (pages.length) editPage(pages[pages.length - 1]);
  } });
  const name = input("Scan " + new Date().toLocaleDateString([], { year: "numeric", month: "short", day: "numeric" }), { placeholder: "Name" });
  const where = input(folder || "/Scans");
  const drawStrip = () => put(strip, ...pages.map((p, i) => {
    const t = el("div", { class: "spage", onclick: () => editPage(p) }, el("img", { src: p.img.src, alt: "" }), el("span", { text: i + 1 }),
      el("button", { class: "x", type: "button", onclick: e => { e.stopPropagation(); pages.splice(i, 1); drawStrip(); } }, icon("close")));
    return t;
  }), el("button", { class: "spage add", type: "button", onclick: () => cam.click() }, icon("plus"), "Page"));
  drawStrip();
  const editor = el("div", { class: "scanedit" }, el("p", { class: "muted", text: "Take a photo of each page. Then drag the corners onto the page's corners." }));
  const editPage = pg => {
    const cv = el("canvas", { class: "scancv" });
    const filter = select([["doc", "Document (clean white)"], ["gray", "Gray"], ["color", "Color"], ["bw", "Black and white"]], pg.filter, { onchange: e => { pg.filter = e.target.value; } });
    const rot = btn("Turn", "restore", () => { pg.img = rotateImage(pg.img); const w = pg.img.width, h = pg.img.height, m = 0.04; pg.corners = [[w * m, h * m], [w * (1 - m), h * m], [w * (1 - m), h * (1 - m)], [w * m, h * (1 - m)]]; editPage(pg); });
    put(editor, el("div", { class: "row", style: { alignItems: "center", marginBottom: "8px" } }, field("Look", filter), rot), cv);
    const img = pg.img, W = img.naturalWidth || img.width, H = img.naturalHeight || img.height;
    const scale = Math.min(1, 520 / W, 520 / H);
    cv.width = W * scale; cv.height = H * scale;
    const ctx = cv.getContext("2d");
    const paint = () => {
      ctx.drawImage(img, 0, 0, cv.width, cv.height);
      ctx.strokeStyle = "#2f6fed"; ctx.lineWidth = 2; ctx.fillStyle = "rgba(47,111,237,.12)";
      ctx.beginPath(); pg.corners.forEach(([x, y], i) => i ? ctx.lineTo(x * scale, y * scale) : ctx.moveTo(x * scale, y * scale)); ctx.closePath(); ctx.fill(); ctx.stroke();
      ctx.fillStyle = "#2f6fed";
      pg.corners.forEach(([x, y]) => { ctx.beginPath(); ctx.arc(x * scale, y * scale, 9, 0, 7); ctx.fill(); });
    };
    paint();
    let dragging = -1;
    const pos = e => { const r = cv.getBoundingClientRect(); return [(e.clientX - r.left) * cv.width / r.width / scale, (e.clientY - r.top) * cv.height / r.height / scale]; };
    cv.onpointerdown = e => {
      const [x, y] = pos(e);
      let best = 1e9;
      pg.corners.forEach(([cx, cy], i) => { const d = Math.hypot(cx - x, cy - y); if (d < best) { best = d; dragging = i; } });
      if (best * scale > 40) dragging = -1; else cv.setPointerCapture(e.pointerId);
    };
    cv.onpointermove = e => { if (dragging < 0) return; const [x, y] = pos(e); pg.corners[dragging] = [Math.max(0, Math.min(W, x)), Math.max(0, Math.min(H, y))]; paint(); };
    cv.onpointerup = () => { dragging = -1; };
  };
  dialog({ title: "Scan a document", wide: true, body: el("div", {}, cam, strip, editor, el("div", { class: "row" }, field("Name", name), field("Folder", where)),
    S.me.ocr ? el("p", { class: "muted", style: { fontSize: "12.5px" }, text: "Cove reads the words on the pages, so search finds this document." }) : null),
  buttons: [{ label: "Cancel" }, { label: "Save PDF", primary: true, action: async () => {
    if (!pages.length) { toast("Take a photo of a page first", true); return false; }
    const fd = new FormData();
    for (const [i, p] of pages.entries()) fd.append("page", await flatten(p), `page${i + 1}.jpg`);
    const path = joinPath(where.value.trim().replace(/\/+$/, "") || "/", (name.value.trim() || "Scan") + ".pdf");
    const res = await fetch("/api/scan?path=" + encodeURIComponent(path), { method: "POST", body: fd, headers: { "X-Cove": "1" } });
    const out = await res.json();
    if (!res.ok) throw new Error(out.error || "Saving didn't work");
    toast("Saved " + out.path);
    if (location.hash.startsWith("#/files")) refreshSoon();
  } }] });
  setTimeout(() => cam.click(), 100);
}

function loadImage(file) {
  return new Promise((ok, bad) => { const img = new Image(); img.onload = () => ok(img); img.onerror = bad; img.src = URL.createObjectURL(file); });
}
function rotateImage(img) {
  const cv = document.createElement("canvas");
  cv.width = img.naturalHeight || img.height; cv.height = img.naturalWidth || img.width;
  const ctx = cv.getContext("2d");
  ctx.translate(cv.width, 0); ctx.rotate(Math.PI / 2); ctx.drawImage(img, 0, 0);
  return cv;
}

// flatten warps the page's four corners into a rectangle (a homography, pixel by pixel) and cleans it up.
async function flatten(p) {
  const [a, b, cc, d] = p.corners;
  const wPx = Math.max(Math.hypot(b[0] - a[0], b[1] - a[1]), Math.hypot(cc[0] - d[0], cc[1] - d[1]));
  const hPx = Math.max(Math.hypot(d[0] - a[0], d[1] - a[1]), Math.hypot(cc[0] - b[0], cc[1] - b[1]));
  const W = Math.round(Math.min(1700, wPx)), H = Math.round(W * hPx / wPx);
  const srcCv = document.createElement("canvas");
  const iw = p.img.naturalWidth || p.img.width, ih = p.img.naturalHeight || p.img.height;
  srcCv.width = iw; srcCv.height = ih;
  const sctx = srcCv.getContext("2d");
  sctx.drawImage(p.img, 0, 0);
  const src = sctx.getImageData(0, 0, iw, ih).data;
  const Hm = homography([[0, 0], [W, 0], [W, H], [0, H]], [a, b, cc, d]);
  const out = document.createElement("canvas");
  out.width = W; out.height = H;
  const octx = out.getContext("2d");
  const img = octx.createImageData(W, H), o = img.data;
  for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) {
    const den = Hm[6] * x + Hm[7] * y + 1;
    const sx = Math.round((Hm[0] * x + Hm[1] * y + Hm[2]) / den), sy = Math.round((Hm[3] * x + Hm[4] * y + Hm[5]) / den);
    const k = (y * W + x) * 4;
    if (sx < 0 || sy < 0 || sx >= iw || sy >= ih) { o[k] = o[k + 1] = o[k + 2] = 255; o[k + 3] = 255; continue; }
    const s = (sy * iw + sx) * 4;
    let r = src[s], g = src[s + 1], bl = src[s + 2];
    if (p.filter !== "color") {
      let v = 0.299 * r + 0.587 * g + 0.114 * bl;
      if (p.filter === "doc") v = Math.max(0, Math.min(255, (v - 60) * 255 / 140)); // whiten paper, darken ink
      if (p.filter === "bw") v = v > 140 ? 255 : 0;
      r = g = bl = v;
    }
    o[k] = r; o[k + 1] = g; o[k + 2] = bl; o[k + 3] = 255;
  }
  octx.putImageData(img, 0, 0);
  return new Promise(ok => out.toBlob(ok, "image/jpeg", 0.82));
}

// homography solves for the 8 numbers that take the four points from to the four points to.
function homography(from, to) {
  const A = [], B = [];
  for (let i = 0; i < 4; i++) {
    const [x, y] = from[i], [u, v] = to[i];
    A.push([x, y, 1, 0, 0, 0, -u * x, -u * y]); B.push(u);
    A.push([0, 0, 0, x, y, 1, -v * x, -v * y]); B.push(v);
  }
  for (let c = 0; c < 8; c++) { // Gaussian elimination
    let p = c;
    for (let r = c + 1; r < 8; r++) if (Math.abs(A[r][c]) > Math.abs(A[p][c])) p = r;
    [A[c], A[p]] = [A[p], A[c]]; [B[c], B[p]] = [B[p], B[c]];
    for (let r = 0; r < 8; r++) {
      if (r === c) continue;
      const f = A[r][c] / A[c][c];
      for (let k = c; k < 8; k++) A[r][k] -= f * A[c][k];
      B[r] -= f * B[c];
    }
  }
  return B.map((b, i) => b / A[i][i]);
}

// --- home records: appliances, warranties, documents

const REC_CATS = [["appliance", "Appliances"], ["electronics", "Electronics"], ["vehicle", "Vehicles"], ["document", "Documents and IDs"], ["insurance", "Insurance"], ["home", "Home"], ["other", "Other"]];

async function showRecords(c) {
  if (isKid()) { location.hash = "#/home"; return; }
  let list;
  try { [list] = await Promise.all([HH.list("record"), people()]); } catch (e) { return fail(e); }
  const soon = ymd(addDays(new Date(), 60)), today = ymd(new Date());
  const flag = r => {
    const out = [];
    for (const [d, w] of [[r.warranty, "Warranty"], [r.expires, "Expires"]]) {
      if (!d) continue;
      out.push(el("span", { class: "pill" + (d < today ? " gone" : d <= soon ? " warn" : ""), text: `${w} ${d < today ? "ended" : ""} ${parseYmd(d).toLocaleDateString([], { dateStyle: "medium" })}` }));
    }
    return out;
  };
  const by = {};
  for (const r of list) (by[r.category || "other"] = by[r.category || "other"] || []).push(r);
  put(c, pageHead("Home records", "Warranties, manuals, serial numbers, passports. Cove reminds you before things run out.", btn("Add", "plus", () => recordDialog(null, c), "primary")),
    ...(list.length ? REC_CATS.filter(([k]) => by[k]).flatMap(([k, label]) => [el("h3", { class: "sec", text: label }),
      el("div", { class: "card items-list" }, by[k].sort((a, b) => a.name.localeCompare(b.name)).map(r => el("div", { style: { cursor: "pointer" }, onclick: () => recordDialog(r, c) },
        icon(k === "document" ? "key" : k === "vehicle" ? "place" : "file"),
        el("div", { class: "grow" }, el("div", { style: { fontWeight: 600 } }, r.name, r.private ? el("span", { class: "pill", style: { marginLeft: "6px" }, text: "private" }) : null),
          el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [r.person && nameOf(r.person), r.brand, r.model, r.serial && "S/N " + r.serial, (r.files || []).length ? `📎 ${r.files.length}` : ""].filter(Boolean).join(" · ") })),
        ...flag(r))))]) : [emptyCard("key", "Nothing here yet", "Add the fridge, the car, everyone's passports: with the receipt and manual attached, and dates Cove watches.")]));
}

function recordDialog(r, c) {
  r = r || { category: "appliance", files: [] };
  const f = {
    name: input(r.name, { placeholder: "Dishwasher" }), category: select(REC_CATS, r.category),
    person: select([["", "—"], ...PEOPLE.map(p => [p.name, p.display || p.name])], r.person),
    brand: input(r.brand), model: input(r.model), serial: input(r.serial), where: input(r.where, { placeholder: "Bought at, or kept in" }),
    bought: input(r.bought, { type: "date" }), price: input(r.price || "", { type: "number", step: "0.01" }),
    warranty: input(r.warranty, { type: "date" }), expires: input(r.expires, { type: "date" }), notes: textarea(r.notes),
  };
  const priv = el("input", { type: "checkbox", checked: r.private });
  let attached = [...(r.files || [])];
  const filesBox = el("div", { class: "items-list" });
  const drawFiles = () => put(filesBox, ...attached.map(p => el("div", {}, kindIcon(kindOfName(p)), el("a", { class: "grow", href: files.content(p), target: "_blank", text: baseName(p) }),
    el("button", { class: "btn ghost icon", type: "button", onclick: () => { attached = attached.filter(x => x !== p); drawFiles(); } }, icon("close")))));
  drawFiles();
  const up = el("input", { type: "file", multiple: true, class: "hide", onchange: async e => {
    for (const file of e.target.files) {
      const path = `/Home records/${(f.name.value || "Untitled").replace(/[\/\\]/g, "-")}/${file.name}`;
      try { const res = await xhrSend("PUT", "/api/files/content?path=" + encPath(path), file); attached.push(res.path || path); } catch (err) { fail(err); }
    }
    drawFiles();
  } });
  dialog({ title: r.id ? r.name : "New home record", wide: true, body: el("div", {},
    el("div", { class: "row" }, field("Name", f.name), field("Kind", f.category), field("Whose", f.person)),
    el("div", { class: "row" }, field("Brand", f.brand), field("Model", f.model), field("Serial or number", f.serial)),
    el("div", { class: "row" }, field("Bought", f.bought), field("Price", f.price), field("Where", f.where)),
    el("div", { class: "row" }, field("Warranty ends", f.warranty), field("Expires (passports, insurance)", f.expires)),
    field("Notes", f.notes),
    el("div", { class: "label", text: "Receipts, manuals, photos" }), filesBox, up, btn("Attach files", "upload", () => up.click(), "ghost"),
    el("label", { class: "check", style: { marginTop: "10px" } }, priv, "Private (only you see it)")),
  buttons: [r.id ? { label: "Delete", danger: true, action: async () => { if (!(await confirmBox("Delete this record?", "Attached files stay in your files.", "Delete", true))) return false; await HH.del("record", r.id).catch(fail); showRecords(c); } } : null, "grow",
    { label: "Cancel" }, { label: "Save", primary: true, action: async () => {
      const o = { id: r.id, private: priv.checked, files: attached, price: parseFloat(f.price.value) || 0 };
      for (const k of ["name", "category", "person", "brand", "model", "serial", "where", "bought", "warranty", "expires", "notes"]) o[k] = f[k].value;
      await HH.save("record", o);
      showRecords(c);
    } }].filter(Boolean) });
}
function kindOfName(p) {
  const e = extOf(p);
  return ["jpg", "jpeg", "png", "heic", "webp"].includes(e) ? "image" : e === "pdf" ? "pdf" : ["doc", "docx"].includes(e) ? "doc" : "file";
}

// --- budget: bills, spending, settling up

const BUDGET = { month: new Date().toISOString().slice(0, 7) };

async function showBudget(c) {
  if (isKid()) { location.hash = "#/home"; return; }
  let b;
  try { [b] = await Promise.all([api("GET", "/api/budget?month=" + BUDGET.month), people()]); } catch (e) { return fail(e); }
  const m = new Date(BUDGET.month + "-01T12:00:00");
  const shift = n => { const d = new Date(m); d.setMonth(d.getMonth() + n); BUDGET.month = d.toISOString().slice(0, 7); showBudget(c); };
  const cats = Object.entries(b.byCategory).sort((x, y) => y[1] - x[1]);
  const top = cats.length ? cats[0][1] : 1;
  const today = ymd(new Date());
  put(c, 
    pageHead("Budget", null, btn("", "left", () => shift(-1), "ghost icon"), el("b", { text: m.toLocaleDateString([], { month: "long", year: "numeric" }) }), btn("", "right", () => shift(1), "ghost icon"),
      btn("Bill", "plus", () => billDialog(null, c)), btn("Spending", "plus", () => expenseDialog(null, c), "primary")),
    el("div", { class: "stats" },
      el("div", { class: "card stat" }, el("small", { text: "Bills this month" }), el("b", { text: money(b.billTotal) }), el("span", { class: "muted", text: `${money(b.billPaid)} paid` })),
      el("div", { class: "card stat" }, el("small", { text: "Other spending" }), el("b", { text: money(b.spent) })),
      el("div", { class: "card stat" }, el("small", { text: "Altogether" }), el("b", { text: money(b.billTotal + b.spent) }))),
    b.settle.length ? el("div", { class: "card section" }, el("h3", { text: "Settle up" }), ...b.settle.map(s => el("div", { class: "row", style: { alignItems: "center", marginBottom: "6px" } },
      el("div", { class: "grow" }, el("b", { text: nameOf(s.from) }), ` owes `, el("b", { text: nameOf(s.to) }), " " + money(s.amount)),
      btn("Mark paid back", "check", async () => { try { await HH.save("expense", { title: "Paid back", amount: s.amount, paidBy: s.from, split: [s.to], settle: true, date: today }); showBudget(c); } catch (e) { fail(e); } })))) : null,
    el("div", { class: "hgrid" },
      el("div", { class: "card hcard" }, el("div", { class: "hhead" }, icon("calendar"), el("h3", { text: "Bills" })),
        b.bills.length ? el("div", { class: "items-list", style: { margin: "0 -16px -14px" } }, b.bills.map(x => el("div", {},
          el("input", { type: "checkbox", checked: x.isPaid, disabled: x.autopay, title: x.autopay ? "Paid automatically" : "Paid", onclick: async e => {
            const paid = new Set(x.paid || []);
            e.target.checked ? paid.add(x.period) : paid.delete(x.period);
            try { await HH.save("bill", { ...x, paid: [...paid] }); showBudget(c); } catch (err) { fail(err); }
          } }),
          el("div", { class: "grow", style: { cursor: "pointer" }, onclick: () => billDialog(x, c) }, el("div", { style: { fontWeight: 600 }, text: x.name }),
            el("div", { class: "muted", style: { fontSize: "12.5px", color: !x.isPaid && x.dueDate < today ? "var(--danger)" : "" }, text: `Due ${parseYmd(x.dueDate).toLocaleDateString([], { month: "short", day: "numeric" })}${x.autopay ? " · autopay" : ""}${x.payer ? " · " + nameOf(x.payer) : ""}` })),
          el("b", { text: money(x.amount) })))) : el("p", { class: "muted", text: "No bills this month." })),
      el("div", { class: "card hcard" }, el("div", { class: "hhead" }, icon("sheet"), el("h3", { text: "Where it went" })),
        cats.length ? el("div", {}, cats.map(([k, v]) => el("div", { class: "catbar" }, el("div", { class: "row" }, el("span", { class: "grow", text: k }), el("b", { text: money(v) })),
          el("div", { class: "usage" }, el("div", { style: { width: (100 * v / top) + "%" } }))))) : el("p", { class: "muted", text: "Nothing yet." }))),
    el("h3", { class: "sec", text: "Spending" }),
    b.expenses.length ? el("div", { class: "card items-list" }, b.expenses.map(x => el("div", { style: { cursor: "pointer" }, onclick: () => expenseDialog(x, c) },
      el("div", { class: "ficon k-file" }, icon(x.settle ? "restore" : "sheet")),
      el("div", { class: "grow" }, el("div", { style: { fontWeight: 600 }, text: x.settle ? `${nameOf(x.paidBy)} paid ${nameOf(x.split[0])} back` : x.title }),
        el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [parseYmd(x.date).toLocaleDateString([], { month: "short", day: "numeric" }), x.category, x.paidBy && !x.settle ? "paid by " + nameOf(x.paidBy) : "", (x.split || []).length > 1 ? "split " + x.split.length + " ways" : ""].filter(Boolean).join(" · ") })),
      el("b", { text: money(x.amount) })))) : emptyCard("sheet", "No spending this month", "Write down shared costs: Cove works out who owes whom."));
}

function billDialog(x, c) {
  x = x || { repeat: "monthly", day: new Date().getDate(), month: new Date().getMonth() + 1 };
  const name = input(x.name, { placeholder: "Electricity" }), amount = input(x.amount || "", { type: "number", step: "0.01" });
  const repeat = select([["monthly", "Every month"], ["yearly", "Every year"], ["once", "Once"]], x.repeat);
  const day = input(x.day, { type: "number", min: 1, max: 31 }), month = select([...Array(12)].map((_, i) => [i + 1, new Date(2000, i, 1).toLocaleDateString([], { month: "long" })]), x.month);
  const date = input(x.date || ymd(new Date()), { type: "date" });
  const payer = select([["", "—"], ...PEOPLE.map(p => [p.name, p.display || p.name])], x.payer || S.me.user.name);
  const category = input(x.category, { placeholder: "Utilities", list: "cats" });
  const autopay = el("input", { type: "checkbox", checked: x.autopay });
  const when = el("div", { class: "row" });
  const drawWhen = () => put(when, ...(repeat.value === "once" ? [field("Due", date)] : repeat.value === "yearly" ? [field("Month", month), field("Day", day)] : [field("Day of the month", day)]));
  repeat.onchange = drawWhen; drawWhen();
  dialog({ title: x.id ? x.name : "New bill", body: el("div", {}, el("div", { class: "row" }, field("Bill", name), field("Amount ($)", amount)), field("How often", repeat), when,
    el("div", { class: "row" }, field("Who pays", payer), field("Category", category)), el("label", { class: "check" }, autopay, "Paid automatically (no reminders)")),
  buttons: [x.id ? { label: "Delete", danger: true, action: async () => { await HH.del("bill", x.id).catch(fail); showBudget(c); } } : null, "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => {
      await HH.save("bill", { ...x, name: name.value, amount: parseFloat(amount.value) || 0, repeat: repeat.value, day: +day.value, month: +month.value, date: date.value,
        payer: payer.value, category: category.value, autopay: autopay.checked });
      showBudget(c);
    } }].filter(Boolean) });
}

function expenseDialog(x, c) {
  x = x || { date: ymd(new Date()), paidBy: S.me.user.name, split: [] };
  const title = input(x.title, { placeholder: "Groceries" }), amount = input(x.amount || "", { type: "number", step: "0.01" });
  const date = input(x.date, { type: "date" });
  const paidBy = select(PEOPLE.map(p => [p.name, p.display || p.name]), x.paidBy);
  const category = input(x.category, { placeholder: "Food" });
  const split = personChecks(PEOPLE, x.split);
  dialog({ title: x.id ? "Spending" : "Add spending", body: el("div", {}, el("div", { class: "row" }, field("What", title), field("Amount ($)", amount)),
    el("div", { class: "row" }, field("Day", date), field("Paid by", paidBy), field("Category", category)),
    field("Shared between (leave empty if it isn't shared)", split)),
  buttons: [x.id ? { label: "Delete", danger: true, action: async () => { await HH.del("expense", x.id).catch(fail); showBudget(c); } } : null, "grow", { label: "Cancel" },
    { label: "Save", primary: true, action: async () => {
      await HH.save("expense", { ...x, title: title.value, amount: parseFloat(amount.value) || 0, date: date.value, paidBy: paidBy.value, category: category.value, split: split.value() });
      showBudget(c);
    } }].filter(Boolean) });
}

// --- trips: the plan, packing, papers

async function showTrips(c, rest) {
  let list;
  try { [list] = await Promise.all([HH.list("trip"), people()]); } catch (e) { return fail(e); }
  if (rest && rest[0]) {
    const t = list.find(x => String(x.id) === rest[0]);
    if (t) return showTrip(c, t);
  }
  const today = ymd(new Date());
  const upcoming = list.filter(t => t.end >= today).sort((a, b) => a.start.localeCompare(b.start));
  const past = list.filter(t => t.end < today).sort((a, b) => b.start.localeCompare(a.start));
  const card = t => {
    const days = Math.round((parseYmd(t.start) - parseYmd(today)) / 864e5);
    return el("div", { class: "card trip", onclick: () => { location.hash = "#/trips/" + t.id; } },
      el("h3", { text: t.name }), el("div", { class: "muted", text: [t.where, fmtRange(t.start, t.end)].filter(Boolean).join(" · ") }),
      days > 0 ? el("div", { class: "countdown", text: `in ${days} day${days === 1 ? "" : "s"}` }) : t.end >= today ? el("div", { class: "countdown", text: "happening now" }) : null,
      el("div", { class: "muted", style: { fontSize: "12.5px", marginTop: "6px" }, text: [t.owner, ...(t.people || [])].filter((v, i, a) => a.indexOf(v) === i).map(nameOf).join(", ") }));
  };
  put(c, pageHead("Trips", null, btn("New trip", "plus", () => tripDialog(null, c), "primary")),
    upcoming.length ? el("div", { class: "rgrid" }, upcoming.map(card)) : emptyCard("place", "No trips planned", "Plan the days, the bookings and who packs what, together."),
    past.length ? el("h3", { class: "sec", text: "Past trips" }) : null, past.length ? el("div", { class: "rgrid" }, past.map(card)) : null);
}
function fmtRange(a, b) {
  const o = { month: "short", day: "numeric" };
  return a === b ? parseYmd(a).toLocaleDateString([], o) : parseYmd(a).toLocaleDateString([], o) + " – " + parseYmd(b).toLocaleDateString([], o);
}

function tripDialog(t, c) {
  t = t || { people: [], plan: [], packing: [], files: [] };
  const name = input(t.name, { placeholder: "Summer at the lake" }), where = input(t.where, { placeholder: "Lake Tahoe" });
  const start = input(t.start || ymd(addDays(new Date(), 14)), { type: "date" }), end = input(t.end || t.start || ymd(addDays(new Date(), 16)), { type: "date" });
  const who = personChecks(PEOPLE.filter(p => p.name !== (t.owner || S.me.user.name)), t.people);
  const notes = textarea(t.notes, { placeholder: "Anything else" });
  const cal = el("input", { type: "checkbox", checked: !t.id });
  dialog({ title: t.id ? "Edit trip" : "New trip", body: el("div", {}, field("Trip", name), field("Where", where), el("div", { class: "row" }, field("From", start), field("To", end)),
    field("Who's going (besides " + (t.owner ? nameOf(t.owner) : "you") + ")", who), field("Notes", notes),
    t.id ? null : el("label", { class: "check" }, cal, "Put it on my calendar")),
  buttons: [t.id ? { label: "Delete trip", danger: true, action: async () => { if (!(await confirmBox("Delete this trip?", "Its plan and packing list go too.", "Delete", true))) return false; await HH.del("trip", t.id).catch(fail); location.hash = "#/trips"; } } : null, "grow",
    { label: "Cancel" }, { label: "Save", primary: true, action: async () => {
      const o = { ...t, name: name.value, where: where.value, start: start.value, end: end.value, people: who.value(), notes: notes.value };
      if (!t.id && cal.checked) {
        try {
          const cals = await api("GET", "/api/calendars?kind=events");
          const k = cals.find(x => x.owner === S.me.user.name && !x.subscription) || cals[0];
          await api("POST", "/api/events", { cal: k.id, title: "✈️ " + name.value, location: where.value, allDay: true, start: start.value, end: ymd(addDays(parseYmd(end.value < start.value ? start.value : end.value), 1)), tz, invite: who.value() });
        } catch (e) { fail(e); }
      }
      const r = await HH.save("trip", o);
      location.hash = "#/trips/" + r.id;
      if (t.id) showTrips(c, [String(t.id)]);
    } }].filter(Boolean) });
}

function showTrip(c, t) {
  const save = async () => { try { const r = await HH.save("trip", t); Object.assign(t, r); } catch (e) { fail(e); } showTrip(c, t); };
  const days = [];
  for (let d = parseYmd(t.start); ymd(d) <= t.end; d = addDays(d, 1)) days.push(ymd(d));
  const extra = (t.plan || []).filter(p => !days.includes(p.date)).map(p => p.date);
  const everyone = [t.owner, ...(t.people || [])].filter((v, i, a) => a.indexOf(v) === i);
  const packFor = TRIPS.pack;
  const stopDialog = (stop, date) => {
    stop = stop || { date, time: "", title: "", where: "", ref: "", notes: "" };
    const f = { date: input(stop.date, { type: "date" }), time: input(stop.time, { type: "time" }), title: input(stop.title, { placeholder: "Flight to Honolulu" }),
      where: input(stop.where), ref: input(stop.ref, { placeholder: "Booking or flight number" }), notes: textarea(stop.notes) };
    dialog({ title: stop.title || "Add to the plan", body: el("div", {}, el("div", { class: "row" }, field("Day", f.date), field("Time", f.time)), field("What", f.title), field("Where", f.where), field("Booking", f.ref), field("Notes", f.notes)),
      buttons: [stop.title ? { label: "Remove", danger: true, action: () => { t.plan = t.plan.filter(x => x !== stop); save(); } } : null, "grow", { label: "Cancel" },
        { label: "Save", primary: true, action: () => {
          const n = { date: f.date.value, time: f.time.value, title: f.title.value, where: f.where.value, ref: f.ref.value, notes: f.notes.value };
          t.plan = (t.plan || []).filter(x => x !== stop).concat([n]); save();
        } }].filter(Boolean) });
  };
  const newItem = input("", { placeholder: "Add something to pack…", onkeydown: e => {
    if (e.key === "Enter" && newItem.value.trim()) { t.packing = (t.packing || []).concat([{ text: newItem.value.trim(), who: packFor === "*" ? "" : packFor, done: false }]); save(); }
  } });
  const up = el("input", { type: "file", multiple: true, class: "hide", onchange: async e => {
    for (const file of e.target.files) {
      const path = `/Trips/${t.name.replace(/[\/\\]/g, "-")}/${file.name}`;
      try { const res = await xhrSend("PUT", "/api/files/content?path=" + encPath(path), file); t.files = (t.files || []).concat([res.path || path]); } catch (err) { fail(err); }
    }
    save();
  } });
  const packing = (t.packing || []).filter(p => packFor === "*" || p.who === packFor || !p.who);
  put(c, 
    pageHead(t.name, [t.where, fmtRange(t.start, t.end)].filter(Boolean).join(" · "), btn("Trips", "left", () => { location.hash = "#/trips"; }, "ghost"), btn("Edit", "edit", () => tripDialog(t, c))),
    el("p", { class: "muted", text: "Going: " + everyone.map(nameOf).join(", ") + (t.notes ? " · " + t.notes : "") }),
    el("div", { class: "hgrid" },
      el("div", { class: "card hcard" }, el("div", { class: "hhead" }, icon("calendar"), el("h3", { text: "Plan" }), el("div", { class: "grow" }), btn("Add", "plus", () => stopDialog(null, t.start), "ghost")),
        ...[...days, ...extra].map(d => {
          const here = (t.plan || []).filter(p => p.date === d);
          return el("div", { class: "tday" }, el("div", { class: "tdate", text: parseYmd(d).toLocaleDateString([], { weekday: "short", month: "short", day: "numeric" }) }),
            here.length ? here.map(p => el("div", { class: "tstop", onclick: () => stopDialog(p) }, el("b", { text: (p.time ? p.time + " " : "") + p.title }),
              p.where || p.ref ? el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [p.where, p.ref && "Ref " + p.ref].filter(Boolean).join(" · ") }) : null)) :
              el("button", { class: "linkbtn", onclick: () => stopDialog(null, d), text: "+ add" }));
        })),
      el("div", { class: "card hcard" }, el("div", { class: "hhead" }, icon("check"), el("h3", { text: "Packing" }), el("div", { class: "grow" }),
        select([["*", "Everyone"], ...everyone.map(n => [n, nameOf(n)])], packFor, { style: { width: "auto" }, onchange: e => { TRIPS.pack = e.target.value; showTrip(c, t); } })),
        el("div", { class: "flist" }, packing.map(p => el("label", { class: p.done ? "done" : "" }, el("input", { type: "checkbox", checked: p.done, onclick: () => { p.done = !p.done; save(); } }),
          el("span", { text: p.text }), p.who ? el("small", { text: nameOf(p.who) }) : null))), newItem,
        el("div", { class: "hhead", style: { marginTop: "14px" } }, icon("file"), el("h3", { text: "Tickets and papers" }), el("div", { class: "grow" }), up, btn("Attach", "upload", () => up.click(), "ghost")),
        el("div", { class: "items-list" }, (t.files || []).map(p => el("div", {}, kindIcon(kindOfName(p)), el("a", { class: "grow", href: files.content(p), target: "_blank", text: baseName(p) })))))));
}
const TRIPS = { pack: "*" };

// --- chat: the family's rooms

const CHAT = { room: null };

async function showChat(c, rest) {
  let rooms;
  try { [rooms] = await Promise.all([api("GET", "/api/chat"), people()]); } catch (e) { return fail(e); }
  const id = rest && rest[0] ? rest[0] : (innerWidth > 760 && rooms[0] ? String(rooms[0].id) : null);
  CHAT.room = id;
  const list = el("div", { class: "rooms card" }, rooms.map(r => el("button", { class: "room" + (String(r.id) === id ? " on" : ""), onclick: () => { location.hash = "#/chat/" + r.id; } },
    el("div", { class: "avatar", text: r.name.slice(0, 1).toUpperCase() }),
    el("div", { class: "grow", style: { minWidth: 0 } }, el("div", { style: { fontWeight: 600 }, text: r.name }), el("div", { class: "muted ellip", style: { fontSize: "12.5px" }, text: r.last ? r.lastWho + ": " + r.last : "No messages yet" })),
    r.unread ? el("span", { class: "badge", text: r.unread }) : null)),
    el("button", { class: "room", onclick: () => roomDialog(c) }, el("div", { class: "avatar", style: { background: "var(--sunk)", color: "var(--muted)" } }, icon("plus")), el("div", { class: "grow", text: "New room" })));
  const pane = el("div", { class: "chatpane card" });
  put(c, el("div", { class: "chat" + (id ? " open" : "") }, list, pane));
  if (id) drawRoom(pane, id, c);
}

async function drawRoom(pane, id, c) {
  let r;
  try { r = await api("GET", `/api/chat/${id}/messages`); } catch (e) { put(pane, el("p", { class: "muted", style: { padding: "20px" }, text: e.message })); return; }
  const msgs = el("div", { class: "msgs" });
  let last = null;
  for (const m of r.messages) {
    const day = new Date(m.created * 1000).toDateString();
    if (day !== last) { msgs.append(el("div", { class: "mday", text: new Date(m.created * 1000).toLocaleDateString([], { weekday: "long", month: "short", day: "numeric" }) })); last = day; }
    msgs.append(el("div", { class: "msg" + (m.mine ? " mine" : "") },
      m.mine ? null : el("div", { class: "mwho", text: m.who }),
      el("div", { class: "bubble", oncontextmenu: e => { if (m.mine || S.me.user.admin) { e.preventDefault(); popMenu(e, [{ label: "Delete", icon: "trash", danger: true, action: async () => { await api("DELETE", "/api/chat-messages/" + m.id).catch(fail); drawRoom(pane, id, c); } }]); } } },
        m.photo ? el("img", { src: `/api/chat-photos/${m.id}?w=640`, alt: "", onclick: () => window.open(`/api/chat-photos/${m.id}?w=1600`, "_blank") }) : null,
        m.text ? el("div", { class: "mtext" }, ...linkify(m.text)) : null),
      el("div", { class: "mtime", text: new Date(m.created * 1000).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }) })));
  }
  const text = el("textarea", { class: "input", rows: 1, placeholder: "Message " + r.room.name, onkeydown: e => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); } },
    oninput: () => { text.style.height = "auto"; text.style.height = Math.min(140, text.scrollHeight) + "px"; } });
  const send = async () => {
    const v = text.value.trim();
    if (!v) return;
    text.value = "";
    try { await api("POST", `/api/chat/${id}/messages`, { text: v }); } catch (e) { fail(e); text.value = v; }
    drawRoom(pane, id, c);
  };
  const photo = el("input", { type: "file", accept: "image/*", class: "hide", onchange: async e => {
    const f = e.target.files[0];
    if (!f) return;
    try { await xhrSend("POST", `/api/chat/${id}/messages`, f); } catch (err) { fail(err); }
    drawRoom(pane, id, c);
  } });
  put(pane, 
    el("div", { class: "chead" }, el("button", { class: "btn ghost icon back", onclick: () => { location.hash = "#/chat"; } }, icon("left")),
      el("b", { text: r.room.name }), el("span", { class: "muted", style: { fontSize: "12.5px" }, text: r.room.members.length ? r.room.members.map(nameOf).join(", ") : "Everyone" }),
      el("div", { class: "grow" }), r.room.createdBy === S.me.user.name || (S.me.user.admin && r.room.createdBy) ? btn("", "trash", async () => {
        if (await confirmBox("Delete " + r.room.name + "?", "Its messages and photos go for good.", "Delete", true)) { await api("DELETE", "/api/chat/" + id).catch(fail); location.hash = "#/chat"; }
      }, "ghost icon") : null),
    msgs.childElementCount ? msgs : el("div", { class: "msgs empty-msgs", text: "Say hi 👋" }),
    el("div", { class: "compose" }, photo, el("button", { class: "btn ghost icon", onclick: () => photo.click(), "aria-label": "Photo" }, icon("image")), text,
      el("button", { class: "btn primary", onclick: send }, "Send")));
  msgs.scrollTop = msgs.scrollHeight;
  if (document.activeElement === document.body || !document.activeElement) text.focus({ preventScroll: true });
  pollNotifications && chatBadge();
}

function linkify(s) {
  const out = [];
  let i = 0;
  for (const m of s.matchAll(/https?:\/\/[^\s<]+/g)) {
    out.push(s.slice(i, m.index), el("a", { href: m[0], target: "_blank", rel: "noopener", text: m[0] }));
    i = m.index + m[0].length;
  }
  out.push(s.slice(i));
  return out;
}

function roomDialog(c) {
  const name = input("", { placeholder: "Parents" });
  const who = personChecks(PEOPLE.filter(p => p.name !== S.me.user.name), []);
  dialog({ title: "New room", body: el("div", {}, field("Name", name), field("Who's in it (none: everyone)", who)),
    buttons: [{ label: "Cancel" }, { label: "Create", primary: true, action: async () => { const r = await api("POST", "/api/chat", { name: name.value, members: who.value() }); location.hash = "#/chat/" + r.id; } }] });
}

async function chatBadge() {
  const b = document.querySelector('[data-nav="chat"] .nbadge');
  if (!b) return;
  try {
    const rooms = await api("GET", "/api/chat");
    const n = rooms.reduce((a, r) => a + r.unread, 0);
    b.textContent = n; b.classList.toggle("hide", !n);
  } catch (_) {}
}

// --- Ask Cove

const ASK = { history: [] };

function showAsk(c) {
  if (!S.me.ai) {
    put(c, pageHead("Ask Cove"), emptyCard("search", "Ask Cove isn't on yet", S.me.user.admin ? "Pick the home AI server (LM Studio or Ollama) in Admin → AI." : "An admin turns it on in Admin → AI, with an AI server on the home network."));
    return;
  }
  const log = el("div", { class: "asklog" });
  const q = el("textarea", { class: "input", rows: 2, placeholder: "When is the dentist? What's for dinner this week? What's our car insurance number?",
    onkeydown: e => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); go(); } } });
  const go = () => { const v = q.value.trim(); if (!v) return; q.value = ""; c.querySelector(".askcard .chips")?.remove(); askStream(v, null, log, ASK.history); };
  const ideas = ["What's on this week?", "Add milk and eggs to the shopping list", "Put the dentist on Thursday at 3", "Which chores are left today?", "What do we still need to pack?"];
  for (const h of ASK.history) log.append(el("div", { class: "qa " + h.role }, h.role === "user" ? h.content : el("div", { class: "md" }, markdown(h.content))));
  put(c, 
    pageHead("Ask Cove", "Answers from your calendar, tasks, fridge, chores, meals, trips, records and documents. It runs on your home network.",
      ASK.history.length ? btn("New conversation", "refresh", () => { ASK.history = []; showAsk(c); }, "ghost") : null),
    el("div", { class: "card askcard" }, log,
      ASK.history.length ? null : el("div", { class: "chips", style: { padding: "0 0 10px" } }, ideas.map(i => el("button", { class: "chip", onclick: () => { q.value = i; go(); } }, i))),
      el("div", { class: "compose" }, q, micButton(q, go), el("button", { class: "btn primary", onclick: go }, "Ask"))));
  setTimeout(() => q.focus(), 50);
}

// askStream asks and writes the answer into log as it comes (about one document when path is set).
async function askStream(question, path, log, history) {
  const qEl = el("div", { class: "qa user", text: question });
  const aEl = el("div", { class: "qa assistant" }, el("div", { class: "md muted", text: "Thinking…" }));
  log.append(qEl, aEl);
  log.scrollTop = log.scrollHeight;
  let text = "", sources = [];
  try {
    const res = await fetch("/api/ask", { method: "POST", headers: { "Content-Type": "application/json", "X-Cove": "1" }, body: JSON.stringify({ question, path, history: history.slice(-10) }) });
    if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || "Asking didn't work");
    const reader = res.body.getReader(), dec = new TextDecoder();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let i;
      while ((i = buf.indexOf("\n\n")) >= 0) {
        const line = buf.slice(0, i); buf = buf.slice(i + 2);
        if (!line.startsWith("data: ")) continue;
        const ev = JSON.parse(line.slice(6));
        if (ev.sources) sources = ev.sources;
        if (ev.action) {
          let box = aEl.querySelector(".actions");
          if (!box) { box = el("div", { class: "actions" }); aEl.prepend(box); }
          box.append(el("div", { class: "actdone" + (/^Couldn't/.test(ev.action) ? " failed" : "") }, icon(/^Couldn't/.test(ev.action) ? "close" : "check"), ev.action));
          if (text === "") { const md = aEl.querySelector(".md.muted"); if (md) md.remove(); }
        }
        if (ev.text) {
          text += ev.text;
          put(aEl, aEl.querySelector(".actions"), el("div", { class: "md" }, markdown(text)));
          log.scrollTop = log.scrollHeight;
        }
        if (ev.error) throw new Error(ev.error);
      }
    }
  } catch (e) {
    put(aEl, el("div", { class: "muted", style: { color: "var(--danger)" }, text: e.message }));
    return;
  }
  // name only the documents the answer used (by name, or by a number or word only that document has)
  const lower = text.toLowerCase();
  const used = sources.filter(s => lower.includes(s.name.replace(/\.[^.]+$/, "").toLowerCase()));
  sources = used;
  if (sources.length) aEl.append(el("div", { class: "sources" }, "From: ", ...sources.map(s => el("a", { href: "#", text: s.name, onclick: e => {
    e.preventDefault(); S.share = null; S.path = parentOf(s.path); const it = { name: s.name, path: s.path, kind: kindOfName(s.path) }; S.items = [it]; openItem(it);
  } }))));
  history.push({ role: "user", content: question }, { role: "assistant", content: text });
}

// askAbout opens a conversation about one document (from the viewer).
function askAbout(it) {
  const log = el("div", { class: "asklog", style: { maxHeight: "55vh" } });
  const history = [];
  const q = el("input", { class: "input", placeholder: "Ask something about it…", onkeydown: e => { if (e.key === "Enter") { e.preventDefault(); if (q.value.trim()) { askStream(q.value.trim(), it.path, log, history); q.value = ""; } } } });
  dialog({ title: "Ask about “" + it.name + "”", wide: true, body: el("div", {}, log, el("div", { class: "compose" }, q)), buttons: [{ label: "Done" }] });
  askStream("Summarize this document in a few short points.", it.path, log, history);
}

// --- the fridge's polls and countdowns

function pollDialog(c) {
  const qn = input("", { placeholder: "Where should we eat Friday?" });
  const opts = el("div");
  const add = v => { const i = input(v || "", { placeholder: "An answer", onkeydown: e => { if (e.key === "Enter") { e.preventDefault(); add(); } } }); opts.append(el("div", { style: { marginBottom: "6px" } }, i)); setTimeout(() => i.focus(), 10); };
  add(); add();
  const until = select([["0", "Stays up"], ["1", "Ends after a day"], ["3", "After 3 days"], ["7", "After a week"]], "3");
  dialog({ title: "New poll on the fridge", body: el("div", {}, field("Question", qn), el("div", { class: "label", text: "Answers" }), opts,
    btn("Add an answer", "plus", () => add(), "ghost"), field("Ends", until)),
  buttons: [{ label: "Cancel" }, { label: "Put it up", primary: true, action: async () => {
    await api("POST", "/api/fridge", { kind: "poll", text: qn.value, color: "blue", items: [...opts.querySelectorAll("input")].map(i => ({ text: i.value })), expiresDays: +until.value });
    showFridge(c);
  } }] });
}

function countdownDialog(c) {
  const name = input("", { placeholder: "Beach vacation" }), day = input(ymd(addDays(new Date(), 30)), { type: "date" });
  dialog({ title: "New countdown", body: el("div", {}, field("To what", name), field("Day", day)),
    buttons: [{ label: "Cancel" }, { label: "Put it up", primary: true, action: async () => { await api("POST", "/api/fridge", { kind: "countdown", text: name.value, date: day.value, color: "orange" }); showFridge(c); } }] });
}

// fridgeExtra draws a poll's answers or a countdown's days (called from fridgeCard).
function fridgeExtra(n, c, card) {
  if (n.kind === "countdown") {
    const days = Math.round((parseYmd(n.date) - parseYmd(ymd(new Date()))) / 864e5);
    card.append(el("div", { class: "cdown" }, el("b", { text: days > 0 ? days : days === 0 ? "🎉" : "✓" }), el("span", { text: days > 1 ? "days to go" : days === 1 ? "day to go" : days === 0 ? "Today!" : "done" })),
      el("div", { class: "muted", style: { fontSize: "12px", textAlign: "center" }, text: parseYmd(n.date).toLocaleDateString([], { weekday: "long", month: "long", day: "numeric" }) }));
  }
  if (n.kind === "poll") {
    const total = n.items.reduce((a, it) => a + (it.votes || []).length, 0);
    card.append(el("div", { class: "poll" }, n.items.map((it, i) => {
      const v = (it.votes || []).length, mine = (it.votes || []).includes(S.me.user.name);
      return el("button", { class: "popt" + (mine ? " mine" : ""), onclick: async e => {
        e.stopPropagation();
        try { await api("POST", `/api/fridge/${n.id}/vote`, { option: i }); } catch (err) { fail(err); }
        if (c) showFridge(c); else route();
      } }, el("div", { class: "pbar", style: { width: total ? (100 * v / total) + "%" : "0" } }), el("span", { text: it.text }),
        el("small", { text: v ? `${v}${(it.votes || []).length ? " · " + it.votes.map(nameOf).join(", ") : ""}` : "" }));
    })));
  }
}
