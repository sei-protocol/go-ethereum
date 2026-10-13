// Copyright 2025 The go-ethereum Authors
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

package rpc

// Sei: concurrent request-byte admission control for persistent connections
// (WebSocket, IPC, stdio) and the per-dispatch deadline hook. Upstream files only
// call into this file at their choke points (newHandler, jsonCodec.readBatch,
// websocket frame reads, Client.read/dispatch, handleBatch/handleMsg, runMethod).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sync/semaphore"
)

const (
	// WSAdmissionReasonBudgetWaitTimeout is emitted when the read loop times out
	// waiting for concurrent-byte budget before the next frame is decoded.
	WSAdmissionReasonBudgetWaitTimeout = "budget_wait_timeout"
	// WSAdmissionReasonFrameAdmissionTimeout is emitted when a decoded frame
	// cannot be admitted under the concurrent-byte budget.
	WSAdmissionReasonFrameAdmissionTimeout = "frame_admission_timeout"
	// WSAdmissionReasonOversizeFrame is emitted when gorilla's read limit rejects
	// an incoming message, on the first frame or partway through continuation frames.
	WSAdmissionReasonOversizeFrame = "oversize_frame"

	defaultWSAdmissionTimeout = 30 * time.Second
)

const (
	errcodeRequestTooLarge   = -32004
	errcodeBudgetWaitTimeout = -32005

	errMsgRequestTooLarge   = "request exceeds concurrent request-byte budget"
	errMsgBudgetWaitTimeout = "timed out waiting for concurrent request-byte budget"
)

// errBudgetWaitTimeout is a sentinel error that the handler wraps its pre-decode
// budget-acquire error in, so the read loop can recognize an admission-control
// rejection and report it to the peer before closing the connection.
var errBudgetWaitTimeout = errors.New(errMsgBudgetWaitTimeout)

// handlerSeiConfig carries the Sei-specific settings from Server/Client into each
// connection handler. It is passed to newHandler as a single value so upstream call
// sites only change by one argument.
type handlerSeiConfig struct {
	wsConcurrentBudget *semaphore.Weighted
	readLimit          int64
	// admissionEventHook is called on WS admission-control events (see
	// WSAdmissionReason*).
	admissionEventHook func(reason string)
	wsAdmissionTimeout time.Duration
	// deadlineHook applies a context to every method dispatch in runMethod; see
	// Server.SetDeadlineHook.
	deadlineHook DeadlineHook
}

// handlerSei is embedded in handler so its fields are promoted.
type handlerSei struct {
	handlerSeiConfig
	// preDecodeHeld is true if acquirePreDecode reserved budget for the frame in
	// flight. Single-goroutine access only (Client.read), so a plain bool is fine.
	preDecodeHeld bool
}

func newHandlerSei(cfg handlerSeiConfig) handlerSei {
	cfg.wsAdmissionTimeout = wsAdmissionTimeoutOrDefault(cfg.wsAdmissionTimeout)
	return handlerSei{handlerSeiConfig: cfg}
}

func wsAdmissionTimeoutOrDefault(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return defaultWSAdmissionTimeout
}

// acquirePreDecode reserves frame budget before the codec reads/decodes the next
// message. The wait is bounded by the handler's wsAdmissionTimeout.
func (h *handler) acquirePreDecode(ctx context.Context) error {
	if h.wsConcurrentBudget == nil || h.readLimit <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, h.wsAdmissionTimeout)
	defer cancel()
	if err := h.wsConcurrentBudget.Acquire(ctx, h.readLimit); err != nil {
		h.fireAdmissionEventOnBudgetTimeout(err, WSAdmissionReasonBudgetWaitTimeout)
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", errBudgetWaitTimeout, err)
		}
		// Not a timeout (e.g. connection teardown) — don't report as one.
		return fmt.Errorf("concurrent request byte budget: %w", err)
	}
	h.preDecodeHeld = true
	return nil
}

func (h *handler) fireAdmissionEventOnBudgetTimeout(err error, reason string) {
	if errors.Is(err, context.DeadlineExceeded) && h.admissionEventHook != nil {
		h.admissionEventHook(reason)
	}
}

// releasePreDecode releases a pre-decode reservation after a failed read/decode or a
// failed commitFrameBudget call.
func (h *handler) releasePreDecode() {
	if !h.preDecodeHeld {
		return
	}
	h.preDecodeHeld = false
	h.wsConcurrentBudget.Release(h.readLimit)
}

