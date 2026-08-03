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
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie/utils"
	"github.com/holiman/uint256"
)

// stubStateDB is a minimal StateDB for EIP-7702 code-load tests.
type stubStateDB struct {
	codes            map[common.Address][]byte
	accessList       map[common.Address]struct{}
	getCodeCalls     int
	getCodeSizeCalls int
}

func newStubStateDB() *stubStateDB {
	return &stubStateDB{
		codes:      make(map[common.Address][]byte),
		accessList: make(map[common.Address]struct{}),
	}
}

func (s *stubStateDB) setCode(addr common.Address, code []byte) {
	s.codes[addr] = code
}

func (s *stubStateDB) CreateAccount(common.Address)  {}
func (s *stubStateDB) CreateContract(common.Address) {}
func (s *stubStateDB) SubBalance(common.Address, *uint256.Int, tracing.BalanceChangeReason) uint256.Int {
	return *uint256.NewInt(0)
}
func (s *stubStateDB) AddBalance(common.Address, *uint256.Int, tracing.BalanceChangeReason) uint256.Int {
	return *uint256.NewInt(0)
}
func (s *stubStateDB) GetBalance(common.Address) *uint256.Int { return uint256.NewInt(0) }
func (s *stubStateDB) GetNonce(common.Address) uint64         { return 0 }
func (s *stubStateDB) SetNonce(common.Address, uint64, tracing.NonceChangeReason) {
}
func (s *stubStateDB) GetCodeHash(addr common.Address) common.Hash {
	code := s.codes[addr]
	if len(code) == 0 {
		return types.EmptyCodeHash
	}
	return crypto.Keccak256Hash(code)
}
func (s *stubStateDB) GetCode(addr common.Address) []byte {
	s.getCodeCalls++
	return s.codes[addr]
}
func (s *stubStateDB) SetCode(addr common.Address, code []byte) []byte {
	prev := s.codes[addr]
	s.codes[addr] = code
	return prev
}
func (s *stubStateDB) GetCodeSize(addr common.Address) int {
	s.getCodeSizeCalls++
	return len(s.codes[addr])
}
func (s *stubStateDB) AddRefund(uint64)  {}
func (s *stubStateDB) SubRefund(uint64)  {}
func (s *stubStateDB) GetRefund() uint64 { return 0 }
func (s *stubStateDB) GetCommittedState(common.Address, common.Hash) common.Hash {
	return common.Hash{}
}
func (s *stubStateDB) GetState(common.Address, common.Hash) common.Hash { return common.Hash{} }
func (s *stubStateDB) SetState(common.Address, common.Hash, common.Hash) common.Hash {
	return common.Hash{}
}
func (s *stubStateDB) GetStorageRoot(common.Address) common.Hash { return common.Hash{} }
func (s *stubStateDB) GetTransientState(common.Address, common.Hash) common.Hash {
	return common.Hash{}
}
func (s *stubStateDB) SetTransientState(common.Address, common.Hash, common.Hash) {}
func (s *stubStateDB) SelfDestruct(common.Address) uint256.Int {
	return *uint256.NewInt(0)
}
func (s *stubStateDB) HasSelfDestructed(common.Address) bool { return false }
func (s *stubStateDB) SelfDestruct6780(common.Address) (uint256.Int, bool) {
	return *uint256.NewInt(0), false
}
func (s *stubStateDB) Exist(common.Address) bool { return true }
func (s *stubStateDB) Empty(common.Address) bool { return false }
func (s *stubStateDB) AddressInAccessList(addr common.Address) bool {
	_, ok := s.accessList[addr]
	return ok
}
func (s *stubStateDB) SlotInAccessList(common.Address, common.Hash) (bool, bool) {
	return false, false
}
func (s *stubStateDB) AddAddressToAccessList(addr common.Address) {
	s.accessList[addr] = struct{}{}
}
func (s *stubStateDB) AddSlotToAccessList(common.Address, common.Hash) {}
func (s *stubStateDB) PointCache() *utils.PointCache                   { return nil }
func (s *stubStateDB) Prepare(params.Rules, common.Address, common.Address, *common.Address, []common.Address, types.AccessList) {
}
func (s *stubStateDB) RevertToSnapshot(int) {}
func (s *stubStateDB) Snapshot() int        { return 0 }
func (s *stubStateDB) AddLog(*types.Log)    {}
func (s *stubStateDB) AddPreimage(common.Hash, []byte) {
}
func (s *stubStateDB) Witness() *stateless.Witness { return nil }
func (s *stubStateDB) AccessEvents() *AccessEvents { return nil }
func (s *stubStateDB) Finalise(bool)               {}
func (s *stubStateDB) Error() error                { return nil }
func (s *stubStateDB) SetBalance(common.Address, *uint256.Int, tracing.BalanceChangeReason) {
}
func (s *stubStateDB) SetStorage(common.Address, map[common.Hash]common.Hash) {}
func (s *stubStateDB) Commit(uint64, bool, bool) (common.Hash, error) {
	return common.Hash{}, nil
}
func (s *stubStateDB) SetTxContext(common.Hash, int) {}
func (s *stubStateDB) Copy() StateDB {
	panic("stubStateDB.Copy is not implemented")
}
func (s *stubStateDB) IntermediateRoot(bool) common.Hash {
	return common.Hash{}
}
func (s *stubStateDB) GetLogs(common.Hash, uint64, common.Hash) []*types.Log {
	return nil
}
func (s *stubStateDB) TxIndex() int { return 0 }
func (s *stubStateDB) Preimages() map[common.Hash][]byte {
	return nil
}
func (s *stubStateDB) Logs() []*types.Log { return nil }
func (s *stubStateDB) SetEVM(*EVM)        {}

