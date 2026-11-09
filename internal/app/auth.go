package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookie = "cove_session"
	loginCookie   = "cove_login"
	sessionLife   = 30 * 24 * time.Hour
)

type user struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	Email   string `json:"email"`
	Admin   bool   `json:"admin"` // in Homeport's admins group
	Kid     bool   `json:"kid"`   // an admin made this a kid's account: a simpler Cove, no money or public links
	Guest   bool   `json:"guest"` // family who don't live here (grandparents): the calendar, shared albums, the fridge, chat
	// Groups are the Homeport groups signing in reported (nil: unknown, leave them as they are).
	Groups []string `json:"-"`
}

var userPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// validUser says whether a name can be a user (it's also their folder's name).
func validUser(name string) bool {
	return userPattern.MatchString(name) && !strings.Contains(name, "..")
}

func (s *Server) currentUser(r *http.Request) (user, bool) {
	if s.cfg.DevUser != "" {
		u := user{Name: s.cfg.DevUser, Display: s.cfg.DevUser, Admin: true}
		if s.ensureUser(u) != nil {
			return u, false
		}
		s.db.QueryRow(`SELECT kid, guest FROM users WHERE name = ?`, u.Name).Scan(&u.Kid, &u.Guest)
		return u, true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return user{}, false
	}
	var u user
	var expires int64
	var seen int64
	err = s.db.QueryRow(`SELECT u.name, u.display, u.email, u.admin, u.kid, u.guest, u.seen, s.expires FROM sessions s JOIN users u ON u.name = s.user
		WHERE s.id = ?`, hashHex(c.Value)).Scan(&u.Name, &u.Display, &u.Email, &u.Admin, &u.Kid, &u.Guest, &seen, &expires)
	if err != nil || expires < time.Now().Unix() {
		return user{}, false
	}
	if time.Since(time.Unix(seen, 0)) > 5*time.Minute {
		s.db.Exec(`UPDATE users SET seen = ? WHERE name = ?`, time.Now().Unix(), u.Name)
	}
	return u, true
}

// api wraps the app's own API: a signed-in user, and for changes the X-Cove header, which other sites' pages can't
// send (so they can't make changes with someone's cookie).
func (s *Server) api(h func(http.ResponseWriter, *http.Request, user)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		if !ok {
			jsonError(w, http.StatusUnauthorized, "You're signed out.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Cove") != "1" {
			jsonError(w, http.StatusForbidden, "Changes need the X-Cove header.")
			return
		}
		if u.Guest && !u.Admin && !guestMay(r) {
			jsonError(w, http.StatusForbidden, "That part of Cove is for the people who live here.")
			return
		}
		h(w, r, u)
	}
}

// ensureUser records a user who signed in, and makes their folders and first calendar.
func (s *Server) ensureUser(u user) error {
	if !validUser(u.Name) {
		return fmt.Errorf("%q can't be a Cove user name", u.Name)
	}
	_, err := s.db.Exec(`INSERT INTO users (name, display, email, admin, created, seen) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET display = excluded.display, email = excluded.email, admin = excluded.admin, seen = excluded.seen`,
		u.Name, u.Display, u.Email, u.Admin, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		return err
	}
	for _, d := range []string{"files/" + u.Name, "trash/" + u.Name} {
		if err := s.data.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	if _, err = s.calendars(u.Name); err != nil {
		return err
	}
	if u.Groups != nil {
		s.setGroups(u.Name, u.Groups)
	}
	return nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u user) {
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "url": s.cfg.URL, "office": s.cfg.Collabora != "",
		"version": s.cfg.Version, "devMode": s.cfg.DevUser != "", "ai": s.aiConfig().on(), "smart": s.aiConfig().Embed != "", "ocr": ocrAvailable(),
		"home": s.haConfig().Token != "" && len(s.haConfig().Entities) > 0, "jellyfin": s.jellyConfig().Key != "", "call": s.setting("call.url") != "", "vision": s.aiConfig().Vision != "",
		"backups": s.offsiteConfig().Repo != ""})
}

// --- signing in with Homeport (OpenID Connect, code flow with PKCE)

type oidcEndpoints struct {
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	Userinfo      string `json:"userinfo_endpoint"`
}

