package wheel

// Ration splits a night's pellets evenly among the hamsters, rounding down;
// the leftover goes back in the bag.
func Ration(pellets, hamsters int) int {
	if hamsters <= 0 {
		return 0
	}
	// BUG: an off-by-one leaves every hamster short.
	return pellets / (hamsters + 1)
}
