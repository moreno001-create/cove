package app

import (
	"encoding/binary"
	"io"
	"io/fs"
	"math"
	"net/http"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Photos: every picture and video in someone's files on one timeline (by when it was taken, from the camera's EXIF),
// albums (shared albums take photos from everyone at home), and the places they were taken for a map. Phones back up
// their camera roll into Photos/YEAR/MONTH, skipping what's there already.

const photosSchema = `
CREATE TABLE IF NOT EXISTS photo_meta (
	owner TEXT NOT NULL,
	path TEXT NOT NULL,
	mod INTEGER NOT NULL,
	size INTEGER NOT NULL,
	taken INTEGER NOT NULL,
	lat REAL NOT NULL DEFAULT 0,
	lon REAL NOT NULL DEFAULT 0,
	PRIMARY KEY (owner, path)
);
CREATE TABLE IF NOT EXISTS albums (
	id INTEGER PRIMARY KEY,
	owner TEXT NOT NULL,
	name TEXT NOT NULL,
	shared INTEGER NOT NULL DEFAULT 0,
	created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS album_items (
	album INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
	owner TEXT NOT NULL,
	path TEXT NOT NULL,
	added_by TEXT NOT NULL,
	added INTEGER NOT NULL,
	PRIMARY KEY (album, owner, path)
);`

type photoMeta struct {
	Owner string  `json:"owner"`
	Path  string  `json:"path"`
	Name  string  `json:"name"`
	Kind  string  `json:"kind"` // image or video
	Size  int64   `json:"size"`
	Taken int64   `json:"taken"` // unix seconds
	Lat   float64 `json:"lat,omitempty"`
	Lon   float64 `json:"lon,omitempty"`
	// Caption is what the home AI saw in it (for finding photos by what's in them).
	Caption string `json:"caption,omitempty"`
}

// exifInfo reads when a JPEG was taken and where (0, 0 when it doesn't say).
func exifInfo(r io.Reader) (taken time.Time, lat, lon float64) {
	head := make([]byte, 256<<10)
	n, _ := io.ReadFull(r, head)
	head = head[:n]
	if len(head) < 4 || head[0] != 0xFF || head[1] != 0xD8 {
		return
	}
	var t []byte
	for i := 2; i+4 <= len(head); {
		if head[i] != 0xFF {
			return
		}
		marker, size := head[i+1], int(binary.BigEndian.Uint16(head[i+2:]))
		if marker == 0xE1 && i+4+size <= len(head) && size > 8 && string(head[i+4:i+10]) == "Exif\x00\x00" {
			t = head[i+10 : i+2+size]
			break
		}
		if marker == 0xDA {
			return
		}
		i += 2 + size
	}
	if len(t) < 8 {
		return
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return
	}
	type entry struct {
		typ   uint16
		count uint32
		at    int // where the value is (inline or at the offset)
	}
	readIFD := func(off int) map[uint16]entry {
		out := map[uint16]entry{}
		if off <= 0 || off+2 > len(t) {
			return out
		}
		count := int(bo.Uint16(t[off:]))
		for k := 0; k < count && k < 500; k++ {
			e := off + 2 + k*12
			if e+12 > len(t) {
				break
			}
			typ, cnt := bo.Uint16(t[e+2:]), bo.Uint32(t[e+4:])
			width := map[uint16]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 7: 1, 9: 4, 10: 8}[typ] * int(cnt)
			at := e + 8
			if width > 4 {
				at = int(bo.Uint32(t[e+8:]))
			}
			if width >= 0 && at >= 0 && at+width <= len(t) {
				out[bo.Uint16(t[e:])] = entry{typ, cnt, at}
			}
		}
		return out
	}
	long := func(e entry) int { return int(bo.Uint32(t[e.at:])) }
	text := func(e entry) string { return strings.TrimRight(string(t[e.at:e.at+int(e.count)]), "\x00 ") }
	rational := func(e entry, i int) float64 {
		at := e.at + i*8
		if at+8 > len(t) {
			return 0
		}
		den := bo.Uint32(t[at+4:])
		if den == 0 {
			return 0
		}
		return float64(bo.Uint32(t[at:])) / float64(den)
	}
	ifd0 := readIFD(int(bo.Uint32(t[4:])))
	when := ""
	if p, ok := ifd0[0x8769]; ok {
		exif := readIFD(long(p))
		for _, tag := range []uint16{0x9003, 0x9004} {
			if e, ok := exif[tag]; ok && e.typ == 2 {
				when = text(e)
				break
			}
		}
	}
	if when == "" {
		if e, ok := ifd0[0x0132]; ok && e.typ == 2 {
			when = text(e)
		}
	}
	if tm, err := time.ParseInLocation("2006:01:02 15:04:05", when, time.Local); err == nil && tm.Year() > 1900 {
		taken = tm
	}
	if p, ok := ifd0[0x8825]; ok {
		gps := readIFD(long(p))
		la, okLa := gps[2]
		lo, okLo := gps[4]
		if okLa && okLo && la.typ == 5 && lo.typ == 5 && la.count >= 3 && lo.count >= 3 {
			lat = rational(la, 0) + rational(la, 1)/60 + rational(la, 2)/3600
			lon = rational(lo, 0) + rational(lo, 1)/60 + rational(lo, 2)/3600
			if r, ok := gps[1]; ok && t[r.at] == 'S' {
				lat = -lat
			}
			if r, ok := gps[3]; ok && t[r.at] == 'W' {
				lon = -lon
			}
			if math.Abs(lat) > 90 || math.Abs(lon) > 180 || lat == 0 && lon == 0 {
				lat, lon = 0, 0
			}
		}
	}
	return
}

