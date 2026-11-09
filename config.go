package main

import (
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/simonvetter/modbus"
	"gopkg.in/yaml.v3"
)

const (
	defaultModbusPort = "502"
	defaultDBURL      = "postgres://postgres:postgres@localhost:5432/app?sslmode=disable"
	defaultHTTPListen = ":8080"
)

type Config struct {
	// Modbus TCP device address, "host" or "host:port" (port defaults to 502).
	Host string `yaml:"host"`
	// Polling interval in milliseconds.
	PollingIntervalMs int `yaml:"polling_interval_ms"`
	// TimescaleDB connection string (optional).
	DBURL string `yaml:"db_url"`
	// Device name stored with every reading.
	Device string `yaml:"device"`
	// Registers to poll.
	Registers []Register `yaml:"registers"`

	HTTPListen string `yaml:"http_listen"`
}

type RegisterType string

const (
	TypeFloat32 RegisterType = "float32"
	TypeUint32  RegisterType = "uint32"
)

// RegisterKind is the Modbus register table.
type RegisterKind string

const (
	KindInput   RegisterKind = "input"   // 3xxxx, function 0x04
	KindHolding RegisterKind = "holding" // 4xxxx, function 0x03
)

func (k RegisterKind) modbusType() modbus.RegType {
	if k == KindHolding {
		return modbus.HOLDING_REGISTER
	}
	return modbus.INPUT_REGISTER
}

type Register struct {
	Name string `yaml:"name"`
	// Register table (default input).
	Kind RegisterKind `yaml:"kind"`
	// Offset within the table (30000/40000 + Address in device docs).
	Address uint16       `yaml:"address"`
	Type    RegisterType `yaml:"type"`
	// Store every Nth poll (default 1).
	Every int `yaml:"every"`
	// Optional, informational.
	Unit        string `yaml:"unit"`
	Description string `yaml:"description"`

	Min *float64 `yaml:"min"`
	Max *float64 `yaml:"max"`
}

// Size returns the number of 16-bit registers the value occupies.
func (r *Register) Size() int {
	return 2 // float32 and uint32 both span two registers
}

func (r *Register) validateRange() error {
	if r.Min == nil && r.Max == nil {
		return nil
	}
	if r.Kind != KindHolding {
		return errors.New("min/max are allowed only for holding registers")
	}
	for _, b := range []*float64{r.Min, r.Max} {
		if b != nil {
			if err := r.checkType(*b); err != nil {
				return fmt.Errorf("min/max: %w", err)
			}
		}
	}
	if r.Min != nil && r.Max != nil && *r.Min > *r.Max {
		return fmt.Errorf("min %g is greater than max %g", *r.Min, *r.Max)
	}
	return nil
}

func (r *Register) checkType(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return errors.New("value must be a finite number")
	}
	if r.Type == TypeUint32 {
		if v != math.Trunc(v) || v < 0 || v > math.MaxUint32 {
			return fmt.Errorf("%g is not a valid uint32", v)
		}
		return nil
	}
	if math.Abs(v) > math.MaxFloat32 {
		return fmt.Errorf("%g is out of float32 range", v)
	}
	return nil
}

func (r *Register) checkValue(v float64) error {
	if err := r.checkType(v); err != nil {
		return err
	}
	if r.Min != nil && v < *r.Min {
		return fmt.Errorf("%g is below min %g", v, *r.Min)
	}
	if r.Max != nil && v > *r.Max {
		return fmt.Errorf("%g is above max %g", v, *r.Max)
	}
	return nil
}

// compareRegisters orders registers by kind, then address.
func compareRegisters(a, b Register) int {
	if c := strings.Compare(string(a.Kind), string(b.Kind)); c != 0 {
		return c
	}
	return int(a.Address) - int(b.Address)
}

func validateRegisters(regs []Register) error {
	if len(regs) == 0 {
		return errors.New("at least one register is required")
	}

	names := make(map[string]bool, len(regs))
	for i := range regs {
		r := &regs[i]
		if r.Name == "" {
			return fmt.Errorf("register #%d: name is required", i+1)
		}
		if names[r.Name] {
			return fmt.Errorf("register %q: duplicate name", r.Name)
		}
		names[r.Name] = true

		if r.Kind == "" {
			r.Kind = KindInput
		}
		if r.Kind != KindInput && r.Kind != KindHolding {
			return fmt.Errorf("register %q: kind must be %s or %s", r.Name, KindInput, KindHolding)
		}
		if r.Type != TypeFloat32 && r.Type != TypeUint32 {
			return fmt.Errorf("register %q: type must be %s or %s", r.Name, TypeFloat32, TypeUint32)
		}
		if int(r.Address)+r.Size() > 1<<16 {
			return fmt.Errorf("register %q: address %d out of range", r.Name, r.Address)
		}
		if r.Every == 0 {
			r.Every = 1
		}
		if r.Every < 0 {
			return fmt.Errorf("register %q: every must be >= 1", r.Name)
		}
		if err := r.validateRange(); err != nil {
			return fmt.Errorf("register %q: %w", r.Name, err)
		}
	}

	sorted := slices.Clone(regs)
	slices.SortFunc(sorted, compareRegisters)
	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		if prev.Kind == cur.Kind && int(prev.Address)+prev.Size() > int(cur.Address) {
			return fmt.Errorf("registers %q and %q overlap", prev.Name, cur.Name)
		}
	}
	return nil
}

// loadConfig reads the YAML config from the path given in the CONFIG env variable.
func loadConfig() (*Config, error) {
	path := os.Getenv("CONFIG")
	if path == "" {
		return nil, errors.New("CONFIG env variable is not set")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if cfg.Host == "" {
		return nil, errors.New("host is required")
	}
	if cfg.PollingIntervalMs <= 0 {
		return nil, errors.New("polling_interval_ms must be > 0")
	}
	if cfg.DBURL == "" {
		cfg.DBURL = defaultDBURL
	}
	if cfg.Device == "" {
		return nil, errors.New("device is required")
	}
	if cfg.HTTPListen == "" {
		cfg.HTTPListen = defaultHTTPListen
	}
	if err := validateRegisters(cfg.Registers); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) PollInterval() time.Duration {
	return time.Duration(c.PollingIntervalMs) * time.Millisecond
}

// Address returns host:port, adding the default Modbus port when missing.
func (c *Config) Address() string {
	if _, _, err := net.SplitHostPort(c.Host); err == nil {
		return c.Host
	}
	return net.JoinHostPort(c.Host, defaultModbusPort)
}

func (c *Config) ModbusURL() string {
	return "tcp://" + c.Address()
}
