package main

import (
	"sync"
	"time"
)

// Status is the lifecycle of an operation as seen by a polling client.
type Status int

const (
	Pending Status = iota
	Done
	Failed
)

// Operation is the API server's in-memory handle for a unit of client work.
// Clients poll Get(id) to watch Status move from Pending to Done.
type Operation struct {
	ID        uint64
	CreatedAt time.Time
	Deadline  time.Time
	Status    Status
	DoneAt    time.Time
}

// Store is the API server's in-memory operation table. It hands new work to the
// queue and lets the backend mark operations complete. Nothing here is ever
// evicted; abandoned operations linger as Pending forever, which is part of the
// story.
type Store struct {
	mu  sync.Mutex
	ops map[uint64]*Operation
	seq uint64
}

func NewStore() *Store {
	return &Store{ops: make(map[uint64]*Operation)}
}

// Start records a new operation and returns its id plus the Work to enqueue.
func (s *Store) Start(now, deadline time.Time) (uint64, Work) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := s.seq
	s.ops[id] = &Operation{ID: id, CreatedAt: now, Deadline: deadline, Status: Pending}
	return id, Work{OpID: id, Enqueued: now, Deadline: deadline}
}

// Get returns the current status of an operation (the "Get operation" API).
func (s *Store) Get(id uint64) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op, ok := s.ops[id]; ok {
		return op.Status
	}
	return Failed
}

// Complete marks an operation done. The backend calls this when heavy work
// finishes, regardless of whether anyone is still waiting for the result.
func (s *Store) Complete(id uint64, doneAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op, ok := s.ops[id]; ok {
		op.Status = Done
		op.DoneAt = doneAt
	}
}
