package histimport

import (
	"archive/zip"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ErrNoJSONInZip / ErrDecompressLimit surface to the DM flow as specific
// remediation messages rather than an opaque parse failure.
var (
	ErrNoJSONInZip     = errors.New("no .json entry in the zip archive")
	ErrDecompressLimit = errors.New("decompressed size exceeds the safety limit")
)

// rc adapts a reader + cleanup into an io.ReadCloser.
type rc struct {
	io.Reader
	closeFn func() error
}

func (r rc) Close() error {
	if r.closeFn != nil {
		return r.closeFn()
	}
	return nil
}

// WrapDecompressed sniffs the leading magic bytes (filename is only a
// hint - Telegram Desktop emits result.json that an admin may zip under
// any name) and returns a reader yielding the underlying export JSON:
//
//   - gzip (1f 8b): streamed through compress/gzip, bomb-guarded by a
//     decompressed io.LimitReader at limit.
//   - zip (PK): spilled to a bounded temp file (a zip is a container, not
//     a stream - archive/zip needs io.ReaderAt+size), then the single
//     .json entry is opened (prefer result.json, else the largest .json;
//     directory and __MACOSX entries ignored). LimitReader-guarded.
//   - anything else: treated as raw JSON, LimitReader-guarded.
//
// The caller MUST Close the result (closes gzip / removes the temp file).
func WrapDecompressed(name string, r io.Reader, limit int64) (io.ReadCloser, error) {
	br := bufio.NewReader(r)
	magic, _ := br.Peek(2)

	switch {
	case len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("gzip open: %w", err)
		}
		return rc{Reader: limitGuard(gz, limit), closeFn: gz.Close}, nil

	case len(magic) == 2 && magic[0] == 'P' && magic[1] == 'K':
		return openZip(br, limit)

	default:
		return rc{Reader: limitGuard(br, limit)}, nil
	}
}

// limitGuard wraps r so that reading more than limit bytes yields
// ErrDecompressLimit instead of exhausting memory on a decompression
// bomb.
func limitGuard(r io.Reader, limit int64) io.Reader {
	return &guardReader{r: io.LimitReader(r, limit+1), limit: limit}
}

type guardReader struct {
	r     io.Reader
	n     int64
	limit int64
}

func (g *guardReader) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	g.n += int64(n)
	if g.n > g.limit {
		return n, ErrDecompressLimit
	}
	return n, err
}

func openZip(r io.Reader, limit int64) (io.ReadCloser, error) {
	tmp, err := os.CreateTemp("", "bidlobot-import-*.zip")
	if err != nil {
		return nil, fmt.Errorf("zip temp: %w", err)
	}
	cleanup := func() error {
		_ = tmp.Close()
		return os.Remove(tmp.Name())
	}
	// Bound the spill: the upstream download is already size-capped, but
	// guard here too so a local CLI .zip can't fill the disk.
	written, err := io.Copy(tmp, io.LimitReader(r, limit+1))
	if err != nil {
		_ = cleanup()
		return nil, fmt.Errorf("zip spill: %w", err)
	}
	if written > limit {
		_ = cleanup()
		return nil, ErrDecompressLimit
	}

	zr, err := zip.NewReader(tmp, written)
	if err != nil {
		_ = cleanup()
		return nil, fmt.Errorf("zip read: %w", err)
	}

	var chosen *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || strings.HasPrefix(f.Name, "__MACOSX/") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(f.Name), ".json") {
			continue
		}
		base := f.Name
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if strings.EqualFold(base, "result.json") {
			chosen = f
			break
		}
		if chosen == nil || f.UncompressedSize64 > chosen.UncompressedSize64 {
			chosen = f
		}
	}
	if chosen == nil {
		_ = cleanup()
		return nil, ErrNoJSONInZip
	}

	entry, err := chosen.Open()
	if err != nil {
		_ = cleanup()
		return nil, fmt.Errorf("zip entry open: %w", err)
	}
	return rc{
		Reader: limitGuard(entry, limit),
		closeFn: func() error {
			_ = entry.Close()
			return cleanup()
		},
	}, nil
}
