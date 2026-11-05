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

// Sei extensions to the tracing API. These are kept out of api.go so that
// upstream updates only conflict at the call sites.

package tracers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers/tracersutils"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

var errExecutionTimeout = errors.New("execution timeout")

// TxTraceResult is the result of a single transaction trace.
type TxTraceResult = txTraceResult

// SeiBackend holds the Backend methods needed by chains (such as Sei) whose
// blocks, state and precompiles are not those of a geth node.
type SeiBackend interface {
	// BlockWithTraceMetadataByNumber returns the block together with the
	// metadata describing how to replay it. A nil metadata slice means every
	// transaction of the block is traced in order.
	BlockWithTraceMetadataByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, []tracersutils.TraceBlockMetadata, error)
	// BlockWithTraceMetadataByHash is BlockWithTraceMetadataByNumber by hash.
	BlockWithTraceMetadataByHash(ctx context.Context, hash common.Hash) (*types.Block, []tracersutils.TraceBlockMetadata, error)
	// ChainConfigAtHeight returns the chain configuration in effect at height.
	ChainConfigAtHeight(height int64) *params.ChainConfig
	// GetCustomPrecompiles returns the chain-specific precompiles at height.
	GetCustomPrecompiles(height int64) map[common.Address]vm.CustomPrecompiledContract
	// PrepareTx prepares statedb for executing tx, before it is traced.
	PrepareTx(statedb vm.StateDB, tx *types.Transaction) error
	// GetBlockContext returns the EVM block context used to execute block on
	// top of statedb.
	GetBlockContext(ctx context.Context, block *types.Block, statedb vm.StateDB, backend ethapi.ChainContextBackend) (vm.BlockContext, error)
}

// blockWithTraceMetadataByNumber is the metadata aware variant of blockByNumber.
func (api *API) blockWithTraceMetadataByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, []tracersutils.TraceBlockMetadata, error) {
	block, metadata, err := api.backend.BlockWithTraceMetadataByNumber(ctx, number)
	if err != nil {
		return nil, nil, err
	}
	if block == nil {
		return nil, nil, fmt.Errorf("block #%d not found", number)
	}
	return block, metadata, nil
}

// blockWithTraceMetadataByHash is the metadata aware variant of blockByHash.
func (api *API) blockWithTraceMetadataByHash(ctx context.Context, hash common.Hash) (*types.Block, []tracersutils.TraceBlockMetadata, error) {
	block, metadata, err := api.backend.BlockWithTraceMetadataByHash(ctx, hash)
	if err != nil {
		return nil, nil, err
	}
	if block == nil {
		return nil, nil, fmt.Errorf("block %s not found", hash.Hex())
	}
	return block, metadata, nil
}

// newEVM creates an EVM for executing at the given block number, using the
// chain configuration and custom precompiles in effect at that height.
func (api *API) newEVM(blockCtx vm.BlockContext, statedb vm.StateDB, number int64, vmConfig vm.Config) *vm.EVM {
	return vm.NewEVMWithCustomPrecompiles(blockCtx, statedb, api.backend.ChainConfigAtHeight(number), vmConfig, api.backend.GetCustomPrecompiles(number))
}

// traceBlockWithMetadata traces the transactions of block as described by
// metadata. Entries flagged ShouldIncludeInTraceResult trace the transaction at
// IdxInEthBlock; the others run their TraceRunnable, so that transactions which
// are not part of the EVM block still update the state.
func (api *API) traceBlockWithMetadata(ctx context.Context, block *types.Block, metadata []tracersutils.TraceBlockMetadata, blockCtx vm.BlockContext, statedb vm.SeiStateDB, config *TraceConfig) ([]*TxTraceResult, error) {
	var (
		txs       = block.Transactions()
		blockHash = block.Hash()
		signer    = types.MakeSigner(api.backend.ChainConfig(), block.Number(), block.Time())
		results   = make([]*TxTraceResult, len(txs))
	)
	for i, md := range metadata {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("trace aborted at metadata entry %d/%d: %w", i+1, len(metadata), err)
		}
		if !md.ShouldIncludeInTraceResult {
			// Not included in the result, but still needs to be run because it
			// may affect the cumulative state.
			if md.TraceRunnable != nil {
				md.TraceRunnable(statedb)
			}
			continue
		}
		idx := md.IdxInEthBlock
		if idx < 0 || idx >= len(txs) {
			return nil, fmt.Errorf("trace metadata entry %d references transaction %d, block has %d", i, idx, len(txs))
		}
		tx := txs[idx]
		msg, _ := core.TransactionToMessage(tx, signer, block.BaseFee())
		txctx := &Context{
			BlockHash:   blockHash,
			BlockNumber: block.Number(),
			TxIndex:     idx,
			TxHash:      tx.Hash(),
		}
		res, err := api.traceTx(ctx, tx, msg, txctx, blockCtx, statedb, config, nil)
		if err != nil {
			results[idx] = &TxTraceResult{TxHash: tx.Hash(), Error: err.Error()}
			// Discard the partial changes of the failed transaction. The native
			// state only changes once a transaction passes its pre-checks and
			// has no snapshot to revert to otherwise.
			if _, native := vm.NativeState(statedb); !native {
				statedb.RevertToSnapshot(0)
			}
		} else {
			results[idx] = &TxTraceResult{TxHash: tx.Hash(), Result: res}
		}
	}
	return results, nil
}

