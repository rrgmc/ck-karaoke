package protocol

import "testing"

func TestCleanName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Maria", "Maria"},
		{"  Maria  ", "Maria"},
		{"Maria   Silva", "Maria Silva"},
		{"Maria\tSilva", "Maria Silva"},
		// Non-ASCII is dropped rather than escaped: the device's font is
		// unknown, and boxes are worse than a name without its accents.
		{"João", "Joo"},
		{"Ana-Lúcia", "Ana-Lcia"},
		{"bad\x00name", "badname"},
		{"", DefaultClientName},
		{"   ", DefaultClientName},
		{"\x01\x02", DefaultClientName},
	}
	for _, c := range cases {
		if got := CleanName(c.in); got != c.want {
			t.Errorf("CleanName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanNameRespectsTheLimit(t *testing.T) {
	long := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	got := CleanName(long)
	if len(got) > NameLimit {
		t.Errorf("CleanName kept %d characters, over the %d limit: %q", len(got), NameLimit, got)
	}
	if got != long[:NameLimit] {
		t.Errorf("CleanName truncated to %q, want the first %d characters", got, NameLimit)
	}
	// Whatever comes out has to survive the encoder, which writes the length as
	// a single byte.
	if len(got) > maxClientName {
		t.Fatal("a cleaned name cannot be length-prefixed")
	}
}

// The device records the client that reserved a song as "<counter>.<name>".
func TestQueuedBy(t *testing.T) {
	cases := []struct {
		fields []string
		want   string
	}{
		{[]string{"123456789", "30.NE2211", "1"}, "NE2211"},
		{[]string{"123456789", "1.RANGEL-PC", "1"}, "RANGEL-PC"},
		{[]string{"123456789", "3.Maria Silva", "1"}, "Maria Silva"},
		{[]string{"123456789", "NODOT", "1"}, "NODOT"},
		{[]string{"123456789"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := (QueueEntry{Fields: c.fields}).QueuedBy(); got != c.want {
			t.Errorf("QueuedBy(%q) = %q, want %q", c.fields, got, c.want)
		}
	}
}
