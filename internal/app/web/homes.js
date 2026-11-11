// Cove: households (the switcher, who's in them), "who sees this" on every page, the Sharing page, and the admin's
// Households card.
"use strict";

const HOME = {
  get current() { return (S.me.homes || []).find(h => h.id === S.me.household) || null; },
};

function setHome(id) {
  try { localStorage.setItem("cove.home", String(id)); } catch (_) {}
  api("GET", "/api/me").then(me => { S.me = me; PEOPLE = null; drawShell(); route(); toast("Now in " + (HOME.current ? HOME.current.name : "your household")); }).catch(fail);
}

// homeSwitch is the sidebar's household button: which household you're looking at, and the others you're in.
function homeSwitch() {
  const homes = S.me.homes || [];
  const cur = HOME.current;
  if (!homes.length) return el("button", { class: "homeswitch none", onclick: () => notInHome() }, icon("home"), el("span", { text: "Not in a household yet" }));
  return el("button", { class: "homeswitch", title: "The household you're looking at", onclick: e => {
    popMenu(e.currentTarget, [
      ...homes.map(h => ({ label: (h.id === S.me.household ? "✓ " : "") + h.name, icon: "home", action: () => { if (h.id !== S.me.household) setHome(h.id); } })),
      "-",
      { label: "Who's in " + cur.name, icon: "user", action: () => whoIsIn(cur) },
      S.me.user.admin ? { label: "Manage households", icon: "settings", action: () => { location.hash = "#/admin"; setTimeout(() => document.getElementById("households")?.scrollIntoView({ behavior: "smooth" }), 500); } } : null,
    ]);
  } }, icon("home"), el("span", { class: "grow", text: cur ? cur.name : homes[0].name }), homes.length > 1 ? icon("right") : null);
}

function whoIsIn(h) {
  dialog({ title: h.name, body: el("div", {},
    el("p", { class: "muted", style: { marginTop: 0 }, text: "Everyone here shares this household's Fridge, Family chat, meals, chores, shopping, budget, trips and other household pages." }),
    el("div", { class: "items-list card", style: { boxShadow: "none" } }, h.members.map(m => el("div", {}, el("div", { class: "avatar", text: (m.display || m.name).slice(0, 1).toUpperCase() }),
      el("div", { class: "grow", text: m.display || m.name }), m.kid ? el("span", { class: "pill", text: "kid" }) : null, m.guest ? el("span", { class: "pill", text: "guest" }) : null)))),
    buttons: [{ label: "Done", primary: true }] });
}

function notInHome() {
  dialog({ title: "Not in a household yet", body: el("p", { text: "Your own files, calendars, notes and photos work already. The Fridge, Family chat, meals, chores and the other household pages belong to a household: ask an admin to add you to yours (Admin → Households)." }),
    buttons: [{ label: "OK", primary: true }] });
}

// --- "who sees this", on every page

const HOUSEHOLD_PAGES = ["fridge", "chat", "meals", "shopping", "chores", "rides", "trips", "wishes", "health", "pantry", "upkeep", "records", "budget", "sitter", "school", "kiosk", "print", "inventory"];
const PERSONAL_PAGES = { files: "Your files", notes: "Your notes", journal: "Only you", calendar: "Your calendars", tasks: "Your task lists", contacts: "Your contacts", photos: "Your photos" };

// markWho puts a small "who sees this" label at the top of the page.
function markWho(section) {
  const c = $("content");
  const bar = c.querySelector(".bar") || c.querySelector(".page-title")?.parentElement;
  if (!bar || bar.querySelector(".whosees")) return;
  let chip = null;
  if (HOUSEHOLD_PAGES.includes(section)) {
    const h = HOME.current;
    chip = h ? el("button", { class: "whosees home", title: "Who sees this page", onclick: () => whoIsIn(h) }, icon("home"), `${h.name} · ${h.members.length} ${h.members.length === 1 ? "person" : "people"}`)
      : el("button", { class: "whosees none", onclick: () => notInHome() }, icon("home"), "No household yet");
  } else if (section === "journal") {
    chip = el("span", { class: "whosees lock" }, icon("lock"), "Only you");
  } else if (PERSONAL_PAGES[section] && !(section === "files" && S.share) && !location.hash.startsWith("#/shared/")) {
    chip = el("a", { class: "whosees lock", href: "#/sharing", title: "Everything you share, and with whom" }, icon("lock"), PERSONAL_PAGES[section] + ": private unless you share");
  } else if (section === "shared" && S.shareInfo) {
    chip = el("span", { class: "whosees" }, icon("user"), `Shared with you by ${S.shareInfo.who} · ${S.shareInfo.edit ? "you can change it" : "view only"}`);
  }
  if (!chip) return;
  const title = bar.querySelector(".page-title, .crumbs");
  if (title && title.nextSibling) title.after(chip);
  else bar.prepend(chip);
}

// --- the Sharing page