func newPragueTestEVM(t *testing.T, statedb StateDB) *EVM {
	t.Helper()
	vmctx := BlockContext{
		CanTransfer: func(StateDB, common.Address, *uint256.Int) bool { return true },
		Transfer:    func(StateDB, common.Address, common.Address, *uint256.Int) {},
		BlockNumber: big.NewInt(1),
		Time:        1,
		Random:      &common.Hash{}, // post-merge; required for Prague rules
	}
	return NewEVM(vmctx, statedb, params.MergedTestChainConfig, Config{}, nil)
}

func TestResolveCodeHashSkipsFullCodeForNonDelegation(t *testing.T) {
	statedb := newStubStateDB()
	addr := common.BytesToAddress([]byte("large-contract"))
	code := make([]byte, params.MaxCodeSize)
	statedb.setCode(addr, code)

	evm := newPragueTestEVM(t, statedb)
	if !evm.chainRules.IsPrague {
		t.Fatal("expected Prague rules")
	}

	got := evm.resolveCodeHash(addr)
	want := crypto.Keccak256Hash(code)
	if got != want {
		t.Fatalf("code hash mismatch: got %x want %x", got, want)
	}
	if statedb.getCodeCalls != 0 {
		t.Fatalf("GetCode calls = %d, want 0 for non-delegation sized code", statedb.getCodeCalls)
	}
	if statedb.getCodeSizeCalls == 0 {
		t.Fatal("expected GetCodeSize to be consulted")
	}
}

func TestResolveCodeHashLoadsDelegationDesignator(t *testing.T) {
	statedb := newStubStateDB()
	target := common.BytesToAddress([]byte("delegate-target"))
	eoa := common.BytesToAddress([]byte("delegated-eoa"))
	targetCode := []byte{0x00}
	statedb.setCode(target, targetCode)
	statedb.setCode(eoa, types.AddressToDelegation(target))

	evm := newPragueTestEVM(t, statedb)

	got := evm.resolveCodeHash(eoa)
	want := crypto.Keccak256Hash(targetCode)
	if got != want {
		t.Fatalf("delegated code hash mismatch: got %x want %x", got, want)
	}
	if statedb.getCodeCalls == 0 {
		t.Fatal("expected GetCode for designator-sized delegation code")
	}
}

func TestResolveCodeHashDesignatorLengthWrongPrefix(t *testing.T) {
	statedb := newStubStateDB()
	addr := common.BytesToAddress([]byte("almost-designator"))
	// Exact designator length, but not a delegation prefix — size gate opens,
	// ParseDelegation must still reject and fall back to the account hash.
	code := make([]byte, len(types.DelegationPrefix)+common.AddressLength)
	code[0] = 0xff
	statedb.setCode(addr, code)

	evm := newPragueTestEVM(t, statedb)
	got := evm.resolveCodeHash(addr)
	want := crypto.Keccak256Hash(code)
	if got != want {
		t.Fatalf("code hash mismatch: got %x want %x", got, want)
	}
	if statedb.getCodeCalls == 0 {
		t.Fatal("expected GetCode when size matches designator length")
	}
	if _, ok := types.ParseDelegation(code); ok {
		t.Fatal("test setup: code must not parse as a delegation")
	}
}

