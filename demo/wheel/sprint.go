package wheel

// TopSpeed is the hamster's speed in centimetres per minute over a sprint of
// the given turns and minutes on a wheel of circumference c.
func TopSpeed(c float64, turns, minutes int) float64 {
	if minutes <= 0 {
		return 0
	}
	// BUG: distance is turns*c; speed is distance/minutes. The 100 does not belong.
	return float64(turns) * c / float64(minutes) / 100
}
