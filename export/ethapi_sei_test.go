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

package export

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/params"
)

func toJSONMap(t *testing.T, v any) map[string]any {
	t.Helper()
	enc, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(enc, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// The export constructors omit blockTimestamp for a mined tx and otherwise match ethapi.
func TestRPCTransactionOmitsBlockTimestamp(t *testing.T) {
	key, err := crypto.HexToECDSA("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	if err != nil {
		t.Fatal(err)
	}
	config := params.MergedTestChainConfig
	to := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	tx, err := types.SignNewTx(key, types.LatestSigner(config), &types.DynamicFeeTx{
		ChainID:   config.ChainID,
		Nonce:     1,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(10),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	header := &types.Header{Number: big.NewInt(5), Time: 1_700_000_000, BaseFee: big.NewInt(7)}
	block := types.NewBlockWithHeader(header).WithBody(types.Body{Transactions: types.Transactions{tx}})

	cases := []struct {
		name     string
		got, ref *RPCTransaction
	}{
		{"NewRPCTransaction",
			NewRPCTransaction(tx, block.Hash(), 5, header.Time, 0, header.BaseFee, config),
			ethapi.NewRPCTransaction(tx, block.Hash(), 5, header.Time, 0, header.BaseFee, config)},
		{"NewRPCTransactionFromBlockIndex",
			NewRPCTransactionFromBlockIndex(block, 0, config),
			ethapi.NewRPCTransactionFromBlockIndex(block, 0, config)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, want := toJSONMap(t, c.got), toJSONMap(t, c.ref)
			if _, ok := got["blockTimestamp"]; ok {
				t.Fatalf("blockTimestamp emitted: %v", got["blockTimestamp"])
			}
			if _, ok := want["blockTimestamp"]; !ok {
				t.Fatal("ethapi constructor no longer sets blockTimestamp")
			}
			delete(want, "blockTimestamp")
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("fields differ:\n got %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
	if NewRPCTransactionFromBlockIndex(block, 1, config) != nil {
		t.Fatal("out-of-range index should return nil")
	}
}
