package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseShard(t *testing.T) {
	tests := []struct {
		in      string
		want    Shard
		wantErr string
	}{
		{in: "", want: Shard{}},
		{in: "1/1", want: Shard{Index: 1, Total: 1}},
		{in: " 2/4 ", want: Shard{Index: 2, Total: 4}},
		{in: "3", wantErr: "want INDEX/TOTAL"},
		{in: "a/b", wantErr: "want INDEX/TOTAL"},
		{in: "0/4", wantErr: "the index runs from 1 to the total"},
		{in: "5/4", wantErr: "the index runs from 1 to the total"},
		{in: "1/0", wantErr: "the index runs from 1 to the total"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseShard(tt.in)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShardKeeps(t *testing.T) {
	var kept [][]int
	for i := 1; i <= 3; i++ {
		s := Shard{Index: i, Total: 3}
		var ks []int
		for k := 0; k < 7; k++ {
			if s.Keeps(k) {
				ks = append(ks, k)
			}
		}
		kept = append(kept, ks)
	}
	assert.Equal(t, [][]int{{0, 3, 6}, {1, 4}, {2, 5}}, kept, "every test is in exactly one shard")
	assert.True(t, Shard{}.Keeps(5))
	assert.True(t, Shard{Index: 1, Total: 1}.Keeps(5))
	assert.Equal(t, "2/3", Shard{Index: 2, Total: 3}.String())
	assert.Equal(t, "", Shard{}.String())
}