func (s *Server) oidc(ctx context.Context) (*oidcEndpoints, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endpoints != nil {
		return s.endpoints, nil
	}
	var ep oidcEndpoints
	if err := s.getJSON(ctx, s.cfg.Issuer+"/.well-known/openid-configuration", "", &ep); err != nil {
		return nil, fmt.Errorf("can't reach Homeport's sign-in at %s: %w", s.cfg.Issuer, err)
	}
	if ep.Authorization == "" || ep.Token == "" || ep.Userinfo == "" {
		return nil, errors.New("Homeport's sign-in didn't name its endpoints")
	}
	s.endpoints = &ep
	return &ep, nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DevUser != "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ep, err := s.oidc(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	state, verifier := randomToken(16), randomToken(32)
	sum := sha256.Sum256([]byte(verifier))
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: s.sign(state + "." + verifier), Path: "/auth/", MaxAge: 600,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode})
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {s.cfg.ClientID},
		"redirect_uri":          {s.cfg.URL + "/auth/callback"},
		"scope":                 {"openid profile email groups"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, ep.Authorization+"?"+q.Encode(), http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		http.Error(w, "Homeport didn't sign you in: "+e+" "+q.Get("error_description"), http.StatusForbidden)
		return
	}
	c, err := r.Cookie(loginCookie)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	v, ok := s.verify(c.Value)
	state, verifier, _ := strings.Cut(v, ".")
	if !ok || state == "" || q.Get("state") != state {
		http.Error(w, "This sign-in link is out of date. Open Cove again.", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Path: "/auth/", MaxAge: -1})
	u, err := s.exchange(r.Context(), q.Get("code"), verifier)
	if err != nil {
		http.Error(w, "Signing in didn't work: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := s.ensureUser(u); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	token := randomToken(32)
	if _, err := s.db.Exec(`INSERT INTO sessions (id, user, expires) VALUES (?, ?, ?)`, hashHex(token), u.Name,
		time.Now().Add(sessionLife).Unix()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", MaxAge: int(sessionLife.Seconds()),
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

// exchange trades the sign-in code for an access token, and that for who signed in.
func (s *Server) exchange(ctx context.Context, code, verifier string) (user, error) {
	ep, err := s.oidc(ctx)
	if err != nil {
		return user{}, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.cfg.URL + "/auth/callback"},
		"code_verifier": {verifier}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ep.Token, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(s.cfg.ClientID), url.QueryEscape(s.cfg.ClientSecret))
	res, err := s.http.Do(req)
	if err != nil {
		return user{}, err
	}
	defer res.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok)
	if res.StatusCode != http.StatusOK || tok.AccessToken == "" {
		return user{}, fmt.Errorf("Homeport answered %d %s %s", res.StatusCode, tok.Error, tok.Description)
	}
	var info struct {
		Sub      string   `json:"sub"`
		Username string   `json:"preferred_username"`
		Name     string   `json:"name"`
		Email    string   `json:"email"`
		Groups   []string `json:"groups"`
	}
	if err := s.getJSON(ctx, ep.Userinfo, tok.AccessToken, &info); err != nil {
		return user{}, err
	}
	name := info.Username
	if name == "" {
		name = info.Sub
	}
	groups := info.Groups
	if groups == nil {
		groups = []string{}
	}
	return user{Name: name, Display: info.Name, Email: info.Email, Admin: slices.Contains(info.Groups, "admins"), Groups: groups}, nil
}

func (s *Server) getJSON(ctx context.Context, u, bearer string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", u, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v)
}

// sameOrigin says a form post came from Cove's own pages or the phone itself (the share sheet), not another site
// (Homeport's other apps live on sibling addresses, which cookies count as the same site).
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		origin := r.Header.Get("Origin")
		return origin == "" || origin == "null" || strings.EqualFold(strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://"), r.Host)
	}
	return false
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "Sign out from Cove itself.", http.StatusForbidden)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.db.Exec(`DELETE FROM sessions WHERE id = ?`, hashHex(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/signed-out", http.StatusSeeOther)
}

func (s *Server) signedOut(w http.ResponseWriter, r *http.Request) { s.page(w, r, "signed-out.html") }

// --- app passwords, for sync apps (WebDAV and CalDAV can't sign in through Homeport)

func (s *Server) davUser(r *http.Request) (user, bool) {
	name, pass, ok := r.BasicAuth()
	if !ok || pass == "" {
		return user{}, false
	}
	var u user
	var id int64
	err := s.db.QueryRow(`SELECT a.id, u.name, u.display, u.email FROM app_passwords a JOIN users u ON u.name = a.user
		WHERE a.hash = ? AND a.user = ?`, hashHex(normalizeAppPassword(pass)), name).Scan(&id, &u.Name, &u.Display, &u.Email)
	if err != nil {
		return user{}, false
	}
	s.db.Exec(`UPDATE app_passwords SET last_used = ? WHERE id = ? AND last_used < ?`, time.Now().Unix(), id,
		time.Now().Add(-time.Minute).Unix())
	return u, true
}

// normalizeAppPassword ignores case and the dashes between groups, as people type them on phones.
func normalizeAppPassword(p string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(p))
}

type appPassword struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Created  int64  `json:"created"`
	LastUsed int64  `json:"lastUsed"`
}

func (s *Server) listAppPasswords(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT id, name, created, last_used FROM app_passwords WHERE user = ? ORDER BY created DESC`, u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []appPassword{}
	for rows.Next() {
		var a appPassword
		if rows.Scan(&a.ID, &a.Name, &a.Created, &a.LastUsed) == nil {
			list = append(list, a)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createAppPassword(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		jsonError(w, http.StatusBadRequest, "Give it a name (up to 80 characters), like “iPhone”.")
		return
	}
	pass := newAppPassword()
	if _, err := s.db.Exec(`INSERT INTO app_passwords (user, name, hash, created) VALUES (?, ?, ?, ?)`, u.Name, in.Name,
		hashHex(normalizeAppPassword(pass)), time.Now().Unix()); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"password": pass, "user": u.Name})
}

// newAppPassword is 25 random letters and digits in groups of five (about 125 bits).
func newAppPassword() string {
	const chars = "abcdefghjkmnpqrstuvwxyz23456789" // no 0/o, 1/l/i
	var out strings.Builder
	for n := 0; n < 25; {
		var b [1]byte
		rand.Read(b[:])
		if int(b[0]) >= 256/len(chars)*len(chars) { // keeps every character equally likely
			continue
		}
		if n > 0 && n%5 == 0 {
			out.WriteByte('-')
		}
		out.WriteByte(chars[int(b[0])%len(chars)])
		n++
	}
	return out.String()
}

func (s *Server) deleteAppPassword(w http.ResponseWriter, r *http.Request, u user) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	res, err := s.db.Exec(`DELETE FROM app_passwords WHERE id = ? AND user = ?`, id, u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, http.StatusNotFound, "There's no such app password.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

var errNotFound = sql.ErrNoRows
