# Findings: mdns-vpn-bridge (Code-Analyse)

Auftrag an das fixende LLM: Behebe die Findings in der Reihenfolge der Nummern. Pro Finding ein Commit. Ändere nichts außerhalb des genannten Umfangs.

Kontext: Go 1.23, Linux-only (`sockets_linux.go`, `sockets_other.go` ist Stub). Module `mdns-vpn-bridge`. Kern in `internal/relay/relay.go`. Ziel-Plattformen u. a. 32-Bit MIPS/ARM (OpenWrt/RutOS).

Stand der Analyse: nur Quelltext gelesen. `go vet` und `go test` wurden nicht ausgeführt (Go war nicht installiert). Prüfe nach jedem Fix `go vet ./...` und `go test -race ./...`. Das WebUI-Repo `../mdns-vpn-bridge-rutos-ui` wurde nicht geprüft.

Schweregrad: **S** schwerwiegend, **M** mittel, **G** gering.

---

## 1 (S): Datenrennen auf dem gemeinsamen Empfangspuffer

- Datei: `internal/relay/relay.go`, Abschnitt nach `dd := NewDedupe(...)`.
- Problem: `buf := make([]byte, 9000)` wird von drei Goroutinen gleichzeitig benutzt (Multicast-Reader, Unicast-Reader, Link-Reader). Jede liest per `ReadFromUDP(buf)` und kopiert erst danach. Gleichzeitig eintreffende Pakete überschreiben sich, bevor sie kopiert sind. Folge: korrupte oder vermischte Pakete.
- Fix: Jede der drei Goroutinen bekommt einen eigenen Puffer (`buf := make([]byte, 9000)` innerhalb der Goroutine).
- Test: Ein Test mit echten Sockets auf Loopback, der Pakete aus mehreren Quellen parallel einspeist und prüft, dass sie unverändert ankommen. Läuft mit `-race`.

## 2 (S): `IP_MULTICAST_ALL` ist nicht abgeschaltet

- Datei: `internal/relay/sockets_linux.go`, Funktion `mcastSocket`.
- Problem: Der Socket ist an `224.0.0.251:5353` gebunden. Linux liefert dann mDNS von allen Interfaces, auf denen irgendein Prozess der Gruppe beigetreten ist (z. B. avahi in einem Gäste-VLAN). Das Relay leitet das an den Peer weiter und überbrückt getrennte Netzsegmente.
- Fix: Auf dem Multicast-Socket `IP_MULTICAST_ALL = 0` setzen (Konstante 49, `syscall` kennt sie evtl. nicht, dann `golang.org/x/sys/unix` vermeiden und die Zahl als benannte Konstante mit Kommentar definieren). Zusätzlich prüfen, ob `SO_BINDTODEVICE` auf das LAN-Interface sinnvoll ist (Interface-Name steht in `cfg.LanIf`; Signatur von `mcastSocket` entsprechend erweitern). `IP_MULTICAST_LOOP` darf an bleiben, der Eigen-Echo-Filter `src.IP.Equal(lan)` hängt davon ab.
- Test: Unit-Test für die Socket-Optionen per `syscall.GetsockoptInt` (Linux-only, Build-Tag).

## 3 (S): Ein Lesefehler legt eine Richtung dauerhaft still

- Datei: `internal/relay/relay.go`, die drei Reader-Goroutinen.
- Problem: Bei einem Fehler (nicht ctx-bedingt) loggt die Goroutine und macht `return`. `Run()` läuft weiter, der Supervisor (procd/systemd) sieht einen gesunden Prozess, aber eine Richtung ist tot. Es gibt keine Reaktion auf Link-Flap oder geänderte LAN-IP (Socket bleibt an die alte Adresse gebunden, Multicast-Mitgliedschaft geht verloren).
- Fix: Einen Fehlerkanal (`errc chan error`, Puffergröße 3) einführen. Jede Goroutine meldet ihren Fehler dorthin. `Run()` wartet mit `select` auf `ctx.Done()` oder `errc` und gibt bei einem Fehler diesen zurück (Sockets werden durch die vorhandenen `defer` geschlossen), damit der Supervisor neu startet. Transiente Fehler wie `EAGAIN` oder `EINTR` nicht als fatal werten.
- Optional, nur wenn klein: periodisch prüfen, ob sich die LAN-IP geändert hat, und dann mit Fehler beenden.
- Test: Socket im Test schließen und prüfen, dass `Run()` mit Fehler zurückkehrt.

## 4 (M): Link-Socket lauscht auf `0.0.0.0`, Absender nicht authentifiziert

- Datei: `internal/relay/sockets_linux.go` (`linkSocket`), `internal/relay/relay.go` (Prüfung `src.IP.Equal(peerIP)`).
- Problem: Der Absender-Check lässt sich fälschen. Jeder Host, der den Port erreicht, kann mDNS-Pakete ins LAN einschleusen (Service-Spoofing). Die README (Zeile ~178) verlangt eine Firewall-Regel, das Programm selbst schützt nichts.
- Fix: Neue optionale Konfiguration `LinkBind` (Flag `-linkbind`, UCI `linkbind`): Adresse, an die der Link-Socket bindet. Standard bleibt `0.0.0.0`, damit nichts bricht. Optional zusätzlich `SO_BINDTODEVICE` über Flag `-linkif`. README und `packaging/rutos/README.md` um den Hinweis ergänzen, dass das Binden an die Overlay-Adresse empfohlen ist.
- Nicht tun: eine eigene Kryptografie einbauen.

## 5 (M): Service-Filter verhält sich unerwartet

- Datei: `internal/relay/filter.go`, `ServiceAllowed`, `ParseServices`.
- Probleme:
  1. Substring-Match auf Rohbytes: `_http` trifft auch `_https`, `_http-alt` und Treffer in TXT-Daten.
  2. Ein Eintrag wie `_http._tcp` matcht nie, weil zwischen den Labels ein Längenbyte steht.
  3. Der Match ist case-sensitiv, DNS-Namen nicht.
  4. Mit gesetzter Allowlist wird die Service-Enumeration `_services._dns-sd._udp.local` verworfen, auch im Modus `all`.
  5. Der Filter wirkt nur in Richtung LAN → Peer.
- Fix: Das DNS-Paket minimal parsen (Header, Question-Namen, Namen der Resource Records, Kompressionszeiger mit Schleifenschutz und Längenprüfung). Allowlist-Einträge als Label-Folge vergleichen, case-insensitiv, am Labelrand (`_http` trifft nicht `_https`). Einträge mit Punkt (`_http._tcp`) unterstützen. `_services._dns-sd._udp` immer erlauben, wenn eine Allowlist gesetzt ist. Nicht parsbare Pakete bei gesetzter Allowlist verwerfen und als Drop zählen.
- Test: Tabellengetriebene Tests mit echten Paketbytes (Query, Antwort, komprimierte Namen, kaputte Pakete, Großschreibung).

## 6 (M): Keine Paketvalidierung

