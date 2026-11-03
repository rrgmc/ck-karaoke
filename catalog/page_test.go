package catalog

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSongsAfterPagesThroughEverySongInOrder(t *testing.T) {
	db := openFixture(t)
	ctx := context.Background()

	var ids []int
	after := 0
	for {
		page, err := db.SongsAfter(ctx, after, 3)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		assert.LessOrEqual(t, len(page), 3)
		for _, s := range page {
			ids = append(ids, s.ID)
		}
		after = page[len(page)-1].ID
	}
	assert.Len(t, ids, db.Info().Songs, "every song once")
	assert.IsIncreasing(t, ids)
}
