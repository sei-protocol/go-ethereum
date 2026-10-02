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

// Sei extensions to the EVM. These are kept out of the upstream files so that
// upstream updates only conflict at the call-dispatch choke points.

package vm

import (
	"errors"
	"maps"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// CustomPrecompiledContract is a chain-specific precompile which, unlike the
// upstream PrecompiledContract, receives the calling context and may access
// and modify state through the EVM.
type CustomPrecompiledContract interface {
	RequiredGas(input []byte) uint64
	Run(evm *EVM, sender common.Address, callingContract common.Address, input []byte, value *big.Int, readOnly bool, isFromDelegateCall bool, hooks *tracing.Hooks) ([]byte, error)
}

// DynamicGasPrecompiledContract is a CustomPrecompiledContract which meters its
// own gas instead of charging RequiredGas up front.
type DynamicGasPrecompiledContract interface {
	RunAndCalculateGas(evm *EVM, sender common.Address, callingContract common.Address, input []byte, suppliedGas uint64, value *big.Int, hooks *tracing.Hooks, readOnly bool, isFromDelegateCall bool) (ret []byte, remainingGas uint64, err error)
}

// AbortError is implemented by errors which must abort execution immediately
// and propagate through the call stack without being converted into a failed
// call frame.
type AbortError interface {
	error
	IsAbortError() bool
}

func isAbortError(err error) bool {
	var abortErr AbortError
	return err != nil && errors.As(err, &abortErr) && abortErr.IsAbortError()
}

// NewEVMWithCustomPrecompiles constructs an EVM with additional chain-specific
// precompiles. Upstream precompiles take precedence on address collisions.
func NewEVMWithCustomPrecompiles(blockCtx BlockContext, statedb StateDB, chainConfig *params.ChainConfig, config Config, customPrecompiles map[common.Address]CustomPrecompiledContract) *EVM {
	evm := NewEVM(blockCtx, statedb, chainConfig, config)
	if len(customPrecompiles) > 0 {
		evm.customPrecompiles = make(map[common.Address]CustomPrecompiledContract, len(customPrecompiles))
		for addr, p := range customPrecompiles {
			if _, exists := evm.precompiles[addr]; !exists {
				evm.customPrecompiles[addr] = p
			}
		}
	}
	return evm
}

// customPrecompile returns the custom precompile at addr, if any. Upstream
// precompiles shadow custom ones, matching NewEVMWithCustomPrecompiles.
func (evm *EVM) customPrecompile(addr common.Address) (CustomPrecompiledContract, bool) {
	if _, ok := evm.precompiles[addr]; ok {
		return nil, false
	}
	p, ok := evm.customPrecompiles[addr]
	return p, ok
}

// isPrecompile reports whether addr is an upstream or custom precompile.
func (evm *EVM) isPrecompile(addr common.Address) bool {
	if _, ok := evm.precompiles[addr]; ok {
		return true
	}
	_, ok := evm.customPrecompiles[addr]
	return ok
}

// CustomPrecompiles returns a copy of the custom precompiles of this EVM.
func (evm *EVM) CustomPrecompiles() map[common.Address]CustomPrecompiledContract {
	return maps.Clone(evm.customPrecompiles)
}

// GetPrecompiles returns the addresses of all upstream and custom precompiles.
func (evm *EVM) GetPrecompiles() []common.Address {
	res := make([]common.Address, 0, len(evm.precompiles)+len(evm.customPrecompiles))
	for addr := range evm.precompiles {
		res = append(res, addr)
	}
	for addr := range evm.customPrecompiles {
		if _, ok := evm.precompiles[addr]; !ok {
			res = append(res, addr)
		}
	}
	return res
}

// GetDepth returns the current call depth.
func (evm *EVM) GetDepth() int {
	return evm.depth
}

// ReadOnly reports whether the current call frame is static.
func (evm *EVM) ReadOnly() bool {
	return evm.readOnly
}

// RunCustomPrecompiledContract runs a custom precompile with its calling
// context. The call depth is incremented so a precompile which re-enters the
// EVM is accounted for. Custom precompiles are never served from the
// precompile result cache, as they may be stateful.
func RunCustomPrecompiledContract(p CustomPrecompiledContract, evm *EVM, sender common.Address, callingContract common.Address, address common.Address, input []byte, gas GasBudget, value *big.Int, logger *tracing.Hooks, readOnly bool, isFromDelegateCall bool) (ret []byte, remaining GasBudget, err error) {
	evm.depth++
	defer func() { evm.depth-- }()

	if dp, ok := p.(DynamicGasPrecompiledContract); ok {
		ret, left, err := dp.RunAndCalculateGas(evm, sender, callingContract, input, gas.ExecutionGas, value, logger, readOnly, isFromDelegateCall)
		gas.ChargeExecutionOnly(gas.ExecutionGas - min(left, gas.ExecutionGas))
		return ret, gas, err
	}
	gasCost := p.RequiredGas(input)
	prior, ok := gas.ChargeExecution(gasCost)
	if !ok {
		return nil, gas, ErrOutOfGas
	}
	if logger.HasGasHook() {
		logger.EmitGasChange(prior.AsTracing(), gas.AsTracing(), tracing.GasChangeCallPrecompiledContract)
	}
	if evm.chainRules.IsAmsterdam {
		evm.StateDB.Touch(address)
	}
	ret, err = p.Run(evm, sender, callingContract, input, value, readOnly, isFromDelegateCall, logger)
	return ret, gas, err
}

// GetDeploymentCode runs init code at address without creating the account,
// returning the code that would be deployed and the gas left.
func (evm *EVM) GetDeploymentCode(caller common.Address, code []byte, gas uint64, value *big.Int, address common.Address) ([]byte, uint64, error) {
	contract := NewContract(caller, address, uint256.MustFromBig(value), NewGasBudget(gas, 0), newMapJumpDests())
	contract.SetCallCode(crypto.Keccak256Hash(code), code)
	ret, err := evm.Run(contract, nil, false)
	// Check whether the max code size has been exceeded, assign err if the case.
	if err == nil {
		err = CheckMaxCodeSize(&evm.chainRules, uint64(len(ret)))
	}
	// Reject code starting with 0xEF if EIP-3541 is enabled.
	if err == nil && len(ret) >= 1 && ret[0] == 0xEF && evm.chainRules.IsLondon {
		err = ErrInvalidCode
	}
	if err == nil {
		createDataGas := uint64(len(ret)) * params.CreateDataGas
		if !contract.chargeExecution(createDataGas, evm.Config.Tracer, tracing.GasChangeCallCodeStorage) {
			err = ErrCodeStoreOutOfGas
		}
	}
	// When an error was returned by the EVM or when setting the creation code
	// above we consume any gas remaining. Additionally, when we're in homestead
	// this also counts for code storage gas errors.
	if err != nil && (evm.chainRules.IsHomestead || err != ErrCodeStoreOutOfGas) {
		if err != ErrExecutionReverted {
			contract.chargeExecution(contract.Gas.ExecutionGas, evm.Config.Tracer, tracing.GasChangeCallFailedExecution)
		}
	}
	return ret, contract.Gas.ExecutionGas, err
}
