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
