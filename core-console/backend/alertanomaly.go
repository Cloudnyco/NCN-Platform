// alertanomaly.go — baseline / trend anomaly detection for the alert engine.
//
// Fixed thresholds are the wrong tool for metrics whose "normal" varies by node:
// fra→tyo latency is legitimately high, so a global `probe_max_rtt_ms > 200`
// either false-fires on that link or misses a real regression on a fast one.
// An anomaly rule instead learns each (node, metric) series' own rolling normal
// and fires only when the current value deviates from THAT by N sigma — no
// per-node threshold to hand-tune.
//
// The baseline is an exponentially-weighted moving mean + variance (EWMA), so it
// tracks slow drift while a short spike barely moves it (large z-score exactly
// when abnormal). State is in-memory and rebuilt over a ~window warmup after a
// restart; anomaly rules stay quiet until they have minSamples of history.

package main

import "math"

// ewmaStat is the rolling mean+variance for one (node, metric, window) series.
type ewmaStat struct {
	count int
	mean  float64
	vari  float64 // EWMA of squared deviation
	alpha float64
}

func newEWMA(window int) *ewmaStat {
	if window < 2 {
		window = 2
	}
	return &ewmaStat{alpha: 2.0 / (float64(window) + 1.0)}
}

// observe folds a new sample into the running mean + variance.
func (s *ewmaStat) observe(v float64) {
	if s.count == 0 {
		s.mean, s.vari, s.count = v, 0, 1
		return
	}
	d := v - s.mean
	s.mean += s.alpha * d
	// EWMA variance (West): vari = (1-α)(vari + α·d²)
	s.vari = (1 - s.alpha) * (s.vari + s.alpha*d*d)
	s.count++
}

// stats returns (mean, stddev, ready). ready=false until minSamples observed.
func (s *ewmaStat) stats(minSamples int) (mean, sd float64, ready bool) {
	if s == nil || s.count < minSamples {
		return 0, 0, false
	}
	return s.mean, math.Sqrt(s.vari), true
}

// anomalySpec is the compiled anomaly config for one rule (0 sigma ⇒ not an
// anomaly rule; the engine builds a normal threshold evaluator instead).
type anomalySpec struct {
	sigma    float64
	window   int
	minDelta float64
	dir      alertOp // opGt = high side, opLt = low side, opNe = both
}

const anomalyMinSamplesDiv = 4 // need at least window/4 samples before judging

// anomalyMinSamples is how many observations a series needs before an anomaly
// rule will fire (a quarter of the window, floored at 5).
func (a anomalySpec) minSamples() int {
	m := a.window / anomalyMinSamplesDiv
	if m < 5 {
		m = 5
	}
	return m
}

// judge decides whether v is anomalous against mean±sd, honouring the absolute
// floor (minDelta) so a near-flat series doesn't fire on trivial jitter.
func (a anomalySpec) judge(v, mean, sd float64) bool {
	dev := v - mean
	if a.minDelta > 0 && math.Abs(dev) < a.minDelta {
		return false
	}
	var hot bool
	if sd <= 1e-9 {
		// Flat baseline: any real jump past minDelta counts (we got here only if
		// minDelta==0 or |dev|>=minDelta).
		hot = math.Abs(dev) > 0
	} else {
		z := dev / sd
		switch a.dir {
		case opLt:
			hot = z <= -a.sigma
		case opNe:
			hot = math.Abs(z) >= a.sigma
		default: // opGt
			hot = z >= a.sigma
		}
	}
	if !hot {
		return false
	}
	// Direction filter for the flat/minDelta path too.
	switch a.dir {
	case opLt:
		return dev < 0
	case opNe:
		return true
	default:
		return dev > 0
	}
}
