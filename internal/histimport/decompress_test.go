package histimport_test

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/veschin/bidlobot/internal/histimport"
)

const oneMiB = 1 << 20

// gzipBytes gzip-compresses b.
func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// zipEntry is one file to place in a synthetic archive.
type zipEntry struct {
	name string
	body string
	dir  bool
}

// makeZip builds an in-memory zip from entries.
func makeZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		name := e.name
		if e.dir && !strings.HasSuffix(name, "/") {
			name += "/"
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if !e.dir {
			if _, err := io.WriteString(w, e.body); err != nil {
				t.Fatalf("zip write %q: %v", name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// readWrapped wraps src under name, reads it fully, and always closes the
// returned ReadCloser (zip spills a temp file that Close must remove).
func readWrapped(t *testing.T, name string, src []byte, limit int64) ([]byte, error) {
	t.Helper()
	rc, err := histimport.WrapDecompressed(name, bytes.NewReader(src), limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rc.Close(); cerr != nil {
			t.Errorf("close wrapped reader: %v", cerr)
		}
	}()
	return io.ReadAll(rc)
}

func TestWrapDecompressedGzip(t *testing.T) {
	got, err := readWrapped(t, "x.gz", gzipBytes(t, []byte(realExport)), oneMiB)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if string(got) != realExport {
		t.Fatalf("gzip roundtrip mismatch:\n got %q\nwant %q", got, realExport)
	}
}

func TestWrapDecompressedZipChoosesResultJSON(t *testing.T) {
	// A realistic Telegram Desktop zip: a directory entry, __MACOSX
	// junk, a decoy small JSON, and the real result.json. result.json
	// must be chosen by name even though it is not the only .json.
	zb := makeZip(t, []zipEntry{
		{name: "ChatExport_2025/", dir: true},
		{name: "__MACOSX/._result.json", body: "macos junk"},
		{name: "ChatExport_2025/a.json", body: `{"name":"decoy","messages":[]}`},
		{name: "ChatExport_2025/result.json", body: realExport},
	})
	got, err := readWrapped(t, "anything.zip", zb, oneMiB)
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	if string(got) != realExport {
		t.Fatalf("zip did not yield result.json:\n got %q", got)
	}
}

func TestWrapDecompressedZipNoJSON(t *testing.T) {
	zb := makeZip(t, []zipEntry{
		{name: "notes.txt", body: "just a readme, no export here"},
	})
	_, err := readWrapped(t, "x.zip", zb, oneMiB)
	if !errors.Is(err, histimport.ErrNoJSONInZip) {
		t.Fatalf("error = %v, want ErrNoJSONInZip", err)
	}
}

func TestWrapDecompressedZipPrefersResultOverLarger(t *testing.T) {
	// Even when another .json is present, result.json wins by name
	// regardless of size ordering (here a.json is the smaller one and
	// result.json carries the real, larger payload).
	zb := makeZip(t, []zipEntry{
		{name: "a.json", body: `{"x":1}`},
		{name: "result.json", body: realExport},
	})
	got, err := readWrapped(t, "x.zip", zb, oneMiB)
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	if string(got) != realExport {
		t.Fatalf("result.json not chosen over a.json:\n got %q", got)
	}
}

func TestWrapDecompressedRawPassthrough(t *testing.T) {
	got, err := readWrapped(t, "x.json", []byte(realExport), oneMiB)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if string(got) != realExport {
		t.Fatalf("raw passthrough mismatch:\n got %q", got)
	}
}

func TestWrapDecompressedBombGuard(t *testing.T) {
	// 10000 bytes of payload through a 100-byte limit: reading must fail
	// with ErrDecompressLimit instead of returning the whole body.
	payload := strings.Repeat("a", 10000)
	rc, err := histimport.WrapDecompressed("x.json", strings.NewReader(payload), 100)
	if err != nil {
		t.Fatalf("WrapDecompressed (raw, over limit): %v", err)
	}
	defer func() { _ = rc.Close() }()

	_, err = io.ReadAll(rc)
	if !errors.Is(err, histimport.ErrDecompressLimit) {
		t.Fatalf("error = %v, want ErrDecompressLimit", err)
	}
}
