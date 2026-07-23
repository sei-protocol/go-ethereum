package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"
)

func TestWSConcurrentRequestBytesSingleInFlight(t *testing.T) {
	t.Parallel()

	const pad = 48
	srv := newTestServer()
	p1, p2 := net.Pipe()
	sleepDuration := 50 * time.Millisecond
	makeMsg := func(id int) string {
		return fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"test_sleep","params":[%d],"_pad":"%s"}`,
			id, sleepDuration.Nanoseconds(), strings.Repeat("x", pad),
		)
	}
	payload := makeMsg(1)
	frameSize := int64(len(payload))
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(frameSize)
	go srv.ServeCodec(NewCodec(p1), 0)
	t.Cleanup(func() { p2.Close(); p1.Close(); srv.Stop() })
	if _, err := io.WriteString(p2, payload); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(p2)
	_ = p2.SetReadDeadline(time.Now().Add(time.Second))
	var resp jsonrpcMessage
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestWSConcurrentRequestBytes(t *testing.T) {
	t.Parallel()

	const (
		sleepDuration = 200 * time.Millisecond
		pad           = 48
	)

	srv := newTestServer()

	p1, p2 := net.Pipe()

	makeMsg := func(id int) string {
		return fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"test_sleep","params":[%d],"_pad":"%s"}`,
			id, sleepDuration.Nanoseconds(), strings.Repeat("x", pad),
		)
	}
	payload := makeMsg(1)
	frameSize := int64(len(payload))
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(frameSize)

	serveDone := make(chan struct{})
	go func() {
		srv.ServeCodec(NewCodec(p1), 0)
		close(serveDone)
	}()

	t.Cleanup(func() {
		p2.Close()
		p1.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("ServeCodec did not exit within 2s")
		}
		srv.Stop()
	})

	writeReq := func(id int) {
		msg := makeMsg(id)
		if len(msg) != len(payload) {
			t.Fatalf("unexpected payload size %d != %d", len(msg), len(payload))
		}
		if _, err := io.WriteString(p2, msg); err != nil {
			t.Fatalf("write request %d: %v", id, err)
		}
	}

	// Budget holds through handler work; the second frame is not read until then.
	writeReq(1)
	writeReq(2)

	dec := json.NewDecoder(p2)

	_ = p2.SetReadDeadline(time.Now().Add(sleepDuration + time.Second))
	var firstResp jsonrpcMessage
	if err := dec.Decode(&firstResp); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if string(firstResp.ID) != "1" {
		t.Fatalf("expected first response id 1, got %s", string(firstResp.ID))
	}

	_ = p2.SetReadDeadline(time.Now().Add(sleepDuration + time.Second))
	var secondResp jsonrpcMessage
	if err := dec.Decode(&secondResp); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if string(secondResp.ID) != "2" {
		t.Fatalf("expected second response id 2, got %s", string(secondResp.ID))
	}
	_ = p2.SetReadDeadline(time.Time{})
}

func TestSetWSConcurrentRequestBytesRaisesToReadLimit(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	srv.SetReadLimits(500)
	srv.SetWSConcurrentRequestBytes(100)
	if srv.wsConcurrentBudget == nil {
		t.Fatal("expected budget to be configured")
	}
	if !srv.wsConcurrentBudget.TryAcquire(500) {
		t.Fatal("budget should allow a single max-size frame")
	}
	srv.wsConcurrentBudget.Release(500)
}

func TestSetReadLimitsRecomputesConcurrentBudget(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	srv.SetWSConcurrentRequestBytes(100)
	srv.SetReadLimits(500)
	if srv.wsConcurrentBudget == nil {
		t.Fatal("expected budget to be configured")
	}
	if !srv.wsConcurrentBudget.TryAcquire(500) {
		t.Fatal("budget should be recomputed when read limit increases")
	}
	srv.wsConcurrentBudget.Release(500)
}

