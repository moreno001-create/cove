package app

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Households: the Fridge, the family chat, meals, chores, the budget and every other household list belong to one
// household, and only its members see them. An admin makes households and puts people in them (Admin →
// Households). Someone in two households (grandparents, a shared-custody kid) switches between them; the app sends
// the one being looked at with each request (X-Cove-Household), and Cove checks they're a member.
//
// What's personal (files, calendars, tasks, contacts, notes, photos, the journal) isn't in a household: it's yours,
// shared only with the people, groups or links you pick, and those can be in any household.

const householdsSchema = `
CREATE TABLE IF NOT EXISTS households (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS household_members (
	household INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	added INTEGER NOT NULL,
	PRIMARY KEY (household, user)
);`

// householdTables are the tables whose rows belong to a household.
var householdTables = []string{"hh", "fridge", "chat_rooms", "albums", "payouts", "reward_claims", "sitter_links"}

// migrateHouseholds gives the household-wide tables a household column and, the first time, puts everyone and
// everything there is into one household (Cove was one household before).
func migrateHouseholds(db *sql.DB) {
	for _, t := range householdTables {
		db.Exec(`ALTER TABLE ` + t + ` ADD COLUMN household INTEGER NOT NULL DEFAULT 0`)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM households`).Scan(&n)
	var users int
	db.QueryRow(`SELECT count(*) FROM users`).Scan(&users)
	if n > 0 || users == 0 {
		return
	}
	res, err := db.Exec(`INSERT INTO households (name, created) VALUES ('Our household', ?)`, time.Now().Unix())
	if err != nil {
		return
	}
	id, _ := res.LastInsertId()
	db.Exec(`INSERT INTO household_members (household, user, added) SELECT ?, name, ? FROM users`, id, time.Now().Unix())
	for _, t := range householdTables {
		db.Exec(`UPDATE `+t+` SET household = ? WHERE household = 0`, id)
	}
	db.Exec(`UPDATE shopping_history SET item = ? || '|' || item WHERE item NOT LIKE '%|%'`, strconv.FormatInt(id, 10))
}

type household struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Members []member `json:"members"`
}

type member struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	Kid     bool   `json:"kid"`
	Guest   bool   `json:"guest"`
}

// homesOf are the households a user is in, in the order they joined them.
func (s *Server) homesOf(name string) []household {
	rows, err := s.db.Query(`SELECT h.id, h.name FROM households h JOIN household_members m ON m.household = h.id WHERE m.user = ? ORDER BY m.added, h.id`, name)
	if err != nil {
		return nil
	}
	var out []household
	for rows.Next() {
		var h household
		if rows.Scan(&h.ID, &h.Name) == nil {
			out = append(out, h)
		}
	}
	rows.Close()
	return out
}

// members are the user names in a household (none for 0).
func (s *Server) members(home int64) []string {
	var out []string
	if rows, err := s.db.Query(`SELECT user FROM household_members WHERE household = ? ORDER BY added`, home); err == nil {
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

// othersIn are a household's members but one.
func (s *Server) othersIn(home int64, name string) []string {
	return slices.DeleteFunc(s.members(home), func(n string) bool { return n == name })
}

func (s *Server) membersOf(home int64) []member {
	out := []member{}
	if rows, err := s.db.Query(`SELECT u.name, u.display, u.kid, u.guest FROM household_members m JOIN users u ON u.name = m.user
		WHERE m.household = ? ORDER BY lower(coalesce(nullif(u.display, ''), u.name))`, home); err == nil {
		for rows.Next() {
			var m member
			if rows.Scan(&m.Name, &m.Display, &m.Kid, &m.Guest) == nil {
				out = append(out, m)
			}
		}
		rows.Close()
	}
	return out
}

func (s *Server) inHousehold(home int64, name string) bool {
	var n int
	s.db.QueryRow(`SELECT count(*) FROM household_members WHERE household = ? AND user = ?`, home, name).Scan(&n)
	return n > 0
}

// homeFor is the household a request is about: the one it names, when the user is in it, else their first.
func (s *Server) homeFor(r *http.Request, name string) int64 {
	want, _ := strconv.ParseInt(firstNonEmpty(r.Header.Get("X-Cove-Household"), r.URL.Query().Get("home")), 10, 64)
	homes := s.homesOf(name)
	for _, h := range homes {
		if h.ID == want {
			return h.ID
		}
	}
	if len(homes) > 0 {
		return homes[0].ID
	}
	return 0
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// needHome answers for household pages when the user isn't in a household yet.
func needHome(w http.ResponseWriter, u user) bool {
	if u.Home == 0 {
		jsonError(w, http.StatusConflict, "You're not in a household yet: ask an admin to add you (Admin → Households).")
		return false
	}
	return true
}

// firstSignIn: the very first person makes the first household; later people wait for an admin to add them (so a
// second family on the same server never lands in the first one's Fridge).
func (s *Server) firstSignIn(name string) {
	var homes, users int
	s.db.QueryRow(`SELECT count(*) FROM households`).Scan(&homes)
	s.db.QueryRow(`SELECT count(*) FROM users`).Scan(&users)
	if homes == 0 {
		res, err := s.db.Exec(`INSERT INTO households (name, created) VALUES ('Our household', ?)`, time.Now().Unix())
		if err == nil {
			id, _ := res.LastInsertId()
			s.db.Exec(`INSERT OR IGNORE INTO household_members (household, user, added) VALUES (?, ?, ?)`, id, name, time.Now().Unix())
		}
		return
	}
	if len(s.homesOf(name)) == 0 && s.once("newperson|"+name, 365*24*time.Hour) {
		for _, a := range s.admins() {
			if a != name {
				s.notify(a, s.displayOf(name)+" signed in to Cove for the first time: add them to a household", "#/admin")
			}
		}
	}
}

// --- the API

func (s *Server) myHouseholds(w http.ResponseWriter, r *http.Request, u user) {
	homes := s.homesOf(u.Name)
	if homes == nil {
		homes = []household{}
	}
	for i := range homes {
		homes[i].Members = s.membersOf(homes[i].ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"homes": homes, "current": u.Home})
}

func (s *Server) adminHouseholds(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT id, name FROM households ORDER BY id`)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []household{}
	for rows.Next() {
		var h household
		if rows.Scan(&h.ID, &h.Name) == nil {
			out = append(out, h)
		}
	}
	rows.Close()
	for i := range out {
		out[i].Members = s.membersOf(out[i].ID)
	}
	var lone []string
	if rows, err := s.db.Query(`SELECT name FROM users WHERE name NOT IN (SELECT user FROM household_members) ORDER BY name`); err == nil {
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				lone = append(lone, n)
			}
		}
		rows.Close()
	}
	if lone == nil {
		lone = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"households": out, "nowhere": lone})
}

