// Package scripts tests the release shell scripts. It has no non-test code.
package scripts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testTag = "v9.9.9"

// ociBuilder writes a multi-platform OCI archive shaped like the one
// `docker buildx build --output type=oci` writes.
type ociBuilder struct {
	blobs map[string][]byte
}

func (b *ociBuilder) add(data []byte) map[string]any {
	sum := sha256.Sum256(data)
	d := "sha256:" + hex.EncodeToString(sum[:])
	b.blobs[d] = data
	return map[string]any{"digest": d, "size": len(data)}
}

func (b *ociBuilder) addJSON(mediaType string, v any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	desc := b.add(data)
	desc["mediaType"] = mediaType
	return desc
}

func tarOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gzipOf(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type platformImage struct {
	user        string
	healthcheck bool
	binary      []byte // nil: no layer has the server binary
}

type archiveOpts struct {
	flatIndex bool // index.json lists the platform manifests itself
}

// writeArchive writes an OCI archive with one image per platform (amd64, arm64).
func writeArchive(t *testing.T, path string, imgs map[string]platformImage, opts archiveOpts) {
	t.Helper()
	b := &ociBuilder{blobs: map[string][]byte{}}
	var manifests []map[string]any
	for _, arch := range []string{"amd64", "arm64"} {
		img, ok := imgs[arch]
		if !ok {
			continue
		}
		cfg := map[string]any{"User": img.user}
		if img.healthcheck {
			cfg["Healthcheck"] = map[string]any{"Test": []string{"CMD", "true"}}
		}
		config := b.addJSON("application/vnd.oci.image.config.v1+json", map[string]any{
			"architecture": arch, "os": "linux", "config": cfg,
		})
		base := b.add(gzipOf(t, tarOf(t, map[string][]byte{"etc/passwd": []byte("root:x:0:0::/root:/sbin/nologin\n")})))
		base["mediaType"] = "application/vnd.oci.image.layer.v1.tar+gzip"
		layers := []map[string]any{base}
		if img.binary != nil {
			top := b.add(gzipOf(t, tarOf(t, map[string][]byte{"usr/local/bin/deploybot-server": img.binary})))
			top["mediaType"] = "application/vnd.oci.image.layer.v1.tar+gzip"
			layers = append(layers, top)
		}
		m := b.addJSON("application/vnd.oci.image.manifest.v1+json", map[string]any{
			"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
			"config": config, "layers": layers,
		})
		m["platform"] = map[string]any{"architecture": arch, "os": "linux"}
		manifests = append(manifests, m)
	}
	index := map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": manifests}
	top := index
	if !opts.flatIndex {
		desc := b.addJSON("application/vnd.oci.image.index.v1+json", index)
		top = map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []any{desc}}
	}
	files := map[string][]byte{"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`)}
	topJSON, _ := json.Marshal(top)
	files["index.json"] = topJSON
	for d, data := range b.blobs {
		files["blobs/sha256/"+strings.TrimPrefix(d, "sha256:")] = data
	}
	if err := os.WriteFile(path, tarOf(t, files), 0o644); err != nil {
		t.Fatal(err)
	}
}

func bundleBinary(arch string) []byte { return []byte("deploybot-server " + arch + "\n") }

func goodServer() map[string]platformImage {
	return map[string]platformImage{
		"amd64": {user: "nonroot:nonroot", binary: bundleBinary("amd64")},
		"arm64": {user: "nonroot:nonroot", binary: bundleBinary("arm64")},
	}
}

func goodConsole() map[string]platformImage {
	return map[string]platformImage{
		"amd64": {user: "node", healthcheck: true},
		"arm64": {user: "node", healthcheck: true},
	}
}

// run writes the two archives and the bundle binaries, runs the script and
// returns its exit code and output.
func run(t *testing.T, server, console map[string]platformImage, opts archiveOpts) (int, string) {
	t.Helper()
	dir := t.TempDir()
	images := filepath.Join(dir, "images")
	dist := filepath.Join(dir, "dist")
	for _, d := range []string{images, dist} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeArchive(t, filepath.Join(images, "server.tar"), server, opts)
	writeArchive(t, filepath.Join(images, "console.tar"), console, opts)
	for _, arch := range []string{"amd64", "arm64"} {
		if err := os.WriteFile(filepath.Join(dist, "deploybot-server-"+testTag+"-linux-"+arch), bundleBinary(arch), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "check-images.sh", testTag, images, dist)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		}
		t.Fatalf("running check-images.sh: %v", err)
	}
	return 0, string(out)
}

func TestCheckImagesPasses(t *testing.T) {
	for _, opts := range []archiveOpts{{}, {flatIndex: true}} {
		code, out := run(t, goodServer(), goodConsole(), opts)
		if code != 0 {
			t.Fatalf("flatIndex=%v: exit %d\n%s", opts.flatIndex, code, out)
		}
		if n := strings.Count(out, "ok: "); n != 8 {
			t.Errorf("flatIndex=%v: %d ok lines, want 8\n%s", opts.flatIndex, n, out)
		}
	}
}

func TestCheckImagesFails(t *testing.T) {
	cases := []struct {
		name    string
		edit    func(server, console map[string]platformImage)
		wantOut string
	}{
		{"server root user", func(s, c map[string]platformImage) {
			i := s["arm64"]
			i.user = "root"
			s["arm64"] = i
		}, `server linux/arm64: runs as root (User "root")`},
		{"server no user", func(s, c map[string]platformImage) {
			i := s["amd64"]
			i.user = ""
			s["amd64"] = i
		}, `server linux/amd64: runs as root (User "")`},
		{"console uid 0", func(s, c map[string]platformImage) {
			i := c["amd64"]
			i.user = "0:0"
			c["amd64"] = i
		}, `console linux/amd64: runs as root (User "0:0")`},
		{"console without healthcheck", func(s, c map[string]platformImage) {
			i := c["arm64"]
			i.healthcheck = false
			c["arm64"] = i
		}, "console linux/arm64: HEALTHCHECK is missing"},
		{"server binary differs", func(s, c map[string]platformImage) {
			i := s["arm64"]
			i.binary = []byte("something else\n")
			s["arm64"] = i
		}, "server linux/arm64: binary differs from deploybot-server-v9.9.9-linux-arm64"},
		{"server binary missing", func(s, c map[string]platformImage) {
			i := s["amd64"]
			i.binary = nil
			s["amd64"] = i
		}, "server linux/amd64: no layer has /usr/local/bin/deploybot-server"},
		{"platform missing", func(s, c map[string]platformImage) {
			delete(s, "arm64")
		}, "server linux/arm64: not exactly one manifest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, console := goodServer(), goodConsole()
			tc.edit(server, console)
			code, out := run(t, server, console, archiveOpts{})
			if code != 1 {
				t.Fatalf("exit %d, want 1\n%s", code, out)
			}
			if !strings.Contains(out, "FAIL: "+tc.wantOut) {
				t.Errorf("output lacks %q\n%s", tc.wantOut, out)
			}
		})
	}
}

func TestCheckImagesUsage(t *testing.T) {
	out, err := exec.Command("bash", "check-images.sh", "0.2.50", "a", "b").CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 || !strings.Contains(string(out), "usage:") {
		t.Fatalf("err %v, output %q", err, out)
	}
}
