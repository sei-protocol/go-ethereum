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

package tracers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/tracers"
	_ "github.com/ethereum/go-ethereum/eth/tracers/js"
	_ "github.com/ethereum/go-ethereum/eth/tracers/native"
	"github.com/ethereum/go-ethereum/eth/tracers/tracersutils"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
)

var (
	failedTxKey, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	failedTxSender  = crypto.PubkeyToAddress(failedTxKey.PublicKey)
	failedTxTo      = common.HexToAddress("0x00000000000000000000000000000000000a0001")
	failedTxBalance = big.NewInt(params.Ether)

	// transferInput is ERC-20 transfer calldata, without the amount.
	transferInput = append(common.FromHex("0xa9059cbb"), common.LeftPadBytes(failedTxTo.Bytes(), 32)...)
	// floorInput is calldata of 100 non-zero bytes: 22600 gas of intrinsic gas, and
	// 25000 gas of EIP-7623 floor data gas.
	floorInput = append(common.FromHex("0xa9059cbb"), bytes.Repeat([]byte{1}, 96)...)
)

// replayBackend replays the blocks of a test chain under a higher base fee or the rules
// of a later fork, so that a transaction the chain included fails its state transition,
// as the transactions of a Sei block can.
type replayBackend struct {
	*tracers.TestBackend
	baseFee  *big.Int            // base fee of the replay, if set
	config   *params.ChainConfig // post-merge chain config of the replay, if set
	metadata bool                // whether blocks are traced through their metadata
}

func (b *replayBackend) ChainConfigAtHeight(height int64) *params.ChainConfig {
	if b.config != nil {
		return b.config
	}
	return b.TestBackend.ChainConfigAtHeight(height)
}

func (b *replayBackend) BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, []tracersutils.TraceBlockMetadata, error) {
	block, _, err := b.TestBackend.BlockByNumber(ctx, number)
	if err != nil || block == nil || !b.metadata {
		return block, nil, err
	}
	metadata := make([]tracersutils.TraceBlockMetadata, len(block.Transactions()))
	for i := range metadata {
		metadata[i] = tracersutils.TraceBlockMetadata{ShouldIncludeInTraceResult: true, IdxInEthBlock: i}
	}
	return block, metadata, nil
}

func (b *replayBackend) StateAtTransaction(ctx context.Context, block *types.Block, txIndex int, reexec uint64) (*types.Transaction, vm.BlockContext, vm.StateDB, tracers.StateReleaseFunc, error) {
	tx, vmctx, statedb, release, err := b.TestBackend.StateAtTransaction(ctx, block, txIndex, reexec)
	return tx, b.replayContext(vmctx), statedb, release, err
}

func (b *replayBackend) GetBlockContext(ctx context.Context, block *types.Block, statedb vm.StateDB, backend ethapi.ChainContextBackend) (vm.BlockContext, error) {
	vmctx, err := b.TestBackend.GetBlockContext(ctx, block, statedb, backend)
	return b.replayContext(vmctx), err
}

// PrepareTx starts every transaction on a fresh journal, so that snapshot 0 is the
// state before the transaction, as it is in Sei's state.
func (b *replayBackend) PrepareTx(statedb vm.StateDB, tx *types.Transaction) error {
	statedb.Finalise(true)
	statedb.Snapshot()
	return nil
}

func (b *replayBackend) replayContext(vmctx vm.BlockContext) vm.BlockContext {
	if b.baseFee != nil {
		vmctx.BaseFee = b.baseFee
	}
	if b.config != nil {
		vmctx.Random = &common.Hash{}
	}
	return vmctx
}

