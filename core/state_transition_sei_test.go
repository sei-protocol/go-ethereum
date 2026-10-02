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

package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

var (
	seiTestSender   = common.HexToAddress("0x1000")
	seiTestTo       = common.HexToAddress("0x2000")
	seiTestCoinbase = common.HexToAddress("0x3000")
	seiTestCustom   = common.HexToAddress("0x1001")
	seiTestBaseFee  = big.NewInt(100)
)

type nopCustomPrecompile struct{}

func (nopCustomPrecompile) RequiredGas([]byte) uint64 { return 0 }
func (nopCustomPrecompile) Run(*vm.EVM, common.Address, common.Address, []byte, *big.Int, bool, bool, *tracing.Hooks) ([]byte, error) {
	return nil, nil
}

func newSeiTransitionEnv(t *testing.T, receivesBaseFee bool) (*vm.EVM, *state.StateDB) {
	t.Helper()
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	statedb.AddBalance(seiTestSender, uint256.NewInt(params.Ether), tracing.BalanceChangeUnspecified)
	config := *params.MergedTestChainConfig
	config.SeiCoinbaseReceivesBaseFee = receivesBaseFee
	ctx := vm.BlockContext{
		CanTransfer: CanTransfer,
		Transfer:    Transfer,
		Coinbase:    seiTestCoinbase,
		BlockNumber: big.NewInt(1),
		Random:      &common.Hash{},
		BaseFee:     seiTestBaseFee,
		BlobBaseFee: big.NewInt(1),
		GasLimit:    30_000_000,
	}
	custom := map[common.Address]vm.CustomPrecompiledContract{seiTestCustom: nopCustomPrecompile{}}
	return vm.NewEVMWithCustomPrecompiles(ctx, statedb, &config, vm.Config{}, custom), statedb
}

func seiTestMessage(nonce uint64) *Message {
	return &Message{
		From:      seiTestSender,
		To:        &seiTestTo,
		Nonce:     nonce,
		Value:     new(uint256.Int),
		GasLimit:  params.TxGas,
		GasPrice:  uint256.NewInt(110),
		GasFeeCap: uint256.NewInt(110),
		GasTipCap: uint256.NewInt(10),
	}
}

func runSeiTransition(t *testing.T, evm *vm.EVM, msg *Message, feeCharged, incNonce bool) *ExecutionResult {
	t.Helper()
	evm.SetTxContext(NewEVMTxContext(msg))
	res, err := NewStateTransition(evm, msg, NewGasPool(30_000_000), feeCharged, incNonce).Execute()
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func TestSeiCoinbaseFee(t *testing.T) {
	for _, receivesBaseFee := range []bool{false, true} {
		evm, statedb := newSeiTransitionEnv(t, receivesBaseFee)
		res := runSeiTransition(t, evm, seiTestMessage(0), false, true)
		want := res.UsedGas * 10
		if receivesBaseFee {
			want = res.UsedGas * 110
		}
		if have := statedb.GetBalance(seiTestCoinbase).Uint64(); have != want {
			t.Errorf("receivesBaseFee=%v: coinbase balance have %d, want %d", receivesBaseFee, have, want)
		}
	}
}

func TestSeiNonceIncrementOptional(t *testing.T) {
	for _, incNonce := range []bool{false, true} {
		evm, statedb := newSeiTransitionEnv(t, false)
		runSeiTransition(t, evm, seiTestMessage(0), false, incNonce)
		want := uint64(0)
		if incNonce {
			want = 1
		}
		if have := statedb.GetNonce(seiTestSender); have != want {
			t.Errorf("incNonce=%v: nonce have %d, want %d", incNonce, have, want)
		}
	}
}

func TestSeiFeeCharged(t *testing.T) {
	evm, statedb := newSeiTransitionEnv(t, false)
	before := statedb.GetBalance(seiTestSender).Clone()
	// A wrong nonce passes because the stateless checks were done by the caller.
	res := runSeiTransition(t, evm, seiTestMessage(42), true, false)

	// Gas was not bought, but the unused gas (none here) and nothing else is returned.
	refund := uint256.NewInt((params.TxGas - res.UsedGas) * 110)
	want := new(uint256.Int).Add(before, refund)
	if have := statedb.GetBalance(seiTestSender); !have.Eq(want) {
		t.Errorf("sender balance have %v, want %v", have, want)
	}
	// The stateless checks remain available to the caller.
	st := NewStateTransition(evm, seiTestMessage(42), nil, true, false)
	if err := st.StatelessChecks(); err == nil {
		t.Errorf("expected nonce error from StatelessChecks")
	}
}

func TestSeiCustomPrecompileWarmed(t *testing.T) {
	evm, statedb := newSeiTransitionEnv(t, false)
	runSeiTransition(t, evm, seiTestMessage(0), false, true)
	if !statedb.AddressInAccessList(seiTestCustom) {
		t.Errorf("custom precompile not in access list")
	}
}
