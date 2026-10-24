# CLAUDE.md

Gemeinsame Einkaufslisten-PWA für zwei Personen. Architektur, API, Sicherheit,
Design-Entscheidungen und Deployment stehen in `README.md`. Lies sie, bevor du
größere Änderungen machst.

## Arbeitsweise

- **Einfachheit vor allem.** Keine neuen Abhängigkeiten, Frameworks,
  Build-Tools oder Abstraktionsschichten. Backend bleibt in `main.go`, Frontend
  in Vanilla HTML/CSS/JS unter `static/`. Keine Features über die Anfrage hinaus.
- **Keine Tests schreiben oder ausführen.** Der Nutzer testet direkt live.
  `go vet ./...` als Kompilier-Check reicht.
- **Deployen mit `./deploy.sh`.** Kein sudo nötig: Das Skript lädt das Binary
  hoch und beendet den Prozess, systemd (`Restart=always`) startet ihn nach
  etwa 5 Sekunden neu. Danach prüfen:
  `ssh deploy@91.99.174.129 'systemctl is-active shopping-list'`.
- **Schema-Änderungen nur als neuer Eintrag am Ende von `migrations`** in
  `main.go`. Bestehende Einträge nie ändern, die Produktionsdatenbank hat sie
  schon angewendet.
- **Nicht committen:** die Datei `deploy` im Projektordner. Sie ist eine
  veraltete Kopie von `deploy.sh`.

## Oberfläche

- Alle Texte auf Deutsch, API-Fehlermeldungen ebenfalls.
- Orientierung an Apple: ruhige Optik, viel Weißraum, Action Sheets statt
  Popups, große Touch-Ziele.
- Keine sichtbaren Fokus-Rahmen auf Touch-Elementen. Für Tastatur-Fokus eine
  dezente Alternative wählen (Unterstreichung, Hinterlegung).
- Farben nur über die CSS-Variablen in `:root` und `[data-theme="dark"]`.
