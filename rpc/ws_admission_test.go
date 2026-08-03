package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sync/semaphore"
)

func wsTestURL(httpsrv *httptest.Server) string {
	return "ws:" + strings.TrimPrefix(httpsrv.URL, "http:")
}

func startWSTestServer(t *testing.T, srv *Server) (*httptest.Server, string) {
	t.Helper()
	httpsrv := httptest.NewServer(srv.WebsocketHandler([]string{"*"}))
	t.Cleanup(func() {
		httpsrv.Close()
		srv.Stop()
	})
	return httpsrv, wsTestURL(httpsrv)
}

func dialWS(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	return conn
}

func writeWSJSON(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatalf("write websocket message: %v", err)
	}
}

func readWSJSON(t *testing.T, conn *websocket.Conn, v interface{}) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket message: %v", err)
	}
	conn.SetReadDeadline(time.Time{})
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal websocket message: %v", err)
	}
}

func makeSleepMsg(id int, sleep time.Duration, pad int) string {
	return fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%d,"method":"test_sleep","params":[%d],"_pad":"%s"}`,
		id, sleep.Nanoseconds(), strings.Repeat("x", pad),
	)
}

func TestWSIdleConnectionsDoNotHoldBudget(t *testing.T) {
	t.Parallel()

	const (
		frameSize = 500
		budget    = 1000
		idleConns = 15
	)

	srv := newTestServer()
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(budget)
	_, wsURL := startWSTestServer(t, srv)

	conns := make([]*websocket.Conn, idleConns)
	for i := range conns {
		conns[i] = dialWS(t, wsURL)
	}
	t.Cleanup(func() {
		for _, c := range conns {
			c.Close()
		}
	})

	deadline := time.Now().Add(time.Second)
	for {
		if srv.wsConcurrentBudget.TryAcquire(budget) {
			srv.wsConcurrentBudget.Release(budget)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("idle websocket connections should not hold byte budget")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWSManyIdleSubscriptions(t *testing.T) {
	t.Parallel()

	const (
		frameSize = 500
		budget    = 1000
		numConns  = 20
	)

	srv := newTestServer()
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(budget)
	_, wsURL := startWSTestServer(t, srv)

	subReq := `{"jsonrpc":"2.0","id":1,"method":"nftest_subscribe","params":["someSubscription",1,0]}`
	conns := make([]*websocket.Conn, numConns)
	for i := range conns {
		conn := dialWS(t, wsURL)
		writeWSJSON(t, conn, subReq)
		var subResp jsonrpcMessage
		readWSJSON(t, conn, &subResp)
		if subResp.Error != nil {
			t.Fatalf("subscribe failed: %v", subResp.Error)
		}
		conns[i] = conn
	}
	t.Cleanup(func() {
		for _, c := range conns {
			c.Close()
		}
	})

	// No settling delay needed: each subscribe ack was already read above.
	extra := dialWS(t, wsURL)
	writeWSJSON(t, extra, makeSleepMsg(99, 10*time.Millisecond, 48))
	var resp jsonrpcMessage
	readWSJSON(t, extra, &resp)
	if resp.Error != nil {
		t.Fatalf("extra connection request failed: %v", resp.Error)
	}
}

func TestWSBudgetAcquiredOnlyOnFrame(t *testing.T) {
	t.Parallel()

	const (
		sleepDuration = 200 * time.Millisecond
		pad           = 48
	)

	srv := newTestServer()
	payload := makeSleepMsg(1, sleepDuration, pad)
	frameSize := int64(len(payload))
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(frameSize)
	_, wsURL := startWSTestServer(t, srv)

	conn := dialWS(t, wsURL)
	writeWSJSON(t, conn, payload)

	held := false
	deadline := time.Now().Add(sleepDuration)
	for time.Now().Before(deadline) {
		if !srv.wsConcurrentBudget.TryAcquire(frameSize) {
			held = true
			break
		}
		srv.wsConcurrentBudget.Release(frameSize)
		time.Sleep(5 * time.Millisecond)
	}
	if !held {
		t.Fatal("byte budget should be held while the first frame is in flight")
	}

	var resp jsonrpcMessage
	readWSJSON(t, conn, &resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	time.Sleep(50 * time.Millisecond)
	if !srv.wsConcurrentBudget.TryAcquire(frameSize) {
		t.Fatal("byte budget should be fully available while blocked waiting for the next frame")
	}
	srv.wsConcurrentBudget.Release(frameSize)
}

// TestWSPartialFrameStallReleasesBudget verifies that a peer which sends only the
// first fragment of a message and then stalls cannot hold pre-decode budget
// indefinitely: once wsAdmissionTimeout elapses since the first frame arrived, the
// server must abandon the read and release the reservation, even though the
// connection is otherwise alive.
func TestWSPartialFrameStallReleasesBudget(t *testing.T) {
	t.Parallel()

	const (
		waitTimeout = 50 * time.Millisecond
		readLimit   = 256
		budget      = 256
	)

	srv := newTestServer()
	srv.SetWSAdmissionTimeout(waitTimeout)
	srv.SetReadLimits(readLimit)
	srv.SetWSConcurrentRequestBytes(budget)
	_, wsURL := startWSTestServer(t, srv)

	// A small write buffer forces the partial payload onto the wire immediately;
	// with the default dialer buffer the bytes would never leave the client.
	dialer := &websocket.Dialer{WriteBufferSize: 1}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	w, err := conn.NextWriter(websocket.TextMessage)
	if err != nil {
		t.Fatalf("next writer: %v", err)
	}
	if _, err := w.Write([]byte(`{"jsonrpc":"2.0",`)); err != nil {
		t.Fatalf("write partial frame: %v", err)
	}
	// Deliberately never call w.Close() or send the rest of the message: the
	// server has read the first fragment (NextReader returned) and reserved
	// budget for it, but the message never finishes arriving.

	budgetHeld := false
	holdDeadline := time.Now().Add(time.Second)
	for time.Now().Before(holdDeadline) {
		if !srv.wsConcurrentBudget.TryAcquire(budget) {
			budgetHeld = true
			break
		}
		srv.wsConcurrentBudget.Release(budget)
		time.Sleep(5 * time.Millisecond)
	}
	if !budgetHeld {
		t.Fatal("budget should be held while partial frame is stalled")
	}

	releaseDeadline := time.Now().Add(waitTimeout + time.Second)
	for {
		if srv.wsConcurrentBudget.TryAcquire(budget) {
			srv.wsConcurrentBudget.Release(budget)
			return
		}
		if time.Now().After(releaseDeadline) {
			t.Fatal("budget reserved for a stalled partial frame should be released after wsAdmissionTimeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWSLargeFrameRejectedByReadLimit(t *testing.T) {
	t.Parallel()

	const (
		readLimit = 256
		budget    = 1024
	)

	srv := newTestServer()
	srv.SetReadLimits(readLimit)
	srv.SetWSConcurrentRequestBytes(budget)
	_, wsURL := startWSTestServer(t, srv)

	conn := dialWS(t, wsURL)
	oversized := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"test_echo","params":["%s"]}`,
		strings.Repeat("x", readLimit),
	)
	writeWSJSON(t, conn, oversized)

	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected connection to close after oversized frame")
	}

	if !srv.wsConcurrentBudget.TryAcquire(budget) {
		t.Fatal("byte budget should be unchanged after read-limit rejection")
	}
	srv.wsConcurrentBudget.Release(budget)
}

