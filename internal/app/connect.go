package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// What Cove connects to at home, set up by an admin: Home Assistant (lights, locks, the garage door, temperatures,
// the solar inverter's numbers, the weather), Jellyfin ("continue watching"), and an off-site copy of Cove's files
// and database snapshots with restic. Tokens and passwords are kept in Cove's database and never sent back to
// the browser.

const settingsSchema = `
CREATE TABLE IF NOT EXISTS app_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`

func (s *Server) setting(key string) string {
	var v string
	s.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v)
	return v
}

func (s *Server) setSetting(key, value string) {
	s.db.Exec(`INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
}

func (s *Server) settingJSON(key string, v any) {
	json.Unmarshal([]byte(s.setting(key)), v)
}

func (s *Server) setSettingJSON(key string, v any) {
	b, _ := json.Marshal(v)
	s.setSetting(key, string(b))
}

func cleanURL(u string) (string, error) {
	u = strings.TrimSuffix(strings.TrimSpace(u), "/")
	if u == "" {
		return "", nil
	}
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return "", fmt.Errorf("%q isn't an address like http://192.168.1.20:8123", u)
	}
	return u, nil
}

var connectHTTP = &http.Client{Timeout: 15 * time.Second}

// --- Home Assistant

type haEntity struct {
	ID    string `json:"id"`    // light.kitchen
	Label string `json:"label"` // what Cove calls it ("" : Home Assistant's name)
	Home  bool   `json:"home"`  // on the home page
}

type haConfig struct {
	URL      string     `json:"url"`
	Token    string     `json:"token,omitempty"`
	Entities []haEntity `json:"entities"`
	// Energy: sensors for the solar card (Home Assistant's EG4, Enphase, SolarEdge… integrations).
	Solar   string `json:"solar"`   // power now (W)
	Today   string `json:"today"`   // energy made today (kWh)
	Battery string `json:"battery"` // battery level (%)
	Load    string `json:"load"`    // the house's use now (W)
	Grid    string `json:"grid"`    // from (+) or to (−) the grid now (W)
	Weather string `json:"weather"` // weather.home
}

func (s *Server) haConfig() haConfig {
	var c haConfig
	s.settingJSON("ha", &c)
	return c
}

func (s *Server) haDo(ctx context.Context, method, path string, body any, out any) error {
	c := s.haConfig()
	if c.URL == "" || c.Token == "" {
		return errors.New("Home Assistant isn't connected (Admin → Home)")
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := connectHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Home Assistant can't be reached: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == 401 {
		return errors.New("Home Assistant didn't take the token (make a new long-lived token in your Home Assistant profile)")
	}
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 500))
		return fmt.Errorf("Home Assistant answered %d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 20<<20)).Decode(out)
	}
	return nil
}

type haState struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged string         `json:"last_changed"`
}

type deviceView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	State    string `json:"state"`
	Unit     string `json:"unit,omitempty"`
	Can      bool   `json:"can"`     // u may switch it
	Careful  bool   `json:"careful"` // a lock or a door: ask first
	Home     bool   `json:"home"`
	Category string `json:"category"` // control or sensor
}

// controllable domains, and the ones that need an "are you sure" (and aren't for kids)
var haToggles = map[string]bool{"light": true, "switch": true, "fan": true, "lock": true, "cover": true, "input_boolean": true, "scene": true, "script": true, "media_player": true, "climate": false}
var haCareful = map[string]bool{"lock": true, "cover": true, "alarm_control_panel": true}

func (s *Server) haStates(ctx context.Context) (map[string]haState, error) {
	var list []haState
	if err := s.haDo(ctx, "GET", "/api/states", nil, &list); err != nil {
		return nil, err
	}
	out := map[string]haState{}
	for _, st := range list {
		out[st.EntityID] = st
	}
	return out, nil
}

func (s *Server) devices(ctx context.Context, u user) ([]deviceView, map[string]haState, error) {
	c := s.haConfig()
	states, err := s.haStates(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := []deviceView{}
	for _, e := range c.Entities {
		st, ok := states[e.ID]
		if !ok {
			continue
		}
		domain, _, _ := strings.Cut(e.ID, ".")
		d := deviceView{ID: e.ID, Name: e.Label, Domain: domain, State: st.State, Home: e.Home, Careful: haCareful[domain]}
		if d.Name == "" {
			d.Name, _ = st.Attributes["friendly_name"].(string)
		}
		d.Unit, _ = st.Attributes["unit_of_measurement"].(string)
		d.Category = "sensor"
		if _, ok := haToggles[domain]; ok {
			d.Category = "control"
			d.Can = !(u.Kid && d.Careful)
		}
		out = append(out, d)
	}
	return out, states, nil
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request, u user) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, states, err := s.devices(ctx, u)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": list, "energy": s.energy(states), "weather": s.weather(states), "people": whoIsHome(states)})
}

type energyView struct {
	Solar   *float64 `json:"solar"`
	Today   *float64 `json:"today"`
	Battery *float64 `json:"battery"`
	Load    *float64 `json:"load"`
	Grid    *float64 `json:"grid"`
}

func (s *Server) energy(states map[string]haState) *energyView {
	c := s.haConfig()
	num := func(id string) *float64 {
		st, ok := states[id]
		if id == "" || !ok {
			return nil
		}
		f, err := strconv.ParseFloat(st.State, 64)
		if err != nil {
			return nil
		}
		if unit, _ := st.Attributes["unit_of_measurement"].(string); unit == "kW" {
			f *= 1000
		}
		return &f
	}
	e := energyView{num(c.Solar), num(c.Today), num(c.Battery), num(c.Load), num(c.Grid)}
	if e.Solar == nil && e.Today == nil && e.Battery == nil && e.Load == nil && e.Grid == nil {
		return nil
	}
	return &e
}

type weatherView struct {
	State string  `json:"state"` // sunny, rainy, partlycloudy…
	Temp  float64 `json:"temp"`
	Unit  string  `json:"unit"`
}

func (s *Server) weather(states map[string]haState) *weatherView {
	st, ok := states[s.haConfig().Weather]
	if !ok {
		return nil
	}
	w := weatherView{State: st.State}
	w.Temp, _ = st.Attributes["temperature"].(float64)
	w.Unit, _ = st.Attributes["temperature_unit"].(string)
	return &w
}

// switchDevice turns something on or off (or locks, opens…) in Home Assistant.
func (s *Server) switchDevice(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		ID     string `json:"id"`
		Action string `json:"action"` // toggle, on, off, lock, unlock, open, close
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.haAction(r.Context(), u, in.ID, in.Action); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) haAction(ctx context.Context, u user, id, action string) error {
	c := s.haConfig()
	if !slices.ContainsFunc(c.Entities, func(e haEntity) bool { return e.ID == id }) {
		return errors.New("That isn't one of the things Cove shows.")
	}
	domain, _, _ := strings.Cut(id, ".")
	if u.Kid && haCareful[domain] {
		return errors.New("Ask a grown-up to do that.")
	}
	service := map[string]string{"toggle": "toggle", "on": "turn_on", "off": "turn_off", "lock": "lock", "unlock": "unlock", "open": "open_cover", "close": "close_cover"}[action]
	switch {
	case service == "":
		return errors.New("Cove doesn't know how to do that.")
	case domain == "lock" && service == "toggle":
		return errors.New("Say lock or unlock.")
	case domain == "cover" && service == "toggle":
		service = "toggle"
	case domain == "scene" || domain == "script":
		service = "turn_on"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.haDo(ctx, "POST", "/api/services/"+domain+"/"+service, map[string]string{"entity_id": id}, nil); err != nil {
		return err
	}
	if haCareful[domain] {
		log.Printf("%s: %s %s", u.Name, service, id)
	}
	return nil
}

// --- the admin's settings for Home Assistant

func (s *Server) getHA(w http.ResponseWriter, r *http.Request, u user) {
	c := s.haConfig()
	set := c.Token != ""
	c.Token = ""
	writeJSON(w, http.StatusOK, map[string]any{"config": c, "tokenSet": set})
}

func (s *Server) putHA(w http.ResponseWriter, r *http.Request, u user) {
	var in haConfig
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	var err error
	if in.URL, err = cleanURL(in.URL); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(in.Token) == "" { // left empty: keep the one there is
		in.Token = s.haConfig().Token
	}
	in.Token = strings.TrimSpace(in.Token)
	s.setSettingJSON("ha", in)
	s.getHA(w, r, u)
}

// haEntities lists everything Home Assistant has, for the admin to pick from.
func (s *Server) haEntities(w http.ResponseWriter, r *http.Request, u user) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	states, err := s.haStates(ctx)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	type ent struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		State string `json:"state"`
		Unit  string `json:"unit"`
	}
	out := []ent{}
	for id, st := range states {
		n, _ := st.Attributes["friendly_name"].(string)
		unit, _ := st.Attributes["unit_of_measurement"].(string)
		out = append(out, ent{id, n, st.State, unit})
	}
	slices.SortFunc(out, func(a, b ent) int { return strings.Compare(a.ID, b.ID) })
	writeJSON(w, http.StatusOK, out)
}

// --- Jellyfin

type jellyConfig struct {
	URL    string `json:"url"`    // where Cove reaches it
	Public string `json:"public"` // where people open it
	Key    string `json:"key,omitempty"`
}

func (s *Server) jellyConfig() jellyConfig {
	var c jellyConfig
	s.settingJSON("jellyfin", &c)
	if c.Public == "" {
		c.Public = c.URL
	}
	return c
}

func (s *Server) jellyGet(ctx context.Context, path string, out any) error {
	c := s.jellyConfig()
	if c.URL == "" || c.Key == "" {
		return errors.New("Jellyfin isn't connected (Admin → Home)")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.URL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", `MediaBrowser Token="`+c.Key+`"`)
	res, err := connectHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Jellyfin can't be reached: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("Jellyfin answered %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 20<<20)).Decode(out)
}

type jellyItem struct {
	ID       string  `json:"Id"`
	Name     string  `json:"Name"`
	Series   string  `json:"SeriesName"`
	Type     string  `json:"Type"`
	Year     int     `json:"ProductionYear"`
	Progress float64 `json:"progress"`
	UserData *struct {
		PlayedPercentage float64 `json:"PlayedPercentage"`
	} `json:"UserData,omitempty"`
}

// jellyUser is the Jellyfin account with the same name as the Cove one (or its display name).
func (s *Server) jellyUser(ctx context.Context, u user) (string, error) {
	var users []struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	}
	if err := s.jellyGet(ctx, "/Users", &users); err != nil {
		return "", err
	}
	for _, x := range users {
		if strings.EqualFold(x.Name, u.Name) || u.Display != "" && strings.EqualFold(x.Name, u.Display) {
			return x.ID, nil
		}
	}
	return "", fmt.Errorf("there's no Jellyfin account called %s", u.Name)
}

func (s *Server) watching(w http.ResponseWriter, r *http.Request, u user) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	id, err := s.jellyUser(ctx, u)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	var resume struct {
		Items []jellyItem `json:"Items"`
	}
	s.jellyGet(ctx, "/Users/"+url.PathEscape(id)+"/Items/Resume?Limit=8&MediaTypes=Video", &resume)
	var latest []jellyItem
	s.jellyGet(ctx, "/Users/"+url.PathEscape(id)+"/Items/Latest?Limit=12&IncludeItemTypes=Movie,Episode", &latest)
	for _, l := range [][]jellyItem{resume.Items, latest} {
		for i := range l {
			if l[i].UserData != nil {
				l[i].Progress = l[i].UserData.PlayedPercentage
				l[i].UserData = nil
			}
		}
	}
	if resume.Items == nil {
		resume.Items = []jellyItem{}
	}
	if latest == nil {
		latest = []jellyItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"resume": resume.Items, "latest": latest, "url": s.jellyConfig().Public})
}

// jellyImage passes a poster through (the browser can't send Jellyfin's key).
func (s *Server) jellyImage(w http.ResponseWriter, r *http.Request, u user) {
	c := s.jellyConfig()
	id := r.PathValue("id")
	if c.URL == "" || strings.ContainsAny(id, "/?#.") {
		http.NotFound(w, r)
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), "GET", c.URL+"/Items/"+url.PathEscape(id)+"/Images/Primary?maxHeight=360&quality=85", nil)
	req.Header.Set("Authorization", `MediaBrowser Token="`+c.Key+`"`)
	res, err := connectHTTP.Do(req)
	if err != nil || res.StatusCode != 200 {
		if res != nil {
			res.Body.Close()
		}
		http.NotFound(w, r)
		return
	}
	defer res.Body.Close()
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	io.Copy(w, io.LimitReader(res.Body, 10<<20))
}

func (s *Server) getJelly(w http.ResponseWriter, r *http.Request, u user) {
	c := s.jellyConfig()
	set := c.Key != ""
	c.Key = ""
	writeJSON(w, http.StatusOK, map[string]any{"config": c, "keySet": set})
}

func (s *Server) putJelly(w http.ResponseWriter, r *http.Request, u user) {
	var in jellyConfig
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	var err error
	if in.URL, err = cleanURL(in.URL); err == nil {
		in.Public, err = cleanURL(in.Public)
	}
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(in.Key) == "" {
		in.Key = s.jellyConfig().Key
	}
	in.Key = strings.TrimSpace(in.Key)
	s.setSettingJSON("jellyfin", in)
	s.getJelly(w, r, u)
}

// --- off-site backups with restic

type offsiteConfig struct {
	Repo     string `json:"repo"` // s3:…, b2:…, sftp:…, rest:…
	Password string `json:"password,omitempty"`
	Env      string `json:"env,omitempty"` // KEY=value lines: AWS_ACCESS_KEY_ID, B2_ACCOUNT_ID…
	Hour     int    `json:"hour"`          // when, each night (local time of the server)
}

type offsiteStatus struct {
	Last  int64  `json:"last"`
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Size  string `json:"size"`
	Count int    `json:"count"`
}

func (s *Server) offsiteConfig() offsiteConfig {
	c := offsiteConfig{Hour: 3}
	s.settingJSON("offsite", &c)
	return c
}

func resticPath() string {
	p, _ := exec.LookPath(envOr("COVE_RESTIC", "restic"))
	return p
}

func (s *Server) restic(ctx context.Context, c offsiteConfig, args ...string) ([]byte, error) {
	bin := resticPath()
	if bin == "" {
		return nil, errors.New("restic isn't in Cove's image")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY="+c.Repo, "RESTIC_PASSWORD="+c.Password, "RESTIC_CACHE_DIR="+s.cfg.Data+"/.restic-cache")
	for _, line := range strings.Split(c.Env, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k != "" && !strings.ContainsAny(k, " \t") {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return out.Bytes(), fmt.Errorf("restic %s: %v: %s", args[0], err, msg)
	}
	return out.Bytes(), nil
}

// backUpOffsite sends the files and the database snapshots to the off-site repository (making it the first time).
func (s *Server) backUpOffsite() error {
	c := s.offsiteConfig()
	if c.Repo == "" || c.Password == "" {
		return errors.New("off-site backups aren't set up")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	defer cancel()
	st := offsiteStatus{Last: time.Now().Unix()}
	defer func() { s.setSettingJSON("offsite.status", st) }()
	if _, err := s.restic(ctx, c, "cat", "config"); err != nil {
		if _, err := s.restic(ctx, c, "init"); err != nil {
			st.Error = err.Error()
			return err
		}
	}
	s.snapshot(time.Now())
	_, err := s.restic(ctx, c, "backup", "--tag", "cove", "--exclude-caches",
		s.cfg.Data+"/files", s.cfg.Data+"/backups", s.cfg.Data+"/versions", s.cfg.Data+"/fridge", s.cfg.Data+"/chat", s.cfg.Data+"/secret.key")
	if err != nil {
		st.Error = err.Error()
		return err
	}
	if time.Now().Weekday() == time.Sunday {
		s.restic(ctx, c, "forget", "--tag", "cove", "--keep-daily", "14", "--keep-weekly", "8", "--keep-monthly", "12", "--prune")
	}
	if out, err := s.restic(ctx, c, "snapshots", "--tag", "cove", "--json"); err == nil {
		var snaps []json.RawMessage
		json.Unmarshal(out, &snaps)
		st.Count = len(snaps)
	}
	if out, err := s.restic(ctx, c, "stats", "--json"); err == nil {
		var x struct {
			Total int64 `json:"total_size"`
		}
		json.Unmarshal(out, &x)
		st.Size = fmtBytes(x.Total)
	}
	st.OK = true
	return nil
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// offsiteEvery runs the backup once a night at the hour set.
func (s *Server) offsiteEvery() {
	last := ""
	for {
		time.Sleep(10 * time.Minute)
		c := s.offsiteConfig()
		now := time.Now()
		if c.Repo == "" || now.Hour() != c.Hour || last == now.Format("2006-01-02") {
			continue
		}
		last = now.Format("2006-01-02")
		if err := s.backUpOffsite(); err != nil {
			log.Printf("off-site backup: %v", err)
			for _, a := range s.admins() {
				s.notify(a, "The off-site backup didn't work: "+firstLine(err.Error()), "#/admin")
			}
		}
	}
}

func (s *Server) admins() []string {
	var out []string
	if rows, err := s.db.Query(`SELECT name FROM users WHERE admin = 1`); err == nil {
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				out = append(out, n)
			}
		}
		rows.Close()
	}
	return out
}

func (s *Server) getOffsite(w http.ResponseWriter, r *http.Request, u user) {
	c := s.offsiteConfig()
	set := c.Password != ""
	envKeys := []string{}
	for _, line := range strings.Split(c.Env, "\n") {
		if k, _, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			envKeys = append(envKeys, k)
		}
	}
	c.Password, c.Env = "", ""
	var st offsiteStatus
	s.settingJSON("offsite.status", &st)
	writeJSON(w, http.StatusOK, map[string]any{"config": c, "passwordSet": set, "envKeys": envKeys, "status": st, "restic": resticPath() != ""})
}

func (s *Server) putOffsite(w http.ResponseWriter, r *http.Request, u user) {
	var in offsiteConfig
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	old := s.offsiteConfig()
	in.Repo = strings.TrimSpace(in.Repo)
	if in.Password == "" {
		in.Password = old.Password
	}
	if strings.TrimSpace(in.Env) == "" {
		in.Env = old.Env
	}
	in.Hour = min(max(in.Hour, 0), 23)
	s.setSettingJSON("offsite", in)
	s.getOffsite(w, r, u)
}

func (s *Server) runOffsite(w http.ResponseWriter, r *http.Request, u user) {
	go func() {
		if err := s.backUpOffsite(); err != nil {
			s.notify(u.Name, "The off-site backup didn't work: "+firstLine(err.Error()), "#/admin")
		} else {
			s.notify(u.Name, "The off-site backup is done.", "#/admin")
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}