// safeCallHash returns the hash of the transaction described by args, or the
// zero hash if it cannot be built. It is used while recovering from a panic.
func safeCallHash(args ethapi.TransactionArgs) (hash common.Hash) {
	defer func() { _ = recover() }()
	return args.ToTransaction(types.LegacyTxType).Hash()
}

// errorTrace converts a failure to execute a transaction into a trace result.
// The call tracers get a single failed top-level frame, so that clients still
// receive a well-formed trace.
func errorTrace(err error, tx *types.Transaction, message *core.Message, txctx *Context, vmctx vm.BlockContext, config *TraceConfig) (interface{}, error) {
	if config != nil && config.Tracer != nil {
		switch *config.Tracer {
		case "callTracer":
			errTrace := map[string]interface{}{
				"from":    message.From.Hex(),
				"gas":     hexutil.Uint64(message.GasLimit),
				"gasUsed": "0x0",
				"input":   "0x",
				"error":   err.Error(),
				"type":    "CALL",
			}
			if message.Value != nil {
				errTrace["value"] = (*hexutil.Big)(message.Value.ToBig())
			}
			if message.To != nil {
				errTrace["to"] = message.To.Hex()
			} else {
				errTrace["type"] = "CREATE"
			}
			if message.Data != nil {
				errTrace["input"] = hexutil.Encode(message.Data)
			}
			bz, err := json.Marshal(errTrace)
			if err != nil {
				return nil, fmt.Errorf("tracing failed: %w", err)
			}
			return json.RawMessage(bz), nil
		case "flatCallTracer":
			action := map[string]interface{}{
				"callType": "call",
				"from":     message.From.Hex(),
				"gas":      hexutil.Uint64(message.GasLimit),
				"input":    "0x",
			}
			if message.Value != nil {
				action["value"] = (*hexutil.Big)(message.Value.ToBig())
			}
			if message.To != nil {
				action["to"] = message.To.Hex()
			}
			if message.Data != nil {
				action["input"] = hexutil.Encode(message.Data)
			}
			errTrace := map[string]interface{}{
				"action":      action,
				"blockHash":   txctx.BlockHash,
				"blockNumber": txctx.BlockNumber,
				"result": map[string]interface{}{
					"gasUsed": "0x0",
					"output":  "0x",
				},
				"subtraces":           0,
				"traceAddress":        []string{},
				"transactionHash":     tx.Hash(),
				"transactionPosition": txctx.TxIndex,
				"error":               err.Error(),
			}
			bz, err := json.Marshal([]map[string]interface{}{errTrace})
			if err != nil {
				return nil, fmt.Errorf("tracing failed: %w", err)
			}
			return json.RawMessage(bz), nil
		}
	}
	// A transaction with insufficient funds can be included in a Sei block, as
	// fees are charged before execution. Such a transaction has nothing to trace.
	// Calls which are not part of a block (TraceCall) still report the error.
	if txctx != nil && txctx.BlockHash != (common.Hash{}) && strings.Contains(err.Error(), core.ErrInsufficientFunds.Error()) {
		return json.RawMessage(`{}`), nil
	}
	return nil, fmt.Errorf("tracing failed: %w", err)
}
