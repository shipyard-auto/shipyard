package commsctl

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarGzNamed(t *testing.T, name string, content []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(content)), Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gw.Close()
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func TestProductNames(t *testing.T) {
	pl := Platform{OS: "linux", Arch: "arm64"}
	if got := WhatsApp.ReleaseTag("0.3.0"); got != "comms-whatsapp-v0.3.0" {
		t.Errorf("tag=%s", got)
	}
	if got := WhatsApp.ArtifactName("0.3.0", pl); got != "shipyard-comms-whatsapp_0.3.0_linux_arm64.tar.gz" {
		t.Errorf("artifact=%s", got)
	}
	if got := WhatsApp.ChecksumManifestName("0.3.0"); got != "shipyard-comms-whatsapp_0.3.0_checksums.txt" {
		t.Errorf("checksums=%s", got)
	}
	// The package-level helpers stay the comms ones.
	if ArtifactName("1.0.0", pl) != Comms.ArtifactName("1.0.0", pl) || ReleaseTag("1.0.0") != "comms-v1.0.0" {
		t.Error("comms helpers changed")
	}
}

func TestInstallWhatsAppProduct(t *testing.T) {
	inst, binDir := newTestInstaller(t, "0.3.0")
	inst.Product = WhatsApp
	tarBytes, sha := tarGzNamed(t, WhatsApp.Binary, []byte("#!/bin/sh\necho 0.3.0\n"))
	artifact := WhatsApp.ArtifactName("0.3.0", inst.Platform)
	base := inst.ReleaseBase + "/comms-whatsapp-v0.3.0/"
	inst.HTTPClient = &fakeHTTPClient{responses: map[string]func() *http.Response{
		base + artifact: okResponse(tarBytes),
		base + WhatsApp.ChecksumManifestName("0.3.0"): okResponse([]byte(fmt.Sprintf("%s  %s\n", sha, artifact))),
	}}
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	if inst.BinPath() != filepath.Join(binDir, "shipyard-comms-whatsapp") {
		t.Fatalf("bin path=%s", inst.BinPath())
	}
	if _, err := os.Stat(inst.BinPath()); err != nil {
		t.Fatal(err)
	}
	if v, err := inst.InstalledVersion(); err != nil || v != "0.3.0" {
		t.Fatalf("installed version=%q err=%v", v, err)
	}
	// A comms archive must not satisfy a whatsapp install.
	if err := ExtractNamedBinary(writeTemp(t, tarBytes), filepath.Join(t.TempDir(), "x"), "shipyard-comms"); err == nil {
		t.Fatal("extracted the wrong binary")
	}
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCommsPurgeKeepsProviderState(t *testing.T) {
	inst, _ := newTestInstaller(t, "0.3.0")
	for _, f := range []string{"channels.json", "secrets/abc.json", "whatsapp/session.db", "whatsapp/archive.db"} {
		p := filepath.Join(inst.StateDir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inst.Purge = true
	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"channels.json", "secrets"} {
		if _, err := os.Stat(filepath.Join(inst.StateDir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived the comms purge", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(inst.StateDir, "whatsapp", "session.db")); err != nil {
		t.Fatalf("comms purge deleted the WhatsApp session: %v", err)
	}

	// Purging the WhatsApp product removes its own directory.
	wa, _ := newTestInstaller(t, "0.3.0")
	wa.Product, wa.Purge = WhatsApp, true
	wa.StateDir = filepath.Join(inst.StateDir, "whatsapp")
	if err := wa.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wa.StateDir); !os.IsNotExist(err) {
		t.Fatal("whatsapp purge left its state")
	}
}

func TestResolveLatestVersionPrefixesAreDisjoint(t *testing.T) {
	body := `[{"tag_name":"comms-whatsapp-v0.3.0"},{"tag_name":"comms-v0.4.0-rc1","prerelease":true},{"tag_name":"comms-v0.3.0"},{"tag_name":"crew-v0.7.0"}]`
	client := &fakeHTTPClient{responses: map[string]func() *http.Response{
		commsReleasesAPI: func() *http.Response {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
		},
	}}
	if v, err := ResolveLatestVersion(context.Background(), client, Comms); err != nil || v != "0.3.0" {
		t.Fatalf("comms latest=%q err=%v (must skip the whatsapp tag and the prerelease)", v, err)
	}
	if v, err := ResolveLatestVersion(context.Background(), client, WhatsApp); err != nil || v != "0.3.0" {
		t.Fatalf("whatsapp latest=%q err=%v", v, err)
	}
	if !strings.Contains(commsReleasesAPI, "per_page=100") {
		t.Fatal("release discovery must look past the 30 newest releases")
	}
}