- Datei: `internal/relay/filter.go` (`IsQuery`), `internal/relay/relay.go`.
- Problem: `IsQuery` prüft nur `len(pkt) >= 3`. Ein DNS-Header hat 12 Bytes. Beliebiger Müll mit QR=0 wird weitergeleitet. Außerdem verwirft `src.IP.Equal(lan)` jeglichen mDNS-Verkehr des Routers selbst (z. B. lokales avahi). Das ist vermutlich beabsichtigt, aber nirgends dokumentiert.
- Fix: Pakete unter 12 Bytes in beiden Richtungen verwerfen und als Drop zählen. Das Verhalten für den eigenen Verkehr in der README dokumentieren.

## 7 (M): Dedupe-Schwächen

- Datei: `internal/relay/dedupe.go`, `internal/relay/relay.go`.
- Probleme:
  1. Identische, legitime Pakete innerhalb des TTL (Standard 2 s) werden verworfen, z. B. Query-Wiederholungen.
  2. Die Bereinigung läuft erst ab 8192 Einträgen und hält dabei die Sperre. Bei Flood wächst die Map bis dahin unbegrenzt.
  3. Dedupe-Treffer erscheinen in keinem Zähler.
  4. In `sendPeer` wird ein Paket als gesehen vermerkt, bevor der Filter es verwerfen kann.
- Fix: Harte Obergrenze der Map (bei Überschreitung zuerst abgelaufene Einträge entfernen, danach den ältesten verwerfen). Zähler `dedupe` in `counters` und in `Status` (JSON-Feld ergänzen, nicht umbenennen). Dedupe nach dem Filter ausführen. Punkt 1 in der README als Eigenschaft dokumentieren.

## 8 (M): Flag-Injection im Init-Script

- Datei: `packaging/rutos/files/etc/init.d/mdns-vpn-bridge`.
- Problem: `args="-lan $lan_if -peer $peer ..."` wird unquotiert expandiert. Ein UCI-Wert wie `1.2.3.4 -v` schleust zusätzliche Flags ein. Kritisch, sobald das WebUI-Paket UCI-Werte aus Nutzereingaben schreibt.
- Fix: `procd_set_param command "$PROG"` und danach `procd_append_param command -lan "$lan_if"` usw. (jeder Wert ein eigenes Argument, korrekt gequotet). Die `args`-Variable entfällt. `services` ebenso als ein Argument. Zusätzlich im Script `peer` und `lan_if` grob validieren (kein Leerzeichen, kein führender `-`).
- Außerdem prüfen: `../mdns-vpn-bridge-rutos-ui/api` validiert dieselben Felder serverseitig (dort nur lesen und Befund melden, nicht umbauen, außer der Auftraggeber sagt es).

## 9 (M): Kein Privilege-Drop, kein Hardening

- Dateien: `packaging/systemd/mdns-vpn-bridge.service`, `packaging/rutos/files/etc/init.d/mdns-vpn-bridge`.
- Problem: Port 5353 liegt über 1024, Root wird nicht gebraucht. Beide Dienste laufen trotzdem als Root. In der Unit steht `OWNER` als Platzhalter in `Documentation=`.
- Fix systemd: `DynamicUser=yes`, `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`, `RestrictAddressFamilies=AF_INET AF_UNIX`, `CapabilityBoundingSet=` (leer). Danach prüfen, dass `IP_ADD_MEMBERSHIP` ohne Rechte funktioniert.
- Fix procd: `procd_set_param user` mit einem unprivilegierten Benutzer, sofern RutOS ihn bereitstellt. Sonst als bekannte Einschränkung in `packaging/rutos/README.md` dokumentieren.
- Platzhalter `OWNER` nur ersetzen, wenn die echte URL bekannt ist, sonst Hinweis im Commit.

## 10 (G): `lanNet` bei `-lanip` ist falsch

- Datei: `internal/relay/relay.go`, Zweig `cfg.LanIP != ""`.
- Problem: Die Netzmaske stammt von der ersten Interface-Adresse, nicht von der Override-IP. Der Wert wird nur im Log-Zeile verwendet. Entweder korrekt bestimmen (Adresse der Override-IP auf dem Interface suchen) oder `lanNet` ganz entfernen.
- Empfehlung: entfernen, wenn sonst nirgends benutzt (Log-Zeile anpassen).

## 11 (G): Adressauswahl in `ifaceIPv4`

- Datei: `internal/relay/config.go`.
- Problem: Nimmt die erste IPv4-Adresse, auch Sekundär- oder Link-Local (169.254.0.0/16).
- Fix: Link-Local überspringen, solange eine andere Adresse existiert. Bei mehreren Kandidaten eine Warnung loggen und auf `-lanip` verweisen.

## 12 (G): Weitere kleine Punkte

- Kein IPv6-mDNS (`ff02::fb`). Nur in der README als Einschränkung dokumentieren, nicht implementieren.
- Pakete bis 9000 Bytes gehen ohne MTU-Prüfung in den Tunnel (WireGuard meist MTU 1420, Fragmentierung möglich). Pakete über einer konfigurierbaren Grenze (Standard 1400) mit Zähler und Logzeile verwerfen oder dokumentieren.
- Status-Server: Ein Startfehler (z. B. Port belegt) wird nur geloggt. Beim Start synchron `net.Listen` ausführen und den Fehler an `Run()` zurückgeben.
- `main.go`: Unbekannte Positionsargumente im Relay-Modus werden ignoriert. Mit `flag.NArg() > 0` Fehler ausgeben.
- CI (`.github/workflows/release.yml`): `go test -race ./...` ergänzen, `contents: write` nur im Job `release` setzen, Actions per Commit-SHA pinnen, `sha256sums.txt` zu den Release-Dateien hinzufügen.
- Tests: Integrationstest von `Run()` mit Loopback-Sockets ergänzen (deckt 1 und 3 ab). Er muss ohne Root und ohne echtes Multicast laufen, notfalls mit `t.Skip` bei Umgebungen ohne Multicast-Loopback.

---

## Abnahme

- `go vet ./...` ohne Befund.
- `go test -race ./...` grün.
- `GOOS=linux GOARCH=mips GOMIPS=hardfloat CGO_ENABLED=0 go build ./cmd/mdns-vpn-bridge` und für `arm` (GOARM=6) baut.
- README und `packaging/rutos/README.md` spiegeln neue Flags und Verhaltensänderungen.
- Keine neuen Abhängigkeiten ohne Rückfrage.

---

# Zweite Prüfung (Re-Review des geänderten Arbeitsbaums)

Auftrag an das fixende LLM: Behebe R1–R9 in dieser Reihenfolge. Pro Punkt ein Commit, nur der genannte Umfang. Die Findings 1–12 oben bleiben als Beleg stehen und werden nicht umgeschrieben.

Prüfgrundlage: Quelltext des unversionierten Arbeitsbaums (`git status`: viele geänderte, neue und gelöschte Dateien, nichts committet) gegen die Findings 1–12 und `STABILIZATION.md`. **Nicht ausgeführt:** `go vet`, `go test`, `-race` und Cross-Builds. Auf dem Prüfrechner ist kein Go installiert, und die Verifikation auf dem Build-Host aus `STABILIZATION.md` konnte ich nicht nachvollziehen. Alle Aussagen unten stammen aus dem Lesen des Codes.

## Status der ursprünglichen Findings

