package app

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
)

// More for files: unpacking a zip where it is, a folder's size, and finding duplicate files.

const hashesSchema = `
CREATE TABLE IF NOT EXISTS file_hashes (
	owner TEXT NOT NULL,
	path TEXT NOT NULL,
	size INTEGER NOT NULL,
	mod INTEGER NOT NULL,
	sha TEXT NOT NULL,
	PRIMARY KEY (owner, path)
);`

// unzip unpacks a zip into a folder beside it (named like it). Paths in the zip can't climb out of that folder.
func (s *Server) unzip(w http.ResponseWriter, r *http.Request, u user, a access) {
	var in struct {
		Path string `json:"path"`
	}
	readJSON(r, &in)
	p := a.path(in.Path)
	f, err := s.data.Open(s.rel(a.owner, p))
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		jsonError(w, http.StatusBadRequest, "That isn't a zip file Cove can open.")
		return
	}
	var total uint64
	for _, zf := range zr.File {
		total += zf.UncompressedSize64
	}
	if len(zr.File) > 50000 || total > 20<<30 {
		jsonError(w, http.StatusBadRequest, "That zip is too big to unpack here (over 50,000 files or 20 GB).")
		return
	}
	if err := s.checkQuota(a.owner, int64(total)); err != nil {
		jsonError(w, http.StatusInsufficientStorage, err.Error()+".")
		return
	}
	dest := s.freeName(a.owner, strings.TrimSuffix(p, path.Ext(p)))
	if err := s.data.MkdirAll(s.rel(a.owner, dest), 0o755); err != nil {
		fileError(w, err)
		return
	}
	n := 0
	for _, zf := range zr.File {
		name := cleanRel(zf.Name)
		if name == "." || strings.HasPrefix(path.Base(name), "__MACOSX") || strings.Contains(zf.Name, "__MACOSX/") || hidden(path.Base(name)) {
			continue
		}
		target := dest + "/" + name
		if zf.FileInfo().IsDir() {
			s.data.MkdirAll(s.rel(a.owner, target), 0o755)
			continue
		}
		if !zf.Mode().IsRegular() { // no links
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		_, err = s.writeFile(a.owner, target, io.LimitReader(rc, int64(zf.UncompressedSize64)+1), false, zf.Modified)
		rc.Close()
		if err != nil {
			jsonError(w, http.StatusBadRequest, "Unpacking stopped: "+err.Error())
			return
		}
		n++
	}
	s.logActivity(a.owner, u, "unpacked “"+path.Base(p)+"”", undoStep{Kind: "trash-file", Path: dest})
	writeJSON(w, http.StatusOK, map[string]any{"path": a.shown(dest), "files": n})
}

func (s *Server) folderInfo(w http.ResponseWriter, r *http.Request, u user, a access) {
	p := s.rel(a.owner, a.path(r.URL.Query().Get("path")))
	var size int64
	files, folders := 0, 0
	deadline := time.Now().Add(10 * time.Second)
	fs.WalkDir(s.data.FS(), p, func(q string, d fs.DirEntry, err error) error {
		if err != nil || hidden(d.Name()) || time.Now().After(deadline) {
			return nil
		}
		if d.IsDir() {
			if q != p {
				folders++
			}
			return nil
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
			files++
		}
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]any{"size": size, "files": files, "folders": folders})
}

// duplicates finds files with the same contents in the user's folder (biggest first). Hashes are kept until a file
// changes.
func (s *Server) duplicates(w http.ResponseWriter, r *http.Request, u user) {
	root := "files/" + u.Name
	bySize := map[int64][]string{}
	mods := map[string]int64{}
	fs.WalkDir(s.data.FS(), root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || hidden(d.Name()) {
			if d != nil && d.IsDir() && p != root {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil && info.Size() > 0 {
				rel := strings.TrimPrefix(p, root+"/")
				bySize[info.Size()] = append(bySize[info.Size()], rel)
				mods[rel] = info.ModTime().UnixNano()
			}
		}
		return nil
	})
	type group struct {
		Size  int64     `json:"size"`
		Files []fileHit `json:"files"`
	}
	var groups []group
	deadline := time.Now().Add(60 * time.Second)
	for size, paths := range bySize {
		if len(paths) < 2 || time.Now().After(deadline) {
			continue
		}
		bySha := map[string][]string{}
		for _, p := range paths {
			if sha := s.hashOf(u.Name, p, size, mods[p]); sha != "" {
				bySha[sha] = append(bySha[sha], p)
			}
		}
		for _, same := range bySha {
			if len(same) < 2 {
				continue
			}
			g := group{Size: size}
			sort.Strings(same)
			for _, p := range same {
				if info, err := s.data.Stat(s.rel(u.Name, p)); err == nil {
					g.Files = append(g.Files, fileHit{fileItem: item(info), Path: "/" + p})
				}
			}
			groups = append(groups, g)
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Size*int64(len(groups[i].Files)) > groups[j].Size*int64(len(groups[j].Files))
	})
	if groups == nil {
		groups = []group{}
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) hashOf(owner, p string, size, mod int64) string {
	var sha string
	if s.db.QueryRow(`SELECT sha FROM file_hashes WHERE owner = ? AND path = ? AND size = ? AND mod = ?`, owner, p, size, mod).Scan(&sha) == nil {
		return sha
	}
	f, err := s.data.Open(s.rel(owner, p))
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	sha = hex.EncodeToString(h.Sum(nil))
	s.db.Exec(`INSERT INTO file_hashes (owner, path, size, mod, sha) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(owner, path) DO UPDATE SET size = excluded.size, mod = excluded.mod, sha = excluded.sha`, owner, p, size, mod, sha)
	return sha
}
