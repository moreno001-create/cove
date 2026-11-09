package app

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	gowebdav "github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

type rig struct {
	t   *testing.T
	s   *Server
	srv *httptest.Server
}

func newRig(t *testing.T, mod func(*Config)) *rig {
	t.Helper()
	cfg := Config{Data: t.TempDir(), DevUser: "alice"}
	if mod != nil {
		mod(&cfg)
	}
	r := &rig{t: t}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { r.s.Handler().ServeHTTP(w, req) }))
	if cfg.URL == "" {
		cfg.URL = r.srv.URL
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.s = s
	t.Cleanup(func() { r.srv.Close(); s.Close() })
	return r
}

// call makes an app request (with the X-Cove header) and decodes a JSON answer into out.
func (r *rig) call(method, path string, body any, out any) int {
	r.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, r.srv.URL+path, rd)
	req.Header.Set("X-Cove", "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			r.t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, data)
		}
	}
	return res.StatusCode
}

func (r *rig) list(dir string) []string {
	r.t.Helper()
	var out struct{ Items []fileItem }
	if c := r.call("GET", "/api/files?path="+url.QueryEscape(dir), nil, &out); c != 200 {
		r.t.Fatalf("list %s: %d", dir, c)
	}
	var names []string
	for _, it := range out.Items {
		names = append(names, it.Name)
	}
	return names
}

func TestFiles(t *testing.T) {
	r := newRig(t, nil)
	if c := r.call("PUT", "/api/files/content?path=/docs/a.txt", "hello", nil); c != 201 {
		t.Fatalf("upload: %d", c)
	}
	var up struct{ Path string }
	r.call("PUT", "/api/files/content?path=/docs/a.txt", "again", &up)
	if up.Path != "/docs/a (2).txt" {
		t.Errorf("a second upload should get a free name, got %q", up.Path)
	}
	r.call("PUT", "/api/files/content?path=/docs/a.txt&conflict=replace", "replaced", nil)
	if got := r.list("/docs"); strings.Join(got, ",") != "a (2).txt,a.txt" {
		t.Errorf("list: %v", got)
	}
	// Paths can't climb out of the user's folder.
	r.call("PUT", "/api/files/content?path=../../escape.txt", "x", nil)
	if _, err := os.Stat(filepath.Join(r.s.cfg.Data, "escape.txt")); err == nil {
		t.Fatal("an upload got outside the user's folder")
	}
	if _, err := os.Stat(filepath.Join(r.s.cfg.Data, "files/alice/escape.txt")); err != nil {
		t.Errorf("../ should clean to the top of the user's folder: %v", err)
	}
	// Pages are downloaded, not shown on Cove's address.
	r.call("PUT", "/api/files/content?path=/page.html", "<script>alert(1)</script>", nil)
	res, _ := http.Get(r.srv.URL + "/api/files/content?path=/page.html")
	if ct := res.Header.Get("Content-Type"); strings.Contains(ct, "html") || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("an HTML file was served as %s, %s", ct, res.Header.Get("Content-Security-Policy"))
	}
	res.Body.Close()
	res, _ = http.Get(r.srv.URL + "/api/files/content?path=/docs/a.txt")
	if b, _ := io.ReadAll(res.Body); string(b) != "replaced" || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("text file: %q %s", b, res.Header.Get("Content-Type"))
	}
	res.Body.Close()

	if c := r.call("POST", "/api/files/move", map[string]string{"from": "/docs", "to": "/docs/inner"}, nil); c != 400 {
		t.Errorf("moving a folder into itself: %d", c)
	}
	r.call("POST", "/api/files/mkdir", map[string]string{"path": "/archive"}, nil)
	if c := r.call("POST", "/api/files/move", map[string]string{"from": "/docs", "to": "/archive/docs"}, nil); c != 200 {
		t.Fatalf("move: %d", c)
	}
	// The trash keeps things 30 days, and restoring puts them back.
	r.call("POST", "/api/files/delete", map[string]any{"paths": []string{"/archive/docs"}}, nil)
	var trash []trashItem
	r.call("GET", "/api/trash", nil, &trash)
	if len(trash) != 1 || trash[0].Path != "/archive/docs" || !trash[0].Dir || trash[0].Size != int64(len("replaced")+len("again")) {
		t.Fatalf("trash: %+v", trash)
	}
	r.call("POST", "/api/trash/restore", map[string]any{"ids": []string{trash[0].ID}}, nil)
	if got := r.list("/archive/docs"); len(got) != 2 {
		t.Errorf("restored folder: %v", got)
	}
	r.call("POST", "/api/files/delete", map[string]any{"paths": []string{"/archive"}}, nil)
	r.s.cleanUp(time.Now().Add(31 * 24 * time.Hour))
	r.call("GET", "/api/trash", nil, &trash)
	if len(trash) != 0 {
		t.Errorf("trash older than 30 days should be emptied: %+v", trash)
	}

	r.call("PUT", "/api/files/content?path=/zipme/one.txt", "1", nil)
	r.call("PUT", "/api/files/content?path=/zipme/sub/two.txt", "22", nil)
	res, _ = http.Get(r.srv.URL + "/api/files/zip?path=/zipme")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "one.txt,sub/,sub/two.txt" {
		t.Errorf("zip: %v", names)
	}
	var found struct{ Files []fileHit }
	r.call("GET", "/api/search?q=TWO", nil, &found)
	if len(found.Files) != 1 || found.Files[0].Path != "/zipme/sub/two.txt" {
		t.Errorf("search: %+v", found)
	}
}

func TestChangesNeedTheHeader(t *testing.T) {
	r := newRig(t, nil)
	req, _ := http.NewRequest("POST", r.srv.URL+"/api/files/mkdir", strings.NewReader(`{"path":"/x"}`))
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Errorf("a change without X-Cove: %d", res.StatusCode)
	}
}

func TestShares(t *testing.T) {
	r := newRig(t, nil)
	r.call("PUT", "/api/files/content?path=/trip/photo.jpg", "jpeg", nil)
	var sh share
	if c := r.call("POST", "/api/shares", map[string]any{"path": "/trip", "password": "sesame", "mode": "upload"}, &sh); c != 201 {
		t.Fatalf("share: %d", c)
	}
	if !sh.Dir || !sh.HasPassword || sh.Mode != "upload" || !strings.HasPrefix(sh.URL, r.srv.URL+"/s/") {
		t.Fatalf("share: %+v", sh)
	}
	jar, _ := cookiejar.New(nil)
	guest := &http.Client{Jar: jar}
	get := func(p string) (int, string) {
		res, err := guest.Get(r.srv.URL + "/s/" + sh.Token + p)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if c, _ := get("/content?path=/photo.jpg"); c != 401 {
		t.Errorf("a locked link gave the file: %d", c)
	}
	if c, body := get("/info"); c != 200 || !strings.Contains(body, `"needsPassword":true`) || strings.Contains(body, "photo") {
		t.Errorf("info before unlocking: %d %s", c, body)
	}
	unlock := func(pw string) int {
		res, _ := guest.Post(r.srv.URL+"/s/"+sh.Token+"/unlock", "application/json", strings.NewReader(`{"password":"`+pw+`"}`))
		res.Body.Close()
		return res.StatusCode
	}
	if c := unlock("wrong"); c != 403 {
		t.Errorf("wrong password: %d", c)
	}
	if c := unlock("sesame"); c != 200 {
		t.Fatalf("right password: %d", c)
	}
	if c, body := get("/content?path=/photo.jpg"); c != 200 || body != "jpeg" {
		t.Errorf("unlocked file: %d %q", c, body)
	}
	if c, _ := get("/content?path=/../../../secret.key"); c != 404 {
		t.Errorf("a link reached outside its folder: %d", c)
	}
	req, _ := http.NewRequest("PUT", r.srv.URL+"/s/"+sh.Token+"/upload?name=../../evil.txt", strings.NewReader("hi"))
	res, _ := guest.Do(req)
	res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatalf("upload to link: %d", res.StatusCode)
	}
	if got := r.list("/trip"); strings.Join(got, ",") != "evil.txt,photo.jpg" {
		t.Errorf("an upload through a link should land in the shared folder: %v", got)
	}
	// Moving keeps the link; deleting ends it.
	r.call("POST", "/api/files/move", map[string]string{"from": "/trip", "to": "/Trips 2026"}, nil)
	if c, body := get("/content?path=/photo.jpg"); c != 200 || body != "jpeg" {
		t.Errorf("after a move: %d %q", c, body)
	}
	r.call("POST", "/api/files/delete", map[string]any{"paths": []string{"/Trips 2026"}}, nil)
	if c, _ := get("/info"); c != 404 {
		t.Errorf("a link to a deleted folder: %d", c)
	}
	// A drop box takes files and shows nothing.
	r.call("POST", "/api/files/mkdir", map[string]string{"path": "/inbox"}, nil)
	r.call("PUT", "/api/files/content?path=/inbox/private.txt", "mine", nil)
	var drop share
	r.call("POST", "/api/shares", map[string]any{"path": "/inbox", "mode": "drop"}, &drop)
	res, _ = http.Get(r.srv.URL + "/s/" + drop.Token + "/info")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(b), "private") || !strings.Contains(string(b), `"mode":"drop"`) {
		t.Errorf("drop box info: %s", b)
	}
	for _, p := range []string{"/content?path=/private.txt", "/zip", "/thumb?path=/private.txt"} {
		res, _ = http.Get(r.srv.URL + "/s/" + drop.Token + p)
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Errorf("drop box %s: %d", p, res.StatusCode)
		}
	}
	req, _ = http.NewRequest("PUT", r.srv.URL+"/s/"+drop.Token+"/upload?name=private.txt", strings.NewReader("theirs"))
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if got := r.list("/inbox"); res.StatusCode != 201 || strings.Join(got, ",") != "private (2).txt,private.txt" {
		t.Errorf("drop box upload: %d %v", res.StatusCode, got)
	}
	r.call("PUT", "/api/files/content?path=/old.txt", "x", nil)
	var short share
	r.call("POST", "/api/shares", map[string]any{"path": "/old.txt", "expiresDays": 1}, &short)
	r.s.db.Exec(`UPDATE shares SET expires = ? WHERE token = ?`, time.Now().Add(-time.Hour).Unix(), short.Token)
	res, _ = http.Get(r.srv.URL + "/s/" + short.Token + "/content")
	res.Body.Close()
	if res.StatusCode != 410 {
		t.Errorf("an expired link: %d", res.StatusCode)
	}
}