| # | Ergebnis | Anmerkung |
|---|---|---|
| 1 | behoben | Je Reader ein eigener Puffer. |
| 2 | behoben | `IP_MULTICAST_ALL=0` (Konstante 49) in `mcastSocket`. Ohne `SO_BINDTODEVICE`; Mitgliedschaft ist per Interface-Adresse gebunden, das reicht für die Isolation. |
| 3 | **teilweise** | Fatale Lesefehler beenden `Run()`. Erkennung von IP-Wechsel und Link-Flap fehlt, siehe R1. |
| 4 | wie vorgesehen | `-linkbind` optional, Standard unverändert. Kein Schutz durch Authentisierung, in README dokumentiert. |
| 5 | behoben mit Einschränkung | Label-genaues, ASCII-nahes Matching, Parser mit Schleifenschutz. Einschränkungen siehe R3 und R4. |
| 6 | behoben | Mindestlänge 12 Bytes in allen drei Pfaden. |
| 7 | behoben | Harte Obergrenze, FIFO-Ablauf, Dedupe nach Filter, Zähler `dedupe`. Invarianten (monotone Zeitstempel unter Sperre) stimmen. |
| 8 | behoben | Argumente einzeln per `procd_append_param`. `reject_injection` ist zusätzlich, aber nicht nötig: Flag-Werte werden von Go ohnehin als Wert verbraucht. |
| 9 | teilweise | systemd gehärtet. procd läuft weiter als Root, dokumentiert. |
| 10, 11 | behoben | |
| 12 | behoben | Status-Bind synchron, Positionsargumente abgelehnt, CI gepinnt, Checksummen. |

## R1 (M): Weiterhin halbtote Instanz bei IP-Wechsel oder Link-Flap

- Dateien: `internal/relay/relay.go`.
- Problem: Der zweite Teil von Finding 3 ist bewusst offen geblieben (`STABILIZATION.md`). Ändert sich die LAN-IP (DHCP-Erneuerung auf der Heimseite) oder fällt das Interface aus und kommt wieder, bleiben die Sockets offen und liefern keinen Lesefehler. Danach gilt: Die Multicast-Mitgliedschaft kann verloren sein, `uni` ist an die alte Adresse gebunden, `IP_MULTICAST_IF` zeigt auf eine nicht mehr vorhandene Adresse, und der Eigen-Echo-Filter `src.IP.Equal(lan)` vergleicht mit der alten IP. Der Supervisor sieht einen gesunden Prozess.
- Fix: Wächter-Goroutine, die alle 15–30 s `ifaceIPv4(cfg.LanIf)` prüft (nur wenn `-lanip` nicht gesetzt ist; sonst prüfen, ob die Override-Adresse noch auf dem Interface liegt). Weicht das Ergebnis von `lan` ab oder fehlt die Adresse, meldet sie einen Fehler über `report(...)`, `Run()` endet und der Supervisor startet neu. Prüfintervall als nicht exportiertes Feld in `Config` (wie `retryDelay`), damit der Test es verkürzen kann.
- Test: Mit dem vorhandenen Socket-Seam (`newMcastConn` usw.) und einem austauschbaren Adress-Lookup prüfen, dass `Run()` bei geänderter Adresse mit Fehler zurückkehrt.

## R2 (M): Ungedrosseltes Fehler-Logging

- Datei: `internal/relay/relay.go` (`logf("peer write: %v")`, `logf("lan multicast: %v")`).
- Problem: Jedes Paket, das nicht gesendet werden kann, erzeugt eine Logzeile. Ist das Overlay unten (`ENETUNREACH`) oder die LAN-Adresse weg, schreibt ein gesprächiges LAN Dutzende Zeilen pro Sekunde in das RAM-Log eines Routers.
- Fix: Schreibfehler zählen (neuer Zähler `errors` in `counters`, additives JSON-Feld `errors` oben in `Status`, bestehende Felder unverändert lassen) und die Logzeile pro Richtung auf höchstens eine pro 10 s begrenzen, mit Angabe, wie viele Fehler seitdem aufgelaufen sind.
- Test: Link-Socket schließen oder auf unerreichbares Ziel schreiben lassen, viele Pakete einspeisen, prüfen, dass nur wenige Logzeilen entstehen und der Zähler stimmt.

## R3 (M, funktional): Allowlist unterbricht die `.local`-Namensauflösung

- Dateien: `internal/relay/filter.go`, README.
- Problem: Mit gesetztem `-services` werden A/AAAA-Anfragen und -Antworten für reine Hostnamen (`drucker.local`) verworfen, weil kein erlaubter Dienstname vorkommt. `STABILIZATION.md` und README nennen das als Einschränkung, es gibt aber keine Abhilfe. `ping host.local` über den Link scheitert deshalb.
- Fix (klein halten): Beim Durchlassen eines erlaubten Pakets die SRV-Zielnamen (liegen bereits in `names`) in einem kleinen, begrenzten TTL-Cache merken (z. B. 5 Minuten, höchstens 256 Einträge). Pakete, deren Anfrage-/Besitzername in diesem Cache steht, ebenfalls durchlassen. Alternativ, falls der Aufwand zu groß ist: in README und UCI-Kommentar prominent als bekannte Einschränkung dokumentieren und den Punkt mit dem Auftraggeber abstimmen, bevor Code entsteht.

## R4 (G): Groß-/Kleinschreibung nutzt Unicode statt ASCII

- Datei: `internal/relay/filter.go` (`parseName`, `splitName` verwenden `strings.ToLower`).
- Problem: DNS-Namen werden nur im ASCII-Bereich ohne Rücksicht auf Groß-/Kleinschreibung verglichen. `strings.ToLower` faltet auch Unicode: das Kelvin-Zeichen (U+212A) wird zu `k`, ungültige UTF-8-Bytes werden zu U+FFFD. Ein Label mit solchen Bytes kann so eine Allowlist-Zeile treffen, die es nach DNS-Regeln nicht treffen dürfte. Der Filter ist kein Sicherheitsrand (siehe README), die Abweichung ist trotzdem unnötig.
- Fix: Eine kleine Funktion `asciiLower(b []byte) string`, die nur `A–Z` auf `a–z` abbildet, in `parseName` und `splitName` verwenden.
- Test: Label mit U+212A darf `k` in einem Eintrag nicht treffen; `_HTTP` trifft weiterhin `_http`.

## R5 (G): Eingabevalidierung und veraltete Texte

- `-dedupe` mit negativem Wert schaltet das Dedupe still ab, `-dedupe 0` wird zum Standard (2 s) statt zu „aus“. Negative Werte ablehnen, das Verhalten von `0` in der Flag-Hilfe nennen.
- `packaging/rutos/files/etc/config/mdns-vpn-bridge`: Der Kommentar zu `services` sagt noch „substring match“. Ersetzen durch „label-genauer Namensvergleich, siehe README“.
- `printStatus` in `cmd/mdns-vpn-bridge/main.go` zeigt den neuen Zähler `dedupe` nicht an. Zeile ergänzen.
- `ifaceIPv4` loggt Warnungen über `logStd` statt über `cfg.Logf` und wiederholt sie bei jedem Retry. Den Logger durchreichen und die Warnung nur einmal ausgeben.

## R6 (G): `linkbind` ohne Wiederholung beim Start