// newReplayBackend creates a chain of one block, holding the transactions that txs
// returns for the base fee of the block, signed by failedTxSender.
func newReplayBackend(t *testing.T, txs func(baseFee *big.Int) []types.TxData) (*replayBackend, *types.Block, []*types.Transaction) {
	t.Helper()
	genesis := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc:  types.GenesisAlloc{failedTxSender: {Balance: failedTxBalance}},
	}
	signer := types.LatestSigner(params.TestChainConfig)
	var signed []*types.Transaction
	backend := tracers.NewTestBackend(t, 1, genesis, func(i int, b *core.BlockGen) {
		for _, data := range txs(b.BaseFee()) {
			tx := types.MustSignNewTx(failedTxKey, signer, data)
			b.AddTx(tx)
			signed = append(signed, tx)
		}
	})
	t.Cleanup(backend.Teardown)
	replay := &replayBackend{TestBackend: backend}
	block, _, err := replay.BlockByNumber(context.Background(), 1)
	require.NoError(t, err)
	return replay, block, signed
}

func transfer(nonce uint64, feeCap *big.Int, gas uint64, input []byte) *types.DynamicFeeTx {
	return &types.DynamicFeeTx{
		ChainID:   params.TestChainConfig.ChainID,
		Nonce:     nonce,
		GasTipCap: new(big.Int),
		GasFeeCap: feeCap,
		Gas:       gas,
		To:        &failedTxTo,
		Value:     big.NewInt(1000),
		Data:      input,
	}
}

func traceConfig(tracer string, tracerConfig string) *tracers.TraceConfig {
	config := &tracers.TraceConfig{}
	if tracer != "" {
		config.Tracer = &tracer
	}
	if tracerConfig != "" {
		config.TracerConfig = json.RawMessage(tracerConfig)
	}
	return config
}

func lower(addr common.Address) string {
	return strings.ToLower(addr.Hex())
}

type tracerCase struct {
	name   string
	config *tracers.TraceConfig
	want   string
}

// requireTraces requires every tracer of cases to trace tx as its case wants.
func requireTraces(t *testing.T, api *tracers.API, tx *types.Transaction, cases []tracerCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := api.TraceTransaction(context.Background(), tx.Hash(), tc.config)
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(res.(json.RawMessage)))
		})
	}
}

