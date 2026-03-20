// Copyright 2021 The go-ethereum Authors
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

package logger

import (
	"encoding/json"
	"errors"
	"math/big"
	"runtime"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

type dummyStatedb struct {
	state.StateDB
}

func (*dummyStatedb) GetRefund() uint64                                    { return 1337 }
func (*dummyStatedb) GetState(_ common.Address, _ common.Hash) common.Hash { return common.Hash{} }
func (*dummyStatedb) SetState(_ common.Address, _ common.Hash, _ common.Hash) common.Hash {
	return common.Hash{}
}

func TestStoreCapture(t *testing.T) {
	var (
		logger   = NewStructLogger(nil)
		evm      = vm.NewEVM(vm.BlockContext{}, &dummyStatedb{}, params.TestChainConfig, vm.Config{Tracer: logger.Hooks()}, nil)
		contract = vm.NewContract(common.Address{}, common.Address{}, new(uint256.Int), 100000, nil)
	)
	contract.Code = []byte{byte(vm.PUSH1), 0x1, byte(vm.PUSH1), 0x0, byte(vm.SSTORE)}
	var index common.Hash
	logger.OnTxStart(evm.GetVMContext(), nil, common.Address{})
	_, err := evm.Interpreter().Run(vm.CALL, contract, []byte{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(logger.storage[contract.Address()]) == 0 {
		t.Fatalf("expected exactly 1 changed value on address %x, got %d", contract.Address(),
			len(logger.storage[contract.Address()]))
	}
	exp := common.BigToHash(big.NewInt(1))
	if logger.storage[contract.Address()][index] != exp {
		t.Errorf("expected %x, got %x", exp, logger.storage[contract.Address()][index])
	}
}

// TestSLOADStorageDelta verifies that each SLOAD log entry contains only the
// single slot that was read, not a clone of the entire cumulative storage map.
// This is the fix for quadratic memory growth (immunefi-69712).
func TestSLOADStorageDelta(t *testing.T) {
	var (
		logger   = NewStructLogger(&Config{Limit: 10 * 1024 * 1024})
		evm      = vm.NewEVM(vm.BlockContext{}, &dummyStatedb{}, params.TestChainConfig, vm.Config{Tracer: logger.Hooks()}, nil)
		contract = vm.NewContract(common.Address{}, common.Address{}, new(uint256.Int), 500000, nil)
	)
	// Build bytecode: 100 iterations of PUSH1 <i>, SLOAD (reads slot i)
	numSlots := 100
	var code []byte
	for i := 0; i < numSlots; i++ {
		code = append(code, byte(vm.PUSH1), byte(i), byte(vm.SLOAD), byte(vm.POP))
	}
	code = append(code, byte(vm.STOP))
	contract.Code = code

	logger.OnTxStart(evm.GetVMContext(), nil, common.Address{})
	_, err := evm.Interpreter().Run(vm.CALL, contract, []byte{}, false)
	if err != nil {
		t.Fatal(err)
	}

	// The cumulative storage should have all slots
	if len(logger.storage[contract.Address()]) != numSlots {
		t.Fatalf("expected %d cumulative storage entries, got %d", numSlots, len(logger.storage[contract.Address()]))
	}

	// Parse each SLOAD log entry and verify its storage map has exactly 1 entry
	for i, entry := range logger.logs {
		var parsed structLogLegacy
		if err := json.Unmarshal(entry, &parsed); err != nil {
			t.Fatalf("log %d: unmarshal error: %v", i, err)
		}
		if parsed.Op != "SLOAD" {
			continue
		}
		if parsed.Storage == nil {
			t.Fatalf("SLOAD log at index %d: expected storage entry, got nil", i)
		}
		if len(*parsed.Storage) != 1 {
			t.Fatalf("SLOAD log at index %d: expected 1 storage entry (delta only), got %d (cumulative clone leak)", i, len(*parsed.Storage))
		}
	}
}

// TestSLOADMemoryLinear verifies that tracing many SLOADs produces memory
// usage that grows linearly, not quadratically.
func TestSLOADMemoryLinear(t *testing.T) {
	var (
		logger   = NewStructLogger(&Config{Limit: 50 * 1024 * 1024})
		evm      = vm.NewEVM(vm.BlockContext{}, &dummyStatedb{}, params.TestChainConfig, vm.Config{Tracer: logger.Hooks()}, nil)
		contract = vm.NewContract(common.Address{}, common.Address{}, new(uint256.Int), 50_000_000, nil)
	)
	numSlots := 10000
	var code []byte
	for i := 0; i < numSlots; i++ {
		code = append(code, byte(vm.PUSH2))
		code = append(code, byte(i>>8), byte(i&0xff))
		code = append(code, byte(vm.SLOAD), byte(vm.POP))
	}
	code = append(code, byte(vm.STOP))
	contract.Code = code

	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	logger.OnTxStart(evm.GetVMContext(), nil, common.Address{})
	_, err := evm.Interpreter().Run(vm.CALL, contract, []byte{}, false)
	if err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	allocatedMB := float64(memAfter.TotalAlloc-memBefore.TotalAlloc) / (1024 * 1024)
	// With the quadratic bug, 10K SLOADs would produce ~500MB+ of allocations.
	// With the fix, it should be well under 50MB.
	maxAllowedMB := 50.0
	if allocatedMB > maxAllowedMB {
		t.Fatalf("memory usage too high: %.1f MB allocated for %d SLOADs (max allowed: %.0f MB). "+
			"Possible quadratic clone regression.", allocatedMB, numSlots, maxAllowedMB)
	}
	t.Logf("OK: %.1f MB allocated for %d SLOADs", allocatedMB, numSlots)
}

// Tests that blank fields don't appear in logs when JSON marshalled, to reduce
// logs bloat and confusion. See https://github.com/ethereum/go-ethereum/issues/24487
func TestStructLogMarshalingOmitEmpty(t *testing.T) {
	tests := []struct {
		name string
		log  *StructLog
		want string
	}{
		{"empty err and no fields", &StructLog{},
			`{"pc":0,"op":0,"gas":"0x0","gasCost":"0x0","memSize":0,"stack":null,"depth":0,"refund":0,"opName":"STOP"}`},
		{"with err", &StructLog{Err: errors.New("this failed")},
			`{"pc":0,"op":0,"gas":"0x0","gasCost":"0x0","memSize":0,"stack":null,"depth":0,"refund":0,"opName":"STOP","error":"this failed"}`},
		{"with mem", &StructLog{Memory: make([]byte, 2), MemorySize: 2},
			`{"pc":0,"op":0,"gas":"0x0","gasCost":"0x0","memory":"0x0000","memSize":2,"stack":null,"depth":0,"refund":0,"opName":"STOP"}`},
		{"with 0-size mem", &StructLog{Memory: make([]byte, 0)},
			`{"pc":0,"op":0,"gas":"0x0","gasCost":"0x0","memSize":0,"stack":null,"depth":0,"refund":0,"opName":"STOP"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blob, err := json.Marshal(tt.log)
			if err != nil {
				t.Fatal(err)
			}
			if have, want := string(blob), tt.want; have != want {
				t.Fatalf("mismatched results\n\thave: %v\n\twant: %v", have, want)
			}
		})
	}
}
