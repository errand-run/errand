package page

import (
	"reflect"
	"testing"
)

// The scale of a curve is the one the run said; else log for what is positive and spans more than two decades.
func TestTheScaleOfACurve(t *testing.T) {
	j := &journal{}
	j.read(`{"t": 10, "residual": 1.0, "loss": 1.5, "energy": 3.0, "wide": 1.0, "signed": -1.0}
{"t": 11, "residual": 1e-4, "loss": 1.2, "energy": 2.0, "wide": 1000.0, "signed": 500.0, "scales": {"loss": "log", "wide": "lin", "energy": "cubic"}}
{"t": 12.5, "residual": 1e-5}
`)
	s, _ := j.state()
	got := map[string]string{}
	for _, name := range s.Order {
		got[name] = s.scale(name)
	}
	want := map[string]string{"residual": "log", "loss": "log", "energy": "lin", "wide": "lin", "signed": "lin"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scales: %v", got)
	}
	if _, there := s.Curves["scales"]; there {
		t.Error("what says the scales is no curve")
	}
	points := curvePoints(s, []string{"residual", "nothing"})
	one := points["residual"].(map[string]any)
	if len(points) != 1 || !reflect.DeepEqual(one["t"], []float64{0, 1, 2.5}) || !reflect.DeepEqual(one["v"], []float64{1, 1e-4, 1e-5}) {
		t.Errorf("the points, from the journal's first line: %v", points)
	}
}

// A long curve is drawn by fewer points: its ends and its spikes are among them, in their order.
func TestALongCurveKeepsItsSpikes(t *testing.T) {
	values := make([]float64, 100000)
	values[33333], values[66666] = 7, -7
	at := thinned(values, 2000)
	kept := map[int]bool{}
	for k, i := range at {
		kept[i] = true
		if k > 0 && i <= at[k-1] {
			t.Fatalf("out of order at %d: %d after %d", k, i, at[k-1])
		}
	}
	if len(at) > 2000 || !kept[0] || !kept[len(values)-1] || !kept[33333] || !kept[66666] {
		t.Errorf("%d points; ends and spikes: %v %v %v %v", len(at), kept[0], kept[len(values)-1], kept[33333], kept[66666])
	}
	if got := thinned(values[:5], 2000); !reflect.DeepEqual(got, []int{0, 1, 2, 3, 4}) {
		t.Errorf("a short one, whole: %v", got)
	}
}
