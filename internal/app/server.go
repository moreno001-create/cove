// Package app is Cove's server: files with share links, calendars (also over CalDAV), office documents through
// Collabora Online (WOPI) and WebDAV for sync apps.
package app

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	xwebdav "golang.org/x/net/webdav"
)

//go:embed web
var webFiles embed.FS

type Server struct {
	cfg  Config
	db   *sql.DB
	data *os.Root // the data folder; every file path goes through it, so none can lead outside
	key  []byte   // signs cookies, share unlocks and document tokens
	http *http.Client
	mux  *http.ServeMux

	mu        sync.Mutex
	endpoints *oidcEndpoints
	office    *officeApps
	locks     map[string]xwebdav.LockSystem // WebDAV locks, per user

	done chan struct{}  // closed when the server stops: background jobs end
	bg   sync.WaitGroup // jobs that write files, waited for on Close
}

func New(cfg Config) (*Server, error) {
	if err := os.MkdirAll(cfg.Data, 0o750); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(cfg.Data)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{"files", "trash", "uploads", "thumbs", "versions"} {
		if err := root.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	key, err := loadKey(root)
	if err != nil {
		return nil, err
	}
	db, err := openDB(filepath.Join(cfg.Data, "cove.db"))
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, db: db, data: root, key: key, http: &http.Client{Timeout: 30 * time.Second},
		mux: http.NewServeMux(), locks: map[string]xwebdav.LockSystem{}, done: make(chan struct{})}
	setTrusted(cfg.URL)
	s.routes()
	go s.cleanUpEvery(time.Hour)
	go s.remindEvery(time.Minute)
	go s.indexEvery(20 * time.Minute)
	go s.refreshSubscriptionsEvery(6 * time.Hour)
	go s.snapshotEvery(6 * time.Hour)
	go s.offsiteEvery()
	return s, nil
}

func (s *Server) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.bg.Wait()
	s.data.Close()
	return s.db.Close()
}

