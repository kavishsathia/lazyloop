// Package wheel measures a hamster's night on the wheel.
package wheel

import "errors"

// Wheel is a hamster wheel with a circumference in centimetres.
type Wheel struct{ Circumference float64 }

// ErrNoWheel is returned for a wheel with no size; nobody runs on a point.
var ErrNoWheel = errors.New("wheel has no circumference")

// Turns reports how many FULL turns cover the given distance. Partial turns do
// not count: a hamster that stops mid-turn has not turned.
func (w Wheel) Turns(distance float64) (int, error) {
	if w.Circumference <= 0 {
		return 0, ErrNoWheel
	}
	// BUG: this rounds to the nearest turn; it must truncate to full turns.
	return int(distance/w.Circumference + 0.5), nil
}

// Distance is how far the hamster went, in centimetres, after this many turns.
func (w Wheel) Distance(turns int) float64 { return float64(turns) * w.Circumference }
