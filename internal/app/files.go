package app

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Paths in the API are inside the user's own folder, "/" being its top. cleanRel turns one into a path for os.Root
// ("." for the top); it can't climb out, since "/.." cleans to "/".
func cleanRel(p string) string {
	c := path.Clean("/" + p)
	if c == "/" {
		return "."
	}
	return c[1:]
}

// rel is where a user's path is in the data folder.
func (s *Server) rel(user, p string) string {
	if r := cleanRel(p); r != "." {
		return "files/" + user + "/" + r
	}
	return "files/" + user
}

// Cove's own unfinished uploads are hidden.
func hidden(name string) bool { return strings.HasPrefix(name, ".cove-") }

type fileItem struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
	Mod  int64  `json:"mod"` // milliseconds
	Kind string `json:"kind"`
}

func item(info fs.FileInfo) fileItem {
	it := fileItem{Name: info.Name(), Dir: info.IsDir(), Mod: info.ModTime().UnixMilli(), Kind: kindOf(info.Name(), info.IsDir())}
	if !it.Dir {
		it.Size = info.Size()
	}
	return it
}

var kinds = map[string]string{}

func init() {
	for kind, exts := range map[string]string{
		"image":   "jpg jpeg png gif webp avif bmp svg heic heif tif tiff ico",
		"video":   "mp4 m4v mov webm mkv avi wmv mpg mpeg",
		"audio":   "mp3 m4a aac flac wav ogg opus wma",
		"pdf":     "pdf",
		"doc":     "doc docx odt rtf pages",
		"sheet":   "xls xlsx ods csv numbers",
		"slides":  "ppt pptx odp key",
		"archive": "zip rar 7z tar gz tgz bz2 xz",
		"text":    "txt md markdown log ini conf cfg",
		"code":    "go js ts jsx tsx py rb rs c h cpp java kt swift sh json yaml yml toml xml html css sql",
	} {
		for _, e := range strings.Fields(exts) {
			kinds[e] = kind
		}
	}
}

func kindOf(name string, dir bool) string {
	if dir {
		return "folder"
	}
	if k, ok := kinds[strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))]; ok {
		return k
	}
	return "file"
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request, u user, a access) {
	p := a.path(r.URL.Query().Get("path"))
	f, err := s.data.Open(s.rel(a.owner, p))
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "That isn't a folder.")
		return
	}
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
	out := map[string]any{"path": a.shown(p), "items": items, "edit": a.edit}
	if a.share == 0 {
		out["sharing"] = s.sharedIn(u.Name, p)
	}
	writeJSON(w, http.StatusOK, out)
}

func sortItems(items []fileItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Dir != items[j].Dir {
			return items[i].Dir
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
}

func fileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		jsonError(w, http.StatusNotFound, "It isn't there any more.")
	case errors.Is(err, fs.ErrExist):
		jsonError(w, http.StatusConflict, "Something with that name is already there.")
	default:
		jsonError(w, http.StatusBadRequest, err.Error())
	}
}

func (s *Server) fileContent(w http.ResponseWriter, r *http.Request, u user, a access) {
	s.serveFile(w, r, s.rel(a.owner, a.path(r.URL.Query().Get("path"))), r.URL.Query().Get("download") == "1")
}

