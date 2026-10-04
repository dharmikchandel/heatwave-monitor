package config

import (
	"testing"
	"time"
)

func TestString(t *testing.T) {
	t.Setenv("X_STR", "v")
	if String("X_STR", "d") != "v" || String("X_UNSET_STR", "d") != "d" {
		t.Error("String did not honour value/fallback")
	}
	t.Setenv("X_EMPTY", "")
	if String("X_EMPTY", "d") != "d" {
		t.Error("empty value should fall back")
	}
}

func TestInt(t *testing.T) {
	t.Setenv("X_INT", "42")
	t.Setenv("X_BAD_INT", "nope")
	if Int("X_INT", 1) != 42 || Int("X_BAD_INT", 7) != 7 || Int("X_UNSET_INT", 9) != 9 {
		t.Error("Int did not honour value/fallback")
	}
}

func TestDuration(t *testing.T) {
	t.Setenv("X_DUR", "15m")
	t.Setenv("X_BAD_DUR", "soon")
	if Duration("X_DUR", time.Second) != 15*time.Minute || Duration("X_BAD_DUR", time.Second) != time.Second || Duration("X_UNSET_DUR", time.Hour) != time.Hour {
		t.Error("Duration did not honour value/fallback")
	}
}

func TestFloatAndBool(t *testing.T) {
	t.Setenv("X_FLOAT", "0.25")
	t.Setenv("X_BAD_FLOAT", "lots")
	if Float("X_FLOAT", 1) != 0.25 || Float("X_BAD_FLOAT", 2) != 2 || Float("X_UNSET_FLOAT", 3) != 3 {
		t.Error("Float did not honour value/fallback")
	}
	t.Setenv("X_BOOL", "false")
	t.Setenv("X_BAD_BOOL", "maybe")
	if Bool("X_BOOL", true) || !Bool("X_BAD_BOOL", true) || Bool("X_UNSET_BOOL", false) {
		t.Error("Bool did not honour value/fallback")
	}
}
