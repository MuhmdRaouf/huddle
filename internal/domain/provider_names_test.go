package domain

import (
	"reflect"
	"testing"
)

func TestProviderNames(t *testing.T) {
	t.Run("lists the five providers in the order lanes and lists name them", func(t *testing.T) {
		want := []string{"zai", "kimi", "deepseek", "minimax", "qwen"}
		if got := ProviderNames(); !reflect.DeepEqual(got, want) {
			t.Fatalf("ProviderNames() = %v, want %v", got, want)
		}
	})

	t.Run("one call's answer belongs to that call alone", func(t *testing.T) {
		first := ProviderNames()
		first[0] = "changed"
		second := ProviderNames()
		want := []string{"zai", "kimi", "deepseek", "minimax", "qwen"}
		if !reflect.DeepEqual(second, want) {
			t.Fatalf("after the first slice changes, ProviderNames() = %v, want %v", second, want)
		}
	})
}
