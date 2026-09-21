// Copyright 2023 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package vm

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestMemoryCopy(t *testing.T) {
	// Test cases from https://eips.ethereum.org/EIPS/eip-5656#test-cases
	for i, tc := range []struct {
		dst, src, len uint64
		pre           string
		want          string
	}{
		{ // MCOPY 0 32 32 - copy 32 bytes from offset 32 to offset 0.
			0, 32, 32,
			"0000000000000000000000000000000000000000000000000000000000000000 000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
			"000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f 000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
		},

		{ // MCOPY 0 0 32 - copy 32 bytes from offset 0 to offset 0.
			0, 0, 32,
			"0101010101010101010101010101010101010101010101010101010101010101",
			"0101010101010101010101010101010101010101010101010101010101010101",
		},
		{ // MCOPY 0 1 8 - copy 8 bytes from offset 1 to offset 0 (overlapping).
			0, 1, 8,
			"000102030405060708 000000000000000000000000000000000000000000000000",
			"010203040506070808 000000000000000000000000000000000000000000000000",
		},
		{ // MCOPY 1 0 8 - copy 8 bytes from offset 0 to offset 1 (overlapping).
			1, 0, 8,
			"000102030405060708 000000000000000000000000000000000000000000000000",
			"000001020304050607 000000000000000000000000000000000000000000000000",
		},
		// Tests below are not in the EIP, but maybe should be added
		{ // MCOPY 0xFFFFFFFFFFFF 0xFFFFFFFFFFFF 0 - copy zero bytes from out-of-bounds index(overlapping).
			0xFFFFFFFFFFFF, 0xFFFFFFFFFFFF, 0,
			"11",
			"11",
		},
		{ // MCOPY 0xFFFFFFFFFFFF 0 0 - copy zero bytes from start of mem to out-of-bounds.
			0xFFFFFFFFFFFF, 0, 0,
			"11",
			"11",
		},
		{ // MCOPY 0 0xFFFFFFFFFFFF 0 - copy zero bytes from out-of-bounds to start of mem
			0, 0xFFFFFFFFFFFF, 0,
			"11",
			"11",
		},
	} {
		m := NewMemory()
		// Clean spaces
		data := common.FromHex(strings.ReplaceAll(tc.pre, " ", ""))
		// Set pre
		m.Resize(uint64(len(data)))
		m.Set(0, uint64(len(data)), data)
		// Do the copy
		m.Copy(tc.dst, tc.src, tc.len)
		want := common.FromHex(strings.ReplaceAll(tc.want, " ", ""))
		if have := m.store; !bytes.Equal(want, have) {
			t.Errorf("case %d: want: %#x\nhave: %#x\n", i, want, have)
		}
	}
}

func BenchmarkResize(b *testing.B) {
	memory := NewMemory()
	for i := range b.N {
		memory.Resize(uint64(i))
	}
}

// TestMemoryViewCapacityClamped verifies that the slices handed out by GetPtr,
// Data and Store cannot be appended into the unallocated tail of the backing
// array. Resize reslices within capacity, so a stray append by a caller (e.g. a
// custom precompile) would otherwise make newly expanded memory read non-zero,
// breaking the EVM invariant that fresh memory is zeroed.
func TestMemoryViewCapacityClamped(t *testing.T) {
	m := NewMemory()
	m.Resize(128)          // grow the backing array...
	m.store = m.store[:32] // ...then shrink the view, leaving spare capacity
	if cap(m.store) < 128 {
		t.Fatalf("test precondition: want spare capacity, have cap %d", cap(m.store))
	}

	for _, tc := range []struct {
		name string
		view []byte
	}{
		{"GetPtr", m.GetPtr(0, 32)},
		{"GetPtr offset", m.GetPtr(16, 16)},
		{"Data", m.Data()},
		{"Store", m.Store()},
	} {
		if have, want := cap(tc.view), len(tc.view); have != want {
			t.Errorf("%s: capacity not clamped: have %d, want %d", tc.name, have, want)
		}
		// Appending must reallocate rather than write into m.store's tail.
		_ = append(tc.view, 0xff)
	}

	m.Resize(128)
	if want := make([]byte, 96); !bytes.Equal(m.store[32:], want) {
		t.Errorf("expanded memory not zeroed: %#x", m.store[32:])
	}
}
