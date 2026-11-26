package spec

import (
	"reflect"
	"testing"
)

func env(entries ...any) map[string]any { return map[string]any{"env": entries} }

func TestRemoveEnv(t *testing.T) {
	m := env("A=1", "V3COMPAT_NATIVE=1", "V3COMPAT_NATIVE_X=1", "V3COMPAT_NATIVE", "B=V3COMPAT_NATIVE=1", "V3COMPAT_NATIVE=0", "V3COMPAT_AUDIT=1")
	got, err := removeEnv(m, "V3COMPAT_NATIVE", "V3COMPAT_AUDIT", "V3COMPAT_CACHE")
	if err != nil || !reflect.DeepEqual(got, []string{"V3COMPAT_NATIVE", "V3COMPAT_AUDIT"}) {
		t.Fatal(got, err)
	}
	want := []any{"A=1", "V3COMPAT_NATIVE_X=1", "B=V3COMPAT_NATIVE=1"}
	if !reflect.DeepEqual(m["env"], want) {
		t.Fatalf("%v", m["env"])
	}
}

func TestRemoveEnvNothingToRemove(t *testing.T) {
	m := env("A=1")
	if got, err := removeEnv(m, "B"); got != nil || err != nil || !reflect.DeepEqual(m["env"], []any{"A=1"}) {
		t.Fatal(got, err, m)
	}
	m = map[string]any{}
	if got, err := removeEnv(m, "B"); got != nil || err != nil {
		t.Fatal(got, err)
	}
	if _, ok := m["env"]; ok {
		t.Fatal("env created")
	}
}

func TestEnvNonStringFailsClosed(t *testing.T) {
	for name, f := range map[string]func(map[string]any) error{
		"remove": func(m map[string]any) error { _, err := removeEnv(m, "A"); return err },
		"set":    func(m map[string]any) error { return setEnv(m, "A", "1") },
	} {
		m := env("B=1", 5)
		if err := f(m); err == nil {
			t.Errorf("%s: no error", name)
		}
		if !reflect.DeepEqual(m["env"], []any{"B=1", 5}) {
			t.Errorf("%s: modified %v", name, m["env"])
		}
	}
}

func TestSetEnv(t *testing.T) {
	m := env("A=1", "K=old", "K=older", "K")
	if err := setEnv(m, "K", "new"); err != nil || !reflect.DeepEqual(m["env"], []any{"A=1", "K=new"}) {
		t.Fatal(err, m)
	}
	m = map[string]any{}
	if err := setEnv(m, "K", "v"); err != nil || !reflect.DeepEqual(m["env"], []any{"K=v"}) {
		t.Fatal(err, m)
	}
	m = env("K_X=1")
	if err := setEnv(m, "K", "v"); err != nil || !reflect.DeepEqual(m["env"], []any{"K_X=1", "K=v"}) {
		t.Fatal(err, m)
	}
}