// TestTraceTxBelowBaseFee traces a block transaction whose fee cap falls below the base
// fee: the chain fails it before charging it, and bumps the sender's nonce.
func TestTraceTxBelowBaseFee(t *testing.T) {
	backend, block, txs := newReplayBackend(t, func(baseFee *big.Int) []types.TxData {
		return []types.TxData{transfer(0, baseFee, 30000, transferInput)}
	})
	backend.baseFee = new(big.Int).Add(block.BaseFee(), common.Big1)
	tx := txs[0]
	txErr := fmt.Sprintf("%v: address %v, maxFeePerGas: %v, baseFee: %v", core.ErrFeeCapTooLow, failedTxSender.Hex(), block.BaseFee(), backend.baseFee)
	input := hexutil.Encode(transferInput)
	callFrame := fmt.Sprintf(`{"from":"%s","gas":"0x7530","gasUsed":"0x0","to":"%s","input":"%s","error":"%s","value":"0x3e8","type":"CALL"}`,
		lower(failedTxSender), lower(failedTxTo), input, txErr)
	prestate := fmt.Sprintf(`{"%s":{"balance":"0x0"},"%s":{"balance":"0x0"},"%s":{"balance":"0xde0b6b3a7640000"}}`,
		lower(block.Coinbase()), lower(failedTxTo), lower(failedTxSender))
	prestateDiff := fmt.Sprintf(`{"post":{"%s":{"nonce":1}},"pre":{"%s":{"balance":"0xde0b6b3a7640000"}}}`,
		lower(failedTxSender), lower(failedTxSender))
	fourByte := `{"0xa9059cbb-32":1}`

	requireTraces(t, tracers.NewAPI(backend), tx, []tracerCase{
		{name: "structLogger", config: nil, want: `{"gas":0,"failed":true,"returnValue":"0x","structLogs":[]}`},
		{name: "callTracer", config: traceConfig("callTracer", ""), want: fmt.Sprintf(
			`{"error":"%s","from":"%s","gas":"0x7530","gasUsed":"0x0","input":"%s","to":"%s","type":"CALL","value":"0x3e8"}`,
			txErr, failedTxSender.Hex(), input, failedTxTo.Hex())},
		{name: "callTracer withLog", config: traceConfig("callTracer", `{"withLog":true}`), want: fmt.Sprintf(
			`{"error":"%s","from":"%s","gas":"0x7530","gasUsed":"0x0","input":"%s","to":"%s","type":"CALL","value":"0x3e8"}`,
			txErr, failedTxSender.Hex(), input, failedTxTo.Hex())},
		{name: "flatCallTracer", config: traceConfig("flatCallTracer", ""), want: fmt.Sprintf(
			`[{"action":{"callType":"call","from":"%s","gas":"0x7530","input":"%s","to":"%s","value":"0x3e8"},"blockHash":"%s","blockNumber":1,"error":"%s","result":{"gasUsed":"0x0","output":"0x"},"subtraces":0,"traceAddress":[],"transactionHash":"%s","transactionPosition":0}]`,
			failedTxSender.Hex(), input, failedTxTo.Hex(), block.Hash().Hex(), txErr, tx.Hash().Hex())},
		{name: "prestateTracer", config: traceConfig("prestateTracer", ""), want: prestate},
		{name: "prestateTracer diffMode", config: traceConfig("prestateTracer", `{"diffMode":true}`), want: prestateDiff},
		{name: "4byteTracer", config: traceConfig("4byteTracer", ""), want: fourByte},
		{name: "noopTracer", config: traceConfig("noopTracer", ""), want: `{}`},
		{name: "muxTracer", config: traceConfig("muxTracer", `{"callTracer":{"withLog":true},"prestateTracer":{"diffMode":true},"4byteTracer":{}}`), want: fmt.Sprintf(
			`{"callTracer":%s,"prestateTracer":%s,"4byteTracer":%s}`, callFrame, prestateDiff, fourByte)},
		{name: "muxTracer flatCallTracer", config: traceConfig("muxTracer", `{"flatCallTracer":{}}`), want: fmt.Sprintf(
			`{"flatCallTracer":[{"action":{"callType":"call","from":"%s","gas":"0x7530","input":"%s","to":"%s","value":"0x3e8"},"blockHash":"%s","blockNumber":1,"error":"%s","subtraces":0,"traceAddress":[],"transactionHash":"%s","transactionPosition":0,"type":"call"}]}`,
			lower(failedTxSender), input, lower(failedTxTo), block.Hash().Hex(), txErr, tx.Hash().Hex())},
		{name: "js", config: traceConfig(`{result: function(ctx) { return {type: ctx.type, error: ctx.error, gasUsed: ctx.gasUsed}; }, fault: function() {}}`, ""), want: fmt.Sprintf(
			`{"type":"CALL","error":"%s","gasUsed":0}`, txErr)},
	})
}

