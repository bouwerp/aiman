package images_test

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	harnessscanner "github.com/bouwerp/aiman/build/harness-scanner"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/images"
	"github.com/bouwerp/aiman/internal/harness/images/imagestest"
)

// The Docker tests run with AIMAN_TEST_DOCKER=1. They build a static aiman
// binary for the scanner container and the scanner image from
// build/harness-scanner (Trivy, ClamAV, YARA, osv-scanner; a network is
// needed the first time). They use no scanner database downloads: ClamAV gets
// a one-signature database holding EICAR's hash.

var (
	dockerOnce   sync.Once
	dockerTmp    string
	aimanBin     string
	scannerImage string
	dockerErr    error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if dockerTmp != "" {
		_ = os.RemoveAll(dockerTmp)
	}
	os.Exit(code)
}

func needDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("AIMAN_TEST_DOCKER") != "1" {
		t.Skip("set AIMAN_TEST_DOCKER=1 to run Docker tests")
	}
	dockerOnce.Do(func() {
		if dockerTmp, dockerErr = os.MkdirTemp("", "aiman-scan-test-"); dockerErr != nil {
			return
		}
		aimanBin = filepath.Join(dockerTmp, "aiman")
		cmd := exec.Command("go", "build", "-o", aimanBin, "./cmd/aiman")
		cmd.Dir = filepath.Join("..", "..", "..")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			dockerErr = &buildErr{string(out)}
			return
		}
		si := &images.ScannerImage{Docker: images.ExecDockerCLI, Dockerfile: harnessscanner.Dockerfile, Dir: dockerTmp}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		scannerImage, dockerErr = si.Ensure(ctx)
	})
	if dockerErr != nil {
		t.Fatal(dockerErr)
	}
}

type buildErr struct{ out string }

func (e *buildErr) Error() string { return "build aiman: " + e.out }

// eicar is the EICAR test file, assembled so this source holds no copy.
func eicar() string {
	return `X5O!P%@AP[4\PZX54(P^)7CC)7}$` + "EICAR-STANDARD-ANTIVIRUS" + `-TEST-FILE!$H+H*`
}

// scanDB is a database directory whose ClamAV database knows only EICAR.
func scanDB(t *testing.T) string {
	t.Helper()
	db := t.TempDir()
	if err := os.MkdirAll(filepath.Join(db, "clamav"), 0o700); err != nil {
		t.Fatal(err)
	}
	sig := "44d88612fea8a8f36de82e1278abb02f:68:Eicar-Test-Signature\n"
	if err := os.WriteFile(filepath.Join(db, "clamav", "test.hdb"), []byte(sig), 0o600); err != nil {
		t.Fatal(err)
	}
	return db
}