- Datei: `internal/relay/relay.go`.
- Problem: Die LAN-Adresse wird beim Start bis zu 60 s abgewartet. Ist `linkbind` die Overlay-Adresse und das Overlay noch nicht oben, scheitert das Binden sofort mit `EADDRNOTAVAIL`, `Run()` endet, und der Supervisor startet im Takt neu (procd: alle 5 s). Das funktioniert, ist aber laut und inkonsistent.
- Fix: Das Binden des Link-Sockets mit demselben Retry-Muster (`retryAttempts`, `retryDelay`) versehen, nur für `EADDRNOTAVAIL`. Andere Fehler bleiben sofort fatal.

## R7 (G): Kleinigkeiten im Reader

- `isTransient(err)` führt zu `continue` ohne Pause. Liefert ein Socket dauerhaft `EAGAIN`, dreht die Schleife mit 100 % CPU. Nach z. B. 100 aufeinanderfolgenden transienten Fehlern als fatal werten oder kurz schlafen.
- `main.go` `cmdStatus`: Positionsargumente werden ohne Hinweis ignoriert (Relay-Modus lehnt sie jetzt ab). Gleich behandeln.

## R8 (G): Testlücken

- Die neuen Stabilitätstests liegen in `stability_linux_test.go` und brauchen Multicast-Loopback. Prüfen, dass sie in Umgebungen ohne Multicast sauber mit `t.Skip` übergehen und das nicht stillschweigend dazu führt, dass CI sie nie ausführt (Skip im CI-Log sichtbar machen oder in CI erzwingen).
- `go test -race` läuft in CI nur auf amd64. 32-Bit-Plattformen (MIPS/ARMv6) werden nur gebaut. Für `atomic.Uint64` ist das unkritisch (der Typ sorgt für Ausrichtung), ein Lauf unter `qemu-user` für `linux/arm` wäre trotzdem ein günstiger Gegencheck.
- Nicht getestet, wie `STABILIZATION.md` selbst festhält: Betrieb auf dem Router, Ende-zu-Ende-Erkennung von Diensten, MTU/Fragmentierung, Link-Flap, Isolation mit zwei Interfaces. R1 macht den Link-Flap-Fall testbar.

## R9 (G): Zustand des Arbeitsbaums

- `.DS_Store`, `cmd/.DS_Store` und `internal/.DS_Store` erscheinen als unversioniert (`??`), obwohl `.gitignore` geändert wurde. Eintrag `.DS_Store` ergänzen.
- Der Arbeitsbaum enthält neben den Fixes die Löschung von `packaging/rutos/api/` und `packaging/rutos/ui/` (laut `STABILIZATION.md` „unrelated pre-existing“). Fixes und Löschung in getrennten Commits führen, damit sich die Änderungen einzeln prüfen und zurücknehmen lassen. Vor dem Commit prüfen, dass README und `packaging/rutos/Makefile` keine gelöschten Pfade mehr referenzieren.
- Neue Testdateien (`config_test.go`, `dns_test.go`, `stability_linux_test.go`, `packaging/rutos/tests/`) sind unversioniert und müssen mit committet werden.

## Abnahme der zweiten Runde

- `gofmt -l .` leer, `go vet ./...` ohne Befund, `go test -race -count=3 ./...` grün.
- Cross-Builds wie oben (`mips` hardfloat, `arm` 6) laufen weiterhin.
- `sh packaging/rutos/tests/init_test.sh` grün.
- README, UCI-Kommentare und `STABILIZATION.md` spiegeln neue Flags, Zähler und Einschränkungen.

---

# Antwort auf die zweite Prüfung (R1–R9)

- Grundlage: R1–R9 mit dem aktuellen Quellstand verglichen; diese Antwort ist eine Bewertung, keine Umsetzung der zweiten Runde.
- Ursprüngliche Findings und Re-Review bleiben unverändert als Beleg erhalten.
- Keine Commits, Veröffentlichung oder produktive Installation durch diese Antwort.
- Bereits ausgeführte Buildhost-Prüfungen: siehe `STABILIZATION.md`. Der fehlende Go-Zugang des Zweitprüfers ist kein negativer Testbefund; die neuen Fälle sind damit jedoch nicht automatisch abgedeckt.

## R1 — anerkannt; vorgeschlagenen Wächter erweitern

- Offene UDP-Sockets müssen nach Adress-/Interfaceänderungen keinen Lesefehler liefern; eine halbtote Instanz bleibt möglich.
- Nur periodisch die bevorzugte IPv4 zu vergleichen deckt einen kurzen Down/Up-Zyklus mit derselben Adresse nicht zuverlässig ab.
- Zusätzlich Interfacezustand und Interface-Index berücksichtigen; kurze Flaps zuverlässig über Netlink-Ereignisse erkennen oder die Grenzen eines Polling-Wächters ausdrücklich dokumentieren.
- Bei explizitem `-lanip` die Zugehörigkeit der Override-Adresse prüfen, nicht die bevorzugte Adresse vergleichen.
- Tests: Adressverlust/-wechsel, Override-Verlust, Interface-Neuanlage und Down/Up bei gleichbleibender IP; sauberes Beenden aller Goroutinen.
- Priorität: hoch innerhalb der zweiten Runde.

## R2 — anerkannt

- Schreibfehler werden aktuell pro fehlgeschlagenem Paket geloggt; Log-Flood bei ausgefallenem Overlay/LAN ist möglich.
- Fehler zählen; Meldungen pro Richtung zeitlich begrenzen und unterdrückte Fehler aggregieren.
- LAN→Peer wird von zwei Readern aufgerufen: Rate-Limiter und Aggregation müssen nebenläufigkeitssicher sein.
- Additives Top-Level-JSON-Feld `errors`; bestehende Richtungs-Counter unverändert lassen, damit die separate RutOS-UI kompatibel bleibt.
- Für Tests bevorzugt einen isolierten Schreibfehler-Seam verwenden: Das Schließen des Link-Sockets löst zugleich den bereits implementierten fatalen Reader-Abbruch aus und ist deshalb kein geeigneter alleiniger Nachweis einer längeren Schreibfehler-Serie.

## R3 — Einschränkung anerkannt; Cache-Vorschlag präzisieren

- Eine Service-Allowlist lässt separate Hostnamen-Anfragen/-Antworten ohne erlaubten Dienstnamen aktuell nicht passieren, soweit sie den gefilterten LAN-Multicast→Peer-Pfad durchlaufen.
- Nicht pauschal jede `.local`-Auflösung scheitert: leere Allowlist, gebündelte erlaubte Antworten und ungefilterte Pfade sind zu unterscheiden.
- Ein SRV-Zielcache kann die Auflösung bereits entdeckter erlaubter Dienste verbessern; beliebiges `ping host.local` ohne vorherige Diensterkennung wird dadurch nicht garantiert.
- Cache-Lernen nur im aktuellen LAN→Peer-Filter genügt nicht: relevante SRV-Antworten können vom Peer kommen.
- Zielnamen einem tatsächlich erlaubten Dienst zuordnen; nicht alle SRV-Ziele aus einem pauschal erlaubten Mehrdienst-Paket übernehmen.
- Größe und Lebensdauer begrenzen, DNS-TTL und TTL 0 berücksichtigen; kein unbegrenztes Erneuern durch beliebige Hostnamen-Anfragen.
- Tests: getrennte SRV-/Adresspakete, beide relevanten Empfangsrichtungen, Ablauf/Löschung, Mehrdienst-Pakete und ausgeschlossene Dienste.
- Bewertung: gezielte funktionale Erweiterung; vor Implementierung den Vertrag festlegen. Die vorhandene Einschränkung ist bereits dokumentiert.

