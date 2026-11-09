package app

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// Big files upload in chunks, like copyparty: the app starts an upload, sends pieces in order, and finishes it. A
// dropped connection or a reloaded page carries on from what arrived (the same file to the same place resumes the
// same upload). Pieces collect in uploads/<user>/<id>.part, outside the user's folder, for up to a week.

const uploadsSchema = `
CREATE TABLE IF NOT EXISTS uploads (
	id TEXT PRIMARY KEY,
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	owner TEXT NOT NULL, -- whose folder it goes in (someone else's, when it's shared with the uploader)
	path TEXT NOT NULL,
	size INTEGER NOT NULL,
	mod INTEGER NOT NULL,
	created INTEGER NOT NULL
);`

type upload struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Received int64  `json:"received"`
	mod      int64
	owner    string
}

func partName(user, id string) string { return "uploads/" + user + "/" + id + ".part" }

func (s *Server) startUpload(w http.ResponseWriter, r *http.Request, u user, a access) {
	var in struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
		Mod  int64  `json:"mod"` // milliseconds
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := a.path(in.Path)
	if !a.inside(p) || hidden(path.Base(p)) || in.Size < 0 {
		jsonError(w, http.StatusBadRequest, "That isn't a file name Cove can use.")
		return
	}
	if err := s.checkQuota(a.owner, in.Size); err != nil {
		jsonError(w, http.StatusInsufficientStorage, err.Error()+".")
		return
	}
	id := hashHex(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d", u.Name, a.owner, p, in.Size, in.Mod))[:24]
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO uploads (id, user, owner, path, size, mod, created) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, u.Name, a.owner, p, in.Size, in.Mod, time.Now().Unix()); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.data.MkdirAll("uploads/"+u.Name, 0o750); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	up, err := s.upload(u.Name, id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	up.Path = a.shown(strings.TrimPrefix(up.Path, "/"))
	writeJSON(w, http.StatusOK, up)
}

func (s *Server) upload(user, id string) (upload, error) {
	up := upload{ID: id}
	err := s.db.QueryRow(`SELECT owner, path, size, mod FROM uploads WHERE id = ? AND user = ?`, id, user).Scan(&up.owner, &up.Path, &up.Size, &up.mod)
	if err != nil {
		return up, err
	}
	if info, err := s.data.Stat(partName(user, id)); err == nil {
		up.Received = info.Size()
	}
	up.Path = "/" + up.Path
	return up, nil
}

func (s *Server) uploadStatus(w http.ResponseWriter, r *http.Request, u user) {
	up, err := s.upload(u.Name, r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusNotFound, "That upload isn't going on any more.")
		return
	}
	writeJSON(w, http.StatusOK, up)
}

// uploadChunk appends a piece at ?offset=, which must be what has arrived so far (else 409 says how much that is).
func (s *Server) uploadChunk(w http.ResponseWriter, r *http.Request, u user) {
	up, err := s.upload(u.Name, r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusNotFound, "That upload isn't going on any more.")
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset != up.Received {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "That piece doesn't follow what arrived.", "received": up.Received})
		return
	}
	f, err := s.data.OpenFile(partName(u.Name, up.ID), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, up.Size-offset))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// What arrived stays; the app asks how much and carries on.
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "The piece didn't finish arriving.", "received": offset + n})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"received": offset + n})
}

func (s *Server) finishUpload(w http.ResponseWriter, r *http.Request, u user, a access) {
	var in struct {
		Conflict string `json:"conflict"` // "replace", or keep both
	}
	readJSON(r, &in)
	up, err := s.upload(u.Name, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, http.StatusNotFound, "That upload isn't going on any more.")
		return
	} else if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if up.owner != a.owner { // the share it was going into ended
		jsonError(w, http.StatusForbidden, errReadOnly.Error())
		return
	}
	if up.Received != up.Size {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Not all of it has arrived.", "received": up.Received})
		return
	}
	part := partName(u.Name, up.ID)
	if up.Size == 0 {
		if err := s.data.WriteFile(part, nil, 0o640); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	p := cleanRel(up.Path)
	if !a.inside(p) || (a.base != "." && !strings.HasPrefix(p, a.base+"/")) {
		jsonError(w, http.StatusForbidden, errReadOnly.Error())
		return
	}
	if err := s.data.MkdirAll(s.rel(a.owner, path.Dir(p)), 0o755); err != nil {
		fileError(w, err)
		return
	}
	if up.mod > 0 {
		t := time.UnixMilli(up.mod)
		s.data.Chtimes(part, t, t)
	}
	final := p
	if info, err := s.data.Stat(s.rel(a.owner, p)); err == nil {
		if info.IsDir() || in.Conflict != "replace" {
			final = s.freeName(a.owner, p)
		} else {
			s.keepVersion(a.owner, p, false)
		}
	}
	if err := s.data.Rename(part, s.rel(a.owner, final)); err != nil {
		fileError(w, err)
		return
	}
	s.db.Exec(`DELETE FROM uploads WHERE id = ?`, up.ID)
	s.forgetUsage(a.owner)
	s.sharedUpload(u, a, final)
	s.logActivity(a.owner, u, "added “"+path.Base(final)+"”", undoStep{Kind: "trash-file", Path: final})
	s.changed("files", s.fileWatchers(a.owner)...)
	info, err := s.data.Stat(s.rel(a.owner, final))
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"path": a.shown(final), "item": item(info)})
}

func (s *Server) cancelUpload(w http.ResponseWriter, r *http.Request, u user) {
	id := r.PathValue("id")
	s.data.Remove(partName(u.Name, id))
	s.db.Exec(`DELETE FROM uploads WHERE id = ? AND user = ?`, id, u.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// dropOldUploads forgets uploads untouched for a week.
func (s *Server) dropOldUploads(now time.Time) {
	rows, err := s.db.Query(`SELECT id, user FROM uploads`)
	if err != nil {
		return
	}
	var old [][2]string
	for rows.Next() {
		var id, user string
		if rows.Scan(&id, &user) != nil {
			continue
		}
		info, err := s.data.Stat(partName(user, id))
		if (err != nil && errors.Is(err, fs.ErrNotExist)) || (err == nil && now.Sub(info.ModTime()) > 7*24*time.Hour) {
			var created int64
			s.db.QueryRow(`SELECT created FROM uploads WHERE id = ?`, id).Scan(&created)
			if now.Sub(time.Unix(created, 0)) > 7*24*time.Hour {
				old = append(old, [2]string{id, user})
			}
		}
	}
	rows.Close()
	for _, o := range old {
		s.data.Remove(partName(o[1], o[0]))
		s.db.Exec(`DELETE FROM uploads WHERE id = ?`, o[0])
	}
}
