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

// Sei extensions to the state transition.

package core

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// BlockState is the state needed to finish a transaction or block: the
// intermediate root, receipt logs and transaction index. It is satisfied by
// *state.StateDB and vm.SeiStateDB.
type BlockState interface {
	IntermediateRoot(rules params.Rules) common.Hash
	GetLogs(hash common.Hash, blockNumber uint64, blockHash common.Hash, blockTime uint64) []*types.Log
	TxIndex() int
}

var (
	_ BlockState = (*state.StateDB)(nil)
	_ BlockState = (vm.SeiStateDB)(nil)
)

// StateTransition is a state transition whose fee handling and nonce
// increment can be owned by the caller.
type StateTransition = stateTransition

// NewStateTransition creates a state transition. When feeCharged is set the
// stateless checks and gas pre-payment are skipped, as the caller (Sei's ante
// handler) has already performed them. When shouldIncrementNonce is false the
// sender nonce is not incremented for message calls.
func NewStateTransition(evm *vm.EVM, msg *Message, gp *GasPool, feeCharged bool, shouldIncrementNonce bool) *StateTransition {
	if gp == nil {
		gp = NewGasPool(msg.GasLimit)
	}
	st := newStateTransition(evm, msg, gp)
	st.feeCharged = feeCharged
	st.shouldIncrementNonce = shouldIncrementNonce
	return st
}

// Execute transitions the state by applying the message. See execute.
func (st *stateTransition) Execute() (*ExecutionResult, error) {
	return st.execute()
}

// StatelessChecks validates the message against the consensus rules without
// charging for it.
func (st *stateTransition) StatelessChecks() error {
	return st.statelessChecks(st.rules())
}

// BuyGas checks the sender can afford the message and pre-pays its gas.
func (st *stateTransition) BuyGas() error {
	return st.buyGas()
}

func (st *stateTransition) rules() params.Rules {
	return st.evm.ChainConfig().Rules(st.evm.Context.BlockNumber, st.evm.Context.Random != nil, st.evm.Context.Time)
}

// activePrecompiles returns the precompile addresses to warm, including the
// EVM's custom precompiles.
func (st *stateTransition) activePrecompiles(rules params.Rules) []common.Address {
	active := vm.ActivePrecompiles(rules)
	custom := st.evm.CustomPrecompiles()
	if len(custom) == 0 {
		return active
	}
	res := make([]common.Address, 0, len(active)+len(custom))
	res = append(res, active...)
	for addr := range custom {
		res = append(res, addr)
	}
	return res
}

// coinbaseFeePerGas returns the fee per gas credited to the coinbase. When
// ChainConfig.SeiCoinbaseReceivesBaseFee is set the base fee is not burned and
// the coinbase receives base fee plus tip, where both are derived from the
// block context's base fee (not msg.GasPrice, which Sei may have computed
// against a different base fee):
//
//	London:     baseFee + min(gasTipCap, gasFeeCap-baseFee) = min(gasFeeCap, baseFee+gasTipCap)
//	pre-London: baseFee + gasPrice
func (st *stateTransition) coinbaseFeePerGas(effectiveTip *uint256.Int) *uint256.Int {
	if !st.evm.ChainConfig().SeiCoinbaseReceivesBaseFee {
		return effectiveTip
	}
	baseFee := new(uint256.Int)
	if st.evm.Context.BaseFee != nil {
		baseFee = uint256.MustFromBig(st.evm.Context.BaseFee)
	}
	if !st.rules().IsLondon || st.msg.GasFeeCap == nil || st.msg.GasTipCap == nil {
		return new(uint256.Int).Add(baseFee, st.msg.GasPrice)
	}
	fee := new(uint256.Int).Add(baseFee, st.msg.GasTipCap)
	if fee.Gt(st.msg.GasFeeCap) {
		fee.Set(st.msg.GasFeeCap)
	}
	return fee
}
