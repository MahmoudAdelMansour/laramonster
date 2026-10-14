package laravel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Param is a constructor parameter, as the runner describes it.
type Param struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Kind     string          `json:"kind"` // int, float, string, bool, array, model, enum, date, object, mixed
	Class    string          `json:"class,omitempty"`
	Enum     *EnumInfo       `json:"enum,omitempty"`
	Nullable bool            `json:"nullable"`
	Optional bool            `json:"optional"`
	Default  json.RawMessage `json:"default,omitempty"`
}

type EnumInfo struct {
	Class string `json:"class"`
	Cases []any  `json:"cases"`
}

// Job is a job class as the runner found it.
type Job struct {
	Class             string         `json:"class"`
	File              string         `json:"file"`
	Line              int            `json:"line"`
	Doc               string         `json:"doc,omitempty"`
	Params            []Param        `json:"params"`
	Queued            bool           `json:"queued"`
	Interfaces        []string       `json:"interfaces"`
	Traits            []string       `json:"traits"`
	Settings          PHPMap[any]    `json:"settings"`
	Methods           []string       `json:"methods"`
	HandleParams      []string       `json:"handle_params"`
	HandleSource      string         `json:"handle_source"`
	ConstructorSource string         `json:"constructor_source"`
	Helpers           PHPMap[string] `json:"helpers,omitempty"` // other methods, by name
}

// PHPMap is a JSON object written by PHP. json_encode turns an empty PHP
// array into [] rather than {}, so [] (and null) read as an empty map.
type PHPMap[V any] map[string]V

func (m *PHPMap[V]) UnmarshalJSON(b []byte) error {
	switch t := strings.TrimSpace(string(b)); {
	case t == "null", t == "[]":
		*m = PHPMap[V]{}
		return nil
	case strings.HasPrefix(t, "["):
		return fmt.Errorf("expected an object, got a list: %.40s", t)
	}
	var out map[string]V
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	*m = out
	return nil
}

