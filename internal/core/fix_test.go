package core

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPackageArg(t *testing.T) {
	for _, c := range []struct{ in, name, arg string }{
		{"spatie/laravel-data", "spatie/laravel-data", "spatie/laravel-data"},
		{" Spatie/Laravel-Data:^4.0 ", "spatie/laravel-data", "spatie/laravel-data:^4.0"},
		{"laravel/framework:^12.0 || ^13.0", "laravel/framework", "laravel/framework:^12.0 || ^13.0"},
		{"filament/filament", "filament/filament", "filament/filament:^5.0"}, // the catalog's constraint
		{"filament/filament:^4.0", "filament/filament", "filament/filament:^4.0"},
	} {
		name, arg, ok := packageArg(c.in)
		if !ok || name != c.name || arg != c.arg {
			t.Errorf("packageArg(%q) = %q, %q, %v; want %q, %q", c.in, name, arg, ok, c.name, c.arg)
		}
	}
	for _, bad := range []string{"", "laravel", "laravel/", "a/b:", "a/b:^1;rm -rf /", "a/b:$(id)", "../x/y"} {
		if _, _, ok := packageArg(bad); ok {
			t.Errorf("packageArg(%q) accepted", bad)
		}
	}
}

func TestTailDetail(t *testing.T) {
	tl := newTail(io.Discard)
	io.WriteString(tl, "Loading composer repositories\n\x1b[32m 19/113 [====>-------]  16%\x1b[39m\r")
	io.WriteString(tl, "Deprecated: Constant PDO::MYSQL_ATTR_SSL_CA is deprecated\n\n")
	io.WriteString(tl, "  \x1b[41m ERROR \x1b[49m There are no commands defined in the \"filament\" namespace.\n")
	got := tl.Detail()
	if strings.Contains(got, "Deprecated") || strings.Contains(got, "[====") || strings.Contains(got, "\x1b") {
		t.Errorf("noise kept: %q", got)
	}
	if !strings.HasSuffix(got, `ERROR  There are no commands defined in the "filament" namespace.`) {
		t.Errorf("Detail() = %q", got)
	}
}

func TestComposerPackages(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"require":{"php":"^8.2","ext-intl":"*","Laravel/Framework":"^12.0"},"require-dev":{"pestphp/pest":"^4"}}`), 0o644)
	got, err := ComposerPackages(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"laravel/framework", "pestphp/pest"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTailComposerProblem(t *testing.T) {
	tl := newTail(io.Discard)
	io.WriteString(tl, `Your requirements could not be resolved to an installable set of packages.

  Problem 1
    - Root composer.json requires filament/filament ^5.0 -> satisfiable by filament/filament[v5.8.2, ..., v5.10.2].
    - filament/support[v5.8.2, ..., v5.10.2] require ext-intl * -> it is missing from your system. Install or enable PHP's intl extension.
    - symfony/polyfill-mbstring v1.43.0 requires ext-iconv * -> it is missing from your system. Install or enable PHP's iconv extension.

To enable extensions, verify that they are enabled in your .ini files:
    - /home/x/php.ini
You can also run `+"`php --ini`"+` in a terminal to see which files are used by PHP in CLI mode.
`)
	if got := tl.Detail(); !strings.HasPrefix(got, "Problem 1") || !strings.Contains(got, "ext-intl") || strings.Contains(got, "php --ini") {
		t.Errorf("Detail() = %q", got)
	}
	if got := tl.MissingExt(); got != "intl" {
		t.Errorf("MissingExt() = %q", got)
	}
}
