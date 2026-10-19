package secrets

import "testing"

func TestRoundTrip(t *testing.T) {
	manager, err := New("test key")
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := manager.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == "secret" {
		t.Fatal("secret was not encrypted")
	}
	plain, err := manager.Decrypt(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "secret" {
		t.Fatalf("got %q", plain)
	}
}
