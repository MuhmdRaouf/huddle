package process

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestIsAlive(t *testing.T) {
	t.Run("answers for this process and for one that has exited", func(t *testing.T) {
		if !IsAlive(int64(os.Getpid())) {
			t.Fatal("own pid is not alive")
		}
		child := exec.Command(selfExe(t))
		child.Env = childEnv("three-lines")
		if err := child.Run(); err != nil {
			t.Fatalf("child: %v", err)
		}
		if IsAlive(int64(child.Process.Pid)) {
			t.Fatalf("pid %d is alive after its exit", child.Process.Pid)
		}
	})

	t.Run("refuses pids that name no process", func(t *testing.T) {
		if IsAlive(0) || IsAlive(-1) {
			t.Fatal("0 and -1 must not be probed")
		}
	})
}

func TestSignalGroup(t *testing.T) {
	t.Run("refuses unsafe pids", func(t *testing.T) {
		for _, pid := range []int64{0, -1, 1, int64(os.Getpid())} {
			if SignalGroup(pid, syscall.SIGTERM) {
				t.Fatalf("signalGroup(%d) = true, want false", pid)
			}
		}
	})
}

func TestExitSignalName(t *testing.T) {
	child := exec.Command(selfExe(t))
	child.Env = childEnv("three-lines")
	if err := child.Run(); err != nil {
		t.Fatalf("child: %v", err)
	}
	if got := exitSignalName(child.ProcessState); got != "" {
		t.Fatalf("exitSignalName of a clean exit = %q, want empty", got)
	}
}

func TestSignalName(t *testing.T) {
	if got := signalName(syscall.SIGTERM); got != "SIGTERM" {
		t.Fatalf("signalName(SIGTERM) = %q, want SIGTERM", got)
	}
	if got := signalName(syscall.SIGKILL); got != "SIGKILL" {
		t.Fatalf("signalName(SIGKILL) = %q, want SIGKILL", got)
	}
	if got := signalName(syscall.Signal(99)); got != "SIG99" {
		t.Fatalf("signalName(99) = %q, want SIG99", got)
	}
}