func makeAppPassword(t *testing.T, r *rig) string {
	var out struct{ Password string }
	if c := r.call("POST", "/api/app-passwords", map[string]string{"name": "phone"}, &out); c != 201 || len(out.Password) != 29 {
		t.Fatalf("app password: %d %q", c, out.Password)
	}
	return out.Password
}

type basicAuth struct{ user, pass string }

func (b basicAuth) Do(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(b.user, b.pass)
	return http.DefaultClient.Do(req)
}

func TestWebDAV(t *testing.T) {
	r := newRig(t, nil)
	pass := makeAppPassword(t, r)
	do := func(method, p, body, pw string) *http.Response {
		req, _ := http.NewRequest(method, r.srv.URL+p, strings.NewReader(body))
		req.SetBasicAuth("alice", pw)
		if method == "PROPFIND" {
			req.Header.Set("Depth", "1")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if res := do("PROPFIND", "/dav/files/", "", "not-it"); res.StatusCode != 401 {
		t.Errorf("wrong app password: %d", res.StatusCode)
	}
	if res := do("PUT", "/dav/files/notes.txt", "from finder", strings.ToUpper(pass)); res.StatusCode != 201 {
		t.Fatalf("PUT (password typed in capitals): %d", res.StatusCode)
	}
	if res := do("PROPFIND", "/dav/files/", "", pass); res.StatusCode != 207 {
		t.Errorf("PROPFIND: %d", res.StatusCode)
	}
	if got := r.list("/"); len(got) != 1 || got[0] != "notes.txt" {
		t.Errorf("a WebDAV upload should show in the app: %v", got)
	}
	do("DELETE", "/dav/files/notes.txt", "", pass)
	var trash []trashItem
	r.call("GET", "/api/trash", nil, &trash)
	if len(trash) != 1 {
		t.Errorf("deleting over WebDAV should go to the trash: %+v", trash)
	}
}

func TestCalDAV(t *testing.T) {
	r := newRig(t, nil)
	pass := makeAppPassword(t, r)
	c, err := caldav.NewClient(basicAuth{"alice", pass}, r.srv.URL+"/dav/cal/")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil || principal != "/dav/cal/alice/" {
		t.Fatalf("principal: %q %v", principal, err)
	}
	home, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	cals, err := c.FindCalendars(ctx, home)
	if err != nil || len(cals) != 2 || cals[0].Name != "Personal" || cals[1].SupportedComponentSet[0] != "VTODO" {
		t.Fatalf("calendars: %+v %v", cals, err)
	}
	// A weekly meeting from a phone, in New York time, with one week skipped and one moved.
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:standup@phone\r\nDTSTAMP:20261101T000000Z\r\nSUMMARY:Standup\r\n" +
		"DTSTART;TZID=America/New_York:20261102T090000\r\nDTEND;TZID=America/New_York:20261102T093000\r\n" +
		"RRULE:FREQ=WEEKLY;COUNT=4\r\nEXDATE;TZID=America/New_York:20261109T090000\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:standup@phone\r\nDTSTAMP:20261101T000000Z\r\nSUMMARY:Standup (moved)\r\n" +
		"RECURRENCE-ID;TZID=America/New_York:20261116T090000\r\n" +
		"DTSTART;TZID=America/New_York:20261116T110000\r\nDTEND;TZID=America/New_York:20261116T113000\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	obj, err := ical.NewDecoder(strings.NewReader(ics)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutCalendarObject(ctx, cals[0].Path+"standup.ics", obj); err != nil {
		t.Fatalf("PUT event: %v", err)
	}
	got, err := c.QueryCalendar(ctx, cals[0].Path, &caldav.CalendarQuery{CompFilter: caldav.CompFilter{Name: "VCALENDAR",
		Comps: []caldav.CompFilter{{Name: "VEVENT"}}}})
	if err != nil || len(got) != 1 {
		t.Fatalf("query: %d %v", len(got), err)
	}
	var occ []occurrence
	r.call("GET", "/api/events?from=2026-11-01&to=2026-11-30&tz=America/New_York", nil, &occ)
	var seen []string
	for _, o := range occ {
		seen = append(seen, o.Title+"@"+o.Start)
	}
	want := "Standup@2026-11-02T09:00:00-05:00,Standup (moved)@2026-11-16T11:00:00-05:00,Standup@2026-11-23T09:00:00-05:00"
	if strings.Join(seen, ",") != want {
		t.Errorf("occurrences:\n got %v\nwant %v", strings.Join(seen, ","), want)
	}
	// Someone else's calendars are out of reach.
	other := &http.Client{}
	req, _ := http.NewRequest("PROPFIND", r.srv.URL+"/dav/cal/bob/calendars/personal/", nil)
	req.SetBasicAuth("alice", pass)
	res, _ := other.Do(req)
	res.Body.Close()
	if res.StatusCode < 400 {
		t.Errorf("another user's calendar: %d", res.StatusCode)
	}
	_ = gowebdav.NewHTTPError
}

func TestEvents(t *testing.T) {
	r := newRig(t, nil)
	var made struct{ ID string }
	if c := r.call("POST", "/api/events", eventInput{Cal: "personal", Title: "Dentist", Start: "2026-12-03T15:00:00-08:00",
		End: "2026-12-03T16:00:00-08:00", TZ: "America/Los_Angeles", Repeat: "MONTHLY"}, &made); c != 201 {
		t.Fatalf("create: %d", c)
	}
	r.call("POST", "/api/events", eventInput{Cal: "personal", Title: "Holiday", AllDay: true, Start: "2026-12-24", End: "2026-12-26"}, nil)
	var occ []occurrence
	r.call("GET", "/api/events?from=2026-12-01&to=2027-01-31&tz=America/Los_Angeles", nil, &occ)
	var seen []string
	for _, o := range occ {
		seen = append(seen, o.Title+"@"+o.Start+"-"+o.End)
	}
	want := "Dentist@2026-12-03T15:00:00-08:00-2026-12-03T16:00:00-08:00,Holiday@2026-12-24-2026-12-26,Dentist@2027-01-03T15:00:00-08:00-2027-01-03T16:00:00-08:00"
	if strings.Join(seen, ",") != want {
		t.Errorf("events:\n got %s\nwant %s", strings.Join(seen, ","), want)
	}
	cal, name, _ := strings.Cut(made.ID, "/")
	if c := r.call("PUT", "/api/events/"+cal+"/"+name, eventInput{Cal: "personal", Title: "Dentist (moved)",
		Start: "2026-12-04T10:00:00-08:00", End: "2026-12-04T11:00:00-08:00", TZ: "America/Los_Angeles"}, nil); c != 200 {
		t.Fatalf("update: %d", c)
	}
	obj, _ := r.s.loadObject("alice", cal, name)
	ev := obj.Events()[0]
	if seq, _ := ev.Props.Get(ical.PropSequence).Int(); seq != 1 || ev.Props.Get(ical.PropRecurrenceRule) != nil ||
		ev.Props.Get(ical.PropDateTimeStart).Params.Get(ical.PropTimezoneID) != "America/Los_Angeles" {
		t.Errorf("updated event: %+v", ev.Props)
	}
	if c := r.call("DELETE", "/api/events/"+cal+"/"+name, nil, nil); c != 200 {
		t.Errorf("delete: %d", c)
	}
}

func TestOffice(t *testing.T) {
	collabora := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `<wopi-discovery><net-zone name="external-http"><app name="writer">
			<action ext="docx" name="view" urlsrc="https://office.example/browser/x/cool.html?"/>
			<action ext="docx" name="edit" urlsrc="https://office.example/browser/x/cool.html?"/>
			</app></net-zone></wopi-discovery>`)
	}))
	defer collabora.Close()
	r := newRig(t, func(c *Config) { c.Collabora = collabora.URL })
	var made struct{ Path string }
	r.call("POST", "/api/files/new", map[string]string{"path": "/", "kind": "document", "name": "Letter"}, &made)
	if made.Path != "/Letter.docx" {
		t.Fatalf("new document: %q", made.Path)
	}
	var open struct {
		URL, Token string
		TTL        int64
	}
	if c := r.call("POST", "/api/office/open", map[string]string{"path": made.Path}, &open); c != 200 {
		t.Fatalf("open: %d", c)
	}
	u, _ := url.Parse(open.URL)
	src := u.Query().Get("WOPISrc")
	if !strings.HasPrefix(open.URL, "https://office.example/browser/x/cool.html?WOPISrc=") || !strings.HasPrefix(src, r.srv.URL+"/wopi/files/") {
		t.Fatalf("open URL: %s", open.URL)
	}
	wopi := func(method, suffix, token string, body string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(method, src+suffix+"?access_token="+url.QueryEscape(token), strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	res, body := wopi("GET", "", open.Token, "", nil)
	var info map[string]any
	json.Unmarshal([]byte(body), &info)
	if res.StatusCode != 200 || info["BaseFileName"] != "Letter.docx" || info["UserCanWrite"] != true {
		t.Fatalf("CheckFileInfo: %d %s", res.StatusCode, body)
	}
	if res, _ := wopi("GET", "", open.Token+"x", "", nil); res.StatusCode != 401 {
		t.Errorf("a changed token: %d", res.StatusCode)
	}
	stamp := info["LastModifiedTime"].(string)
	if res, body := wopi("POST", "/contents", open.Token, "DOCX v1", map[string]string{"X-WOPI-Override": "PUT",
		"X-COOL-WOPI-Timestamp": stamp}); res.StatusCode != 200 {
		t.Fatalf("PutFile: %d %s", res.StatusCode, body)
	}
	if res, body := wopi("POST", "/contents", open.Token, "DOCX old", map[string]string{"X-WOPI-Override": "PUT",
		"X-COOL-WOPI-Timestamp": stamp}); res.StatusCode != 409 || !strings.Contains(body, "1010") {
		t.Errorf("saving over a newer file: %d %s", res.StatusCode, body)
	}
	if _, body := wopi("GET", "/contents", open.Token, "", nil); body != "DOCX v1" {
		t.Errorf("GetFile: %q", body)
	}
	// A token for one file doesn't open another.
	r.call("PUT", "/api/files/content?path=/other.docx", "secret", nil)
	other := strings.Replace(src, fileID("alice", "Letter.docx"), fileID("alice", "other.docx"), 1)
	req, _ := http.NewRequest("GET", other+"/contents?access_token="+url.QueryEscape(open.Token), nil)
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Errorf("a token opened another file: %d", res.StatusCode)
	}
}

