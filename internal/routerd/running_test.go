package routerd

// RunningOf on its own: the router status and the hub's health answer name is the record's, but
// only while its pid lives and its port answers the health path.

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
)

func TestRunningOfNamesOnlyALiveAnsweringRouter(t *testing.T) {
	t.Parallel()
	t.Run("no record is no router", func(t *testing.T) {
		t.Parallel()
		if _, ok := RunningOf(context.Background(), startEnv(t)); ok {
			t.Error("RunningOf with no record = ok, want false")
		}
	})
	t.Run("a live pid whose port answers is the router", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusOK, healthBody("zai"))
		port, _ := strconv.Atoi(env["HUDDLE_ROUTER_PORT"])
		writeStartRecord(t, env, record{PID: os.Getpid(), Port: port, Version: "test", Started: "now"})
		got, ok := RunningOf(context.Background(), env)
		if !ok || got.PID != os.Getpid() || got.Port != port || got.Version != "test" || got.Components != nil {
			t.Errorf("RunningOf = %+v, %v, want pid %d on port %d with no components", got, ok, os.Getpid(), port)
		}
	})
	t.Run("a dead pid is no router", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusOK, healthBody("zai"))
		port, _ := strconv.Atoi(env["HUDDLE_ROUTER_PORT"])
		writeStartRecord(t, env, record{PID: int(deadPid(t)), Port: port, Started: "now"})
		if _, ok := RunningOf(context.Background(), env); ok {
			t.Error("RunningOf over a dead pid = ok, want false")
		}
	})
	t.Run("a live pid whose port answers no router is no router", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusNotFound, "")
		port, _ := strconv.Atoi(env["HUDDLE_ROUTER_PORT"])
		writeStartRecord(t, env, record{PID: os.Getpid(), Port: port, Started: "now"})
		if _, ok := RunningOf(context.Background(), env); ok {
			t.Error("RunningOf over a port with no router = ok, want false")
		}
	})
}
