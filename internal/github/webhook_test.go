package github

import "testing"

func TestVerify(t *testing.T) {
	body := []byte("Hello, World!")
	header := "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if !Verify("It's a Secret to Everybody", body, header) {
		t.Fatal("official GitHub fixture did not verify")
	}
	if Verify("wrong", body, header) {
		t.Fatal("wrong secret verified")
	}
}