func (s *Server) saveHousehold(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
		jsonError(w, http.StatusBadRequest, "Give the household a name (“The Morenos”, “Grandma & Grandpa”).")
		return
	}
	name := clip(in.Name, 80)
	if id := r.PathValue("id"); id != "" {
		res, _ := s.db.Exec(`UPDATE households SET name = ? WHERE id = ?`, name, id)
		if n, _ := res.RowsAffected(); n == 0 {
			jsonError(w, http.StatusNotFound, "There's no such household.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	res, err := s.db.Exec(`INSERT INTO households (name, created) VALUES (?, ?)`, name, time.Now().Unix())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) deleteHousehold(w http.ResponseWriter, r *http.Request, u user) {
	id := r.PathValue("id")
	var members, things int
	s.db.QueryRow(`SELECT count(*) FROM household_members WHERE household = ?`, id).Scan(&members)
	for _, t := range householdTables {
		var n int
		s.db.QueryRow(`SELECT count(*) FROM `+t+` WHERE household = ?`, id).Scan(&n)
		things += n
	}
	if members > 0 || things > 0 {
		jsonError(w, http.StatusConflict, "Only an empty household can be removed: take everyone out first, and it must have nothing on its Fridge, lists or chat.")
		return
	}
	s.db.Exec(`DELETE FROM households WHERE id = ?`, id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) setMember(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		User string `json:"user"`
		In   bool   `json:"in"`
	}
	if err := readJSON(r, &in); err != nil || in.User == "" {
		jsonError(w, http.StatusBadRequest, "Who?")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var name string
	if s.db.QueryRow(`SELECT name FROM households WHERE id = ?`, id).Scan(&name) != nil {
		jsonError(w, http.StatusNotFound, "There's no such household.")
		return
	}
	if !in.In {
		s.db.Exec(`DELETE FROM household_members WHERE household = ? AND user = ?`, id, in.User)
		s.changed("household", in.User)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO household_members (household, user, added) VALUES (?, ?, ?)`, id, in.User, time.Now().Unix()); err != nil {
		jsonError(w, http.StatusBadRequest, "There's no such person in Cove (they need to have signed in once).")
		return
	}
	if in.User != u.Name {
		s.notify(in.User, displayName(u)+" added you to the household “"+name+"”", "#/home")
	}
	s.changed("household", in.User)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

var errNoHome = errors.New("not in a household")
