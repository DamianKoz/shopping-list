// Command shopping-list serves shared shopping lists for two people.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

//go:embed static
var staticFiles embed.FS

const (
	maxNameLen    = 200
	sessionCookie = "session"
	sessionMaxAge = 365 * 24 * time.Hour
	loginWindow   = time.Minute
	maxLoginFails = 5
	pingInterval  = 25 * time.Second
)

const schema = `
CREATE TABLE IF NOT EXISTS items (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	completed INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`

// migrations run once each, in order; PRAGMA user_version counts the applied ones.
var migrations = []string{
	`CREATE TABLE lists (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	INSERT INTO lists (id, name) VALUES (1, 'Einkauf');
	ALTER TABLE items ADD COLUMN list_id INTEGER NOT NULL DEFAULT 1;
	CREATE INDEX items_list_id ON items (list_id);`,
}

type list struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Items []item `json:"items"`
}

type item struct {
	ID        int64  `json:"id"`
	ListID    int64  `json:"list_id"`
	Name      string `json:"name"`
	Completed bool   `json:"completed"`
	CreatedAt string `json:"created_at"`
}

const itemColumns = `id, list_id, name, completed, created_at`

func scanItem(row interface{ Scan(...any) error }) (item, error) {
	var it item
	err := row.Scan(&it.ID, &it.ListID, &it.Name, &it.Completed, &it.CreatedAt)
	return it, err
}

type app struct {
	db         *sql.DB
	password   string
	sessionKey []byte
	events     *hub
	logins     *loginLimiter
}

func main() {
	addr := env("LISTEN_ADDR", "127.0.0.1:4002")
	dbPath := env("DB_PATH", "shop.db")
	password := os.Getenv("APP_PASSWORD")
	if password == "" {
		log.Fatal("APP_PASSWORD must be set")
	}

	db, err := openDB(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	a, err := newApp(db, password)
	if err != nil {
		log.Fatal(err)
	}

	// Cancelled on shutdown so that open SSE streams return and don't block it.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	srv := &http.Server{
		Addr:              addr,
		Handler:           a.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	srv.RegisterOnShutdown(cancelBase)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		log.Printf("listening on %s, database %s", addr, dbPath)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		log.Printf("server: %v", err)
		return
	case <-ctx.Done():
	}

	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// A single connection serializes writes, which is plenty for two users.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}

func newApp(db *sql.DB, password string) (*app, error) {
	secret, err := sessionSecret(db)
	if err != nil {
		return nil, err
	}
	// Deriving the key from the password means changing it logs everyone out.
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(password))
	return &app{
		db:         db,
		password:   password,
		sessionKey: mac.Sum(nil),
		events:     newHub(),
		logins:     &loginLimiter{},
	}, nil
}

// sessionSecret returns a random secret that is created once and kept in the
// database, so sessions survive restarts.
func sessionSecret(db *sql.DB) ([]byte, error) {
	b := make([]byte, 32)
	rand.Read(b)
	if _, err := db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES ('session_secret', ?)`, hex.EncodeToString(b)); err != nil {
		return nil, fmt.Errorf("store session secret: %w", err)
	}
	var s string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'session_secret'`).Scan(&s); err != nil {
		return nil, fmt.Errorf("load session secret: %w", err)
	}
	return hex.DecodeString(s)
}

