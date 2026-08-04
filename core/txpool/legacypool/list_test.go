// Copyright 2016 The go-ethereum Authors
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
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// Tests that transactions can be added to strict lists and list contents and
// nonce boundaries are correctly maintained.
func TestStrictListAdd(t *testing.T) {
	// Generate a list of transactions to insert
	key, _ := crypto.GenerateKey()

	txs := make(types.Transactions, 1024)
	for i := 0; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 0, key)
	}
	// Insert the transactions in a random order
	list := newList(true)
	for _, v := range rand.Perm(len(txs)) {
		list.Add(txs[v], DefaultConfig.PriceBump)
	}
	// Verify internal state
	if len(list.txs.items) != len(txs) {
		t.Errorf("transaction count mismatch: have %d, want %d", len(list.txs.items), len(txs))
	}
	for i, tx := range txs {
		if list.txs.items[tx.Nonce()] != tx {
			t.Errorf("item %d: transaction mismatch: have %v, want %v", i, list.txs.items[tx.Nonce()], tx)
		}
	}
}

// TestListAddVeryExpensive tests adding txs which exceed 256 bits in cost. It is
// expected that the list does not panic.
func TestListAddVeryExpensive(t *testing.T) {
	key, _ := crypto.GenerateKey()
	list := newList(true)
	for i := 0; i < 3; i++ {
		value := big.NewInt(100)
		gasprice, _ := new(big.Int).SetString("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 0)
		gaslimit := uint64(i)
		tx, _ := types.SignTx(types.NewTransaction(uint64(i), common.Address{}, value, gaslimit, gasprice, nil), types.HomesteadSigner{}, key)
		t.Logf("cost: %x bitlen: %d\n", tx.Cost(), tx.Cost().BitLen())
		list.Add(tx, DefaultConfig.PriceBump)
	}
}

// TestListAddTotalcostOverflow tests that adding a transaction whose cost, once
// accumulated onto the list's totalcost, would overflow 256 bits is rejected
// rather than silently wrapping the tracked total.
func TestListAddTotalcostOverflow(t *testing.T) {
	key, _ := crypto.GenerateKey()
	list := newList(true)

	// First tx: cost equal to the max uint256 value (gasLimit 0 keeps gas cost
	// out of the equation, so cost == value).
	tx0, _ := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: 0, To: &common.Address{}, Value: math.MaxBig256, Gas: 0, GasPrice: big.NewInt(1)}), types.HomesteadSigner{}, key)
	if added, _ := list.Add(tx0, DefaultConfig.PriceBump); !added {
		t.Fatalf("expected first transaction to be added")
	}
	if list.totalcost.Cmp(uint256.MustFromBig(math.MaxBig256)) != 0 {
		t.Fatalf("totalcost mismatch after first add: have %v, want %v", list.totalcost, math.MaxBig256)
	}

	// Second tx: any positive cost added on top now overflows totalcost.
	tx1, _ := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: 1, To: &common.Address{}, Value: big.NewInt(1), Gas: 0, GasPrice: big.NewInt(1)}), types.HomesteadSigner{}, key)
	added, _ := list.Add(tx1, DefaultConfig.PriceBump)
	if added {
		t.Fatalf("expected overflowing transaction to be rejected")
	}
	// totalcost must be untouched by the rejected add.
	if list.totalcost.Cmp(uint256.MustFromBig(math.MaxBig256)) != 0 {
		t.Fatalf("totalcost corrupted by rejected add: have %v, want %v", list.totalcost, math.MaxBig256)
	}
	// The rejected tx must not have been stored either.
	if list.txs.Get(1) != nil {
		t.Fatalf("rejected transaction should not be stored in the list")
	}
}

// TestListAddTotalCostOverflowOnReplace tests the fee-bump replacement path:
// if applying a valid replacement (subtracting the old tx's cost and adding
// the new one) would overflow totalcost, the replacement must be rejected
// and totalcost/the stored tx must be left exactly as they were.
func TestListAddTotalCostOverflowOnReplace(t *testing.T) {
	key, _ := crypto.GenerateKey()
	list := newList(true)

	// Filler tx at a different nonce, pushing totalcost close to the ceiling.
	filler, _ := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: 2, To: &common.Address{}, Value: new(big.Int).Sub(math.MaxBig256, big.NewInt(10)), Gas: 0, GasPrice: big.NewInt(1)}), types.HomesteadSigner{}, key)
	if added, _ := list.Add(filler, DefaultConfig.PriceBump); !added {
		t.Fatalf("expected filler transaction to be added")
	}

	// Original tx at nonce 0, low gas price so a fee-bumped replacement is easy to construct.
	orig, _ := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: 0, To: &common.Address{}, Value: big.NewInt(5), Gas: 0, GasPrice: big.NewInt(10)}), types.HomesteadSigner{}, key)
	if added, _ := list.Add(orig, DefaultConfig.PriceBump); !added {
		t.Fatalf("expected original transaction to be added")
	}
	totalBefore := new(uint256.Int).Set(list.totalcost)

	// Replacement satisfies the fee-bump requirement (higher gas price) but its
	// cost, once substituted for orig's, would overflow totalcost.
	replacement, _ := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: 0, To: &common.Address{}, Value: big.NewInt(11), Gas: 0, GasPrice: big.NewInt(100)}), types.HomesteadSigner{}, key)
	added, _ := list.Add(replacement, DefaultConfig.PriceBump)
	if added {
		t.Fatalf("expected overflowing replacement to be rejected")
	}
	if list.totalcost.Cmp(totalBefore) != 0 {
		t.Fatalf("totalCost corrupted by rejected replacement: have %v, want %v", list.totalcost, totalBefore)
	}
	if list.txs.Get(0) != orig {
		t.Fatalf("rejected replacement should leave the original transaction in place")
	}
}

func BenchmarkListAdd(b *testing.B) {
	// Generate a list of transactions to insert
	key, _ := crypto.GenerateKey()

	txs := make(types.Transactions, 100000)
	for i := 0; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 0, key)
	}
	// Insert the transactions in a random order
	priceLimit := uint256.NewInt(DefaultConfig.PriceLimit)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		list := newList(true)
		for _, v := range rand.Perm(len(txs)) {
			list.Add(txs[v], DefaultConfig.PriceBump)
			list.Filter(priceLimit, DefaultConfig.PriceBump)
		}
	}
}

func BenchmarkListCapOneTx(b *testing.B) {
	// Generate a list of transactions to insert
	key, _ := crypto.GenerateKey()

	txs := make(types.Transactions, 32)
	for i := 0; i < len(txs); i++ {
		txs[i] = transaction(uint64(i), 0, key)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		list := newList(true)
		// Insert the transactions in a random order
		for _, v := range rand.Perm(len(txs)) {
			list.Add(txs[v], DefaultConfig.PriceBump)
		}
		b.StartTimer()
		list.Cap(list.Len() - 1)
		b.StopTimer()
	}
}
