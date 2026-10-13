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
	"errors"
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core/txpool"
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

// TestPriceHeapCmp tests that the price heap comparison function works as intended.
// It also tests combinations where the basefee is higher than the gas fee cap, which
// are useful to sort in the mempool to support basefee changes.
func TestPriceHeapCmp(t *testing.T) {
	key, _ := crypto.GenerateKey()
	txs := []*types.Transaction{
		// nonce, gaslimit, gasfee, gastip
		dynamicFeeTx(0, 1000, big.NewInt(2), big.NewInt(1), key),
		dynamicFeeTx(0, 1000, big.NewInt(1), big.NewInt(2), key),
		dynamicFeeTx(0, 1000, big.NewInt(1), big.NewInt(1), key),
		dynamicFeeTx(0, 1000, big.NewInt(1), big.NewInt(0), key),
	}

	// create priceHeap
	ph := &priceHeap{}

	// now set the basefee on the heap
	for _, basefee := range []uint64{0, 1, 2, 3} {
		ph.baseFee = uint256.NewInt(basefee)

		for i := 0; i < len(txs); i++ {
			for j := 0; j < len(txs); j++ {
				switch {
				case i == j:
					if c := ph.cmp(txs[i], txs[j]); c != 0 {
						t.Errorf("tx %d should be equal priority to tx %d with basefee %d (cmp=%d)", i, j, basefee, c)
					}
				case i < j:
					if c := ph.cmp(txs[i], txs[j]); c != 1 {
						t.Errorf("tx %d vs tx %d comparison inconsistent with basefee %d (cmp=%d)", i, j, basefee, c)
					}
				}
			}
		}
	}
}

// TestListAddTotalcostOverflow tests that an add overflowing totalcost is rejected without mutation.
func TestListAddTotalcostOverflow(t *testing.T) {
	key, _ := crypto.GenerateKey()
	list := newList(true)

	// Zero gas limit makes cost equal to value.
	tx0, _ := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: 0, To: &common.Address{}, Value: math.MaxBig256, Gas: 0, GasPrice: big.NewInt(1)}), types.HomesteadSigner{}, key)
	if added, _, err := list.Add(tx0, DefaultConfig.PriceBump); !added || err != nil {
		t.Fatalf("expected first transaction to be added, err %v", err)
	}
	if list.totalcost.Cmp(uint256.MustFromBig(math.MaxBig256)) != 0 {
		t.Fatalf("totalcost mismatch after first add: have %v, want %v", list.totalcost, math.MaxBig256)
	}

	// Any positive cost now overflows totalcost.
	tx1, _ := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: 1, To: &common.Address{}, Value: big.NewInt(1), Gas: 0, GasPrice: big.NewInt(1)}), types.HomesteadSigner{}, key)
	added, _, err := list.Add(tx1, DefaultConfig.PriceBump)
	if added || !errors.Is(err, txpool.ErrTotalCostOverflow) {
		t.Fatalf("expected overflowing transaction to be rejected with ErrTotalCostOverflow, added=%v err=%v", added, err)
	}
	if list.totalcost.Cmp(uint256.MustFromBig(math.MaxBig256)) != 0 {
		t.Fatalf("totalcost corrupted by rejected add: have %v, want %v", list.totalcost, math.MaxBig256)
	}
	if list.txs.Get(1) != nil {
		t.Fatalf("rejected transaction should not be stored in the list")
	}
}

// TestListAddTotalCostOverflowOnReplace tests that a replacement overflowing totalcost is rejected without mutation.
func TestListAddTotalCostOverflowOnReplace(t *testing.T) {
	key, _ := crypto.GenerateKey()
	list := newList(true)

	// Filler at a different nonce pushing totalcost near the ceiling.
	filler, _ := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: 2, To: &common.Address{}, Value: new(big.Int).Sub(math.MaxBig256, big.NewInt(10)), Gas: 0, GasPrice: big.NewInt(1)}), types.HomesteadSigner{}, key)
	if added, _, err := list.Add(filler, DefaultConfig.PriceBump); !added || err != nil {
		t.Fatalf("expected filler transaction to be added, err %v", err)
	}

	// Low-priced original so a fee-bumped replacement is easy to construct.
	orig, _ := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: 0, To: &common.Address{}, Value: big.NewInt(5), Gas: 0, GasPrice: big.NewInt(10)}), types.HomesteadSigner{}, key)
	if added, _, err := list.Add(orig, DefaultConfig.PriceBump); !added || err != nil {
		t.Fatalf("expected original transaction to be added, err %v", err)
	}
	totalBefore := new(uint256.Int).Set(list.totalcost)

	// Replacement meets the price bump but overflows totalcost.
	replacement, _ := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: 0, To: &common.Address{}, Value: big.NewInt(11), Gas: 0, GasPrice: big.NewInt(100)}), types.HomesteadSigner{}, key)
	added, _, err := list.Add(replacement, DefaultConfig.PriceBump)
	if added || !errors.Is(err, txpool.ErrTotalCostOverflow) {
		t.Fatalf("expected overflowing replacement to be rejected with ErrTotalCostOverflow, added=%v err=%v", added, err)
	}
	if list.totalcost.Cmp(totalBefore) != 0 {
		t.Fatalf("totalCost corrupted by rejected replacement: have %v, want %v", list.totalcost, totalBefore)
	}
	if list.txs.Get(0) != orig {
		t.Fatalf("rejected replacement should leave the original transaction in place")
	}
}

// TestListAddTotalCostReplacementNearMax tests that a replacement near the uint256 ceiling updates totalcost correctly.
func TestListAddTotalCostReplacementNearMax(t *testing.T) {
	key, _ := crypto.GenerateKey()
	list := newList(true)

	filler, _ := types.SignTx(types.NewTx(&types.LegacyTx{
		Nonce: 1, To: &common.Address{}, Value: new(big.Int).Sub(math.MaxBig256, big.NewInt(110)), Gas: 0, GasPrice: big.NewInt(1),
	}), types.HomesteadSigner{}, key)
	if added, _, err := list.Add(filler, DefaultConfig.PriceBump); !added || err != nil {
		t.Fatalf("failed to add filler: added=%v err=%v", added, err)
	}

	orig, _ := types.SignTx(types.NewTx(&types.LegacyTx{
		Nonce: 0, To: &common.Address{}, Value: big.NewInt(100), Gas: 0, GasPrice: big.NewInt(10),
	}), types.HomesteadSigner{}, key)
	if added, _, err := list.Add(orig, DefaultConfig.PriceBump); !added || err != nil {
		t.Fatalf("failed to add original: added=%v err=%v", added, err)
	}

	replacement, _ := types.SignTx(types.NewTx(&types.LegacyTx{
		Nonce: 0, To: &common.Address{}, Value: big.NewInt(50), Gas: 0, GasPrice: big.NewInt(100),
	}), types.HomesteadSigner{}, key)
	if added, _, err := list.Add(replacement, DefaultConfig.PriceBump); !added || err != nil {
		t.Fatalf("failed to add replacement: added=%v err=%v", added, err)
	}

	want := uint256.MustFromBig(math.MaxBig256)
	want.Sub(want, uint256.NewInt(60))
	if list.totalcost.Cmp(want) != 0 {
		t.Fatalf("totalcost mismatch after near-max replacement: have %v, want %v", list.totalcost, want)
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