// TestTraceTxBelowFloorDataGas traces a block transaction whose gas limit falls below
// its EIP-7623 floor data gas: the chain fails it after charging it its gas limit, and
// bumps the sender's nonce.
func TestTraceTxBelowFloorDataGas(t *testing.T) {
	backend, block, txs := newReplayBackend(t, func(baseFee *big.Int) []types.TxData {
		return []types.TxData{transfer(0, baseFee, 22600, floorInput)}
	})
	backend.config = params.MergedTestChainConfig
	tx := txs[0]
	txErr := fmt.Sprintf("%v: have 22600, want 25000", core.ErrFloorDataGas)
	input := hexutil.Encode(floorInput)
	charged := new(big.Int).Sub(failedTxBalance, new(big.Int).Mul(big.NewInt(22600), block.BaseFee()))

	requireTraces(t, tracers.NewAPI(backend), tx, []tracerCase{
		{name: "structLogger", config: nil, want: `{"gas":22600,"failed":true,"returnValue":"0x","structLogs":[]}`},
		{name: "callTracer", config: traceConfig("callTracer", ""), want: fmt.Sprintf(
			`{"error":"%s","from":"%s","gas":"0x5848","gasUsed":"0x5848","input":"%s","to":"%s","type":"CALL","value":"0x3e8"}`,
			txErr, failedTxSender.Hex(), input, failedTxTo.Hex())},
		{name: "flatCallTracer", config: traceConfig("flatCallTracer", ""), want: fmt.Sprintf(
			`[{"action":{"callType":"call","from":"%s","gas":"0x5848","input":"%s","to":"%s","value":"0x3e8"},"blockHash":"%s","blockNumber":1,"error":"%s","result":{"gasUsed":"0x5848","output":"0x"},"subtraces":0,"traceAddress":[],"transactionHash":"%s","transactionPosition":0}]`,
			failedTxSender.Hex(), input, failedTxTo.Hex(), block.Hash().Hex(), txErr, tx.Hash().Hex())},
		{name: "prestateTracer diffMode", config: traceConfig("prestateTracer", `{"diffMode":true}`), want: fmt.Sprintf(
			`{"post":{"%s":{"balance":"%s","nonce":1}},"pre":{"%s":{"balance":"0xde0b6b3a7640000"}}}`,
			lower(failedTxSender), hexutil.EncodeBig(charged), lower(failedTxSender))},
		{name: "muxTracer", config: traceConfig("muxTracer", `{"callTracer":{}}`), want: fmt.Sprintf(
			`{"callTracer":{"from":"%s","gas":"0x5848","gasUsed":"0x5848","to":"%s","input":"%s","error":"%s","value":"0x3e8","type":"CALL"}}`,
			lower(failedTxSender), lower(failedTxTo), input, txErr)},
	})
}

