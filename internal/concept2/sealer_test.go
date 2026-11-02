package concept2_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/softsrv/starter/internal/concept2"
)

func TestSealer_RoundTrip(t *testing.T) {
	secret := []byte("very-secure-jwt-secret-at-least-32-bytes")
	sealer := concept2.NewSealer(secret)
	userID := uuid.MustParse("01912345-6789-7abc-def0-123456789abc")
	plaintext := []byte("secret-concept2-access-token-1234567890")

	sealed, err := sealer.Seal(plaintext, userID)
	if err != nil {
		t.Fatalf("Seal: unexpected error: %v", err)
	}

	opened, err := sealer.Open(sealed, userID)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}

	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open returned %q, want %q", opened, plaintext)
	}
}

func TestSealer_TwoSealsOfSamePlaintextDiffer(t *testing.T) {
	secret := []byte("very-secure-jwt-secret-at-least-32-bytes")
	sealer := concept2.NewSealer(secret)
	userID := uuid.New()
	plaintext := []byte("access-token-constant-payload")

	seal1, err := sealer.Seal(plaintext, userID)
	if err != nil {
		t.Fatalf("Seal 1: %v", err)
	}
	seal2, err := sealer.Seal(plaintext, userID)
	if err != nil {
		t.Fatalf("Seal 2: %v", err)
	}

	if bytes.Equal(seal1, seal2) {
		t.Fatal("two Seals of the same plaintext must differ due to random nonce")
	}

	// Verify the 12-byte nonce prefixes differ
	if bytes.Equal(seal1[:12], seal2[:12]) {
		t.Fatal("nonce prefixes must differ between two Seals")
	}
}

func TestSealer_PlaintextNotSubstringOfCiphertext(t *testing.T) {
	secret := []byte("very-secure-jwt-secret-at-least-32-bytes")
	sealer := concept2.NewSealer(secret)
	userID := uuid.New()
	plaintext := []byte("plaintext-token-that-should-never-leak-in-ciphertext")

	sealed, err := sealer.Seal(plaintext, userID)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if bytes.Contains(sealed, plaintext) {
		t.Fatalf("sealed ciphertext must not contain plaintext %q", plaintext)
	}
}

func TestSealer_DifferentSecretYieldsErrDecrypt(t *testing.T) {
	secretA := []byte("jwt-secret-version-a-at-least-32-bytes!!")
	secretB := []byte("jwt-secret-version-b-at-least-32-bytes!!")
	sealerA := concept2.NewSealer(secretA)
	sealerB := concept2.NewSealer(secretB)
	userID := uuid.New()
	plaintext := []byte("sensitive-refresh-token")

	sealed, err := sealerA.Seal(plaintext, userID)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err = sealerB.Open(sealed, userID)
	if !errors.Is(err, concept2.ErrDecrypt) {
		t.Fatalf("Open with different secret: got %v, want %v", err, concept2.ErrDecrypt)
	}
}

func TestSealer_TamperedAndShortBlobsYieldErrDecrypt(t *testing.T) {
	secret := []byte("very-secure-jwt-secret-at-least-32-bytes")
	sealer := concept2.NewSealer(secret)
	userID := uuid.New()
	plaintext := []byte("token-for-tamper-testing")

	sealed, err := sealer.Seal(plaintext, userID)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Short blobs (less than nonce length + GCM overhead = 12 + 16 = 28 bytes)
	for length := 0; length < 28; length++ {
		truncated := sealed[:length]
		_, err := sealer.Open(truncated, userID)
		if !errors.Is(err, concept2.ErrDecrypt) {
			t.Errorf("Open truncated blob len %d: got %v, want %v", length, err, concept2.ErrDecrypt)
		}
	}

	// Tampered blob: flip every single byte one by one
	for i := range sealed {
		tampered := bytes.Clone(sealed)
		tampered[i] ^= 0x5a
		_, err := sealer.Open(tampered, userID)
		if !errors.Is(err, concept2.ErrDecrypt) {
			t.Errorf("Open with byte %d tampered: got %v, want %v", i, err, concept2.ErrDecrypt)
		}
	}
}

func TestSealer_UserIsolation_WrongUserYieldsErrDecrypt(t *testing.T) {
	secret := []byte("very-secure-jwt-secret-at-least-32-bytes")
	sealer := concept2.NewSealer(secret)
	userA := uuid.MustParse("01912345-6789-7abc-def0-12345678900a")
	userB := uuid.MustParse("01912345-6789-7abc-def0-12345678900b")
	plaintext := []byte("token-owned-by-user-a")

	sealedForA, err := sealer.Seal(plaintext, userA)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Attempting to open user A's token as user B must fail with ErrDecrypt
	_, err = sealer.Open(sealedForA, userB)
	if !errors.Is(err, concept2.ErrDecrypt) {
		t.Fatalf("Open with user B: got %v, want %v", err, concept2.ErrDecrypt)
	}
}

func TestSealer_EmptyPlaintextRoundTrips(t *testing.T) {
	secret := []byte("very-secure-jwt-secret-at-least-32-bytes")
	sealer := concept2.NewSealer(secret)
	userID := uuid.New()

	for _, tc := range []struct {
		name      string
		plaintext []byte
	}{
		{name: "empty slice", plaintext: []byte{}},
		{name: "nil slice", plaintext: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sealed, err := sealer.Seal(tc.plaintext, userID)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if len(sealed) != 12+16 { // 12-byte nonce + 16-byte tag
				t.Fatalf("len(sealed) = %d, want %d", len(sealed), 12+16)
			}
			opened, err := sealer.Open(sealed, userID)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if len(opened) != 0 {
				t.Fatalf("opened len = %d, want 0", len(opened))
			}
		})
	}
}

func TestSealer_NoTokenOrKeyInError(t *testing.T) {
	secretStr := "jwt-secret-super-sensitive-string-32B"
	tokenStr := "c2-access-token-super-sensitive-token"
	sealer := concept2.NewSealer([]byte(secretStr))
	userID := uuid.New()

	sealed, err := sealer.Seal([]byte(tokenStr), userID)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0x01

	_, err = sealer.Open(tampered, userID)
	if err == nil {
		t.Fatal("Open expected error, got nil")
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, secretStr) {
		t.Fatalf("error message %q leaks secret string", errMsg)
	}
	if strings.Contains(errMsg, tokenStr) {
		t.Fatalf("error message %q leaks token string", errMsg)
	}
}
