package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/simonvetter/modbus"
)

type holdingWrite struct {
	reg   *Register
	value float64
}

type holdingRequest struct {
	writes []holdingWrite
	reply  chan holdingResponse
}

type holdingResponse struct {
	values map[string]float64
	err    error
}

var errNotConnected = errors.New("not connected to the Modbus device")

func waitRetry(ctx context.Context, d time.Duration, reqs <-chan holdingRequest) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case req := <-reqs:
			req.reply <- holdingResponse{err: errNotConnected}
		}
	}
}

func holdingBlocks(blocks []block) []block {
	var out []block
	for _, b := range blocks {
		if b.kind == KindHolding {
			out = append(out, b)
		}
	}
	return out
}

func handleHolding(client *modbus.ModbusClient, blocks []block, req holdingRequest) error {
	writeErr := writeHolding(client, req.writes)
	values, readErr := readHolding(client, blocks)
	err := writeErr
	if err == nil {
		err = readErr
	}
	req.reply <- holdingResponse{values: values, err: err}
	return err
}

func writeHolding(client *modbus.ModbusClient, writes []holdingWrite) error {
	for _, w := range writes {
		var err error
		if w.reg.Type == TypeFloat32 {
			err = client.WriteFloat32(w.reg.Address, float32(w.value))
		} else {
			err = client.WriteUint32(w.reg.Address, uint32(w.value))
		}
		if err != nil {
			return fmt.Errorf("write %s: %w", w.reg.Name, err)
		}
	}
	return nil
}

func readHolding(client *modbus.ModbusClient, blocks []block) (map[string]float64, error) {
	values := make(map[string]float64)
	for i := range blocks {
		b := &blocks[i]
		words, err := client.ReadRegisters(b.start, b.size, modbus.HOLDING_REGISTER)
		if err != nil {
			return values, fmt.Errorf("read holding registers %d..%d: %w", b.start, b.start+b.size-1, err)
		}
		for j := range b.regs {
			values[b.regs[j].Name] = b.regs[j].decode(b, words)
		}
	}
	return values, nil
}
