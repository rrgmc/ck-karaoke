package protocol

import "testing"

// A ping payload as a VSK 5.0 unit sends it.
const samplePing = "pxP24QU5JH0tCuLmHlHHaGiHe4qPS2UneDQBSJUW2p6z5Ja3sZUMeIENIwnAsH/rwZhw4kXI\n" +
	"xc1MWBPHOCPRNA==\n"

func TestDecodeDeviceInfo(t *testing.T) {
	info, err := DecodeDeviceInfo([]byte(samplePing))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.Product != "RafKara" {
		t.Errorf("Product = %q, want %q", info.Product, "RafKara")
	}
	if info.Model != "MK8513-BR-B" {
		t.Errorf("Model = %q, want %q", info.Model, "MK8513-BR-B")
	}
	// The catalogue identifier the status frames and the download path also use.
	if info.DatabaseID != DatabaseID {
		t.Errorf("DatabaseID = %q, want %q", info.DatabaseID, DatabaseID)
	}
	t.Logf("%s", info)
	t.Logf("all fields: %q", info.Fields)
}

func TestDecodeDeviceInfoRejectsRubbish(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{"not base64", "!!!not base64!!!"},
		{"empty", ""},
		{"wrong block size", "YWJj"}, // 3 bytes
	} {
		if _, err := DecodeDeviceInfo([]byte(tc.payload)); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}
