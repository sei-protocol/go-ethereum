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

package state

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// foreignStateDB is a non-native HookableStateDB implementation.
type foreignStateDB struct{ *StateDB }

func TestHookedStateWrapsForeignStateDB(t *testing.T) {
	inner, _ := New(types.EmptyRootHash, NewDatabaseForTesting())
	var changes int
	hooked := NewHookedState(foreignStateDB{inner}, &tracing.Hooks{
		OnBalanceChange: func(common.Address, *big.Int, *big.Int, tracing.BalanceChangeReason) { changes++ },
	})
	addr := common.HexToAddress("0x01")
	hooked.AddBalance(addr, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	hooked.SelfDestruct(addr)
	hooked.Finalise(params.Rules{IsEIP158: true})
	if changes == 0 {
		t.Fatal("balance hook not invoked")
	}
	if inner.Exist(addr) {
		t.Fatal("self-destructed account not finalised by the inner state")
	}
}