// Signing in through a stand-in for Homeport's OpenID Connect.
func TestSignIn(t *testing.T) {
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": issuer.URL + "/authorize",
				"token_endpoint": issuer.URL + "/token", "userinfo_endpoint": issuer.URL + "/userinfo"})
		case "/authorize":
			q := req.URL.Query()
			http.Redirect(w, req, q.Get("redirect_uri")+"?code=c0de&state="+q.Get("state"), http.StatusFound)
		case "/token":
			id, secret, _ := req.BasicAuth()
			req.ParseForm()
			if id != "cove" || secret != "s3cret" || req.PostForm.Get("code") != "c0de" || req.PostForm.Get("code_verifier") == "" {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "token_type": "Bearer"})
		case "/userinfo":
			if req.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"sub": "bob", "preferred_username": "bob", "name": "Bob B"})
		}
	}))
	defer issuer.Close()
	r := newRig(t, func(c *Config) { c.DevUser, c.Issuer, c.ClientID, c.ClientSecret = "", issuer.URL, "cove", "s3cret" })
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		t.Logf("→ %s", req.URL)
		if len(via) > 8 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}}
	res, err := browser.Get(r.srv.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("before signing in: %d", res.StatusCode)
	}
	res, err = browser.Get(r.srv.URL + "/") // → /login → issuer → /auth/callback → /
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	res, _ = browser.Get(r.srv.URL + "/api/me")
	var me struct{ User user }
	json.NewDecoder(res.Body).Decode(&me)
	res.Body.Close()
	if me.User.Name != "bob" || me.User.Display != "Bob B" {
		t.Fatalf("signed in as %+v", me.User)
	}
	if _, err := os.Stat(filepath.Join(r.s.cfg.Data, "files/bob")); err != nil {
		t.Errorf("bob's folder: %v", err)
	}
	// A callback with someone else's state is refused.
	noFollow := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, _ = noFollow.Get(r.srv.URL + "/login") // a sign-in under way, with its own state
	res.Body.Close()
	res, _ = noFollow.Get(r.srv.URL + "/auth/callback?code=c0de&state=forged")
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Errorf("forged state: %d", res.StatusCode)
	}
}

func TestAppPasswordShape(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		p := newAppPassword()
		if len(p) != 29 || strings.Count(p, "-") != 4 || seen[p] {
			t.Fatalf("app password %q", p)
		}
		seen[p] = true
	}
}

func TestChunkedUpload(t *testing.T) {
	r := newRig(t, nil)
	data := bytes.Repeat([]byte("0123456789"), 1000)
	var up upload
	if c := r.call("POST", "/api/uploads", map[string]any{"path": "/big/video.mp4", "size": len(data), "mod": 1700000000000}, &up); c != 200 || up.Received != 0 {
		t.Fatalf("start: %d %+v", c, up)
	}
	var got struct{ Received int64 }
	r.call("PUT", "/api/uploads/"+up.ID+"?offset=0", data[:4000], &got)
	if got.Received != 4000 {
		t.Fatalf("first piece: %+v", got)
	}
	// A piece out of order is refused with how much arrived; starting again resumes.
	if c := r.call("PUT", "/api/uploads/"+up.ID+"?offset=9000", data[9000:], &got); c != 409 || got.Received != 4000 {
		t.Errorf("out of order: %d %+v", c, got)
	}
	var again upload
	r.call("POST", "/api/uploads", map[string]any{"path": "/big/video.mp4", "size": len(data), "mod": 1700000000000}, &again)
	if again.ID != up.ID || again.Received != 4000 {
		t.Errorf("resuming: %+v", again)
	}
	if c := r.call("POST", "/api/uploads/"+up.ID+"/finish", map[string]string{}, nil); c != 409 {
		t.Errorf("finishing early: %d", c)
	}
	r.call("PUT", "/api/uploads/"+up.ID+"?offset=4000", data[4000:], &got)
	var done struct{ Path string }
	if c := r.call("POST", "/api/uploads/"+up.ID+"/finish", map[string]string{}, &done); c != 201 || done.Path != "/big/video.mp4" {
		t.Fatalf("finish: %d %+v", c, done)
	}
	b, _ := os.ReadFile(filepath.Join(r.s.cfg.Data, "files/alice/big/video.mp4"))
	info, _ := os.Stat(filepath.Join(r.s.cfg.Data, "files/alice/big/video.mp4"))
	if !bytes.Equal(b, data) || info.ModTime().UnixMilli() != 1700000000000 {
		t.Errorf("uploaded file: %d bytes, %v", len(b), info.ModTime())
	}
}

func TestThumbnails(t *testing.T) {
	r := newRig(t, nil)
	// A 40×20 picture: left half red, right half blue.
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			c := color.RGBA{255, 0, 0, 255}
			if x >= 20 {
				c = color.RGBA{0, 0, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	r.call("PUT", "/api/files/content?path=/pic.png", buf.Bytes(), nil)
	res, err := http.Get(r.srv.URL + "/api/files/thumb?path=/pic.png&w=20")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	th, err := jpeg.Decode(res.Body)
	if err != nil || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumbnail: %v %s", err, res.Header.Get("Content-Type"))
	}
	if b := th.Bounds(); b.Dx() != 20 || b.Dy() != 10 {
		t.Errorf("thumbnail size %v", b)
	}
	// Orientation 6 (turned clockwise): the 40×20 picture shows 20×40, red on top.
	turned := orient(img, 6)
	if b := turned.Bounds(); b.Dx() != 20 || b.Dy() != 40 {
		t.Fatalf("turned size %v", b)
	}
	if rr, _, bb, _ := turned.At(10, 5).RGBA(); rr < bb {
		t.Errorf("turned: top should be red")
	}
	exif := []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00")
	if o := exifOrientation(exif); o != 6 {
		t.Errorf("EXIF orientation: %d", o)
	}
}

// people is a rig with real sign-ins (no try-out user), and a client per person.
type people struct {
	*rig
	cookie map[string]string
}

func newPeople(t *testing.T, mod func(*Config), names ...string) *people {
	r := newRig(t, func(c *Config) {
		c.DevUser, c.Issuer, c.ClientID, c.ClientSecret = "", "http://issuer.invalid", "cove", "x"
		if mod != nil {
			mod(c)
		}
	})
	p := &people{rig: r, cookie: map[string]string{}}
	for _, n := range names {
		if err := r.s.ensureUser(user{Name: n, Display: strings.ToUpper(n[:1]) + n[1:]}); err != nil {
			t.Fatal(err)
		}
		tok := randomToken(16)
		r.s.db.Exec(`INSERT INTO sessions (id, user, expires) VALUES (?, ?, ?)`, hashHex(tok), n, time.Now().Add(time.Hour).Unix())
		p.cookie[n] = tok
	}
	return p
}

func (p *people) as(who, method, path string, body any, out any) int {
	p.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, p.srv.URL+path, rd)
	req.Header.Set("X-Cove", "1")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie[who]})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			p.t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, data)
		}
	}
	return res.StatusCode
}

