package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// The ports, volumes and files item schemas name every field, so an agent
// does not have to guess the shape.
func TestSpecMountSchemas(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(catalogWriteSchema), &schema); err != nil {
		t.Fatal(err)
	}
	specProps := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)
	want := map[string][]string{
		"ports":   {"host", "container", "public"},
		"volumes": {"name", "target", "read_only"},
		"files":   {"file", "target"},
	}
	for field, keys := range want {
		prop, _ := specProps[field].(map[string]any)
		if prop == nil {
			t.Fatalf("%s missing", field)
		}
		if desc, _ := prop["description"].(string); desc == "" {
			t.Fatalf("%s has no description", field)
		}
		items, _ := prop["items"].(map[string]any)
		if items == nil || items["additionalProperties"] != false {
			t.Fatalf("%s items %#v", field, items)
		}
		props, _ := items["properties"].(map[string]any)
		if len(props) != len(keys) {
			t.Fatalf("%s item keys %#v", field, props)
		}
		for _, key := range keys {
			if _, ok := props[key]; !ok {
				t.Fatalf("%s item lacks %s", field, key)
			}
		}
	}
	ports := specProps["ports"].(map[string]any)
	if desc := ports["description"].(string); !strings.Contains(desc, "127.0.0.1") || !strings.Contains(desc, "0.0.0.0") {
		t.Fatalf("ports description does not say how binding is chosen: %s", desc)
	}
	// check_readiness shares the spec schema.
	if !strings.Contains(checkReadinessSchema, `"read_only"`) {
		t.Fatal("check_readiness spec lacks the volumes item schema")
	}
}

func TestSpecMountsAccepted(t *testing.T) {
	for _, spec := range []string{
		`{"kind":"service","image":"nginx","ports":[{"host":8080,"container":80}]}`,
		`{"kind":"service","image":"nginx","ports":[{"host":80,"container":80,"public":true},{"host":443,"container":443,"public":false}]}`,
		`{"kind":"service","image":"nginx","ports":null,"volumes":null,"files":null}`,
		`{"kind":"service","image":"nginx","ports":[],"volumes":[],"files":[]}`,
		`{"kind":"service","image":"nginx","volumes":[{"name":"data","target":"/var/lib/app","read_only":true}]}`,
		`{"kind":"service","image":"nginx","volumes":[{"name":"root","target":"/"}]}`,
		`{"kind":"service","image":"nginx","volumes":[{"name":"data","target":"/data"}],"files":[{"file":"app.yaml","target":"/etc/app/app.yaml"}]}`,
		`{"kind":"service","image":"nginx","volumes":[{"name":"data","target":"/data"}],"files":[{"file":"app.yaml","target":"/data2/app.yaml"}]}`,
	} {
		if err := validateImageSpec(json.RawMessage(spec)); err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
	}
}

func TestSpecMountsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, spec string
		want       []string
	}{
		{"port string", `{"ports":["80:80"]}`, []string{"ports[0] is a string", `"host": 1-65535`, "127.0.0.1", "0.0.0.0"}},
		{"ports object", `{"ports":{"host":80}}`, []string{"ports must be an array", `"container"`}},
		{"host_ip key", `{"ports":[{"host":80,"container":80,"host_ip":"0.0.0.0"}]}`, []string{"ports[0].host_ip is not a field", "public true binds 0.0.0.0"}},
		{"odd key", `{"ports":[{"host":80,"container":80,"Bind":"x"}]}`, []string{"ports[0] has an unknown field", `"public": false`}},
		{"missing container", `{"ports":[{"host":80}]}`, []string{"ports[0] needs host and container"}},
		{"host range", `{"ports":[{"host":0,"container":80}]}`, []string{"ports[0].host must be an integer from 1 to 65535"}},
		{"container float", `{"ports":[{"host":80,"container":80.5}]}`, []string{"ports[0].container must be an integer"}},
		{"public string", `{"ports":[{"host":80,"container":80,"public":"yes"}]}`, []string{"ports[0].public must be true or false"}},
		{"dup host", `{"ports":[{"host":80,"container":80},{"host":80,"container":81}]}`, []string{"ports[1].host repeats"}},
		{"too many ports", `{"ports":[` + strings.TrimSuffix(strings.Repeat(`{"host":1,"container":1},`, 17), ",") + `]}`, []string{"ports allows at most 16"}},
		{"volume string", `{"volumes":["data:/data"]}`, []string{"volumes[0] is a string", `"target": "/var/lib/app"`}},
		{"volume source key", `{"volumes":[{"source":"data","target":"/data"}]}`, []string{"volumes[0].source is not a field"}},
		{"volume name", `{"volumes":[{"name":"Data","target":"/data"}]}`, []string{"volumes[0].name must be lowercase"}},
		{"volume target", `{"volumes":[{"name":"data","target":"data"}]}`, []string{"volumes[0].target must be an absolute path"}},
		{"volume dotdot", `{"volumes":[{"name":"data","target":"/a/../b"}]}`, []string{"volumes[0].target"}},
		{"volume dup", `{"volumes":[{"name":"a","target":"/d"},{"name":"b","target":"/d"}]}`, []string{"volumes[1].target repeats"}},
		{"volume ro", `{"volumes":[{"name":"a","target":"/d","read_only":1}]}`, []string{"volumes[0].read_only must be true or false"}},
		{"file path key", `{"files":[{"file":"a.yaml","path":"/etc/a"}]}`, []string{"files[0].path is not a field", "request_file"}},
		{"file name", `{"files":[{"file":"A.yaml","target":"/etc/a"}]}`, []string{"files[0].file must be a project file name"}},
		{"file trailing slash", `{"files":[{"file":"a.yaml","target":"/etc/a/"}]}`, []string{"files[0].target"}},
		{"file proc", `{"files":[{"file":"a.yaml","target":"/proc/x"}]}`, []string{"files[0].target", "/proc"}},
		{"file dup", `{"files":[{"file":"a.yaml","target":"/a"},{"file":"a.yaml","target":"/b"}]}`, []string{"files[1].file repeats"}},
		{"file overlap", `{"volumes":[{"name":"d","target":"/data/"}],"files":[{"file":"a.yaml","target":"/data/a"}]}`, []string{"files[0].target overlaps volumes[0].target"}},
		{"file over root volume", `{"volumes":[{"name":"d","target":"/"}],"files":[{"file":"a.yaml","target":"/etc/a"}]}`, []string{"overlaps volumes[0]"}},
	} {
		err := validateImageSpec(json.RawMessage(tc.spec))
		if err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: %q lacks %q", tc.name, err, want)
			}
		}
	}
}

// No message echoes a value the caller sent.
func TestSpecMountsDoNotEchoValues(t *testing.T) {
	for _, spec := range []string{
		`{"ports":["s3cr3t-value"]}`,
		`{"ports":[{"host":80,"container":80,"S3cr3t-value":1}]}`,
		`{"volumes":[{"name":"s3cr3t-value!","target":"/d"}]}`,
		`{"volumes":[{"name":"d","target":"s3cr3t-value"}]}`,
		`{"files":[{"file":"s3cr3t-value!","target":"/a"}]}`,
		`{"files":[{"file":"a","target":"/proc/s3cr3t-value"}]}`,
	} {
		err := validateImageSpec(json.RawMessage(spec))
		if err == nil || strings.Contains(strings.ToLower(err.Error()), "s3cr3t") {
			t.Fatalf("%s: %v", spec, err)
		}
	}
}
