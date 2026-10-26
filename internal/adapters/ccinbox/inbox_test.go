package ccinbox

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tempSocketDir is a short-lived directory for a test's socket: its path stays under the
// 104-byte Unix socket path limit, which t.TempDir()'s deeper tree can pass on macOS.
func tempSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func listen(t *testing.T, dir, name string) (path string, ln net.Listener) {
	t.Helper()
	path = filepath.Join(dir, name)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	t.Cleanup(func() { ln.Close() })
	return path, ln
}

// acceptOnce accepts one connection and sends it to the channel, or sends the accept error.
func acceptOnce(t *testing.T, ln net.Listener) <-chan net.Conn {
	t.Helper()
	accepted := make(chan net.Conn, 1)
	t.Cleanup(func() {
		select {
		case conn := <-accepted:
			conn.Close()
		default:
		}
	})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- conn
	}()
	return accepted
}

func TestPostWritesOneUserLine(t *testing.T) {
	dir := tempSocketDir(t)
	path, ln := listen(t, dir, "one.sock")
	accepted := acceptOnce(t, ln)

	m := Message{SessionID: "5cfe8626-2c6f-4f0d-8ff8-4f7c4a3b9c01", Text: "quote \"this\"\nsecond line — ✓"}
	if err := Post(context.Background(), path, m); err != nil {
		t.Fatalf("Post: %v", err)
	}

	var conn net.Conn
	select {
	case conn = <-accepted:
		if conn == nil {
			t.Fatal("the listener accepted no connection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Post never dialled the socket")
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read the line: %v", err)
	}
	var got wireLine
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	if got.Type != "user" {
		t.Errorf("type = %q, want %q", got.Type, "user")
	}
	if got.SessionID != m.SessionID {
		t.Errorf("session_id = %q, want %q", got.SessionID, m.SessionID)
	}
	if got.Message.Role != "user" {
		t.Errorf("message.role = %q, want %q", got.Message.Role, "user")
	}
	if got.Message.Content != m.Text {
		t.Errorf("message.content = %q, want %q", got.Message.Content, m.Text)
	}

	// Post closed the connection after the one line: the next read is EOF.
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("read after the line = %v, want io.EOF", err)
	}
}

func TestPostSendsTheAuthLineFirst(t *testing.T) {
	dir := tempSocketDir(t)
	path, ln := listen(t, dir, "auth.sock")
	accepted := acceptOnce(t, ln)

	m := Message{SessionID: "5cfe8626-2c6f-4f0d-8ff8-4f7c4a3b9c01", Text: "carry on", Token: "tok-7d21"}
	if err := Post(context.Background(), path, m); err != nil {
		t.Fatalf("Post: %v", err)
	}

	var conn net.Conn
	select {
	case conn = <-accepted:
		if conn == nil {
			t.Fatal("the listener accepted no connection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Post never dialled the socket")
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read the wire: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("the wire carries %d lines (%q), want the auth line then the user line", len(lines), data)
	}
	var auth struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &auth); err != nil {
		t.Fatalf("decode the first line %q: %v", lines[0], err)
	}
	if auth.Type != "auth" || auth.Token != m.Token {
		t.Errorf("the first line = %q, want the auth line naming the session's token", lines[0])
	}
	var got wireLine
	if err := json.Unmarshal([]byte(lines[1]), &got); err != nil {
		t.Fatalf("decode the second line %q: %v", lines[1], err)
	}
	if got.Type != "user" || got.SessionID != m.SessionID || got.Message.Content != m.Text {
		t.Errorf("the second line = %+v, want the user turn", got)
	}
}

func TestPostAnswersNoSocketAsAValue(t *testing.T) {
	dir := tempSocketDir(t)
	path := filepath.Join(dir, "missing.sock")

	err := Post(context.Background(), path, Message{SessionID: "s1", Text: "hello"})
	var noSocket *NoSocket
	if !errors.As(err, &noSocket) {
		t.Fatalf("Post = %v (%T), want a *NoSocket", err, err)
	}
	if noSocket.Path != path {
		t.Errorf("NoSocket.Path = %q, want %q", noSocket.Path, path)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path %q", err, path)
	}
}

func TestPostRefusesATextOverTheLimit(t *testing.T) {
	dir := tempSocketDir(t)
	path, ln := listen(t, dir, "long.sock")
	accepted := make(chan net.Conn, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()

	text := strings.Repeat("x", MaxTextBytes+1)
	err := Post(context.Background(), path, Message{SessionID: "s1", Text: text})
	var tooLong *TooLong
	if !errors.As(err, &tooLong) {
		t.Fatalf("Post = %v (%T), want a *TooLong", err, err)
	}
	if tooLong.Bytes != MaxTextBytes+1 {
		t.Errorf("TooLong.Bytes = %d, want %d", tooLong.Bytes, MaxTextBytes+1)
	}

	select {
	case conn := <-accepted:
		t.Fatalf("Post dialled the socket for an over-limit text; accepted %v", conn)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPostGivesUpAtItsDeadline(t *testing.T) {
	dir := tempSocketDir(t)
	path, ln := listen(t, dir, "slow.sock")
	accepted := acceptOnce(t, ln)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	text := strings.Repeat("x", MaxTextBytes)
	start := time.Now()
	err := Post(ctx, path, Message{SessionID: "s1", Text: text})
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Post after %s = %v, want an error matching context.DeadlineExceeded", elapsed, err)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("Post took %s, want it to give up at the 300 ms deadline", elapsed)
	}

	select {
	case conn := <-accepted:
		if conn == nil {
			t.Fatal("the listener accepted no connection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Post never dialled the socket")
	}
}

func TestPostRefusesAnEmptyText(t *testing.T) {
	dir := tempSocketDir(t)
	path := filepath.Join(dir, "empty.sock")

	err := Post(context.Background(), path, Message{SessionID: "s1", Text: ""})
	var invalid *Invalid
	if !errors.As(err, &invalid) {
		t.Fatalf("Post with an empty text = %v (%T), want an *Invalid", err, err)
	}
	if invalid.Field != "Text" {
		t.Errorf("Invalid.Field = %q, want %q", invalid.Field, "Text")
	}

	err = Post(context.Background(), path, Message{SessionID: "", Text: "hello"})
	invalid = nil
	if !errors.As(err, &invalid) {
		t.Fatalf("Post with an empty session id = %v (%T), want an *Invalid", err, err)
	}
	if invalid.Field != "SessionID" {
		t.Errorf("Invalid.Field = %q, want %q", invalid.Field, "SessionID")
	}
}
