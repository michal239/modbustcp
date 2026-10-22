// Command simulator is a Modbus TCP server that replays converter readings
// recorded from the real device (data.json), looping forever.
//
// Register map (big endian, high word first) matches the device:
//   - input registers: 18 REAL values at 0..35, 3 UDINT values at 36..41 (replayed);
//   - holding registers: 15 REAL at 0..29, 6 UDINT at 30..41 (R/W settings kept
//     in memory, starting from sample defaults, see settings).
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/simonvetter/modbus"
)

//go:embed data.json
var dataJSON []byte

const (
	timeLayout   = "2006-01-02 15:04:05.999999-07"
	defaultDelay = 500 * time.Millisecond
	registers    = 18*2 + 3*2

	// Holding registers 40000..40041: 15 REAL + 6 UDINT settings.
	holdingRegisters = 15*2 + 6*2
)

// setting is one R/W holding register value (2 registers, at index*2).
type setting struct {
	name  string
	real  bool // REAL (float32) or UDINT (uint32)
	value float64
}

// settings in register order from 40000. Values are sample defaults, not taken
// from the real device.
var settings = []setting{
	{"rUHVmax", true, 800},
	{"rULVmax", true, 50},
	{"rUHVmin", true, 0},
	{"rULVmin", true, 0},
	{"rIHVmax", true, 50},
	{"rILVmax", true, 50},
	{"rSPSman", true, 0},
	{"rDPSman", true, 0},
	{"rEref", true, 0},
	{"rKp_U", true, 1},
	{"rki_U", true, 0.1},
	{"rKp_I", true, 1},
	{"rki_I", true, 0.1},
	{"rUHVstart", true, 0},
	{"rULVstart", true, 0},
	{"uState", false, 0},
	{"uReg_side", false, 0},
	{"uConvertOnOff", false, 0},
	{"uConvertSleep", false, 0},
	{"uID", false, 1},
	{"uMaster", false, 0},
}

func (s *setting) encode(regs []uint16) {
	raw := uint32(s.value)
	if s.real {
		raw = math.Float32bits(float32(s.value))
	}
	regs[0], regs[1] = uint16(raw>>16), uint16(raw) // high word first
}

func (s *setting) format(regs []uint16) string {
	raw := uint32(regs[0])<<16 | uint32(regs[1])
	if s.real {
		return fmt.Sprintf("%s = %g", s.name, math.Float32frombits(raw))
	}
	return fmt.Sprintf("%s = %d", s.name, raw)
}

func defaultHolding() (regs [holdingRegisters]uint16) {
	for i := range settings {
		settings[i].encode(regs[i*2 : i*2+2])
	}
	return regs
}

type record struct {
	Time string `json:"time"`

	// REAL, in register order.
	UHV     float32 `json:"uhv"`
	ULV     float32 `json:"ulv"`
	IHV     float32 `json:"ihv"`
	ILV     float32 `json:"ilv"`
	PHV     float32 `json:"phv"`
	PLV     float32 `json:"plv"`
	TempMax float32 `json:"temp_max"`
	Tmos1   float32 `json:"tmos1"`
	Tmos2   float32 `json:"tmos2"`
	Tmos3   float32 `json:"tmos3"`
	Tmos4   float32 `json:"tmos4"`
	Tter1   float32 `json:"tter1"`
	Tter2   float32 `json:"tter2"`
	Tter3   float32 `json:"tter3"`
	Tter4   float32 `json:"tter4"`
	PIe     float32 `json:"pie"`
	SPS     float32 `json:"sps"`
	DPS     float32 `json:"dps"`

	// UDINT, in register order (exported from Postgres BIGINT as strings).
	Status  uint32 `json:"status,string"`
	Alarms  uint32 `json:"alarms,string"`
	Version uint32 `json:"version,string"`
}

type frame struct {
	at   time.Time
	regs [registers]uint16
}

