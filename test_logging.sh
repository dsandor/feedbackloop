#!/bin/bash
# Test logging output from proxy

# Start proxy in background and capture stderr
./feedbackloop 2> /tmp/feedbackloop_test.log &
PROXY_PID=$!

# Wait for startup
sleep 2

# Run test client
go run test_client.go > /dev/null 2>&1

# Give it time to finish
sleep 1

# Kill proxy
kill $PROXY_PID 2>/dev/null
wait $PROXY_PID 2>/dev/null || true

# Check logs for tool_call events
echo "=== Tool Call Request Logs ==="
cat /tmp/feedbackloop_test.log | jq -c 'select(.event_type == "tool_call_request")' 2>/dev/null | head -3

echo ""
echo "=== Tool Call Response Logs ==="
cat /tmp/feedbackloop_test.log | jq -c 'select(.event_type == "tool_call_response")' 2>/dev/null | head -3

echo ""
echo "=== Correlation ID Check ==="
# Extract first correlation ID and show matching request/response
CORR_ID=$(cat /tmp/feedbackloop_test.log | jq -r 'select(.event_type == "tool_call_request") | .correlation_id' 2>/dev/null | head -1)
if [ -n "$CORR_ID" ]; then
    echo "Checking correlation ID: $CORR_ID"
    cat /tmp/feedbackloop_test.log | jq -c "select(.correlation_id == \"$CORR_ID\")" 2>/dev/null
else
    echo "No correlation ID found"
fi

# Cleanup
rm -f /tmp/feedbackloop_test.log
