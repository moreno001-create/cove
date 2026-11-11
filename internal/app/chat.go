package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Family chat: a Family room with everyone, and rooms for some of the household ("Parents", "Trip to Maui").
// Messages are text or a photo; whoever is in a room hears about new ones (at most every few minutes per room).

const chatSchema = `
CREATE TABLE IF NOT EXISTS chat_rooms (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	members TEXT NOT NULL DEFAULT '', -- JSON list of names; '' is everyone
	created_by TEXT NOT NULL,
	created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS chat_messages (
	id INTEGER PRIMARY KEY,
	room INTEGER NOT NULL REFERENCES chat_rooms(id) ON DELETE CASCADE,
	author TEXT NOT NULL,
	text TEXT NOT NULL,
	photo TEXT NOT NULL DEFAULT '',
	created INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS chat_messages_room ON chat_messages(room, id);
CREATE TABLE IF NOT EXISTS chat_read (
	user TEXT NOT NULL,
	room INTEGER NOT NULL,
	last INTEGER NOT NULL,
	PRIMARY KEY (user, room)
);`

type chatRoom struct {
	ID        int64    `json:"id"`
	Household int64    `json:"household"`
	Name      string   `json:"name"`
	Members   []string `json:"members"` // empty: everyone in the household
	CreatedBy string   `json:"createdBy"`
	Unread    int      `json:"unread"`
	Last      string   `json:"last"`
	LastWho   string   `json:"lastWho"`
	LastAt    int64    `json:"lastAt"`
}

func (r chatRoom) has(name string) bool {
	return len(r.Members) == 0 || slices.Contains(r.Members, name)
}

func (s *Server) chatRooms(u user) []chatRoom {
	if u.Home == 0 {
		return []chatRoom{}
	}
	var n int
	if s.db.QueryRow(`SELECT count(*) FROM chat_rooms WHERE household = ? AND members = ''`, u.Home).Scan(&n); n == 0 { // each household's own Family room
		s.db.Exec(`INSERT INTO chat_rooms (household, name, created_by, created) VALUES (?, 'Family', '', ?)`, u.Home, time.Now().Unix())
	}
	rows, err := s.db.Query(`SELECT id, household, name, members, created_by FROM chat_rooms WHERE household = ? ORDER BY id`, u.Home)
	if err != nil {
		return nil
	}
	var all []chatRoom
	for rows.Next() {
		var r chatRoom
		var members string
		if rows.Scan(&r.ID, &r.Household, &r.Name, &members, &r.CreatedBy) == nil {
			json.Unmarshal([]byte(members), &r.Members)
			if r.Members == nil {
				r.Members = []string{}
			}
			if r.has(u.Name) {
				all = append(all, r)
			}
		}
	}
	rows.Close()
	for i := range all {
		r := &all[i]
		var last int64
		s.db.QueryRow(`SELECT last FROM chat_read WHERE user = ? AND room = ?`, u.Name, r.ID).Scan(&last)
		s.db.QueryRow(`SELECT count(*) FROM chat_messages WHERE room = ? AND id > ? AND author != ?`, r.ID, last, u.Name).Scan(&r.Unread)
		var photo string
		if s.db.QueryRow(`SELECT text, photo, author, created FROM chat_messages WHERE room = ? ORDER BY id DESC LIMIT 1`, r.ID).Scan(&r.Last, &photo, &r.LastWho, &r.LastAt) == nil {
			if r.Last == "" && photo != "" {
				r.Last = "📷 Photo"
			}
			r.LastWho = s.displayOf(r.LastWho)
		}
	}
	return all
}

func (s *Server) chatRoom(u user, id string) (chatRoom, bool) {
	for _, r := range s.chatRooms(u) {
		if strconv.FormatInt(r.ID, 10) == id {
			return r, true
		}
	}
	return chatRoom{}, false
}

func (s *Server) listChat(w http.ResponseWriter, r *http.Request, u user) {
	writeJSON(w, http.StatusOK, s.chatRooms(u))
}

func (s *Server) roomMembers(r chatRoom) []string {
	if len(r.Members) == 0 {
		return s.members(r.Household)
	}
	return r.Members
}

func (s *Server) createRoom(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if err := readJSON(r, &in); err != nil || clip(in.Name, 100) == "" {
		jsonError(w, http.StatusBadRequest, "Give the room a name.")
		return
	}
	if !needHome(w, u) {
		return
	}
	members := slices.DeleteFunc(trimList(in.Members, 50), func(n string) bool { return !s.inHousehold(u.Home, n) })
	if len(members) > 0 && !slices.Contains(members, u.Name) {
		members = append(members, u.Name)
	}
	m, _ := json.Marshal(members)
	if len(members) == 0 {
		m = []byte("")
	}
	res, err := s.db.Exec(`INSERT INTO chat_rooms (household, name, members, created_by, created) VALUES (?, ?, ?, ?, ?)`, u.Home, clip(in.Name, 100), string(m), u.Name, time.Now().Unix())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	room := chatRoom{ID: id, Household: u.Home, Name: clip(in.Name, 100), Members: members}
	s.changed("chat", s.roomMembers(room)...)
	writeJSON(w, http.StatusCreated, room)
}

