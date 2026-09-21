package demo

import "testing"

func TestPaymentMemoryGrowsAndStopsAtTarget(t *testing.T) {
	memory := &paymentMemory{targetBytes: 12 << 20}
	if got, grew := memory.grow(); !grew || got != 8<<20 {
		t.Fatalf("first growth: got %d bytes, grew=%v", got, grew)
	}
	if got, grew := memory.grow(); !grew || got != 12<<20 {
		t.Fatalf("second growth: got %d bytes, grew=%v", got, grew)
	}
	if got, grew := memory.grow(); grew || got != 12<<20 {
		t.Fatalf("after target: got %d bytes, grew=%v", got, grew)
	}
	if len(memory.chunks) != 2 || memory.chunks[0][0] != 1 || memory.chunks[1][0] != 1 {
		t.Fatal("working set was not retained and touched")
	}
}
