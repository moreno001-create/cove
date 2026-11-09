package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// What the security review found, kept fixed.

func TestChoreTickedOncePerTurn(t *testing.T) {
	p := newPeople(t, nil, "alice", "kid", "sib")
	p.s.db.Exec(`UPDATE users SET kid = 1 WHERE name IN ('kid', 'sib')`)
	var c map[string]any
	p.as("alice", "POST", "/api/hh/chore", chore{Title: "Dishes", Who: []string{"kid"}, Repeat: "daily", Cents: 500}, &c)
	id := int64(c["id"].(float64))
	if code := p.as("sib", "POST", fmt.Sprintf("/api/chores/%d/done", id), nil, nil); code != 403 {
		t.Errorf("a kid ticked someone else's chore: %d", code)
	}
	if code := p.as("kid", "POST", fmt.Sprintf("/api/chores/%d/done", id), nil, nil); code != 201 {
		t.Fatalf("ticking: %d", code)
	}
	for i := 0; i < 3; i++ {
		if code := p.as("kid", "POST", fmt.Sprintf("/api/chores/%d/done", id), nil, nil); code != 409 {
			t.Errorf("ticked again: %d", code)
		}
	}
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM chore_done`).Scan(&n)
	if n != 1 {
		t.Errorf("%d ticks", n)
	}
}

func TestOutsideRequestsStayOutside(t *testing.T) {
	p := newRig(t, nil) // its server is on 127.0.0.1
	for _, c := range []*http.Client{subClient, ntfyClient, pushHTTP} {
		if _, err := c.Get(p.srv.URL + "/healthz"); err == nil || !strings.Contains(err.Error(), "home network") {
			t.Errorf("reached this machine: %v", err)
		}
	}
	for _, target := range []string{"http://192.168.1.1/", "http://10.0.0.1:8123/", "http://100.64.1.1/", "http://169.254.169.254/latest/meta-data"} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
		if _, err := subClient.Do(req); err == nil || !strings.Contains(err.Error(), "home network") {
			t.Errorf("%s: %v", target, err)
		}
		cancel()
	}
	setTrusted("https://cove.home.example.com")
	defer setTrusted("")
	if trustedSuffix != ".home.example.com" {
		t.Errorf("trusted %q", trustedSuffix)
	}
	trustedSuffix = ""
}

func TestOfficeTokenEndsWithTheShare(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	p.s.writeFile("alice", "/Taxes.docx", strings.NewReader("x"), false, time.Time{})
	var sh struct{ ID int64 }
	if c := p.as("alice", "POST", "/api/user-shares", map[string]any{"path": "/Taxes.docx", "user": "bob", "edit": true}, nil); c != 201 {
		t.Fatalf("sharing: %d", c)
	}
	p.s.db.QueryRow(`SELECT id FROM user_shares WHERE target = 'bob'`).Scan(&sh.ID)
	tok := officeToken{Owner: "alice", Path: "Taxes.docx", User: "bob", Write: true, Share: sh.ID, Expires: time.Now().Add(time.Hour).UnixMilli()}
	b, _ := json.Marshal(tok)
	access := p.s.sign(base64.RawURLEncoding.EncodeToString(b))
	info := func() int {
		res, err := http.Get(p.srv.URL + "/wopi/files/" + fileID("alice", "Taxes.docx") + "?access_token=" + access)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := info(); c != 200 {
		t.Fatalf("with the share: %d", c)
	}
	p.as("alice", "DELETE", fmt.Sprintf("/api/user-shares/%d", sh.ID), nil, nil)
	if c := info(); c != 401 {
		t.Errorf("after the share ended: %d", c)
	}
}

func TestCancelUploadStaysInUploads(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	p.s.data.MkdirAll("uploads/alice", 0o750)
	p.s.writeFile("bob", "/Notes.part", strings.NewReader("bob's"), false, time.Time{})
	p.as("alice", "DELETE", "/api/uploads/..%2F..%2Ffiles%2Fbob%2FNotes", nil, nil)
	if _, err := p.s.data.Stat("files/bob/Notes.part"); err != nil {
		t.Errorf("bob's file went: %v", err)
	}
}

func TestShareTargetOnlyFromCove(t *testing.T) {
	p := newPeople(t, nil, "alice")
	req, _ := http.NewRequest("POST", p.srv.URL+"/share-target", strings.NewReader("text=hi"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-site") // a sibling app on the main domain
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie["alice"]})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 403 {
		t.Errorf("another site posted to the share target: %d", res.StatusCode)
	}
}
