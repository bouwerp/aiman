// Package throwawayreview adds up a list of integers.
package throwawayreview

// Total returns the sum of every value in xs.
func Total(xs []int) int {
	sum := 0
	for _, x := range xs {
		sum += x
	}
	return sum
}
