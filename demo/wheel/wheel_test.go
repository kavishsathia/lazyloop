package wheel

import "testing"

func TestTurnsCountOnlyFullTurns(t *testing.T) {
	w := Wheel{Circumference: 50}
	for _, c := range []struct {
		distance float64
		want     int
	}{{0, 0}, {49, 0}, {50, 1}, {149, 2}, {150, 3}} {
		got, err := w.Turns(c.distance)
		if err != nil {
			t.Fatalf("Turns(%v) error = %v", c.distance, err)
		}
		if got != c.want {
			t.Errorf("Turns(%v) = %d, want %d", c.distance, got, c.want)
		}
	}
}

func TestTurnsRefusesAWheelWithNoSize(t *testing.T) {
	if _, err := (Wheel{}).Turns(10); err != ErrNoWheel {
		t.Fatalf("Turns on an empty wheel = %v, want ErrNoWheel", err)
	}
}

func TestRationSplitsEvenlyRoundingDown(t *testing.T) {
	for _, c := range []struct{ pellets, hamsters, want int }{{10, 2, 5}, {7, 2, 3}, {9, 3, 3}, {5, 0, 0}} {
		if got := Ration(c.pellets, c.hamsters); got != c.want {
			t.Errorf("Ration(%d, %d) = %d, want %d", c.pellets, c.hamsters, got, c.want)
		}
	}
}

func TestTopSpeedIsDistanceOverTime(t *testing.T) {
	if got := TopSpeed(50, 40, 10); got != 200 {
		t.Fatalf("TopSpeed(50, 40, 10) = %v, want 200", got)
	}
	if got := TopSpeed(50, 40, 0); got != 0 {
		t.Fatalf("TopSpeed with no time = %v, want 0", got)
	}
}