// loadKey reads the key that signs Cove's cookies and tokens, making it the first time.
func loadKey(root *os.Root) ([]byte, error) {
	b, err := root.ReadFile("secret.key")
	if err == nil && len(b) >= 32 {
		return b, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	b = make([]byte, 32)
	rand.Read(b)
	return b, root.WriteFile("secret.key", b, 0o600)
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) ListenAndServe() error {
	srv := &http.Server{Addr: s.cfg.Addr, Handler: s.Handler(), ReadHeaderTimeout: 30 * time.Second}
	return srv.ListenAndServe()
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /{$}", s.appPage)
	m.Handle("GET /assets/", http.StripPrefix("/assets/", http.HandlerFunc(serveAsset)))
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })

	m.HandleFunc("GET /login", s.login)
	m.HandleFunc("GET /auth/callback", s.callback)
	m.HandleFunc("POST /logout", s.logout)
	m.HandleFunc("GET /signed-out", s.signedOut)

	m.HandleFunc("GET /api/me", s.api(s.me))
	m.HandleFunc("GET /api/home", s.api(s.home))
	m.HandleFunc("GET /api/live", s.api(s.liveStream))
	m.HandleFunc("GET /api/push/key", s.api(s.pushKey))
	m.HandleFunc("POST /api/push/subscribe", s.api(s.pushSubscribe))
	m.HandleFunc("POST /api/push/unsubscribe", s.api(s.pushUnsubscribe))
	m.HandleFunc("GET /api/subscriptions", s.api(func(w http.ResponseWriter, r *http.Request, u user) { writeJSON(w, 200, s.subscriptionsOf(u.Name)) }))
	m.HandleFunc("POST /api/subscriptions", s.api(s.createSubscription))
	m.HandleFunc("PUT /api/subscriptions/{id}", s.api(s.updateSubscription))
	m.HandleFunc("DELETE /api/subscriptions/{id}", s.api(s.deleteSubscription))
	m.HandleFunc("POST /api/subscriptions/{id}/refresh", s.api(s.refreshSubscription))
	m.HandleFunc("GET /api/freebusy", s.api(s.freeBusy))
	m.HandleFunc("GET /api/calendars/{id}/members", s.api(s.listMembers))
	m.HandleFunc("GET /api/export", s.api(s.exportAll))
	m.HandleFunc("GET /api/groups", s.api(s.listGroups))
	m.HandleFunc("GET /api/group-shares", s.api(s.listGroupShares))
	m.HandleFunc("POST /api/group-shares", s.api(s.createGroupShare))
	m.HandleFunc("DELETE /api/group-shares/{id}", s.api(s.deleteGroupShare))
	m.HandleFunc("GET /api/comments", s.api(s.listComments))
	m.HandleFunc("POST /api/comments", s.api(s.addComment))
	m.HandleFunc("DELETE /api/comments/{id}", s.api(s.deleteComment))
	m.HandleFunc("POST /api/files/unzip", s.api(s.files(s.unzip)))
	m.HandleFunc("GET /api/files/info", s.api(s.files(s.folderInfo)))
	m.HandleFunc("GET /api/files/duplicates", s.api(s.duplicates))
	m.HandleFunc("GET /api/contacts/duplicates", s.api(s.contactDuplicates))
	m.HandleFunc("POST /api/contacts/merge", s.api(s.mergeContacts))
	m.HandleFunc("GET /api/activity", s.api(s.listActivity))
	m.HandleFunc("POST /api/activity/{id}/undo", s.api(s.undoActivity))
	m.HandleFunc("GET /api/templates", s.api(s.listTemplates))
	m.HandleFunc("GET /api/fridge", s.api(s.listFridge))
	m.HandleFunc("POST /api/fridge", s.api(s.addFridge))
	m.HandleFunc("POST /api/fridge/photo", s.api(s.addFridgePhoto))
	m.HandleFunc("GET /api/fridge/{id}/photo", s.api(s.fridgePhoto))
	m.HandleFunc("PUT /api/fridge/{id}", s.api(s.updateFridge))
	m.HandleFunc("DELETE /api/fridge/{id}", s.api(s.removeFridge))
	m.HandleFunc("POST /api/fridge/{id}/vote", s.api(s.voteFridge))
	m.HandleFunc("GET /api/hh/{kind}", s.api(s.listHH))
	m.HandleFunc("POST /api/hh/{kind}", s.api(s.saveHH))
	m.HandleFunc("PUT /api/hh/{kind}/{id}", s.api(s.saveHH))
	m.HandleFunc("DELETE /api/hh/{kind}/{id}", s.api(s.deleteHH))
	m.HandleFunc("POST /api/meals/shopping", s.api(s.mealsToShopping))
	m.HandleFunc("POST /api/shopping", s.api(func(w http.ResponseWriter, r *http.Request, u user) {
		var in struct {
			Items []string `json:"items"`
		}
		if err := readJSON(r, &in); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		n, err := s.addToShopping(u, in.Items)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"added": n})
	}))
	m.HandleFunc("GET /api/chores", s.api(s.choreBoard))
	m.HandleFunc("POST /api/chores/{id}/done", s.api(s.choreDone))
	m.HandleFunc("DELETE /api/chores/done/{id}", s.api(s.choreUndo))
	m.HandleFunc("POST /api/allowance", s.api(s.payAllowance))
	m.HandleFunc("GET /api/budget", s.api(s.budget))
	m.HandleFunc("GET /api/chat", s.api(s.listChat))
	m.HandleFunc("POST /api/chat", s.api(s.createRoom))
	m.HandleFunc("DELETE /api/chat/{id}", s.api(s.deleteRoom))
	m.HandleFunc("GET /api/chat/{id}/messages", s.api(s.chatMessages))
	m.HandleFunc("POST /api/chat/{id}/messages", s.api(s.postChat))
	m.HandleFunc("GET /api/chat-photos/{id}", s.api(s.chatPhoto))
	m.HandleFunc("DELETE /api/chat-messages/{id}", s.api(s.deleteChatMessage))
	m.HandleFunc("GET /api/photos", s.api(s.listPhotos))
	m.HandleFunc("POST /api/photos/have", s.api(s.photosHave))
	m.HandleFunc("GET /api/albums", s.api(s.listAlbums))
	m.HandleFunc("POST /api/albums", s.api(s.saveAlbum))
	m.HandleFunc("PUT /api/albums/{id}", s.api(s.saveAlbum))
	m.HandleFunc("DELETE /api/albums/{id}", s.api(s.deleteAlbum))
	m.HandleFunc("GET /api/albums/{id}", s.api(s.albumItems))
	m.HandleFunc("POST /api/albums/{id}/items", s.api(s.addToAlbum))
	m.HandleFunc("DELETE /api/albums/{id}/items", s.api(s.removeFromAlbum))
	m.HandleFunc("GET /api/albums/{id}/photo", s.api(s.albumPhoto))
	m.HandleFunc("POST /api/scan", s.api(s.files(s.saveScan)))
	m.HandleFunc("POST /api/ask", s.api(s.ask))
	m.HandleFunc("GET /api/devices", s.api(s.listDevices))
	m.HandleFunc("POST /api/receipts", s.api(s.readReceipt))
	m.HandleFunc("POST /api/mail/sort", s.api(s.sortLetter))
	m.HandleFunc("POST /api/mail/file", s.api(s.fileLetter))
	m.HandleFunc("POST /api/recipes/import", s.api(s.importRecipe))
	m.HandleFunc("GET /api/rewards", s.api(s.rewardsShop))
	m.HandleFunc("POST /api/rewards/{id}/claim", s.api(s.claimReward))
	m.HandleFunc("POST /api/reward-claims/{id}", s.api(s.answerClaim))
	m.HandleFunc("GET /api/fridge/{id}/wifi", s.api(s.fridgeWifi))
	m.HandleFunc("POST /api/photos/import", s.api(s.importPhotos))
	m.HandleFunc("POST /api/devices", s.api(s.switchDevice))
	m.HandleFunc("GET /api/watching", s.api(s.watching))
	m.HandleFunc("GET /api/watching/image/{id}", s.api(s.jellyImage))
	m.HandleFunc("GET /api/meds/today", s.api(s.medsToday))
	m.HandleFunc("POST /api/meds/{id}/taken", s.api(s.medTaken))
	m.HandleFunc("POST /api/pantry/{id}/use", s.api(s.pantryUse))
	m.HandleFunc("GET /api/pantry/lookup", s.api(s.pantryLookup))
	m.HandleFunc("GET /api/journal/{id}/photo", s.api(s.journalPhoto))
	m.HandleFunc("GET /api/memories", s.api(s.memories))
	m.HandleFunc("GET /api/briefing", s.api(s.briefingNow))
	m.HandleFunc("GET /api/admin/ha", s.api(s.admin(s.getHA)))
	m.HandleFunc("PUT /api/admin/ha", s.api(s.admin(s.putHA)))
	m.HandleFunc("GET /api/admin/ha/entities", s.api(s.admin(s.haEntities)))
	m.HandleFunc("GET /api/admin/jellyfin", s.api(s.admin(s.getJelly)))
	m.HandleFunc("PUT /api/admin/jellyfin", s.api(s.admin(s.putJelly)))
	m.HandleFunc("GET /api/admin/offsite", s.api(s.admin(s.getOffsite)))
	m.HandleFunc("PUT /api/admin/offsite", s.api(s.admin(s.putOffsite)))
	m.HandleFunc("POST /api/admin/offsite/run", s.api(s.admin(s.runOffsite)))
	m.HandleFunc("GET /api/admin/ai", s.api(s.admin(s.getAI)))
	m.HandleFunc("PUT /api/admin/ai", s.api(s.admin(s.putAI)))
	m.HandleFunc("GET /api/admin/ai/models", s.api(s.admin(s.aiModels)))
	m.HandleFunc("PUT /api/admin/users/{name}/kid", s.api(s.admin(s.adminSetKid)))
	m.HandleFunc("GET /api/favorites", s.api(s.listFavorites))
	m.HandleFunc("POST /api/favorites", s.api(s.starFavorite))
	m.HandleFunc("DELETE /api/favorites", s.api(s.removeFavorite))
	m.HandleFunc("GET /api/notifications", s.api(s.listNotifications))
	m.HandleFunc("POST /api/notifications/read", s.api(s.readNotifications))
	m.HandleFunc("GET /api/settings", s.api(s.getSettings))
	m.HandleFunc("PUT /api/settings", s.api(s.putSettings))
	m.HandleFunc("POST /api/settings/test-ntfy", s.api(s.testNtfy))
	m.HandleFunc("GET /api/notes", s.api(s.listNotes))
	m.HandleFunc("POST /api/notes", s.api(s.createNote))
	m.HandleFunc("POST /api/notes/pin", s.api(s.pinNote))
	m.HandleFunc("GET /api/trash/items", s.api(s.listTrashItems))
	m.HandleFunc("POST /api/trash/items/restore", s.api(s.restoreTrashItem))
	m.HandleFunc("POST /api/trash/items/delete", s.api(s.deleteTrashItems))
	m.HandleFunc("GET /api/shares/{token}/qr", s.api(s.shareQR))
	m.HandleFunc("POST /api/calendars/{id}/import", s.api(s.importCalendar))
	m.HandleFunc("GET /api/calendars/{id}/export", s.api(s.exportCalendar))
	m.HandleFunc("POST /api/contacts/import", s.api(s.importContacts))
	m.HandleFunc("GET /api/contacts/export", s.api(s.exportContacts))
	m.HandleFunc("GET /api/events/{cal}/{name}/guests", s.api(s.eventGuests))
	m.HandleFunc("POST /api/invites/{id}", s.api(s.answerInvite))
	m.HandleFunc("GET /api/admin/users", s.api(s.admin(s.adminUsers)))
	m.HandleFunc("PUT /api/admin/users/{name}", s.api(s.admin(s.adminSetQuota)))
	m.HandleFunc("GET /api/admin/shares", s.api(s.admin(s.adminShares)))
	m.HandleFunc("DELETE /api/admin/links/{token}", s.api(s.admin(s.adminEndLink)))
	m.HandleFunc("DELETE /api/admin/shares/{kind}/{id}", s.api(s.admin(s.adminEndShare)))
	m.HandleFunc("GET /api/files", s.api(s.files(s.listFiles)))
	m.HandleFunc("GET /api/files/content", s.api(s.files(s.fileContent)))
	m.HandleFunc("PUT /api/files/content", s.api(s.files(s.uploadFile)))
	m.HandleFunc("GET /api/files/zip", s.api(s.files(s.zipFolder)))
	m.HandleFunc("GET /api/files/thumb", s.api(s.files(s.fileThumb)))
	m.HandleFunc("POST /api/uploads", s.api(s.files(s.startUpload)))
	m.HandleFunc("GET /api/uploads/{id}", s.api(s.uploadStatus))
	m.HandleFunc("PUT /api/uploads/{id}", s.api(s.uploadChunk))
	m.HandleFunc("POST /api/uploads/{id}/finish", s.api(s.files(s.finishUpload)))
	m.HandleFunc("DELETE /api/uploads/{id}", s.api(s.cancelUpload))
	m.HandleFunc("POST /api/files/mkdir", s.api(s.files(s.makeFolder)))
	m.HandleFunc("POST /api/files/new", s.api(s.files(s.newDocument)))
	m.HandleFunc("POST /api/files/move", s.api(s.files(s.moveFile)))
	m.HandleFunc("POST /api/files/delete", s.api(s.files(s.deleteFiles)))
	m.HandleFunc("GET /api/search", s.api(s.search))
	m.HandleFunc("GET /api/trash", s.api(s.listTrash))
	m.HandleFunc("POST /api/trash/restore", s.api(s.restoreTrash))
	m.HandleFunc("POST /api/trash/delete", s.api(s.deleteTrash))

	m.HandleFunc("GET /api/shares", s.api(s.listShares))
	m.HandleFunc("POST /api/shares", s.api(s.createShare))
	m.HandleFunc("DELETE /api/shares/{token}", s.api(s.deleteShare))

	m.HandleFunc("GET /api/app-passwords", s.api(s.listAppPasswords))
	m.HandleFunc("POST /api/app-passwords", s.api(s.createAppPassword))
	m.HandleFunc("DELETE /api/app-passwords/{id}", s.api(s.deleteAppPassword))

	m.HandleFunc("GET /manifest.webmanifest", s.manifest)
	m.HandleFunc("GET /sw.js", serviceWorker)
	m.HandleFunc("POST /share-target", s.shareTarget)

	m.HandleFunc("GET /api/calendars", s.api(s.listCalendars))
	m.HandleFunc("GET /api/calendars/{id}/shares", s.api(s.listCalendarShares))
	m.HandleFunc("POST /api/calendars/{id}/shares", s.api(s.shareCalendar))
	m.HandleFunc("DELETE /api/calendar-shares/{id}", s.api(s.unshareCalendar))
	m.HandleFunc("GET /api/tasks", s.api(s.listTasks))
	m.HandleFunc("POST /api/tasks", s.api(s.createTask))
	m.HandleFunc("PUT /api/tasks/{list}/{name}", s.api(s.updateTask))
	m.HandleFunc("DELETE /api/tasks/{list}/{name}", s.api(s.deleteTask))
	m.HandleFunc("GET /api/contacts", s.api(s.listContacts))
	m.HandleFunc("POST /api/contacts", s.api(s.createContact))
	m.HandleFunc("PUT /api/contacts/{book}/{name}", s.api(s.updateContact))
	m.HandleFunc("DELETE /api/contacts/{book}/{name}", s.api(s.deleteContact))
	m.HandleFunc("POST /api/calendars", s.api(s.createCalendar))
	m.HandleFunc("PUT /api/calendars/{id}", s.api(s.updateCalendar))
	m.HandleFunc("DELETE /api/calendars/{id}", s.api(s.deleteCalendar))
	m.HandleFunc("GET /api/events", s.api(s.listEvents))
	m.HandleFunc("POST /api/events", s.api(s.createEvent))
	m.HandleFunc("PUT /api/events/{cal}/{name}", s.api(s.updateEvent))
	m.HandleFunc("DELETE /api/events/{cal}/{name}", s.api(s.deleteEvent))

	m.HandleFunc("GET /api/office", s.api(s.officeTypes))
	m.HandleFunc("POST /api/office/open", s.api(s.files(s.officeOpen)))
	if s.cfg.OfficeInternal != "" {
		for _, p := range officePaths {
			m.Handle(p, s.officeProxy())
		}
	}

	m.HandleFunc("GET /api/users", s.api(s.listUsers))
	m.HandleFunc("GET /api/user-shares", s.api(s.listUserShares))
	m.HandleFunc("POST /api/user-shares", s.api(s.createUserShare))
	m.HandleFunc("DELETE /api/user-shares/{id}", s.api(s.deleteUserShare))
	m.HandleFunc("GET /api/shared-with-me", s.api(s.sharedWithMe))
	m.HandleFunc("GET /api/recent", s.api(s.listRecent))
	m.HandleFunc("GET /api/versions", s.api(s.files(s.listVersions)))
	m.HandleFunc("GET /api/versions/content", s.api(s.files(s.versionContent)))
	m.HandleFunc("POST /api/versions/restore", s.api(s.files(s.restoreVersion)))

	m.HandleFunc("GET /s/{token}", s.sharePage)
	m.HandleFunc("GET /s/{token}/info", s.shareInfo)
	m.HandleFunc("POST /s/{token}/unlock", s.shareUnlock)
	m.HandleFunc("GET /s/{token}/content", s.shareContent)
	m.HandleFunc("GET /s/{token}/zip", s.shareZip)
	m.HandleFunc("GET /s/{token}/thumb", s.shareThumb)
	m.HandleFunc("PUT /s/{token}/upload", s.shareUpload)
	m.HandleFunc("POST /s/{token}/office", s.shareOffice)
	m.HandleFunc("GET /s/{token}/office", s.shareOfficeTypes)

	m.Handle("/dav/", s.dav())
	m.HandleFunc("/.well-known/caldav", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dav/cal/", http.StatusMovedPermanently)
	})
	m.HandleFunc("/.well-known/carddav", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dav/card/", http.StatusMovedPermanently)
	})

	m.HandleFunc("GET /wopi/files/{id}", s.wopiInfo)
	m.HandleFunc("GET /wopi/files/{id}/contents", s.wopiGet)
	m.HandleFunc("POST /wopi/files/{id}/contents", s.wopiPut)
	m.HandleFunc("POST /wopi/files/{id}", s.wopiOther)
}

