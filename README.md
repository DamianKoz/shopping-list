# Einkaufsliste

Eine gemeinsame Einkaufsliste für zwei Personen: ein Go-Binary mit SQLite,
Live-Sync per Server-Sent Events und installierbar als PWA.

## Voraussetzungen

- Go 1.26 oder neuer (nur zum Bauen; auf dem Server wird kein Go gebraucht)
- Ein Linux-Server mit Caddy als HTTPS-Reverse-Proxy

## Lokal starten

```sh
APP_PASSWORD=ein-langes-passwort go run .
```

Dann <http://127.0.0.1:4002> öffnen. Die Datenbank `shop.db` wird beim ersten
Start angelegt.

Tests: `go test ./...`

## Konfiguration

| Variable       | Standard          | Bedeutung                                   |
|----------------|-------------------|---------------------------------------------|
| `APP_PASSWORD` | – (Pflicht)       | Gemeinsames Passwort                        |
| `LISTEN_ADDR`  | `127.0.0.1:4002`  | Adresse und Port                            |
| `DB_PATH`      | `shop.db`         | Pfad zur SQLite-Datei                       |

Wird das Passwort geändert, werden alle bestehenden Sessions ungültig.

## Binary bauen

Der SQLite-Treiber ist reines Go, daher klappt Cross-Compiling ohne C-Compiler:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o shopping-list-linux .
```

Alle statischen Dateien sind per `go:embed` im Binary enthalten.

## Auf dem Server einrichten (einmalig)

```sh
# Ordner und Konfiguration
ssh deploy@91.99.174.129 'mkdir -p ~/shopping-list && cd ~/shopping-list &&
  umask 077 && printf "APP_PASSWORD=%s\nLISTEN_ADDR=127.0.0.1:4002\nDB_PATH=/home/deploy/shopping-list/shop.db\n" "$(openssl rand -base64 24)" > env'

# Binary und systemd-Unit hochladen
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o shopping-list-linux .
scp shopping-list-linux deploy@91.99.174.129:shopping-list/shopping-list
scp shopping-list.service deploy@91.99.174.129:shopping-list/

# Auf dem Server (braucht sudo)
sudo cp ~/shopping-list/shopping-list.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now shopping-list
```

Das Passwort steht danach in `~/shopping-list/env`.

## Updates deployen

```sh
./deploy.sh
```

Baut das Binary, lädt es per `scp` hoch und startet den Dienst neu. Die
Datenbank bleibt dabei erhalten.

Logs: `journalctl -u shopping-list -f`

## Reverse-Proxy (Caddy)

In `/etc/caddy/Caddyfile` ergänzen:

```
einkauf.dmko.org {
	reverse_proxy 127.0.0.1:4002
}
```

Dann `sudo systemctl reload caddy`. Caddy besorgt das HTTPS-Zertifikat
automatisch und reicht Server-Sent Events ohne weitere Einstellungen durch.
Auf `encode gzip` bewusst verzichten, damit der Event-Stream nicht gepuffert
wird.

## Datenbank sichern

Optional, falls doch einmal gewünscht:

```sh
sqlite3 ~/shopping-list/shop.db ".backup ~/shop-backup.db"
```

## PWA installieren

1. `https://einkauf.dmko.org` im Browser öffnen.
2. **iOS (Safari):** Teilen → „Zum Home-Bildschirm“.
   **Android (Chrome):** Menü → „App installieren“ bzw. „Zum Startbildschirm hinzufügen“.
3. Die installierte App einmal öffnen und anmelden. Auf iOS hat die App eigene
   Cookies, deshalb ist dort ein zweites Login nötig. Danach bleibt man ein Jahr
   angemeldet.

Ohne Verbindung zeigt die App den zuletzt geladenen Stand mit dem Hinweis
„Keine Verbindung – nur lesen“. Änderungen sind erst wieder möglich, wenn der
Server erreichbar ist.
