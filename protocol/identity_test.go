package protocol

import (
	"encoding/hex"
	"regexp"
	"testing"
)

// The four bytes a session frame carries are an Adler-32 of its body, not a
// per-client secret. These pairs are reference session-start frames.
//
// This is what makes a generated identity work: the device is checking a
// checksum it can recompute, not looking a client up.
func TestSessionChecksumMatchesGolden(t *testing.T) {
	golden := map[string]string{
		"14e67f11-71f8-450d-b998-9a8e96eaac69": "fe5b0b01",
		"18f69aba-9053-469b-a786-1a0753e05bfd": "00ad0ae9",
		"396f4bd8-2e11-417f-bbd0-0b4939a4eaed": "0a0d0b70",
		"543794b7-9f04-4d1f-9fb2-4158b4dfe25c": "fbba0af3",
		"66c5cc52-1eef-43b9-8f4a-10a2adf13fcb": "151a0bc9",
		"a39825b9-d348-44b2-8910-340f65f4e324": "f1480a2c",
		"a3d788e1-9849-49e1-b9f7-d7bc299c9a9e": "07450b41",
		"bbc23918-f94e-4533-8bae-62ca7a2b855c": "0c630b46",
		"bd838981-a7b3-40c2-b884-dbcbc6056aec": "0af20b73",
		"c1dc70b9-d05f-4dab-b7b7-b2070e9f9015": "18d20b6b",
	}
	for id, want := range golden {
		got := SessionChecksum(id, DefaultClientName)
		if hex.EncodeToString(got[:]) != want {
			t.Errorf("%s: checksum %x, reference has %s", id, got, want)
		}
	}
}

// A wrong checksum is what the device refuses.
func TestSessionChecksumDependsOnTheClientID(t *testing.T) {
	a := SessionChecksum("396f4bd8-2e11-417f-bbd0-0b4939a4eaed", DefaultClientName)
	b := SessionChecksum("bd838981-a7b3-40c2-b884-dbcbc6056aec", DefaultClientName)
	if a == b {
		t.Fatal("two client ids produced the same checksum")
	}
	if n := SessionChecksum("396f4bd8-2e11-417f-bbd0-0b4939a4eaed", "OTHER"); n == a {
		t.Error("the name does not affect the checksum, but it is part of the body")
	}
}

// An empty name means the default, and the checksum has to agree with what the
// encoder actually writes or the device sees a mismatch.
func TestSessionChecksumDefaultsTheName(t *testing.T) {
	id := "396f4bd8-2e11-417f-bbd0-0b4939a4eaed"
	if SessionChecksum(id, "") != SessionChecksum(id, DefaultClientName) {
		t.Error("an empty name checksums differently from the default it becomes")
	}
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewIdentityIsUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := NewIdentity()
		if !uuidV4.MatchString(id.ClientID) {
			t.Fatalf("generated %q, which is not a version 4 UUID", id.ClientID)
		}
		if len(id.ClientID) != 36 {
			t.Fatalf("client id is %d characters; the frame declares 36", len(id.ClientID))
		}
		if seen[id.ClientID] {
			t.Fatal("generated a duplicate; two programs would displace each other")
		}
		seen[id.ClientID] = true
	}
}

// The name is part of the checksummed body: changing the name while keeping a
// fixed tag breaks the checksum every time. With a correct checksum the device
// accepts any name.
func TestNameIsCheckummedNotFixed(t *testing.T) {
	id := "396f4bd8-2e11-417f-bbd0-0b4939a4eaed"
	base := SessionChecksum(id, DefaultClientName)
	for _, name := range []string{"NE2212", "ABC123", "NE221", "KITCHEN"} {
		if SessionChecksum(id, name) == base {
			t.Errorf("name %q checksums the same as %q; the name is in the body and must change it",
				name, DefaultClientName)
		}
	}
	// Short and long names both have to encode, since the length is a byte.
	if got := SessionChecksum(id, "A"); got == base {
		t.Error("a one-character name checksums like the default")
	}
}