// TestFrameBudgetExceededResponse covers the response written when a frame decodes
// successfully but exceeds the concurrent request-byte budget.
func TestFrameBudgetExceededResponse(t *testing.T) {
	t.Parallel()

	call := &jsonrpcMessage{Version: vsn, ID: json.RawMessage("1"), Method: "test_method"}
	notification := &jsonrpcMessage{Version: vsn, Method: "test_method"}

	t.Run("call", func(t *testing.T) {
		resp := frameBudgetExceededResponse([]*jsonrpcMessage{call}, false)
		msg, ok := resp.(*jsonrpcMessage)
		if !ok {
			t.Fatalf("expected *jsonrpcMessage, got %T", resp)
		}
		if msg.Error == nil || msg.Error.Code != errcodeRequestTooLarge {
			t.Fatalf("expected error code %d, got %+v", errcodeRequestTooLarge, msg.Error)
		}
		if string(msg.ID) != string(call.ID) {
			t.Fatalf("expected response id %s, got %s", call.ID, msg.ID)
		}
	})

	t.Run("notification", func(t *testing.T) {
		if resp := frameBudgetExceededResponse([]*jsonrpcMessage{notification}, false); resp != nil {
			t.Fatalf("expected no response for a notification, got %v", resp)
		}
	})

	t.Run("batch", func(t *testing.T) {
		resp := frameBudgetExceededResponse([]*jsonrpcMessage{notification, call}, true)
		batch, ok := resp.([]*jsonrpcMessage)
		if !ok || len(batch) != 1 {
			t.Fatalf("expected a single-element batch response, got %T: %v", resp, resp)
		}
		if batch[0].Error == nil || batch[0].Error.Code != errcodeRequestTooLarge {
			t.Fatalf("expected error code %d, got %+v", errcodeRequestTooLarge, batch[0].Error)
		}
		if string(batch[0].ID) != string(call.ID) {
			t.Fatalf("expected response tagged with the call's id %s, got %s", call.ID, batch[0].ID)
		}
	})

	t.Run("batch with no calls", func(t *testing.T) {
		resp := frameBudgetExceededResponse([]*jsonrpcMessage{notification}, true)
		batch, ok := resp.([]*jsonrpcMessage)
		if !ok || len(batch) != 1 {
			t.Fatalf("expected a single-element batch response, got %T: %v", resp, resp)
		}
		if string(batch[0].ID) != string(null) {
			t.Fatalf("expected null id when no call is present, got %s", batch[0].ID)
		}
	})
}

// stubJSONWriter satisfies jsonWriter for handler unit tests.
type stubJSONWriter struct{}

func (stubJSONWriter) writeJSON(context.Context, interface{}, bool) error { return nil }
func (stubJSONWriter) closed() <-chan interface{}                         { return make(chan interface{}) }
func (stubJSONWriter) remoteAddr() string                                 { return "" }

func newAdmissionTestHandler(budget int64, readLimit int64, timeout time.Duration, hook func(string)) *handler {
	return newHandler(
		context.Background(),
		stubJSONWriter{},
		sequentialIDGenerator(),
		new(serviceRegistry),
		0, 0,
		semaphore.NewWeighted(budget),
		readLimit,
		hook,
		timeout,
	)
}

func TestWSAdmissionTimeoutOrDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{name: "zero uses default", timeout: 0, want: defaultWSAdmissionTimeout},
		{name: "negative uses default", timeout: -time.Second, want: defaultWSAdmissionTimeout},
		{name: "positive is kept", timeout: 5 * time.Second, want: 5 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := wsAdmissionTimeoutOrDefault(tc.timeout); got != tc.want {
				t.Fatalf("wsAdmissionTimeoutOrDefault(%v) = %v, want %v", tc.timeout, got, tc.want)
			}
		})
	}
}

func TestFireAdmissionEventOnBudgetTimeout(t *testing.T) {
	t.Parallel()

	const reason = WSAdmissionReasonPreDecodeTimeout

	t.Run("fires on deadline exceeded", func(t *testing.T) {
		var got string
		h := newAdmissionTestHandler(1, 1, time.Second, func(r string) { got = r })
		h.fireAdmissionEventOnBudgetTimeout(context.DeadlineExceeded, reason)
		if got != reason {
			t.Fatalf("hook reason = %q, want %q", got, reason)
		}
	})

	t.Run("does not fire on cancel", func(t *testing.T) {
		var hookCalled bool
		h := newAdmissionTestHandler(1, 1, time.Second, func(string) { hookCalled = true })
		h.fireAdmissionEventOnBudgetTimeout(context.Canceled, reason)
		if hookCalled {
			t.Fatal("hook should not run for context.Canceled")
		}
	})

	t.Run("does not fire without hook", func(t *testing.T) {
		h := newAdmissionTestHandler(1, 1, time.Second, nil)
		h.fireAdmissionEventOnBudgetTimeout(context.DeadlineExceeded, reason)
	})
}

