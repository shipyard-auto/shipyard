package addon

import (
	"reflect"
	"testing"
)

func TestKind_BinaryName(t *testing.T) {
	cases := []struct {
		kind Kind
		want string
	}{
		{KindComms, "shipyard-comms"},
		{KindCrew, "shipyard-crew"},
		{KindFairway, "shipyard-fairway"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := tc.kind.BinaryName(); got != tc.want {
				t.Errorf("BinaryName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKind_InstallCommand(t *testing.T) {
	cases := []struct {
		kind Kind
		want string
	}{
		{KindComms, "shipyard comms install"},
		{KindCrew, "shipyard crew install"},
		{KindFairway, "shipyard fairway install"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := tc.kind.InstallCommand(); got != tc.want {
				t.Errorf("InstallCommand() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAllKinds_IncludesComms(t *testing.T) {
	want := []Kind{KindComms, KindCrew, KindFairway}
	got := AllKinds()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AllKinds() = %v, want %v", got, want)
	}
}
