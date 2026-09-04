package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// makeScalarBatch builds a compact batch array of n integer elements. Each element is a
// scalar rather than a JSON-RPC object, which is the shape that makes a small frame
// expand into one jsonrpcMessage per element.
func makeScalarBatch(n int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('1')
	}
	b.WriteByte(']')
	return b.String()
}

// makeCallBatch builds a batch of n test_echo calls, the first of which carries id 1.
// Keep n small: the body counts against defaultBodyLimit, and the server only ever
// decodes itemLimit+1 elements, so a few hundred exercises the same path as a few
// thousand.
func makeCallBatch(n int) string {
	elems := make([]string, n)
	for i := range elems {
		elems[i] = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"test_echo","params":["x",99]}`, i+1)
	}
	return "[" + strings.Join(elems, ",") + "]"
}

// makeTruncationProbe builds an oversize batch whose only call, id 99, sits just past the
// first itemLimit+1 elements. respondWithBatchTooLarge reports the first decoded call's
// id, so a null id means the decode stopped at the limit and an id of 99 means it did
// not.
func makeTruncationProbe(itemLimit int) string {
	elems := make([]string, itemLimit+1)
	for i := range elems {
		elems[i] = `{"jsonrpc":"2.0","method":"test_echo","params":["x",99]}`
	}
	elems = append(elems, `{"jsonrpc":"2.0","id":99,"method":"test_echo","params":["x",99]}`)
	return "[" + strings.Join(elems, ",") + "]"
}

// assertBatchTooLarge checks that resp is the single batch-too-large error response, and
// that it carries wantID ("null" for the id-less form).
func assertBatchTooLarge(t *testing.T, resp []jsonrpcMessage, wantID string) {
	t.Helper()

	if len(resp) != 1 {
		t.Fatalf("got %d responses, want 1", len(resp))
	}
	if resp[0].Error == nil || resp[0].Error.Message != errMsgBatchTooLarge {
		t.Fatalf("wrong response to oversize batch: %+v", resp[0])
	}
	if id := string(resp[0].ID); id != wantID {
		t.Fatalf("error id = %s, want %s", id, wantID)
	}
}

func TestParseMessageBatchItemLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		itemLimit int
		wantLen   int
		wantBatch bool
	}{
		{name: "under limit", raw: makeScalarBatch(3), itemLimit: 5, wantLen: 3, wantBatch: true},
		{name: "at limit", raw: makeScalarBatch(5), itemLimit: 5, wantLen: 5, wantBatch: true},
		{name: "one over limit", raw: makeScalarBatch(6), itemLimit: 5, wantLen: 6, wantBatch: true},
		{name: "far over limit", raw: makeScalarBatch(100000), itemLimit: 5, wantLen: 6, wantBatch: true},
		{name: "no limit", raw: makeScalarBatch(1000), itemLimit: 0, wantLen: 1000, wantBatch: true},
		{name: "empty batch", raw: "[]", itemLimit: 5, wantLen: 0, wantBatch: true},
		{name: "single message", raw: `{"jsonrpc":"2.0","id":1,"method":"test_echo"}`, itemLimit: 5, wantLen: 1, wantBatch: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			msgs, batch := parseMessage(json.RawMessage(test.raw), test.itemLimit)
			if len(msgs) != test.wantLen {
				t.Fatalf("decoded %d messages, want %d", len(msgs), test.wantLen)
			}
			if batch != test.wantBatch {
				t.Fatalf("batch = %v, want %v", batch, test.wantBatch)
			}
		})
	}
}

// TestParseMessageBatchAllocationsBounded is the regression guard for the report: a
// compact array of scalars must not allocate a jsonrpcMessage per element before the
// item limit is consulted.
// It cannot run in parallel: testing.AllocsPerRun panics in a parallel test.
func TestParseMessageBatchAllocationsBounded(t *testing.T) {
	const (
		elements  = 200000
		itemLimit = 100
	)
	raw := json.RawMessage(makeScalarBatch(elements))

	allocs := testing.AllocsPerRun(2, func() {
		parseMessage(raw, itemLimit)
	})
	// Bound generously: the point is the order of magnitude, not the exact count. Before
	// the limit was pushed into parseMessage this exceeded `elements` allocations.
	if maxAllocs := float64(10 * itemLimit); allocs > maxAllocs {
		t.Fatalf("parseMessage made %.0f allocations for a %d-element batch limited to %d, want at most %.0f",
			allocs, elements, itemLimit, maxAllocs)
	}
}

// TestWSOversizeBatchRejectedAndConnectionSurvives checks that the websocket codec reads
// with the handler's item limit, that the truncated decode still produces the
// protocol-level rejection, and that the connection remains usable after it.
func TestWSOversizeBatchRejectedAndConnectionSurvives(t *testing.T) {
	t.Parallel()

	const itemLimit = 4

	srv := newTestServer()
	srv.SetBatchLimits(itemLimit, 100000)
	_, wsURL := startWSTestServer(t, srv)

	conn := dialWS(t, wsURL)
	defer conn.Close()

	// A call just past the decoded prefix must not be found, which is only true if the
	// codec passed the item limit into parseMessage.
	writeWSJSON(t, conn, makeTruncationProbe(itemLimit))
	var probeResp []jsonrpcMessage
	readWSJSON(t, conn, &probeResp)
	assertBatchTooLarge(t, probeResp, "null")

	// A call inside the decoded prefix is still found, so the null id above is truncation
	// rather than ids going missing altogether.
	writeWSJSON(t, conn, makeCallBatch(200))
	var callResp []jsonrpcMessage
	readWSJSON(t, conn, &callResp)
	assertBatchTooLarge(t, callResp, "1")

	// A large cheap array, the shape from the report, is rejected the same way.
	writeWSJSON(t, conn, makeScalarBatch(50000))
	var scalarResp []jsonrpcMessage
	readWSJSON(t, conn, &scalarResp)
	assertBatchTooLarge(t, scalarResp, "null")

	// The connection must still serve the next request.
	writeWSJSON(t, conn, `{"jsonrpc":"2.0","id":7,"method":"test_echo","params":["x",99]}`)
	var next jsonrpcMessage
	readWSJSON(t, conn, &next)
	if next.Error != nil {
		t.Fatalf("request after oversize batch failed: %v", next.Error)
	}
}

// TestHTTPOversizeBatchRejected covers the single-request path, which builds its handler
// separately from ServeCodec and so wires the codec up on its own. The null id in the
// probe case is what proves that wiring is in place: without serveSingleRequest's
// attachHandler call the codec reads unlimited, finds the trailing call and reports its
// id.
func TestHTTPOversizeBatchRejected(t *testing.T) {
	t.Parallel()

	const itemLimit = 4

	srv := newTestServer()
	defer srv.Stop()
	srv.SetBatchLimits(itemLimit, 100000)
	httpsrv := httptest.NewServer(srv)
	defer httpsrv.Close()

	tests := []struct {
		name   string
		batch  string
		wantID string
	}{
		// A call past the decoded prefix cannot be reported.
		{name: "truncation probe", batch: makeTruncationProbe(itemLimit), wantID: "null"},
		// A call inside the prefix still is, so the null id above is truncation rather
		// than ids going missing altogether.
		{name: "leading call", batch: makeCallBatch(200), wantID: "1"},
		// The large cheap array from the report.
		{name: "scalar batch", batch: makeScalarBatch(50000), wantID: "null"},
	}
	for _, test := range tests {
		// Not parallel: the subtests must run before the deferred httpsrv.Close.
		t.Run(test.name, func(t *testing.T) {
			resp, err := http.Post(httpsrv.URL, "application/json", strings.NewReader(test.batch))
			if err != nil {
				t.Fatalf("post batch: %v", err)
			}
			defer resp.Body.Close()

			var msgs []jsonrpcMessage
			if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			assertBatchTooLarge(t, msgs, test.wantID)
		})
	}
}
