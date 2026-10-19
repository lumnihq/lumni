package worker

import "testing"

func TestWebhookSignature(t *testing.T) {
	got := signature([]byte("It's a Secret to Everybody"), []byte("Hello, World!"))
	want := "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if got != want {
		t.Fatalf("signature=%s", got)
	}
}