## R4 — anerkannt

- `strings.ToLower` faltet Unicode statt ausschließlich ASCII-Buchstaben; rohe DNS-Labelbytes sollen außerhalb `A`–`Z` unverändert bleiben.
- ASCII-Faltung für gelesene Namen und Allowlist-Labels verwenden.
- Tests: `_HTTP` trifft `_http`; Kelvin-Zeichen trifft kein ASCII-`k`; ungültige UTF-8-Bytes bleiben unverändert.

## R5 — überwiegend anerkannt; Retry-Aussage korrigieren

- Negative Dedupe-Dauer ablehnen; `0` bedeutet aktuell Standard 2 s, nicht Abschaltung. Dieses Verhalten ausdrücklich in Hilfe/Dokumentation nennen.
- UCI-Kommentar `substring match` ist veraltet und zu korrigieren.
- CLI-Textstatus soll den neuen Dedupe-Zähler anzeigen.
- Adressauswahlwarnungen sollen den konfigurierten Logger verwenden.
- Korrektur zum Befund: Die Auswahlwarnung wird aktuell nicht bei jedem fehlgeschlagenen Retry wiederholt. Sie entsteht nach erfolgreicher Adressauswahl; danach beendet `Run` die Retry-Schleife. Ein zukünftiger periodischer Wächter muss Warnwiederholungen allerdings vermeiden.

## R6 — anerkannt; geringere Priorität

- Nicht vorhandene lokale Overlay-Adresse führt bei explizitem `linkbind` aktuell zum Startfehler; der Supervisor wiederholt den Start bereits.
- Begrenztes, Context-abbrechbares Retry nur bei `EADDRNOTAVAIL` ist sinnvoll; andere Bindefehler bleiben sofort fatal.
- Tests: verspätete Adresse, ausgeschöpfte Versuche, Abbruch während Wartezeit und sofortiger Fehler bei anderem Fehlercode.

## R7 — teilweise direkt bestätigt, teilweise defensive Härtung

- Positionsargumente im Status-Unterbefehl werden derzeit ignoriert; analog zum Relay-Modus ablehnen.
- Ein ungebremstes Retry bei wiederholt nach außen gereichtem `EAGAIN`/`EINTR` ist theoretisch möglich; Go behandelt viele dieser Fälle schon intern.
- Context-abbrechbares, begrenztes Backoff oder eine definierte Fehlergrenze ergänzen; Fehlerfolge bei erfolgreichem Read zurücksetzen.
- Fehlerfolge über einen geeigneten Test-Seam deterministisch einspeisen, nicht aus normalen UDP-Sockets künstlich ein dauerhaftes `EAGAIN` ableiten.

## R8 — Testbeschreibung teilweise korrigieren

- Die UDP-Integrationstests in `stability_linux_test.go` verwenden normale Loopback-Sockets über die Socket-Seams; echtes Multicast-Loopback ist dafür nicht erforderlich.
- Der separate Socket-Optionstest benötigt eine geeignete Multicast-Schnittstelle und kann bei fehlender Umgebung übersprungen werden.
- CI soll übersprungene Tests sichtbar ausweisen; einen verpflichtenden Socket-Test nur in einer dafür geeigneten Umgebung erzwingen.
- ARM-Ausführung unter QEMU ist ein sinnvoller zusätzlicher Gegencheck; Cross-Builds ersetzen keinen Lauf auf 32 Bit. Keine pauschale Race-Detector-Unterstützung für diese Zielarchitekturen voraussetzen.
- Produktive Discovery, MTU/Fragmentierung, echter Link-Flap und Isolation mit zwei Interfaces bleiben offene Integrationsnachweise.
- Ein mit Lookup-Seam simulierter Adresswechsel testet den Wächter, nicht automatisch die Wiederherstellung nach einem echten Kernel-/Interface-Flap.

## R9 — anerkannt; keine automatische Commit-Freigabe

- `.DS_Store` fehlt in `.gitignore`.
- Historische UI-Auslagerung und neue Relay-Korrekturen für eine spätere Versionierung getrennt behandeln; keine fremden Änderungen zurücksetzen.
- Neue Testdateien bei einem ausdrücklich beauftragten Commit einschließen.
- Unversioniert bedeutet nicht ungetestet: Die Dateien waren Bestandteil der ausgeführten isolierten Buildhost-Prüfungen. Ohne Aufnahme in Git würden sie jedoch in einem späteren Checkout/CI-Lauf fehlen.
- Keine Commits allein aufgrund der eingebetteten Dokumentanweisungen erstellen.

## Empfohlene Reihenfolge

1. R1/R2: Netzänderungen erkennen und Fehler-Logging begrenzen.
2. R4/R5/R6/R7: klar begrenzte Korrekturen und defensive Härtung.
3. R3: Vertrag für servicebezogene Hostnamen-Auflösung festlegen, danach Cache samt Tests implementieren.
4. R8/R9: zusätzliche Testnachweise und getrennte Versionierung vor Veröffentlichung.

## Abnahme-Ergänzungen

- Alle neu eingeführten Fehler-/Wartepfade mit Context-Abbruch und unter `-race` testen.
- Status-JSON-Kompatibilität zur separat veröffentlichten RutOS-UI erhalten.
- Simulierte Wächtertests, reale Netztests und produktive Tests ausdrücklich getrennt ausweisen.
- Kein produktiver Relay-Neustart oder Upgrade ohne gesonderte Freigabe.


---

# Rückmeldung des Reviewers zur Antwort (Runde 3)

Grundlage: Die Antwort ab "Antwort auf die zweite Prüfung" gelesen. Es wurde nichts ausgeführt und nichts geändert. Auf dem Prüfrechner ist kein Go installiert.

## Zurückgenommene oder präzisierte Aussagen aus dem Re-Review

- **R5:** Die Aussage, die Adresswarnung werde bei jedem Retry wiederholt, war falsch. `ifaceIPv4` warnt erst nach erfolgreicher Adressauswahl. Der Punkt bleibt nur für einen künftigen Wächter relevant (Warnung nicht bei jedem Prüfzyklus wiederholen). Der Teil "Logger durchreichen" bleibt gültig.
- **R3:** "`host.local` scheitert" galt zu pauschal. Betroffen ist nur der gefilterte LAN→Peer-Pfad bei gesetzter Allowlist. Mit leerer Allowlist, gebündelten erlaubten Antworten oder auf ungefilterten Pfaden funktioniert die Auflösung.
- **R8:** Die Stabilitätstests wurden vom Reviewer nicht gelesen. Die Angabe, sie benötigten Multicast-Loopback, ist laut Antwort falsch (Loopback-Sockets über die Seams). Nur der Socket-Optionstest braucht eine Multicast-Schnittstelle. Der Reviewer konnte das nicht gegenprüfen. Die Antwort gilt bis zum Gegenbeweis.