// Shown in the browser; everything else downloads. Pages, scripts and SVG could run on Cove's address with the
// viewer's sign-in, so they're shown as text, downloaded, or sandboxed.
var inlineTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"image/avif": true, "image/bmp": true, "image/svg+xml": true, "application/pdf": true}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name string, download bool) {
	f, err := s.data.Open(name)
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		jsonError(w, http.StatusBadRequest, "That's a folder.")
		return
	}
	base := info.Name()
	ext := strings.ToLower(path.Ext(base))
	ctype := mime.TypeByExtension(ext)
	if i := strings.IndexByte(ctype, ';'); i >= 0 {
		ctype = ctype[:i]
	}
	inline := !download
	switch k := kindOf(base, false); {
	case inlineTypes[ctype], strings.HasPrefix(ctype, "video/"), strings.HasPrefix(ctype, "audio/"):
	case k == "text" || k == "code" || ext == ".csv":
		ctype = "text/plain; charset=utf-8"
	default:
		inline = false
	}
	if ctype == "" || !inline {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	disposition := "inline"
	if !inline {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", disposition+"; filename*=UTF-8''"+url.PathEscape(base))
	if ctype != "application/pdf" { // browsers won't show PDFs in a sandbox
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'")
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// uploadFile takes the file as the request body (no form encoding, so any size streams to disk). It's written next to
// its place and renamed into it when complete. ?conflict=replace replaces a file already there; otherwise the new one
// gets a free name ("photo (2).jpg").
func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request, u user, a access) {
	q := r.URL.Query()
	p := a.path(q.Get("path"))
	if a.file && q.Get("conflict") == "replace" {
		// saving the shared file itself (an edited text file)
	} else if !a.inside(p) || hidden(path.Base(p)) {
		jsonError(w, http.StatusBadRequest, "That isn't a file name Cove can use.")
		return
	}
	var mod time.Time
	if ms, err := strconv.ParseInt(r.Header.Get("X-Mod-Time"), 10, 64); err == nil && ms > 0 {
		mod = time.UnixMilli(ms)
	}
	_, existed := s.data.Stat(s.rel(a.owner, p))
	final, err := s.writeFile(a.owner, p, r.Body, q.Get("conflict") == "replace", mod)
	if err != nil {
		fileError(w, err)
		return
	}
	if existed != nil || final != p {
		s.logActivity(a.owner, u, "added “"+path.Base(final)+"”", undoStep{Kind: "trash-file", Path: final})
	} else {
		s.logActivity(a.owner, u, "changed “"+path.Base(final)+"”", undoStep{})
	}
	info, err := s.data.Stat(s.rel(a.owner, final))
	if err != nil {
		fileError(w, err)
		return
	}
	if kindOf(final, false) == "text" || kindOf(final, false) == "code" {
		s.touchRecent(u, a, final)
	}
	s.sharedUpload(u, a, final)
	writeJSON(w, http.StatusCreated, map[string]any{"path": a.shown(final), "item": item(info)})
}

// writeFile saves body at p (a cleaned path in the user's folder), making missing folders, and returns where it went.
func (s *Server) writeFile(user, p string, body io.Reader, replace bool, mod time.Time) (string, error) {
	dir := path.Dir(p)
	if err := s.data.MkdirAll(s.rel(user, dir), 0o755); err != nil {
		return "", err
	}
	tmp := s.rel(user, path.Join(dir, ".cove-upload-"+randomToken(9)))
	f, err := s.data.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		s.data.Remove(tmp)
		return "", fmt.Errorf("the upload didn't finish: %w", err)
	}
	if err := s.checkQuota(user, n); err != nil {
		s.data.Remove(tmp)
		s.forgetUsage(user)
		return "", err
	}
	defer s.forgetUsage(user)
	if !mod.IsZero() {
		s.data.Chtimes(tmp, mod, mod)
	}
	final := p
	if info, err := s.data.Stat(s.rel(user, p)); err == nil {
		if info.IsDir() || !replace {
			final = s.freeName(user, p)
		} else {
			s.keepVersion(user, p, false)
		}
	}
	if err := s.data.Rename(tmp, s.rel(user, final)); err != nil {
		s.data.Remove(tmp)
		return "", err
	}
	s.changed("files", s.fileWatchers(user)...)
	s.readSoon(user)
	return final, nil
}

// freeName is p, or "name (2).ext", "name (3).ext"… whichever isn't taken.
func (s *Server) freeName(user, p string) string {
	ext := path.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	if ext == p || strings.HasSuffix(stem, "/") { // ".bashrc": the dot isn't an extension
		stem, ext = p, ""
	}
	for i := 1; ; i++ {
		c := p
		if i > 1 {
			c = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		if _, err := s.data.Lstat(s.rel(user, c)); errors.Is(err, fs.ErrNotExist) {
			return c
		}
	}
}

func (s *Server) makeFolder(w http.ResponseWriter, r *http.Request, u user, a access) {
	var in struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := a.path(in.Path)
	if !a.inside(p) || hidden(path.Base(p)) {
		jsonError(w, http.StatusBadRequest, "That isn't a folder name Cove can use.")
		return
	}
	if err := s.data.Mkdir(s.rel(a.owner, p), 0o755); err != nil {
		fileError(w, err)
		return
	}
	s.logActivity(a.owner, u, "made the folder “"+path.Base(p)+"”", undoStep{Kind: "trash-file", Path: p})
	s.changed("files", s.fileWatchers(a.owner)...)
	writeJSON(w, http.StatusCreated, map[string]string{"path": a.shown(p)})
}

func (s *Server) moveFile(w http.ResponseWriter, r *http.Request, u user, a access) {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	from, to := a.path(in.From), a.path(in.To)
	if !a.inside(from) || !a.inside(to) {
		jsonError(w, http.StatusForbidden, "What was shared with you can't be moved or renamed (its owner can).")
		return
	}
	if err := s.move(a.owner, from, to); err != nil {
		fileError(w, err)
		return
	}
	verb := "moved “" + path.Base(from) + "” to “" + path.Dir(to) + "”"
	if path.Dir(from) == path.Dir(to) {
		verb = "renamed “" + path.Base(from) + "” to “" + path.Base(to) + "”"
	}
	s.logActivity(a.owner, u, verb, undoStep{Kind: "move-file", Path: from, To: to})
	writeJSON(w, http.StatusOK, map[string]string{"path": a.shown(to)})
}

// move renames or moves within the user's folder (never over something) and keeps share links pointing at it.
func (s *Server) move(user, from, to string) error {
	switch {
	case from == "." || to == ".":
		return errors.New("the top folder can't be moved")
	case to == from:
		return nil
	case strings.HasPrefix(to+"/", from+"/"):
		return errors.New("a folder can't go inside itself")
	case hidden(path.Base(to)):
		return errors.New("that isn't a name Cove can use")
	}
	if _, err := s.data.Lstat(s.rel(user, to)); err == nil && !strings.EqualFold(from, to) {
		return fs.ErrExist
	}
	if err := s.data.Rename(s.rel(user, from), s.rel(user, to)); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE shares SET path = ? || substr(path, ?) WHERE user = ? AND (path = ? OR substr(path, 1, ?) = ?)`,
		to, len(from)+1, user, from, len(from)+1, from+"/")
	s.followPath(user, from, to)
	s.changed("files", s.fileWatchers(user)...)
	return err
}

func (s *Server) deleteFiles(w http.ResponseWriter, r *http.Request, u user, a access) {
	var in struct {
		Paths []string `json:"paths"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, p := range in.Paths {
		if !a.inside(a.path(p)) {
			jsonError(w, http.StatusForbidden, "What was shared with you can't be deleted (its owner can).")
			return
		}
		tid, err := s.trashID(a.owner, a.path(p))
		if err != nil {
			fileError(w, err)
			return
		}
		s.logActivity(a.owner, u, "deleted “"+path.Base(a.path(p))+"”", undoStep{Kind: "restore-file", TrashID: tid})
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": len(in.Paths)})
}

// trash moves p to the user's trash (kept 30 days) and ends its share links.
func (s *Server) trash(user, p string) error {
	_, err := s.trashID(user, p)
	return err
}

// trashID is trash, telling which trash item it became.
func (s *Server) trashID(user, p string) (string, error) {
	if p == "." {
		return "", errors.New("the top folder can't be deleted")
	}
	info, err := s.data.Lstat(s.rel(user, p))
	if err != nil {
		return "", err
	}
	id := randomToken(9)
	dir := "trash/" + user + "/" + id
	if err := s.data.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	if err := s.data.Rename(s.rel(user, p), dir+"/"+path.Base(p)); err != nil {
		s.data.Remove(dir)
		return "", err
	}
	size := info.Size()
	if info.IsDir() {
		size = s.folderSize(dir + "/" + path.Base(p))
	}
	s.db.Exec(`DELETE FROM shares WHERE user = ? AND (path = ? OR substr(path, 1, ?) = ?)`, user, p, len(p)+1, p+"/")
	s.followPath(user, p, "")
	_, err = s.db.Exec(`INSERT INTO trash (id, user, orig, deleted, dir, size) VALUES (?, ?, ?, ?, ?, ?)`,
		id, user, p, time.Now().Unix(), info.IsDir(), size)
	s.changed("files", s.fileWatchers(user)...)
	return id, err
}

func (s *Server) folderSize(name string) int64 {
	var total int64
	fs.WalkDir(s.data.FS(), name, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

type trashItem struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Name    string `json:"name"`
	Dir     bool   `json:"dir"`
	Size    int64  `json:"size"`
	Deleted int64  `json:"deleted"`
	Kind    string `json:"kind"`
}

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT id, orig, dir, size, deleted FROM trash WHERE user = ? ORDER BY deleted DESC`, u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []trashItem{}
	for rows.Next() {
		var t trashItem
		if rows.Scan(&t.ID, &t.Path, &t.Dir, &t.Size, &t.Deleted) == nil {
			t.Name = path.Base(t.Path)
			t.Kind = kindOf(t.Name, t.Dir)
			t.Path = "/" + t.Path
			list = append(list, t)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) restoreTrash(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	var restored []string
	for _, id := range in.IDs {
		var orig string
		if err := s.db.QueryRow(`SELECT orig FROM trash WHERE id = ? AND user = ?`, id, u.Name).Scan(&orig); err != nil {
			jsonError(w, http.StatusNotFound, "That isn't in the trash any more.")
			return
		}
		to := s.freeName(u.Name, orig)
		if err := s.data.MkdirAll(s.rel(u.Name, path.Dir(to)), 0o755); err != nil {
			fileError(w, err)
			return
		}
		dir := "trash/" + u.Name + "/" + id
		if err := s.data.Rename(dir+"/"+path.Base(orig), s.rel(u.Name, to)); err != nil {
			fileError(w, err)
			return
		}
		s.data.Remove(dir)
		s.db.Exec(`DELETE FROM trash WHERE id = ?`, id)
		restored = append(restored, "/"+to)
	}
	writeJSON(w, http.StatusOK, map[string]any{"restored": restored})
}

// deleteTrash deletes for good: the listed items, or everything with "all".
func (s *Server) deleteTrash(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.All {
		rows, err := s.db.Query(`SELECT id FROM trash WHERE user = ?`, u.Name)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		in.IDs = nil
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				in.IDs = append(in.IDs, id)
			}
		}
		rows.Close()
	}
	for _, id := range in.IDs {
		if err := s.purgeTrash(u.Name, id); err != nil {
			fileError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": len(in.IDs)})
}

func (s *Server) purgeTrash(user, id string) error {
	if strings.ContainsAny(id, "/\\.") || id == "" {
		return errors.New("not a trash item")
	}
	if err := s.data.RemoveAll("trash/" + user + "/" + id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM trash WHERE id = ? AND user = ?`, id, user)
	return err
}

func (s *Server) zipFolder(w http.ResponseWriter, r *http.Request, u user, a access) {
	p := a.path(r.URL.Query().Get("path"))
	s.serveZip(w, s.rel(a.owner, p), zipName(p, a.owner))
}

func zipName(p, fallback string) string {
	if p == "." {
		return fallback + ".zip"
	}
	return path.Base(p) + ".zip"
}

// serveZip streams a folder as a zip, without making it first.
func (s *Server) serveZip(w http.ResponseWriter, name, filename string) {
	info, err := s.data.Stat(name)
	if err != nil {
		fileError(w, err)
		return
	}
	if !info.IsDir() {
		jsonError(w, http.StatusBadRequest, "That isn't a folder.")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(filename))
	zw := zip.NewWriter(w)
	defer zw.Close()
	fsys := s.data.FS()
	fs.WalkDir(fsys, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == name || hidden(d.Name()) {
			return nil
		}
		relName := strings.TrimPrefix(p, name+"/")
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hdr, _ := zip.FileInfoHeader(info)
		hdr.Name = relName
		if d.IsDir() {
			hdr.Name += "/"
			_, err := zw.CreateHeader(hdr)
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		hdr.Method = zip.Deflate
		out, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := fsys.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		_, err = io.Copy(out, f)
		return err
	})
}

// sharedUpload tells the owner of a shared folder that someone added to it (at most every 10 minutes per share).
func (s *Server) sharedUpload(u user, a access, p string) {
	if a.share != 0 && s.once(fmt.Sprintf("upload|%d", a.share), 10*time.Minute) {
		s.notify(a.owner, fmt.Sprintf("%s added “%s” to “%s”", displayName(u), path.Base(p), path.Base(a.base)), fmt.Sprintf("#/files/%s", url.PathEscape(path.Dir(p))))
	}
}
