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

package legacypool

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
)

func valueTx(nonce uint64, value *big.Int, key *ecdsa.PrivateKey) *types.Transaction {
	const intrinsicGas = 21000
	tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		To:       &common.Address{},
		Value:    value,
		Gas:      intrinsicGas,
		GasPrice: big.NewInt(1),
	}), types.HomesteadSigner{}, key)
	return tx
}

func maxAffordableValue(extra *big.Int) *big.Int {
	value := new(big.Int).Sub(math.MaxBig256, big.NewInt(21000))
	if extra != nil {
		value.Sub(value, extra)
	}
	return value
}

// TestAddRemoteTotalCostOverflowError tests that a queued tx overflowing total cost returns ErrTotalCostOverflow.
func TestAddRemoteTotalCostOverflowError(t *testing.T) {
	pool, key := setupPool()
	defer pool.Close()

	from, _ := types.Sender(types.HomesteadSigner{}, valueTx(0, big.NewInt(1), key))
	testAddBalance(pool, from, math.MaxBig256)

	if err := pool.addRemoteSync(valueTx(0, big.NewInt(1), key)); err != nil {
		t.Fatalf("failed to add base transaction: %v", err)
	}

	filler := valueTx(1, maxAffordableValue(nil), key)
	pool.mu.Lock()
	_, seedErr := pool.queue.add(filler)
	if seedErr == nil {
		pool.all.Add(filler)
	}
	pool.mu.Unlock()
	if seedErr != nil {
		t.Fatalf("failed to seed queue filler: %v", seedErr)
	}

	err := pool.addRemoteSync(valueTx(2, big.NewInt(1), key))
	if !errors.Is(err, txpool.ErrTotalCostOverflow) {
		t.Fatalf("expected ErrTotalCostOverflow, got %v", err)
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internals inconsistent: %v", err)
	}
}

// TestAddRemoteTotalCostOverflowErrorFullPool tests that a full pool rejects a total-cost overflow before evicting.
func TestAddRemoteTotalCostOverflowErrorFullPool(t *testing.T) {
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	blockchain := newTestBlockChain(params.TestChainConfig, 10_000_000, statedb, new(event.Feed))

	config := testTxPoolConfig
	config.GlobalSlots = 2
	config.GlobalQueue = 1

	pool := New(config, blockchain)
	if err := pool.Init(config.PriceLimit, blockchain.CurrentBlock(), newReserver()); err != nil {
		t.Fatalf("failed to init pool: %v", err)
	}
	defer pool.Close()

	// Two cheap evictable fillers from unrelated accounts.
	fillerHashes := make([]common.Hash, 0, 2)
	for range 2 {
		fillerKey, _ := crypto.GenerateKey()
		testAddBalance(pool, crypto.PubkeyToAddress(fillerKey.PublicKey), big.NewInt(1000000))

		filler := pricedTransaction(0, 21000, big.NewInt(1), fillerKey)
		if err := pool.addRemoteSync(filler); err != nil {
			t.Fatalf("failed to seed filler transaction: %v", err)
		}
		fillerHashes = append(fillerHashes, filler.Hash())
	}

	// Third account with a queued tx at the uint256 ceiling fills the last slot.
	targetKey, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(targetKey.PublicKey)
	testAddBalance(pool, from, math.MaxBig256)

	queuedFiller := valueTx(1, maxAffordableValue(nil), targetKey)
	pool.mu.Lock()
	_, seedErr := pool.queue.add(queuedFiller)
	if seedErr == nil {
		pool.all.Add(queuedFiller)
	}
	pool.mu.Unlock()
	if seedErr != nil {
		t.Fatalf("failed to seed queue filler: %v", seedErr)
	}

	if got, want := pool.all.Slots(), int(config.GlobalSlots+config.GlobalQueue); got != want {
		t.Fatalf("pool not at capacity before overflow add: have %d slots, want %d", got, want)
	}

	// Same account as queuedFiller, priced well above the fillers.
	overflowTx := pricedTransaction(2, 21000, big.NewInt(1000), targetKey)

	err := pool.addRemoteSync(overflowTx)
	if !errors.Is(err, txpool.ErrTotalCostOverflow) {
		t.Fatalf("expected ErrTotalCostOverflow, got %v", err)
	}

	for _, hash := range fillerHashes {
		if pool.all.Get(hash) == nil {
			t.Fatalf("filler transaction %x was evicted despite the incoming transaction being rejected", hash)
		}
	}
	if pool.all.Get(queuedFiller.Hash()) == nil {
		t.Fatalf("target account's queued filler was evicted despite the incoming transaction being rejected")
	}
	if pool.all.Get(overflowTx.Hash()) != nil {
		t.Fatalf("overflowing transaction should not have been added to the pool")
	}
	if got, want := pool.all.Slots(), int(config.GlobalSlots+config.GlobalQueue); got != want {
		t.Fatalf("pool slot count changed by rejected overflow add: have %d, want %d", got, want)
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internals inconsistent: %v", err)
	}
}

