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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// SeiStateDB is the state surface used by RPC simulation, gas estimation and
// tracing. It is implemented by Sei's StateDB and, through NativeStateDB, by the
// upstream *state.StateDB, so those code paths are not tied to the native trie
// backed state.
type SeiStateDB interface {
	StateDB

	// Copy returns an independent copy of the state.
	Copy() SeiStateDB
	// Error returns any memorized database failure.
	Error() error
	// SetEVM informs the state of the EVM executing against it.
	SetEVM(evm *EVM)

	SetBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason)
	SetStorage(addr common.Address, storage map[common.Hash]common.Hash)
	GetStorageRoot(addr common.Address) common.Hash
	IntermediateRoot(rules params.Rules) common.Hash
	GetLogs(hash common.Hash, blockNumber uint64, blockHash common.Hash, blockTime uint64) []*types.Log
	TxIndex() int
}

// NativeStateDB adapts the upstream *state.StateDB to SeiStateDB.
type NativeStateDB struct {
	*state.StateDB
}

// WrapStateDB adapts statedb to SeiStateDB, returning nil for a nil statedb.
func WrapStateDB(statedb *state.StateDB) SeiStateDB {
	if statedb == nil {
		return nil
	}
	return &NativeStateDB{statedb}
}

// NativeState returns the upstream *state.StateDB behind statedb, if any.
func NativeState(statedb StateDB) (*state.StateDB, bool) {
	switch s := statedb.(type) {
	case *NativeStateDB:
		return s.StateDB, s.StateDB != nil
	case *state.StateDB:
		return s, s != nil
	}
	return nil, false
}

// Copy implements SeiStateDB.
func (s *NativeStateDB) Copy() SeiStateDB {
	return WrapStateDB(s.StateDB.Copy())
}

// SetEVM implements SeiStateDB; the native state does not need the EVM.
func (s *NativeStateDB) SetEVM(*EVM) {}

var _ SeiStateDB = (*NativeStateDB)(nil)
