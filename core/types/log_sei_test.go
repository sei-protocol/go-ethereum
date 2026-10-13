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

package types

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// A zero BlockTimestamp is omitted from log JSON; a set one is kept and round-trips.
func TestLogBlockTimestampOmittedWhenZero(t *testing.T) {
	log := Log{Address: common.HexToAddress("0x01"), Topics: []common.Hash{}, TxHash: common.HexToHash("0x02"), Data: []byte{}}

	enc, err := json.Marshal(&log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "blockTimestamp") {
		t.Fatalf("zero blockTimestamp emitted: %s", enc)
	}

	log.BlockTimestamp = 0x64
	enc, err = json.Marshal(&log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(enc), `"blockTimestamp":"0x64"`) {
		t.Fatalf("blockTimestamp missing: %s", enc)
	}
	var dec Log
	if err := json.Unmarshal(enc, &dec); err != nil {
		t.Fatal(err)
	}
	if dec.BlockTimestamp != log.BlockTimestamp {
		t.Fatalf("blockTimestamp round-trip: got %d, want %d", dec.BlockTimestamp, log.BlockTimestamp)
	}
}
