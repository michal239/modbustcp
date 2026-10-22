# Collector Modbus TCP → TimescaleDB

Program cyklicznie odczytuje rejestry przekształtnika DC-DC przez Modbus TCP i zapisuje je do TimescaleDB. Dane są widoczne w Grafanie.

## Konfiguracja

Ścieżkę do pliku konfiguracyjnego podaje zmienna środowiskowa `CONFIG`.

```yaml
host: 10.0.10.21:502          # adres urządzenia (port domyślnie 502)
polling_interval_ms: 500      # interwał odczytu w ms
db_url: postgres://postgres:postgres@localhost:5432/app?sslmode=disable
device: ModbusTCP             # nazwa urządzenia w bazie

registers:
  - { name: rUHV,    address: 0, type: float32, every: 1, unit: V, description: "Napięcie obwodu pośredniczącego DC" }
  - { name: rUHVmax, kind: holding, address: 0, type: float32, every: 8, unit: V }
  # ...
```

Pola rejestru:

- `name`: nazwa rejestru w bazie;
- `kind`: `input` (3xxxx, domyślnie) lub `holding` (4xxxx);
- `address`: offset bez prefiksu, np. `30036` → `36`, `40012` → `12`;
- `type`: `float32` lub `uint32`;
- `every`: zapisuj co N-ty odczyt (domyślnie 1);
- `unit`, `description`: opcjonalne.

## Uruchomienie w Dockerze

W `config.yaml` adres bazy to `timescaledb:5432`:

```yaml
db_url: postgres://postgres:postgres@timescaledb:5432/app?sslmode=disable
```

Uruchomienie:

```sh
docker compose up
```

Grafana działa pod adresem <http://localhost:3000> (login `admin` / `admin`).

Przydatne komendy:

```sh
docker compose logs -f collector           # logi
docker compose up -d --build collector     # przebudowanie po zmianie kodu
docker compose restart collector           # restart po zmianie config.yaml
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
