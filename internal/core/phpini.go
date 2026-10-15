package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"laramonster/internal/config"
	"laramonster/internal/php"
	"laramonster/internal/sites"
)

// IniSetting is a php.ini directive the settings editor offers with a
// label and the right kind of input. Any other directive can be added by
// name.
type IniSetting struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Group   string   `json:"group"`
	Help    string   `json:"help"`
	Kind    string   `json:"kind"` // size, seconds, number, onoff, select, text
	Options []string `json:"options,omitempty"`
}

// IniGroups in display order.
var IniGroups = []string{"Limits", "Uploads", "Errors", "Date & sessions", "OPcache"}

var IniSettings = []IniSetting{
	{Name: "memory_limit", Label: "Memory limit", Group: "Limits", Kind: "size", Help: "Most memory one request or command may use. -1 means no limit."},
	{Name: "max_execution_time", Label: "Max execution time", Group: "Limits", Kind: "seconds", Help: "Seconds a web request may run (command-line PHP has no limit). 0 means no limit."},
	{Name: "max_input_time", Label: "Max input time", Group: "Limits", Kind: "seconds", Help: "Seconds to read a request's input, such as an upload. -1 uses the execution time."},
	{Name: "max_input_vars", Label: "Max input variables", Group: "Limits", Kind: "number", Help: "Most form fields one request may send. Raise it for very large forms."},
	{Name: "upload_max_filesize", Label: "Max upload size", Group: "Uploads", Kind: "size", Help: "Largest single uploaded file. Keep it at or below the max POST size."},
	{Name: "post_max_size", Label: "Max POST size", Group: "Uploads", Kind: "size", Help: "Largest whole request body, all files and fields together."},
	{Name: "max_file_uploads", Label: "Max files per request", Group: "Uploads", Kind: "number", Help: "Most files one request may upload."},
	{Name: "display_errors", Label: "Show errors", Group: "Errors", Kind: "onoff", Help: "Print PHP errors in the page. Laravel shows its own error page either way."},
	{Name: "error_reporting", Label: "Error reporting", Group: "Errors", Kind: "select", Help: "Which errors PHP reports.",
		Options: []string{"E_ALL", "E_ALL & ~E_DEPRECATED", "E_ALL & ~E_DEPRECATED & ~E_NOTICE", "E_ERROR | E_PARSE"}},
	{Name: "log_errors", Label: "Log errors", Group: "Errors", Kind: "onoff", Help: "Write errors to the PHP log (Logs page)."},
	{Name: "date.timezone", Label: "Time zone", Group: "Date & sessions", Kind: "text", Help: "Default time zone, for example UTC or Africa/Cairo. Laravel uses its own config/app.php setting."},
	{Name: "session.gc_maxlifetime", Label: "Session lifetime", Group: "Date & sessions", Kind: "seconds", Help: "Seconds before PHP's own sessions expire. Laravel sessions use SESSION_LIFETIME instead."},
	{Name: "opcache.enable", Label: "OPcache", Group: "OPcache", Kind: "onoff", Help: "Cache compiled PHP for faster pages."},
	{Name: "opcache.validate_timestamps", Label: "Notice code changes", Group: "OPcache", Kind: "onoff", Help: "Check files for changes. Keep it on while developing, or edits won't show."},
	{Name: "opcache.revalidate_freq", Label: "Check for changes every", Group: "OPcache", Kind: "seconds", Help: "Seconds between checks for changed files. 0 checks on every request."},
	{Name: "opcache.memory_consumption", Label: "OPcache memory (MB)", Group: "OPcache", Kind: "number", Help: "Megabytes for compiled code. Takes effect when PHP restarts."},
}

// ScopeGlobal is every PHP version. Other scopes are a version ("8.4") or
// SiteScope(name).
const ScopeGlobal = "global"

func SiteScope(site string) string { return "site:" + site }

var (
	iniName  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)
	versionS = regexp.MustCompile(`^\d+\.\d+$`)
)

// Settings PHP can't take from -d or that would break LaraMonster.
var iniReserved = []string{"extension", "zend_extension", "extension_dir", "sendmail_path", "auto_prepend_file", "disable_functions", "open_basedir"}

func checkIni(name, value string) error {
	if !iniName.MatchString(name) || len(name) > 64 {
		return fmt.Errorf("%q isn't a php.ini setting name", name)
	}
	if slices.Contains(iniReserved, name) {
		return fmt.Errorf("%s can't be set here: LaraMonster manages it", name)
	}
	if strings.ContainsAny(value, "\r\n\x00") || len(value) > 512 {
		return fmt.Errorf("%s: the value must be one line", name)
	}
	return nil
}

