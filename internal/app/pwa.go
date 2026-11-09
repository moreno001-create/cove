package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// Cove installs as an app (Add to Home Screen): a manifest, icons, and a service worker. Installed on Android, it's
// also in the share sheet: what's shared to it lands in "Shared to Cove".

const inboxFolder = "Shared to Cove"

func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	json.NewEncoder(w).Encode(map[string]any{
		"name": "Cove", "short_name": "Cove", "id": "/", "start_url": "/", "scope": "/", "display": "standalone",
		"background_color": "#f5f6f8", "theme_color": "#f5f6f8",
		"description": "Files, calendars, contacts, tasks and documents for the household",
		"icons": []map[string]string{
			{"src": "/assets/icon-192.png", "sizes": "192x192", "type": "image/png"},
			{"src": "/assets/icon-512.png", "sizes": "512x512", "type": "image/png"},
			{"src": "/assets/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
		"share_target": map[string]any{
			"action": "/share-target", "method": "POST", "enctype": "multipart/form-data",
			"params": map[string]any{"title": "title", "text": "text", "url": "url",
				"files": []map[string]any{{"name": "files", "accept": []string{"*/*"}}}},
		},
		"shortcuts": []map[string]string{
			{"name": "Calendar", "url": "/#/calendar"}, {"name": "Tasks", "url": "/#/tasks"}, {"name": "Contacts", "url": "/#/contacts"},
		},
	})
}

// The service worker is served from the top so it covers the whole app. It only passes requests on: Cove needs the
// server, and nothing private is kept in the browser.
func serviceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript")
	w.Header().Set("Cache-Control", "no-cache")
	io.WriteString(w, swSource)
}

// The service worker: notifications pushed to the phone, and offline reading. The app's pages and the lists people
// read on the go (tasks, notes, calendars) are answered from the network when there is one and from the last copy
// when there isn't. Files and everything else always come from the server.
const swSource = `const CACHE = "cove-v6";
const SHELL = ["/", "/assets/theme.js", "/assets/style.css", "/assets/common.js", "/assets/app.js", "/assets/house.js", "/assets/family.js", "/assets/extras.js", "/assets/batch4.js", "/assets/icon.svg", "/assets/icon-192.png"];
const OFFLINE = [/^\/api\/(me|tasks|notes|calendars|home|fridge|office|settings|chores|users)(\?|$)/, /^\/api\/hh\/(recipe|meal|trip|chore)(\?|$)/, /^\/api\/files\/content\?path=%2FNotes%2F/];
self.addEventListener("install", e => { e.waitUntil(caches.open(CACHE).then(c => c.addAll(SHELL)).catch(() => {})); self.skipWaiting(); });
self.addEventListener("activate", e => e.waitUntil(self.clients.claim()));
self.addEventListener("fetch", e => {
  const u = new URL(e.request.url);
  if (e.request.method !== "GET" || u.origin !== location.origin) return;
  const path = u.pathname + u.search;
  if (!(SHELL.includes(u.pathname) || OFFLINE.some(re => re.test(path)))) return;
  e.respondWith(fetch(e.request).then(res => {
    if (res.ok) { const copy = res.clone(); caches.open(CACHE).then(c => c.put(e.request, copy)); }
    return res;
  }).catch(() => caches.match(e.request).then(hit => hit || Response.error())));
});
self.addEventListener("push", e => {
  let d = {};
  try { d = e.data.json(); } catch (_) { d = { body: e.data ? e.data.text() : "" }; }
  e.waitUntil(self.registration.showNotification(d.title || "Cove", { body: d.body || "", icon: "/assets/icon-192.png", badge: "/assets/icon-192.png", data: { link: d.link || "/" } }));
});
self.addEventListener("notificationclick", e => {
  e.notification.close();
  const link = (e.notification.data && e.notification.data.link) || "/";
  e.waitUntil(self.clients.matchAll({ type: "window" }).then(list => {
    for (const c of list) if (new URL(c.url).origin === location.origin) { c.navigate(link); return c.focus(); }
    return self.clients.openWindow(link);
  }));
});
`

// shareTarget saves what the phone's share sheet sent: files as they are, text and links as a note. The session
// cookie is SameSite=Lax, so other sites can't post here as the user.
func (s *Server) shareTarget(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "Sharing to Cove only works from the phone's share sheet.", http.StatusForbidden)
		return
	}
	u, ok := s.currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "Nothing was shared.", http.StatusBadRequest)
		return
	}
	var title, text, link string
	saved := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			http.Error(w, "The share didn't arrive whole.", http.StatusBadRequest)
			return
		}
		switch name := part.FormName(); {
		case name == "files" && part.FileName() != "":
			fn := path.Base(cleanRel(part.FileName()))
			if fn == "." || hidden(fn) {
				fn = "Shared file"
			}
			if _, err := s.writeFile(u.Name, inboxFolder+"/"+fn, part, false, time.Time{}); err != nil {
				http.Error(w, "Saving it didn't work: "+err.Error(), http.StatusInternalServerError)
				return
			}
			saved++
		case name == "title" || name == "text" || name == "url":
			b, _ := io.ReadAll(io.LimitReader(part, 64<<10))
			switch name {
			case "title":
				title = string(b)
			case "text":
				text = string(b)
			default:
				link = string(b)
			}
		}
		part.Close()
	}
	if strings.TrimSpace(title+text+link) != "" && saved == 0 {
		note := strings.TrimSpace(strings.Join(nonEmpty(title, text, link), "\n\n")) + "\n"
		name := fmt.Sprintf("%s/Note %s.md", inboxFolder, time.Now().Format("2006-01-02 15.04"))
		if _, err := s.writeFile(u.Name, name, strings.NewReader(note), false, time.Time{}); err != nil {
			http.Error(w, "Saving it didn't work: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/#/files/"+url.PathEscape(inboxFolder), http.StatusSeeOther)
}

func nonEmpty(v ...string) []string {
	var out []string
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
