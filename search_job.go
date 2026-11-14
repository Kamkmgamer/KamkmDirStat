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

var searchState struct {
	sync.Mutex
	seq        int
	pending    *search.Query
	pendingSeq int
	busy       bool
	cancel     context.CancelFunc
	status     SearchStatus
}

func startSearch(q search.Query) (int, error) {
	if len(q.Text) > 1000 || len(q.Exts) > 50 {
		return 0, errors.New("The search is too long.")
	}
	searchState.Lock()
	defer searchState.Unlock()
	searchState.seq++
	searchState.pending, searchState.pendingSeq = &q, searchState.seq
	if searchState.cancel != nil {
		searchState.cancel()
	}
	if !searchState.busy {
		searchState.busy = true
		go runSearches()
	}
	return searchState.seq, nil
}

func searchStatus() SearchStatus {
	searchState.Lock()
	defer searchState.Unlock()
	return searchState.status
}

func runSearches() {
	for {
		searchState.Lock()
		if searchState.pending == nil {
			searchState.busy = false
			searchState.Unlock()
			return
		}
		q, seq := *searchState.pending, searchState.pendingSeq
		searchState.pending = nil
		ctx, cancel := context.WithCancel(context.Background())
		searchState.cancel = cancel
		searchState.Unlock()

		result, err := runSearch(ctx, q)
		cancel()

		searchState.Lock()
		searchState.cancel = nil
		if searchState.pending == nil || seq == searchState.seq {
			s := SearchStatus{Seq: seq}
			if err != nil {
				s.Error = err.Error()
			} else {
				s.Result = &result
			}
			searchState.status = s
		}
		searchState.Unlock()
	}
}

func runSearch(ctx context.Context, q search.Query) (search.Result, error) {
	if q.Scope == "drives" {
		return searchDrives(ctx, q)
	}
	scanState.Lock()
	tree, running := scanState.tree, scanState.status.Running
	scanState.Unlock()
	if tree == nil {
		return search.Result{}, errors.New("No scan results yet.")
	}
	if running {
		return search.Result{}, errors.New("Search is available when the scan finishes.")
	}
	return tree.search(ctx, q)
}
