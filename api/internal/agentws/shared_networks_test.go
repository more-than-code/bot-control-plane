package agentws

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.25 item 2: hello.programs.shared_networks bounds.
func TestParseProgramsSharedNetworks(t *testing.T) {
	const base = `{"enabled":true,"run_as":["joe"],"allow_shell":false,"policy_hash":"abc","images":true,"docker":true,"projects":["daikou","defect-drainer"]}`
	with := func(nets string) json.RawMessage {
		return json.RawMessage(base[:len(base)-1] + `,"shared_networks":` + nets + `}`)
	}

	absent, ok := parsePrograms(json.RawMessage(base))
	if !ok || absent == nil || absent.SharedNetworks != nil {
		t.Fatalf("absent %#v %v", absent, ok)
	}
	nulled, ok := parsePrograms(with(`null`))
	if !ok || nulled == nil || nulled.SharedNetworks != nil {
		t.Fatalf("null %#v %v", nulled, ok)
	}
	empty, ok := parsePrograms(with(`[]`))
	if !ok || empty == nil || empty.SharedNetworks == nil || len(empty.SharedNetworks) != 0 {
		t.Fatalf("empty %#v %v", empty, ok)
	}
	on, ok := parsePrograms(with(`[{"name":"dd","subnet":"10.200.10.0/24","projects":["daikou","defect-drainer"]},{"name":"lone","subnet":"192.168.40.16/28","projects":null},{"name":"big","subnet":"172.16.0.0/16","projects":[]}]`))
	if !ok || on == nil || len(on.SharedNetworks) != 3 {
		t.Fatalf("on %#v %v", on, ok)
	}
	if n := on.SharedNetworks[0]; n.Name != "dd" || n.Subnet != "10.200.10.0/24" || len(n.Projects) != 2 {
		t.Fatalf("dd %#v", n)
	}
	if on.SharedNetworks[1].Projects == nil || on.SharedNetworks[2].Projects == nil {
		t.Fatal("member list left nil")
	}

	nine := make([]string, 9)
	for i := range nine {
		nine[i] = fmt.Sprintf(`{"name":"n%d","subnet":"10.200.%d.0/24","projects":[]}`, i, i)
	}
	members := make([]string, 33)
	for i := range members {
		members[i] = fmt.Sprintf(`"p%d"`, i)
	}
	bad := map[string]string{
		"not array":       `{"dd":"10.200.10.0/24"}`,
		"nine networks":   "[" + strings.Join(nine, ",") + "]",
		"33 members":      `[{"name":"dd","subnet":"10.200.10.0/24","projects":[` + strings.Join(members, ",") + `]}]`,
		"bad name":        `[{"name":"DD","subnet":"10.200.10.0/24","projects":[]}]`,
		"long name":       `[{"name":"` + strings.Repeat("a", 33) + `","subnet":"10.200.10.0/24","projects":[]}]`,
		"duplicate name":  `[{"name":"dd","subnet":"10.200.10.0/24","projects":[]},{"name":"dd","subnet":"10.200.11.0/24","projects":[]}]`,
		"bad slug":        `[{"name":"dd","subnet":"10.200.10.0/24","projects":["Daikou"]}]`,
		"duplicate slug":  `[{"name":"dd","subnet":"10.200.10.0/24","projects":["daikou","daikou"]}]`,
		"public subnet":   `[{"name":"dd","subnet":"8.8.8.0/24","projects":[]}]`,
		"host bits":       `[{"name":"dd","subnet":"10.200.10.1/24","projects":[]}]`,
		"prefix 15":       `[{"name":"dd","subnet":"10.200.0.0/15","projects":[]}]`,
		"prefix 29":       `[{"name":"dd","subnet":"10.200.10.0/29","projects":[]}]`,
		"ipv6":            `[{"name":"dd","subnet":"fd00::/64","projects":[]}]`,
		"not cidr":        `[{"name":"dd","subnet":"10.200.10.0","projects":[]}]`,
		"leading zero":    `[{"name":"dd","subnet":"10.200.010.0/24","projects":[]}]`,
		"unknown key":     `[{"name":"dd","subnet":"10.200.10.0/24","projects":[],"driver":"bridge"}]`,
		"missing subnet":  `[{"name":"dd","projects":[]}]`,
		"172 outside /12": `[{"name":"dd","subnet":"172.32.0.0/16","projects":[]}]`,
	}
	for name, nets := range bad {
		if p, ok := parsePrograms(with(nets)); ok {
			t.Fatalf("%s accepted %#v", name, p)
		}
	}
}

// Accepted boundaries and more refusals (agent-protocol v1.13 item 1), and
// validSharedNetworks does not change its input (the null → [] read is in
// parsePrograms).
func TestSharedNetworksBoundaries(t *testing.T) {
	const base = `{"enabled":true,"run_as":["joe"],"allow_shell":false,"policy_hash":"abc","images":true,"docker":true}`
	with := func(nets string) json.RawMessage {
		return json.RawMessage(base[:len(base)-1] + `,"shared_networks":` + nets + `}`)
	}
	eight := make([]string, 8)
	for i := range eight {
		eight[i] = fmt.Sprintf(`{"name":"n%d","subnet":"10.200.%d.0/24","projects":[]}`, i, i)
	}
	members := make([]string, 32)
	for i := range members {
		members[i] = fmt.Sprintf(`"p%d"`, i)
	}
	if p, ok := parsePrograms(with("[" + strings.Join(eight, ",") + "]")); !ok || len(p.SharedNetworks) != 8 {
		t.Fatalf("8 networks %#v %v", p, ok)
	}
	if p, ok := parsePrograms(with(`[{"name":"dd","subnet":"10.200.10.0/24","projects":[` + strings.Join(members, ",") + `]}]`)); !ok || len(p.SharedNetworks[0].Projects) != 32 {
		t.Fatalf("32 members %#v %v", p, ok)
	}
	for name, nets := range map[string]string{
		"projects string":   `[{"name":"dd","subnet":"10.200.10.0/24","projects":"daikou"}]`,
		"non-string member": `[{"name":"dd","subnet":"10.200.10.0/24","projects":[1]}]`,
		"missing name":      `[{"subnet":"10.200.10.0/24","projects":[]}]`,
		"numeric name":      `[{"name":7,"subnet":"10.200.10.0/24","projects":[]}]`,
		"0.0.0.0/0":         `[{"name":"dd","subnet":"0.0.0.0/0","projects":[]}]`,
		"10.0.0.0/8":        `[{"name":"dd","subnet":"10.0.0.0/8","projects":[]}]`,
		"100.64.0.0/16":     `[{"name":"dd","subnet":"100.64.0.0/16","projects":[]}]`,
		"192.168.0.0/29":    `[{"name":"dd","subnet":"192.168.0.0/29","projects":[]}]`,
	} {
		if p, ok := parsePrograms(with(nets)); ok {
			t.Fatalf("%s accepted %#v", name, p)
		}
	}
	in := []store.SharedNetwork{{Name: "dd", Subnet: "10.200.10.0/24"}}
	if !validSharedNetworks(in) || in[0].Projects != nil {
		t.Fatalf("validSharedNetworks changed its input: %#v", in)
	}
}