// CheckScope accepts "global", an installed-looking version or a known site.
func CheckScope(scope string) error {
	switch {
	case scope == ScopeGlobal, versionS.MatchString(scope):
		return nil
	case strings.HasPrefix(scope, "site:"):
		_, list, err := Sites()
		if err != nil {
			return err
		}
		if _, ok := sites.Find(list, strings.TrimPrefix(scope, "site:")); !ok {
			return fmt.Errorf("no site named %q", strings.TrimPrefix(scope, "site:"))
		}
		return nil
	}
	return fmt.Errorf("unknown settings scope %q", scope)
}

// SetPHPSettings changes settings in one scope: a nil value removes it.
func SetPHPSettings(scope string, values map[string]*string) error {
	if err := CheckScope(scope); err != nil {
		return err
	}
	for k, v := range values {
		if v != nil {
			if err := checkIni(k, strings.TrimSpace(*v)); err != nil {
				return err
			}
		}
	}
	return Mutate(func(c *config.Config) error {
		if c.PHPSettings == nil {
			c.PHPSettings = map[string]map[string]string{}
		}
		m := c.PHPSettings[scope]
		if m == nil {
			m = map[string]string{}
		}
		for k, v := range values {
			if v == nil || strings.TrimSpace(*v) == "" {
				delete(m, k)
			} else {
				m[k] = strings.TrimSpace(*v)
			}
		}
		if len(m) == 0 {
			delete(c.PHPSettings, scope)
		} else {
			c.PHPSettings[scope] = m
		}
		return nil
	})
}

// MergedIni is what a site (or, with site "", a version) runs with:
// global, then the version, then the site.
func MergedIni(c *config.Config, version, site string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, c.PHPSettings[ScopeGlobal])
	maps.Copy(out, c.PHPSettings[php.Normalize(version)])
	if site != "" {
		maps.Copy(out, c.PHPSettings[SiteScope(site)])
	}
	return out
}

// PoolIni gives php-fpm its settings: per version, and per site for sites
// with settings of their own (those get their own pool).
func PoolIni(c *config.Config, list []sites.Site) (ini map[string]map[string]string, own map[string]string) {
	ini, own = map[string]map[string]string{}, map[string]string{}
	versions := map[string]bool{}
	for _, s := range list {
		if s.PHP != "" {
			versions[s.PHP] = true
		}
	}
	for v := range versions {
		ini[v] = MergedIni(c, v, "")
	}
	for _, s := range list {
		if s.PHP == "" || len(c.PHPSettings[SiteScope(s.Name)]) == 0 {
			continue
		}
		k := php.SiteKey(s.PHP, s.Name)
		own[s.Name] = k
		ini[k] = MergedIni(c, s.PHP, s.Name)
	}
	return ini, own
}

// iniArgsFor is the -d arguments for PHP run in dir (CLI, composer, artisan).
func iniArgsFor(inst php.Install, dir string) []string {
	c, err := config.Load()
	if err != nil {
		return nil
	}
	site := ""
	if _, list, err := Sites(); err == nil {
		if s, ok := sites.ForPath(list, dir); ok {
			site = s.Name
		}
	}
	return php.IniArgs(MergedIni(c, inst.Version, site))
}

// IniRow is one setting as the editor shows it: the value PHP actually
// uses and which layer set it.
type IniRow struct {
	IniSetting
	Value  string            `json:"value"`  // in effect
	Base   string            `json:"base"`   // from php.ini or PHP's default
	Source string            `json:"source"` // site, version, global or php.ini
	Layers map[string]string `json:"layers"` // global, version, site → value set there
}

// PHPSettingsView is everything the editor needs for one scope.
type PHPSettingsView struct {
	Scope   string   `json:"scope"`
	Version string   `json:"version"` // the PHP whose values are shown
	Site    string   `json:"site,omitempty"`
	IniFile string   `json:"ini_file,omitempty"` // the version's php.ini, when LaraMonster installed it
	Groups  []string `json:"groups"`
	Rows    []IniRow `json:"rows"`
}