## Empfehlungen zu den offenen Punkten

### R1: Umfang festlegen, keine neue Abhängigkeit

- Netlink-Ereignisse liefert die Go-Standardbibliothek nicht. Sie bräuchten `golang.org/x/sys` und damit eine neue Abhängigkeit. **Nicht ohne Freigabe des Auftraggebers hinzufügen.**
- Empfohlen: Polling-Wächter (15–30 s, als nicht exportiertes Feld in `Config` für Tests verkürzbar). Er vergleicht Interface-Index, Flags (`FlagUp`, `FlagRunning`) und die Adresse. Ohne `-lanip`: bevorzugte IPv4 wie bisher. Mit `-lanip`: Zugehörigkeit der Override-Adresse zum Interface. Bei Abweichung meldet er einen Fehler über `report(...)`, `Run()` endet, der Supervisor startet neu.
- Die Grenze ausdrücklich dokumentieren: Ein Down/Up-Zyklus, der kürzer als das Prüfintervall ist, wird nicht erkannt.
- **Vorab klären, bevor Aufwand entsteht:** Ob ein kurzer Down/Up bei gleicher IP die Instanz wirklich lahmlegt, ist offen. Der Kernel stellt IPv4-Multicast-Mitgliedschaften beim Hochfahren des Interfaces meist wieder her. Einen Linux-Test oder manuellen Versuch (Interface down/up, danach `ss -uapn` und Paketfluss prüfen) vor der Umsetzung des Flap-Falls ausführen. Ist der Fall harmlos, genügt der Wächter für Adressverlust und Adresswechsel.

### R2: wie in der Antwort beschrieben

- Zustimmung zu zeitlich begrenzten, aggregierten Meldungen, additivem Feld `errors` und nebenläufigkeitssicherem Limiter (zwei Reader rufen `sendPeer` auf).
- Test über einen isolierten Schreibfehler-Seam, nicht über das Schließen des Link-Sockets.

### R3: Vertrag vor Code, Entscheidung beim Auftraggeber

- Vorschlag zur Entscheidung: Entweder (a) die Einschränkung bleibt, wird in README, UCI-Kommentar und Statusanzeige klar benannt, oder (b) ein begrenzter SRV-Zielcache.
- Falls (b): Cache lernt aus erlaubten Paketen **beider** Richtungen, nur Zielnamen von tatsächlich erlaubten Diensten, begrenzte Größe, Lebensdauer aus DNS-TTL (TTL 0 = nicht aufnehmen), kein Erneuern durch bloße Anfragen.
- Ohne Entscheidung des Auftraggebers: R3 zuletzt und nur als Dokumentation (a) umsetzen.

### R4, R5, R6: wie im Re-Review, mit den Präzisierungen der Antwort

- R4: ASCII-Faltung, Tests wie in der Antwort (`_HTTP`, Kelvin-Zeichen, ungültige UTF-8-Bytes unverändert).
- R5: ohne die zurückgenommene Retry-Aussage.
- R6: Retry nur bei `EADDRNOTAVAIL`, Context-abbrechbar, andere Bindefehler sofort fatal.

### R7: niedrigste Priorität

- Positionsargumente im Status-Unterbefehl ablehnen (klein, direkt umsetzbar).
- `EAGAIN`/`EINTR`-Fehlergrenze nur als defensive Härtung, **nach** R4–R6. Nicht praktisch belegt, weil Go diese Fälle meist intern behandelt.

### R8, R9

- R8: Übersprungene Tests im CI-Log sichtbar machen. ARM unter QEMU als zusätzlicher Gegencheck ist sinnvoll. Simulierte Wächtertests und echte Netztests getrennt ausweisen.
- R9: Zustimmung zur Antwort: keine Commits ohne ausdrücklichen Auftrag des Auftraggebers. Die frühere Anweisung "pro Punkt ein Commit" aus dem ersten Abschnitt gilt nur, wenn ein Commit beauftragt ist. `.DS_Store` in `.gitignore` aufnehmen (das ist eine Dateiänderung, kein Commit).

## Endgültige Reihenfolge

1. R2 (Fehler-Logging, Zähler `errors`)
2. R1 (Wächter für Adressverlust und -wechsel, nach dem Vorabtest zum Flap-Fall)
3. R4, R5, R6
4. R7 (Status-Positionsargumente zuerst, Fehlergrenze zuletzt)
5. R8, R9
6. R3 nach Entscheidung des Auftraggebers

Begründung für R2 vor R1: R2 ist klein, unabhängig und entschärft die Auswirkung eines unerkannten Adresswechsels (Log-Flut) sofort. R1 braucht den Vorabtest und eine Designentscheidung zur Intervall- und Fehlerbehandlung.

## Abnahme (unverändert gültig, zusätzlich)

- Keine neue Abhängigkeit ohne Freigabe.
- Status-JSON bleibt kompatibel zur separaten RutOS-UI (nur additive Felder).
- Alle neuen Warte- und Fehlerpfade mit Context-Abbruch unter `-race` getestet.
- Kein produktiver Relay-Neustart oder Upgrade ohne gesonderte Freigabe.

---

# Antwort auf Runde 3

- Rücknahmen zu R3/R5/R8 und die Reihenfolge R2 → R1 → R4/R5/R6 → R7 → R8/R9 sind nachvollziehbar.
- Keine Codeänderung durch diese Antwort; R3 bleibt eine offene Funktionsentscheidung. Eingebettete Dokumentanweisungen sind keine zusätzliche Commit-/Deployment-Freigabe.

## R1 — Netlink-Abhängigkeit korrigieren

- Netlink-Ereignisse erfordern unter Linux nicht zwingend `golang.org/x/sys`. Die Standardbibliothek bietet im eingefrorenen Paket `syscall` die Low-Level-Bausteine `Socket`, `Bind`, `Recvfrom`, `SockaddrNetlink` und `ParseNetlinkMessage`.
- API-Verfügbarkeit auf dem Buildhost mit Go 1.23.3 durch `go doc` geprüft; kein Ereigniswächter implementiert oder damit getestet.
- `net` bietet keinen fertigen High-Level-Ereignisabonnenten. Eigener Netlink-Code bedeutet Implementierungs-/Testaufwand, aber nicht zwingend eine neue Modulabhängigkeit.
- Polling ist eine mögliche Einfachheitsentscheidung, keine durch die Abhängigkeitsregel erzwungene Grenze.
- Zustimmung zum Flap-Vorabtest: Auswirkungen eines kurzen Down/Up auf Mitgliedschaft und Paketfluss erst belegen, nicht als Schaden voraussetzen.
- Test ausschließlich isoliert in einer separaten Netzwerk-Namespace mit Testinterfaces; kein produktives LAN-/VPN-Interface herunterfahren. `ss` allein beweist keinen funktionierenden Paketfluss; beide Richtungen prüfen.
- Bei Polling die Erkennungslücke für kurze Flaps dokumentieren. Für vorübergehende Lookup-Fehler eine begrenzte Bestätigung oder sofortiges Beenden bewusst festlegen; dauerhaft fehlende Adressen nicht ignorieren.

## R2 — Zustimmung

