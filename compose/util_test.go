package compose

import (
	"reflect"
	"testing"
)

func TestMergeEnv_KeyOnlyPreservedAndOverridden(t *testing.T) {
	got := mergeEnv(
		[]string{"A", "B=2"},
		[]string{"A=1", "C"},
	)
	want := []string{"A=1", "B=2", "C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
}

func TestMergeEnv_KeyOnlyOverrideToNoValue(t *testing.T) {
	got := mergeEnv(
		[]string{"A=1"},
		[]string{"A"},
	)
	want := []string{"A"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
}