func TestCallVariantGasEIP7702SkipsFullCodeForNonDelegation(t *testing.T) {
	statedb := newStubStateDB()
	addr := common.BytesToAddress([]byte("large-contract"))
	statedb.setCode(addr, make([]byte, params.MaxCodeSize))
	statedb.AddAddressToAccessList(addr)

	evm := newPragueTestEVM(t, statedb)
	contract := NewContract(common.Address{}, common.Address{}, uint256.NewInt(0), 1_000_000, nil)

	stack := Newstack()
	defer returnStack(stack)
	stack.Push(uint256.NewInt(0))                       // value (Back(2))
	stack.Push(new(uint256.Int).SetBytes(addr.Bytes())) // addr (Back(1))
	stack.Push(uint256.NewInt(100_000))                 // gas (Back(0))

	before := statedb.getCodeCalls
	if _, err := gasCallEIP7702(evm, contract, stack, NewMemory(), 0); err != nil {
		t.Fatalf("gasCallEIP7702: %v", err)
	}
	if statedb.getCodeCalls != before {
		t.Fatalf("GetCode calls increased by %d, want 0 for non-delegation sized code", statedb.getCodeCalls-before)
	}
	if statedb.getCodeSizeCalls == 0 {
		t.Fatal("expected GetCodeSize to be consulted")
	}
}

func TestCallVariantGasEIP7702ChargesDelegationTarget(t *testing.T) {
	statedb := newStubStateDB()
	target := common.BytesToAddress([]byte("delegate-target"))
	eoa := common.BytesToAddress([]byte("delegated-eoa"))
	baselineAddr := common.BytesToAddress([]byte("baseline-eoa"))
	statedb.setCode(target, []byte{0x00})
	statedb.setCode(eoa, types.AddressToDelegation(target))
	// Same length as a designator, but wrong prefix — no target access charge.
	baselineCode := make([]byte, len(types.DelegationPrefix)+common.AddressLength)
	baselineCode[0] = 0xff
	statedb.setCode(baselineAddr, baselineCode)
	statedb.AddAddressToAccessList(eoa)
	statedb.AddAddressToAccessList(baselineAddr)

	evm := newPragueTestEVM(t, statedb)
	pushCallStack := func(addr common.Address) *Stack {
		stack := Newstack()
		stack.Push(uint256.NewInt(0))
		stack.Push(new(uint256.Int).SetBytes(addr.Bytes()))
		stack.Push(uint256.NewInt(100_000))
		return stack
	}

	baselineContract := NewContract(common.Address{}, common.Address{}, uint256.NewInt(0), 1_000_000, nil)
	baselineStack := pushCallStack(baselineAddr)
	baseline, err := gasCallEIP7702(evm, baselineContract, baselineStack, NewMemory(), 0)
	returnStack(baselineStack)
	if err != nil {
		t.Fatalf("baseline gasCallEIP7702: %v", err)
	}

	contract := NewContract(common.Address{}, common.Address{}, uint256.NewInt(0), 1_000_000, nil)
	gasBefore := contract.Gas
	stack := pushCallStack(eoa)
	defer returnStack(stack)

	dyn, err := gasCallEIP7702(evm, contract, stack, NewMemory(), 0)
	if err != nil {
		t.Fatalf("gasCallEIP7702: %v", err)
	}
	if statedb.getCodeCalls == 0 {
		t.Fatal("expected GetCode for delegation designator")
	}
	if !statedb.AddressInAccessList(target) {
		t.Fatal("expected delegation target to be added to access list")
	}
	want := baseline + params.ColdAccountAccessCostEIP2929
	if dyn != want {
		t.Fatalf("dynamic gas %d, want baseline %d + cold access %d = %d", dyn, baseline, params.ColdAccountAccessCostEIP2929, want)
	}
	if contract.Gas != gasBefore {
		t.Fatalf("contract gas after calculator = %d, want %d (charges returned to dynamic gas)", contract.Gas, gasBefore)
	}
}

func TestCallVariantGasEIP7702DesignatorLengthWrongPrefix(t *testing.T) {
	statedb := newStubStateDB()
	addr := common.BytesToAddress([]byte("almost-designator"))
	code := make([]byte, len(types.DelegationPrefix)+common.AddressLength)
	code[0] = 0xff
	statedb.setCode(addr, code)
	statedb.AddAddressToAccessList(addr)

	evm := newPragueTestEVM(t, statedb)
	contract := NewContract(common.Address{}, common.Address{}, uint256.NewInt(0), 1_000_000, nil)

	stack := Newstack()
	defer returnStack(stack)
	stack.Push(uint256.NewInt(0))
	stack.Push(new(uint256.Int).SetBytes(addr.Bytes()))
	stack.Push(uint256.NewInt(100_000))

	before := statedb.getCodeCalls
	if _, err := gasCallEIP7702(evm, contract, stack, NewMemory(), 0); err != nil {
		t.Fatalf("gasCallEIP7702: %v", err)
	}
	if statedb.getCodeCalls <= before {
		t.Fatal("expected GetCode when size matches designator length")
	}
}