func (s *Server) deleteRoom(w http.ResponseWriter, r *http.Request, u user) {
	room, ok := s.chatRoom(u, r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "There's no such room.")
		return
	}
	if room.CreatedBy == "" || room.CreatedBy != u.Name && !u.Admin {
		jsonError(w, http.StatusForbidden, "Only whoever made the room can remove it (and the Family room stays).")
		return
	}
	var photos []string
	if rows, err := s.db.Query(`SELECT photo FROM chat_messages WHERE room = ? AND photo != ''`, room.ID); err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				photos = append(photos, p)
			}
		}
		rows.Close()
	}
	for _, p := range photos {
		s.data.Remove(p)
	}
	s.db.Exec(`DELETE FROM chat_messages WHERE room = ?`, room.ID)
	s.db.Exec(`DELETE FROM chat_read WHERE room = ?`, room.ID)
	s.db.Exec(`DELETE FROM chat_rooms WHERE id = ?`, room.ID)
	s.changed("chat", s.roomMembers(room)...)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type chatMessage struct {
	ID      int64  `json:"id"`
	Author  string `json:"author"`
	Who     string `json:"who"`
	Text    string `json:"text"`
	Photo   bool   `json:"photo"`
	Created int64  `json:"created"`
	Mine    bool   `json:"mine"`
}

func (s *Server) chatMessages(w http.ResponseWriter, r *http.Request, u user) {
	room, ok := s.chatRoom(u, r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "There's no such room.")
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if before <= 0 {
		before = 1 << 62
	}
	rows, err := s.db.Query(`SELECT id, author, text, photo != '', created FROM chat_messages WHERE room = ? AND id < ? ORDER BY id DESC LIMIT 60`, room.ID, before)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	list := []chatMessage{}
	for rows.Next() {
		var m chatMessage
		if rows.Scan(&m.ID, &m.Author, &m.Text, &m.Photo, &m.Created) == nil {
			m.Mine = m.Author == u.Name
			list = append(list, m)
		}
	}
	rows.Close()
	names := map[string]string{}
	for i := range list {
		if _, ok := names[list[i].Author]; !ok {
			names[list[i].Author] = s.displayOf(list[i].Author)
		}
		list[i].Who = names[list[i].Author]
	}
	slices.Reverse(list)
	if len(list) > 0 && before == 1<<62 {
		s.db.Exec(`INSERT INTO chat_read (user, room, last) VALUES (?, ?, ?) ON CONFLICT(user, room) DO UPDATE SET last = max(last, excluded.last)`,
			u.Name, room.ID, list[len(list)-1].ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": room, "messages": list})
}

func (s *Server) postChat(w http.ResponseWriter, r *http.Request, u user) {
	room, ok := s.chatRoom(u, r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "There's no such room.")
		return
	}
	var text, photo string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "image/") {
		b, err := io.ReadAll(io.LimitReader(r.Body, 30<<20))
		ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp"}[http.DetectContentType(b)]
		if err != nil || ext == "" {
			jsonError(w, http.StatusBadRequest, "That isn't a JPEG, PNG, GIF or WebP picture.")
			return
		}
		s.data.MkdirAll("chat", 0o750)
		photo = "chat/" + randomToken(12) + ext
		if err := s.data.WriteFile(photo, b, 0o640); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		text = clip(r.URL.Query().Get("caption"), 2000)
	} else {
		var in struct {
			Text string `json:"text"`
		}
		if err := readJSON(r, &in); err != nil || clip(in.Text, 8000) == "" {
			jsonError(w, http.StatusBadRequest, "Write something first.")
			return
		}
		text = clip(in.Text, 8000)
	}
	res, err := s.db.Exec(`INSERT INTO chat_messages (room, author, text, photo, created) VALUES (?, ?, ?, ?, ?)`, room.ID, u.Name, text, photo, time.Now().Unix())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	s.db.Exec(`INSERT INTO chat_read (user, room, last) VALUES (?, ?, ?) ON CONFLICT(user, room) DO UPDATE SET last = excluded.last`, u.Name, room.ID, id)
	members := s.roomMembers(room)
	preview := firstLine(text)
	if preview == "" {
		preview = "sent a photo"
	}
	for _, m := range members {
		if m != u.Name && s.once(fmt.Sprintf("chat|%d|%s", room.ID, m), 3*time.Minute) {
			go s.pushTo(m, fmt.Sprintf("%s in %s: %s", displayName(u), room.Name, preview), "#/chat/"+strconv.FormatInt(room.ID, 10))
		}
	}
	s.changed("chat", members...)
	writeJSON(w, http.StatusCreated, chatMessage{ID: id, Author: u.Name, Who: displayName(u), Text: text, Photo: photo != "", Created: time.Now().Unix(), Mine: true})
}

func (s *Server) chatPhoto(w http.ResponseWriter, r *http.Request, u user) {
	var room int64
	var photo string
	if s.db.QueryRow(`SELECT room, photo FROM chat_messages WHERE id = ?`, r.PathValue("id")).Scan(&room, &photo) != nil || photo == "" {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.chatRoom(u, strconv.FormatInt(room, 10)); !ok {
		http.NotFound(w, r)
		return
	}
	s.serveThumb(w, r, photo)
}

func (s *Server) deleteChatMessage(w http.ResponseWriter, r *http.Request, u user) {
	var room int64
	var author, photo string
	if s.db.QueryRow(`SELECT room, author, photo FROM chat_messages WHERE id = ?`, r.PathValue("id")).Scan(&room, &author, &photo) != nil {
		jsonError(w, http.StatusNotFound, "That message is gone.")
		return
	}
	if author != u.Name && !u.Admin {
		jsonError(w, http.StatusForbidden, "Only your own messages.")
		return
	}
	s.db.Exec(`DELETE FROM chat_messages WHERE id = ?`, r.PathValue("id"))
	if photo != "" {
		s.data.Remove(photo)
	}
	if rm, ok := s.chatRoom(u, strconv.FormatInt(room, 10)); ok {
		s.changed("chat", s.roomMembers(rm)...)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// chatUnread is how many messages u hasn't read, for the menu's badge.
func (s *Server) chatUnread(u user) int {
	n := 0
	for _, r := range s.chatRooms(u) {
		n += r.Unread
	}
	return n
}
