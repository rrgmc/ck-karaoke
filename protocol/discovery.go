package protocol

import (
	"crypto/cipher"
	"crypto/des"
	"encoding/base64"
	"fmt"
	"strings"
)

// The offline ping payload is base64 of DES/CBC/NoPadding ciphertext with a
// fixed key and IV. NoPadding is why the payload is always a whole number of
// 8-byte blocks.
var (
	discoveryKey = []byte{0x8F, 0x66, 0x0F, 0x72, 0xD0, 0xD4, 0xD0, 0x8F}
	discoveryIV  = []byte{0x7C, 0xBA, 0x70, 0xF5, 0x77, 0x4E, 0x49, 0xF8}
)

// DeviceInfo is what a device says about itself in its ping reply.
type DeviceInfo struct {
	// Product is the product family, such as "RafKara".
	Product string
	// Model is the hardware model, such as "MK8513-BR-B".
	Model string
	// DatabaseID identifies the song catalogue. It matches status field 13 and
	// the directory in DefaultDatabasePath, so a downloaded catalogue can be
	// checked against the device that is actually in front of you.
	DatabaseID string
	// Fields is every slash-separated field, including the ones with no known
	// meaning.
	Fields []string
}

func (d DeviceInfo) String() string {
	s := d.Product
	if d.Model != "" {
		s += " " + d.Model
	}
	if d.DatabaseID != "" {
		s += " db=" + d.DatabaseID
	}
	return s
}

// Discovery payload field positions. A typical payload:
//
//	RafKara//MK8513-BR-B/1/d91c983b52ce486f24c53af1430f21e0/1/M/A//
const (
	infoProduct    = 0
	infoModel      = 2
	infoDatabaseID = 4
	infoMinFields  = 5
)

// DecodeDeviceInfo decrypts and parses a ping payload.
func DecodeDeviceInfo(payload []byte) (DeviceInfo, error) {
	text := strings.NewReplacer("\n", "", "\r", "").Replace(string(payload))
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return DeviceInfo{}, fmt.Errorf("ping payload is not base64: %w", err)
	}
	if len(raw) == 0 || len(raw)%des.BlockSize != 0 {
		return DeviceInfo{}, fmt.Errorf("ping payload is %d bytes, not a whole number of DES blocks", len(raw))
	}

	block, err := des.NewCipher(discoveryKey)
	if err != nil {
		return DeviceInfo{}, err
	}

	// Encrypt, not decrypt: the device produced the payload with the inverse
	// transform, so the plain text is recovered by running the cipher forward.
	// The decrypt direction returns noise.
	out := make([]byte, len(raw))
	cipher.NewCBCEncrypter(block, discoveryIV).CryptBlocks(out, raw)

	plain := strings.TrimRight(string(out), "\x00")
	fields := strings.Split(plain, "/")
	if len(fields) < infoMinFields {
		return DeviceInfo{}, fmt.Errorf("ping payload decoded to %q, which has %d fields", plain, len(fields))
	}

	return DeviceInfo{
		Product:    fields[infoProduct],
		Model:      fields[infoModel],
		DatabaseID: fields[infoDatabaseID],
		Fields:     fields,
	}, nil
}
