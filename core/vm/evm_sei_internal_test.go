// Copyright 2026 The go-ethereum Authors
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
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// codeCountingStateDB counts full code loads.
type codeCountingStateDB struct {
	StateDB
	codeReads int
}

func (s *codeCountingStateDB) GetCode(addr common.Address) []byte {
	s.codeReads++
	return s.StateDB.GetCode(addr)
}

func TestDelegationResolutionChecksCodeSizeFirst(t *testing.T) {
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	plain := common.HexToAddress("0x01aa")
	delegated := common.HexToAddress("0x01bb")
	target := common.HexToAddress("0x01cc")
	targetCode := []byte{0x60, 0x00}
	statedb.SetCode(plain, bytes.Repeat([]byte{0x5b}, 100), tracing.CodeChangeUnspecified)
	statedb.SetCode(delegated, types.AddressToDelegation(target), tracing.CodeChangeUnspecified)
	statedb.SetCode(target, targetCode, tracing.CodeChangeUnspecified)

	counting := &codeCountingStateDB{StateDB: statedb}
	evm := NewEVM(BlockContext{BlockNumber: new(big.Int), Random: &common.Hash{}}, counting, params.MergedTestChainConfig, Config{})

	if got, want := evm.resolveCodeHash(plain), crypto.Keccak256Hash(bytes.Repeat([]byte{0x5b}, 100)); got != want {
		t.Fatalf("plain code hash: have %x, want %x", got, want)
	}
	if counting.codeReads != 0 {
		t.Fatalf("non-delegation code was loaded %d times", counting.codeReads)
	}
	if got, want := evm.resolveCodeHash(delegated), crypto.Keccak256Hash(targetCode); got != want {
		t.Fatalf("delegated code hash: have %x, want %x", got, want)
	}
	if counting.codeReads != 1 {
		t.Fatalf("delegation code loads: have %d, want 1", counting.codeReads)
	}
}

func TestDelegationResolutionDesignatorLengthWrongPrefix(t *testing.T) {
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	addr := common.HexToAddress("0x01aa")
	code := make([]byte, len(types.DelegationPrefix)+common.AddressLength)
	code[0] = 0xff
	statedb.SetCode(addr, code, tracing.CodeChangeUnspecified)

	counting := &codeCountingStateDB{StateDB: statedb}
	evm := NewEVM(BlockContext{BlockNumber: new(big.Int), Random: &common.Hash{}}, counting, params.MergedTestChainConfig, Config{})

	if got, want := evm.resolveCodeHash(addr), crypto.Keccak256Hash(code); got != want {
		t.Fatalf("code hash: have %x, want %x", got, want)
	}
	if counting.codeReads != 1 {
		t.Fatalf("designator-length code loads: have %d, want 1", counting.codeReads)
	}
}

func TestCallGasEIP7702DelegationCodeLoads(t *testing.T) {
	wrongPrefix := make([]byte, len(types.DelegationPrefix)+common.AddressLength)
	wrongPrefix[0] = 0xff
	target := common.HexToAddress("0x01cc")

	for _, tt := range []struct {
		name      string
		code      []byte
		wantReads int
		wantExtra uint64
	}{
		{"plain", bytes.Repeat([]byte{0x5b}, params.MaxCodeSize), 0, 0},
		{"designator length, wrong prefix", wrongPrefix, 1, 0},
		{"delegated", types.AddressToDelegation(target), 1, params.ColdAccountAccessCostEIP2929},
	} {
		t.Run(tt.name, func(t *testing.T) {
			statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
			addr := common.HexToAddress("0x01aa")
			statedb.SetCode(addr, tt.code, tracing.CodeChangeUnspecified)
			statedb.SetCode(target, []byte{0x00}, tracing.CodeChangeUnspecified)
			statedb.AddAddressToAccessList(addr)

			counting := &codeCountingStateDB{StateDB: statedb}
			evm := NewEVM(BlockContext{BlockNumber: new(big.Int), Random: &common.Hash{}}, counting, params.MergedTestChainConfig, Config{})
			contract := NewContract(common.Address{}, common.Address{}, new(uint256.Int), NewGasBudget(1_000_000, 0), nil)
			gasBefore := contract.Gas

			stack := newStackForTesting()
			stack.push(new(uint256.Int))                        // value
			stack.push(new(uint256.Int).SetBytes(addr.Bytes())) // addr
			stack.push(uint256.NewInt(100_000))                 // gas
			dyn, err := gasCallEIP7702(evm, contract, stack, NewMemory(), 0)
			if err != nil {
				t.Fatalf("gasCallEIP7702: %v", err)
			}
			if counting.codeReads != tt.wantReads {
				t.Fatalf("code loads: have %d, want %d", counting.codeReads, tt.wantReads)
			}
			if got, want := dyn.ExecutionGas, evm.callGasTemp+tt.wantExtra; got != want {
				t.Fatalf("dynamic gas: have %d, want %d", got, want)
			}
			if tt.wantExtra != 0 && !statedb.AddressInAccessList(target) {
				t.Fatal("delegation target not added to access list")
			}
			if contract.Gas != gasBefore {
				t.Fatalf("contract gas: have %v, want %v", contract.Gas, gasBefore)
			}
		})
	}
}
