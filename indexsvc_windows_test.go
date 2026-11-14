package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// fakeIndexer has one ready drive and never looks at the real ones.
func fakeIndexer(t *testing.T) *driveIndexer {
	idx := search.NewLive()
	idx.Update(func(u search.Updater) {
		u.Set(5, `Q:\`, -1, 0, true)
		u.Set(20, "Films", 5, 0, true)
		u.Set(21, "Holiday.mp4", 20, 900, false)
	})
	loaded := make(chan struct{})
	close(loaded)
	s := &driveStore{root: `Q:\`, key: "q", idx: idx, rootSlot: 5, mode: "live", state: "ready", loaded: loaded,
		done: make(chan struct{}), refresh: make(chan struct{}, 1), cancel: func() {}, synced: time.Now()}
	ix := &driveIndexer{service: true, dir: t.TempDir(), stores: map[string]*driveStore{"q": s}, listed: time.Now().Add(time.Hour), watching: true}
	s.ix = ix
	return ix
}

func ask(t *testing.T, r *bufio.Reader, w net.Conn, req any) indexServiceReply {
	t.Helper()
	line, _ := json.Marshal(req)
	if _, err := w.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	var reply indexServiceReply
	b, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

// The service answers status, search and refresh requests on one connection.
func TestIndexServiceRequests(t *testing.T) {
	ix := fakeIndexer(t)
	client, server := net.Pipe()
	defer client.Close()
	go serveConn(server, ix)
	r := bufio.NewReader(client)

	reply := ask(t, r, client, indexServiceRequest{Op: "status"})
	if reply.Status == nil || reply.Build == "" || len(reply.Status.Drives) != 1 || reply.Status.Drives[0].Mode != "live" || reply.Status.Drives[0].Entries != 3 {
		t.Fatalf("status %+v", reply)
	}
	reply = ask(t, r, client, indexServiceRequest{Op: "search", Query: &search.Query{Text: "holi"}})
	if reply.Error != "" || reply.Result == nil || len(reply.Result.Hits) != 1 || reply.Result.Hits[0].Path != `Q:\Films` {
		t.Fatalf("search %+v", reply)
	}
	if reply = ask(t, r, client, indexServiceRequest{Op: "refresh", Root: `Q:\`}); reply.Error != "" {
		t.Fatalf("refresh %+v", reply)
	}
	if reply = ask(t, r, client, indexServiceRequest{Op: "refresh", Root: `Z:\`}); reply.Error == "" {
		t.Fatal("refreshing a drive that is not indexed")
	}
	if reply = ask(t, r, client, indexServiceRequest{Op: "delete"}); reply.Error == "" {
		t.Fatal("unknown request accepted")
	}
	if reply = ask(t, r, client, "not a request"); reply.Error == "" {
		t.Fatal("bad request accepted")
	}
}

// The pipe's security lets this user in. Whether others are kept out is up to Windows.
func TestIndexPipeOpensForUser(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf(`\\.\pipe\KamkmDirStatIndexTest-%d`, os.Getpid())
	l, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: pipeSecurity(user.User.Sid.String())})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go servePipe(l, fakeIndexer(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := winio.DialPipeContext(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var pid uint32
	if f, ok := conn.(interface{ Fd() uintptr }); !ok {
		t.Fatal("the pipe has no handle to check its server")
	} else if err := windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid); err != nil || pid != uint32(os.Getpid()) {
		t.Fatalf("server process %d, %v", pid, err)
	}
	if reply := ask(t, bufio.NewReader(conn), conn, indexServiceRequest{Op: "search", Query: &search.Query{Kind: "videos"}}); reply.Result == nil || reply.Result.Total != 1 {
		t.Fatalf("over the pipe %+v", reply)
	}
}