func (r *record) toFrame() (frame, error) {
	at, err := time.Parse(timeLayout, r.Time)
	if err != nil {
		return frame{}, err
	}

	f := frame{at: at}
	reals := []float32{
		r.UHV, r.ULV, r.IHV, r.ILV, r.PHV, r.PLV, r.TempMax,
		r.Tmos1, r.Tmos2, r.Tmos3, r.Tmos4,
		r.Tter1, r.Tter2, r.Tter3, r.Tter4,
		r.PIe, r.SPS, r.DPS,
	}
	uints := []uint32{r.Status, r.Alarms, r.Version}

	i := 0
	put := func(v uint32) { // high word first
		f.regs[i] = uint16(v >> 16)
		f.regs[i+1] = uint16(v)
		i += 2
	}
	for _, v := range reals {
		put(math.Float32bits(v))
	}
	for _, v := range uints {
		put(v)
	}
	return f, nil
}

func loadFrames() ([]frame, error) {
	var records []record
	if err := json.Unmarshal(dataJSON, &records); err != nil {
		return nil, err
	}

	frames := make([]frame, 0, len(records))
	for i := range records {
		f, err := records[i].toFrame()
		if err != nil {
			return nil, err
		}
		frames = append(frames, f)
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].at.Before(frames[j].at) })
	return frames, nil
}

// handler serves the current frame as input registers and in-memory holding
// registers; coils and discrete inputs are rejected.
type handler struct {
	unitID uint8

	mu      sync.RWMutex
	cur     *frame
	holding [holdingRegisters]uint16
}

func (h *handler) set(f *frame) {
	h.mu.Lock()
	h.cur = f
	h.mu.Unlock()
}

func (h *handler) HandleInputRegisters(req *modbus.InputRegistersRequest) ([]uint16, error) {
	if req.UnitId != h.unitID {
		return nil, modbus.ErrBadUnitId
	}
	end := int(req.Addr) + int(req.Quantity)
	if end > registers {
		return nil, modbus.ErrIllegalDataAddress
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]uint16(nil), h.cur.regs[req.Addr:end]...), nil
}

func (h *handler) HandleCoils(*modbus.CoilsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalFunction
}

func (h *handler) HandleDiscreteInputs(*modbus.DiscreteInputsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalFunction
}

// HandleHoldingRegisters serves the R/W settings, kept in memory (initially zero).
func (h *handler) HandleHoldingRegisters(req *modbus.HoldingRegistersRequest) ([]uint16, error) {
	if req.UnitId != h.unitID {
		return nil, modbus.ErrBadUnitId
	}
	end := int(req.Addr) + int(req.Quantity)
	if end > holdingRegisters {
		return nil, modbus.ErrIllegalDataAddress
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if req.IsWrite {
		copy(h.holding[req.Addr:end], req.Args)
		log.Printf("write holding registers %d..%d from %s", req.Addr, end-1, req.ClientAddr)
		for i := int(req.Addr) / 2; i*2 < end; i++ {
			log.Printf("  %s", settings[i].format(h.holding[i*2:i*2+2]))
		}
	}
	return append([]uint16(nil), h.holding[req.Addr:end]...), nil
}

// replay advances through frames with the recorded time gaps, looping forever.
func replay(h *handler, frames []frame, stop <-chan struct{}) {
	for i := 0; ; i = (i + 1) % len(frames) {
		h.set(&frames[i])

		delay := defaultDelay
		if next := i + 1; next < len(frames) {
			if d := frames[next].at.Sub(frames[i].at); d > 0 {
				delay = d
			}
		}

		select {
		case <-stop:
			return
		case <-time.After(delay):
		}
	}
}

func main() {
	listen := flag.String("listen", "tcp://0.0.0.0:5502", "Modbus TCP listen URL")
	unitID := flag.Uint("unit", 1, "Modbus unit id")
	flag.Parse()

	frames, err := loadFrames()
	if err != nil {
		log.Fatalf("load data: %v", err)
	}
	if len(frames) == 0 {
		log.Fatal("no data")
	}

	h := &handler{unitID: uint8(*unitID), cur: &frames[0], holding: defaultHolding()}
	server, err := modbus.NewServer(&modbus.ServerConfiguration{
		URL:        *listen,
		Timeout:    30 * time.Second,
		MaxClients: 5,
	}, h)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}
	if err = server.Start(); err != nil {
		log.Fatalf("start server: %v", err)
	}
	defer server.Stop()
	log.Printf("simulator listening on %s (unit %d), replaying %d frames", *listen, *unitID, len(frames))

	stop := make(chan struct{})
	go replay(h, frames, stop)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	close(stop)
	log.Println("shutting down")
}
