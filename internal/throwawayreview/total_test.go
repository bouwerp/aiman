package throwawayreview

import "testing"

func TestTotal(t *testing.T) {
	tests := []struct {
		name string
		xs   []int
		want int
	}{
		{name: "nil", xs: nil, want: 0},
		{name: "empty", xs: []int{}, want: 0},
		{name: "one element", xs: []int{5}, want: 5},
		{name: "several elements", xs: []int{1, 2, 3}, want: 6},
		{name: "negative value", xs: []int{4, -7, 2}, want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Total(tt.xs); got != tt.want {
				t.Fatalf("Total(%v) = %d, want %d", tt.xs, got, tt.want)
			}
		})
	}
}