func TestSharingWithPeople(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob", "carol")
	p.as("alice", "PUT", "/api/files/content?path=/Family/plan.txt", "v1", nil)
	p.as("alice", "PUT", "/api/files/content?path=/Private/diary.txt", "secret", nil)
	var users []person
	p.as("alice", "GET", "/api/users", nil, &users)
	if len(users) != 2 || users[0].Name != "bob" {
		t.Errorf("people to share with: %+v", users)
	}
	if c := p.as("alice", "POST", "/api/user-shares", map[string]any{"path": "/Family", "user": "bob", "edit": false}, nil); c != 201 {
		t.Fatalf("share: %d", c)
	}
	var mine []userShare
	p.as("bob", "GET", "/api/shared-with-me", nil, &mine)
	if len(mine) != 1 || mine[0].Name != "Family" || mine[0].Who != "Alice" || mine[0].Edit {
		t.Fatalf("shared with bob: %+v", mine)
	}
	id := fmt.Sprint(mine[0].ID)
	var list struct {
		Path  string
		Items []fileItem
		Edit  bool
	}
	p.as("bob", "GET", "/api/files?share="+id+"&path=/", nil, &list)
	if list.Path != "/" || len(list.Items) != 1 || list.Items[0].Name != "plan.txt" || list.Edit {
		t.Fatalf("bob's view: %+v", list)
	}
	// Viewing only: no changes. And nothing outside the shared folder.
	if c := p.as("bob", "PUT", "/api/files/content?share="+id+"&path=/new.txt", "x", nil); c != 403 {
		t.Errorf("a view-only share took an upload: %d", c)
	}
	res, _ := http.Get(p.srv.URL + "/api/files/content?share=" + id + "&path=/../Private/diary.txt")
	res.Body.Close()
	var got map[string]any
	if c := p.as("bob", "GET", "/api/files/content?share="+id+"&path=/../Private/diary.txt", nil, nil); c != 404 {
		t.Errorf("a share reached outside its folder: %d", c)
	}
	if c := p.as("carol", "GET", "/api/files?share="+id, nil, &got); c != 404 {
		t.Errorf("someone it isn't shared with: %d", c)
	}
	// With editing: changes land in alice's folder, the shared folder itself stays put.
	p.as("alice", "POST", "/api/user-shares", map[string]any{"path": "/Family", "user": "bob", "edit": true}, nil)
	if c := p.as("bob", "PUT", "/api/files/content?share="+id+"&path=/plan.txt&conflict=replace", "v2 by bob", nil); c != 201 {
		t.Fatalf("bob edits: %d", c)
	}
	if b, _ := os.ReadFile(filepath.Join(p.s.cfg.Data, "files/alice/Family/plan.txt")); string(b) != "v2 by bob" {
		t.Errorf("alice's file: %q", b)
	}
	if c := p.as("bob", "POST", "/api/files/delete?share="+id, map[string]any{"paths": []string{"/"}}, nil); c != 403 {
		t.Errorf("deleting the shared folder itself: %d", c)
	}
	// alice renames it: the share follows.
	p.as("alice", "POST", "/api/files/move", map[string]string{"from": "/Family", "to": "/Our family"}, nil)
	p.as("bob", "GET", "/api/files?share="+id+"&path=/", nil, &list)
	if len(list.Items) != 1 {
		t.Errorf("after alice renamed it: %+v", list)
	}
	// Recent files point through the share.
	p.as("bob", "PUT", "/api/files/content?share="+id+"&path=/notes.md", "# hi", nil)
	var recent []struct {
		Path  string
		Share int64
	}
	p.as("bob", "GET", "/api/recent", nil, &recent)
	if len(recent) != 2 || recent[0].Path != "/notes.md" || fmt.Sprint(recent[0].Share) != id {
		t.Errorf("bob's recent: %+v", recent)
	}
	// Stopping sharing ends access.
	p.as("alice", "DELETE", "/api/user-shares/"+id, nil, nil)
	if c := p.as("bob", "GET", "/api/files?share="+id, nil, &got); c != 404 {
		t.Errorf("after stopping sharing: %d", c)
	}
}

func TestVersions(t *testing.T) {
	r := newRig(t, nil)
	r.call("PUT", "/api/files/content?path=/doc.txt", "one", nil)
	r.call("PUT", "/api/files/content?path=/doc.txt&conflict=replace", "two", nil)
	r.call("PUT", "/api/files/content?path=/doc.txt&conflict=replace", "three", nil) // within 10 minutes: no new version
	var vs []version
	r.call("GET", "/api/versions?path=/doc.txt", nil, &vs)
	if len(vs) != 1 {
		t.Fatalf("versions: %+v", vs)
	}
	if res, _ := http.Get(r.srv.URL + "/api/versions?path=/doc.txt"); res != nil {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if !strings.Contains(string(b), fmt.Sprintf(`"id":"%d"`, vs[0].ID)) {
			t.Errorf("version ids should be strings (JavaScript rounds big numbers): %s", b)
		}
	}
	res, _ := http.Get(fmt.Sprintf("%s/api/versions/content?path=/doc.txt&id=%d", r.srv.URL, vs[0].ID))
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "one" {
		t.Errorf("version content: %q", b)
	}
	if c := r.call("POST", "/api/versions/restore", map[string]any{"path": "/doc.txt", "id": fmt.Sprint(vs[0].ID)}, nil); c != 200 {
		t.Fatalf("restore: %d", c)
	}
	b, _ = os.ReadFile(filepath.Join(r.s.cfg.Data, "files/alice/doc.txt"))
	r.call("GET", "/api/versions?path=/doc.txt", nil, &vs)
	if string(b) != "one" || len(vs) != 2 {
		t.Errorf("after restoring: %q, %d versions", b, len(vs))
	}
	// Versions follow a rename.
	r.call("POST", "/api/files/move", map[string]string{"from": "/doc.txt", "to": "/renamed.txt"}, nil)
	r.call("GET", "/api/versions?path=/renamed.txt", nil, &vs)
	if len(vs) != 2 {
		t.Errorf("versions after a rename: %d", len(vs))
	}
}

