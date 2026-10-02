// This file checks the behavior of the batch item limit code.
// In tests, the batch item limit is set to 4. So to trigger the error,
// all batches in this file have 5 elements.

// For batches that do not contain any calls, a response message with "id" == null
// is returned.

--> [{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","method":"test_echo","params":["x",99]}]
<-- [{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"batch too large"}}]

// For batches with at least one call, the call's "id" is used.
--> [{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","id":3,"method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","method":"test_echo","params":["x",99]}]
<-- [{"jsonrpc":"2.0","id":3,"error":{"code":-32600,"message":"batch too large"}}]

// The batch is only decoded up to itemLimit+1 elements, so a call that appears after that
// prefix cannot contribute its "id". Here the first five elements are notifications and
// the call sits at index 5, past the decoded prefix, so the error carries a null id.
--> [{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","method":"test_echo","params":["x",99]},{"jsonrpc":"2.0","id":3,"method":"test_echo","params":["x",99]}]
<-- [{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"batch too large"}}]