// photosOf is the user's pictures and videos, newest first (what's new or changed is read now).
func (s *Server) photosOf(owner string) []photoMeta {
	known := map[string]photoMeta{}
	type stamp struct{ mod, size int64 }
	stamps := map[string]stamp{}
	if rows, err := s.db.Query(`SELECT path, mod, size, taken, lat, lon FROM photo_meta WHERE owner = ?`, owner); err == nil {
		for rows.Next() {
			var p photoMeta
			var st stamp
			if rows.Scan(&p.Path, &st.mod, &st.size, &p.Taken, &p.Lat, &p.Lon) == nil {
				known[p.Path], stamps[p.Path] = p, st
			}
		}
		rows.Close()
	}
	root := "files/" + owner
	var out []photoMeta
	type fresh struct {
		p   photoMeta
		mod int64
	}
	var news []fresh
	fs.WalkDir(s.data.FS(), root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if hidden(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		kind := kindOf(d.Name(), d.IsDir())
		if kind != "image" && kind != "video" || strings.HasSuffix(strings.ToLower(d.Name()), ".svg") || strings.HasSuffix(strings.ToLower(d.Name()), ".ico") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel := "/" + strings.TrimPrefix(p, root+"/")
		m := photoMeta{Owner: owner, Path: rel, Name: d.Name(), Kind: kind, Size: info.Size()}
		if k, ok := known[rel]; ok && stamps[rel] == (stamp{info.ModTime().UnixNano(), info.Size()}) {
			m.Taken, m.Lat, m.Lon = k.Taken, k.Lat, k.Lon
		} else {
			m.Taken = info.ModTime().Unix()
			if ext := strings.ToLower(path.Ext(d.Name())); ext == ".jpg" || ext == ".jpeg" {
				if f, err := s.data.Open(p); err == nil {
					tm, lat, lon := exifInfo(f)
					f.Close()
					if !tm.IsZero() {
						m.Taken = tm.Unix()
					}
					m.Lat, m.Lon = lat, lon
				}
			}
			news = append(news, fresh{m, info.ModTime().UnixNano()})
		}
		delete(known, rel)
		out = append(out, m)
		return nil
	})
	for _, n := range news {
		s.db.Exec(`INSERT INTO photo_meta (owner, path, mod, size, taken, lat, lon) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(owner, path) DO UPDATE SET mod = excluded.mod, size = excluded.size, taken = excluded.taken, lat = excluded.lat, lon = excluded.lon`,
			owner, n.p.Path, n.mod, n.p.Size, n.p.Taken, n.p.Lat, n.p.Lon)
	}
	for p := range known { // gone
		s.db.Exec(`DELETE FROM photo_meta WHERE owner = ? AND path = ?`, owner, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Taken > out[j].Taken || out[i].Taken == out[j].Taken && out[i].Path < out[j].Path
	})
	return out
}

