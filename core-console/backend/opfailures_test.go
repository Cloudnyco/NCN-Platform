// Pure/in-memory tests for the op-failure store. Operate on a local
// opFailureStore — never touch globalOpFailures, the audit log, or the fleet.

package main

import "testing"

func TestOpFailureStoreRingAndDismiss(t *testing.T) {
	s := &opFailureStore{}
	for i := 0; i < opFailureCap+25; i++ {
		s.add(&opFailure{ID: string(rune('a'+i%26)) + itoaTest(i), Kind: opKindDelete, Target: "n", At: int64(i), Status: "open"})
	}
	if got := len(s.listSnapshot(false)); got != opFailureCap {
		t.Fatalf("ring not capped: got %d want %d", got, opFailureCap)
	}
	// Newest-first: highest At first.
	list := s.listSnapshot(false)
	if list[0].At < list[len(list)-1].At {
		t.Fatalf("not newest-first: first.At=%d last.At=%d", list[0].At, list[len(list)-1].At)
	}
	// Dismiss the newest → openOnly drops it, openCount falls by one.
	before := s.openCount()
	if !s.dismiss(list[0].ID) {
		t.Fatalf("dismiss returned false for existing id %q", list[0].ID)
	}
	if s.openCount() != before-1 {
		t.Fatalf("openCount after dismiss = %d want %d", s.openCount(), before-1)
	}
	for _, f := range s.listSnapshot(true) {
		if f.ID == list[0].ID {
			t.Fatalf("dismissed failure still in openOnly list")
		}
	}
	if s.dismiss("nope") {
		t.Fatalf("dismiss of unknown id returned true")
	}
}

func TestOpKindLabel(t *testing.T) {
	cases := map[string]string{
		opKindDecommission: "decommission",
		opKindRecommission: "recommission",
		opKindDelete:       "delete",
		opKindMeshApply:    "mesh apply",
		opKindOnboard:      "onboard / provision",
		"weird":            "weird",
	}
	for k, want := range cases {
		if got := opKindLabel(k); got != want {
			t.Fatalf("opKindLabel(%q) = %q want %q", k, got, want)
		}
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestOpFailureDedup verifies a repeat (same kind+target+reason) within the
// window is suppressed, while a different reason still notifies.
func TestOpFailureDedup(t *testing.T) {
	a := &opFailure{Kind: opKindMeshApply, Target: "dedup-test-1", Reason: "exit=1"}
	if !opFailureShouldNotify(a) {
		t.Fatal("first notify should be allowed")
	}
	if opFailureShouldNotify(a) {
		t.Fatal("immediate repeat should be deduped")
	}
	b := &opFailure{Kind: opKindMeshApply, Target: "dedup-test-1", Reason: "exit=2"}
	if !opFailureShouldNotify(b) {
		t.Fatal("different reason should notify")
	}
}