func TestGuestEditingAndOfficeProxy(t *testing.T) {
	var office *httptest.Server
	office = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/hosting/discovery":
			fmt.Fprintf(w, `<wopi-discovery><net-zone name="external-http"><app name="writer">
				<action ext="docx" name="edit" urlsrc="https://cove.example/browser/x/cool.html?"/></app></net-zone></wopi-discovery>`)
		case strings.HasPrefix(req.URL.Path, "/browser/"):
			fmt.Fprintf(w, "collabora page for %s", req.Host)
		default:
			w.WriteHeader(404)
		}
	}))
	defer office.Close()
	r := newRig(t, func(c *Config) { c.OfficeInternal, c.WOPIURL, c.Collabora = office.URL, "http://app:8080", "set" })
	// Collabora's pages come through Cove's address; its admin doesn't.
	res, _ := http.Get(r.srv.URL + "/browser/x/cool.html")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.HasPrefix(string(b), "collabora page for 127.0.0.1") {
		t.Errorf("through the proxy: %d %q", res.StatusCode, b)
	}
	res, _ = http.Get(r.srv.URL + "/browser/dist/admin/admin.html")
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("Collabora's admin through Cove: %d", res.StatusCode)
	}
	r.call("PUT", "/api/files/content?path=/Plan.docx", "DOCX", nil)
	var open struct{ URL, Token string }
	r.call("POST", "/api/office/open", map[string]string{"path": "/Plan.docx"}, &open)
	if !strings.Contains(open.URL, "WOPISrc=http%3A%2F%2Fapp%3A8080%2Fwopi%2Ffiles%2F") || !strings.Contains(open.URL, "revisionhistory=1") {
		t.Errorf("open URL: %s", open.URL)
	}
	// A view link opens the document read-only; an edit link lets a guest save.
	var view, edit share
	r.call("POST", "/api/shares", map[string]any{"path": "/Plan.docx"}, &view)
	r.call("POST", "/api/shares", map[string]any{"path": "/Plan.docx", "mode": "edit"}, &edit)
	if edit.Mode != "edit" {
		t.Fatalf("edit link: %+v", edit)
	}
	guestOpen := func(token string) (string, bool) {
		res, err := http.Post(r.srv.URL+"/s/"+token+"/office", "application/json", strings.NewReader(`{"name":"Grandma"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var o struct {
			Token string
			Write bool
		}
		json.NewDecoder(res.Body).Decode(&o)
		return o.Token, o.Write
	}
	vt, vw := guestOpen(view.Token)
	et, ew := guestOpen(edit.Token)
	if vw || !ew {
		t.Fatalf("write: view link %v, edit link %v", vw, ew)
	}
	wopi := r.srv.URL + "/wopi/files/" + fileID("alice", "Plan.docx")
	put := func(tok string) int {
		req, _ := http.NewRequest("POST", wopi+"/contents?access_token="+url.QueryEscape(tok), strings.NewReader("DOCX by guest"))
		req.Header.Set("X-WOPI-Override", "PUT")
		res, _ := http.DefaultClient.Do(req)
		res.Body.Close()
		return res.StatusCode
	}
	if c := put(vt); c != 401 {
		t.Errorf("saving with a view link: %d", c)
	}
	if c := put(et); c != 200 {
		t.Errorf("saving with an edit link: %d", c)
	}
	res, _ = http.Get(wopi + "?access_token=" + url.QueryEscape(et))
	var info map[string]any
	json.NewDecoder(res.Body).Decode(&info)
	res.Body.Close()
	if info["UserFriendlyName"] != "Grandma (guest)" || info["OwnerId"] != "alice" {
		t.Errorf("guest CheckFileInfo: %+v", info)
	}
}

func TestSharedCalendars(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob", "carol")
	var family calendarInfo
	p.as("alice", "POST", "/api/calendars", map[string]string{"name": "Family", "color": "#ef4444"}, &family)
	if c := p.as("alice", "POST", "/api/calendars/"+family.ID+"/shares", map[string]any{"user": "*", "edit": true}, nil); c != 201 {
		t.Fatalf("share with everyone: %d", c)
	}
	// carol only views
	p.as("alice", "POST", "/api/calendars/"+family.ID+"/shares", map[string]any{"user": "carol", "edit": false}, nil)
	var bobs []calendarInfo
	p.as("bob", "GET", "/api/calendars?kind=events", nil, &bobs)
	ref := "alice~" + family.ID
	bobs = slices.DeleteFunc(bobs, func(c calendarInfo) bool { return c.Virtual })
	if len(bobs) != 2 || bobs[1].ID != ref || !bobs[1].Shared || !bobs[1].Edit || bobs[1].Who != "Alice" {
		t.Fatalf("bob's calendars: %+v", bobs)
	}
	ev := eventInput{Cal: ref, Title: "Picnic", AllDay: true, Start: "2026-12-05", End: "2026-12-06"}
	if c := p.as("bob", "POST", "/api/events", ev, nil); c != 201 {
		t.Fatalf("bob adds to the family calendar: %d", c)
	}
	if c := p.as("carol", "POST", "/api/events", eventInput{Cal: ref, Title: "No", AllDay: true, Start: "2026-12-05"}, nil); c != 400 {
		t.Errorf("carol (view only) added an event: %d", c)
	}
	var occ []occurrence
	p.as("alice", "GET", "/api/events?from=2026-12-01&to=2026-12-31", nil, &occ)
	if len(occ) != 1 || occ[0].Title != "Picnic" || occ[0].Cal != family.ID {
		t.Fatalf("alice sees: %+v", occ)
	}
	p.as("carol", "GET", "/api/events?from=2026-12-01&to=2026-12-31", nil, &occ)
	if len(occ) != 1 || !occ[0].ReadOnly {
		t.Errorf("carol sees: %+v", occ)
	}
	// Over CalDAV, carol's phone sees it and can't write to it.
	var pw struct{ Password string }
	p.as("carol", "POST", "/api/app-passwords", map[string]string{"name": "phone"}, &pw)
	c, _ := caldav.NewClient(basicAuth{"carol", pw.Password}, p.srv.URL+"/dav/cal/")
	cals, err := c.FindCalendars(context.Background(), "/dav/cal/carol/calendars/")
	if err != nil || len(cals) != 3 || cals[2].Path != "/dav/cal/carol/calendars/"+ref+"/" || cals[2].Name != "Family (Alice)" {
		t.Fatalf("carol's CalDAV calendars: %+v %v", cals, err)
	}
	obj, _ := ical.NewDecoder(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:x1\r\nDTSTAMP:20261101T000000Z\r\nDTSTART;VALUE=DATE:20261207\r\nSUMMARY:sneaky\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")).Decode()
	if _, err := c.PutCalendarObject(context.Background(), cals[2].Path+"x1.ics", obj); err == nil {
		t.Errorf("a view-only calendar took an event over CalDAV")
	}
	// bob takes it off his list; alice still has it.
	p.as("bob", "DELETE", "/api/calendars/"+ref, nil, nil)
	p.as("bob", "GET", "/api/calendars?kind=events", nil, &bobs)
	bobs = slices.DeleteFunc(bobs, func(c calendarInfo) bool { return c.Virtual })
	if len(bobs) != 1 {
		t.Errorf("after bob removed it: %+v", bobs)
	}
	if c := p.as("bob", "PUT", "/api/calendars/"+family.ID, calendarInfo{Name: "Mine now", Color: "#000000"}, nil); c != 404 {
		t.Errorf("bob renamed alice's calendar: %d", c)
	}
}

func TestTasks(t *testing.T) {
	r := newRig(t, nil)
	var lists []calendarInfo
	r.call("GET", "/api/calendars?kind=tasks", nil, &lists)
	if len(lists) != 1 || lists[0].ID != "tasks" {
		t.Fatalf("task lists: %+v", lists)
	}
	var milk, bread task
	r.call("POST", "/api/tasks", taskInput{List: "tasks", Title: "Milk"}, &milk)
	r.call("POST", "/api/tasks", taskInput{List: "tasks", Title: "Bread", Due: "2026-11-20"}, &bread)
	list, name, _ := strings.Cut(milk.ID, "/")
	if c := r.call("PUT", "/api/tasks/"+list+"/"+name, taskInput{Title: "Milk", Done: true}, nil); c != 200 {
		t.Fatalf("complete: %d", c)
	}
	var all []task
	r.call("GET", "/api/tasks", nil, &all)
	if len(all) != 2 || all[0].Title != "Bread" || all[0].Due != "2026-11-20" || !all[1].Done || all[1].Completed == 0 {
		t.Fatalf("tasks: %+v", all)
	}
	// It's a VTODO a phone would sync.
	obj, _ := r.s.loadObject("alice", "tasks", name)
	todo := todoOf(obj)
	if st, _ := todo.Props.Text(ical.PropStatus); st != "COMPLETED" || todo.Props.Get(ical.PropCompleted) == nil {
		t.Errorf("stored task: %+v", todo.Props)
	}
	// A task made on a phone shows up.
	pass := makeAppPassword(t, r)
	c, _ := caldav.NewClient(basicAuth{"alice", pass}, r.srv.URL+"/dav/cal/")
	o, _ := ical.NewDecoder(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VTODO\r\nUID:eggs\r\nDTSTAMP:20261101T000000Z\r\nSUMMARY:Eggs\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")).Decode()
	if _, err := c.PutCalendarObject(context.Background(), "/dav/cal/alice/calendars/tasks/eggs.ics", o); err != nil {
		t.Fatal(err)
	}
	r.call("GET", "/api/tasks", nil, &all)
	if len(all) != 3 {
		t.Errorf("after a phone added one: %+v", all)
	}
}

func TestContacts(t *testing.T) {
	r := newRig(t, nil)
	var made contact
	in := contact{Given: "Ada", Family: "Lovelace", Org: "Engines", Phones: []labeled{{"mobile", "+1 555 0100"}},
		Emails: []labeled{{"home", "ada@example.com"}}, Address: "12 Analytical St\nLondon\n\nUK", Birthday: "1815-12-10"}
	if c := r.call("POST", "/api/contacts", in, &made); c != 201 || made.Name != "Ada Lovelace" {
		t.Fatalf("create: %d %+v", c, made)
	}
	// A phone sees it over CardDAV, and one it adds shows in the app.
	pass := makeAppPassword(t, r)
	cl, err := carddav.NewClient(basicAuth{"alice", pass}, r.srv.URL+"/dav/card/")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	principal, err := cl.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := cl.FindAddressBookHomeSet(ctx, principal)
	books, err := cl.FindAddressBooks(ctx, home)
	if err != nil || len(books) != 1 {
		t.Fatalf("address books: %+v %v", books, err)
	}
	objs, err := cl.QueryAddressBook(ctx, books[0].Path, &carddav.AddressBookQuery{DataRequest: carddav.AddressDataRequest{AllProp: true}})
	if err != nil || len(objs) != 1 || objs[0].Card.Value(vcard.FieldFormattedName) != "Ada Lovelace" || objs[0].Card.Value(vcard.FieldTelephone) != "+1 555 0100" {
		t.Fatalf("over CardDAV: %+v %v", objs, err)
	}
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldUID, "grace")
	card.SetValue(vcard.FieldFormattedName, "Grace Hopper")
	if _, err := cl.PutAddressObject(ctx, books[0].Path+"grace.vcf", card); err != nil {
		t.Fatal(err)
	}
	var list []contact
	r.call("GET", "/api/contacts", nil, &list)
	if len(list) != 2 || list[0].Name != "Ada Lovelace" || list[1].Name != "Grace Hopper" {
		t.Fatalf("contacts: %+v", list)
	}
	// Editing keeps what the app doesn't show.
	book, name, _ := strings.Cut(list[1].ID, "/")
	stored, _ := r.s.loadCard("alice", book, name)
	stored.SetValue("X-SPOUSE", "nobody")
	r.s.saveCard("alice", book, name, stored)
	r.call("PUT", "/api/contacts/"+book+"/"+name, contact{Name: "Grace Hopper", Org: "Navy"}, nil)
	stored, _ = r.s.loadCard("alice", book, name)
	if stored.Value("X-SPOUSE") != "nobody" || stored.Value(vcard.FieldOrganization) != "Navy" {
		t.Errorf("after editing: %+v", stored)
	}
}

func TestShareTarget(t *testing.T) {
	r := newRig(t, nil)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	f, _ := mw.CreateFormFile("files", "receipt.pdf")
	f.Write([]byte("%PDF"))
	mw.Close()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := noFollow.Post(r.srv.URL+"/share-target", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 303 || !strings.Contains(res.Header.Get("Location"), "Shared%20to%20Cove") {
		t.Errorf("share target answered %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	body.Reset()
	mw = multipart.NewWriter(&body)
	mw.WriteField("title", "Recipe")
	mw.WriteField("url", "https://example.com/soup")
	mw.Close()
	res, _ = noFollow.Post(r.srv.URL+"/share-target", mw.FormDataContentType(), &body)
	res.Body.Close()
	got := r.list("/Shared to Cove")
	if len(got) != 2 || got[1] != "receipt.pdf" || !strings.HasPrefix(got[0], "Note ") {
		t.Errorf("shared to Cove: %v", got)
	}
	res, _ = http.Get(r.srv.URL + "/manifest.webmanifest")
	var m map[string]any
	json.NewDecoder(res.Body).Decode(&m)
	res.Body.Close()
	if m["share_target"] == nil || m["display"] != "standalone" {
		t.Errorf("manifest: %+v", m)
	}
}

func TestImportExport(t *testing.T) {
	r := newRig(t, nil)
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Google//EN\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:America/Chicago\r\nBEGIN:STANDARD\r\nDTSTART:19701101T020000\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0600\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:a@google\r\nDTSTAMP:20261101T000000Z\r\nDTSTART;TZID=America/Chicago:20261203T100000\r\nDTEND;TZID=America/Chicago:20261203T110000\r\nSUMMARY:Haircut\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:b@google\r\nDTSTAMP:20261101T000000Z\r\nDTSTART;VALUE=DATE:20261225\r\nSUMMARY:Christmas\r\nEND:VEVENT\r\n" +
		"BEGIN:VTODO\r\nUID:c@google\r\nDTSTAMP:20261101T000000Z\r\nSUMMARY:not an event\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	var res importResult
	r.call("POST", "/api/calendars/personal/import", ics, &res)
	if res.Added != 2 || res.Skipped != 1 {
		t.Fatalf("first import: %+v", res)
	}
	r.call("POST", "/api/calendars/personal/import", ics, &res)
	if res.Added != 0 || res.Unchanged != 2 {
		t.Errorf("importing again should change nothing: %+v", res)
	}
	r.call("POST", "/api/calendars/personal/import", strings.Replace(ics, "Haircut", "Haircut (moved)", 1), &res)
	if res.Updated != 1 || res.Unchanged != 1 {
		t.Errorf("an edited event should be updated: %+v", res)
	}
	resp, _ := http.Get(r.srv.URL + "/api/calendars/personal/export")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Haircut (moved)") || strings.Count(string(b), "BEGIN:VTIMEZONE") != 1 || strings.Count(string(b), "BEGIN:VEVENT") != 2 {
		t.Errorf("export:\n%s", b)
	}
	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada Lovelace\r\nEMAIL:ada@example.com\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:g1\r\nN:Hopper;Grace;;;\r\nTEL:+1 555 0199\r\nEND:VCARD\r\n"
	r.call("POST", "/api/contacts/import", vcf, &res)
	if res.Added != 2 {
		t.Fatalf("contacts import: %+v", res)
	}
	r.call("POST", "/api/contacts/import", vcf, &res)
	if res.Added != 0 {
		t.Errorf("importing contacts again duplicated: %+v", res)
	}
	var people []contact
	r.call("GET", "/api/contacts", nil, &people)
	if len(people) != 2 || people[1].Name != "Grace Hopper" {
		t.Errorf("contacts: %+v", people)
	}
	resp, _ = http.Get(r.srv.URL + "/api/contacts/export")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Count(string(b), "BEGIN:VCARD") != 2 {
		t.Errorf("contacts export: %s", b)
	}
}

func TestHomeFavoritesBirthdays(t *testing.T) {
	r := newRig(t, nil)
	r.call("PUT", "/api/files/content?path=/Plans/trip.md", "# trip", nil)
	if c := r.call("POST", "/api/favorites", map[string]string{"path": "/Plans"}, nil); c != 201 {
		t.Fatalf("star: %d", c)
	}
	r.call("POST", "/api/files/move", map[string]string{"from": "/Plans", "to": "/Trips"}, nil)
	today := time.Now().UTC()
	r.call("POST", "/api/contacts", contact{Given: "Ada", Family: "L", Birthday: today.AddDate(-30, 0, 2).Format("2006-01-02")}, nil)
	r.call("POST", "/api/tasks", taskInput{List: "tasks", Title: "Call mom", Due: today.Format("2006-01-02")}, nil)
	var home struct {
		Favorites []favorite
		Events    []occurrence
		Tasks     []task
		Recent    []recentEntry
	}
	r.call("GET", "/api/home?tz=UTC", nil, &home)
	if len(home.Favorites) != 1 || home.Favorites[0].Path != "/Trips" {
		t.Errorf("favorites follow a rename: %+v", home.Favorites)
	}
	if len(home.Events) != 1 || home.Events[0].Title != "Ada L's birthday (30)" || home.Events[0].Cal != "~birthdays" {
		t.Errorf("birthday on the home page: %+v", home.Events)
	}
	if len(home.Tasks) != 1 || home.Tasks[0].Title != "Call mom" {
		t.Errorf("tasks due: %+v", home.Tasks)
	}
	if len(home.Recent) != 1 || home.Recent[0].Name != "trip.md" {
		t.Errorf("recent: %+v", home.Recent)
	}
	r.call("DELETE", "/api/favorites?path=/Trips", nil, nil)
	r.call("GET", "/api/home?tz=UTC", nil, &home)
	if len(home.Favorites) != 0 {
		t.Errorf("after unstarring: %+v", home.Favorites)
	}
}

func TestNotificationsAndReminders(t *testing.T) {
	pushed := make(chan string, 10)
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		pushed <- req.URL.Path + " " + string(b) + " " + req.Header.Get("Click")
	}))
	defer ntfy.Close()
	p := newPeople(t, nil, "alice", "bob")
	if c := p.as("bob", "PUT", "/api/settings", userSettings{Ntfy: ntfy.URL + "/cove-bob", TZ: "UTC"}, nil); c != 200 {
		t.Fatalf("settings: %d", c)
	}
	p.as("alice", "PUT", "/api/files/content?path=/Family/list.txt", "x", nil)
	p.as("alice", "POST", "/api/user-shares", map[string]any{"path": "/Family", "user": "bob", "edit": true}, nil)
	var fam calendarInfo
	p.as("alice", "POST", "/api/calendars", map[string]string{"name": "Family"}, &fam)
	p.as("alice", "POST", "/api/calendars/"+fam.ID+"/shares", map[string]any{"user": "bob", "edit": true}, nil)
	start := time.Now().UTC().Add(20 * time.Minute).Truncate(time.Minute)
	fifteen := 15
	p.as("alice", "POST", "/api/events", eventInput{Cal: fam.ID, Title: "Dinner", Start: start.Format(time.RFC3339),
		End: start.Add(time.Hour).Format(time.RFC3339), TZ: "UTC", Reminder: &fifteen}, nil)
	var notes struct {
		Items  []struct{ Text, Link string }
		Unread int
	}
	p.as("bob", "GET", "/api/notifications", nil, &notes)
	var texts []string
	for _, n := range notes.Items {
		texts = append(texts, n.Text)
	}
	want := []string{"Alice added “Dinner” to Family", "Alice shared the calendar “Family” with you", "Alice shared “Family” with you"}
	if strings.Join(texts, " | ") != strings.Join(want, " | ") || notes.Unread != 3 {
		t.Errorf("bob's notifications:\n got %q\nwant %q", texts, want)
	}
	select {
	case got := <-pushed:
		if !strings.HasPrefix(got, "/cove-bob Alice shared “Family” with you "+p.srv.URL+"/#/shared/") {
			t.Errorf("ntfy got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Error("nothing was pushed to ntfy")
	}
	// The reminder fires 15 minutes before, for both of them, once.
	p.s.sendReminders(start.Add(-16*time.Minute), start.Add(-14*time.Minute))
	p.s.sendReminders(start.Add(-16*time.Minute), start.Add(-14*time.Minute))
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE text LIKE 'Dinner at %'`).Scan(&n)
	if n != 2 {
		t.Errorf("reminders sent: %d", n)
	}
	p.as("bob", "POST", "/api/notifications/read", nil, nil)
	p.as("bob", "GET", "/api/notifications", nil, &notes)
	if notes.Unread != 0 {
		t.Errorf("unread after reading: %d", notes.Unread)
	}
}

func TestInvites(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	invitees := []string{"bob"}
	var made struct{ ID string }
	p.as("alice", "POST", "/api/events", eventInput{Cal: "personal", Title: "Picnic", AllDay: true, Start: "2026-12-05", End: "2026-12-06", Invite: &invitees}, &made)
	var occ []occurrence
	p.as("bob", "GET", "/api/events?from=2026-12-01&to=2026-12-31", nil, &occ)
	if len(occ) != 1 || occ[0].Invite == nil || occ[0].Invite.Status != "NEEDS-ACTION" || occ[0].Invite.Organizer != "Alice" {
		t.Fatalf("bob's invitation: %+v", occ)
	}
	if c := p.as("bob", "POST", fmt.Sprintf("/api/invites/%d", occ[0].Invite.ID), map[string]string{"status": "ACCEPTED"}, nil); c != 200 {
		t.Fatalf("accept: %d", c)
	}
	var guests []guest
	cal, name, _ := strings.Cut(made.ID, "/")
	p.as("alice", "GET", "/api/events/"+cal+"/"+name+"/guests", nil, &guests)
	if len(guests) != 1 || guests[0].Status != "ACCEPTED" {
		t.Errorf("guests: %+v", guests)
	}
	obj, _ := p.s.loadObject("alice", cal, name)
	if a := obj.Events()[0].Props.Get(ical.PropAttendee); a == nil || a.Params.Get(ical.ParamParticipationStatus) != "ACCEPTED" {
		t.Errorf("ATTENDEE in the event: %+v", a)
	}
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'alice' AND text = 'Bob is coming to “Picnic”'`).Scan(&n)
	if n != 1 {
		t.Error("alice wasn't told bob is coming")
	}
}

func TestItemTrash(t *testing.T) {
	r := newRig(t, nil)
	var made struct{ ID string }
	r.call("POST", "/api/events", eventInput{Cal: "personal", Title: "Oops", AllDay: true, Start: "2026-12-05"}, &made)
	r.call("DELETE", "/api/events/"+made.ID, nil, nil)
	var c contact
	r.call("POST", "/api/contacts", contact{Name: "Temp Person"}, &c)
	r.call("DELETE", "/api/contacts/"+c.ID, nil, nil)
	var items []trashedItem
	r.call("GET", "/api/trash/items", nil, &items)
	if len(items) != 2 || items[0].Kind != "contact" || items[1].Title != "Oops" || items[1].Where != "Personal" {
		t.Fatalf("trash: %+v", items)
	}
	for _, it := range items {
		r.call("POST", "/api/trash/items/restore", map[string]string{"id": it.ID}, nil)
	}
	var occ []occurrence
	r.call("GET", "/api/events?from=2026-12-01&to=2026-12-31", nil, &occ)
	var people []contact
	r.call("GET", "/api/contacts", nil, &people)
	if len(occ) != 1 || len(people) != 1 {
		t.Errorf("after restoring: %d events, %d contacts", len(occ), len(people))
	}
}

func TestSearchInsideDocuments(t *testing.T) {
	r := newRig(t, nil)
	var docx bytes.Buffer
	zw := zip.NewWriter(&docx)
	f, _ := zw.Create("word/document.xml")
	f.Write([]byte(`<w:document><w:body><w:p><w:r><w:t>The lighthouse keeper's ledger</w:t></w:r></w:p></w:body></w:document>`))
	zw.Close()
	r.call("PUT", "/api/files/content?path=/Docs/report.docx", docx.Bytes(), nil)
	r.call("PUT", "/api/files/content?path=/Docs/notes.md", "Remember the zeppelin tickets", nil)
	var pdf bytes.Buffer
	var stream bytes.Buffer
	zl := zlib.NewWriter(&stream)
	zl.Write([]byte("BT /F1 12 Tf 72 712 Td (Quarterly aardvark census) Tj ET"))
	zl.Close()
	pdf.WriteString("%PDF-1.4\n1 0 obj << /Length " + fmt.Sprint(stream.Len()) + " /Filter /FlateDecode >>\nstream\n")
	pdf.Write(stream.Bytes())
	pdf.WriteString("\nendstream\nendobj\n%%EOF")
	r.call("PUT", "/api/files/content?path=/Docs/census.pdf", pdf.Bytes(), nil)
	r.call("POST", "/api/tasks", taskInput{List: "tasks", Title: "Buy zeppelin model"}, nil)
	for q, want := range map[string]string{"lighthouse": "/Docs/report.docx", "zeppel": "/Docs/notes.md", "aardvark": "/Docs/census.pdf"} {
		var res struct {
			Files []fileHit
			Tasks []task
		}
		r.call("GET", "/api/search?q="+q, nil, &res)
		if len(res.Files) != 1 || res.Files[0].Path != want || !strings.Contains(res.Files[0].Snippet, "\x01") {
			t.Errorf("search %q: %+v", q, res.Files)
		}
		if q == "zeppel" && len(res.Tasks) != 1 {
			t.Errorf("search %q tasks: %+v", q, res.Tasks)
		}
	}
	// Deleted files leave the index.
	r.call("POST", "/api/files/delete", map[string]any{"paths": []string{"/Docs/report.docx"}}, nil)
	var res struct{ Files []fileHit }
	r.call("GET", "/api/search?q=lighthouse", nil, &res)
	if len(res.Files) != 0 {
		t.Errorf("a deleted file was still found: %+v", res.Files)
	}
}

func TestAdminQuotaQRAndNotes(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	p.s.db.Exec(`UPDATE users SET admin = 1 WHERE name = 'alice'`)
	if c := p.as("bob", "GET", "/api/admin/users", nil, nil); c != 403 {
		t.Errorf("admin page for a non-admin: %d", c)
	}
	if c := p.as("alice", "PUT", "/api/admin/users/bob", map[string]int{"quota": 10}, nil); c != 200 {
		t.Fatalf("set quota: %d", c)
	}
	if c := p.as("bob", "PUT", "/api/files/content?path=/big.txt", strings.Repeat("x", 11), nil); c != 400 {
		t.Errorf("an upload over the limit: %d", c)
	}
	if c := p.as("bob", "POST", "/api/uploads", map[string]any{"path": "/big.bin", "size": 11}, nil); c != 507 {
		t.Errorf("a chunked upload over the limit: %d", c)
	}
	if c := p.as("bob", "PUT", "/api/files/content?path=/small.txt", "12345", nil); c != 201 {
		t.Errorf("an upload within the limit: %d", c)
	}
	var users []struct {
		Name  string
		Used  int64
		Quota int64
	}
	p.as("alice", "GET", "/api/admin/users", nil, &users)
	if len(users) != 2 || users[1].Name != "bob" || users[1].Used != 5 || users[1].Quota != 10 {
		t.Errorf("admin users: %+v", users)
	}
	var sh share
	p.as("bob", "POST", "/api/shares", map[string]any{"path": "/small.txt"}, &sh)
	req, _ := http.NewRequest("GET", p.srv.URL+"/api/shares/"+sh.Token+"/qr", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie["bob"]})
	res, _ := http.DefaultClient.Do(req)
	img, err := png.Decode(res.Body)
	res.Body.Close()
	if err != nil || img.Bounds().Dx() != 512 {
		t.Errorf("QR code: %v", err)
	}
	for range 2 {
		res, _ = http.Get(p.srv.URL + "/s/" + sh.Token + "/content?download=1")
		res.Body.Close()
	}
	var all struct{ Links []share }
	p.as("alice", "GET", "/api/admin/shares", nil, &all)
	if len(all.Links) != 1 || all.Links[0].Downloads != 2 || all.Links[0].Owner != "bob" {
		t.Errorf("links on the admin page: %+v", all.Links)
	}
	// Notes
	var made struct{ Path string }
	p.as("alice", "POST", "/api/notes", map[string]string{"title": "Packing"}, &made)
	p.as("alice", "PUT", "/api/files/content?conflict=replace&path="+url.QueryEscape(made.Path), "# Packing\n\n- [x] socks\n- [ ] hat\n", nil)
	p.as("alice", "POST", "/api/notes", map[string]string{"title": "Ideas"}, nil)
	p.as("alice", "POST", "/api/notes/pin", map[string]any{"path": made.Path, "pinned": true}, nil)
	var notes []note
	p.as("alice", "GET", "/api/notes", nil, &notes)
	if len(notes) != 2 || notes[0].Title != "Packing" || !notes[0].Pinned || notes[0].Done != 1 || notes[0].Total != 2 {
		t.Errorf("notes: %+v", notes)
	}
}

