package storage

import "testing"

func TestEvidenceBatchBoundMatchesBoundedCollectorPlan(t *testing.T) {
	for _, size := range []int{1, 9, 15, maxEvidenceBatchSize} {
		if err := validateEvidenceBatchSize(size); err != nil {
			t.Fatalf("bounded evidence batch size %d rejected: %v", size, err)
		}
	}
	for _, size := range []int{0, maxEvidenceBatchSize + 1} {
		if err := validateEvidenceBatchSize(size); err == nil {
			t.Fatalf("out-of-range evidence batch size %d accepted", size)
		}
	}
}
