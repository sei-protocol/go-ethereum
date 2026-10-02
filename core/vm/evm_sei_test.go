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

package vm_test

import (
	"bytes"
	"errors"
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

var abortingPrecompileAddress = common.BytesToAddress([]byte{0xff})

type testAbortError struct{}

func (testAbortError) Error() string      { return "abort execution" }
func (testAbortError) IsAbortError() bool { return true }

type abortingPrecompile struct{ err error }

func (p abortingPrecompile) RequiredGas([]byte) uint64 { return 0 }

func (p abortingPrecompile) Run(*vm.EVM, common.Address, common.Address, []byte, *big.Int, bool, bool, *tracing.Hooks) ([]byte, error) {
	return []byte{0xaa}, p.err
}

// contextPrecompile records the context it was invoked with.
type contextPrecompile struct {
	sender, calling common.Address
	value           *big.Int
	readOnly        bool
	delegate        bool
	depth           int
}

func (p *contextPrecompile) RequiredGas([]byte) uint64 { return 100 }

func (p *contextPrecompile) Run(evm *vm.EVM, sender common.Address, calling common.Address, _ []byte, value *big.Int, readOnly bool, delegate bool, _ *tracing.Hooks) ([]byte, error) {
	p.sender, p.calling, p.value, p.readOnly, p.delegate, p.depth = sender, calling, value, readOnly, delegate, evm.GetDepth()
	return []byte{0x01}, nil
}

// dynamicPrecompile meters its own gas.
type dynamicPrecompile struct{ cost uint64 }

func (p dynamicPrecompile) RequiredGas([]byte) uint64 { panic("not used for dynamic gas precompiles") }

func (p dynamicPrecompile) Run(*vm.EVM, common.Address, common.Address, []byte, *big.Int, bool, bool, *tracing.Hooks) ([]byte, error) {
	panic("not used for dynamic gas precompiles")
}

func (p dynamicPrecompile) RunAndCalculateGas(_ *vm.EVM, _ common.Address, _ common.Address, _ []byte, supplied uint64, _ *big.Int, _ *tracing.Hooks, _ bool, _ bool) ([]byte, uint64, error) {
	return []byte{0x02}, supplied - p.cost, nil
}

func newSeiTestEVM(t *testing.T, custom map[common.Address]vm.CustomPrecompiledContract) (*vm.EVM, *state.StateDB, common.Address) {
	t.Helper()
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	caller := common.HexToAddress("0x100")
	statedb.CreateAccount(caller)
	statedb.AddBalance(caller, uint256.NewInt(1), tracing.BalanceChangeUnspecified)

	evm := vm.NewEVMWithCustomPrecompiles(vm.BlockContext{
		BlockNumber: big.NewInt(0),
		CanTransfer: func(vm.StateDB, common.Address, *uint256.Int) bool { return true },
		Transfer:    func(vm.StateDB, common.Address, common.Address, *uint256.Int, *params.Rules) {},
	}, statedb, params.TestChainConfig, vm.Config{}, custom)
	return evm, statedb, caller
}

func TestCustomPrecompileContext(t *testing.T) {
	addr := common.HexToAddress("0x1001")
	p := &contextPrecompile{}
	evm, _, caller := newSeiTestEVM(t, map[common.Address]vm.CustomPrecompiledContract{addr: p})

	gas := vm.NewGasBudget(10_000, 0)
	ret, left, err := evm.Call(caller, addr, nil, gas, uint256.NewInt(1))
	if err != nil || !bytes.Equal(ret, []byte{0x01}) {
		t.Fatalf("call: ret %x err %v", ret, err)
	}
	if used := left.Used(gas); used != 100 {
		t.Errorf("gas used: have %d, want 100", used)
	}
	if p.sender != caller || p.calling != caller || p.value.Uint64() != 1 || p.readOnly || p.delegate || p.depth != 1 {
		t.Errorf("CALL context: %+v", p)
	}

	if _, _, err := evm.StaticCall(caller, addr, nil, gas); err != nil {
		t.Fatal(err)
	}
	if !p.readOnly || p.delegate || p.value != nil {
		t.Errorf("STATICCALL context: %+v", p)
	}

	origin := common.HexToAddress("0x99")
	if _, _, err := evm.DelegateCall(origin, caller, addr, nil, gas, new(uint256.Int)); err != nil {
		t.Fatal(err)
	}
	if p.sender != origin || p.calling != caller || !p.delegate || p.value != nil {
		t.Errorf("DELEGATECALL context: %+v", p)
	}

	if _, _, err := evm.CallCode(caller, addr, nil, gas, new(uint256.Int)); err != nil {
		t.Fatal(err)
	}
	if !p.delegate {
		t.Errorf("CALLCODE context: %+v", p)
	}

	found := false
	for _, a := range evm.GetPrecompiles() {
		found = found || a == addr
	}
	if !found {
		t.Errorf("custom precompile missing from GetPrecompiles")
	}
}

func TestCustomPrecompileDynamicGas(t *testing.T) {
	addr := common.HexToAddress("0x1002")
	evm, _, caller := newSeiTestEVM(t, map[common.Address]vm.CustomPrecompiledContract{addr: dynamicPrecompile{cost: 1234}})

	gas := vm.NewGasBudget(10_000, 0)
	ret, left, err := evm.Call(caller, addr, nil, gas, new(uint256.Int))
	if err != nil || !bytes.Equal(ret, []byte{0x02}) {
		t.Fatalf("call: ret %x err %v", ret, err)
	}
	if used := left.Used(gas); used != 1234 {
		t.Errorf("gas used: have %d, want 1234", used)
	}
}

func TestCustomPrecompileDoesNotShadowBuiltin(t *testing.T) {
	ecrecover := common.BytesToAddress([]byte{0x01})
	evm, _, caller := newSeiTestEVM(t, map[common.Address]vm.CustomPrecompiledContract{ecrecover: abortingPrecompile{err: testAbortError{}}})
	if _, _, err := evm.Call(caller, ecrecover, nil, vm.NewGasBudget(10_000, 0), new(uint256.Int)); err != nil {
		t.Fatalf("builtin ecrecover should win, got %v", err)
	}
}

func TestCallsPropagateAbortError(t *testing.T) {
	abortErr := testAbortError{}
	custom := map[common.Address]vm.CustomPrecompiledContract{abortingPrecompileAddress: abortingPrecompile{err: abortErr}}
	gas := vm.NewGasBudget(100_000, 0)

	tests := map[string]func(*vm.EVM, common.Address) ([]byte, vm.GasBudget, error){
		"CALL": func(evm *vm.EVM, c common.Address) ([]byte, vm.GasBudget, error) {
			return evm.Call(c, abortingPrecompileAddress, nil, gas, new(uint256.Int))
		},
		"CALLCODE": func(evm *vm.EVM, c common.Address) ([]byte, vm.GasBudget, error) {
			return evm.CallCode(c, abortingPrecompileAddress, nil, gas, new(uint256.Int))
		},
		"DELEGATECALL": func(evm *vm.EVM, c common.Address) ([]byte, vm.GasBudget, error) {
			return evm.DelegateCall(c, c, abortingPrecompileAddress, nil, gas, new(uint256.Int))
		},
		"STATICCALL": func(evm *vm.EVM, c common.Address) ([]byte, vm.GasBudget, error) {
			return evm.StaticCall(c, abortingPrecompileAddress, nil, gas)
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			evm, _, caller := newSeiTestEVM(t, custom)
			ret, left, err := run(evm, caller)
			if !errors.Is(err, abortErr) {
				t.Fatalf("want abort error, got %v", err)
			}
			if left.ExecutionGas != gas.ExecutionGas {
				t.Errorf("want gas preserved, got %v", left)
			}
			if !bytes.Equal(ret, []byte{0xaa}) {
				t.Errorf("want return data aa, got %x", ret)
			}
		})
	}
}