// commitFrameBudget finalizes the reservation for a successfully decoded frame,
// reconciling any pre-decode reservation against the frame's actual size. If no
// pre-decode reservation is held (readLimit unset, or the codec never wired in
// this handler), it falls back to acquiring the full frame size directly.
func (h *handler) commitFrameBudget(ctx context.Context, rawLen int64) (release func(), err error) {
	if h.wsConcurrentBudget == nil {
		return func() {}, nil
	}
	if h.readLimit <= 0 || !h.preDecodeHeld {
		if rawLen <= 0 {
			return func() {}, nil
		}
		ctx, cancel := context.WithTimeout(ctx, h.wsAdmissionTimeout)
		defer cancel()
		if err := h.wsConcurrentBudget.Acquire(ctx, rawLen); err != nil {
			h.fireAdmissionEventOnBudgetTimeout(err, WSAdmissionReasonFrameAdmissionTimeout)
			return nil, fmt.Errorf("concurrent request byte budget exhausted: %w", err)
		}
		return func() { h.wsConcurrentBudget.Release(rawLen) }, nil
	}

	weight := rawLen
	if weight <= 0 {
		weight = h.readLimit
	}
	switch {
	case weight < h.readLimit:
		h.wsConcurrentBudget.Release(h.readLimit - weight)
	case weight > h.readLimit:
		ctx, cancel := context.WithTimeout(ctx, h.wsAdmissionTimeout)
		defer cancel()
		if err := h.wsConcurrentBudget.Acquire(ctx, weight-h.readLimit); err != nil {
			h.fireAdmissionEventOnBudgetTimeout(err, WSAdmissionReasonFrameAdmissionTimeout)
			return nil, fmt.Errorf("concurrent request byte budget exhausted: %w", err)
		}
	}
	h.preDecodeHeld = false
	return func() { h.wsConcurrentBudget.Release(weight) }, nil
}

// frameBudgetExceededResponse builds the JSON-RPC error response for a frame that decoded
// successfully but could not be admitted under the concurrent request-byte budget. It
// returns nil when no response is due (a single notification).
func frameBudgetExceededResponse(msgs []*jsonrpcMessage, batch bool) *jsonrpcMessage {
	err := &internalServerError{errcodeRequestTooLarge, errMsgRequestTooLarge}
	if !batch {
		msg := msgs[0]
		if msg.isNotification() {
			return nil
		}
		return msg.errorResponse(err)
	}
	resp := errorMessage(err)
	for _, msg := range msgs {
		if msg.isCall() {
			resp.ID = msg.ID
			break
		}
	}
	return resp
}

// handleReadErrorSei writes the Sei-specific responses for a read error. Unmatched
// errors (e.g. oversize WS frames) get no response: gorilla already sent its own
// close(1009) for those.
func handleReadErrorSei(codec ServerCodec, err error) {
	if errors.Is(err, errBudgetWaitTimeout) {
		msg := errorMessage(&internalServerError{errcodeBudgetWaitTimeout, errMsgBudgetWaitTimeout})
		codec.writeJSON(context.Background(), msg, true)
	}
}

// admitFrame commits budget for a successfully read frame. On failure the pre-decode
// reservation is released, the peer is told why, and the returned error should end
// the read loop.
func (h *handler) admitFrame(codec ServerCodec, msgs []*jsonrpcMessage, batch bool, rawLen int64) (func(), error) {
	release, err := h.commitFrameBudget(h.rootCtx, rawLen)
	if err != nil {
		h.releasePreDecode()
		if resp := frameBudgetExceededResponse(msgs, batch); resp != nil {
			if batch {
				codec.writeJSONBatch(context.Background(), []*jsonrpcMessage{resp}, true)
			} else {
				codec.writeJSON(context.Background(), resp, true)
			}
		}
		return nil, err
	}
	return release, nil
}

// handlerSetter is implemented by codecs that need the handler wired in to enforce its
// read limits (jsonCodec and, via embedding, websocketCodec).
type handlerSetter interface {
	setHandler(h *handler)
}

// attachHandler wires h into codec so reads on it observe the handler's limits. A codec
// that does not implement handlerSetter reads unlimited, and the handler's own checks
// remain the backstop.
func attachHandler(codec ServerCodec, h *handler) {
	if c, ok := codec.(handlerSetter); ok {
		c.setHandler(h)
	}
}

// setHandler wires in the handler whose limits govern reads on this codec.
func (c *jsonCodec) setHandler(h *handler) {
	c.handler = h
}

// batchItemLimit returns the number of batch elements a read may decode, or 0 when no
// limit applies.
func (c *jsonCodec) batchItemLimit() int {
	if c.handler == nil {
		return 0
	}
	return c.handler.batchRequestLimit
}

// beforeReadSei reserves budget before a stream codec's blocking read. Unlike
// websocketCodec, this happens before the next message starts arriving, so an idle
// IPC/stdio connection still holds it indefinitely. Codecs that delimit messages
// themselves (WebSocket) reserve in their frame reader instead.
func (c *jsonCodec) beforeReadSei() error {
	if c.handler == nil || c.readFrame != nil {
		return nil
	}
	return c.handler.acquirePreDecode(c.handler.rootCtx)
}

// readFailedSei releases any pre-decode reservation after a failed read.
func (c *jsonCodec) readFailedSei() {
	if c.handler != nil {
		c.handler.releasePreDecode()
	}
}

