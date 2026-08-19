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
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
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

// TestAddRemoteTotalCostOverflowError verifies that a brand-new queued transaction
// rejected for total-cost overflow surfaces ErrTotalCostOverflow rather than
// ErrReplaceUnderpriced.
func TestAddRemoteTotalCostOverflowError(t *testing.T) {
	pool, key := setupPool()
	defer pool.Close()

	from, _ := types.Sender(types.HomesteadSigner{}, valueTx(0, big.NewInt(1), key))
	testAddBalance(pool, from, math.MaxBig256)

	if err := pool.addRemote(valueTx(0, big.NewInt(1), key)); err != nil {
		t.Fatalf("failed to add base transaction: %v", err)
	}

	filler := valueTx(1, maxAffordableValue(nil), key)
	pool.mu.Lock()
	if pool.queue[from] == nil {
		pool.queue[from] = newList(false)
	}
	if _, _, err := pool.queue[from].Add(filler, pool.config.PriceBump); err != nil {
		t.Fatalf("failed to seed queue filler: %v", err)
	}
	pool.all.Add(filler)
	pool.mu.Unlock()

	err := pool.addRemote(valueTx(2, big.NewInt(1), key))
	if !errors.Is(err, txpool.ErrTotalCostOverflow) {
		t.Fatalf("expected ErrTotalCostOverflow, got %v", err)
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internals inconsistent: %v", err)
	}
}

// TestPromoteExecutablesStopsOnOverflow verifies that promotion halts after a
// total-cost overflow instead of leaving a nonce gap in pending.
func TestPromoteExecutablesStopsOnOverflow(t *testing.T) {
	pool, key := setupPool()
	defer pool.Close()

	from, _ := types.Sender(types.HomesteadSigner{}, valueTx(0, maxAffordableValue(nil), key))
	tx0 := valueTx(0, maxAffordableValue(nil), key)
	tx1 := valueTx(1, big.NewInt(1), key)
	tx2 := valueTx(2, big.NewInt(1), key)

	pool.mu.Lock()
	pool.pending[from] = newList(true)
	if _, _, err := pool.pending[from].Add(tx0, pool.config.PriceBump); err != nil {
		t.Fatalf("failed to seed pending tx0: %v", err)
	}
	pool.all.Add(tx0)
	pool.pendingNonces.set(from, 1)

	pool.queue[from] = newList(false)
	if _, _, err := pool.queue[from].Add(tx1, pool.config.PriceBump); err != nil {
		t.Fatalf("failed to seed queue tx1: %v", err)
	}
	if _, _, err := pool.queue[from].Add(tx2, pool.config.PriceBump); err != nil {
		t.Fatalf("failed to seed queue tx2: %v", err)
	}
	pool.all.Add(tx1)
	pool.all.Add(tx2)
	pool.mu.Unlock()

	testAddBalance(pool, from, math.MaxBig256)
	pool.promoteExecutables([]common.Address{from})

	pool.mu.RLock()
	defer pool.mu.RUnlock()

	pending := pool.pending[from]
	if pending != nil && pending.Contains(2) {
		t.Fatalf("pending must not contain nonce 2 after overflow at nonce 1")
	}
	if pending == nil || !pending.Contains(0) {
		t.Fatalf("pending should contain nonce 0")
	}
	if pool.queue[from] == nil || !pool.queue[from].Contains(2) {
		t.Fatalf("nonce 2 should remain queued after promotion stopped")
	}
	if pool.all.Get(tx1.Hash()) != nil {
		t.Fatalf("overflowing promoted transaction should be dropped from pool.all")
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internals inconsistent: %v", err)
	}
}

// TestDemoteReenqueueTotalCostOverflow verifies demoted transactions that cannot
// be re-queued due to overflow are dropped instead of lingering in pool.all.
func TestDemoteReenqueueTotalCostOverflow(t *testing.T) {
	pool, key := setupPool()
	defer pool.Close()

	from, _ := types.Sender(types.HomesteadSigner{}, valueTx(0, big.NewInt(1), key))
	tx0 := valueTx(0, big.NewInt(1), key)
	tx1 := valueTx(1, big.NewInt(1), key)
	filler := valueTx(100, maxAffordableValue(nil), key)

	pool.mu.Lock()
	pool.pending[from] = newList(true)
	if _, _, err := pool.pending[from].Add(tx0, pool.config.PriceBump); err != nil {
		t.Fatalf("failed to seed pending tx0: %v", err)
	}
	if _, _, err := pool.pending[from].Add(tx1, pool.config.PriceBump); err != nil {
		t.Fatalf("failed to seed pending tx1: %v", err)
	}
	pool.queue[from] = newList(false)
	if _, _, err := pool.queue[from].Add(filler, pool.config.PriceBump); err != nil {
		t.Fatalf("failed to seed queue filler: %v", err)
	}
	for _, tx := range append(pool.pending[from].Flatten(), filler) {
		pool.all.Add(tx)
	}
	pool.mu.Unlock()

	pool.removeTx(tx0.Hash(), false, true)

	pool.mu.RLock()
	defer pool.mu.RUnlock()

	if pool.all.Get(tx1.Hash()) != nil {
		t.Fatalf("overflowing demoted transaction should be removed from pool.all")
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internals inconsistent: %v", err)
	}
}