async function showSharing(c) {
  let r;
  try { r = await api("GET", "/api/sharing"); } catch (e) { return fail(e); }
  const icons = { file: "file", folder: "folder", calendar: "calendar", album: "image", link: "share" };
  const open = e => {
    if (e.kind === "file" || e.kind === "folder" || e.kind === "link") { if (e.path) goFolder(e.kind === "folder" ? e.path : parentOf(e.path), null); }
    else if (e.kind === "calendar") location.hash = "#/calendar";
    else if (e.kind === "album") location.hash = "#/photos";
  };
  const row = (e, mine) => el("div", {},
    el("div", { class: "ficon k-" + (e.kind === "folder" ? "folder" : "file") }, icon(icons[e.kind] || "file")),
    el("div", { class: "grow", style: { cursor: "pointer" }, onclick: () => open(e) }, el("div", { style: { fontWeight: 600 }, text: e.name }),
      el("div", { class: "muted", style: { fontSize: "12.5px" }, text: [e.kind === "link" ? "link" : e.kind, e.path && e.kind !== "calendar" && e.kind !== "album" ? e.path : "", e.note, e.since ? "since " + fmtDate(e.since * 1000) : ""].filter(Boolean).join(" · ") })),
    el("span", { class: "pill", text: (mine ? "→ " : "from ") + e.with + (e.edit ? " · can change" : " · can view") }),
    mine && e.stop ? btn("Stop sharing", null, async () => {
      if (!(await confirmBox("Stop sharing “" + e.name + "”?", (e.kind === "link" ? "The link stops working for everyone who has it." : e.with + " won't see it any more.") + " Nothing is deleted.", "Stop sharing", true))) return;
      try { await api("DELETE", e.stop); toast("Not shared any more"); showSharing(c); } catch (err) { fail(err); }
    }, "ghost") : null);
  const people = {};
  for (const e of r.mine) for (const w of [e.with]) (people[w] = people[w] || []).push(e);
  put(c, pageHead("Sharing", "Everything you share, and everything shared with you. Household pages are shared with your household's members, not listed here."),
    el("h3", { class: "sec", text: `You share (${r.mine.length})` }),
    r.mine.length ? el("div", {}, Object.entries(people).sort((a, b) => b[1].length - a[1].length).map(([w, list]) => el("div", {},
      el("div", { class: "label", style: { margin: "12px 0 6px" }, text: w }), el("div", { class: "card items-list" }, list.map(e => row(e, true)))))) :
      el("p", { class: "muted", text: "Nothing: everything of yours is private. Share a file, folder or calendar from its ⋯ menu." }),
    el("h3", { class: "sec", text: `Shared with you (${r.withMe.length})` }),
    r.withMe.length ? el("div", { class: "card items-list" }, r.withMe.map(e => row(e, false))) : el("p", { class: "muted", text: "Nobody has shared anything with you yet." }));
}

// --- the admin's Households card

function householdsAdmin() {
  const box = el("div", { class: "card section", id: "households" }, el("h3", { text: "Households" }));
  const draw = async () => {
    let r, users;
    try { [r, users] = await Promise.all([api("GET", "/api/admin/households"), api("GET", "/api/admin/users")]); } catch (e) { return put(box, el("p", { text: e.message })); }
    const nameOf2 = n => { const u = users.find(x => x.name === n); return u ? (u.display || u.name) : n; };
    put(box, el("h3", { text: "Households" }),
      el("p", { class: "muted", style: { fontSize: "13px" }, text: "Each household has its own Fridge, Family chat, meals, chores, shopping, budget and trips; only its members see them. Someone can be in more than one (grandparents): they switch at the top of the menu. New people wait here until you add them." }),
      r.nowhere.length ? el("div", { class: "notice" }, el("b", { text: "Not in a household yet: " }), r.nowhere.map(nameOf2).join(", ")) : null,
      ...r.households.map(h => {
        const add = select([["", "Add someone…"], ...users.filter(u => !h.members.some(m => m.name === u.name)).map(u => [u.name, u.display || u.name])], "", { style: { width: "auto" }, onchange: async e => {
          if (!e.target.value) return;
          try { await api("POST", `/api/admin/households/${h.id}/members`, { user: e.target.value, in: true }); draw(); if (e.target.value === S.me.user.name) setHome(h.id); } catch (err) { fail(err); }
        } });
        return el("div", { class: "homecard" },
          el("div", { class: "row", style: { alignItems: "center" } }, el("b", { class: "grow", text: h.name }),
            btn("Rename", "edit", async () => { const n = await ask("Rename", "Name", h.name, "Save"); if (n) { await api("PUT", "/api/admin/households/" + h.id, { name: n }).catch(fail); draw(); S.me = await api("GET", "/api/me"); drawShell(); } }, "ghost"),
            h.members.length ? null : btn("Remove", "trash", async () => { try { await api("DELETE", "/api/admin/households/" + h.id); draw(); } catch (e) { fail(e); } }, "ghost")),
          el("div", { class: "chips", style: { padding: "6px 0" } }, h.members.map(m => el("span", { class: "chip on" }, m.display || m.name,
            el("button", { class: "x", title: "Take out of " + h.name, onclick: async () => {
              if (!(await confirmBox(`Take ${m.display || m.name} out of ${h.name}?`, "They stop seeing its Fridge, chat and household pages. Their own files and what they've shared stay theirs.", "Take out", true))) return;
              try { await api("POST", `/api/admin/households/${h.id}/members`, { user: m.name, in: false }); draw(); } catch (e) { fail(e); }
            } }, "×")))), add);
      }),
      btn("New household", "plus", async () => { const n = await ask("New household", "Name (“The Morenos”, “Grandma & Grandpa”)", "", "Create"); if (n) { try { await api("POST", "/api/admin/households", { name: n }); draw(); } catch (e) { fail(e); } } }));
  };
  draw();
  return box;
}
