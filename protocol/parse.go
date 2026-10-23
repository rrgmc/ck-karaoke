package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// ParseFrame decodes one application message. fromDevice must say which way the
// frame travelled: device frames carry four extra header bytes and repeat their
// body, so the same bytes decode differently in each direction.
func ParseFrame(data []byte, fromDevice bool) (Frame, error) {
	if len(data) < 1 {
		return nil, errors.New("frame is empty")
	}

	// Database transfer frames are bare opcodes outside the control format.
	switch data[0] {
	case dbChunkID:
		return parseDatabaseChunk(data)
	case dbReadyID, dbInfoID, dbContinueID:
		return &DatabaseFrame{ID: data[0], Payload: append([]byte(nil), data[1:]...)}, nil
	case dbStartID, dbFinishID:
		if len(data) > 1 {
			return nil, fmt.Errorf("database frame %#02x has unexpected payload (%d bytes)", data[0], len(data)-1)
		}
		return &DatabaseFrame{ID: data[0]}, nil
	}

	if len(data) < 18 {
		return nil, fmt.Errorf("control frame too short (%d bytes)", len(data))
	}

	buf := bytes.NewBuffer(data)
	header, err := parseFrameHeader(buf, fromDevice)
	if err != nil {
		return nil, err
	}

	// Status and command frames diverge here: a status frame has four zero bytes
	// then a zero, a command frame has a body length then 0x0A.
	lead, err := readN(buf, 4, "body length")
	if err != nil {
		return nil, err
	}
	marker, err := readN(buf, 1, "body marker")
	if err != nil {
		return nil, err
	}

	var frame Frame
	switch {
	case bytes.Equal(lead, []byte{0x00, 0x00, 0x00, 0x00}) && marker[0] == 0x00:
		frame, err = parseStatusFrame(header, buf)
	case marker[0] == 0x0A:
		frame, err = parseCommandFrame(header, buf, binary.BigEndian.Uint32(lead))
	default:
		return nil, fmt.Errorf("expected body marker 0x00 or 0x0A, got %#02x", marker[0])
	}
	if err != nil {
		return nil, err
	}
	if err := checkReadFull(buf); err != nil {
		return nil, err
	}
	return frame, nil
}

func parseFrameHeader(buf *bytes.Buffer, fromDevice bool) (*FrameHeader, error) {
	h := &FrameHeader{FromDevice: fromDevice}

	ids, err := readN(buf, 2, "class and kind")
	if err != nil {
		return nil, err
	}
	h.Class, h.Kind = ids[0], ids[1]

	if err := readNEquals(buf, 4, []byte{0x00, 0x00, 0x00, 0x00}, "header padding"); err != nil {
		return nil, err
	}
	if err := binary.Read(buf, binary.BigEndian, &h.Seq); err != nil {
		return nil, fmt.Errorf("reading sequence number: %w", err)
	}
	if err := readNEquals(buf, 4, []byte{0xFF, 0xFF, 0xFF, 0xFF}, "header sentinel"); err != nil {
		return nil, err
	}
	if fromDevice {
		if err := readNEquals(buf, 4, []byte{0x00, 0x00, 0x00, 0x00}, "device header padding"); err != nil {
			return nil, err
		}
	}
	tag, err := readN(buf, 4, "command tag")
	if err != nil {
		return nil, err
	}
	copy(h.Tag[:], tag)
	return h, nil
}

func parseCommandFrame(header *FrameHeader, buf *bytes.Buffer, bodyLen uint32) (*CommandFrame, error) {
	frame := &CommandFrame{FrameHeader: *header}

	// Device frames repeat the body: the request as received, then the response.
	bodies := 1
	if header.FromDevice {
		bodies = 2
	}

	for i := range bodies {
		n := bodyLen
		if i == 0 {
			n-- // the leading 0x0A was already consumed
		} else {
			if err := binary.Read(buf, binary.BigEndian, &n); err != nil {
				return nil, fmt.Errorf("reading response body length: %w", err)
			}
			if err := readNEquals(buf, 1, []byte{0x0A}, "response body marker"); err != nil {
				return nil, err
			}
			n--
		}

		raw, err := readN(buf, int(n), fmt.Sprintf("command body %d (%d bytes)", i, n))
		if err != nil {
			return nil, err
		}
		body := bytes.NewBuffer(raw)

		if err := readNEquals(body, 1, []byte{0x24}, "body prefix"); err != nil {
			return nil, err
		}
		clientID, err := readN(body, 36, "client id")
		if err != nil {
			return nil, err
		}
		if i == 0 {
			frame.ClientID = string(clientID)
		}
		// 0x12 introduces a length-prefixed client name — the text the device
		// shows on screen for this remote.
		if err := readNEquals(body, 1, []byte{0x12}, "client name tag"); err != nil {
			return nil, err
		}
		nameLen, err := readN(body, 1, "client name length")
		if err != nil {
			return nil, err
		}
		name, err := readN(body, int(nameLen[0]), "client name")
		if err != nil {
			return nil, err
		}
		if i == 0 {
			frame.Name = string(name)
		}

		if i == 0 {
			frame.Request, err = ParseFieldList(body)
			if err != nil {
				return nil, err
			}
		} else {
			// The second body either repeats the request (marker 'B') or carries
			// new data (marker 'J'); only the latter is worth keeping.
			list, err := parseFieldList(0, body, []byte{MarkerRequest, MarkerResponse})
			if err != nil {
				return nil, err
			}
			if list != nil && list.Marker == MarkerResponse {
				frame.Response = list
			}
			// Session echoes append three trailing bytes.
			if frame.Class == ClassControl && frame.Kind == KindSession {
				trailer, err := readNEqualsValue(body, 3, []byte{0xA0, 0x01, 0x01}, "session trailer")
				if err != nil {
					return nil, err
				}
				frame.Trailer = trailer
			}
		}

		if err := checkReadFull(body); err != nil {
			return nil, fmt.Errorf("command body %d: %w", i, err)
		}
	}
	return frame, nil
}

func parseStatusFrame(header *FrameHeader, buf *bytes.Buffer) (*StatusFrame, error) {
	frame := &StatusFrame{FrameHeader: *header}

	var n uint32
	if err := binary.Read(buf, binary.BigEndian, &n); err != nil {
		return nil, fmt.Errorf("reading status body length: %w", err)
	}
	raw, err := readN(buf, int(n), fmt.Sprintf("status body (%d bytes)", n))
	if err != nil {
		return nil, err
	}

	body := bytes.NewBuffer(raw)
	frame.Fields, err = ParseFieldList(body)
	if err != nil {
		return nil, err
	}
	if err := checkReadFull(body); err != nil {
		return nil, fmt.Errorf("status body: %w", err)
	}
	return frame, nil
}

func parseDatabaseChunk(data []byte) (*DatabaseChunkFrame, error) {
	// The header is opaque and variable-length, so the frame is kept whole and
	// handed to catalog.Reassembler, which finds the payload boundary from the
	// SQLite header and the running total instead of guessing a fixed offset.
	if len(data) < 64 {
		return nil, fmt.Errorf("database chunk too short (%d bytes)", len(data))
	}
	return &DatabaseChunkFrame{Raw: append([]byte(nil), data...)}, nil
}
