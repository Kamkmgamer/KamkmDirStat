package main

import (
	"context"
	"errors"
	"sync"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

// The page searches as the user types. Each startSearch replaces any waiting search and
// cancels a running one, so only the latest query costs time; the page polls searchStatus
// until the result for its number arrives.

type SearchStatus struct {
	Seq    int            `json:"seq"`
	Result *search.Result `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

type searchSession struct {
	sync.Mutex
	seq        int
	pending    *search.Query
	pendingSeq int
	busy       bool
	cancel     context.CancelFunc
	status     SearchStatus
}

func startSearch(q search.Query) (int, error) { return localMachine.startSearch(q) }

func startSearchFor(target string, q search.Query) (int, error) {
	return machineFor(target).startSearch(q)
}

func (m *machine) startSearch(q search.Query) (int, error) {
	if len(q.Text) > 1000 || len(q.Exts) > 50 {
		return 0, errors.New("The search is too long.")
	}
	state := &m.search
	state.Lock()
	defer state.Unlock()
	state.seq++
	state.pending, state.pendingSeq = &q, state.seq
	if state.cancel != nil {
		state.cancel()
	}
	if !state.busy {
		state.busy = true
		go m.runSearches()
	}
	return state.seq, nil
}

func searchStatus() SearchStatus { return localMachine.searchStatus() }

func searchStatusFor(target string) SearchStatus { return machineFor(target).searchStatus() }

func (m *machine) searchStatus() SearchStatus {
	m.search.Lock()
	defer m.search.Unlock()
	return m.search.status
}

func (m *machine) runSearches() {
	state := &m.search
	for {
		state.Lock()
		if state.pending == nil {
			state.busy = false
			state.Unlock()
			return
		}
		q, seq := *state.pending, state.pendingSeq
		state.pending = nil
		ctx, cancel := context.WithCancel(context.Background())
		state.cancel = cancel
		state.Unlock()

		result, err := m.runSearch(ctx, q)
		cancel()

		state.Lock()
		state.cancel = nil
		if state.pending == nil || seq == state.seq {
			s := SearchStatus{Seq: seq}
			if err != nil {
				s.Error = err.Error()
			} else {
				s.Result = &result
			}
			state.status = s
		}
		state.Unlock()
	}
}

func (m *machine) runSearch(ctx context.Context, q search.Query) (search.Result, error) {
	if q.Scope == "drives" {
		return searchDrives(ctx, q)
	}
	tree, running := m.scanTree()
	if tree == nil {
		return search.Result{}, errors.New("No scan results yet.")
	}
	if running {
		return search.Result{}, errors.New("Search is available when the scan finishes.")
	}
	return tree.search(ctx, q)
}
