package app

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

type share struct {
	Token       string `json:"token"`
	User        string `json:"-"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	Dir         bool   `json:"dir"`
	HasPassword bool   `json:"hasPassword"`
	Expires     int64  `json:"expires"` // unix seconds; 0 = never
	Mode        string `json:"mode"`    // view; upload: view and add files; drop: only add files (a drop box)
	Created     int64  `json:"created"`
	Downloads   int    `json:"downloads"`
	URL         string `json:"url"`
	Owner       string `json:"owner,omitempty"` // on the admin page
	password    string // bcrypt hash
}

func (s *Server) listShares(w http.ResponseWriter, r *http.Request, u user) {
	q := `SELECT ` + shareColumns + ` FROM shares WHERE user = ?`
	args := []any{u.Name}
	if p := r.URL.Query().Get("path"); p != "" {
		q += ` AND path = ?`
		args = append(args, cleanRel(p))
	}
	rows, err := s.db.Query(q+` ORDER BY created DESC`, args...)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []share{}
	for rows.Next() {
		if sh, err := s.scanShare(rows); err == nil {
			list = append(list, sh)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) scanShare(row interface{ Scan(...any) error }) (share, error) {
	var sh share
	var mode int
	if err := row.Scan(&sh.Token, &sh.User, &sh.Path, &sh.password, &sh.Expires, &mode, &sh.Created, &sh.Downloads); err != nil {
		return sh, err
	}
	sh.Mode = shareModes[mode]
	sh.HasPassword = sh.password != ""
	sh.Name = path.Base(sh.Path)
	sh.URL = s.cfg.URL + "/s/" + sh.Token
	if info, err := s.data.Stat(s.rel(sh.User, sh.Path)); err == nil {
		sh.Dir = info.IsDir()
	}
	sh.Path = "/" + sh.Path
	return sh, nil
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request, u user) {
	if u.Kid {
		jsonError(w, http.StatusForbidden, "Links anyone can open are for grown-ups. Share with someone at home instead.")
		return
	}
	var in struct {
		Path        string `json:"path"`
		Password    string `json:"password"`
		ExpiresDays int    `json:"expiresDays"`
		Mode        string `json:"mode"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := cleanRel(in.Path)
	if p == "." {
		jsonError(w, http.StatusBadRequest, "Share a file or folder, not everything.")
		return
	}
	info, err := s.data.Stat(s.rel(u.Name, p))
	if err != nil {
		fileError(w, err)
		return
	}
	hash := ""
	if in.Password != "" {
		b, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "That password can't be used (it's too long).")
			return
		}
		hash = string(b)
	}
	var expires int64
	if in.ExpiresDays > 0 {
		expires = time.Now().AddDate(0, 0, in.ExpiresDays).Unix()
	}
	mode := 0
	for i, m := range shareModes {
		if m == in.Mode && (m == "edit") != info.IsDir() {
			mode = i
		}
	}
	token := randomToken(12)
	if _, err := s.db.Exec(`INSERT INTO shares (token, user, path, password, expires, upload, created) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		token, u.Name, p, hash, expires, mode, time.Now().Unix()); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sh, err := s.scanShare(s.db.QueryRow(`SELECT `+shareColumns+` FROM shares WHERE token = ?`, token))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sh)
}

func (s *Server) deleteShare(w http.ResponseWriter, r *http.Request, u user) {
	res, err := s.db.Exec(`DELETE FROM shares WHERE token = ? AND user = ?`, r.PathValue("token"), u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, http.StatusNotFound, "There's no such link.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- the public side of a link

const shareColumns = `token, user, path, password, expires, upload, created, downloads`

// The modes a link can have: folders view, upload (view and add) or drop (add only); files view or edit (documents,
// in Collabora).
var shareModes = []string{"view", "upload", "drop", "edit"}

// openShare finds a link that still works; ok is false once the response has been written.
func (s *Server) openShare(w http.ResponseWriter, r *http.Request, needUnlocked bool) (share, bool) {
	sh, err := s.scanShare(s.db.QueryRow(`SELECT `+shareColumns+` FROM shares WHERE token = ?`,
		r.PathValue("token")))
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			jsonError(w, http.StatusInternalServerError, err.Error())
		} else {
			jsonError(w, http.StatusNotFound, "This link doesn't work any more.")
		}
		return sh, false
	}
	if sh.Expires > 0 && time.Now().Unix() > sh.Expires {
		jsonError(w, http.StatusGone, "This link has expired.")
		return sh, false
	}
	if needUnlocked && !s.unlocked(r, sh) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "This link needs its password.", "needsPassword": true})
		return sh, false
	}
	return sh, true
}

func shareCookie(sh share) string { return "cove_s_" + sh.Token }

// The unlock cookie names the password's hash, so changing the password locks the link again.
func (s *Server) unlocked(r *http.Request, sh share) bool {
	if sh.password == "" {
		return true
	}
	c, err := r.Cookie(shareCookie(sh))
	if err != nil {
		return false
	}
	v, ok := s.verify(c.Value)
	return ok && v == sh.Token+"|"+hashHex(sh.password)[:16]
}

// within is the data-folder path of sub, inside the shared folder (or the shared file itself).
func (s *Server) within(sh share, sub string) string {
	base := strings.TrimPrefix(sh.Path, "/")
	if !sh.Dir {
		return s.rel(sh.User, base)
	}
	return s.rel(sh.User, path.Join(base, cleanRel(sub)))
}

func (s *Server) sharePage(w http.ResponseWriter, r *http.Request) { s.page(w, r, "share.html") }

func (s *Server) shareInfo(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.openShare(w, r, false)
	if !ok {
		return
	}
	out := map[string]any{"name": sh.Name, "dir": sh.Dir, "mode": sh.Mode, "owner": sh.User}
	if !s.unlocked(r, sh) {
		out["needsPassword"] = true
		writeJSON(w, http.StatusOK, out)
		return
	}
	if sh.Mode == "drop" { // a drop box shows nothing of what's in it
		writeJSON(w, http.StatusOK, out)
		return
	}
	name := s.within(sh, r.URL.Query().Get("path"))
	info, err := s.data.Stat(name)
	if err != nil {
		fileError(w, err)
		return
	}
	out["item"] = item(info)
	if info.IsDir() {
		f, err := s.data.Open(name)
		if err != nil {
			fileError(w, err)
			return
		}
		defer f.Close()
		entries, _ := f.ReadDir(-1)
		items := []fileItem{}
		for _, e := range entries {
			if hidden(e.Name()) {
				continue
			}
			if info, err := e.Info(); err == nil {
				items = append(items, item(info))
			}
		}
		sortItems(items)
		out["items"] = items
		out["path"] = "/" + strings.TrimPrefix(cleanRel(r.URL.Query().Get("path")), ".")
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) shareUnlock(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.openShare(w, r, false)
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	readJSON(r, &in)
	if sh.password != "" && bcrypt.CompareHashAndPassword([]byte(sh.password), []byte(in.Password)) != nil {
		time.Sleep(500 * time.Millisecond)
		jsonError(w, http.StatusForbidden, "That isn't the password.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: shareCookie(sh), Value: s.sign(sh.Token + "|" + hashHex(sh.password)[:16]),
		Path: "/s/" + sh.Token, MaxAge: 7 * 24 * 3600, HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) shareContent(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.openShare(w, r, true)
	if !ok {
		return
	}
	if sh.Mode == "drop" {
		jsonError(w, http.StatusForbidden, "This link only takes files.")
		return
	}
	if r.URL.Query().Get("download") == "1" && (r.Header.Get("Range") == "" || strings.HasPrefix(r.Header.Get("Range"), "bytes=0-")) {
		s.db.Exec(`UPDATE shares SET downloads = downloads + 1 WHERE token = ?`, sh.Token)
	}
	s.serveFile(w, r, s.within(sh, r.URL.Query().Get("path")), r.URL.Query().Get("download") == "1")
}

func (s *Server) shareZip(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.openShare(w, r, true)
	if !ok {
		return
	}
	if sh.Mode == "drop" {
		jsonError(w, http.StatusForbidden, "This link only takes files.")
		return
	}
	sub := cleanRel(r.URL.Query().Get("path"))
	name := sh.Name + ".zip"
	if sub != "." {
		name = path.Base(sub) + ".zip"
	}
	s.db.Exec(`UPDATE shares SET downloads = downloads + 1 WHERE token = ?`, sh.Token)
	s.serveZip(w, s.within(sh, sub), name)
}

// shareUpload adds a file to a shared folder that allows it. It never replaces anything.
func (s *Server) shareUpload(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.openShare(w, r, true)
	if !ok {
		return
	}
	if sh.Mode == "view" || !sh.Dir {
		jsonError(w, http.StatusForbidden, "This link doesn't take uploads.")
		return
	}
	q := r.URL.Query()
	name := path.Base(cleanRel(q.Get("name")))
	if name == "." || name == "/" || hidden(name) {
		jsonError(w, http.StatusBadRequest, "That isn't a file name Cove can use.")
		return
	}
	dir := path.Join(strings.TrimPrefix(sh.Path, "/"), cleanRel(q.Get("path")))
	if info, err := s.data.Stat(s.rel(sh.User, dir)); err != nil || !info.IsDir() {
		jsonError(w, http.StatusNotFound, "That folder isn't there any more.")
		return
	}
	final, err := s.writeFile(sh.User, path.Join(dir, name), r.Body, false, time.Time{})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			jsonError(w, http.StatusNotFound, "That folder isn't there any more.")
			return
		}
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.once("link|"+sh.Token, 10*time.Minute) {
		s.notify(sh.User, fmt.Sprintf("Someone added “%s” to “%s” through your link", path.Base(final), sh.Name), "#/files/"+url.PathEscape(strings.TrimPrefix(dir, "./")))
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": path.Base(final)})
}

// shareOffice opens a document from a link in Collabora: to edit with an "edit" link, else to view. The guest says
// what name the others editing see.
func (s *Server) shareOffice(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.openShare(w, r, true)
	if !ok {
		return
	}
	if sh.Mode == "drop" {
		jsonError(w, http.StatusForbidden, "This link only takes files.")
		return
	}
	var in struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	readJSON(r, &in)
	name := strings.TrimSpace(in.Name)
	if len(name) > 60 {
		name = name[:60]
	}
	p := strings.TrimPrefix(s.within(sh, in.Path), "files/"+sh.User+"/")
	if p == "files/"+sh.User {
		p = "."
	}
	s.openDocument(w, r, officeToken{Owner: sh.User, Path: p, User: "guest:" + randomToken(6), Name: name, Write: sh.Mode == "edit"})
}

func (s *Server) shareOfficeTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.openShare(w, r, true); ok {
		s.writeOfficeTypes(w, r)
	}
}

// shareQR is a QR code for a link, to scan with a phone.
func (s *Server) shareQR(w http.ResponseWriter, r *http.Request, u user) {
	var token string
	if s.db.QueryRow(`SELECT token FROM shares WHERE token = ? AND user = ?`, r.PathValue("token"), u.Name).Scan(&token) != nil {
		jsonError(w, http.StatusNotFound, "There's no such link.")
		return
	}
	png, err := qrcode.Encode(s.cfg.URL+"/s/"+token, qrcode.Medium, 512)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	io.Copy(w, bytes.NewReader(png))
}
