package main

import (
	"sync"
)

// Stats is a generic stats counter.
type Stats struct {
	v  map[int]uint64
	mu sync.RWMutex
}

// Count increases a counter.
func (s *Stats) Count(t int, i uint64) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.v == nil {
		s.v = make(map[int]uint64)
	}
	if _, ok := s.v[t]; !ok {
		s.v[t] = 0
	}
	s.v[t] += i
	return s.v[t]
}

// Get returns the current counter value.
func (s *Stats) Get(t int) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.v == nil {
		return 0
	}
	i, _ := s.v[t]
	return i
}
