package db

import (
	"reflect"
	"testing"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.25 item 2: shared_networks survives the jsonb round trip, an
// empty member list stays [], and a row without the key reads as null.
func TestProgramsCodecSharedNetworks(t *testing.T) {
	in := &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, SharedNetworks: []store.SharedNetwork{
		{Name: "dd", Subnet: "10.200.10.0/24", Projects: []string{"daikou"}},
		{Name: "lone", Subnet: "10.200.11.0/24", Projects: []string{}},
	}}
	raw, err := programsJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := decodePrograms(raw.([]byte))
	if err != nil || !reflect.DeepEqual(out.SharedNetworks, in.SharedNetworks) {
		t.Fatalf("round trip %#v %v", out, err)
	}
	old, err := decodePrograms([]byte(`{"enabled":true,"run_as":[]}`))
	if err != nil || old.SharedNetworks != nil {
		t.Fatalf("old row %#v %v", old, err)
	}
}
