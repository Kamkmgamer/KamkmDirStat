// Package wire is the framing the app and the programs it runs on a server speak over an
// SSH session: a header line naming the protocol, then messages of a 4-byte big-endian
// length followed by gzip-compressed JSON. remote_report.py and remote_monitor.py write
// the same frames in Python.
package wire

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const (
	maxFrame   = 8 << 20  // compressed
	maxMessage = 32 << 20 // JSON
)

// Read reads one message. It returns the JSON and the number of bytes read from src.
func Read(src io.Reader) ([]byte, int64, error) {
	var length uint32
	if err := binary.Read(src, binary.BigEndian, &length); err != nil {
		return nil, 0, err
	}
	if length == 0 || length > maxFrame {
		return nil, 0, errors.New("The remote report frame is too large.")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(src, payload); err != nil {
		return nil, 0, err
	}
	z, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	defer z.Close()
	data, err := io.ReadAll(io.LimitReader(z, maxMessage+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > maxMessage {
		return nil, 0, errors.New("The remote report is too large.")
	}
	return data, int64(length) + 4, nil
}

// Write sends v as one message.
func Write(dst io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.Write(make([]byte, 4))
	z, _ := gzip.NewWriterLevel(&buf, 3)
	z.Write(data)
	z.Close()
	frame := buf.Bytes()
	binary.BigEndian.PutUint32(frame, uint32(len(frame)-4))
	_, err = dst.Write(frame)
	return err
}

// Header skips any login banner and returns the rest of the first line that starts with
// prefix, or "" if there is none in the first 64 KiB.
func Header(r *bufio.Reader, prefix string) string {
	size := 0
	for size < 64<<10 {
		line, err := r.ReadString('\n')
		size += len(line)
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(rest)
		}
		if err != nil {
			return ""
		}
	}
	return ""
}
