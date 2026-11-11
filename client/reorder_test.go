package client

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apply runs a plan against a queue the way the unit does: removals keep the
// others' order, FirstOrder adds at the front, AddOrder at the end.
func apply(have []string, p ReorderPlan) []string {
	q := slices.DeleteFunc(slices.Clone(have), func(id string) bool { return slices.Contains(p.Remove, id) })
	for _, id := range p.Front {
		q = slices.Insert(q, 0, id)
	}
	return append(q, p.Back...)
}

func TestPlanReorder(t *testing.T) {
	cases := []struct {
		name       string
		have, want string
		removed    int
	}{
		{"unchanged", "a b c d", "a b c d", 0},
		{"empty", "", "", 0},
		{"to the top", "a b c d", "d a b c", 1},
		{"to the bottom", "a b c d", "b c d a", 1},
		{"up one near the end", "a b c d e", "a b c e d", 1},
		{"up one near the front", "a b c d e", "b a c d e", 1},
		{"up one in the middle", "a b c d e f", "a b d c e f", 3},
		{"reversed", "a b c", "c b a", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			have, want := strings.Fields(c.have), strings.Fields(c.want)
			p, err := PlanReorder(have, want)
			require.NoError(t, err)
			assert.Equal(t, want, apply(have, p))
			assert.Len(t, p.Remove, c.removed)
			assert.Equal(t, c.removed == 0, p.Empty())
		})
	}
}

func TestPlanReorderReachesEveryOrder(t *testing.T) {
	have := []string{"a", "b", "c", "d", "e"}
	var permute func(prefix, rest []string)
	permute = func(prefix, rest []string) {
		if len(rest) == 0 {
			p, err := PlanReorder(have, prefix)
			require.NoError(t, err)
			assert.Equal(t, prefix, apply(have, p), "plan %+v", p)
			return
		}
		for i := range rest {
			next := slices.Delete(slices.Clone(rest), i, i+1)
			permute(append(slices.Clone(prefix), rest[i]), next)
		}
	}
	permute(nil, have)
}

func TestPlanReorderRejectsADifferentSet(t *testing.T) {
	for _, c := range []struct{ have, want string }{
		{"a b", "a"},
		{"a b", "a c"},
		{"a b", "a a"},
		{"a a", "a a"},
	} {
		_, err := PlanReorder(strings.Fields(c.have), strings.Fields(c.want))
		assert.Error(t, err, "%q to %q", c.have, c.want)
	}
}
