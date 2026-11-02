package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Shard is one of Total equal slices of the selected tests: test k (in
// suite then test order, after filters) belongs to shard k mod Total + 1.
// The zero Shard keeps every test.
type Shard struct {
	Index int // 1-based
	Total int
}

// ParseShard reads "i/n", such as 2/4.
func ParseShard(s string) (Shard, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Shard{}, nil
	}
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return Shard{}, fmt.Errorf("shard %q: want INDEX/TOTAL, such as 2/4", s)
	}
	i, err1 := strconv.Atoi(strings.TrimSpace(a))
	n, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil {
		return Shard{}, fmt.Errorf("shard %q: want INDEX/TOTAL, such as 2/4", s)
	}
	if n < 1 || i < 1 || i > n {
		return Shard{}, fmt.Errorf("shard %q: the index runs from 1 to the total", s)
	}
	return Shard{Index: i, Total: n}, nil
}

// Active reports whether the shard narrows the selection.
func (s Shard) Active() bool { return s.Total > 1 }

// Keeps reports whether the k-th selected test (0-based) is in this shard.
func (s Shard) Keeps(k int) bool {
	if !s.Active() {
		return true
	}
	return k%s.Total == s.Index-1
}

// String is "i/n", or "" for the zero Shard.
func (s Shard) String() string {
	if s.Total == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", s.Index, s.Total)
}