func (s *Server) listPhotos(w http.ResponseWriter, r *http.Request, u user) {
	list := s.photosOf(u.Name)
	if list == nil {
		list = []photoMeta{}
	}
	caps := s.photoCaptions(u.Name)
	for i := range list {
		list[i].Caption = caps[list[i].Path]
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		hits := s.searchPhotos(u.Name, q)
		list = slices.DeleteFunc(list, func(p photoMeta) bool { return !hits[p.Path] })
	}
	writeJSON(w, http.StatusOK, list)
}

// photosHave says which of a phone's pictures are backed up already (by name and size, anywhere in the files).
func (s *Server) photosHave(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Files []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"files"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	have := map[string]bool{}
	for _, p := range s.photosOf(u.Name) {
		have[strings.ToLower(p.Name)+"|"+strconv.FormatInt(p.Size, 10)] = true
	}
	out := make([]bool, len(in.Files))
	for i, f := range in.Files {
		out[i] = have[strings.ToLower(f.Name)+"|"+strconv.FormatInt(f.Size, 10)]
	}
	writeJSON(w, http.StatusOK, out)
}

// --- albums

type album struct {
	ID     int64      `json:"id"`
	Owner  string     `json:"owner"`
	Who    string     `json:"who"`
	Name   string     `json:"name"`
	Shared bool       `json:"shared"`
	Count  int        `json:"count"`
	Cover  *albumItem `json:"cover"`
	Mine   bool       `json:"mine"`
}