// TestTraceBlockFailedTxThenSameSender traces a block in which a transaction that fails
// its state transition is followed by a transaction of the same sender, whose nonce
// relies on the failed transaction bumping it.
func TestTraceBlockFailedTxThenSameSender(t *testing.T) {
	backend, block, txs := newReplayBackend(t, func(baseFee *big.Int) []types.TxData {
		return []types.TxData{
			transfer(0, baseFee, 30000, transferInput),
			transfer(1, new(big.Int).Mul(baseFee, common.Big2), params.TxGas, nil),
		}
	})
	backend.baseFee = new(big.Int).Add(block.BaseFee(), common.Big1)
	backend.metadata = true
	api := tracers.NewAPI(backend)
	number := rpc.BlockNumber(block.NumberU64())

	for _, tc := range []struct {
		name   string
		config *tracers.TraceConfig
	}{
		{name: "structLogger", config: nil},
		{name: "callTracer", config: traceConfig("callTracer", "")},
		{name: "prestateTracer", config: traceConfig("prestateTracer", "")},
		{name: "prestateTracer diffMode", config: traceConfig("prestateTracer", `{"diffMode":true}`)},
		{name: "4byteTracer", config: traceConfig("4byteTracer", "")},
		{name: "muxTracer", config: traceConfig("muxTracer", `{"callTracer":{},"prestateTracer":{"diffMode":true}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results, err := api.TraceBlockByNumber(context.Background(), number, tc.config)
			require.NoError(t, err)
			require.Len(t, results, len(txs))
			failed, err := api.TraceTransaction(context.Background(), txs[0].Hash(), tc.config)
			require.NoError(t, err)
			have, err := json.Marshal(results[0].Result)
			require.NoError(t, err)
			require.JSONEq(t, string(failed.(json.RawMessage)), string(have))
			require.Equal(t, txs[1].Hash(), results[1].TxHash)
			require.Empty(t, results[1].Error)
		})
	}

	results, err := api.TraceBlockByNumber(context.Background(), number, traceConfig("prestateTracer", `{"diffMode":true}`))
	require.NoError(t, err)
	var diff struct {
		Pre, Post map[common.Address]struct{ Nonce uint64 }
	}
	bz, err := json.Marshal(results[1].Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(bz, &diff))
	require.Equal(t, uint64(1), diff.Pre[failedTxSender].Nonce, "transaction 1 starts from the nonce transaction 0 bumped")
	require.Equal(t, uint64(2), diff.Post[failedTxSender].Nonce)
}

// TestTraceCallBelowBaseFee checks that a call whose fee cap falls below the base fee
// still reports the failure, as a call is not part of a block.
func TestTraceCallBelowBaseFee(t *testing.T) {
	backend, block, _ := newReplayBackend(t, func(baseFee *big.Int) []types.TxData {
		return []types.TxData{transfer(0, baseFee, 30000, transferInput)}
	})
	api := tracers.NewAPI(backend)
	sender := failedTxSender
	args := ethapi.TransactionArgs{
		From:                 &sender,
		To:                   &failedTxTo,
		Gas:                  (*hexutil.Uint64)(new(uint64)),
		MaxFeePerGas:         (*hexutil.Big)(common.Big1),
		MaxPriorityFeePerGas: (*hexutil.Big)(common.Big0),
	}
	*args.Gas = hexutil.Uint64(params.TxGas)
	txErr := fmt.Sprintf("%v: address %v, maxFeePerGas: 1, baseFee: %v", core.ErrFeeCapTooLow, failedTxSender.Hex(), block.BaseFee())
	at := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(block.NumberU64()))

	_, err := api.TraceCall(context.Background(), args, at, &tracers.TraceCallConfig{TraceConfig: *traceConfig("prestateTracer", "")})
	require.EqualError(t, err, "tracing failed: "+txErr)

	res, err := api.TraceCall(context.Background(), args, at, &tracers.TraceCallConfig{TraceConfig: *traceConfig("callTracer", "")})
	require.NoError(t, err)
	require.JSONEq(t, fmt.Sprintf(`{"error":"%s","from":"%s","gas":"0x5208","gasUsed":"0x0","input":"0x","to":"%s","type":"CALL","value":"0x0"}`,
		txErr, failedTxSender.Hex(), failedTxTo.Hex()), string(res.(json.RawMessage)))
}

func TestEndFailedTx(t *testing.T) {
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	tx := types.NewTx(transfer(0, common.Big1, 22600, floorInput))
	message := &core.Message{From: failedTxSender, To: &failedTxTo, Nonce: 0, GasLimit: 22600, Value: big.NewInt(1000), Data: floorInput}
	inBlock := &tracers.Context{BlockHash: common.Hash{1}, BlockNumber: common.Big1, TxHash: tx.Hash()}

	_, ended := tracers.EndFailedTx(nil, statedb, tx, message, &tracers.Context{}, core.ErrFloorDataGas)
	require.False(t, ended, "a call is not part of a block")
	stale := *message
	stale.Nonce = 1
	_, ended = tracers.EndFailedTx(nil, statedb, tx, &stale, inBlock, core.ErrFloorDataGas)
	require.False(t, ended, "the chain does not bump a nonce that is not the next one")
	require.Zero(t, statedb.GetNonce(failedTxSender))

	gasUsed, ended := tracers.EndFailedTx(nil, statedb, tx, message, inBlock, fmt.Errorf("%w: have 22600, want 25000", core.ErrFloorDataGas))
	require.True(t, ended)
	require.Equal(t, uint64(22600), gasUsed, "failing after the gas purchase is charged the gas limit")
	require.Equal(t, uint64(1), statedb.GetNonce(failedTxSender))

	message.Nonce = 1
	gasUsed, ended = tracers.EndFailedTx(nil, statedb, tx, message, inBlock, fmt.Errorf("%w: address %v", core.ErrFeeCapTooLow, failedTxSender))
	require.True(t, ended)
	require.Zero(t, gasUsed, "failing before the gas purchase is not charged")
	require.Equal(t, uint64(2), statedb.GetNonce(failedTxSender))
}
