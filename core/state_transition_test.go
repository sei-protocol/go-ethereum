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

package core

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

func TestFloorDataGas(t *testing.T) {
	addr1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	key1 := common.HexToHash("0xaa")
	key2 := common.HexToHash("0xbb")

	tests := []struct {
		name       string
		data       []byte
		accessList types.AccessList
		want       uint64
	}{
		{
			name: "pre-amsterdam/empty",
			want: params.TxGas,
		},
		{
			name: "pre-amsterdam/zero-bytes-only",
			data: bytes.Repeat([]byte{0x00}, 100),
			// 100 zero tokens * 10 cost = 1000
			want: params.TxGas + 100*params.TxCostFloorPerToken,
		},
		{
			name: "pre-amsterdam/non-zero-bytes-only",
			data: bytes.Repeat([]byte{0xff}, 100),
			// 100 nz * 4 tokens * 10 cost = 4000
			want: params.TxGas + 100*params.TxTokenPerNonZeroByte*params.TxCostFloorPerToken,
		},
		{
			name: "pre-amsterdam/mixed",
			data: append(bytes.Repeat([]byte{0x00}, 50), bytes.Repeat([]byte{0xff}, 50)...),
			// 50 zero + 50*4 nz = 250 tokens * 10 = 2500
			want: params.TxGas + (50+50*params.TxTokenPerNonZeroByte)*params.TxCostFloorPerToken,
		},
		{
			name: "pre-amsterdam/access-list-ignored",
			data: bytes.Repeat([]byte{0xff}, 10),
			accessList: types.AccessList{
				{Address: addr1, StorageKeys: []common.Hash{key1, key2}},
			},
			// Prague floor is calldata-only; FloorDataGas has no access-list argument.
			want: params.TxGas + 10*params.TxTokenPerNonZeroByte*params.TxCostFloorPerToken,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FloorDataGas(tt.data)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("gas mismatch: got %d, want %d (accessList entries %d)", got, tt.want, len(tt.accessList))
			}
		})
	}
}

func TestIntrinsicGas(t *testing.T) {
	addr1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	key1 := common.HexToHash("0xaa")
	key2 := common.HexToHash("0xbb")

	tests := []struct {
		name        string
		data        []byte
		accessList  types.AccessList
		authList    []types.SetCodeAuthorization
		creation    bool
		isHomestead bool
		isEIP2028   bool
		isEIP3860   bool
		want        uint64
	}{
		{
			name: "frontier/empty-call",
			want: params.TxGas,
		},
		{
			name:        "frontier/contract-creation-pre-homestead",
			creation:    true,
			isHomestead: false,
			// pre-homestead, contract creation still uses TxGas
			want: params.TxGas,
		},
		{
			name:        "homestead/contract-creation",
			creation:    true,
			isHomestead: true,
			want:        params.TxGasContractCreation,
		},
		{
			name: "frontier/non-zero-data",
			data: bytes.Repeat([]byte{0xff}, 100),
			// 100 nz bytes * 68 (frontier)
			want: params.TxGas + 100*params.TxDataNonZeroGasFrontier,
		},
		{
			name:      "istanbul/non-zero-data",
			data:      bytes.Repeat([]byte{0xff}, 100),
			isEIP2028: true,
			// 100 nz bytes * 16 (post-EIP2028)
			want: params.TxGas + 100*params.TxDataNonZeroGasEIP2028,
		},
		{
			name:      "istanbul/zero-data",
			data:      bytes.Repeat([]byte{0x00}, 100),
			isEIP2028: true,
			// 100 zero bytes * 4
			want: params.TxGas + 100*params.TxDataZeroGas,
		},
		{
			name:      "istanbul/mixed-data",
			data:      append(bytes.Repeat([]byte{0x00}, 50), bytes.Repeat([]byte{0xff}, 50)...),
			isEIP2028: true,
			want:      params.TxGas + 50*params.TxDataZeroGas + 50*params.TxDataNonZeroGasEIP2028,
		},
		{
			name:        "shanghai/init-code-word-gas",
			data:        bytes.Repeat([]byte{0x00}, 64), // 2 words
			creation:    true,
			isHomestead: true,
			isEIP2028:   true,
			isEIP3860:   true,
			// TxGasContractCreation + 64 zero bytes * 4 + 2 words * 2
			want: params.TxGasContractCreation + 64*params.TxDataZeroGas + 2*params.InitCodeWordGas,
		},
		{
			name:        "shanghai/init-code-non-multiple-of-32",
			data:        bytes.Repeat([]byte{0x00}, 33), // 2 words (rounded up)
			creation:    true,
			isHomestead: true,
			isEIP2028:   true,
			isEIP3860:   true,
			want:        params.TxGasContractCreation + 33*params.TxDataZeroGas + 2*params.InitCodeWordGas,
		},
		{
			name: "berlin/access-list",
			accessList: types.AccessList{
				{Address: addr1, StorageKeys: []common.Hash{key1, key2}},
				{Address: addr2, StorageKeys: []common.Hash{key1}},
			},
			isEIP2028: true,
			// 2 addrs * 2400 + 3 keys * 1900
			want: params.TxGas + 2*params.TxAccessListAddressGas + 3*params.TxAccessListStorageKeyGas,
		},
		{
			name: "prague/auth-list",
			authList: []types.SetCodeAuthorization{
				{Address: addr1},
				{Address: addr2},
				{Address: addr1},
			},
			isEIP2028: true,
			// 3 auths * 25000
			want: params.TxGas + 3*params.CallNewAccountGas,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IntrinsicGas(tt.data, tt.accessList, tt.authList, tt.creation, tt.isHomestead, tt.isEIP2028, tt.isEIP3860)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("gas mismatch: got %d, want %d", got, tt.want)
			}
		})
	}
}

