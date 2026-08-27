package wheel

import "testing"

func TestTurnsCountOnlyFullTurns(t *testing.T) {
	w := Wheel{Circumference: 50}
	cases := []struct {
		distance float64
		want     int
	}{
		{0, 0},
		{49, 0},
		{50, 1},
		{149, 2},
		{150, 3},
	}
	for _, c := range cases {
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
		t.Fatalf("Turns on an empty wheel error = %v, want ErrNoWheel", err)
	}
}

func TestDistanceRoundTrips(t *testing.T) {
	w := Wheel{Circumference: 62.8}
	turns, err := w.Turns(w.Distance(40))
	if err != nil || turns != 40 {
		t.Fatalf("40 turns round-tripped to %d (%v)", turns, err)
	}
}

func TestMarathonsAreKilometres(t *testing.T) {
	if got := Marathons(5_200_00); got != 5.2 {
		t.Fatalf("Marathons(520000 cm) = %v, want 5.2", got)
	}
}
