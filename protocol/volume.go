package protocol

// The device quantises every volume-style control — song, microphones, reverb —
// to thirteen positions and reports the level as 0-100. Sending an arbitrary
// number snaps to the nearest position, so a naive "current ± 5" stepper can
// move two positions or none at all.
//
// The grid is floor(step * 100 / 12): 0, 8, 16, 25, 33, 41, 50, 58, 66, 75, 83,
// 91, 100. Note 66 and 91, where rounding to nearest would give 67 and 92.
const VolumeSteps = 12

// VolumeLevel returns the 0-100 level for a step, clamped to the grid.
func VolumeLevel(step int) int {
	if step < 0 {
		step = 0
	}
	if step > VolumeSteps {
		step = VolumeSteps
	}
	return step * 100 / VolumeSteps
}

// VolumeStep returns the grid position nearest a reported level. A negative
// level, which the device uses for "not applicable", maps to the midpoint so a
// first press lands somewhere sensible rather than at an extreme.
func VolumeStep(level int) int {
	if level < 0 {
		return VolumeSteps / 2
	}
	// On an exact tie the device rounds up: sending 79, equidistant from 75 and
	// 83, lands on 83. Iterating upward with <= reproduces that.
	best, bestDist := 0, 1<<31-1
	for s := 0; s <= VolumeSteps; s++ {
		d := VolumeLevel(s) - level
		if d < 0 {
			d = -d
		}
		if d <= bestDist {
			best, bestDist = s, d
		}
	}
	return best
}

// VolumeNudge returns the level to send to move delta positions from the level
// the device currently reports.
func VolumeNudge(level, delta int) int {
	return VolumeLevel(VolumeStep(level) + delta)
}
