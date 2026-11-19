package wire

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

func TestWriteThenRead(t *testing.T) {
	var buf bytes.Buffer
	for _, v := range []any{map[string]string{"type": "hello"}, []int{1, 2, 3}} {
		if err := Write(&buf, v); err != nil {
			t.Fatal(err)
		}
	}
	size := int64(buf.Len())
	first, n1, err := Read(&buf)
	if err != nil || string(first) != `{"type":"hello"}` {
		t.Fatalf("first message %q, %v", first, err)
	}
	second, n2, err := Read(&buf)
	if err != nil || string(second) != `[1,2,3]` {
		t.Fatalf("second message %q, %v", second, err)
	}
	if n1+n2 != size {
		t.Fatalf("counted %d bytes of %d", n1+n2, size)
	}
	if _, _, err := Read(&buf); err != io.EOF {
		t.Fatalf("after the last message: %v", err)
	}
}

func TestReadRejectsBadFrames(t *testing.T) {
	for name, frame := range map[string][]byte{
		"empty":     binary.BigEndian.AppendUint32(nil, 0),
		"too large": binary.BigEndian.AppendUint32(nil, maxFrame+1),
		"cut short": append(binary.BigEndian.AppendUint32(nil, 10), 1, 2, 3),
		"not gzip":  append(binary.BigEndian.AppendUint32(nil, 4), 1, 2, 3, 4),
	} {
		if _, _, err := Read(bytes.NewReader(frame)); err == nil {
			t.Errorf("%s frame accepted", name)
		}
	}
}

func TestHeader(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("Welcome to the server\nSSE2 gzip\r\nrest"))
	if got := Header(r, "SSE2 "); got != "gzip" {
		t.Fatalf("header %q", got)
	}
	if rest, _ := io.ReadAll(r); string(rest) != "rest" {
		t.Fatalf("header read past its line: %q", rest)
	}
	if got := Header(bufio.NewReader(strings.NewReader("banner only\n")), "SSE2 "); got != "" {
		t.Fatalf("header %q without a header line", got)
	}
	if got := Header(bufio.NewReader(strings.NewReader(strings.Repeat("x\n", 40<<10)+"SSE2 gzip\n")), "SSE2 "); got != "" {
		t.Fatalf("header %q found after the limit", got)
	}
}