// ReadPHPSettings shows a scope's settings with the values PHP reports.
// For "global" it shows the default PHP version's values.
func ReadPHPSettings(scope string) (PHPSettingsView, error) {
	if err := CheckScope(scope); err != nil {
		return PHPSettingsView{}, err
	}
	c, list, err := Sites()
	if err != nil {
		return PHPSettingsView{}, err
	}
	installs := php.Discover()
	v := PHPSettingsView{Scope: scope, Groups: append(slices.Clone(IniGroups), "Other")}
	switch {
	case scope == ScopeGlobal:
		v.Version = php.Default(c.PHP, installs)
	case strings.HasPrefix(scope, "site:"):
		v.Site = strings.TrimPrefix(scope, "site:")
		s, _ := sites.Find(list, v.Site)
		v.Version = s.PHP
	default:
		v.Version = scope
	}
	inst, ok := php.Find(installs, v.Version)
	if !ok || inst.CLI == "" {
		return v, errors.New("no PHP to read settings from: install a PHP version first")
	}
	if inst.Managed {
		v.IniFile = inst.IniDir + string(os.PathSeparator) + "php.ini"
	}

	layers := map[string]map[string]string{
		"global":  c.PHPSettings[ScopeGlobal],
		"version": c.PHPSettings[inst.Version],
	}
	if v.Site != "" {
		layers["site"] = c.PHPSettings[SiteScope(v.Site)]
	}
	// Every directive any layer sets, plus the curated ones.
	rows := map[string]IniRow{}
	for _, s := range IniSettings {
		rows[s.Name] = IniRow{IniSetting: s}
	}
	for _, l := range layers {
		for k := range l {
			if _, ok := rows[k]; !ok {
				rows[k] = IniRow{IniSetting: IniSetting{Name: k, Label: k, Group: "Other", Kind: "text"}}
			}
		}
	}
	names := slices.Sorted(maps.Keys(rows))
	base, err := iniValues(inst, nil, names)
	if err != nil {
		return v, err
	}
	merged := MergedIni(c, inst.Version, v.Site)
	if scope == ScopeGlobal { // what every version gets from global alone
		merged = maps.Clone(c.PHPSettings[ScopeGlobal])
	}
	if scope == inst.Version {
		merged = MergedIni(c, inst.Version, "")
	}
	effective, err := iniValues(inst, merged, names)
	if err != nil {
		return v, err
	}
	// Command-line PHP forces some settings for itself; websites use the
	// configured value, so read that from php.ini and the layers.
	fileIni := readIniFile(v.IniFile)
	for name, def := range cliForced {
		if _, ok := rows[name]; !ok {
			continue
		}
		b := def
		if fv, ok := fileIni[name]; ok {
			b = fv
		}
		base[name] = b
		effective[name] = b
		if mv, ok := merged[name]; ok {
			effective[name] = mv
		}
	}
	order := map[string]int{}
	for i, s := range IniSettings {
		order[s.Name] = i
	}
	for _, name := range names {
		r := rows[name]
		r.Base, r.Value = base[name], effective[name]
		r.Layers = map[string]string{}
		r.Source = "php.ini"
		for _, l := range []string{"global", "version", "site"} {
			if val, ok := layers[l][name]; ok {
				r.Layers[l] = val
				if scopeIncludes(scope, l) {
					r.Source = l
				}
			}
		}
		v.Rows = append(v.Rows, r)
	}
	slices.SortStableFunc(v.Rows, func(a, b IniRow) int {
		ai, aok := order[a.Name]
		bi, bok := order[b.Name]
		switch {
		case aok && bok:
			return ai - bi
		case aok:
			return -1
		case bok:
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return v, nil
}

// scopeIncludes: which layers apply to what a scope's view shows.
func scopeIncludes(scope, layer string) bool {
	switch {
	case scope == ScopeGlobal:
		return layer == "global"
	case strings.HasPrefix(scope, "site:"):
		return true
	default:
		return layer != "site"
	}
}

// ProjectIni is what PHP reports for directives when it runs for a project
// folder, with every settings layer that applies there.
func ProjectIni(inst php.Install, dir string, names []string) (map[string]string, error) {
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	site := ""
	if _, list, err := Sites(); err == nil {
		if s, ok := sites.ForPath(list, dir); ok {
			site = s.Name
		}
	}
	return iniValues(inst, MergedIni(c, inst.Version, site), names)
}

// iniValues asks PHP for directives' values with settings applied. A
// directive PHP doesn't know comes back as "".
func iniValues(inst php.Install, settings map[string]string, names []string) (map[string]string, error) {
	q, _ := json.Marshal(names)
	// get_cfg_var first: it's the configured value (php.ini or -d), not
	// what the CLI forces for itself (max_execution_time=0, display_errors).
	code := `$o=[];foreach(json_decode($argv[1]) as $n){$v=get_cfg_var($n);if($v===false)$v=ini_get($n);$o[$n]=$v===false?'':(string)$v;}echo json_encode($o);`
	args := append(php.IniArgs(settings), "-r", code, string(q))
	cmd := exec.Command(inst.CLI, args...)
	cmd.Env = append(os.Environ(), inst.Env()...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("PHP %s couldn't read its settings: %w", inst.Full, err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("PHP %s: %w (%s)", inst.Full, err, strings.TrimSpace(string(out)))
	}
	return m, nil
}

// cliForced are settings the PHP CLI overrides for itself, with PHP's
// default for websites.
var cliForced = map[string]string{
	"max_execution_time": "30",
	"max_input_time":     "-1",
	"output_buffering":   "0",
	"html_errors":        "1",
	"implicit_flush":     "0",
}

// readIniFile reads simple "name = value" lines of a php.ini.
func readIniFile(path string) map[string]string {
	out := map[string]string{}
	if path == "" {
		return out
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == ';' || line[0] == '[' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		out[strings.TrimSpace(k)] = v
	}
	return out
}
