package main

import "testing"

// A steady series builds a tight baseline; a sustained-normal value is not
// anomalous, but a large spike in the configured direction is.
func TestAnomalyJudge(t *testing.T) {
	st := newEWMA(120)
	// warm up around 95ms with small jitter (need ≥ minSamples = window/4 = 30)
	jitter := []float64{95, 96, 94, 95, 97, 93, 95, 96, 94, 95}
	for i := 0; i < 4; i++ {
		for _, v := range jitter {
			st.observe(v)
		}
	}
	spec := anomalySpec{sigma: 5, window: 120, minDelta: 30, dir: opGt}
	mean, sd, ready := st.stats(spec.minSamples())
	if !ready {
		t.Fatalf("expected ready after warmup")
	}
	if mean < 90 || mean > 100 {
		t.Fatalf("baseline mean %.1f not near 95", mean)
	}
	// A normal-ish value must NOT be flagged.
	if spec.judge(96, mean, sd) {
		t.Errorf("96ms should be within baseline %.1f±%.1f", mean, sd)
	}
	// A big jump (>minDelta and many sigma) must be flagged.
	if !spec.judge(300, mean, sd) {
		t.Errorf("300ms should be anomalous vs %.1f±%.1f", mean, sd)
	}
	// minDelta floor: a tiny absolute jump never fires even if many sigma.
	tight := anomalySpec{sigma: 3, window: 120, minDelta: 30, dir: opGt}
	if tight.judge(mean+5, mean, 0.5) {
		t.Errorf("a 5ms jump must be ignored under a 30ms minDelta floor")
	}
	// Direction: a low value must not fire a high-side (opGt) rule.
	if spec.judge(10, mean, sd) {
		t.Errorf("opGt must not fire on a LOW outlier")
	}
	// ...but a both-sided (opNe) rule should.
	both := anomalySpec{sigma: 5, window: 120, minDelta: 30, dir: opNe}
	if !both.judge(10, mean, sd) {
		t.Errorf("opNe should fire on a low outlier far from baseline")
	}
}

// Warmup: an anomaly rule stays quiet until it has minSamples of history.
func TestAnomalyWarmup(t *testing.T) {
	st := newEWMA(120)
	spec := anomalySpec{sigma: 5, window: 120, minDelta: 30, dir: opGt}
	st.observe(95)
	st.observe(96)
	if _, _, ready := st.stats(spec.minSamples()); ready {
		t.Errorf("should not be ready with 2 samples (min %d)", spec.minSamples())
	}
}
