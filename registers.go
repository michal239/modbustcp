package main

import (
	"math"
	"slices"
	"strconv"
)

// maxBlockSize is the Modbus limit of registers per read request.
const maxBlockSize = 125

// block is a contiguous range of registers of one kind read with one request.
type block struct {
	kind  RegisterKind
	start uint16
	size  uint16
	regs  []Register
}

// planBlocks groups registers into contiguous blocks of the same kind. Registers are
// merged only when adjacent (no gaps), since devices may reject reads spanning
// unmapped addresses.
func planBlocks(regs []Register) []block {
	sorted := slices.Clone(regs)
	slices.SortFunc(sorted, compareRegisters)

	var blocks []block
	for _, r := range sorted {
		if n := len(blocks); n > 0 {
			b := &blocks[n-1]
			if r.Kind == b.kind && r.Address == b.start+b.size && int(b.size)+r.Size() <= maxBlockSize {
				b.size += uint16(r.Size())
				b.regs = append(b.regs, r)
				continue
			}
		}
		blocks = append(blocks, block{kind: r.Kind, start: r.Address, size: uint16(r.Size()), regs: []Register{r}})
	}
	return blocks
}

// due reports whether the register is stored on the given poll number.
func (r *Register) due(poll uint64) bool {
	return poll%uint64(r.Every) == 0
}

// anyDue reports whether any register in the block is stored on the given poll number.
func (b *block) anyDue(poll uint64) bool {
	for i := range b.regs {
		if b.regs[i].due(poll) {
			return true
		}
	}
	return false
}

// decode extracts the register's value from the block's raw words
// (bytes already ordered by the client, high word first).
func (r *Register) decode(b *block, words []uint16) float64 {
	off := r.Address - b.start
	raw := uint32(words[off])<<16 | uint32(words[off+1])
	if r.Type == TypeFloat32 {
		return float32ToFloat64(math.Float32frombits(raw))
	}
	return float64(raw)
}

// float32ToFloat64 converts via the shortest decimal form, so 24.020405 is stored
// as 24.020405 rather than 24.020404815673828.
func float32ToFloat64(f float32) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(float64(f), 'g', -1, 32), 64)
	return v
}
