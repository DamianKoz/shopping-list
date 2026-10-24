package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testPassword = "correct horse battery"

func newTestApp(t *testing.T) (*app, http.Handler) {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a, err := newApp(db, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return a, a.routes()
}

func sessionFor(a *app) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: a.newSession(time.Now().Add(time.Hour))}
}

func do(t *testing.T, h http.Handler, method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder, want int) T {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d, body %s", rec.Code, want, rec.Body)
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func getLists(t *testing.T, h http.Handler, c *http.Cookie) []list {
	t.Helper()
	return decode[[]list](t, do(t, h, "GET", "/api/lists", "", c), http.StatusOK)
}

func addItem(t *testing.T, h http.Handler, c *http.Cookie, listID int64, name string) item {
	t.Helper()
	return decode[item](t, do(t, h, "POST", "/api/lists/"+itoa(listID)+"/items", `{"name":`+jsonString(name)+`}`, c), http.StatusCreated)
}

func addList(t *testing.T, h http.Handler, c *http.Cookie, name string) list {
	t.Helper()
	return decode[list](t, do(t, h, "POST", "/api/lists", `{"name":`+jsonString(name)+`}`, c), http.StatusCreated)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestItemLifecycle(t *testing.T) {
	a, h := newTestApp(t)
	c := sessionFor(a)

	lists := getLists(t, h, c)
	if len(lists) != 1 || lists[0].Name != "Einkauf" {
		t.Fatalf("expected a default list, got %+v", lists)
	}
	id := lists[0].ID

	milk := addItem(t, h, c, id, "  Milch  ")
	if milk.Name != "Milch" || milk.Completed || milk.ListID != id {
		t.Fatalf("unexpected item %+v", milk)
	}
	bread := addItem(t, h, c, id, "Brot")
	eggs := addItem(t, h, c, id, "Eier")

	if rec := do(t, h, "PATCH", "/api/items/"+itoa(milk.ID), `{"completed":true}`, c); rec.Code != http.StatusOK {
		t.Fatalf("PATCH: status %d", rec.Code)
	}
	items := getLists(t, h, c)[0].Items
	if len(items) != 3 || items[0].ID != bread.ID || items[1].ID != eggs.ID || !items[2].Completed {
		t.Fatalf("open items should come first, got %+v", items)
	}

	// Reactivate and complete another one.
	do(t, h, "PATCH", "/api/items/"+itoa(milk.ID), `{"completed":false}`, c)
	do(t, h, "PATCH", "/api/items/"+itoa(eggs.ID), `{"completed":true}`, c)

	if rec := do(t, h, "DELETE", "/api/items/"+itoa(bread.ID), "", c); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d", rec.Code)
	}
	if rec := do(t, h, "DELETE", "/api/items/"+itoa(bread.ID), "", c); rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE: status %d, want 404", rec.Code)
	}

	if rec := do(t, h, "DELETE", "/api/lists/"+itoa(id)+"/items/completed", "", c); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE completed: status %d", rec.Code)
	}
	items = getLists(t, h, c)[0].Items
	if len(items) != 1 || items[0].ID != milk.ID || items[0].Completed {
		t.Fatalf("only the reactivated item should remain, got %+v", items)
	}
}

func TestMultipleLists(t *testing.T) {
	a, h := newTestApp(t)
	c := sessionFor(a)
	shop := getLists(t, h, c)[0]
	dm := addList(t, h, c, "dm")

	addItem(t, h, c, shop.ID, "Milch")
	soap := addItem(t, h, c, dm.ID, "Seife")
	do(t, h, "PATCH", "/api/items/"+itoa(soap.ID), `{"completed":true}`, c)
	addItem(t, h, c, shop.ID, "Brot")

	// Clearing completed items only affects that list.
	do(t, h, "PATCH", "/api/items/1", `{"completed":true}`, c)
	do(t, h, "DELETE", "/api/lists/"+itoa(dm.ID)+"/items/completed", "", c)
	lists := getLists(t, h, c)
	if len(lists) != 2 || len(lists[0].Items) != 2 || len(lists[1].Items) != 0 {
		t.Fatalf("unexpected lists %+v", lists)
	}

	addItem(t, h, c, dm.ID, "Zahnpasta")
	if rec := do(t, h, "DELETE", "/api/lists/"+itoa(dm.ID), "", c); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE list: status %d", rec.Code)
	}
	var orphans int
	a.db.QueryRow(`SELECT COUNT(*) FROM items WHERE list_id = ?`, dm.ID).Scan(&orphans)
	if orphans != 0 {
		t.Fatalf("deleting a list left %d items behind", orphans)
	}

	if rec := do(t, h, "DELETE", "/api/lists/"+itoa(shop.ID), "", c); rec.Code != http.StatusConflict {
		t.Fatalf("deleting the last list: status %d, want 409", rec.Code)
	}
	if rec := do(t, h, "POST", "/api/lists/"+itoa(dm.ID)+"/items", `{"name":"x"}`, c); rec.Code != http.StatusNotFound {
		t.Fatalf("adding to a deleted list: status %d, want 404", rec.Code)
	}
}

func TestMigratesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(schema + `INSERT INTO items (name, completed) VALUES ('Milch', 0), ('Brot', 1);`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := newApp(db, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	lists := getLists(t, a.routes(), sessionFor(a))
	if len(lists) != 1 || lists[0].Name != "Einkauf" || len(lists[0].Items) != 2 {
		t.Fatalf("existing items should move to the default list, got %+v", lists)
	}

	// Opening again must not re-run the migration.
	db.Close()
	db, err = openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func TestInvalidInput(t *testing.T) {
	a, h := newTestApp(t)
	c := sessionFor(a)
	it := addItem(t, h, c, 1, strings.Repeat("ä", maxNameLen)) // runes, not bytes

	tests := []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/lists/1/items", `{"name":""}`, http.StatusBadRequest},
		{"POST", "/api/lists/1/items", `{"name":"   "}`, http.StatusBadRequest},
		{"POST", "/api/lists/1/items", `{"name":"` + strings.Repeat("a", maxNameLen+1) + `"}`, http.StatusBadRequest},
		{"POST", "/api/lists/1/items", `not json`, http.StatusBadRequest},
		{"POST", "/api/lists/999/items", `{"name":"x"}`, http.StatusNotFound},
		{"POST", "/api/lists", `{"name":" "}`, http.StatusBadRequest},
		{"PATCH", "/api/items/" + itoa(it.ID), `{}`, http.StatusBadRequest},
		{"PATCH", "/api/items/abc", `{"completed":true}`, http.StatusBadRequest},
		{"PATCH", "/api/items/9999", `{"completed":true}`, http.StatusNotFound},
		{"DELETE", "/api/items/-1", "", http.StatusBadRequest},
		{"DELETE", "/api/lists/999", "", http.StatusNotFound},
	}
	for _, tt := range tests {
		if rec := do(t, h, tt.method, tt.path, tt.body, c); rec.Code != tt.want {
			t.Errorf("%s %s %s: status %d, want %d", tt.method, tt.path, tt.body, rec.Code, tt.want)
		}
	}
	if lists := getLists(t, h, c); len(lists) != 1 || len(lists[0].Items) != 1 {
		t.Errorf("invalid requests changed the data: %+v", lists)
	}
}

func TestRequiresAuth(t *testing.T) {
	a, h := newTestApp(t)

	expired := &http.Cookie{Name: sessionCookie, Value: a.newSession(time.Now().Add(-time.Minute))}
	forged := &http.Cookie{Name: sessionCookie, Value: "9999999999.forged"}
	for _, c := range []*http.Cookie{nil, expired, forged} {
		for _, r := range []struct{ method, path, body string }{
			{"GET", "/api/lists", ""},
			{"POST", "/api/lists", `{"name":"x"}`},
			{"DELETE", "/api/lists/1", ""},
			{"POST", "/api/lists/1/items", `{"name":"x"}`},
			{"DELETE", "/api/lists/1/items/completed", ""},
			{"PATCH", "/api/items/1", `{"completed":true}`},
			{"DELETE", "/api/items/1", ""},
			{"GET", "/api/events", ""},
		} {
			if rec := do(t, h, r.method, r.path, r.body, c); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with cookie %v: status %d, want 401", r.method, r.path, c, rec.Code)
			}
		}
	}

	rec := do(t, h, "GET", "/", "", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("GET / without session: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do(t, h, "GET", "/index.html", "", nil); rec.Code == http.StatusOK {
		t.Error("index.html must not be reachable without a session")
	}
}

func TestLogin(t *testing.T) {
	a, h := newTestApp(t)

	login := func(pw string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"password": {pw}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := login(testPassword)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login: status %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected cookies %+v", cookies)
	}
	if strings.Contains(cookies[0].Value, testPassword) {
		t.Fatal("cookie contains the password")
	}
	if rec := do(t, h, "GET", "/api/lists", "", cookies[0]); rec.Code != http.StatusOK {
		t.Fatalf("session from login rejected: status %d", rec.Code)
	}

	for range maxLoginFails {
		if rec := login("wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password: status %d", rec.Code)
		}
	}
	if rec := login(testPassword); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: status %d, want 429", maxLoginFails, rec.Code)
	}

	// Changing the password invalidates existing sessions.
	b, err := newApp(a.db, "another long password")
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, b.routes(), "GET", "/api/lists", "", cookies[0]); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old session after password change: status %d", rec.Code)
	}
}

func TestRejectsCrossOriginWrites(t *testing.T) {
	a, h := newTestApp(t)
	req := httptest.NewRequest("POST", "/api/lists", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(sessionFor(a))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: status %d, want 403", rec.Code)
	}
}

func TestEventsNotifyOnChange(t *testing.T) {
	a, h := newTestApp(t)
	srv := httptest.NewServer(h)
	defer srv.Close()
	c := sessionFor(a)

	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	req.AddCookie(c)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	// Wait for the initial retry line so the subscription is registered.
	if l := <-lines; !strings.HasPrefix(l, "retry:") {
		t.Fatalf("first line %q", l)
	}

	addItem(t, h, c, 1, "Kaffee")

	timeout := time.After(2 * time.Second)
	for {
		select {
		case l := <-lines:
			if l == "data: changed" {
				return
			}
		case <-timeout:
			t.Fatal("no change event received")
		}
	}
}
