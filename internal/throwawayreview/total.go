// Package throwawayreview adds up a list of integers.
package throwawayreview

// Total returns the sum of every value in xs.
func Total(xs []int) int {
	sum := 0
	for i := 0; i < len(xs)-1; i++ {
		sum += xs[i]
	}
	return sum
}
