package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
)

// Deleting a file or folder from the scan's list or map: to the Recycle Bin (the Trash on
// macOS and Linux), or for good. Only items of this computer's finished scan can be deleted,
// never a scanned folder or drive itself, and never through a link.

type DeleteRequest struct {
	Target    string `json:"target,omitempty"`
	Dir       int    `json:"dir"`
	Name      string `json:"name"`
	Permanent bool   `json:"permanent,omitempty"`
}

type DeleteStatus struct {
	Seq       int    `json:"seq"`
	Running   bool   `json:"running"`
	Name      string `json:"name,omitempty"`
	Path      string `json:"path,omitempty"`
	Permanent bool   `json:"permanent,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Freed     int64  `json:"freed,omitempty"`
	Error     string `json:"error,omitempty"`
}

var deleteState struct {
	sync.Mutex
	status DeleteStatus
}

// trash is moveToTrash; tests replace it.
var trash = moveToTrash

func startDelete(req DeleteRequest) (int, error) {
	if req.Target == "remote" {
		return 0, errors.New("Files on a server cannot be deleted from here.")
	}
	if req.Name == "" {
		return 0, errors.New("Choose a file or folder inside the scan to delete.")
	}
	tree, err := finishedLocalTree()
	if err != nil {
		return 0, err
	}
	it, err := tree.item(req.Dir, req.Name)
	if err != nil {
		return 0, err
	}
	roots := tree.scanRoots()
	if err := checkRemovable(roots, it.path); err != nil {
		return 0, err
	}
	deleteState.Lock()
	defer deleteState.Unlock()
	if deleteState.status.Running {
		return 0, fmt.Errorf("Still deleting %s.", deleteState.status.Name)
	}
	seq := deleteState.status.Seq + 1
	deleteState.status = DeleteStatus{Seq: seq, Running: true, Name: it.name, Path: it.path, Permanent: req.Permanent, Size: it.size}
	go func() {
		s := DeleteStatus{Seq: seq, Name: it.name, Path: it.path, Permanent: req.Permanent, Size: it.size}
		var err error
		if req.Permanent {
			s.Freed, err = removeTree(context.Background(), cleanTarget{kind: removePath, path: it.path, size: it.size})
		} else if err = trash(it.path); err == nil {
			s.Freed = it.size
		}
		if _, statErr := os.Lstat(it.path); err == nil || os.IsNotExist(statErr) {
			tree.forget(req.Dir, req.Name, it.path, it.size, it.files)
			err = nil
		}
		if err != nil {
			s.Error = err.Error()
		}
		deleteState.Lock()
		deleteState.status = s
		deleteState.Unlock()
	}()
	return seq, nil
}

func deleteStatus() DeleteStatus {
	deleteState.Lock()
	defer deleteState.Unlock()
	return deleteState.status
}
