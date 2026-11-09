# Collector Modbus TCP → TimescaleDB

Program cyklicznie odczytuje rejestry przekształtnika DC-DC przez Modbus TCP i zapisuje je do TimescaleDB. Dane są widoczne w Grafanie.

Gdy połączenie z urządzeniem zostanie zerwane (błąd sieci, uszkodzona ramka albo 3 timeouty z rzędu), collector zamyka je i co 2 s próbuje połączyć się ponownie. W tym czasie panel rejestrów holding zgłasza brak połączenia.

## Konfiguracja

Ścieżkę do pliku konfiguracyjnego podaje zmienna środowiskowa `CONFIG`.

```yaml
host: 10.0.10.21:502          # adres urządzenia (port domyślnie 502)
polling_interval_ms: 500      # interwał odczytu w ms
db_url: postgres://postgres:postgres@localhost:5432/app?sslmode=disable
device: ModbusTCP             # nazwa urządzenia w bazie
http_listen: ":8080"          # adres panelu rejestrów holding (domyślnie :8080)

registers:
  - { name: rUHV,    address: 0, type: float32, every: 1, unit: V, description: "Napięcie obwodu pośredniczącego DC" }
  - { name: rUHVmax, kind: holding, address: 0, type: float32, every: 8, min: 0, max: 800, unit: V }
  # ...
```

Pola rejestru:

- `name`: nazwa rejestru w bazie;
- `kind`: `input` (3xxxx, domyślnie) lub `holding` (4xxxx);
- `address`: offset bez prefiksu, np. `30036` → `36`, `40012` → `12`;
- `type`: `float32` lub `uint32`;
- `every`: zapisuj co N-ty odczyt (domyślnie 1);
- `unit`, `description`: opcjonalne;
- `min`, `max`: opcjonalny dozwolony zakres zapisu, tylko dla rejestrów `holding`.

Zakresy `min`/`max` w dołączonym `config.yaml` są wartościami zastępczymi. Przed użyciem na prawdziwym urządzeniu trzeba je zweryfikować z dokumentacją przekształtnika.

## Panel rejestrów holding

Collector udostępnia stronę do zmiany rejestrów holding pod adresem z `http_listen`, domyślnie <http://localhost:8080>. Serwer startuje zaraz po wczytaniu konfiguracji, a program otwiera wtedy stronę w domyślnej przeglądarce. W Dockerze otwarcie przeglądarki się nie uda: w logu pojawi się tylko informacja, a stronę trzeba otworzyć ręcznie.

Strona działa także wtedy, gdy nie ma połączenia z urządzeniem Modbus. Pokazuje wtedy listę rejestrów bez wartości i błąd połączenia. Po nawiązaniu połączenia wystarczy kliknąć **Odśwież**.

1. Strona odczytuje aktualne wartości z urządzenia (przycisk **Odśwież** robi to ponownie).
2. Zmienione pola są podświetlone. Wartości są sprawdzane pod kątem typu (`uint32`: liczba całkowita) i zakresu `min`/`max`. Jako separator dziesiętny można użyć przecinka.
3. **Pokaż zmiany** ponownie odczytuje urządzenie i pokazuje diff: wartość obecna → nowa. Jeśli wartość na urządzeniu zmieniła się od wczytania strony, diff to zaznacza.
4. **Zapisz na urządzeniu** wysyła zapis. Po zapisie collector od razu odczytuje rejestry ponownie, a strona pokazuje, czy urządzenie zwraca wysłane wartości.

Zapis i odczyt przechodzą przez główną pętlę collectora (kanał), więc nie przeplatają się z cyklicznym odczytem na tym samym połączeniu Modbus. Serwer powtarza walidację niezależnie od strony. Wartość spoza zakresu lub nieznany rejestr kończy się błędem i nic nie jest zapisywane.

API:

```sh
curl localhost:8080/api/holding                                             # odczyt
curl -X POST -d '{"values":{"rKp_U":2.5}}' localhost:8080/api/holding       # zapis
```

Panel nie ma autoryzacji: każdy, kto ma dostęp do portu, może zmieniać nastawy. Aby ograniczyć dostęp do lokalnej maszyny, ustaw `http_listen: "127.0.0.1:8080"` (przy uruchomieniu natywnym).

## Uruchomienie w Dockerze

W `config.yaml` adres bazy to `timescaledb:5432`:

```yaml
db_url: postgres://postgres:postgres@timescaledb:5432/app?sslmode=disable
```

Uruchomienie:

```sh
docker compose up
```

Grafana działa pod adresem <http://localhost:3000> (login `admin` / `admin`), a panel rejestrów holding pod <http://localhost:8080>.

Przydatne komendy:

```sh
docker compose logs -f collector           # logi
docker compose restart collector           # restart po zmianie kodu lub config.yaml
docker compose up -d --build collector     # przebudowanie obrazu po zmianie zależności (go.mod)
docker compose down                        # zatrzymanie
```

## Uruchomienie natywne

1. Uruchom program z katalogu `dist/`:

   ```sh
   # Linux
   CONFIG=config.yaml ./dist/collector-linux-amd64

   # macOS
   CONFIG=config.yaml ./dist/collector-darwin-arm64
   ```

   ```powershell
   # Windows (PowerShell)
   $env:CONFIG = "config.yaml"
   .\dist\collector-windows-amd64.exe
   ```

## Kompilacja

Wymagany jest Go 1.25+ oraz [Task](https://taskfile.dev) (`go install github.com/go-task/task/v3/cmd/task@latest`).

```sh
task build            # linux/amd64, windows/amd64, darwin/arm64 → dist/
task build:linux      # tylko jedna platforma (też build:windows, build:darwin)
task clean            # usunięcie dist/
```

Bez Taska:

```sh
CONFIG=config.yaml go run .
```
