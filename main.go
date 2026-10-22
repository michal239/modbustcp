package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	migrate "github.com/rubenv/sql-migrate"
	"github.com/simonvetter/modbus"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	unitID = 1

	dbMinConns = 2
	dbMaxConns = 10
)

// runMigrations applies pending migrations from migrations/ (embedded in the binary).
// sql-migrate needs *sql.DB, so it gets a database/sql wrapper over the same pool.
func runMigrations(pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	src := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: migrationsFS,
		Root:       "migrations",
	}
	n, err := migrate.Exec(db, "postgres", src, migrate.Up)
	if err != nil {
		return err
	}
	log.Printf("applied %d migration(s)", n)
	return nil
}

// insertReadings stores one poll as rows of (time, device, register, value).
// rows are []any{time, device, register, value}.
func insertReadings(ctx context.Context, pool *pgxpool.Pool, rows [][]any) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := pool.CopyFrom(ctx,
		pgx.Identifier{"register_readings"},
		[]string{"time", "device", "register", "value"},
		pgx.CopyFromRows(rows),
	)
	return err
}

func main() {
	fmt.Println(os.Args)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	modbusURL := cfg.ModbusURL()
	log.Printf("config: host=%s, device=%s, polling interval=%s, %d registers",
		cfg.Address(), cfg.Device, cfg.PollInterval(), len(cfg.Registers))

	blocks := planBlocks(cfg.Registers)
	for _, b := range blocks {
		log.Printf("read block: %s registers %d..%d (%d registers)", b.kind, b.start, b.start+b.size-1, len(b.regs))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	poolCfg, err := pgxpool.ParseConfig(cfg.DBURL)
	if err != nil {
		log.Fatalf("parse db config: %v", err)
	}
	poolCfg.MinConns = dbMinConns
	poolCfg.MaxConns = dbMaxConns

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		log.Fatalf("create db pool: %v", err)
	}
	defer pool.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = pool.Ping(pingCtx)
	cancel()
	if err != nil {
		log.Fatalf("ping timescaledb: %v", err)
	}
	log.Printf("connected to TimescaleDB (min %d, max %d conns)", dbMinConns, dbMaxConns)

	if err = runMigrations(pool); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	client, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL:     modbusURL,
		Timeout: 2 * time.Second,
	})
	if err != nil {
		log.Fatalf("modbus client config: %v", err)
	}

	// Retry until the device is reachable.
	for {
		if err = client.Open(); err == nil {
			break
		}
		log.Printf("connect to %s failed: %v, retrying in 2s", modbusURL, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	defer client.Close()
	log.Printf("connected to Modbus TCP device at %s (unit %d)", modbusURL, unitID)
	if err = client.SetUnitId(unitID); err != nil {
		log.Fatalf("set unit id: %v", err)
	}

	if err = client.SetEncoding(modbus.BIG_ENDIAN, modbus.HIGH_WORD_FIRST); err != nil {
		log.Fatalf("set encoding: %v", err)
	}

	ticker := time.NewTicker(cfg.PollInterval())
	defer ticker.Stop()

	var poll uint64 // poll counter for per-register "every"

	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			return
		case <-ticker.C:
			reqStart := time.Now()
			var rows [][]any
			for i := range blocks {
				b := &blocks[i]
				if !b.anyDue(poll) {
					continue
				}
				words, err := client.ReadRegisters(b.start, b.size, b.kind.modbusType())
				if err != nil {
					log.Printf("cannot read %s registers %d..%d: %v", b.kind, b.start, b.start+b.size-1, err)
					continue
				}
				for j := range b.regs {
					r := &b.regs[j]
					if !r.due(poll) {
						continue
					}
					v := r.decode(b, words)
					//if r.Type == TypeUint32 {
					//	//log.Printf("%-9s = %d (0x%08X)", r.Name, uint32(v), uint32(v))
					//} else {
					//	//log.Printf("%-9s = %.3f", r.Name, v)
					//}
					rows = append(rows, []any{reqStart, cfg.Device, r.Name, v})
				}
			}
			poll++
			fmt.Println("registers read successfully, response time", time.Since(reqStart))

			if len(rows) == 0 {
				continue
			}
			if err := insertReadings(ctx, pool, rows); err != nil {
				log.Printf("insert readings: %v", err)
			}
		}
	}
}