func TestOneOccurrence(t *testing.T) {
	r := newRig(t, nil)
	var made struct{ ID string }
	r.call("POST", "/api/events", eventInput{Cal: "personal", Title: "Swim", Start: "2026-12-01T17:00:00Z", End: "2026-12-01T18:00:00Z",
		TZ: "America/New_York", Repeat: "WEEKLY"}, &made)
	var occ []occurrence
	r.call("GET", "/api/events?from=2026-12-01&to=2026-12-31&tz=UTC", nil, &occ)
	if len(occ) != 5 || occ[1].Instance == "" {
		t.Fatalf("occurrences: %+v", occ)
	}
	second, third := occ[1], occ[2]
	// move only the second, skip only the third
	r.call("PUT", "/api/events/"+made.ID, eventInput{Cal: "personal", Title: "Swim (late)", Start: "2026-12-08T19:00:00Z", End: "2026-12-08T20:00:00Z",
		TZ: "America/New_York", Scope: "this", Instance: second.Instance}, nil)
	r.call("DELETE", "/api/events/"+made.ID+"?scope=this&instance="+url.QueryEscape(third.Instance), nil, nil)
	r.call("GET", "/api/events?from=2026-12-01&to=2026-12-31&tz=UTC", nil, &occ)
	var seen []string
	for _, o := range occ {
		seen = append(seen, o.Title+"@"+o.Start)
	}
	want := "Swim@2026-12-01T12:00:00-05:00,Swim (late)@2026-12-08T14:00:00-05:00,Swim@2026-12-22T12:00:00-05:00,Swim@2026-12-29T12:00:00-05:00"
	if strings.Join(seen, ",") != want {
		t.Errorf("after changing one and skipping one:\n got %s\nwant %s", strings.Join(seen, ","), want)
	}
}