func TestWSOversizeFrameFiresAdmissionHook(t *testing.T) {
	t.Parallel()

	const (
		readLimit = 256
		budget    = 1024
	)

	var (
		mu      sync.Mutex
		reasons []string
	)
	srv := newTestServer()
	srv.SetWSAdmissionEventHook(func(reason string) {
		mu.Lock()
		reasons = append(reasons, reason)
		mu.Unlock()
	})
	srv.SetReadLimits(readLimit)
	srv.SetWSConcurrentRequestBytes(budget)
	_, wsURL := startWSTestServer(t, srv)

	conn := dialWS(t, wsURL)
	oversized := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"test_echo","params":["%s"]}`,
		strings.Repeat("x", readLimit),
	)
	writeWSJSON(t, conn, oversized)

	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected connection to close after oversized frame")
	}

	// Poll: gorilla's close(1009) can reach the client before the hook fires.
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		got := append([]string(nil), reasons...)
		mu.Unlock()
		if len(got) > 0 {
			if len(got) != 1 || got[0] != WSAdmissionReasonOversizeFrame {
				t.Fatalf("admission hook reasons = %v, want [%q]", got, WSAdmissionReasonOversizeFrame)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("expected oversize-frame admission hook to fire")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWSFragmentedOversizeFrameFiresAdmissionHook covers a message that only crosses
// gorilla's read limit partway through a run of continuation frames.
func TestWSFragmentedOversizeFrameFiresAdmissionHook(t *testing.T) {
	t.Parallel()

	const (
		readLimit = 200
		budget    = 1024
		padLen    = 2000
	)

	var (
		mu      sync.Mutex
		reasons []string
	)
	srv := newTestServer()
	srv.SetWSAdmissionEventHook(func(reason string) {
		mu.Lock()
		reasons = append(reasons, reason)
		mu.Unlock()
	})
	srv.SetReadLimits(readLimit)
	srv.SetWSConcurrentRequestBytes(budget)
	_, wsURL := startWSTestServer(t, srv)

	// A small write buffer forces gorilla to split a single large Write into many
	// continuation frames, each well under readLimit on its own.
	dialer := &websocket.Dialer{WriteBufferSize: 16}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	w, err := conn.NextWriter(websocket.TextMessage)
	if err != nil {
		t.Fatalf("next writer: %v", err)
	}
	oversized := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"test_echo","params":["%s"]}`,
		strings.Repeat("x", padLen),
	)
	// The server closes the connection as soon as it detects the read-limit
	// violation, which can happen before the client finishes writing every
	// continuation frame — so a write/close error here is expected, not fatal.
	_, _ = w.Write([]byte(oversized))
	_ = w.Close()

	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected connection to close after oversized fragmented message")
	}

	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		got := append([]string(nil), reasons...)
		mu.Unlock()
		if len(got) > 0 {
			if len(got) != 1 || got[0] != WSAdmissionReasonOversizeFrame {
				t.Fatalf("admission hook reasons = %v, want [%q]", got, WSAdmissionReasonOversizeFrame)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("expected oversize-frame admission hook to fire for a fragmented message")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWSConcurrentLargeFrames(t *testing.T) {
	t.Parallel()

	const (
		sleepDuration = 200 * time.Millisecond
		pad           = 48
	)

	srv := newTestServer()
	payload := makeSleepMsg(1, sleepDuration, pad)
	frameSize := int64(len(payload))
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(2 * frameSize)

	_, wsURL := startWSTestServer(t, srv)
	conn := dialWS(t, wsURL)
	t.Cleanup(func() { conn.Close() })

	writeWSJSON(t, conn, makeSleepMsg(1, sleepDuration, pad))
	writeWSJSON(t, conn, makeSleepMsg(2, sleepDuration, pad))

	// With budget == frameSize the two requests would serialize; with 2*frameSize
	// both should hold budget while test_sleep runs.
	overlapDeadline := time.Now().Add(sleepDuration)
	var overlapped bool
	for time.Now().Before(overlapDeadline) {
		if !srv.wsConcurrentBudget.TryAcquire(frameSize) {
			overlapped = true
			break
		}
		srv.wsConcurrentBudget.Release(frameSize)
		time.Sleep(5 * time.Millisecond)
	}
	if !overlapped {
		t.Fatal("expected two large frames to hold byte budget concurrently")
	}

	var firstResp jsonrpcMessage
	readWSJSON(t, conn, &firstResp)
	if string(firstResp.ID) != "1" {
		t.Fatalf("expected first response id 1, got %s", string(firstResp.ID))
	}

	var secondResp jsonrpcMessage
	readWSJSON(t, conn, &secondResp)
	if string(secondResp.ID) != "2" {
		t.Fatalf("expected second response id 2, got %s", string(secondResp.ID))
	}
}

func TestWSBudgetWaitTimeoutOnActiveBurst(t *testing.T) {
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

	payload := makeSleepMsg(1, sleepDuration, pad)
	frameSize := int64(len(payload))
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(frameSize)
	_, wsURL := startWSTestServer(t, srv)

	conn := dialWS(t, wsURL)
	t.Cleanup(func() { conn.Close() })
	writeWSJSON(t, conn, makeSleepMsg(1, sleepDuration, pad))
	writeWSJSON(t, conn, makeSleepMsg(2, sleepDuration, pad))

	deadline := time.Now().Add(waitTimeout + 300*time.Millisecond)
	for {
		mu.Lock()
		got := append([]string(nil), reasons...)
		mu.Unlock()
		if len(got) > 0 {
			if got[0] != WSAdmissionReasonBudgetWaitTimeout {
				t.Fatalf("hook reason = %q, want %q", got[0], WSAdmissionReasonBudgetWaitTimeout)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected admission hook to fire when a second frame waits on budget")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Confirm the peer actually receives the -32005 error, not just the hook firing.
	conn.SetReadDeadline(time.Now().Add(waitTimeout + 2*sleepDuration))
	foundBudgetTimeoutResp := false
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var resp jsonrpcMessage
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}
		if resp.Error != nil && resp.Error.Code == errcodeBudgetWaitTimeout {
			foundBudgetTimeoutResp = true
			break
		}
	}
	if !foundBudgetTimeoutResp {
		t.Fatalf("expected peer to receive a JSON-RPC error with code %d before the connection closed", errcodeBudgetWaitTimeout)
	}
}

// passthroughCodec embeds ServerCodec without implementing budgetHandlerSetter, so
// attachBudgetHandler's type assertion fails for it — like any decorator that forwards
// reads but not setBudgetHandler.
type passthroughCodec struct {
	ServerCodec
}

// TestBudgetCommitWithoutHandlerWiringDoesNotPanic guards against a regression where
// commitFrameBudget released budget that was never acquired for an unwired codec,
// panicking the semaphore on the first request.
func TestBudgetCommitWithoutHandlerWiringDoesNotPanic(t *testing.T) {
	t.Parallel()

	const (
		readLimit = 256
		budget    = 1024
	)

	srv := newTestServer()
	srv.SetReadLimits(readLimit)
	srv.SetWSConcurrentRequestBytes(budget)

	p1, p2 := net.Pipe()
	wrapped := &passthroughCodec{ServerCodec: NewCodec(p1)}
	go srv.ServeCodec(wrapped, 0)
	t.Cleanup(func() { p2.Close(); p1.Close(); srv.Stop() })

	payload := `{"jsonrpc":"2.0","id":1,"method":"test_echo","params":["hello",1,{"S":"world"}]}`
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

	const reason = WSAdmissionReasonBudgetWaitTimeout

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

func TestAcquirePreDecodeFiresBudgetWaitHookOnTimeout(t *testing.T) {
	t.Parallel()

	const (
		frameBudget = int64(100)
		readLimit   = int64(100)
		waitTimeout = 20 * time.Millisecond
	)

	budget := semaphore.NewWeighted(frameBudget)
	if err := budget.Acquire(context.Background(), frameBudget); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { budget.Release(frameBudget) })

	reasonCh := make(chan string, 1)
	h := newAdmissionTestHandler(frameBudget, readLimit, waitTimeout, func(reason string) {
		reasonCh <- reason
	})
	h.wsConcurrentBudget = budget

	if err := h.acquirePreDecode(context.Background()); err == nil {
		t.Fatal("expected acquirePreDecode to fail when budget is exhausted")
	}

	select {
	case reason := <-reasonCh:
		if reason != WSAdmissionReasonBudgetWaitTimeout {
			t.Fatalf("hook reason = %q, want %q", reason, WSAdmissionReasonBudgetWaitTimeout)
		}
	case <-time.After(waitTimeout + 100*time.Millisecond):
		t.Fatal("expected admission hook to fire on budget wait timeout")
	}
}

func TestCommitFrameBudgetFiresFrameAdmissionHookOnTimeout(t *testing.T) {
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

	if err := h.acquirePreDecode(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := budget.Acquire(context.Background(), frameBudget-readLimit); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.releasePreDecode()
		budget.Release(frameBudget - readLimit)
	})

	if _, err := h.commitFrameBudget(context.Background(), frameBudget); err == nil {
		t.Fatal("expected commitFrameBudget to fail when extra budget is unavailable")
	}

	select {
	case reason := <-reasonCh:
		if reason != WSAdmissionReasonFrameAdmissionTimeout {
			t.Fatalf("hook reason = %q, want %q", reason, WSAdmissionReasonFrameAdmissionTimeout)
		}
	case <-time.After(waitTimeout + 100*time.Millisecond):
		t.Fatal("expected admission hook to fire on frame admission timeout")
	}
}

// TestCommitFrameBudgetFailureReleasesPreDecodeBudget guards against a regression
// where commitFrameBudget cleared preDecodeHeld before a failable Acquire for
// weight > readLimit, making the caller's releasePreDecode a no-op and permanently
// leaking readLimit bytes from the server-wide semaphore.
func TestCommitFrameBudgetFailureReleasesPreDecodeBudget(t *testing.T) {
	t.Parallel()

	const (
		frameBudget = int64(100)
		readLimit   = int64(50)
		waitTimeout = 20 * time.Millisecond
	)

	budget := semaphore.NewWeighted(frameBudget)
	h := newAdmissionTestHandler(frameBudget, readLimit, waitTimeout, nil)
	h.wsConcurrentBudget = budget

	if err := h.acquirePreDecode(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Leave no room for the extra Acquire inside commitFrameBudget.
	if err := budget.Acquire(context.Background(), frameBudget-readLimit); err != nil {
		t.Fatal(err)
	}

	if _, err := h.commitFrameBudget(context.Background(), frameBudget); err == nil {
		t.Fatal("expected commitFrameBudget to fail when extra budget is unavailable")
	}

	// Mirror Client.read's error path.
	h.releasePreDecode()
	budget.Release(frameBudget - readLimit)

	if !budget.TryAcquire(frameBudget) {
		t.Fatal("pre-decode reservation should be released after failed commitFrameBudget")
	}
	budget.Release(frameBudget)
}

func TestServerWSAdmissionBudgetWaitTimeoutFiresHook(t *testing.T) {
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

	payload := makeSleepMsg(1, sleepDuration, pad)
	frameSize := int64(len(payload))
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(frameSize)
	_, wsURL := startWSTestServer(t, srv)

	conn := dialWS(t, wsURL)
	t.Cleanup(func() { conn.Close() })
	writeWSJSON(t, conn, payload)

	var firstResp jsonrpcMessage
	readWSJSON(t, conn, &firstResp)
	if firstResp.Error != nil {
		t.Fatalf("unexpected error: %v", firstResp.Error)
	}

	// After the first request completes the read loop blocks in NextReader without
	// holding budget. The hook must not fire while idle.
	idleDeadline := time.Now().Add(waitTimeout + 200*time.Millisecond)
	for time.Now().Before(idleDeadline) {
		mu.Lock()
		got := append([]string(nil), reasons...)
		mu.Unlock()
		if len(got) > 0 {
			t.Fatalf("hook fired while idle in NextReader: %v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Hold budget again and send a second frame while it is exhausted.
	writeWSJSON(t, conn, makeSleepMsg(2, sleepDuration, pad))
	writeWSJSON(t, conn, makeSleepMsg(3, sleepDuration, pad))

	deadline := time.Now().Add(waitTimeout + 300*time.Millisecond)
	for {
		mu.Lock()
		got := append([]string(nil), reasons...)
		mu.Unlock()
		if len(got) > 0 {
			if got[0] != WSAdmissionReasonBudgetWaitTimeout {
				t.Fatalf("hook reason = %q, want %q", got[0], WSAdmissionReasonBudgetWaitTimeout)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("expected server admission hook to fire on budget wait timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