// readFrameSei reads the next WebSocket message. It waits for the first frame without
// holding budget, so idle connections reserve nothing, then reserves readLimit bytes
// and bounds how long the rest of the message may take to arrive.
func (wc *websocketCodec) readFrameSei() ([]byte, error) {
	_, r, err := wc.conn.NextReader()
	if err != nil {
		wc.fireOversizeFrameHook(err)
		return nil, err
	}
	if h := wc.handler; h != nil {
		if err := h.acquirePreDecode(h.rootCtx); err != nil {
			return nil, err
		}
		if h.preDecodeHeld {
			wc.beginDecodeRead(h.wsAdmissionTimeout)
			defer wc.endDecodeRead()
		}
	}
	frame, err := io.ReadAll(r)
	if err != nil {
		// jsonCodec.readBatch releases the pre-decode reservation.
		//
		// A message split across continuation frames only crosses gorilla's read
		// limit once enough frames have arrived, so ErrReadLimit can surface here
		// instead of from NextReader above.
		wc.fireOversizeFrameHook(err)
		return nil, err
	}
	return frame, nil
}

// beginDecodeRead arms a read deadline bounding how long a frame may take to finish
// arriving once pre-decode budget has been reserved for it, so a peer that stalls
// mid-message (and keeps ponging to defeat pingLoop's own liveness deadline) cannot
// hold that budget indefinitely.
func (wc *websocketCodec) beginDecodeRead(timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	wc.jsonCodec.encMu.Lock()
	defer wc.jsonCodec.encMu.Unlock()
	wc.decodeReadActive = true
	wc.conn.SetReadDeadline(time.Now().Add(timeout))
}

// endDecodeRead clears the deadline armed by beginDecodeRead once the frame has been
// fully read (successfully or not).
func (wc *websocketCodec) endDecodeRead() {
	wc.jsonCodec.encMu.Lock()
	defer wc.jsonCodec.encMu.Unlock()
	if !wc.decodeReadActive {
		return
	}
	wc.decodeReadActive = false
	wc.conn.SetReadDeadline(time.Time{})
}

// fireOversizeFrameHook reports a frame rejected by gorilla's read-limit enforcement
// to the admission event hook. No JSON-RPC response is written for this case: gorilla
// already sends a close(1009, "message too big") handshake to the peer on its own.
func (wc *websocketCodec) fireOversizeFrameHook(err error) {
	if errors.Is(err, websocket.ErrReadLimit) && wc.handler != nil && wc.handler.admissionEventHook != nil {
		wc.handler.admissionEventHook(WSAdmissionReasonOversizeFrame)
	}
}

// DeadlineHook derives the context used to invoke an RPC method. The returned
// context must be non-nil and derived from ctx so that request values and
// cancellation are preserved. The returned CancelFunc must also be non-nil;
// the server calls it after the method callback returns.
//
// A deadline only cancels the derived context. Method callbacks must observe
// context cancellation for the deadline to stop their work.
//
// A method that fails after the deadline passed reports the server's standard
// timeout error (-32002, "request timed out") instead of its own. Anything
// else, including cancellation without a deadline, is reported as-is.
type DeadlineHook func(ctx context.Context, method string) (context.Context, context.CancelFunc)

// deadlineHookScope tracks a deadlineHook application for one method dispatch.
type deadlineHookScope struct {
	parentCtx context.Context
	hookCtx   context.Context
	cancel    context.CancelFunc
}

// startDeadlineHook applies the handler's deadlineHook (if any) to ctx. runMethod is
// reached by the plain call path (handleCall) as well as the *_subscribe setup and
// *_unsubscribe paths, so all of them are bounded here. Call done once the response
// has been built.
func (h *handler) startDeadlineHook(ctx context.Context, method string) (context.Context, deadlineHookScope) {
	if h.deadlineHook == nil {
		return ctx, deadlineHookScope{}
	}
	scope := deadlineHookScope{parentCtx: ctx}
	scope.hookCtx, scope.cancel = h.deadlineHook(ctx, method)
	// Keep dispatch safe if a misconfigured hook violates the documented
	// non-nil return contract.
	if scope.hookCtx != nil {
		ctx = scope.hookCtx
	}
	return ctx, scope
}

func (s deadlineHookScope) done() {
	if s.cancel != nil {
		s.cancel()
	}
}

// mapErr reports a method failure after a hook deadline as the handler's own
// request-timeout error instead of whatever the method returned for its cancelled
// context. Requiring the parent to still be live keeps a client disconnect or an
// outer timeout from being reported as one.
func (s deadlineHookScope) mapErr(err error) error {
	if s.hookCtx != nil && errors.Is(s.hookCtx.Err(), context.DeadlineExceeded) && s.parentCtx.Err() == nil {
		return &internalServerError{errcodeTimeout, errMsgTimeout}
	}
	return err
}

// releaseOnce wraps a budget release so it is safe to call on every exit path; a nil
// release becomes a no-op.
func releaseOnce(release func()) func() {
	if release == nil {
		return func() {}
	}
	return sync.OnceFunc(release)
}

// WithWSAdmissionTimeout bounds how long incoming frames wait for concurrent-byte
// budget before admission is rejected. Zero or negative values select the default (30s).
//
// Note: this option applies when processing incoming frames on persistent connections.
func WithWSAdmissionTimeout(timeout time.Duration) ClientOption {
	return optionFunc(func(cfg *clientConfig) {
		cfg.sei.wsAdmissionTimeout = timeout
	})
}
