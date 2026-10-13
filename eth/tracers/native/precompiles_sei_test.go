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

package native_test

import (
	"encoding/json"
	"errors"
	"math/big"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

var (
	customPrecompileAddr = common.HexToAddress("0x0000000000000000000000000000000000001001")
	contractAddr         = common.HexToAddress("0x00000000000000000000000000000000c0ffee00")
	senderAddr           = common.HexToAddress("0x00000000000000000000000000000000000abcde")
)

type nopPrecompile struct{}

func (nopPrecompile) RequiredGas([]byte) uint64 { return 0 }

func (nopPrecompile) Run(*vm.EVM, common.Address, common.Address, []byte, *big.Int, bool, bool, *tracing.Hooks) ([]byte, error) {
	return nil, nil
}

// customPrecompileEnv returns the tracing context of an EVM with a custom
// precompile at customPrecompileAddr.
func customPrecompileEnv(t *testing.T) *tracing.VMContext {
	blockCtx := vm.BlockContext{BlockNumber: big.NewInt(1), Time: 0, BaseFee: big.NewInt(0), Difficulty: big.NewInt(0)}
	evm := vm.NewEVMWithCustomPrecompiles(blockCtx, nil, params.MainnetChainConfig, vm.Config{},
		map[common.Address]vm.CustomPrecompiledContract{customPrecompileAddr: nopPrecompile{}})
	env := evm.GetVMContext()
	require.Contains(t, env.Precompiles, customPrecompileAddr)
	// The builtin precompiles are listed too, without duplicates.
	rules := params.MainnetChainConfig.Rules(blockCtx.BlockNumber, false, blockCtx.Time)
	for _, addr := range vm.ActivePrecompiles(rules) {
		require.Contains(t, env.Precompiles, addr)
	}
	require.Len(t, env.Precompiles, len(vm.ActivePrecompiles(rules))+1)
	return env
}

func TestVMContextPrecompiles(t *testing.T) {
	env := customPrecompileEnv(t)
	// Tracers extend the precompile list; this must not change the context.
	before := slices.Clone(env.Precompiles)
	tracer, err := tracers.DefaultDirectory.New("4byteTracer", &tracers.Context{}, nil, params.MainnetChainConfig)
	require.NoError(t, err)
	tracer.OnTxStart(env, types.NewTx(&types.LegacyTx{}), senderAddr)
	require.Equal(t, before, env.Precompiles)
}

func TestFourByteTracerCustomPrecompile(t *testing.T) {
	input := []byte{0xde, 0xad, 0xbe, 0xef, 0x01}
	for _, test := range []struct {
		to   common.Address
		want string
	}{
		{to: customPrecompileAddr, want: `{}`},
		{to: contractAddr, want: `{"0xdeadbeef-1":1}`},
	} {
		tracer, err := tracers.DefaultDirectory.New("4byteTracer", &tracers.Context{}, nil, params.MainnetChainConfig)
		require.NoError(t, err)
		tracer.OnTxStart(customPrecompileEnv(t), types.NewTx(&types.LegacyTx{}), senderAddr)
		tracer.OnEnter(0, byte(vm.CALL), senderAddr, test.to, input, 100000, big.NewInt(0))
		res, err := tracer.GetResult()
		require.NoError(t, err)
		require.JSONEq(t, test.want, string(res))
	}
}

func TestFlatCallTracerCustomPrecompile(t *testing.T) {
	for _, test := range []struct {
		to    common.Address
		calls int
	}{
		{to: customPrecompileAddr, calls: 1}, // the precompile call is dropped
		{to: contractAddr, calls: 2},
	} {
		tracer, err := tracers.DefaultDirectory.New("flatCallTracer", &tracers.Context{}, nil, params.MainnetChainConfig)
		require.NoError(t, err)
		tracer.OnTxStart(customPrecompileEnv(t), types.NewTx(&types.LegacyTx{Gas: 100000}), senderAddr)
		tracer.OnEnter(0, byte(vm.CALL), senderAddr, common.HexToAddress("0xc0de"), nil, 100000, big.NewInt(0))
		tracer.OnEnter(1, byte(vm.CALL), common.HexToAddress("0xc0de"), test.to, []byte{1, 2, 3, 4}, 50000, big.NewInt(0))
		tracer.OnExit(1, nil, 100, nil, false)
		tracer.OnExit(0, nil, 1000, nil, false)
		tracer.OnTxEnd(&types.Receipt{GasUsed: 1000}, nil)
		res, err := tracer.GetResult()
		require.NoError(t, err)
		var frames []json.RawMessage
		require.NoError(t, json.Unmarshal(res, &frames))
		require.Len(t, frames, test.calls, "to %v", test.to)
	}
}

// TestCallTracerTxEndWithoutCall checks that the call tracer copes with a
// transaction ending before any call was entered, e.g. when it was stopped.
func TestCallTracerTxEndWithoutCall(t *testing.T) {
	for _, config := range []string{`{}`, `{"withLog":true}`} {
		tracer, err := tracers.DefaultDirectory.New("callTracer", &tracers.Context{}, json.RawMessage(config), params.MainnetChainConfig)
		require.NoError(t, err)
		tracer.Stop(errors.New("stopped"))
		require.NotPanics(t, func() { tracer.OnTxEnd(&types.Receipt{GasUsed: 21000}, nil) })
	}
}

// TestTracerStopReason checks that GetResult reports the error given to Stop.
func TestTracerStopReason(t *testing.T) {
	for _, test := range []struct {
		name       string
		needsFrame bool
	}{
		{"4byteTracer", false},
		{"callTracer", true},
		{"flatCallTracer", true},
		{"prestateTracer", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracer, err := tracers.DefaultDirectory.New(test.name, &tracers.Context{}, nil, params.MainnetChainConfig)
			require.NoError(t, err)
			if test.needsFrame {
				tracer.OnEnter(0, byte(vm.CALL), common.Address{}, common.Address{}, nil, 0, big.NewInt(0))
			}
			stopErr := errors.New("stop error")
			stopped := make(chan struct{})
			go func() {
				tracer.Stop(stopErr)
				close(stopped)
			}()
			<-stopped

			_, err = tracer.GetResult()
			require.ErrorIs(t, err, stopErr)
		})
	}
}
