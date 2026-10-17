package process

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The suite re-executes this test binary as its children, with childModeEnv naming the role the
// child plays (runChildMode). No other runtime is ever started.
const childModeEnv = "PROCESS_TEST_CHILD"

// childDeadline bounds every child the tests start, so one that escapes its test's cleanup cannot linger.
const childDeadline = 60 * time.Second

func TestMain(m *testing.M) {
	if mode := os.Getenv(childModeEnv); mode != "" {
		runChildMode(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// holdOpen keeps the child alive until its deadline.
func holdOpen() {
	time.Sleep(childDeadline)
}

func runChildMode(mode string) {
	switch mode {
	case "upper-stdin":
		input, _ := readAllStdin()
		os.Stderr.WriteString(strings.Repeat("x", 5000) + "END")
		os.Stdout.WriteString(strings.ToUpper(input) + "\n")
		os.Exit(3)
	case "three-lines":
		os.Stdout.WriteString("one\ntwo\nthree")
	case "lines-then-hold":
		os.Stdout.WriteString("one\ntwo\nthree")
		holdOpen()
	case "sleep-forever":
		holdOpen()
	}
}

func readAllStdin() (string, error) {
	data := make([]byte, 0, 512)
	buf := make([]byte, 512)
	for {
		n, err := os.Stdin.Read(buf)
		data = append(data, buf[:n]...)
		if err != nil {
			return string(data), err
		}
	}
}

// childEnv is the child's environment: the child mode, plus any extra assignments. A coverage
// run's GOCOVERDIR carries over, so the child's coverage lands in the run's profile.
func childEnv(mode string, extra ...string) []string {
	env := append([]string{childModeEnv + "=" + mode}, extra...)
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		env = append(env, "GOCOVERDIR="+dir)
	}
	return env
}

func selfExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

func waitFor(t *testing.T, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !probe() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
