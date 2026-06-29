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

type createAbortError struct{}

func (createAbortError) Error() string {
	return "abort contract creation"
}

func (createAbortError) IsAbortError() bool {
	return true
}

type createAbortingInterpreter struct {
	err error
}

func (i createAbortingInterpreter) Run(vm.OpCode, *vm.Contract, []byte, bool) ([]byte, error) {
	return []byte{0xaa}, i.err
}

func (i createAbortingInterpreter) ReadOnly() bool {
	return false
}

type abortingPrecompile struct {
	err error
}

func (p abortingPrecompile) RequiredGas([]byte) uint64 {
	return 0
}

func (p abortingPrecompile) Run(*vm.EVM, common.Address, common.Address, []byte, *big.Int, bool, bool, *tracing.Hooks) ([]byte, error) {
	return []byte{0xaa}, p.err
}

func TestCreatePropagatesAbortError(t *testing.T) {
	tests := []struct {
		name string
		run  func(*vm.EVM, common.Address) ([]byte, common.Address, uint64, error)
	}{
		{
			name: "CREATE",
			run: func(evm *vm.EVM, caller common.Address) ([]byte, common.Address, uint64, error) {
				ret, addr, leftOverGas, err := evm.Create(caller, nil, 100_000, new(uint256.Int))
				return ret, addr, leftOverGas, err
			},
		},
		{
			name: "CREATE2",
			run: func(evm *vm.EVM, caller common.Address) ([]byte, common.Address, uint64, error) {
				ret, addr, leftOverGas, err := evm.Create2(caller, nil, 100_000, new(uint256.Int), new(uint256.Int))
				return ret, addr, leftOverGas, err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			abortErr := createAbortError{}
			evm, statedb, caller := newAbortCreateEVM(t, nil)
			evm.EVMInterpreter = createAbortingInterpreter{err: abortErr}

			ret, addr, leftOverGas, err := tt.run(evm, caller)
			if !errors.Is(err, abortErr) {
				t.Fatalf("want abort error %v, got %v", abortErr, err)
			}
			if leftOverGas != 100_000 {
				t.Fatalf("want gas preserved, got %d", leftOverGas)
			}
			if want := []byte{0xaa}; !bytes.Equal(ret, want) {
				t.Fatalf("want return data %x, got %x", want, ret)
			}
			if !statedb.Exist(addr) {
				t.Fatalf("want abort to propagate before reverting created account %s", addr)
			}
		})
	}
}

func TestCreateOpcodesPropagateAbortError(t *testing.T) {
	tests := []struct {
		name string
		code []byte
	}{
		{name: "CREATE", code: createOpcodeAbortProgram(false)},
		{name: "CREATE2", code: createOpcodeAbortProgram(true)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			abortErr := createAbortError{}
			precompiles := map[common.Address]vm.PrecompiledContract{
				abortingPrecompileAddress: abortingPrecompile{err: abortErr},
			}
			evm, statedb, caller := newAbortCreateEVM(t, precompiles)
			contract := common.HexToAddress("0x200")
			statedb.CreateAccount(contract)
			statedb.SetCode(contract, tt.code)

			ret, _, err := evm.Call(caller, contract, nil, 1_000_000, new(uint256.Int))
			if !errors.Is(err, abortErr) {
				t.Fatalf("want abort error %v, got %v", abortErr, err)
			}
			if want := []byte{0xaa}; !bytes.Equal(ret, want) {
				t.Fatalf("want return data %x, got %x", want, ret)
			}
		})
	}
}

func newAbortCreateEVM(t *testing.T, customPrecompiles map[common.Address]vm.PrecompiledContract) (*vm.EVM, *state.StateDB, common.Address) {
	t.Helper()

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	caller := common.HexToAddress("0x100")
	statedb.CreateAccount(caller)
	statedb.SetBalance(caller, uint256.NewInt(1), tracing.BalanceChangeUnspecified)

	evm := vm.NewEVM(vm.BlockContext{
		BlockNumber: big.NewInt(0),
		CanTransfer: func(vm.StateDB, common.Address, *uint256.Int) bool {
			return true
		},
		Transfer: func(vm.StateDB, common.Address, common.Address, *uint256.Int) {},
	}, statedb, params.TestChainConfig, vm.Config{}, customPrecompiles)
	return evm, statedb, caller
}

func createOpcodeAbortProgram(create2 bool) []byte {
	initcode := []byte{
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