func TestTaskExtras(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	var chores calendarInfo
	p.as("alice", "POST", "/api/calendars", map[string]string{"name": "Chores", "kind": "tasks"}, &chores)
	weekly, bob := "WEEKLY", "bob"
	if c := p.as("alice", "POST", "/api/tasks", taskInput{List: chores.ID, Title: "Bins", Due: "2026-12-01", Repeat: &weekly, Assignee: &bob}, nil); c != 400 {
		t.Errorf("assigning to someone the list isn't shared with: %d", c)
	}
	p.as("alice", "POST", "/api/calendars/"+chores.ID+"/shares", map[string]any{"user": "bob", "edit": true}, nil)
	var bins task
	p.as("alice", "POST", "/api/tasks", taskInput{List: chores.ID, Title: "Bins", Due: "2026-12-01", Repeat: &weekly, Assignee: &bob}, &bins)
	parent := bins.UID
	p.as("alice", "POST", "/api/tasks", taskInput{List: chores.ID, Title: "Rinse the recycling", Parent: &parent}, nil)
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'bob' AND text LIKE 'Alice gave you “Bins”%'`).Scan(&n)
	if n != 1 {
		t.Error("bob wasn't told about his task")
	}
	ref := "alice~" + chores.ID
	_, name, _ := strings.Cut(bins.ID, "/")
	p.as("bob", "PUT", "/api/tasks/"+ref+"/"+name, taskInput{Title: "Bins", Done: true, Due: "2026-12-01"}, nil)
	var tasks []task
	p.as("bob", "GET", "/api/tasks?list="+url.QueryEscape(ref), nil, &tasks)
	var got []string
	for _, t := range tasks {
		got = append(got, fmt.Sprintf("%s|%s|%v|%s|%v", t.Title, t.Due, t.Done, t.Assignee, t.Parent != ""))
	}
	if strings.Join(got, " ; ") != "Bins|2026-12-08|false|bob|false ; Rinse the recycling||false||true" {
		t.Errorf("after ticking off a weekly task: %v", got)
	}
}

func TestSubscriptionsAndFreeBusy(t *testing.T) {
	cal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:h1\r\nDTSTAMP:20260101T000000Z\r\nDTSTART;VALUE=DATE:20261225\r\nSUMMARY:Christmas Day\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	}))
	defer cal.Close()
	p := newPeople(t, nil, "alice", "bob")
	// the server's own addresses are refused
	if c := p.as("alice", "POST", "/api/subscriptions", subscription{Name: "Local", URL: cal.URL}, nil); c != 400 {
		t.Errorf("a subscription to this machine: %d", c)
	}
	old := subClient
	subClient = http.DefaultClient
	defer func() { subClient = old }()
	if c := p.as("alice", "POST", "/api/subscriptions", subscription{Name: "Holidays", URL: strings.Replace(cal.URL, "http://", "webcal://", 1)}, nil); c != 400 {
		// webcal:// means https, which the test server doesn't speak
		t.Logf("webcal: %d", c)
	}
	if c := p.as("alice", "POST", "/api/subscriptions", subscription{Name: "Holidays", URL: cal.URL}, nil); c != 201 {
		t.Fatalf("subscribe: %d", c)
	}
	var occ []occurrence
	p.as("alice", "GET", "/api/events?from=2026-12-01&to=2026-12-31", nil, &occ)
	if len(occ) != 1 || occ[0].Title != "Christmas Day" || !occ[0].ReadOnly {
		t.Errorf("subscribed events: %+v", occ)
	}
	p.as("bob", "POST", "/api/events", eventInput{Cal: "personal", Title: "Private thing", Start: "2026-12-02T15:00:00Z", End: "2026-12-02T16:00:00Z"}, nil)
	var fb map[string][]map[string]string
	p.as("alice", "GET", "/api/freebusy?users=bob&from=2026-12-02T00:00:00Z&to=2026-12-03T00:00:00Z", nil, &fb)
	if len(fb["bob"]) != 1 || fb["bob"][0]["start"] != "2026-12-02T15:00:00Z" {
		t.Errorf("free/busy: %+v", fb)
	}
	b, _ := json.Marshal(fb)
	if strings.Contains(string(b), "Private") {
		t.Error("free/busy showed what the event is")
	}
}

func TestGroupsAndComments(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob", "carol")
	p.s.setGroups("bob", []string{"family"})
	p.as("alice", "PUT", "/api/files/content?path=/Trip/plan.txt", "x", nil)
	if c := p.as("alice", "POST", "/api/group-shares", groupShare{Kind: "file", Ref: "/Trip", Group: "family", Edit: true}, nil); c != 201 {
		t.Fatalf("group share: %d", c)
	}
	var bobs, carols []userShare
	p.as("bob", "GET", "/api/shared-with-me", nil, &bobs)
	if len(bobs) != 1 {
		t.Fatalf("bob (in family): %+v", bobs)
	}
	p.s.setGroups("carol", []string{"family"}) // joins later
	p.as("carol", "GET", "/api/shared-with-me", nil, &carols)
	if len(carols) != 1 {
		t.Errorf("carol joined the group later: %+v", carols)
	}
	p.s.setGroups("bob", []string{}) // leaves
	p.as("bob", "GET", "/api/shared-with-me", nil, &bobs)
	if len(bobs) != 0 {
		t.Errorf("bob left the group: %+v", bobs)
	}
	id := fmt.Sprint(carols[0].ID)
	if c := p.as("carol", "POST", "/api/comments?share="+id, map[string]string{"path": "/plan.txt", "text": "Can @bob drive?"}, nil); c != 201 {
		t.Fatalf("comment: %d", c)
	}
	var cs []comment
	p.as("alice", "GET", "/api/comments?path=/Trip/plan.txt", nil, &cs)
	if len(cs) != 1 || cs[0].Author != "carol" {
		t.Errorf("comments: %+v", cs)
	}
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'bob' AND text LIKE 'Carol mentioned you%'`).Scan(&n)
	var m int
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'alice' AND text LIKE 'Carol commented%'`).Scan(&m)
	if n != 1 || m != 1 {
		t.Errorf("mention %d, owner told %d", n, m)
	}
}

func TestFileAndContactExtras(t *testing.T) {
	r := newRig(t, nil)
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for name, body := range map[string]string{"a.txt": "same", "sub/b.txt": "same", "../../evil.txt": "no", "__MACOSX/._a.txt": "junk"} {
		f, _ := zw.Create(name)
		f.Write([]byte(body))
	}
	zw.Close()
	r.call("PUT", "/api/files/content?path=/pack.zip", zb.Bytes(), nil)
	var out struct {
		Path  string
		Files int
	}
	if c := r.call("POST", "/api/files/unzip", map[string]string{"path": "/pack.zip"}, &out); c != 200 || out.Path != "/pack" || out.Files != 3 {
		t.Fatalf("unzip: %d %+v", c, out)
	}
	if _, err := os.Stat(filepath.Join(r.s.cfg.Data, "files/alice/pack/evil.txt")); err != nil {
		t.Errorf("a ../ path should land inside the folder: %v", err)
	}
	var info struct{ Size, Files int64 }
	r.call("GET", "/api/files/info?path=/pack", nil, &info)
	if info.Files != 3 || info.Size != 10 {
		t.Errorf("folder info: %+v", info)
	}
	var dups []struct{ Files []fileHit }
	r.call("GET", "/api/files/duplicates", nil, &dups)
	if len(dups) != 1 || len(dups[0].Files) != 2 {
		t.Errorf("duplicates: %+v", dups)
	}
	var a, b contact
	r.call("POST", "/api/contacts", contact{Name: "Sam Lee", Emails: []labeled{{"home", "sam@x.org"}}, Groups: []string{"School"}}, &a)
	r.call("POST", "/api/contacts", contact{Name: "Samuel Lee", Emails: []labeled{{"work", "SAM@x.org"}}, Phones: []labeled{{"mobile", "555 0101 000"}}}, &b)
	var groups [][]contact
	r.call("GET", "/api/contacts/duplicates", nil, &groups)
	if len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("contact duplicates: %+v", groups)
	}
	var merged contact
	r.call("POST", "/api/contacts/merge", map[string]any{"ids": []string{a.ID, b.ID}}, &merged)
	var all []contact
	r.call("GET", "/api/contacts", nil, &all)
	if len(all) != 1 || len(all[0].Emails) != 1 || len(all[0].Phones) != 1 || strings.Join(all[0].Groups, ",") != "School" {
		t.Errorf("after merging: %+v", all)
	}
}

func TestActivityUndoTemplatesBackup(t *testing.T) {
	r := newRig(t, nil)
	r.call("PUT", "/api/files/content?path=/keep.txt", "x", nil)
	r.call("POST", "/api/files/delete", map[string]any{"paths": []string{"/keep.txt"}}, nil)
	var made struct{ ID string }
	r.call("POST", "/api/events", eventInput{Cal: "personal", Title: "Before", AllDay: true, Start: "2026-12-05"}, &made)
	r.call("PUT", "/api/events/"+made.ID, eventInput{Cal: "personal", Title: "After", AllDay: true, Start: "2026-12-05"}, nil)
	var acts []struct {
		ID      int64
		Text    string
		CanUndo bool
	}
	r.call("GET", "/api/activity", nil, &acts)
	if len(acts) != 4 || acts[0].Text != "changed “After” in Personal" || acts[2].Text != "deleted “keep.txt”" {
		t.Fatalf("activity: %+v", acts)
	}
	for _, i := range []int{0, 2} {
		if c := r.call("POST", fmt.Sprintf("/api/activity/%d/undo", acts[i].ID), nil, nil); c != 200 {
			t.Errorf("undo %q: %d", acts[i].Text, c)
		}
	}
	if got := r.list("/"); len(got) != 1 || got[0] != "keep.txt" {
		t.Errorf("undoing the delete: %v", got)
	}
	var occ []occurrence
	r.call("GET", "/api/events?from=2026-12-01&to=2026-12-31", nil, &occ)
	if len(occ) != 1 || occ[0].Title != "Before" {
		t.Errorf("undoing the change: %+v", occ)
	}
	var doc struct{ Path string }
	r.call("POST", "/api/files/new", map[string]string{"path": "/", "template": "budget", "name": "Budget"}, &doc)
	b, _ := os.ReadFile(filepath.Join(r.s.cfg.Data, "files/alice/Budget.xlsx"))
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil || doc.Path != "/Budget.xlsx" || len(zr.File) != 5 {
		t.Fatalf("budget template: %v %+v", err, doc)
	}
	var note struct{ Path string }
	r.call("POST", "/api/notes", map[string]string{"title": "Trip", "template": "note-packing"}, &note)
	nb, _ := os.ReadFile(filepath.Join(r.s.cfg.Data, "files/alice/Notes/Trip.md"))
	if !strings.Contains(string(nb), "- [ ] Toothbrush") {
		t.Errorf("packing list: %s", nb)
	}
	if err := r.s.snapshot(time.Now()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(r.s.cfg.Data, "backups/cove-"+time.Now().Format("2006-01-02")+".db")); err != nil || fi.Size() == 0 {
		t.Errorf("snapshot: %v", err)
	}
	res, _ := http.Get(r.srv.URL + "/api/export")
	eb, _ := io.ReadAll(res.Body)
	res.Body.Close()
	ez, err := zip.NewReader(bytes.NewReader(eb), int64(len(eb)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range ez.File {
		names = append(names, f.Name)
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"Files/keep.txt", "Files/Notes/Trip.md", "Calendars/Personal.ics", "Task lists/Tasks.ics", "Contacts.vcf"} {
		if !strings.Contains(joined, want) {
			t.Errorf("export lacks %s: %s", want, joined)
		}
	}
}

func TestFridgeAndLive(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	// bob's open Cove listens
	req, _ := http.NewRequest("GET", p.srv.URL+"/api/live", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie["bob"]})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	got := make(chan string, 20)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := res.Body.Read(buf)
			if err != nil {
				return
			}
			got <- string(buf[:n])
		}
	}()
	time.Sleep(100 * time.Millisecond)
	var list struct{ ID int64 }
	p.as("alice", "POST", "/api/fridge", fridgeInput{Kind: "list", Text: "Shopping", Items: []fridgeItem{{Text: "Milk"}, {Text: "Eggs"}}}, &list)
	deadline := time.After(2 * time.Second)
	var stream string
	for !strings.Contains(stream, "data: fridge") {
		select {
		case s := <-got:
			stream += s
		case <-deadline:
			t.Fatalf("bob's live stream didn't hear about the fridge: %q", stream)
		}
	}
	// bob ticks milk (he can), but can't take alice's list down
	p.as("bob", "PUT", fmt.Sprintf("/api/fridge/%d", list.ID), fridgeInput{Kind: "list", Text: "Bob's now", Items: []fridgeItem{{Text: "Milk", Done: true}, {Text: "Eggs"}}}, nil)
	if c := p.as("bob", "DELETE", fmt.Sprintf("/api/fridge/%d", list.ID), nil, nil); c != 403 {
		t.Errorf("bob took alice's list down: %d", c)
	}
	var notes []fridgeNote
	p.as("alice", "GET", "/api/fridge", nil, &notes)
	if len(notes) != 1 || notes[0].Text != "Shopping" || !notes[0].Items[0].Done || notes[0].Items[0].By != "Bob" {
		t.Errorf("the list after bob ticked milk: %+v", notes)
	}
	var img bytes.Buffer
	png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	req, _ = http.NewRequest("POST", p.srv.URL+"/api/fridge/photo?caption=Hi&expiresDays=1", bytes.NewReader(img.Bytes()))
	req.Header.Set("X-Cove", "1")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie["bob"]})
	pr, _ := http.DefaultClient.Do(req)
	pr.Body.Close()
	if pr.StatusCode != 201 {
		t.Fatalf("photo: %d", pr.StatusCode)
	}
	p.s.dropExpiredFridge(time.Now().Add(48 * time.Hour))
	p.as("alice", "GET", "/api/fridge", nil, &notes)
	if len(notes) != 1 {
		t.Errorf("the photo should have come down after its day: %+v", notes)
	}
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'bob' AND text LIKE 'Alice put a list on the fridge%'`).Scan(&n)
	if n != 1 {
		t.Error("bob wasn't told about the list")
	}
	if c := p.as("bob", "POST", "/api/push/subscribe", map[string]any{"endpoint": "http://not-https", "keys": map[string]string{}}, nil); c != 400 {
		t.Errorf("a bad push subscription: %d", c)
	}
}
