package demo

import "sync"

// paymentMemory simulates a bounded, retained working set. A lower Kubernetes
// memory limit makes a previously healthy workload fail under the same traffic.
type paymentMemory struct {
	mu          sync.Mutex
	chunks      [][]byte
	retained    int
	targetBytes int
}

func (m *paymentMemory) grow() (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.retained >= m.targetBytes {
		return m.retained, false
	}
	const chunkBytes = 8 << 20
	size := min(chunkBytes, m.targetBytes-m.retained)
	chunk := make([]byte, size)
	for offset := 0; offset < len(chunk); offset += 4096 {
		chunk[offset] = 1
	}
	m.chunks = append(m.chunks, chunk)
	m.retained += size
	return m.retained, true
}
