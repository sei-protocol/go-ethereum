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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// HookableStateDB is the state surface wrapped by NewHookedState. It mirrors
// vm.StateDB (which cannot be referenced here without an import cycle), so any
// vm.StateDB implementation, such as Sei's, can be traced. Self-destruct hooks
// at Finalise are only emitted for the native *StateDB.
type HookableStateDB interface {
	CreateAccount(common.Address)
	CreateContract(common.Address)
	IsNewContract(common.Address) bool

	SubBalance(common.Address, *uint256.Int, tracing.BalanceChangeReason) uint256.Int
	AddBalance(common.Address, *uint256.Int, tracing.BalanceChangeReason) uint256.Int
	GetBalance(common.Address) *uint256.Int

	GetNonce(common.Address) uint64
	SetNonce(common.Address, uint64, tracing.NonceChangeReason)

	GetCodeHash(common.Address) common.Hash
	GetCode(common.Address) []byte
	SetCode(common.Address, []byte, tracing.CodeChangeReason) []byte
	GetCodeSize(common.Address) int

	AddRefund(uint64)
	SubRefund(uint64)
	GetRefund() uint64

	GetStateAndCommittedState(common.Address, common.Hash) (common.Hash, common.Hash)
	GetState(common.Address, common.Hash) common.Hash
	SetState(common.Address, common.Hash, common.Hash) common.Hash

	GetTransientState(common.Address, common.Hash) common.Hash
	SetTransientState(common.Address, common.Hash, common.Hash)

	SelfDestruct(common.Address)
	HasSelfDestructed(common.Address) bool

	Exist(common.Address) bool
	Touch(common.Address)
	Empty(common.Address) bool

	AddressInAccessList(common.Address) bool
	SlotInAccessList(common.Address, common.Hash) (addressOk bool, slotOk bool)
	AddAddressToAccessList(common.Address)
	AddSlotToAccessList(common.Address, common.Hash)

	Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses types.AccessList)

	RevertToSnapshot(int)
	Snapshot() int

	AddLog(*types.Log)
	AddPreimage(common.Hash, []byte)

	Witness() *stateless.Witness
	AccessEvents() *AccessEvents

	Finalise(rules params.Rules) *bal.ConstructionBlockAccessList
	SetTxContext(thash common.Hash, ti int, blockAccessIndex uint32)
}

var _ HookableStateDB = (*StateDB)(nil)
