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

// Sei: Server configuration for the method deny list, WebSocket admission control and
// the per-dispatch deadline hook.

import (
	"context"
	"time"

	"golang.org/x/sync/semaphore"
)

// serverSei is embedded in Server so its fields are promoted.
type serverSei struct {
	denyList                 map[string]struct{}
	wsConcurrentRequestBytes int64 // configured limit; 0 disables
	wsConcurrentBudget       *semaphore.Weighted
	admissionEventHook       func(reason string)
	wsAdmissionTimeout       time.Duration
	deadlineHook             DeadlineHook
}

// handlerSeiConfig returns the Sei settings handed to persistent-connection handlers.
func (s *Server) handlerSeiConfig() handlerSeiConfig {
	return handlerSeiConfig{
		wsConcurrentBudget: s.wsConcurrentBudget,
		readLimit:          s.wsReadLimit,
		admissionEventHook: s.admissionEventHook,
		wsAdmissionTimeout: s.wsAdmissionTimeout,
		deadlineHook:       s.deadlineHook,
	}
}

// httpHandlerSeiConfig returns the Sei settings for single-request (HTTP) handlers,
// which are not subject to the persistent-connection byte budget.
func (s *Server) httpHandlerSeiConfig() handlerSeiConfig {
	cfg := s.handlerSeiConfig()
	cfg.wsConcurrentBudget = nil
	cfg.admissionEventHook = nil
	return cfg
}

// SetReadLimits sets the limit for max message size for Websocket requests. It is
// equivalent to SetWebsocketReadLimit, which also resizes the concurrent request-byte
// budget so it fits at least one maximum-size frame.
//
// This method should be called before processing any requests via Websocket server.
func (s *Server) SetReadLimits(limit int64) {
	s.SetWebsocketReadLimit(limit)
}

// SetWSConcurrentRequestBytes bounds the total size, in bytes, of JSON-RPC request
// frames on persistent connections (WebSocket, IPC, stdio) that may be read and
// processed concurrently, weighted by each frame's size. Set to 0 to disable the limit.
// This method should be called before processing any requests via Websocket server.
//
// Note: idle connections avoid holding this budget on WebSocket only; idle
// IPC/stdio connections still hold readLimit bytes indefinitely.
func (s *Server) SetWSConcurrentRequestBytes(limit int64) {
	s.wsConcurrentRequestBytes = limit
	s.recomputeWSConcurrentBudget()
}

// SetWSAdmissionEventHook registers a callback invoked on WS admission-control
// events. reason is one of:
//   - WSAdmissionReasonBudgetWaitTimeout when the read loop stalls waiting for
//     concurrent-byte budget before the next frame is decoded;
//   - WSAdmissionReasonFrameAdmissionTimeout when a decoded frame cannot be
//     admitted under the concurrent-byte budget;
//   - WSAdmissionReasonOversizeFrame when gorilla's read limit rejects an
//     incoming message before or during decode.
func (s *Server) SetWSAdmissionEventHook(hook func(reason string)) {
	s.admissionEventHook = hook
}

// SetWSAdmissionTimeout bounds how long a persistent connection will wait for
// concurrent-byte budget to free up before a frame is read or committed, and how
// long a WebSocket frame may take to finish arriving after its first data frame
// once budget has been reserved for it. Zero or negative values select the
// default (30s). This method should be called before processing any requests
// via Websocket server.
func (s *Server) SetWSAdmissionTimeout(timeout time.Duration) {
	s.wsAdmissionTimeout = timeout
}

// SetDeadlineHook registers a hook that applies a context to every RPC method
// dispatch across all transports, including *_subscribe setup and
// *_unsubscribe calls. The hook receives the request context and full
// "namespace_method" name.
//
// SetDeadlineHook is not safe for concurrent use. Call it before serving
// requests.
func (s *Server) SetDeadlineHook(hook DeadlineHook) {
	s.deadlineHook = hook
}

// RegisterDenyList add given method name to the deny list so that RPC requests that matches
// any of the methods in deny list will got rejected directly.
//
// The deny list is enforced for single-request (HTTP) serving.
func (s *Server) RegisterDenyList(methodName string) {
	if s.denyList == nil {
		s.denyList = make(map[string]struct{})
	}
	s.denyList[methodName] = struct{}{}
}

// rejectDenied writes a method-not-found error and returns true if any request in reqs
// calls a method on the deny list.
func (s *Server) rejectDenied(ctx context.Context, codec ServerCodec, reqs []*jsonrpcMessage) bool {
	for _, req := range reqs {
		if _, found := s.denyList[req.Method]; found {
			resp := errorMessage(&methodNotFoundError{method: req.Method})
			codec.writeJSON(ctx, resp, true)
			return true
		}
	}
	return false
}

func (s *Server) recomputeWSConcurrentBudget() {
	limit := s.wsConcurrentRequestBytes
	if limit <= 0 {
		s.wsConcurrentBudget = nil
		return
	}
	if s.wsReadLimit > 0 && limit < s.wsReadLimit {
		limit = s.wsReadLimit
	}
	s.wsConcurrentBudget = semaphore.NewWeighted(limit)
}