// quarantine fetches img into a layout through the fake registry.
func quarantine(t *testing.T, img imagestest.Image) string {
	t.Helper()
	reg := imagestest.NewRegistry()
	reg.Tag("o/x:1", img)
	res, err := reg.Resolve(context.Background(), "o/x:1", images.HostPlatform())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "q")
	if err := images.Fetch(context.Background(), reg, res, dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func dockerScanner(t *testing.T, image, db string) images.DockerScanner {
	return images.DockerScanner{Image: image, AimanBin: aimanBin, DBDir: db, ScratchDir: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}
}

func staticScan(t *testing.T, img imagestest.Image) images.StageResult {
	t.Helper()
	s := dockerScanner(t, scannerImage, scanDB(t))
	res, err := s.Scan(context.Background(), images.ScanRequest{Digest: img.Digest(), Input: quarantine(t, img), Stage: images.ScanStageStatic,
		Tools: []string{images.ToolClamAV, images.ToolYARA}, MaxBytes: 64 << 20, Timeout: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestClamAVHitBlocks(t *testing.T) {
	needDocker(t)
	img := imagestest.NewImage("eicar", imagestest.Layer(map[string]string{"opt/app/readme.txt": "hello", "opt/app/eicar.com": eicar()}))
	res := staticScan(t, img)
	if len(res.StaticHits) == 0 || !strings.Contains(strings.Join(res.StaticHits, " "), "Eicar") {
		t.Fatalf("static hits = %q, want the EICAR signature", res.StaticHits)
	}
	if d := images.Decide(images.Scan{StaticHits: res.StaticHits, ModelVerdict: images.VerdictClean}, images.DecideInput{Allowed: true, Operator: true}); d.State != images.StateBlocked {
		t.Fatalf("decision = %+v, want blocked", d)
	}
}

func TestDeletedFileStillScanned(t *testing.T) {
	needDocker(t)
	var whiteout bytes.Buffer
	tw := tar.NewWriter(&whiteout)
	_ = tw.WriteHeader(&tar.Header{Name: "tmp/.wh.payload", Typeflag: tar.TypeReg, Mode: 0o644})
	_ = tw.Close()
	img := imagestest.NewImage("deleted",
		imagestest.Layer(map[string]string{"tmp/payload": eicar()}),
		whiteout.Bytes())
	res := staticScan(t, img)
	if !strings.Contains(strings.Join(res.StaticHits, " "), "layer 0/tmp/payload") {
		t.Fatalf("static hits = %q, want the file the second layer deletes", res.StaticHits)
	}
}

func TestScannerHasNoNetwork(t *testing.T) {
	needDocker(t)
	args := images.RunArgs(images.ContainerOptions{
		Name: "aiman-scan-nettest", Image: scannerImage, UID: os.Getuid(), GID: os.Getgid(),
		Cmd: []string{"sh", "-c", "ls /sys/class/net; id -u; touch /x 2>/dev/null && echo rootfs-writable; cat /proc/self/status | grep CapEff"},
	})
	out, code, err := images.ExecDockerCLI(context.Background(), args)
	if err != nil || code != 0 {
		t.Fatalf("docker run = %d, %v: %s", code, err, out)
	}
	lines := strings.Fields(out)
	if len(lines) < 2 || lines[0] != "lo" || strings.Contains(out, "eth0") {
		t.Fatalf("interfaces in the scanner: %q", out)
	}
	if strings.Contains(out, "rootfs-writable") {
		t.Fatal("the scanner's root filesystem is writable")
	}
	if !strings.Contains(out, "CapEff:\t0000000000000000") {
		t.Fatalf("the scanner holds capabilities: %q", out)
	}
}

func TestExtractRefusesTraversal(t *testing.T) {
	needDocker(t)
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	for _, name := range []string{"ok.txt", "../../escape"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
		_, _ = tw.Write([]byte("x"))
	}
	_ = tw.Close()
	img := imagestest.NewImage("traversal", layer.Bytes())
	s := dockerScanner(t, harness.EdgeImage, scanDB(t))
	_, err := s.Scan(context.Background(), images.ScanRequest{Digest: img.Digest(), Input: quarantine(t, img), Stage: images.ScanStageStatic,
		Tools: nil, MaxBytes: 1 << 20, Timeout: 2 * time.Minute})
	if err == nil || !strings.Contains(err.Error(), "unsafe layer entry") {
		t.Fatalf("Scan = %v, want the traversal refused", err)
	}
	matches, _ := filepath.Glob(filepath.Join(s.ScratchDir, "..", "*escape*"))
	more, _ := filepath.Glob(filepath.Join(s.ScratchDir, "*", "*escape*"))
	if len(matches)+len(more) > 0 {
		t.Fatalf("an entry escaped the layer: %q %q", matches, more)
	}
}

func TestLoadedImageIDMatchesScan(t *testing.T) {
	needDocker(t)
	f := newVetFixture(t, func(o *images.VetterOptions) { o.Loader = images.DockerLoader{} })
	img := imagestest.NewImage("load-"+time.Now().Format("150405.000"), imagestest.Layer(map[string]string{"etc/aiman-test": "vetted"}))
	f.reg.Tag("o/loadtest:1", img)
	_, _ = f.ensure(t, "o/loadtest:1", "s1", false)
	rec, err := f.v.Approve(context.Background(), images.ApproveRequest{Digest: img.Digest(), By: images.ApprovedByOperator, Approver: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = images.DockerLoader{}.Remove(context.Background(), rec.LocalName) })
	if !images.IDMatches(rec.ImageID, rec) {
		t.Fatalf("loaded id %s matches neither %s nor %s", rec.ImageID, rec.Digest, rec.ConfigDigest)
	}
	id, err := images.DockerLoader{}.Present(context.Background(), rec.LocalName)
	if err != nil || id != rec.ImageID {
		t.Fatalf("engine has %s, %v; record says %s", id, err, rec.ImageID)
	}
}