// TestPromoteExecutablesStopsOnOverflow tests that promotion halts on overflow without a pending nonce gap.
func TestPromoteExecutablesStopsOnOverflow(t *testing.T) {
	pool, key := setupPool()
	defer pool.Close()

	from, _ := types.Sender(types.HomesteadSigner{}, valueTx(0, maxAffordableValue(nil), key))
	tx0 := valueTx(0, maxAffordableValue(nil), key)
	tx1 := valueTx(1, big.NewInt(1), key)
	tx2 := valueTx(2, big.NewInt(1), key)

	pool.mu.Lock()
	pool.pending[from] = newList(true)
	_, _, err0 := pool.pending[from].Add(tx0, pool.config.PriceBump)
	pool.all.Add(tx0)
	pool.pendingNonces.set(from, 1)

	_, err1 := pool.queue.add(tx1)
	_, err2 := pool.queue.add(tx2)
	pool.all.Add(tx1)
	pool.all.Add(tx2)
	pool.mu.Unlock()

	if err0 != nil {
		t.Fatalf("failed to seed pending tx0: %v", err0)
	}
	if err1 != nil {
		t.Fatalf("failed to seed queue tx1: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("failed to seed queue tx2: %v", err2)
	}

	testAddBalance(pool, from, math.MaxBig256)
	pool.promoteExecutables([]common.Address{from})

	// Release before validatePoolInternals, which takes its own RLock.
	pool.mu.RLock()
	pending := pool.pending[from]
	pendingHasNonce2 := pending != nil && pending.Contains(2)
	pendingHasNonce0 := pending != nil && pending.Contains(0)
	queue, ok := pool.queue.get(from)
	queueHasNonce2 := ok && queue.Contains(2)
	tx1InAll := pool.all.Get(tx1.Hash()) != nil
	pool.mu.RUnlock()

	if pendingHasNonce2 {
		t.Fatalf("pending must not contain nonce 2 after overflow at nonce 1")
	}
	if !pendingHasNonce0 {
		t.Fatalf("pending should contain nonce 0")
	}
	if !queueHasNonce2 {
		t.Fatalf("nonce 2 should remain queued after promotion stopped")
	}
	if tx1InAll {
		t.Fatalf("overflowing promoted transaction should be dropped from pool.all")
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internals inconsistent: %v", err)
	}
}

// TestDemoteReenqueueTotalCostOverflow tests that demoted txs failing to re-enqueue are dropped from pool.all.
func TestDemoteReenqueueTotalCostOverflow(t *testing.T) {
	pool, key := setupPool()
	defer pool.Close()

	from, _ := types.Sender(types.HomesteadSigner{}, valueTx(0, big.NewInt(1), key))
	tx0 := valueTx(0, big.NewInt(1), key)
	tx1 := valueTx(1, big.NewInt(1), key)
	filler := valueTx(100, maxAffordableValue(nil), key)

	pool.mu.Lock()
	pool.pending[from] = newList(true)
	_, _, err0 := pool.pending[from].Add(tx0, pool.config.PriceBump)
	_, _, err1 := pool.pending[from].Add(tx1, pool.config.PriceBump)
	_, err2 := pool.queue.add(filler)
	for _, tx := range append(pool.pending[from].Flatten(), filler) {
		pool.all.Add(tx)
	}
	pool.mu.Unlock()

	if err0 != nil {
		t.Fatalf("failed to seed pending tx0: %v", err0)
	}
	if err1 != nil {
		t.Fatalf("failed to seed pending tx1: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("failed to seed queue filler: %v", err2)
	}

	pool.removeTx(tx0.Hash(), false, true)

	// Release before validatePoolInternals, which takes its own RLock.
	pool.mu.RLock()
	tx1InAll := pool.all.Get(tx1.Hash()) != nil
	pool.mu.RUnlock()

	if tx1InAll {
		t.Fatalf("overflowing demoted transaction should be removed from pool.all")
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internals inconsistent: %v", err)
	}
}

// TestTargetList tests targetList's pending, queue and fresh-list branches.
func TestTargetList(t *testing.T) {
	pool, key := setupPool()
	defer pool.Close()

	from, _ := types.Sender(types.HomesteadSigner{}, valueTx(0, big.NewInt(1), key))

	fresh := pool.targetList(from, valueTx(0, big.NewInt(1), key))
	if fresh == nil || fresh.Len() != 0 {
		t.Fatalf("expected a fresh empty list for an unknown account, got %v", fresh)
	}

	pending := newList(true)
	pendingTx := valueTx(0, big.NewInt(1), key)
	if _, _, err := pending.Add(pendingTx, pool.config.PriceBump); err != nil {
		t.Fatalf("failed to seed pending list: %v", err)
	}
	pool.pending[from] = pending

	queue := newList(false)
	queuedTx := valueTx(1, big.NewInt(1), key)
	if _, _, err := queue.Add(queuedTx, pool.config.PriceBump); err != nil {
		t.Fatalf("failed to seed queue list: %v", err)
	}
	pool.queue.queued[from] = queue

	if got := pool.targetList(from, valueTx(0, big.NewInt(2), key)); got != pending {
		t.Fatalf("expected targetList to return the pending list for a pending nonce")
	}
	if got := pool.targetList(from, valueTx(5, big.NewInt(1), key)); got != queue {
		t.Fatalf("expected targetList to return the queue list when the nonce isn't pending")
	}
}

// TestListAddCostOverflowDirect tests that addCostOverflow reports overflow without mutating the list.
func TestListAddCostOverflowDirect(t *testing.T) {
	key, _ := crypto.GenerateKey()
	list := newList(false)

	filler := valueTx(0, maxAffordableValue(nil), key)
	if _, _, err := list.Add(filler, DefaultConfig.PriceBump); err != nil {
		t.Fatalf("failed to seed filler: %v", err)
	}
	totalBefore := list.totalcost.Clone()

	overflow := valueTx(1, big.NewInt(1), key)
	if err := list.addCostOverflow(overflow); !errors.Is(err, txpool.ErrTotalCostOverflow) {
		t.Fatalf("expected addCostOverflow to report ErrTotalCostOverflow, got %v", err)
	}
	if list.totalcost.Cmp(totalBefore) != 0 {
		t.Fatalf("addCostOverflow must not mutate totalcost: have %v, want %v", list.totalcost, totalBefore)
	}
	if list.txs.Get(overflow.Nonce()) != nil {
		t.Fatalf("addCostOverflow must not store the checked transaction")
	}

	fits := valueTx(2, big.NewInt(1), key)
	list2 := newList(false)
	if err := list2.addCostOverflow(fits); err != nil {
		t.Fatalf("expected no overflow for a small transaction against an empty list, got %v", err)
	}
	if list2.txs.Get(fits.Nonce()) != nil {
		t.Fatalf("addCostOverflow must not store the checked transaction")
	}
}