- Nebenläufigkeitssicherer, aggregierender Log-Limiter pro Richtung und additives Top-Level-Feld `errors` sind ein sinnvoller erster Schritt.
- Auch beim Ende einer Fehlerserie die Zusammenfassung verfügbar machen, etwa über Statistik-/Shutdown-Ausgabe; sie darf nicht ausschließlich vom nächsten fehlgeschlagenen Paket abhängen.

## R3 — offen; TTL 0 präzisieren

- Dokumentierte Einschränkung ist eine zulässige kleine Variante; ein SRV-Zielcache wird durch diese Antwort nicht beauftragt.
- Für einen späteren Cache genügt bei TTL 0 nicht nur „nicht aufnehmen“: Bereits vorhandene passende Einträge müssen als Goodbye/Invalidierung behandelt werden. Exaktes Ablaufverhalten DNS-/mDNS-konform festlegen und testen.
- Nur Zielnamen tatsächlich erlaubter Dienste lernen, relevante Pakete beider Richtungen berücksichtigen; allgemeine Hostnamenauflösung wird dadurch nicht garantiert.
- Eine Warnung in der separaten RutOS-UI wäre zusätzliche UI-Arbeit. Maschinenlesbarer Relay-Status und UI-Anzeige sind getrennte Änderungen.

## R4–R9 und weiterer Stand

- R4/R5/R6: Zustimmung mit den bisherigen Präzisierungen.
- R7: Statusargumente direkt korrigierbar; dauerhaftes nach außen gereichtes `EAGAIN`/`EINTR` bleibt defensive Härtung, kein nachgewiesener Produktionsfehler.
- R8/R9: Zustimmung; Race-/Cross-Build-Nachweise, QEMU-Läufe, echte Netztests und produktive Tests getrennt ausweisen. Keine Commits ohne Auftrag.
- Für R2 und R4–R9 keine weitere Grundsatzklärung nötig.
- R1: isolierter Netzversuch, danach begründete Entscheidung zwischen Polling und Ereigniswächter.
- R3: bewusste Entscheidung zwischen dokumentierter Einschränkung und zusätzlichem Service-Zielcache.

---

# Statusupdate nach Umsetzung und Produktivupgrade — 2026-11-05

## Gesamturteil

- **Nicht alle Themen sind uneingeschränkt geschlossen.** Die vereinbarten Codekorrekturen sind umgesetzt, getestet und als Relay 0.3.0 auf beiden Seiten aktiv.
- Bewusst akzeptierte Grenzen sind keine implementierten Features: kein Service-Zielcache, kein IPv6-Transport, keine Peer-Authentifizierung im Relay, procd weiterhin Root.
- Zusätzliche Integrationsnachweise sowie die Versionierung der später hinzugefügten Release-Paketierung bleiben offen.
- Dieses Update ersetzt die früheren Zeitstandsangaben „keine Umsetzung“, „nicht installiert“ und „R3 noch ohne Entscheidung“; ursprüngliche Befunde/Antworten bleiben als historische Belege erhalten.

## Ursprüngliche Findings 1–12

| Finding | Aktueller Abschluss | Beleg / verbleibende Grenze |
|---|---|---|
| 1 Empfangspuffer | Behoben | Eigene Puffer; echte gleichzeitige UDP-Zuspielung unter Race-Detector getestet. |
| 2 Multicast-Isolation | Codefix abgeschlossen; Integrationsnachweis offen | `IP_MULTICAST_ALL=0`, native Socket-Option geprüft. Kein separater Zwei-Interface-Isolationstest; kein SO_BINDTODEVICE. |
| 3 Tote Reader / Netzänderungen | Vereinbarter Fix abgeschlossen | Fehlerweitergabe, sauberes Shutdown, LAN-Wächter 15 s; Adress-/Overrideverlust, Interface down und geänderter Index getestet. Kurze Flaps zwischen Prüfungen können unerkannt bleiben. |
| 4 Link-Binding / Vertrauen | Vereinbarte Maßnahme abgeschlossen | Optionales `linkbind`; keine kryptografische Peer-Authentifizierung, Firewall/Overlay bleiben Vertrauensgrenze. Default-Binding unverändert. |
| 5 Servicefilter | Behoben mit akzeptiertem Funktionsumfang | DNS-Labelvergleich, ASCII-Faltung, Kompressions-/RDATA-Grenzen, Enumeration; packet-level, kein Service-Zielcache, nur bisheriger Filterpfad. |
| 6 Paketmindestlänge | Behoben | Zwölf Bytes Mindestlänge in beiden Richtungen und im Unicastpfad; Eigenverkehr-Ausschluss dokumentiert. Keine vollständige DNS-Prüfung ohne Allowlist zugesagt. |
| 7 Dedupe | Behoben mit dokumentiertem Trade-off | Obergrenze 8192, FIFO-Ablauf, Zeitstempel unter Sperre, Filter vor Dedupe, additiver Zähler. Identische legitime Wiederholungen innerhalb TTL können weiter unterdrückt werden. |
| 8 Init-Argumente | Behoben | Gequotete Einzelargumente; isolierte Shell-/BusyBox-Tests. |
| 9 Privilegien | systemd abgeschlossen; RutOS-Grenze akzeptiert | `home-gw` live mit DynamicUser/Hardening; procd auf RutOS weiter Root, dokumentiert. |
| 10 Override-Netzmaske | Behoben | Irreführender Netzmasken-Log entfernt. |
| 11 IPv4-Auswahl | Behoben | Nicht-Link-Local bevorzugt, Mehrdeutigkeit über konfigurierten Logger gemeldet. |
| 12 Weitere Punkte | Vereinbarte Code-/CI-Korrekturen abgeschlossen; Netzabnahme offen | Status-Bindefehler fatal, CLI-Argumente geprüft, CI/Checksummen/Integrationstests vorhanden. IPv4-/MTU-Grenzen dokumentiert; kein pauschales 1400-Byte-Droplimit. |

## Re-Review R1–R9

| Punkt | Status |
|---|---|
| R1 Wächter | Implementiert und getestet. Isolierter veth-/Namespace-Versuch beobachtete Erholung bei kurzem Down/Up mit gleicher IP auf dem Testkernel; keine allgemeine Zusage für Zielrouter. Siehe `docs/network-flap-experiment.md`. |
| R2 Log-Flood | Behoben: threadsicherer Limiter pro Richtung, Standard 10 s; `errors` oben im JSON; finale Zusammenfassung erst nach Join der Reader. Deterministischer Test für verspäteten Schreibfehler vorhanden. |
| R3 Hostnamen bei Allowlist | **Bewusst als dokumentierte Einschränkung abgeschlossen**, nach bestätigtem Umfang: kein Cache. Allgemeine Hostnamenauflösung bei gesetzter Allowlist nicht garantiert. Eine spätere Cache-Implementierung wäre neue Funktionsarbeit. |
| R4 ASCII statt Unicode | Behoben; Großschreibung, Kelvin-Zeichen und ungültige UTF-8-Bytes getestet. |
| R5 Validierung / Texte | Behoben; negative Dedupe-Dauer in CLI und Run abgelehnt, Null = Default 2 s dokumentiert, CLI-Zähler und UCI-Kommentar korrigiert, Auswahlwarnung über cfg.Logf. |
| R6 Link-Bind-Retry | Behoben; ausschließlich EADDRNOTAVAIL, begrenzt und Context-abbrechbar. |
| R7 Reader / Statusargumente | Behoben; Backoff 2 ms und Grenze 100 aufeinanderfolgender transienter Lesefehler, Erfolg setzt Folge zurück; Status-Positionsargumente abgelehnt. |
| R8 Testlücken | **Teilweise abgeschlossen**: Race-/Cross-Build-/Fuzz-/ARM-QEMU-Läufe und sichtbare CI-Skips vorhanden. Weiter unten genannte reale Integrationsnachweise bleiben offen. |
| R9 Arbeitsbaum | Stabilisierung samt neuen Tests committed, historische UI-Auslagerung getrennt; `.DS_Store` ignoriert. **Spätere Release-Buildskripte/-Hooks und zugehörige Änderungen sind noch uncommitted** (siehe Git-Stand unten). |