func TestCreatePropagatesAbortError(t *testing.T) {
	abortErr := testAbortError{}
	custom := map[common.Address]vm.CustomPrecompiledContract{abortingPrecompileAddress: abortingPrecompile{err: abortErr}}
	evm, statedb, caller := newSeiTestEVM(t, custom)

	ret, addr, _, err := evm.Create(caller, abortInitCode(), vm.NewGasBudget(1_000_000, 0), new(uint256.Int))
	if !errors.Is(err, abortErr) {
		t.Fatalf("want abort error, got %v", err)
	}
	if !bytes.Equal(ret, []byte{0xaa}) {
		t.Errorf("want return data aa, got %x", ret)
	}
	if !statedb.Exist(addr) {
		t.Errorf("want abort to propagate before reverting created account %s", addr)
	}
}

func TestCreateOpcodesPropagateAbortError(t *testing.T) {
	for _, create2 := range []bool{false, true} {
		abortErr := testAbortError{}
		custom := map[common.Address]vm.CustomPrecompiledContract{abortingPrecompileAddress: abortingPrecompile{err: abortErr}}
		evm, statedb, caller := newSeiTestEVM(t, custom)
		contract := common.HexToAddress("0x200")
		statedb.CreateAccount(contract)
		statedb.SetCode(contract, createOpcodeAbortProgram(create2), tracing.CodeChangeUnspecified)

		ret, _, err := evm.Call(caller, contract, nil, vm.NewGasBudget(1_000_000, 0), new(uint256.Int))
		if !errors.Is(err, abortErr) {
			t.Fatalf("create2=%v: want abort error, got %v", create2, err)
		}
		if !bytes.Equal(ret, []byte{0xaa}) {
			t.Fatalf("create2=%v: want return data aa, got %x", create2, ret)
		}
	}
}

// abortInitCode calls the aborting precompile.
func abortInitCode() []byte {
	return []byte{
		0x60, 0x00, // retSize
		0x60, 0x00, // retOffset
		0x60, 0x00, // inSize
		0x60, 0x00, // inOffset
		0x60, 0x00, // value
		0x60, 0xff, // address
		0x61, 0xff, 0xff, // gas
		byte(vm.CALL),
		byte(vm.STOP),
	}
}

func createOpcodeAbortProgram(create2 bool) []byte {
	initcode := abortInitCode()
	prefix := []byte{
		0x60, byte(len(initcode)), // size
		0x60, 0x00, // offset, patched below
		0x60, 0x00, // destOffset
		byte(vm.CODECOPY),
	}
	if create2 {
		prefix = append(prefix,
			0x60, 0x00, // salt
			0x60, byte(len(initcode)), // size
			0x60, 0x00, // offset
			0x60, 0x00, // endowment
			byte(vm.CREATE2),
			byte(vm.STOP),
		)
	} else {
		prefix = append(prefix,
			0x60, byte(len(initcode)), // size
			0x60, 0x00, // offset
			0x60, 0x00, // value
			byte(vm.CREATE),
			byte(vm.STOP),
		)
	}
	prefix[3] = byte(len(prefix))
	return append(prefix, initcode...)
}