// ScheduleEntry is one scheduled task from the app's schedule.
type ScheduleEntry struct {
	Expression  string `json:"expression"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Timezone    string `json:"timezone"`
	Next        string `json:"next"`
}

// Location is a line of code that refers to a class.
type Location struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Code string `json:"code"`
	abs  string // the file on disk, for reading a command's signature
}

// Step is one thing a job does, in plain words.
type Step struct {
	Kind string `json:"kind"` // mail, notify, http, db, model, files, cache, dispatch, event, command, shell, log, wait, retry, batch, throw
	Text string `json:"text"`
}

// JobAnalysis is the human explanation of a job.
type JobAnalysis struct {
	Summary  string     `json:"summary"`
	Does     []Step     `json:"does"`
	Runs     []string   `json:"runs"`     // when it runs
	Queue    []string   `json:"queue"`    // how it runs on the queue
	Uses     []string   `json:"uses"`     // services injected into handle()
	Schedule []string   `json:"schedule"` // human schedule lines
	From     []Location `json:"from"`     // where it's dispatched
	Warnings []string   `json:"warnings"`
}

var facades = map[string]bool{"DB": true, "Mail": true, "Http": true, "Cache": true, "Log": true, "Storage": true, "Queue": true, "Bus": true, "Event": true,
	"Notification": true, "Artisan": true, "Schema": true, "Redis": true, "Str": true, "Arr": true, "Carbon": true, "Auth": true, "File": true, "Process": true,
	"Config": true, "App": true, "Hash": true, "Crypt": true, "URL": true, "Route": true, "View": true, "Session": true, "Cookie": true, "Gate": true,
	"Validator": true, "Lang": true, "Date": true, "Context": true, "Concurrency": true, "Number": true, "Sleep": true, "Pipeline": true, "Response": true, "Request": true}

var (
	reMailNew     = regexp.MustCompile(`->(?:send|queue|later)\(\s*new\s+\\?([\w\\]+)`)
	reMail        = regexp.MustCompile(`Mail::(?:to|raw|send|queue|html|mailer|cc|bcc)\b`)
	reNotify      = regexp.MustCompile(`(?:->notify(?:Now)?\(|Notification::(?:send|sendNow|route)\([^;]*?)\s*new\s+\\?([\w\\]+)`)
	reNotifyAny   = regexp.MustCompile(`->notify\(|Notification::(?:send|route)`)
	reHTTP        = regexp.MustCompile(`Http::(?:[\w]+\([^;]*?\)->)*?(get|post|put|patch|delete|head)\(\s*['"]([^'"]+)['"]`)
	reHTTPAny     = regexp.MustCompile(`Http::`)
	reDBTable     = regexp.MustCompile(`DB::(?:connection\([^)]*\)->)?table\(\s*['"]([\w.]+)['"]\s*\)[^;]*?->(insert\w*|update\w*|upsert|delete|increment|decrement|truncate)\(`)
	reDBRaw       = regexp.MustCompile(`DB::(statement|unprepared|insert|update|delete)\(`)
	reModelCrud   = regexp.MustCompile(`\b([A-Z]\w+)::(create|forceCreate|firstOrCreate|updateOrCreate|insert|upsert|destroy|truncate)\(`)
	reModelQ      = regexp.MustCompile(`\b([A-Z]\w+)::(?:where\w*|query|whereIn|withTrashed|find\w*)\([^;]*?->(update|delete|forceDelete|increment|decrement|restore)\(`)
	reSave        = regexp.MustCompile(`\$[\w>-]+->(save|update|delete|forceDelete|increment|decrement|restore|touch)\(`)
	reStorage     = regexp.MustCompile(`Storage::(?:disk\(\s*['"]([\w-]+)['"]\s*\)->)?(put\w*|delete\w*|move|copy|append|prepend|write\w*|makeDirectory)\(`)
	reCache       = regexp.MustCompile(`Cache::(?:[\w]+\([^;]*?\)->)?(put|forever|forget|flush|increment|decrement|remember\w*|add|pull|lock)\(`)
	reDispatch    = regexp.MustCompile(`\b([A-Z]\w+)::(dispatch(?:Sync|Now|AfterResponse|If|Unless)?|withChain)\(|dispatch(?:_sync)?\(\s*new\s+\\?([\w\\]+)`)
	reEvent       = regexp.MustCompile(`(?:event|Event::dispatch)\(\s*new\s+\\?([\w\\]+)`)
	reChain       = regexp.MustCompile(`Bus::(chain|batch)\(`)
	reArtisan     = regexp.MustCompile(`Artisan::(?:call|queue)\(\s*['"]([^'"\s]+)`)
	reShell       = regexp.MustCompile(`Process::|\b(?:exec|shell_exec|passthru|system|proc_open)\(`)
	reLog         = regexp.MustCompile(`Log::|\blogger\(|\binfo\(`)
	reSleep       = regexp.MustCompile(`\b(?:sleep|Sleep::for)\(\s*(\d+)`)
	reRelease     = regexp.MustCompile(`\$this->release\(`)
	reFail        = regexp.MustCompile(`\$this->fail\(`)
	reBatches     = regexp.MustCompile(`->(chunk\w*|lazy\w*|cursor|each)\(`)
	reThrow       = regexp.MustCompile(`throw\s+new\s+\\?([\w\\]+)`)
	reOnQueue     = regexp.MustCompile(`onQueue\(\s*['"]([^'"]+)['"]`)
	reOnConn      = regexp.MustCompile(`onConnection\(\s*['"]([^'"]+)['"]`)
	reThisCall    = regexp.MustCompile(`\$this->(\w+)\(`)
	reServiceCall = regexp.MustCompile(`\$(\w+)->(\w+)\(`)
	reBatchCancel = regexp.MustCompile(`batch\(\)\??->cancelled\(`)
	reRedis       = regexp.MustCompile(`Redis::|->(?:zadd|zrem|lpush|rpush|hset|sadd|pipeline)\(`)
	reSignature   = regexp.MustCompile(`\$signature\s*=\s*['"]([\w:.-]+)`)
)

func short(class string) string {
	if i := strings.LastIndex(class, `\`); i >= 0 {
		return class[i+1:]
	}
	return class
}

// AnalyzeJob explains a job: what handle() does, how it runs on the queue,
// and when it runs (schedule, dispatch sites).
func AnalyzeJob(j Job, schedule []ScheduleEntry, from []Location, defaultConnection string, loc *time.Location) JobAnalysis {
	src := stripComments(j.HandleSource)
	// Follow $this->helper() calls (two levels), so work done in the job's
	// own methods counts too.
	seen := map[string]bool{}
	for depth := 0; depth < 2; depth++ {
		for _, m := range reThisCall.FindAllStringSubmatch(src, -1) {
			if body, ok := j.Helpers[m[1]]; ok && !seen[m[1]] {
				seen[m[1]] = true
				src += "\n" + stripComments(body)
			}
		}
	}
	// Injected services: "$feeds->pushToFeeds()" with handle(FeedStore $feeds).
	services := map[string]string{}
	for _, p := range j.HandleParams {
		if t := strings.Fields(p); len(t) == 2 {
			services[strings.TrimPrefix(t[1], "$")] = short(strings.TrimPrefix(t[0], "?"))
		}
	}
	a := JobAnalysis{Does: []Step{}, Runs: []string{}, Queue: []string{}, Uses: []string{}, Schedule: []string{}, From: from, Warnings: []string{}}
	add := func(kind, text string) {
		for _, s := range a.Does {
			if s.Text == text {
				return
			}
		}
		a.Does = append(a.Does, Step{kind, text})
	}

	// Mail and notifications.
	for _, m := range reMailNew.FindAllStringSubmatch(src, -1) {
		add("mail", "Sends the "+short(m[1])+" email")
	}
	if len(a.Does) == 0 && reMail.MatchString(src) {
		add("mail", "Sends an email")
	}
	for _, m := range reNotify.FindAllStringSubmatch(src, -1) {
		add("notify", "Sends the "+short(m[1])+" notification")
	}
	if !slices.ContainsFunc(a.Does, func(s Step) bool { return s.Kind == "notify" }) && reNotifyAny.MatchString(src) {
		add("notify", "Sends a notification")
	}
	// Outside world.
	for _, m := range reHTTP.FindAllStringSubmatch(src, -1) {
		add("http", "Calls an external API: "+strings.ToUpper(m[1])+" "+m[2])
	}
	if !slices.ContainsFunc(a.Does, func(s Step) bool { return s.Kind == "http" }) && reHTTPAny.MatchString(src) {
		add("http", "Calls an external API")
	}
	// Data.
	for _, m := range reDBTable.FindAllStringSubmatch(src, -1) {
		add("db", fmt.Sprintf("%s rows in the %s table", verb(m[2]), m[1]))
	}
	if m := reDBRaw.FindStringSubmatch(src); m != nil {
		add("db", "Runs a raw database query ("+m[1]+")")
	}
	for _, m := range reModelCrud.FindAllStringSubmatch(src, -1) {
		if !facades[m[1]] {
			add("model", fmt.Sprintf("%s %s records", verb(m[2]), m[1]))
		}
	}
	for _, m := range reModelQ.FindAllStringSubmatch(src, -1) {
		if !facades[m[1]] {
			add("model", fmt.Sprintf("%s %s records that match a condition", verb(m[2]), m[1]))
		}
	}
	if m := reSave.FindStringSubmatch(src); m != nil && !slices.ContainsFunc(a.Does, func(s Step) bool { return s.Kind == "model" }) {
		add("model", verb(m[1])+" a record it was given")
	}
	for _, m := range reStorage.FindAllStringSubmatch(src, -1) {
		disk := m[1]
		if disk == "" {
			disk = "default"
		}
		add("files", fmt.Sprintf("%s files on the %s disk", verb(m[2]), disk))
	}
	if reCache.MatchString(src) {
		add("cache", "Changes cached values")
	}
	// More work.
	for _, m := range reDispatch.FindAllStringSubmatch(src, -1) {
		name := m[1]
		if name == "" {
			name = short(m[3])
		}
		if name != "" && !facades[name] && name != "self" && name != "static" {
			add("dispatch", "Dispatches "+name)
		}
	}
	for _, m := range reEvent.FindAllStringSubmatch(src, -1) {
		add("event", "Fires the "+short(m[1])+" event")
	}
	if m := reChain.FindStringSubmatch(src); m != nil {
		add("dispatch", "Starts a job "+m[1])
	}
	for _, m := range reArtisan.FindAllStringSubmatch(src, -1) {
		add("command", "Runs the "+m[1]+" command")
	}
	if reShell.MatchString(src) {
		add("shell", "Runs a shell command")
	}
	if reBatches.MatchString(src) {
		add("batch", "Works through records in batches")
	}
	if m := reSleep.FindStringSubmatch(src); m != nil {
		add("wait", "Waits "+m[1]+" seconds")
	}
	if reRelease.MatchString(src) {
		add("retry", "Can put itself back on the queue to try again later")
	}
	if reFail.MatchString(src) {
		add("retry", "Can mark itself as failed")
	}
	for _, m := range reThrow.FindAllStringSubmatch(src, -1) {
		add("throw", "Can fail with "+short(m[1]))
	}
	if reBatchCancel.MatchString(src) {
		add("retry", "Stops early if its batch was cancelled")
	}
	if reRedis.MatchString(src) {
		add("cache", "Changes data in Redis")
	}
	for _, m := range reServiceCall.FindAllStringSubmatch(src, -1) {
		if svc, ok := services[m[1]]; ok && !facades[svc] {
			add("service", "Calls "+svc+"->"+m[2]+"()")
		}
	}
	if reLog.MatchString(src) {
		add("log", "Writes to the log")
	}

	for _, p := range j.HandleParams {
		if t := strings.Fields(p); len(t) == 2 {
			a.Uses = append(a.Uses, short(strings.TrimPrefix(t[0], "?")))
		}
	}

	// How it runs on the queue.
	set := j.Settings
	if !j.Queued {
		a.Queue = append(a.Queue, "Not queued: it runs immediately, inside whatever dispatches it")
	} else {
		conn := str(set["connection"])
		if m := reOnConn.FindStringSubmatch(j.ConstructorSource); m != nil {
			conn = m[1]
		}
		queue := str(set["queue"])
		if m := reOnQueue.FindStringSubmatch(j.ConstructorSource); m != nil {
			queue = m[1]
		}
		if conn == "" {
			conn = defaultConnection + " (the app's default)"
		}
		if queue == "" {
			queue = "default"
		}
		a.Queue = append(a.Queue, fmt.Sprintf("Runs on the %s queue of the %s connection", queue, conn))
		if conn == "sync" || strings.HasPrefix(conn, "sync ") {
			a.Warnings = append(a.Warnings, "The sync connection runs it immediately, not in the background.")
		}
		switch tries := num(set["tries"]); {
		case tries == 1:
			a.Queue = append(a.Queue, "Doesn't retry: one failure and it's failed")
		case tries > 1:
			line := fmt.Sprintf("Tries up to %d times", tries)
			if b := set["backoff"]; b != nil {
				line += ", waiting " + backoff(b) + " between tries"
			}
			a.Queue = append(a.Queue, line)
		default:
			if slices.Contains(j.Methods, "retryUntil") {
				a.Queue = append(a.Queue, "Retries until the deadline in retryUntil()")
			} else {
				a.Queue = append(a.Queue, "Retries as often as the worker's --tries allows")
			}
		}
		if t := num(set["timeout"]); t > 0 {
			a.Queue = append(a.Queue, fmt.Sprintf("Is stopped after %s", seconds(t)))
		}
		if m := num(set["maxExceptions"]); m > 0 {
			a.Queue = append(a.Queue, fmt.Sprintf("Fails for good after %d exceptions", m))
		}
		if slices.Contains(j.Interfaces, "ShouldBeUnique") {
			line := "Only one copy can wait in the queue at a time"
			if u := num(set["uniqueFor"]); u > 0 {
				line += " (for up to " + seconds(u) + ")"
			}
			a.Queue = append(a.Queue, line)
		}
		if slices.Contains(j.Interfaces, "ShouldBeEncrypted") {
			a.Queue = append(a.Queue, "Its data is encrypted while it waits in the queue")
		}
		if slices.Contains(j.Traits, "SerializesModels") || slices.Contains(j.Traits, "Queueable") {
			for _, p := range j.Params {
				if p.Kind == "model" {
					a.Queue = append(a.Queue, "Models are fetched fresh from the database when it runs")
					break
				}
			}
		}
		if b, ok := set["deleteWhenMissingModels"].(bool); ok && b {
			a.Queue = append(a.Queue, "Is dropped quietly if one of its models was deleted")
		}
		if slices.Contains(j.Methods, "failed") {
			a.Queue = append(a.Queue, "Has a failed() method that runs when it gives up")
		}
		if slices.Contains(j.Methods, "middleware") {
			a.Queue = append(a.Queue, "Uses job middleware (rate limits, overlap locks…)")
		}
	}

	// When it runs.
	for _, s := range schedule {
		if s.Description == j.Class || strings.Contains(s.Command, short(j.Class)) || strings.HasSuffix(s.Description, `\`+short(j.Class)) {
			line := CronHuman(s.Expression)
			if next, err := time.Parse(time.RFC3339, s.Next); err == nil {
				if loc != nil {
					next = next.In(loc)
				}
				line += " · next " + next.Format("Mon 2 Jan 15:04")
			}
			a.Schedule = append(a.Schedule, line)
		}
	}
	if len(a.Schedule) > 0 {
		a.Runs = append(a.Runs, "On a schedule: "+strings.Join(a.Schedule, "; "))
	}
	// Dispatched by an artisan command that is itself scheduled.
	for _, l := range from {
		sig := commandSignature(l)
		if sig == "" {
			continue
		}
		for _, s := range schedule {
			if strings.Contains(s.Command+" "+s.Description, " "+sig) || strings.Contains(s.Command+" "+s.Description, "'"+sig+"'") {
				line := "Via the " + sig + " command: " + CronHuman(s.Expression)
				if !slices.Contains(a.Schedule, line) {
					a.Schedule = append(a.Schedule, line)
					a.Runs = append(a.Runs, line)
				}
			}
		}
	}
	if len(from) > 0 {
		a.Runs = append(a.Runs, fmt.Sprintf("When code dispatches it (%d place%s)", len(from), plural(len(from))))
	}
	if len(a.Runs) == 0 {
		a.Runs = append(a.Runs, "Nothing in app/ or routes/ dispatches or schedules it: it only runs when you dispatch it")
	}

	// One sentence.
	verbs := []string{}
	for _, s := range a.Does {
		if len(verbs) == 3 {
			break
		}
		if s.Kind != "log" && s.Kind != "throw" {
			verbs = append(verbs, lowerFirst(s.Text))
		}
	}
	switch {
	case j.Doc != "":
		a.Summary = j.Doc
	case len(verbs) > 0:
		a.Summary = upperFirst(joinAnd(verbs)) + "."
	case len(a.Does) > 0:
		a.Summary = a.Does[0].Text + "."
	default:
		a.Summary = "LaraMonster couldn't tell what it does from its handle() method; read the code below."
	}
	return a
}

// CronHuman turns common cron expressions into words.
func CronHuman(expr string) string {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return "cron " + expr
	}
	min, hour, dom, mon, dow := f[0], f[1], f[2], f[3], f[4]
	at := func() string {
		h, err1 := strconv.Atoi(hour)
		m, err2 := strconv.Atoi(min)
		if err1 != nil || err2 != nil {
			return ""
		}
		return fmt.Sprintf("%02d:%02d", h, m)
	}
	days := map[string]string{"0": "Sunday", "1": "Monday", "2": "Tuesday", "3": "Wednesday", "4": "Thursday", "5": "Friday", "6": "Saturday", "7": "Sunday"}
	switch {
	case expr == "* * * * *":
		return "Every minute"
	case strings.HasPrefix(min, "*/") && hour == "*" && dom == "*" && mon == "*" && dow == "*":
		return "Every " + strings.TrimPrefix(min, "*/") + " minutes"
	case min == "0" && hour == "*" && dom == "*" && mon == "*" && dow == "*":
		return "Every hour"
	case hour == "*" && dom == "*" && mon == "*" && dow == "*":
		if _, err := strconv.Atoi(min); err == nil {
			return "Every hour at :" + fmt.Sprintf("%02s", min)
		}
	case strings.HasPrefix(hour, "*/") && dom == "*" && mon == "*" && dow == "*" && min == "0":
		return "Every " + strings.TrimPrefix(hour, "*/") + " hours"
	case at() != "" && dom == "*" && mon == "*" && dow == "*":
		return "Every day at " + at()
	case at() != "" && dom == "*" && mon == "*" && dow == "1-5":
		return "Weekdays at " + at()
	case at() != "" && dom == "*" && mon == "*" && days[dow] != "":
		return "Every " + days[dow] + " at " + at()
	case at() != "" && mon == "*" && dow == "*":
		if d, err := strconv.Atoi(dom); err == nil {
			return fmt.Sprintf("Monthly on day %d at %s", d, at())
		}
	case at() != "" && dow == "*":
		return "Yearly (" + dom + "/" + mon + ") at " + at()
	}
	return "cron " + expr
}

// FindReferences lists lines in app/ and routes/ that dispatch or schedule
// a class (its short name followed by ::dispatch, new X, X::class in a
// dispatch/schedule call). The class's own file is skipped.
func FindReferences(projectDir, class, ownFile string) []Location {
	name := regexp.QuoteMeta(short(class))
	re := regexp.MustCompile(`\b` + name + `::(dispatch\w*|withChain)\(|(?:dispatch\w*|event|Schedule::job|->job|Bus::\w+|chain)\(\s*(?:\[\s*)?new\s+\\?(?:[\w\\]+\\)?` + name + `\b|\b` + name + `::class\s*\)\s*->\s*dispatch|\bnew\s+\\?(?:[\w\\]+\\)?` + name + `\(`)
	out := []Location{}
	schedule := regexp.MustCompile(`Schedule::job\(|->job\(`)
	for _, root := range []string{"app", "routes", "database/seeders"} {
		_ = filepath.WalkDir(filepath.Join(projectDir, root), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".php") || len(out) >= 25 {
				return nil
			}
			rel, _ := filepath.Rel(projectDir, p)
			if filepath.ToSlash(rel) == filepath.ToSlash(ownFile) {
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return nil
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
			for n := 1; sc.Scan(); n++ {
				line := sc.Text()
				// Scheduling is reported from the app's schedule, not as a dispatch.
				if re.MatchString(line) && !schedule.MatchString(line) {
					code := strings.TrimSpace(line)
					if len(code) > 160 {
						code = code[:160] + "…"
					}
					out = append(out, Location{File: filepath.ToSlash(rel), Line: n, Code: code, abs: p})
				}
			}
			return nil
		})
	}
	return out
}

// stripComments removes PHP comments, leaving strings alone (a URL's //
// is not a comment) and attributes (#[...]) in place.
func stripComments(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(src) {
				j = len(src) - 1
			}
			b.WriteString(src[i : j+1])
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += end + 3
		case (c == '/' && i+1 < len(src) && src[i+1] == '/') || (c == '#' && (i+1 >= len(src) || src[i+1] != '[')):
			for i < len(src) && src[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// commandSignature reads the artisan command name from a reference found
// in app/Console/Commands.
func commandSignature(l Location) string {
	if l.abs == "" || !strings.Contains(l.File, "Console/Commands/") {
		return ""
	}
	b, err := os.ReadFile(l.abs)
	if err != nil {
		return ""
	}
	if m := reSignature.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

func verb(op string) string {
	op = strings.ToLower(op)
	switch {
	case strings.HasPrefix(op, "insert"), op == "create", op == "forcecreate", op == "put", op == "putfile", op == "putfileas", strings.HasPrefix(op, "write"), op == "append", op == "prepend", op == "makedirectory", op == "copy":
		return "Creates"
	case op == "firstorcreate", op == "updateorcreate", op == "upsert", op == "save", op == "touch":
		return "Creates or updates"
	case strings.HasPrefix(op, "update"), op == "increment", op == "decrement", op == "move", op == "restore":
		return "Updates"
	case strings.HasPrefix(op, "delete"), op == "destroy", op == "forcedelete":
		return "Deletes"
	case op == "truncate":
		return "Empties"
	}
	return "Changes"
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func backoff(v any) string {
	switch b := v.(type) {
	case float64:
		return seconds(int(b))
	case []any:
		parts := []string{}
		for _, x := range b {
			parts = append(parts, seconds(num(x)))
		}
		return strings.Join(parts, ", then ")
	}
	return fmt.Sprint(v)
}

func seconds(s int) string {
	switch {
	case s >= 3600 && s%3600 == 0:
		return fmt.Sprintf("%d hour%s", s/3600, plural(s/3600))
	case s >= 60 && s%60 == 0:
		return fmt.Sprintf("%d minute%s", s/60, plural(s/60))
	}
	return fmt.Sprintf("%d second%s", s, plural(s))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
