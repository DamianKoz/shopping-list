# Einkaufsliste

Gemeinsame Einkaufslisten für zwei Personen. Die App läuft als einzelnes
Go-Binary mit SQLite-Datenbank, synchronisiert Änderungen live zwischen allen
geöffneten Geräten und lässt sich auf iOS und Android wie eine native App zum
Home-Bildschirm hinzufügen.

Live unter <https://einkauf.dmko.org>.

**Inhalt**

1. [Was die App kann](#was-die-app-kann)
2. [Architektur](#architektur)
3. [Datenmodell](#datenmodell)
4. [API](#api)
5. [Synchronisierung](#synchronisierung)
6. [Offline-Verhalten](#offline-verhalten)
7. [Sicherheit](#sicherheit)
8. [Oberfläche](#oberfläche)
9. [Design-Entscheidungen](#design-entscheidungen)
10. [Lokal entwickeln](#lokal-entwickeln)
11. [Konfiguration](#konfiguration)
12. [Deployment](#deployment)
13. [Betrieb](#betrieb)
14. [Grenzen](#grenzen)

---

## Was die App kann

- **Mehrere Listen**, zum Beispiel „Einkauf“ und „dm“. Ein Tipp auf den Titel
  öffnet die Übersicht aller Listen mit der Zahl offener Artikel. Dort legt man
  neue Listen an oder löscht sie. Die letzte Liste lässt sich nicht löschen.
- **Artikel hinzufügen** über das Eingabefeld, mit Enter oder dem +-Button.
- **Abhaken und wieder aktivieren** über große Checkboxen.
- **Aktionsmenü pro Artikel** (…): „Ganz nach oben“, „Verschieben nach …“ in
  eine andere Liste und „Löschen“.
- **Erledigte Artikel** stehen in einem eigenen Bereich unter den offenen,
  durchgestrichen und grau. Der Bereich lässt sich auf- und zuklappen.
  „Erledigte entfernen“ löscht alle erledigten Artikel der aktuellen Liste.
- **Live-Synchronisierung**: Änderungen erscheinen auf allen offenen Geräten
  nahezu sofort.
- **Nur lesen ohne Verbindung**: Ohne Netz zeigt die App den zuletzt
  geladenen Stand aller Listen an, Änderungen sind dann gesperrt.
- **Heller und dunkler Modus**: folgt dem System und lässt sich oben rechts
  umschalten.
- **Ein gemeinsames Passwort** schützt den Zugang. Nach dem Login bleibt man
  ein Jahr angemeldet.

Bewusst nicht enthalten sind Mengen, Kategorien, Preise, Benutzerkonten,
Freigaben, freies Sortieren per Drag & Drop und Offline-Bearbeitung.

---

## Architektur

```
 iPhone / Android / Desktop                      Server (Ubuntu, ARM64)
┌──────────────────────────┐              ┌──────────────────────────────────┐
│ PWA (Vanilla JS)         │    HTTPS     │ Caddy  :443                      │
│  app.js   ── fetch ──────┼─────────────►│  einkauf.dmko.org                │
│  EventSource ◄── SSE ────┼──────────────┤   └─ reverse_proxy 127.0.0.1:4002│
│  sw.js (App-Shell-Cache) │              │                                  │
│  localStorage (Kopie)    │              │ shopping-list (systemd)          │
└──────────────────────────┘              │  net/http + go:embed static/     │
                                          │  SQLite  ~/shopping-list/shop.db │
                                          └──────────────────────────────────┘
```

Es gibt genau einen Prozess. Er liefert die statischen Dateien aus (per
`go:embed` im Binary enthalten), stellt die JSON-API bereit, hält die
SSE-Verbindungen offen und schreibt in SQLite. Caddy übernimmt davor nur HTTPS
und die Zertifikate.

### Dateien

```
shopping-list/
├── main.go               Backend: Routing, Auth, API, SSE, Datenbank, Migrationen
├── main_test.go          Gezielte Tests für API, Auth und Migration
├── go.mod / go.sum       Einzige externe Abhängigkeit: modernc.org/sqlite
├── static/               Wird ins Binary eingebettet
│   ├── index.html        Die einzige App-Ansicht (Liste, Listen-Übersicht, Aktionsmenü)
│   ├── login.html        Login-Seite mit einem Passwortfeld
│   ├── app.js            Gesamte Frontend-Logik
│   ├── theme.js          Hell/Dunkel, wird vor dem ersten Rendern geladen
│   ├── style.css         Gesamtes Styling inkl. Dark Mode
│   ├── sw.js             Service Worker (nur App-Shell, nie die API)
│   ├── manifest.json     PWA-Manifest
│   └── icons/            App-Icons (PNG, 180/192/512 px)
├── shopping-list.service systemd-Unit für den Server
├── deploy.sh             Bauen, hochladen, neu starten
└── README.md
```

`main.go` ist in Abschnitte gegliedert: Setup und Migrationen, Seiten und
Authentifizierung, API, SSE-Hub, Hilfsfunktionen. Es gibt keine Service-,
Repository- oder Controller-Schichten. Jeder Handler führt seine SQL-Queries
direkt aus.

---

## Datenmodell

```sql
lists    (id, name, created_at)
items    (id, list_id, name, completed, position, created_at)
settings (key, value)        -- enthält nur das Session-Secret
```

- `items.list_id` verweist auf eine Liste. Löscht man eine Liste, löscht
  dieselbe Transaktion ihre Artikel mit.
- `items.position` bestimmt die Reihenfolge innerhalb einer Liste. Neue und
  verschobene Artikel bekommen `MAX(position) + 1`, also das Ende. „Ganz nach
  oben“ setzt `MIN(position) - 1`. Bei gleicher Position entscheidet die `id`.
  So braucht es nie ein Umnummerieren anderer Artikel.
- Sortierung: erst offene, dann erledigte Artikel, jeweils nach
  `position, id`.

### Migrationen

Das Schema entwickelt sich über eine Liste von SQL-Schritten in `main.go`
(`migrations`). SQLite speichert in `PRAGMA user_version`, wie viele davon
schon angewendet sind. Beim Start führt die App nur die fehlenden aus, jeden
Schritt in einer eigenen Transaktion. Eine neue Datenbank durchläuft alle
Schritte, eine bestehende nur die neuen.

| Version | Änderung                                                       |
|---------|----------------------------------------------------------------|
| 1       | Tabelle `lists`, Liste „Einkauf“ (id 1), `items.list_id` = 1   |
| 2       | `items.position`                                               |

Für eine neue Änderung hängt man einen Eintrag ans Ende von `migrations` an.
Bestehende Einträge ändert man nie.

### SQLite-Einstellungen

- Treiber `modernc.org/sqlite`: reines Go, kein C-Compiler nötig.
- `journal_mode=WAL` und `busy_timeout=5000` per DSN-Pragma.
- `SetMaxOpenConns(1)`: Alle Zugriffe laufen über eine Verbindung. Schreib-
  konflikte (`SQLITE_BUSY`) entstehen so gar nicht erst, für zwei Nutzer ist
  das mehr als schnell genug.
- Ausschließlich parametrisierte Queries.

---

## API

Alle Endpunkte unter `/api/` verlangen eine gültige Session und antworten mit
JSON. Fehler haben die Form `{"error": "…"}` mit einer deutschen Meldung, die
das Frontend direkt anzeigt.

| Methode  | Pfad                               | Zweck                                         | Erfolg |
|----------|------------------------------------|-----------------------------------------------|--------|
| `GET`    | `/api/lists`                       | Alle Listen inklusive ihrer Artikel           | 200    |
| `POST`   | `/api/lists`                       | Liste anlegen `{"name"}`                      | 201    |
| `DELETE` | `/api/lists/{id}`                  | Liste samt Artikeln löschen                   | 204    |
| `POST`   | `/api/lists/{id}/items`            | Artikel hinzufügen `{"name"}`                 | 201    |
| `DELETE` | `/api/lists/{id}/items/completed`  | Erledigte Artikel der Liste löschen           | 204    |
| `PATCH`  | `/api/items/{id}`                  | `{"completed"}`, `{"list_id"}`, `{"to_top"}`  | 200    |
| `DELETE` | `/api/items/{id}`                  | Artikel löschen                               | 204    |
| `GET`    | `/api/events`                      | SSE-Stream mit Änderungs-Signalen             | 200    |

Validierung: Namen werden getrimmt, Mehrfach-Leerzeichen und Zeilenumbrüche
zusammengefasst. Leere Namen und Namen über 200 Zeichen ergeben `400`.
Request-Bodys sind auf 4 KB begrenzt. Unbekannte IDs ergeben `404`. Das
Löschen der letzten Liste ergibt `409`.

Außerhalb der API:

| Methode | Pfad     | Zweck                                             |
|---------|----------|---------------------------------------------------|
| `GET`   | `/`      | App, ohne Session Weiterleitung auf `/login`      |
| `GET`   | `/login` | Login-Seite                                       |
| `POST`  | `/login` | Passwort prüfen, Session-Cookie setzen            |
| `GET`   | sonst    | Statische Dateien (CSS, JS, Manifest, Icons, SW)  |

---

## Synchronisierung

Das Prinzip ist bewusst einfach: **Der Server sagt nur „etwas hat sich
geändert“, der Client lädt dann den kompletten Stand neu.**

1. Jeder Client hält eine `EventSource`-Verbindung auf `/api/events`.
2. Nach jeder erfolgreichen Änderung ruft der Server `broadcast()` auf. Jeder
   verbundene Client bekommt `data: changed`.
3. Der Client lädt daraufhin `GET /api/lists`. Das sind alle Listen mit allen
   Artikeln in einer einzigen, in sich konsistenten Antwort, gelesen in einer
   Transaktion.

Dass immer alles geladen wird, ist bei ein paar Dutzend Artikeln billiger als
jede Delta-Logik und schließt Inkonsistenzen aus.

### Details, die das robust machen

- **Eigene Änderungen sofort sichtbar:** Nach der Server-Bestätigung wendet
  der Client die Änderung lokal an (`applyLocal`) und lädt danach trotzdem neu.
- **Veraltete Antworten gewinnen nie:** Ein Zähler `seq` wird bei jedem Laden
  und jeder lokalen Änderung erhöht. Eine Lade-Antwort wird nur übernommen,
  wenn seit ihrem Start nichts Neueres passiert ist.
- **Zusammenfassen:** Der Kanal pro Client hat Puffergröße 1. Kommen mehrere
  Änderungen schnell hintereinander, wird nur einmal neu geladen.
- **Heartbeat:** Alle 25 Sekunden ein SSE-Kommentar (`: ping`), damit Proxys
  und Mobilfunknetze die Verbindung nicht als inaktiv schließen.
- **Timeouts:** Der HTTP-Server hat normale Read/Write-Timeouts. Nur für den
  SSE-Request hebt `http.ResponseController` sie auf.
- **Reconnect:** `EventSource` verbindet sich selbst neu (`retry: 3000`). Lehnt
  der Server den Stream ab, etwa bei abgelaufener Session, prüft der Client per
  `load()` und landet gegebenenfalls auf dem Login.
- **App wieder im Vordergrund:** iOS kappt Verbindungen im Hintergrund oft
  stillschweigend. Bei `visibilitychange`, `pageshow` und `online` baut der
  Client die Verbindung deshalb immer neu auf und lädt den Stand.
- **Sauberes Beenden:** Die SSE-Handler hängen am `BaseContext` des Servers.
  Bei SIGTERM wird er abgebrochen, offene Streams enden sofort und der
  Shutdown wartet nicht auf sie.

---

## Offline-Verhalten

Die App braucht für Änderungen eine Verbindung und täuscht nie Erfolg vor.

- **Statusanzeige:** Ist die Live-Verbindung länger als 2 Sekunden weg,
  erscheint „Keine Verbindung – nur lesen“. Checkboxen und Buttons werden
  deaktiviert. Die Verzögerung verhindert Flackern bei kurzen Reconnects.
- **Fehlgeschlagene Aktionen:** API-Aufrufe brechen nach 8 Sekunden ab. Schlägt
  eine Aktion fehl, bleibt die Liste unverändert, eine Fehlermeldung erscheint,
  und der eingetippte Text bleibt im Eingabefeld.
- **Letzter Stand:** Jede erfolgreich geladene Antwort landet in
  `localStorage`. Beim Öffnen zeigt die App sofort diesen Stand und
  aktualisiert dann.
- **Service Worker** (`sw.js`): Er holt HTML, CSS, JS und Icons immer zuerst aus
  dem Netz („network first“). Antwortet das Netz nicht innerhalb von 4
  Sekunden, nimmt er die zwischengespeicherte Version. So öffnet sich die App
  auch ohne Empfang im Supermarkt. `/api/` und `/login` fasst er nie an, die
  API wird also nie aus einem Cache beantwortet.

---

## Sicherheit

Die App ist öffentlich erreichbar, aber ohne Passwort sieht man nur die
Login-Seite.

- **Gemeinsames Passwort** aus der Umgebungsvariable `APP_PASSWORD`. Der
  Vergleich läuft in konstanter Zeit über SHA-256-Hashes, damit die Laufzeit
  nichts über das Passwort verrät.
- **Session-Cookie** `session` mit dem Inhalt `<Ablaufzeit>.<HMAC-SHA256>`.
  Der Server prüft Signatur und Ablauf, ohne Sessions zu speichern. Das
  Passwort steht nie im Cookie oder im Frontend.
  - Der HMAC-Schlüssel wird aus einem zufälligen Secret (beim ersten Start
    erzeugt, in `settings` gespeichert) und dem Passwort abgeleitet. Sessions
    überleben dadurch Neustarts. **Ändert man das Passwort, sind alle Sessions
    sofort ungültig.**
  - Attribute: `HttpOnly`, `SameSite=Lax`, `Secure` hinter HTTPS (erkannt an
    `X-Forwarded-Proto` von Caddy), Laufzeit 365 Tage.
- **CSRF:** `http.CrossOriginProtection` aus der Go-Standardbibliothek lehnt
  schreibende Requests von fremden Seiten ab (über `Sec-Fetch-Site` und
  `Origin`). Zusätzlich schützt `SameSite=Lax`.
- **Login-Sperre:** Höchstens 5 Fehlversuche pro Minute, global für alle.
  Danach kommt `429`. Global statt pro IP, weil es nur zwei Nutzer gibt und so
  keine Proxy-Header ausgewertet werden müssen. Fehlversuche werden geloggt.
- **Security-Header:** strikte `Content-Security-Policy` (`default-src 'self'`,
  kein Inline-Script, kein Framing), `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: same-origin`. API-Antworten sind `no-store`, alles andere
  `no-cache`.
- **XSS:** Das Frontend setzt Nutzertexte nur über `textContent`, nie als HTML.
- **Netzwerk:** Die App lauscht nur auf `127.0.0.1`. Von außen ist sie nur über
  Caddy erreichbar.
- **systemd-Härtung:** `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict`.
  Schreibrechte gibt es nur im App-Ordner.

Öffentlich ohne Login sind nur die statischen Dateien (CSS, JS, Icons,
Manifest). Sie enthalten keine Daten. Das Manifest muss öffentlich sein, weil
Browser es ohne Cookies abrufen.

---

## Oberfläche

- **Eine Ansicht.** Der Titel (Listenname mit ⌄) schaltet zwischen der
  aktuellen Liste und der Listen-Übersicht um. Das entspricht den Titel-Menüs
  in Apples Notizen und Dateien.
- **Aktionsmenü als Action Sheet:** fährt unten auf, mit gruppierten Aktionen,
  rotem „Löschen“ und abgesetztem „Abbrechen“. Umgesetzt mit dem nativen
  `<dialog>`-Element. Fokus-Falle, Escape und abgedunkelter Hintergrund kommen
  damit gratis.
- **Mobile first:** System-Schrift, Schriftgröße 17 px (ab 16 px zoomt iOS beim
  Fokussieren nicht), Zeilen mindestens 56 px hoch, Checkboxen 28 px,
  Safe-Area-Abstände für Notch und Home-Indikator.
- **Wie eine App:** kein Zoom per Doppeltippen (`touch-action: manipulation`),
  keine Textmarkierung auf Bedienelementen, kein Tap-Highlight. Zoomen mit zwei
  Fingern bleibt möglich.
- **Farben** stehen als CSS-Variablen in `:root` und zentral in
  `[data-theme="dark"]`. Titel und Plus-Button teilen sich `--accent`.
- **Hell/Dunkel:** `theme.js` wird synchron im `<head>` geladen und setzt
  `data-theme` vor dem ersten Rendern, daher kein Aufblitzen. Ohne eigene Wahl
  folgt die App `prefers-color-scheme`. Die Statusleisten-Farbe
  (`theme-color`) wechselt mit.
- **Barrierefreiheit:** echte `<button>`- und `<input type="checkbox">`-Elemente
  mit `<label>`, `aria-label` für Icon-Buttons, `aria-expanded` am Titel,
  `aria-current` für die aktive Liste, `role="status"`/`role="alert"` für
  Meldungen. Nach dem Neuzeichnen bleibt der Tastaturfokus auf demselben
  Element.
- Gemerkt **pro Gerät** (`localStorage`): gewählte Liste, Auf/Zu des
  Erledigt-Bereichs, Farbschema.

---

## Design-Entscheidungen

| Entscheidung | Warum |
|---|---|
| **Go + `net/http`, keine Frameworks** | Die Standardbibliothek kann seit Go 1.22 Methoden- und Pfad-Routing (`GET /api/items/{id}`) und seit 1.25 CSRF-Schutz. Mehr braucht es nicht. |
| **Alles in `main.go`** | Knapp 800 Zeilen, in Abschnitte gegliedert. Weitere Pakete würden die Übersicht nicht verbessern. |
| **SQLite statt JSON-Datei oder Postgres** | Eine Datei, kein zusätzlicher Dienst, Transaktionen und sichere Schreibvorgänge inklusive. Eine JSON-Datei hätte am Ende mehr eigenen Code gebraucht. |
| **`modernc.org/sqlite` statt `mattn/go-sqlite3`** | Reines Go, daher lässt sich auf dem Mac ohne C-Toolchain für Linux ARM64 bauen. |
| **SSE statt WebSockets** | Die Kommunikation geht nur vom Server zum Client, `EventSource` verbindet sich selbst neu, und Caddy reicht SSE ohne Konfiguration durch. |
| **Signal + Komplett-Reload statt Deltas** | Kein Zustand, der auseinanderlaufen kann. Bei wenigen Dutzend Artikeln kostet das nichts. |
| **Alle Listen in einer Antwort** | Wechseln zwischen Listen geht ohne Netz und sofort, und offline sind alle Listen lesbar. |
| **Bestätigt statt optimistisch** | Änderungen erscheinen nach der Server-Antwort, also nach Millisekunden. Dafür täuscht die App offline nie eine erfolgreiche Speicherung vor. |
| **Keine Offline-Bearbeitung** | Eine Warteschlange mit Konfliktlösung wäre deutlich mehr Code. Im Laden reicht Lesen ohne Netz. |
| **Signiertes Cookie statt Session-Tabelle** | Kein Session-Speicher, übersteht Neustarts, Passwortwechsel loggt alle aus. |
| **Positionen mit MIN−1 / MAX+1** | „Ganz nach oben“ und „ans Ende“ ändern genau eine Zeile, ein Umnummerieren ist nie nötig. |
| **Aktionsmenü statt Drag & Drop** | Ziehen auf Touchscreens kollidiert mit Scrollen und Antippen und wäre der größte Codeblock der App. „Ganz nach oben“ deckt den eigentlichen Bedarf ab. |
| **Keine Build-Tools** | HTML, CSS und JS werden so ausgeliefert, wie sie im Repo liegen. |
| **Kein Backup** | Bewusst weggelassen, eine verlorene Einkaufsliste ist verschmerzbar. Siehe [Betrieb](#betrieb). |

---

## Lokal entwickeln

Voraussetzung: Go 1.26 oder neuer. Ein älteres `go` lädt die passende
Toolchain dank `GOTOOLCHAIN=auto` selbst nach.

```sh
APP_PASSWORD=test go run .
```

Dann <http://127.0.0.1:4002> öffnen. Die Datenbank `shop.db` entsteht im
aktuellen Ordner. Lokal läuft alles über HTTP, das Cookie ist dann nicht
`Secure`.

```sh
go vet ./...
go test ./...
```

---

## Konfiguration

Alles über Umgebungsvariablen:

| Variable       | Standard          | Bedeutung                                  |
|----------------|-------------------|--------------------------------------------|
| `APP_PASSWORD` | – (Pflicht)       | Gemeinsames Passwort, darf nicht leer sein |
| `LISTEN_ADDR`  | `127.0.0.1:4002`  | Adresse und Port                           |
| `DB_PATH`      | `shop.db`         | Pfad zur SQLite-Datei                      |

Auf dem Server stehen sie in `~/shopping-list/env` (nur für `deploy` lesbar,
`chmod 600`). systemd lädt die Datei per `EnvironmentFile`.

---

## Deployment

### Server

- Ubuntu 24.04, **ARM64**, Benutzer `deploy` auf `91.99.174.129`
- Caddy als Reverse Proxy mit automatischem HTTPS
- App-Ordner `/home/deploy/shopping-list/` mit `shopping-list` (Binary),
  `env` und `shop.db` (+ `-wal`/`-shm`)
- systemd-Dienst `shopping-list` mit `Restart=always`

Go muss auf dem Server nicht installiert sein. Gebaut wird lokal.

### Updates (der Normalfall)

```sh
./deploy.sh
```

Das Skript

1. baut ein statisches Linux-ARM64-Binary
   (`GOOS=linux GOARCH=arm64 CGO_ENABLED=0`),
2. lädt es per `scp` als `shopping-list.new` hoch und benennt es um. Ein
   laufendes Binary lässt sich unter Linux nicht direkt überschreiben,
   umbenennen aber schon.
3. beendet den Prozess mit SIGTERM. Wegen `Restart=always` startet systemd ihn
   nach 5 Sekunden mit dem neuen Binary. Dafür ist kein `sudo` nötig.

Während dieser 5 Sekunden zeigen offene Clients kurz „Keine Verbindung“ und
verbinden sich danach von selbst neu. Ausstehende Migrationen laufen beim
Start automatisch.

### Ersteinrichtung (einmalig)

```sh
# 1. Ordner und Konfiguration mit zufälligem Passwort anlegen
ssh deploy@91.99.174.129 'mkdir -p ~/shopping-list && cd ~/shopping-list &&
  umask 077 && printf "APP_PASSWORD=%s\nLISTEN_ADDR=127.0.0.1:4002\nDB_PATH=/home/deploy/shopping-list/shop.db\n" "$(openssl rand -base64 24)" > env'

# 2. Binary und Unit hochladen
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o shopping-list-linux .
scp shopping-list-linux deploy@91.99.174.129:shopping-list/shopping-list
scp shopping-list.service deploy@91.99.174.129:shopping-list/

# 3. Auf dem Server: Dienst einrichten und starten
sudo cp ~/shopping-list/shopping-list.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now shopping-list
```

### Reverse Proxy (Caddy)

DNS: A-Record `einkauf.dmko.org` → `91.99.174.129`.

In `/etc/caddy/Caddyfile`:

```
einkauf.dmko.org {
	reverse_proxy 127.0.0.1:4002
}
```

Danach `sudo systemctl reload caddy`. Caddy holt das Zertifikat selbst, setzt
`X-Forwarded-Proto` und reicht `text/event-stream` ungepuffert durch.
`encode gzip` ist hier bewusst weggelassen, damit der Event-Stream nicht
gepuffert wird.

### PWA installieren

1. <https://einkauf.dmko.org> öffnen und anmelden.
2. **iOS (Safari):** Teilen → „Zum Home-Bildschirm“.
   **Android (Chrome):** Menü → „App installieren“.
3. Die installierte App öffnen und einmal anmelden. Auf iOS hat die
   installierte App eigene Cookies, deshalb ist dort ein zweites Login nötig.

---

## Betrieb

| Aufgabe | Befehl / Vorgehen |
|---|---|
| Logs ansehen | `journalctl -u shopping-list -f` |
| Status | `systemctl status shopping-list` |
| Passwort ändern | `~/shopping-list/env` bearbeiten, dann `pkill -TERM -x shopping-list`. Alle Geräte müssen sich neu anmelden. |
| Neustart | `pkill -TERM -x shopping-list` (systemd startet neu) |
| Schema-Version | `sqlite3 -readonly ~/shopping-list/shop.db "PRAGMA user_version"` |
| Daten ansehen | `sqlite3 -readonly ~/shopping-list/shop.db "SELECT * FROM items"` |
| Backup (optional) | `sqlite3 ~/shopping-list/shop.db ".backup ~/shop-backup.db"`. Funktioniert im laufenden Betrieb. |

Die Daten liegen ausschließlich in `shop.db` (plus WAL-Dateien). Neustarts und
Deploys lassen sie unberührt.

---

## Grenzen

- **Zwei Nutzer, ein Passwort.** Es gibt keine Unterscheidung, wer etwas
  geändert hat.
- **Login-Sperre global:** Wer das Passwort durchprobiert, sperrt für eine
  Minute auch neue Logins von euch. Bestehende Sessions sind nicht betroffen.
- **Letzte Änderung gewinnt:** Ändern beide gleichzeitig denselben Artikel,
  gilt die spätere Änderung. Bei einer Einkaufsliste ist das unkritisch.
- **Keine Offline-Bearbeitung,** nur Lesen.
- **Listen umbenennen** ist noch nicht möglich.
- **Kurze Ausfälle bei Deploys** (rund 5 Sekunden).
