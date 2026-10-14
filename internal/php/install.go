package php

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"laramonster/internal/fetch"
	"laramonster/internal/paths"
)

// Release is a PHP version that can be downloaded for this platform.
type Release struct {
	Version   string `json:"version"` // "8.4"
	Full      string `json:"full"`    // "8.4.23"
	Installed bool   `json:"installed"`
	urls      []string
	sha256    string
}

const (
	// "common" includes pdo_sqlite/pdo_pgsql (Laravel defaults); "bulk" swaps
	// them for Swoole hooks. It lacks intl, sodium and opcache.
	staticBase  = "https://dl.static-php.dev/static-php-cli/common/"
	windowsBase = "https://windows.php.net/downloads/releases/"
)

// Available lists the newest patch release of each minor version.
func Available() ([]Release, error) {
	var (
		list []Release
		err  error
	)
	if runtime.GOOS == "windows" {
		list, err = windowsReleases()
	} else {
		list, err = staticReleases()
	}
	if err != nil {
		return nil, err
	}
	for i := range list {
		_, err := os.Stat(ManagedDir(list[i].Version))
		list[i].Installed = err == nil
	}
	slices.SortFunc(list, func(a, b Release) int { return compare(b.Version, a.Version) })
	return list, nil
}

func staticPlatform() (string, error) {
	osName := map[string]string{"linux": "linux", "darwin": "macos"}[runtime.GOOS]
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if osName == "" || arch == "" {
		return "", fmt.Errorf("no PHP builds for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return osName + "-" + arch, nil
}

var staticName = regexp.MustCompile(`^php-(\d+\.\d+)\.(\d+)-(cli|fpm)-(.+)\.tar\.gz$`)

func staticReleases() ([]Release, error) {
	platform, err := staticPlatform()
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
	}
	if err := fetch.GetJSON(staticBase+"?format=json", &entries); err != nil {
		return nil, err
	}
	// minor -> patch -> set of flavours present
	have := map[string]map[int]map[string]bool{}
	for _, e := range entries {
		m := staticName.FindStringSubmatch(e.Name)
		if m == nil || m[4] != platform {
			continue
		}
		var patch int
		fmt.Sscan(m[2], &patch)
		if have[m[1]] == nil {
			have[m[1]] = map[int]map[string]bool{}
		}
		if have[m[1]][patch] == nil {
			have[m[1]][patch] = map[string]bool{}
		}
		have[m[1]][patch][m[3]] = true
	}
	var out []Release
	for minor, patches := range have {
		best := -1
		for p, flavours := range patches {
			if flavours["cli"] && flavours["fpm"] && p > best {
				best = p
			}
		}
		if best < 0 || compare(minor, "8.1") < 0 {
			continue
		}
		full := fmt.Sprintf("%s.%d", minor, best)
		out = append(out, Release{
			Version: minor,
			Full:    full,
			urls: []string{
				fmt.Sprintf("%sphp-%s-cli-%s.tar.gz", staticBase, full, platform),
				fmt.Sprintf("%sphp-%s-fpm-%s.tar.gz", staticBase, full, platform),
			},
		})
	}
	return out, nil
}

func windowsReleases() ([]Release, error) {
	var index map[string]map[string]json.RawMessage
	if err := fetch.GetJSON(windowsBase+"releases.json", &index); err != nil {
		return nil, err
	}
	var out []Release
	for minor, entry := range index {
		if compare(minor, "8.1") < 0 {
			continue
		}
		var full string
		_ = json.Unmarshal(entry["version"], &full)
		for key, raw := range entry {
			if !strings.HasPrefix(key, "nts-") || !strings.HasSuffix(key, "-x64") {
				continue
			}
			var build struct {
				Zip struct {
					Path   string `json:"path"`
					Sha256 string `json:"sha256"`
				} `json:"zip"`
			}
			if json.Unmarshal(raw, &build) != nil || build.Zip.Path == "" {
				continue
			}
			out = append(out, Release{
				Version: minor,
				Full:    full,
				urls:    []string{windowsBase + build.Zip.Path},
				sha256:  build.Zip.Sha256,
			})
			break
		}
	}
	return out, nil
}

// ManagedDir is where LaraMonster installs a PHP version.
func ManagedDir(version string) string { return filepath.Join(paths.PHP(), Normalize(version)) }

// InstallVersion downloads and unpacks a PHP version into ManagedDir.
func InstallVersion(version string, progress fetch.Progress) (Release, error) {
	version = Normalize(version)
	list, err := Available()
	if err != nil {
		return Release{}, err
	}
	i := slices.IndexFunc(list, func(r Release) bool { return r.Version == version })
	if i < 0 {
		return Release{}, fmt.Errorf("PHP %s is not available for %s/%s", version, runtime.GOOS, runtime.GOARCH)
	}
	rel := list[i]

	if err := paths.Ensure(); err != nil {
		return rel, err
	}
	tmp, err := os.MkdirTemp(paths.PHP(), ".install-"+version+"-")
	if err != nil {
		return rel, err
	}
	defer os.RemoveAll(tmp)

	// Spread progress over all archives of this release.
	var base int64
	for _, url := range rel.urls {
		archive := filepath.Join(tmp, filepath.Base(url))
		n, err := fetch.Download(url, archive, rel.sha256, func(done, total int64) {
			if progress != nil {
				progress(base+done, -1)
			}
		})
		if err != nil {
			return rel, err
		}
		base += n
		if err := fetch.Extract(archive, tmp); err != nil {
			return rel, err
		}
		os.Remove(archive)
	}
	// The ini names the FINAL directory: tmp is renamed below, and an
	// extension_dir pointing at the temp name loads no extension at all
	// (Windows: no curl, mbstring, openssl, pdo… for every Laravel app).
	dest := ManagedDir(version)
	if err := os.WriteFile(filepath.Join(tmp, "php.ini"), []byte(defaultIni(dest)), 0o644); err != nil {
		return rel, err
	}

	_ = os.RemoveAll(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return rel, err
	}
	rel.Installed = true
	return rel, nil
}

// Uninstall removes a LaraMonster-managed PHP version.
func Uninstall(version string) error {
	dir := ManagedDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("PHP %s is not installed by LaraMonster", Normalize(version))
	}
	return os.RemoveAll(dir)
}

func defaultIni(dir string) string {
	ini := `; Generated by LaraMonster. Edit freely: it is kept until you reinstall this version.
memory_limit = 512M
upload_max_filesize = 128M
post_max_size = 128M
max_execution_time = 120
display_errors = On
error_reporting = E_ALL
date.timezone = UTC
opcache.enable = 1
opcache.enable_cli = 0
opcache.revalidate_freq = 0
`
	if runtime.GOOS == "windows" {
		ini += `extension_dir = "` + filepath.Join(dir, "ext") + `"
extension=curl
extension=fileinfo
extension=gd
extension=intl
extension=mbstring
extension=openssl
extension=pdo_mysql
extension=pdo_pgsql
extension=pdo_sqlite
extension=sodium
extension=sqlite3
extension=zip
zend_extension=opcache
`
	}
	return ini
}
