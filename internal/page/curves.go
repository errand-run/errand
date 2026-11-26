// The curves of a run's steps: on which scale each is drawn, and what of them the page is sent.

package page

import "math"

// scale: "log" or "lin", as the run said it ( `errand.Log` ); not said, log for values all positive that span more than two
// decades -- a residual
func (s *stepFull) scale(name string) string {
	if said := s.Scales[name]; said != "" {
		return said
	}
	lo, hi := math.Inf(1), 0.0
	for _, v := range s.Curves[name] {
		lo, hi = min(lo, v), max(hi, v)
	}
	if lo > 0 && hi > 100*lo {
		return "log"
	}
	return "lin"
}

// curvesOf: what the detail says of each curve -- not its points
func curvesOf(s *stepFull) []map[string]any {
	out := []map[string]any{}
	if s == nil {
		return out
	}
	for _, name := range s.Order {
		values := s.Curves[name]
		varies := false
		for _, v := range values {
			varies = varies || v != values[0]
		}
		out = append(out, map[string]any{"name": name, "n": len(values), "last": values[len(values)-1], "scale": s.scale(name), "varies": varies})
	}
	return out
}

const drawnPoints = 2000 // of a curve, at most, sent to be drawn

// thinned: which points of a long curve are drawn -- the first, the last, and the lowest and highest of each of the slices
// in between, so that a spike is never lost
func thinned(values []float64, most int) []int {
	n := len(values)
	if n <= most {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	slices := (most - 2) / 2
	out := []int{0}
	for k := range slices {
		from, to := 1+k*(n-2)/slices, 1+(k+1)*(n-2)/slices
		if from >= to {
			continue
		}
		lo, hi := from, from
		for i := from + 1; i < to; i++ {
			if values[i] < values[lo] {
				lo = i
			}
			if values[i] > values[hi] {
				hi = i
			}
		}
		out = append(out, min(lo, hi))
		if lo != hi {
			out = append(out, max(lo, hi))
		}
	}
	return append(out, n-1)
}

// curvePoints: the points of the curves asked for: which of the curve's ( `i` ), when ( `t`, seconds since the journal's
// first line ), and the value
func curvePoints(s *stepFull, names []string) map[string]any {
	out := map[string]any{}
	if s == nil {
		return out
	}
	for _, name := range names {
		values, times := s.Curves[name], s.Times[name]
		if len(values) == 0 {
			continue
		}
		at := thinned(values, drawnPoints)
		t, v := make([]float64, len(at)), make([]float64, len(at))
		for k, i := range at {
			t[k], v[k] = math.Round(1000*(times[i]-s.Start))/1000, values[i]
		}
		out[name] = map[string]any{"i": at, "t": t, "v": v, "n": len(values)}
	}
	return out
}