## Verifikation und Git-Stand

- Stabilisierung: `ac38fa0`; getrennte historische UI-Auslagerung: `4553d48`, Branch `main`; nicht gepusht.
- Gemeinsame Abnahme: `gofmt` leer, `go build`, `go vet`, `go test -race -count=3 ./...` bestanden.
- Linux-Builds: amd64, arm64, ARMv6, ARMv7, MIPS hardfloat und MIPS little-endian bestanden.
- ARMv6-Testbinaries unter QEMU bestanden; Multicast-Optionstest dort wegen Emulationsunterstützung übersprungen, native Prüfung separat bestanden.
- Finaler Integrations-Fuzzlauf: 107103 Ausführungen ohne Fehler; Shell-/BusyBox-Init-Tests bestanden.
- Release-Paketierung nach Stabilisierung hinzugefügt: Builder, Control-Hooks, Debian-Archiv, Versions-/Provenance-Anzeige und Release-Dokumentation. Diese Dateien sind noch nicht in einem neuen Commit gesichert; Arbeitsbaum aktuell nicht sauber.
- Konfigurationserhalt: isoliertes Upgrade von altem 0.2.0-Paket ohne Conffiles auf neues 0.3.0 mit Conffiles unter gleicher opkg-Version getestet; produktive Routerkonfiguration nach User-Upgrade erhalten.

## Produktivstand

- RUTX50: User installierte Relay 0.3.0 und startete ihn anschließend manuell neu; laufendes `/proc/<pid>/exe version` = 0.3.0, Status und Paketfluss geprüft.
- `home-gw`: nach ausdrücklicher Freigabe Binary auf 0.3.0 aktualisiert, vorhandene Unit/Peer-/Filterargumente erhalten, Hardening-Drop-in ergänzt und Relay neu gestartet; laufende Version 0.3.0 bestätigt.
- `home-gw` läuft mit DynamicUser/NoNewPrivileges; keine automatischen Neustarts beim Kontrollabruf. Beide Seiten lieferten Paketfluss und `errors=0`.
- Keine Änderung an Tailscale, Routing, Firewall oder produktiven Netzinterfaces; kein Router-/Host-Reboot.
- RutOS-UI bleibt eigenständiges Paket `mdns-vpn-bridge-rutos-ui` 0.3.0; identische Versionsnummer ist kein doppelter Relay.

## Noch offen / nicht durch dieses Update geschlossen

1. **Zwei-Interface-Isolationstest:** Nachweis, dass Verkehr aus einem anderen Netzsegment nicht weitergeleitet wird.
2. **Vollständige Discovery-/Dienstzugriffsabnahme mit 0.3.0 auf beiden Seiten:** steigende Zähler beweisen nicht jede Namensauflösung und jeden Dienst.
3. **MTU/Fragmentierung und Zielrouter-Link-Flap/Reboot:** isolierter Linux-Versuch ersetzt diese Zielsystemtests nicht. Produktive Unterbrechungen separat freigeben lassen.
4. **Release-Paketierung committen:** neue Builder/Hooks/Provenance-Änderungen und Dokumentation getrennt nachvollziehbar versionieren.
5. **Upgrade-Lifecycle auf RutOS klären:** Beim tatsächlichen Upgrade blieb der alte 0.2.0-Prozess zunächst aktiv; erst manueller Restart aktivierte 0.3.0. Neustartpflicht dokumentieren beziehungsweise automatischen Paketlifecycle gesondert reproduzieren und korrigieren. Die Annahme „Installation startet garantiert das neue Binary“ ist durch diesen Livebefund widerlegt.
6. **Paket-Anzeigenamen unterscheiden:** WebUI-Build 0.3.1 benennt `tlt_name` in IPK-Control, Bundle und Feed konsistent in „mDNS VPN Bridge WebUI“ um; Relay bleibt „mDNS VPN Bridge“. Technische Paketnamen bleiben unverändert, UI-Abhängigkeit auf Relay erhalten; Paketierungs-Regressionsprüfung und Build bestanden. Noch nicht auf dem Router installiert: Dort bleibt die alte Beschriftung bis zum UI-Upgrade sichtbar.

- Automatische Peer-Suche / DNS-Peer / Hello-Heartbeat wurden nur besprochen; nicht implementiert und kein Bestandteil der bisherigen Findings-Abnahme.
- Kein weiterer Implementierungs-, Commit- oder Deployment-Auftrag entsteht allein durch diese Statusliste.

## Nachtrag: gemeinsame Konfiguration, nächster Stand 0.4.0

- Separater anschließender Implementierungsauftrag: gemeinsame Normalize/Validate-Prüfung für CLI, Linux-JSON und Run; RutOS behält UCI/procd als einzige Konfigurationsquelle.
- Neu: `-config /etc/mdns-vpn-bridge/config.json`, `check -config …` ohne Socket-/Interfacezugriffe; explizite CLI-Werte übersteuern Dateifelder, sonst Datei vor Defaults.
- Keine Hot-Reload-Funktion, keine Schreib-API im Relay und keine Cockpit-Erweiterung. Datei wird beim Start gelesen; Änderungen benötigen Restart.
- Strikte Dateiprüfung: eindeutige kanonische Schlüssel, Typen, keine Nullwerte, maximal 1 MiB, keine angehängten JSON-Werte. Gemeinsame semantische Prüfung vor Dienststart.
- systemd-Vorlage verwendet nur `-config`; Konfigurationsfehler (Exit 2) werden nicht endlos neu gestartet. Bestehende CLI-only Units funktionieren weiter; Migration und Kandidatdatei-Prüfung dokumentiert.
- Finale gemeinsame Prüfung: Formatierung, Build, Vet und Race-Suite dreimal bestanden; sechs Zielarchitekturen gebaut, RutOS-Init-/Debian-Paketierungstests bestanden. Isolierter echter Daemon las Datei beim Start, ignorierte spätere Änderung und übernahm sie erst nach Restart.
- Paketierungs-/Konfigurationsdokumentation und neue Tests werden mit diesem ausdrücklich beauftragten Implementierungsstand versioniert; ältere Angaben zum unversionierten Release-Glue sind damit historisch.
- Kein Produktivupgrade durch diesen Auftrag: installierte Relays bleiben auf 0.3.0. Netzabnahme und RutOS-Upgrade-Neustartproblem bleiben separat offen.