// appPage is the app itself (one page; the rest is in app.js).
func (s *Server) appPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.currentUser(r); !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	s.page(w, r, "index.html")
}

// page serves one of Cove's pages with a policy that only lets its own scripts run.
func (s *Server) page(w http.ResponseWriter, r *http.Request, name string) {
	office := ""
	if s.cfg.Collabora != "" && s.cfg.OfficeInternal == "" {
		office = " " + s.cfg.Collabora
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob: data: https://tile.openstreetmap.org; media-src 'self' blob:; "+
		"frame-src 'self'"+office+"; form-action 'self'"+office+"; frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
	w.Header().Set("Cache-Control", "no-cache")
	b, err := webFiles.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

func serveAsset(w http.ResponseWriter, r *http.Request) {
	sub, _ := fs.Sub(webFiles, "web")
	http.FileServerFS(sub).ServeHTTP(w, r)
}

func (s *Server) cleanUpEvery(d time.Duration) {
	for {
		s.cleanUp(time.Now())
		time.Sleep(d)
	}
}

// cleanUp drops expired sign-ins and links, and empties trash older than 30 days.
func (s *Server) cleanUp(now time.Time) {
	s.db.Exec(`DELETE FROM sessions WHERE expires < ?`, now.Unix())
	s.db.Exec(`DELETE FROM shares WHERE expires > 0 AND expires < ?`, now.Add(-30*24*time.Hour).Unix())
	s.dropOldUploads(now)
	s.dropExpiredFridge(now)
	s.db.Exec(`DELETE FROM trash_items WHERE deleted < ?`, now.Add(-30*24*time.Hour).Unix())
	s.db.Exec(`DELETE FROM notifications WHERE created < ? AND read = 1`, now.Add(-60*24*time.Hour).Unix())
	rows, err := s.db.Query(`SELECT user, id FROM trash WHERE deleted < ?`, now.Add(-30*24*time.Hour).Unix())
	if err != nil {
		return
	}
	var old [][2]string
	for rows.Next() {
		var u, id string
		if rows.Scan(&u, &id) == nil {
			old = append(old, [2]string{u, id})
		}
	}
	rows.Close()
	for _, o := range old {
		if err := s.purgeTrash(o[0], o[1]); err != nil {
			log.Printf("emptying old trash: %v", err)
		}
	}
}

// --- small helpers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		return errors.New("the request isn't valid JSON")
	}
	return nil
}

func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// sign appends an HMAC to v; verify gives v back if the HMAC is right.
func (s *Server) sign(v string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(v))
	return v + "~" + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) verify(signed string) (string, bool) {
	i := strings.LastIndexByte(signed, '~')
	if i < 0 {
		return "", false
	}
	v := signed[:i]
	if !hmac.Equal([]byte(s.sign(v)), []byte(signed)) {
		return "", false
	}
	return v, true
}

func (s *Server) secureCookies() bool { return strings.HasPrefix(s.cfg.URL, "https://") }
