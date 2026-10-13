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
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// runSstore executes code against a fresh contract and returns gas used and refund.
func runSstore(t *testing.T, config *params.ChainConfig, code string) (uint64, uint64) {
	t.Helper()
	address := common.BytesToAddress([]byte("contract"))
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	statedb.CreateAccount(address)
	statedb.SetCode(address, hexutil.MustDecode(code), tracing.CodeChangeUnspecified)
	statedb.Finalise(params.Rules{IsEIP158: true})

	vmctx := BlockContext{
		BlockNumber: new(big.Int),
		CanTransfer: func(StateDB, common.Address, *uint256.Int) bool { return true },
		Transfer:    func(StateDB, common.Address, common.Address, *uint256.Int, *params.Rules) {},
	}
	evm := NewEVM(vmctx, statedb, config, Config{})
	initialGas := NewGasBudget(100_000, 0)
	_, result, err := evm.Call(common.Address{}, address, nil, initialGas, new(uint256.Int))
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	return result.Used(initialGas), evm.StateDB.GetRefund()
}

func TestSeiSstoreSetGasOverride(t *testing.T) {
	const override = uint64(72_000)
	custom := *params.AllEthashProtocolChanges
	custom.SeiSstoreSetGasEIP2200 = new(uint64)
	*custom.SeiSstoreSetGasEIP2200 = override

	// sstore(0, 1): clean zero to non-zero.
	defUsed, _ := runSstore(t, params.AllEthashProtocolChanges, "0x6001600055")
	seiUsed, _ := runSstore(t, &custom, "0x6001600055")
	if want := defUsed - params.SstoreSetGasEIP2200 + override; seiUsed != want {
		t.Errorf("set gas: have %d, want %d", seiUsed, want)
	}

	// sstore(0, 1); sstore(0, 0): reset to original zero refunds the set cost.
	_, defRefund := runSstore(t, params.AllEthashProtocolChanges, "0x60016000556000600055")
	_, seiRefund := runSstore(t, &custom, "0x60016000556000600055")
	if want := defRefund - params.SstoreSetGasEIP2200 + override; seiRefund != want {
		t.Errorf("refund: have %d, want %d", seiRefund, want)
	}
}
