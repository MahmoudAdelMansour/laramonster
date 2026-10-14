// Package fetch downloads and unpacks release archives.
package fetch

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const userAgent = "LaraMonster (+https://monstersgate.com)"

var httpClient = &http.Client{Timeout: 10 * time.Minute}

// Progress reports bytes downloaded out of total (total may be -1).
type Progress func(done, total int64)

func GetJSON(url string, v any) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// GetText fetches a small text resource.
func GetText(url string, dst *string) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	*dst = string(b)
	return err
}

// Download saves url to dest, verifying sha256 when given.
func Download(url, dest, sum string, progress Progress) (int64, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := sha256.New()
	pw := &progressWriter{total: resp.ContentLength, fn: progress}
	n, err := io.Copy(io.MultiWriter(f, h, pw), resp.Body)
	if err != nil {
		return n, err
	}
	if sum != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), sum) {
		return n, errors.New("checksum mismatch for " + filepath.Base(url))
	}
	return n, nil
}

type progressWriter struct {
	done, total int64
	fn          Progress
	last        time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.fn != nil && time.Since(p.last) > 200*time.Millisecond {
		p.last = time.Now()
		p.fn(p.done, p.total)
	}
	return len(b), nil
}

// safeJoin rejects archive entries that would escape dir.
func safeJoin(dir, name string) (string, error) {
	p := filepath.Join(dir, name)
	if !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe path in archive: %s", name)
	}
	return p, nil
}

func Untar(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := safeJoin(dir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeFile(p, tr, os.FileMode(hdr.Mode)|0o600); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Only relative links that stay inside dir (npm, npx in Node.js).
			if filepath.IsAbs(hdr.Linkname) {
				return fmt.Errorf("unsafe symlink in archive: %s", hdr.Name)
			}
			if _, err := safeJoin(dir, filepath.Join(filepath.Dir(hdr.Name), hdr.Linkname)); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			_ = os.Remove(p)
			if err := os.Symlink(hdr.Linkname, p); err != nil {
				return err
			}
		}
	}
}

func Unzip(archive, dir string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, zf := range r.File {
		p, err := safeJoin(dir, zf.Name)
		if err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		err = writeFile(p, rc, 0o755)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFile(p string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Extract unpacks a .zip or .tar.gz archive into dir.
func Extract(archive, dir string) error {
	if strings.HasSuffix(archive, ".zip") {
		return Unzip(archive, dir)
	}
	return Untar(archive, dir)
}