func (a *app) routes() http.Handler {
	static, _ := fs.Sub(staticFiles, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /{$}", a.index)
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("GET /api/lists", a.requireAuth(a.getLists))
	mux.HandleFunc("POST /api/lists", a.requireAuth(a.addList))
	mux.HandleFunc("DELETE /api/lists/{id}", a.requireAuth(a.deleteList))
	mux.HandleFunc("POST /api/lists/{id}/items", a.requireAuth(a.addItem))
	mux.HandleFunc("DELETE /api/lists/{id}/items/completed", a.requireAuth(a.deleteCompleted))
	mux.HandleFunc("PATCH /api/items/{id}", a.requireAuth(a.updateItem))
	mux.HandleFunc("DELETE /api/items/{id}", a.requireAuth(a.deleteItem))
	mux.HandleFunc("GET /api/events", a.requireAuth(a.streamEvents))

	// Rejects cross-site writes based on Sec-Fetch-Site and Origin (CSRF).
	return securityHeaders(http.NewCrossOriginProtection().Handler(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// --- Pages and authentication ---

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	if !a.authenticated(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.ServeFileFS(w, r, staticFiles, "static/index.html")
}

func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	if a.authenticated(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	renderLogin(w, http.StatusOK, "")
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	if !a.logins.allow() {
		renderLogin(w, http.StatusTooManyRequests, "Zu viele Versuche. Bitte in einer Minute erneut probieren.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !a.checkPassword(r.PostFormValue("password")) {
		a.logins.fail()
		log.Printf("failed login attempt (forwarded for %q)", r.Header.Get("X-Forwarded-For"))
		renderLogin(w, http.StatusUnauthorized, "Falsches Passwort.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.newSession(time.Now().Add(sessionMaxAge)),
		Path:     "/",
		MaxAge:   int(sessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func renderLogin(w http.ResponseWriter, status int, msg string) {
	page, _ := staticFiles.ReadFile("static/login.html")
	if msg != "" {
		page = bytes.Replace(page, []byte("<!--error-->"), []byte(`<p class="error" role="alert">`+msg+`</p>`), 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(page)
}

func (a *app) checkPassword(pw string) bool {
	got := sha256.Sum256([]byte(pw))
	want := sha256.Sum256([]byte(a.password))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// A session is "<expiry unix>.<HMAC of expiry>", so the server can validate it
// without storing anything.
func (a *app) newSession(expires time.Time) string {
	exp := strconv.FormatInt(expires.Unix(), 10)
	return exp + "." + a.sign(exp)
}

func (a *app) sign(s string) string {
	mac := hmac.New(sha256.New, a.sessionKey)
	mac.Write([]byte(s))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *app) authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	exp, sig, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(a.sign(exp))) {
		return false
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && time.Now().Unix() < unix
}

func (a *app) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			writeError(w, http.StatusUnauthorized, "Nicht angemeldet.")
			return
		}
		next(w, r)
	}
}

// loginLimiter allows a few failed logins per minute in total. With only two
// users a global limit is enough and avoids trusting proxy headers for IPs.
type loginLimiter struct {
	mu    sync.Mutex
	fails []time.Time
}

func (l *loginLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-loginWindow)
	for len(l.fails) > 0 && l.fails[0].Before(cutoff) {
		l.fails = l.fails[1:]
	}
	return len(l.fails) < maxLoginFails
}

func (l *loginLimiter) fail() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails = append(l.fails, time.Now())
}

// --- API ---

// getLists returns all lists with their items, so clients can switch lists
// instantly and still show everything when offline.
func (a *app) getLists(w http.ResponseWriter, r *http.Request) {
	lists, err := a.loadLists(r.Context())
	if err != nil {
		serverError(w, "load lists", err)
		return
	}
	writeJSON(w, http.StatusOK, lists)
}

func (a *app) loadLists(ctx context.Context) ([]list, error) {
	// A transaction gives a consistent snapshot across both queries.
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM lists ORDER BY id`)
	if err != nil {
		return nil, err
	}
	lists := []list{}
	index := map[int64]int{}
	for rows.Next() {
		l := list{Items: []item{}}
		if err := rows.Scan(&l.ID, &l.Name); err != nil {
			rows.Close()
			return nil, err
		}
		index[l.ID] = len(lists)
		lists = append(lists, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT `+itemColumns+` FROM items ORDER BY completed, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		if i, ok := index[it.ListID]; ok {
			lists[i].Items = append(lists[i].Items, it)
		}
	}
	return lists, rows.Err()
}

func (a *app) addList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, ok := validName(w, req.Name)
	if !ok {
		return
	}

	l := list{Items: []item{}}
	err := a.db.QueryRowContext(r.Context(),
		`INSERT INTO lists (name) VALUES (?) RETURNING id, name`, name,
	).Scan(&l.ID, &l.Name)
	if err != nil {
		serverError(w, "add list", err)
		return
	}
	a.events.broadcast()
	writeJSON(w, http.StatusCreated, l)
}

func (a *app) deleteList(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, "delete list", err)
		return
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(r.Context(), `DELETE FROM lists WHERE id = ?`, id)
	if err != nil {
		serverError(w, "delete list", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "Liste nicht gefunden.")
		return
	}
	var remaining int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM lists`).Scan(&remaining); err != nil {
		serverError(w, "delete list", err)
		return
	}
	if remaining == 0 {
		writeError(w, http.StatusConflict, "Die letzte Liste kann nicht gelöscht werden.")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM items WHERE list_id = ?`, id); err != nil {
		serverError(w, "delete list items", err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, "delete list", err)
		return
	}
	a.events.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) addItem(w http.ResponseWriter, r *http.Request) {
	listID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, ok := validName(w, req.Name)
	if !ok {
		return
	}

	// Selecting from lists makes the insert a no-op if the list doesn't exist.
	it, err := scanItem(a.db.QueryRowContext(r.Context(),
		`INSERT INTO items (name, list_id) SELECT ?, id FROM lists WHERE id = ? RETURNING `+itemColumns, name, listID))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Liste nicht gefunden.")
		return
	}
	if err != nil {
		serverError(w, "add item", err)
		return
	}
	a.events.broadcast()
	writeJSON(w, http.StatusCreated, it)
}

func (a *app) updateItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Completed *bool `json:"completed"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Completed == nil {
		writeError(w, http.StatusBadRequest, "Feld \"completed\" fehlt.")
		return
	}

	it, err := scanItem(a.db.QueryRowContext(r.Context(),
		`UPDATE items SET completed = ? WHERE id = ? RETURNING `+itemColumns, *req.Completed, id))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Artikel nicht gefunden.")
		return
	}
	if err != nil {
		serverError(w, "update item", err)
		return
	}
	a.events.broadcast()
	writeJSON(w, http.StatusOK, it)
}

func (a *app) deleteItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	res, err := a.db.ExecContext(r.Context(), `DELETE FROM items WHERE id = ?`, id)
	if err != nil {
		serverError(w, "delete item", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "Artikel nicht gefunden.")
		return
	}
	a.events.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) deleteCompleted(w http.ResponseWriter, r *http.Request) {
	listID, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM items WHERE list_id = ? AND completed = 1`, listID); err != nil {
		serverError(w, "delete completed items", err)
		return
	}
	a.events.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

// streamEvents sends a "changed" event whenever anything changes. Clients
// then reload via GET /api/lists.
func (a *app) streamEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// The stream stays open indefinitely, so lift the server's timeouts.
	rc.SetReadDeadline(time.Time{})
	rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := a.events.subscribe()
	defer a.events.unsubscribe(ch)
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	fmt.Fprint(w, "retry: 3000\n\n")
	for {
		if err := rc.Flush(); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			fmt.Fprint(w, "data: changed\n\n")
		case <-ping.C:
			// Keeps proxies and mobile networks from closing an idle connection.
			fmt.Fprint(w, ": ping\n\n")
		}
	}
}

// hub notifies all connected SSE clients about changes.
type hub struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func newHub() *hub {
	return &hub{clients: make(map[chan struct{}]struct{})}
}

func (h *hub) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan struct{}) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

func (h *hub) broadcast() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		// A pending notification already tells the client to reload.
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// --- Helpers ---

// validName normalizes whitespace and checks the length of a list or item name.
func validName(w http.ResponseWriter, raw string) (string, bool) {
	name := strings.Join(strings.Fields(raw), " ")
	if name == "" {
		writeError(w, http.StatusBadRequest, "Der Name darf nicht leer sein.")
		return "", false
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Der Name darf höchstens %d Zeichen lang sein.", maxNameLen))
		return "", false
	}
	return name, true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "Ungültige ID.")
		return 0, false
	}
	return id, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, what string, err error) {
	log.Printf("%s: %v", what, err)
	writeError(w, http.StatusInternalServerError, "Interner Fehler.")
}
