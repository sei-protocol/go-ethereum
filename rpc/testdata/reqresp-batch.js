// There is no response for all-notification batches.

--> [{"jsonrpc":"2.0","method":"test_echo","params":["x",99]}]

// This test checks regular batch calls.

--> [{"jsonrpc":"2.0","id":2,"method":"test_echo","params":[]}, {"jsonrpc":"2.0","id": 3,"method":"test_echo","params":["x",3]}]
<-- [{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"missing value for required argument 0"}},{"jsonrpc":"2.0","id":3,"result":{"String":"x","Int":3,"Args":null}}]

// This test checks a batch of exactly the item limit (4 in tests), which must be served
// rather than rejected. It pins handleBatch's strict "len(msgs) > limit" comparison,
// which parseMessage's decode-one-past-the-limit behavior depends on.

--> [{"jsonrpc":"2.0","id":4,"method":"test_echo","params":["x",4]}, {"jsonrpc":"2.0","id":5,"method":"test_echo","params":["x",5]}, {"jsonrpc":"2.0","id":6,"method":"test_echo","params":["x",6]}, {"jsonrpc":"2.0","id":7,"method":"test_echo","params":["x",7]}]
<-- [{"jsonrpc":"2.0","id":4,"result":{"String":"x","Int":4,"Args":null}},{"jsonrpc":"2.0","id":5,"result":{"String":"x","Int":5,"Args":null}},{"jsonrpc":"2.0","id":6,"result":{"String":"x","Int":6,"Args":null}},{"jsonrpc":"2.0","id":7,"result":{"String":"x","Int":7,"Args":null}}]
