package protocol

import "testing"

// The grid is floor(step*100/12). Every level below is one the device reports,
// and rounding to nearest instead of flooring would have produced 67 and 92.
func TestVolumeGridMatchesDevice(t *testing.T) {
	want := []int{0, 8, 16, 25, 33, 41, 50, 58, 66, 75, 83, 91, 100}
	for s, level := range want {
		if got := VolumeLevel(s); got != level {
			t.Errorf("VolumeLevel(%d) = %d, want %d", s, got, level)
		}
	}
}

// Off-grid values snap the way the device snaps them: 79 -> 83, 84 -> 83,
// 60 -> 58, 63 -> 66.
func TestVolumeSnapping(t *testing.T) {
	for _, tc := range []struct{ sent, want int }{
		{79, 83}, // exact tie, device rounds up
		{84, 83},
		{60, 58},
		{63, 66},
	} {
		if got := VolumeLevel(VolumeStep(tc.sent)); got != tc.want {
			t.Errorf("sending %d snaps to %d, device gives %d", tc.sent, got, tc.want)
		}
	}
}

func TestVolumeNudgeMovesOneStep(t *testing.T) {
	if got := VolumeNudge(83, +1); got != 91 {
		t.Errorf("VolumeNudge(83, +1) = %d, want 91", got)
	}
	if got := VolumeNudge(83, -1); got != 75 {
		t.Errorf("VolumeNudge(83, -1) = %d, want 75", got)
	}
	// The ends must not wrap or overshoot.
	if got := VolumeNudge(100, +1); got != 100 {
		t.Errorf("VolumeNudge(100, +1) = %d, want 100", got)
	}
	if got := VolumeNudge(0, -1); got != 0 {
		t.Errorf("VolumeNudge(0, -1) = %d, want 0", got)
	}
	// A blank reading starts from the middle rather than an extreme.
	if got := VolumeStep(-1); got != VolumeSteps/2 {
		t.Errorf("VolumeStep(-1) = %d, want %d", got, VolumeSteps/2)
	}
}

// Each press of the tempo control moves the device one step, so the reported
// number must move by exactly one. A step is 33.33 per-mille reported whole, so
// truncating made the first press look like it did nothing.
func TestTempoStepsAreWhole(t *testing.T) {
	for _, tc := range []struct {
		raw  int
		want int
	}{
		{934, 2}, {967, 1}, {1000, 0}, {1034, -1}, {1067, -2},
	} {
		f := make([]string, statusFieldCount)
		for i := range f {
			f[i] = ""
		}
		f[fPosition] = "0"
		f[fTempo] = itoa(tc.raw)

		msg, err := parsePlaybackStatus(&StatusFrame{Fields: &FieldList{Fields: f}})
		if err != nil {
			t.Fatalf("raw %d: %v", tc.raw, err)
		}
		if got := msg.(*Status).Tempo; got != tc.want {
			t.Errorf("raw %d reported as %+d, want %+d", tc.raw, got, tc.want)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