type albumItem struct {
	Owner   string `json:"owner"`
	Path    string `json:"path"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	AddedBy string `json:"addedBy"`
	Taken   int64  `json:"taken"`
}

func (s *Server) albumsFor(u user) []album {
	rows, err := s.db.Query(`SELECT id, owner, name, shared FROM albums WHERE owner = ? OR (shared = 1 AND household = ? AND household != 0) ORDER BY created DESC`, u.Name, u.Home)
	if err != nil {
		return nil
	}
	out := []album{}
	for rows.Next() {
		var a album
		if rows.Scan(&a.ID, &a.Owner, &a.Name, &a.Shared) == nil {
			a.Mine = a.Owner == u.Name || u.Admin
			out = append(out, a)
		}
	}
	rows.Close()
	for i := range out {
		a := &out[i]
		a.Who = s.displayOf(a.Owner)
		s.db.QueryRow(`SELECT count(*) FROM album_items WHERE album = ?`, a.ID).Scan(&a.Count)
		var it albumItem
		if s.db.QueryRow(`SELECT owner, path FROM album_items WHERE album = ? ORDER BY added DESC LIMIT 1`, a.ID).Scan(&it.Owner, &it.Path) == nil {
			it.Name, it.Kind = path.Base(it.Path), kindOf(it.Path, false)
			a.Cover = &it
		}
	}
	return out
}

func (s *Server) albumFor(u user, id string) (album, bool) {
	for _, a := range s.albumsFor(u) {
		if strconv.FormatInt(a.ID, 10) == id {
			return a, true
		}
	}
	return album{}, false
}

func (s *Server) listAlbums(w http.ResponseWriter, r *http.Request, u user) {
	writeJSON(w, http.StatusOK, s.albumsFor(u))
}

func (s *Server) saveAlbum(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Name   string `json:"name"`
		Shared bool   `json:"shared"`
	}
	if err := readJSON(r, &in); err != nil || clip(in.Name, 200) == "" {
		jsonError(w, http.StatusBadRequest, "Give the album a name.")
		return
	}
	if id := r.PathValue("id"); id != "" {
		a, ok := s.albumFor(u, id)
		if !ok || !a.Mine {
			jsonError(w, http.StatusForbidden, "Only whoever made the album changes it.")
			return
		}
		s.db.Exec(`UPDATE albums SET name = ?, shared = ? WHERE id = ?`, clip(in.Name, 200), in.Shared, a.ID)
		s.changed("photos", s.members(u.Home)...)
		writeJSON(w, http.StatusOK, map[string]int64{"id": a.ID})
		return
	}
	res, err := s.db.Exec(`INSERT INTO albums (household, owner, name, shared, created) VALUES (?, ?, ?, ?, ?)`, u.Home, u.Name, clip(in.Name, 200), in.Shared, time.Now().Unix())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	if in.Shared {
		for _, o := range s.othersIn(u.Home, u.Name) {
			s.notify(o, displayName(u)+" started a shared album: "+clip(in.Name, 200), "#/photos")
		}
	}
	s.changed("photos", s.members(u.Home)...)
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) deleteAlbum(w http.ResponseWriter, r *http.Request, u user) {
	a, ok := s.albumFor(u, r.PathValue("id"))
	if !ok || !a.Mine {
		jsonError(w, http.StatusForbidden, "Only whoever made the album removes it (the photos stay in their files).")
		return
	}
	s.db.Exec(`DELETE FROM album_items WHERE album = ?`, a.ID)
	s.db.Exec(`DELETE FROM albums WHERE id = ?`, a.ID)
	s.changed("photos", s.members(u.Home)...)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) albumItems(w http.ResponseWriter, r *http.Request, u user) {
	a, ok := s.albumFor(u, r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "There's no such album.")
		return
	}
	rows, err := s.db.Query(`SELECT i.owner, i.path, i.added_by, coalesce(m.taken, i.added) FROM album_items i
		LEFT JOIN photo_meta m ON m.owner = i.owner AND m.path = i.path WHERE i.album = ? ORDER BY 4 DESC`, a.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := []albumItem{}
	for rows.Next() {
		var it albumItem
		if rows.Scan(&it.Owner, &it.Path, &it.AddedBy, &it.Taken) == nil {
			it.Name, it.Kind = path.Base(it.Path), kindOf(it.Path, false)
			items = append(items, it)
		}
	}
	rows.Close()
	writeJSON(w, http.StatusOK, map[string]any{"album": a, "items": items})
}

// addToAlbum puts some of u's own photos in an album (theirs, or a shared one).
func (s *Server) addToAlbum(w http.ResponseWriter, r *http.Request, u user) {
	a, ok := s.albumFor(u, r.PathValue("id"))
	if !ok || !a.Shared && a.Owner != u.Name {
		jsonError(w, http.StatusNotFound, "There's no such album.")
		return
	}
	var in struct {
		Paths []string `json:"paths"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	added := 0
	for _, p := range in.Paths {
		p = "/" + cleanRel(p)
		if k := kindOf(p, false); k != "image" && k != "video" {
			continue
		}
		if _, err := s.data.Stat(s.rel(u.Name, p)); err != nil {
			continue
		}
		if res, err := s.db.Exec(`INSERT OR IGNORE INTO album_items (album, owner, path, added_by, added) VALUES (?, ?, ?, ?, ?)`, a.ID, u.Name, p, u.Name, time.Now().Unix()); err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				added++
			}
		}
	}
	if added > 0 && a.Shared && a.Owner != u.Name {
		s.notify(a.Owner, displayName(u)+" added photos to "+a.Name, "#/photos")
	}
	s.changed("photos", s.members(u.Home)...)
	writeJSON(w, http.StatusOK, map[string]int{"added": added})
}

func (s *Server) removeFromAlbum(w http.ResponseWriter, r *http.Request, u user) {
	a, ok := s.albumFor(u, r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "There's no such album.")
		return
	}
	owner, p := r.URL.Query().Get("owner"), r.URL.Query().Get("path")
	if !a.Mine && owner != u.Name {
		jsonError(w, http.StatusForbidden, "Only your own photos (or any, in your album).")
		return
	}
	s.db.Exec(`DELETE FROM album_items WHERE album = ? AND owner = ? AND path = ?`, a.ID, owner, p)
	s.changed("photos", s.members(u.Home)...)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// albumPhoto shows a photo in an album, whoever's files it is in (?w= for a smaller one).
func (s *Server) albumPhoto(w http.ResponseWriter, r *http.Request, u user) {
	a, ok := s.albumFor(u, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	owner, p := r.URL.Query().Get("owner"), r.URL.Query().Get("path")
	var n int
	if s.db.QueryRow(`SELECT count(*) FROM album_items WHERE album = ? AND owner = ? AND path = ?`, a.ID, owner, p).Scan(&n); n == 0 {
		http.NotFound(w, r)
		return
	}
	name := s.rel(owner, p)
	if r.URL.Query().Get("w") != "" {
		s.serveThumb(w, r, name)
		return
	}
	s.serveFile(w, r, name, false)
}
