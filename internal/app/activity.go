package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// The activity log: what changed in a person's files, calendars, tasks and contacts, and who did it (them, or
// someone they share with), with a way to undo it for a week.

const activitySchema = `
CREATE TABLE IF NOT EXISTS activity (
	id INTEGER PRIMARY KEY,
	owner TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	actor TEXT NOT NULL,
	text TEXT NOT NULL,
	undo TEXT NOT NULL,
	created INTEGER NOT NULL,
	undone INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS activity_owner ON activity(owner, created);`

// undoStep says how to take a change back.
type undoStep struct {
	Kind    string `json:"kind"` // trash-file, restore-file, move-file, restore-item, put-object, trash-object, put-card, trash-card
	Path    string `json:"path,omitempty"`
	To      string `json:"to,omitempty"`
	TrashID string `json:"trash,omitempty"`
	Cal     string `json:"cal,omitempty"`
	Name    string `json:"name,omitempty"`
	Data    string `json:"data,omitempty"`
}

func (s *Server) logActivity(owner string, actor user, text string, undo undoStep) {
	b, _ := json.Marshal(undo)
	s.db.Exec(`INSERT INTO activity (owner, actor, text, undo, created) VALUES (?, ?, ?, ?, ?)`, owner, actor.Name, text, string(b), time.Now().Unix())
	s.db.Exec(`DELETE FROM activity WHERE owner = ? AND created < ?`, owner, time.Now().Add(-90*24*time.Hour).Unix())
}

func (s *Server) listActivity(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT a.id, a.actor, coalesce(nullif(x.display, ''), a.actor), a.text, a.undo, a.created, a.undone FROM activity a
		LEFT JOIN users x ON x.name = a.actor WHERE a.owner = ? ORDER BY a.created DESC, a.id DESC LIMIT 300`, u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type entry struct {
		ID      int64  `json:"id"`
		Actor   string `json:"actor"`
		Who     string `json:"who"`
		Text    string `json:"text"`
		Created int64  `json:"created"`
		Undone  bool   `json:"undone"`
		CanUndo bool   `json:"canUndo"`
	}
	list := []entry{}
	for rows.Next() {
		var e entry
		var undo string
		if rows.Scan(&e.ID, &e.Actor, &e.Who, &e.Text, &undo, &e.Created, &e.Undone) == nil {
			e.CanUndo = !e.Undone && !strings.Contains(undo, `"kind":""`) && time.Since(time.Unix(e.Created, 0)) < 7*24*time.Hour
			list = append(list, e)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) undoActivity(w http.ResponseWriter, r *http.Request, u user) {
	var raw string
	var created int64
	var undone bool
	if err := s.db.QueryRow(`SELECT undo, created, undone FROM activity WHERE id = ? AND owner = ?`, r.PathValue("id"), u.Name).Scan(&raw, &created, &undone); err != nil {
		jsonError(w, http.StatusNotFound, "That isn't in your activity.")
		return
	}
	if undone || time.Since(time.Unix(created, 0)) > 7*24*time.Hour {
		jsonError(w, http.StatusBadRequest, "That can't be undone any more.")
		return
	}
	var step undoStep
	json.Unmarshal([]byte(raw), &step)
	if err := s.undo(u, step); err != nil {
		jsonError(w, http.StatusBadRequest, "Undoing didn't work: "+err.Error())
		return
	}
	s.db.Exec(`UPDATE activity SET undone = 1 WHERE id = ?`, r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) undo(u user, st undoStep) error {
	owner := u.Name
	switch st.Kind {
	case "trash-file":
		_, err := s.trashID(owner, st.Path)
		return err
	case "restore-file":
		_, err := s.restoreFile(owner, st.TrashID)
		return err
	case "move-file":
		return s.move(owner, st.To, st.Path)
	case "restore-item":
		return s.restoreItem(owner, st.TrashID)
	case "put-object", "put-card":
		if st.Kind == "put-card" {
			c, err := decodeCard(st.Data)
			if err == nil {
				_, err = s.saveCard(owner, st.Cal, st.Name, c)
			}
			return err
		}
		obj, err := ical.NewDecoder(strings.NewReader(st.Data)).Decode()
		if err == nil {
			_, err = s.saveObject(owner, st.Cal, st.Name, obj)
		}
		return err
	case "trash-object":
		c := calendarInfo{Owner: owner, id: st.Cal}
		s.db.QueryRow(`SELECT name, kind FROM calendars WHERE user = ? AND id = ?`, owner, st.Cal).Scan(&c.Name, &c.Kind)
		_, err := s.trashObjectID(u, c, st.Name, "event")
		return err
	case "trash-card":
		_, err := s.trashCardID(owner, st.Cal, st.Name)
		return err
	}
	return errors.New("nothing to undo")
}

// restoreFile puts a trashed file or folder back where it was (beside it, if that name was taken meanwhile).
func (s *Server) restoreFile(user, id string) (string, error) {
	var orig string
	if err := s.db.QueryRow(`SELECT orig FROM trash WHERE id = ? AND user = ?`, id, user).Scan(&orig); err != nil {
		return "", errors.New("it isn't in the trash any more")
	}
	to := s.freeName(user, orig)
	if err := s.data.MkdirAll(s.rel(user, path.Dir(to)), 0o755); err != nil {
		return "", err
	}
	dir := "trash/" + user + "/" + id
	if err := s.data.Rename(dir+"/"+path.Base(orig), s.rel(user, to)); err != nil {
		return "", err
	}
	s.data.Remove(dir)
	s.db.Exec(`DELETE FROM trash WHERE id = ?`, id)
	s.changed("files", s.fileWatchers(user)...)
	return to, nil
}

// restoreItem puts a trashed event, task or contact back.
func (s *Server) restoreItem(user, id string) error {
	var kind, cal, name, data string
	if err := s.db.QueryRow(`SELECT kind, cal, name, data FROM trash_items WHERE id = ? AND user = ?`, id, user).Scan(&kind, &cal, &name, &data); err != nil {
		return errors.New("it isn't in the trash any more")
	}
	var err error
	if kind == "contact" {
		err = s.restoreCard(user, cal, name, data)
	} else {
		err = s.restoreObject(user, kind, cal, name, data)
	}
	if err == nil {
		s.db.Exec(`DELETE FROM trash_items WHERE id = ?`, id)
	}
	return err
}

func (s *Server) objectText(owner, cal, name string) string {
	var ics string
	s.db.QueryRow(`SELECT ics FROM events WHERE user = ? AND cal = ? AND name = ?`, owner, cal, name).Scan(&ics)
	return ics
}

func (s *Server) cardText(owner, book, name string) string {
	var vcf string
	s.db.QueryRow(`SELECT vcf FROM cards WHERE user = ? AND book = ? AND name = ?`, owner, book, name).Scan(&vcf)
	return vcf
}
