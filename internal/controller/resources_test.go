package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func TestNodeMaxOldSpaceMiB(t *testing.T) {
	cases := []struct {
		limit string
		want  int64
	}{
		{"", 4096},      // unknown limit — conservative default
		{"256Mi", 512},  // floor
		{"1Gi", 512},    // half=512, limit-2048 negative — floor wins
		{"2Gi", 1024},   // historical half-split
		{"4Gi", 2048},   // half=2048 > limit-2048=2048 (equal)
		{"8Gi", 6144},   // limit-2048 beats half
		{"12Gi", 8192},  // limit-2048=10240 capped
		{"32Gi", 8192},  // hard cap
	}
	for _, c := range cases {
		var q resource.Quantity
		if c.limit != "" {
			q = resource.MustParse(c.limit)
		}
		if got := nodeMaxOldSpaceMiB(q); got != c.want {
			t.Errorf("nodeMaxOldSpaceMiB(%q) = %d, want %d", c.limit, got, c.want)
		}
	}
}
