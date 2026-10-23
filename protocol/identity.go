package protocol

import (
	"crypto/rand"
	"fmt"
)

// Identity is the client a session is opened as.
//
// It is only a UUID and a display name. There is nothing to register: the
// device accepts any UUID, provided the session frame carries the matching
// checksum (see SessionChecksum).
type Identity struct {
	ClientID string
	// Name is the name the device shows beside songs this client queues. Empty
	// means DefaultClientName.
	Name string
}

// NewIdentity returns a fresh identity: a random version 4 UUID.
//
// Two programs need different identities, and two random UUIDs will not
// collide. Reusing one displaces the earlier session silently, which is the
// failure this exists to avoid.
func NewIdentity() Identity {
	return Identity{ClientID: RandomClientID(), Name: DefaultClientName}
}

// RandomClientID returns a random version 4 UUID in the form the device
// expects: 36 characters, lower-case hex, dashed.
func RandomClientID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice, and a client id that is not
		// random would silently collide with another remote.
		panic("protocol: cannot read random bytes for a client id: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (i Identity) String() string {
	return fmt.Sprintf("%s (name %q)", i.ClientID, i.DisplayName())
}

// DisplayName is the name to send, falling back to DefaultClientName.
func (i Identity) DisplayName() string {
	if i.Name == "" {
		return DefaultClientName
	}
	return i.Name
}