// The tests below cover WithGasSurcharge. TestFloorDataGas and TestIntrinsicGas
// above are the Prague subset of upstream core/state_transition_test.go.

func TestGasSurchargeTraceReason(t *testing.T) {
	const surcharge uint64 = 1_000
	var reasons []tracing.GasChangeReason
	var associationDelta uint64
	result, err := executeCall(t, callConfig{
		gasLimit:    params.TxGas + surcharge,
		surcharge:   surcharge,
		reason:      tracing.GasChangeTxAutoAssociation,
		chainConfig: params.TestChainConfig,
		tracer: &tracing.Hooks{
			OnGasChange: func(old, new uint64, reason tracing.GasChangeReason) {
				reasons = append(reasons, reason)
				if reason == tracing.GasChangeTxAutoAssociation {
					associationDelta = old - new
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Err != nil {
		t.Fatalf("vm error: %v", result.Err)
	}
	if result.UsedGas != params.TxGas+surcharge {
		t.Fatalf("UsedGas = %d, want %d", result.UsedGas, params.TxGas+surcharge)
	}
	if associationDelta != surcharge {
		t.Fatalf("TxAutoAssociation delta = %d, want %d", associationDelta, surcharge)
	}
	want := []tracing.GasChangeReason{
		tracing.GasChangeTxInitialBalance,
		tracing.GasChangeTxAutoAssociation,
		tracing.GasChangeTxIntrinsicGas,
	}
	if len(reasons) < len(want) {
		t.Fatalf("OnGasChange reasons %v, want prefix %v", reasons, want)
	}
	for i, reason := range want {
		if reasons[i] != reason {
			t.Fatalf("OnGasChange[%d] = %v, want %v (got %v)", i, reasons[i], reason, reasons)
		}
	}
	autoCount := 0
	for _, reason := range reasons {
		if reason == tracing.GasChangeTxAutoAssociation {
			autoCount++
		}
	}
	if autoCount != 1 {
		t.Fatalf("TxAutoAssociation count = %d, want 1", autoCount)
	}
}

func TestGasSurchargePassesTraceReason(t *testing.T) {
	const surcharge uint64 = 1_000
	var got tracing.GasChangeReason
	var saw bool
	result, err := executeCall(t, callConfig{
		gasLimit:    params.TxGas + surcharge,
		surcharge:   surcharge,
		reason:      tracing.GasChangeUnspecified,
		chainConfig: params.TestChainConfig,
		tracer: &tracing.Hooks{
			OnGasChange: func(old, new uint64, reason tracing.GasChangeReason) {
				if old > new && old-new == surcharge {
					got = reason
					saw = true
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Err != nil {
		t.Fatalf("vm error: %v", result.Err)
	}
	if !saw || got != tracing.GasChangeUnspecified {
		t.Fatalf("surcharge reason = %v (saw %v), want Unspecified", got, saw)
	}
}

func TestGasSurchargeZeroIsUnchanged(t *testing.T) {
	result, err := executeCall(t, callConfig{
		gasLimit:    params.TxGas,
		surcharge:   0,
		chainConfig: params.TestChainConfig,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Err != nil {
		t.Fatalf("vm error: %v", result.Err)
	}
	if result.UsedGas != params.TxGas {
		t.Fatalf("UsedGas = %d, want %d", result.UsedGas, params.TxGas)
	}
}

func TestGasSurchargeInsufficientGas(t *testing.T) {
	const surcharge uint64 = 1_000
	_, err := executeCall(t, callConfig{
		gasLimit:    surcharge - 1,
		surcharge:   surcharge,
		reason:      tracing.GasChangeTxAutoAssociation,
		chainConfig: params.TestChainConfig,
	})
	if !errors.Is(err, ErrIntrinsicGas) {
		t.Fatalf("err = %v, want %v", err, ErrIntrinsicGas)
	}
}

func TestGasSurchargeLeavesTooLittleForIntrinsic(t *testing.T) {
	const surcharge uint64 = 1_000
	_, err := executeCall(t, callConfig{
		gasLimit:    params.TxGas + surcharge - 1,
		surcharge:   surcharge,
		reason:      tracing.GasChangeTxAutoAssociation,
		chainConfig: params.TestChainConfig,
	})
	if !errors.Is(err, ErrIntrinsicGas) {
		t.Fatalf("err = %v, want %v", err, ErrIntrinsicGas)
	}
}

func TestExecutionGasUsedExcludesSurcharge(t *testing.T) {
	st := &StateTransition{
		initialGas:   100_000,
		gasRemaining: 40_000,
		gasSurcharge: 10_000,
	}
	if got := st.executionGasUsed(); got != 50_000 {
		t.Fatalf("executionGasUsed = %d, want 50000", got)
	}
}

func TestCalcRefundCapsAgainstExecutionGas(t *testing.T) {
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	statedb.AddRefund(1_000_000)

	evm := vm.NewEVM(vm.BlockContext{
		BlockNumber: big.NewInt(1),
		Time:        1,
		Difficulty:  big.NewInt(0),
		BaseFee:     big.NewInt(0),
	}, statedb, params.TestChainConfig, vm.Config{}, nil)

	st := NewStateTransition(evm, &Message{GasPrice: big.NewInt(1)}, new(GasPool), true, true)
	st.initialGas = 100_000
	st.gasRemaining = 10_000
	st.gasSurcharge = 80_000

	// gasUsed is 90_000; execution gas is 10_000. EIP-3529 caps refunds at 1/5 of
	// execution gas (2_000), not 1/5 of gasUsed (18_000).
	got := st.calcRefund()
	if want := uint64(10_000 / params.RefundQuotientEIP3529); got != want {
		t.Fatalf("calcRefund = %d, want %d", got, want)
	}
}

func TestPragueFloorExcludesSurcharge(t *testing.T) {
	const (
		zeroBytes = 100
		surcharge = 50_000
	)
	data := make([]byte, zeroBytes)
	floor, err := FloorDataGas(data)
	if err != nil {
		t.Fatal(err)
	}
	intrinsic, err := IntrinsicGas(data, nil, nil, false, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if floor <= intrinsic {
		t.Fatalf("need floor > intrinsic for this fixture: floor=%d intrinsic=%d", floor, intrinsic)
	}

	result, err := executeCall(t, callConfig{
		gasLimit:    surcharge + floor + 10_000,
		surcharge:   surcharge,
		data:        data,
		chainConfig: params.MergedTestChainConfig,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Err != nil {
		t.Fatalf("vm error: %v", result.Err)
	}
	if want := surcharge + floor; result.UsedGas != want {
		t.Fatalf("UsedGas = %d, want surcharge+floor %d (intrinsic %d)", result.UsedGas, want, intrinsic)
	}
}

func TestGasSurchargeFloorAdmissionUsesExecutionBudget(t *testing.T) {
	const surcharge uint64 = 50_000
	data := make([]byte, 100)
	floor, err := FloorDataGas(data)
	if err != nil {
		t.Fatal(err)
	}
	intrinsic, err := IntrinsicGas(data, nil, nil, false, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if floor <= intrinsic {
		t.Fatalf("need floor > intrinsic for this fixture: floor=%d intrinsic=%d", floor, intrinsic)
	}

	gasLimit := surcharge + floor - 1
	executionBudget := gasLimit - surcharge
	if gasLimit < floor {
		t.Fatalf("GasLimit %d should still pass a limit-only floor check of %d", gasLimit, floor)
	}
	if executionBudget >= floor {
		t.Fatalf("execution budget %d should fail a floor check of %d", executionBudget, floor)
	}
	if executionBudget < intrinsic {
		t.Fatalf("execution budget %d should pass intrinsic %d so the error is floor admission", executionBudget, intrinsic)
	}

	_, err = executeCall(t, callConfig{
		gasLimit:    gasLimit,
		surcharge:   surcharge,
		data:        data,
		chainConfig: params.MergedTestChainConfig,
	})
	if !errors.Is(err, ErrFloorDataGas) {
		t.Fatalf("err = %v, want %v", err, ErrFloorDataGas)
	}
	wantMsg := fmt.Sprintf("%s: have %d, want %d", ErrFloorDataGas, executionBudget, floor)
	if err.Error() != wantMsg {
		t.Fatalf("err = %q, want %q", err, wantMsg)
	}
}

type callConfig struct {
	gasLimit    uint64
	surcharge   uint64
	reason      tracing.GasChangeReason
	data        []byte
	chainConfig *params.ChainConfig
	tracer      *tracing.Hooks
}

func executeCall(t *testing.T, cfg callConfig) (*ExecutionResult, error) {
	t.Helper()

	from := common.HexToAddress("0x1")
	to := common.HexToAddress("0xbeef")

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	statedb.AddBalance(from, uint256.NewInt(1_000_000_000), tracing.BalanceChangeUnspecified)

	random := common.Hash{}
	evm := vm.NewEVM(vm.BlockContext{
		CanTransfer: CanTransfer,
		Transfer:    Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		Coinbase:    common.Address{},
		BlockNumber: big.NewInt(1),
		Time:        1,
		Difficulty:  big.NewInt(0),
		GasLimit:    10_000_000,
		BaseFee:     big.NewInt(0),
		BlobBaseFee: big.NewInt(1),
		Random:      &random,
	}, statedb, cfg.chainConfig, vm.Config{Tracer: cfg.tracer}, nil)

	msg := &Message{
		From:      from,
		To:        &to,
		Nonce:     0,
		Value:     big.NewInt(0),
		GasLimit:  cfg.gasLimit,
		GasPrice:  big.NewInt(1),
		GasFeeCap: big.NewInt(1),
		GasTipCap: big.NewInt(1),
		Data:      cfg.data,
	}
	evm.SetTxContext(NewEVMTxContext(msg))
	gp := new(GasPool).AddGas(cfg.gasLimit)
	return NewStateTransition(evm, msg, gp, false, true).WithGasSurcharge(cfg.surcharge, cfg.reason).Execute()
}
