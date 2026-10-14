package stdio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestFrameByteBoundaryIncludesDelimiter(t *testing.T) {
	for _, size := range []int{MaxFrameBytes - 1, MaxFrameBytes, MaxFrameBytes + 1} {
		for _, delimiter := range []string{"\n", "\r\n", ""} {
			prefix := `{"v":1,"id":"boundary","type":"command","method":"echo","payload":"`
			suffix := `"}`
			// UTF-8 bytes, not rune count, own the transport boundary.
			padding := size - len(prefix) - len(suffix)
			frame := prefix + strings.Repeat("é", padding/2) + strings.Repeat("x", padding%2) + suffix
			if len(frame) != size {
				t.Fatal("fixture has wrong byte size")
			}
			t.Run(fmt.Sprintf("bytes=%d/delimiter=%q", size, delimiter), func(t *testing.T) {
				var output strings.Builder
				calls := 0
				server := NewServer(strings.NewReader(frame+delimiter), &output, 1)
				server.Handle("echo", func(context.Context, json.RawMessage) (any, error) {
					calls++
					return "accepted", nil
				})
				if err := server.Serve(context.Background()); err != nil {
					t.Fatalf("bounded frame killed scanner: %v", err)
				}
				var response Response
				if err := json.Unmarshal([]byte(output.String()), &response); err != nil {
					t.Fatal(err)
				}
				if size <= MaxFrameBytes {
					if calls != 1 || !response.OK {
						t.Fatalf("valid maximum refused: calls=%d response=%+v", calls, response)
					}
				} else if calls != 0 || response.OK || response.Error == nil || response.Error.Code != "invalid_request" {
					t.Fatalf("oversized frame reached handler: calls=%d response=%+v", calls, response)
				}
			})
		}
	}
}

func TestFarOversizedFrameEndsWithControlledReadError(t *testing.T) {
	server := NewServer(strings.NewReader(strings.Repeat("x", 2*MaxFrameBytes)+"\n"), &strings.Builder{}, 1)
	if err := server.Serve(context.Background()); err == nil || !strings.Contains(err.Error(), "read stdio frame") {
		t.Fatalf("oversized stream has no controlled terminal error: %v", err)
	}
}