func TestAcquirePreDecodeFiresHookOnTimeout(t *testing.T) {
	t.Parallel()

	const (
		frameBudget = int64(100)
		readLimit   = int64(100)
		waitTimeout = 20 * time.Millisecond
	)

	budget := semaphore.NewWeighted(frameBudget)
	if err := budget.Acquire(t.Context(), frameBudget); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { budget.Release(frameBudget) })

	reasonCh := make(chan string, 1)
	h := newAdmissionTestHandler(frameBudget, readLimit, waitTimeout, func(reason string) {
		reasonCh <- reason
	})
	h.wsConcurrentBudget = budget

	if err := h.acquirePreDecode(t.Context()); err == nil {
		t.Fatal("expected acquirePreDecode to fail when budget is exhausted")
	}

	select {
	case reason := <-reasonCh:
		if reason != WSAdmissionReasonPreDecodeTimeout {
			t.Fatalf("hook reason = %q, want %q", reason, WSAdmissionReasonPreDecodeTimeout)
		}
	case <-time.After(waitTimeout + 100*time.Millisecond):
		t.Fatal("expected admission hook to fire on pre-decode timeout")
	}
}

func TestCommitFrameBudgetFiresHookOnTimeout(t *testing.T) {
	t.Parallel()

	const (
		frameBudget = int64(100)
		readLimit   = int64(50)
		waitTimeout = 20 * time.Millisecond
	)

	budget := semaphore.NewWeighted(frameBudget)
	reasonCh := make(chan string, 1)
	h := newAdmissionTestHandler(frameBudget, readLimit, waitTimeout, func(reason string) {
		reasonCh <- reason
	})
	h.wsConcurrentBudget = budget

	if err := h.acquirePreDecode(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := budget.Acquire(t.Context(), frameBudget-readLimit); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.releasePreDecode()
		budget.Release(frameBudget - readLimit)
	})

	if _, err := h.commitFrameBudget(t.Context(), frameBudget); err == nil {
		t.Fatal("expected commitFrameBudget to fail when extra budget is unavailable")
	}

	select {
	case reason := <-reasonCh:
		if reason != WSAdmissionReasonPostDecodeTimeout {
			t.Fatalf("hook reason = %q, want %q", reason, WSAdmissionReasonPostDecodeTimeout)
		}
	case <-time.After(waitTimeout + 100*time.Millisecond):
		t.Fatal("expected admission hook to fire on post-decode timeout")
	}
}

func TestServerWSAdmissionPreDecodeTimeoutFiresHook(t *testing.T) {
	t.Parallel()

	const (
		sleepDuration = 200 * time.Millisecond
		pad           = 48
		waitTimeout   = 50 * time.Millisecond
	)

	srv := newTestServer()

	var (
		mu      sync.Mutex
		reasons []string
	)
	srv.SetWSAdmissionEventHook(func(reason string) {
		mu.Lock()
		reasons = append(reasons, reason)
		mu.Unlock()
	})
	srv.SetWSAdmissionTimeout(waitTimeout)

	p1, p2 := net.Pipe()
	makeMsg := func(id int) string {
		return fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"test_sleep","params":[%d],"_pad":"%s"}`,
			id, sleepDuration.Nanoseconds(), strings.Repeat("x", pad),
		)
	}
	payload := makeMsg(1)
	frameSize := int64(len(payload))
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(frameSize)

	serveDone := make(chan struct{})
	go func() {
		srv.ServeCodec(NewCodec(p1), 0)
		close(serveDone)
	}()
	t.Cleanup(func() {
		p2.Close()
		p1.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("ServeCodec did not exit within 2s")
		}
		srv.Stop()
	})

	writeReq := func(id int) {
		if _, err := io.WriteString(p2, makeMsg(id)); err != nil {
			t.Fatalf("write request %d: %v", id, err)
		}
	}

	// Request 1 holds the byte budget while it sleeps. The server then tries to
	// admit the next frame and should time out before we send another request.
	writeReq(1)

	deadline := time.Now().Add(waitTimeout + 300*time.Millisecond)
	for {
		mu.Lock()
		got := append([]string(nil), reasons...)
		mu.Unlock()
		if len(got) > 0 {
			if got[0] != WSAdmissionReasonPreDecodeTimeout {
				t.Fatalf("hook reason = %q, want %q", got[0], WSAdmissionReasonPreDecodeTimeout)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("expected server admission hook to fire on pre-decode timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
