<?php
/*
 * LaraMonster runner: boots a Laravel project (with the agent recording in
 * memory) and does one thing, then prints one JSON document after a marker
 * line so app output can't corrupt it.
 *
 *   php -d auto_prepend_file=agent.php runner.php <project> <command> <input.json>
 *
 * Commands: events, dispatch-event, jobs, run-job, queues, queue-action.
 */

const LM_MARK = "\n@@LARAMONSTER-RESULT@@\n";

$base = $argv[1] ?? '';
$command = $argv[2] ?? '';
$input = isset($argv[3]) && is_file($argv[3]) ? (json_decode((string) file_get_contents($argv[3]), true) ?: []) : [];

function lm_out(array $result): never
{
    $json = json_encode($result, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PARTIAL_OUTPUT_ON_ERROR | JSON_INVALID_UTF8_SUBSTITUTE);
    fwrite(STDOUT, LM_MARK.$json."\n");
    exit(0);
}

set_exception_handler(static function (Throwable $e): void {
    lm_out(['ok' => false, 'error' => lm_exception($e)]);
});

function lm_exception(Throwable $e): array
{
    $frames = [];
    foreach (array_slice($e->getTrace(), 0, 12) as $f) {
        if (isset($f['file'])) {
            $frames[] = (class_exists('LaraMonsterTracker', false) ? LaraMonsterTracker::relative($f['file']) : $f['file']).':'.($f['line'] ?? 0)
                .(isset($f['class']) ? ' '.$f['class'].($f['type'] ?? '::').$f['function'].'()' : (isset($f['function']) ? ' '.$f['function'].'()' : ''));
        }
    }

    return (class_exists('LaraMonsterTracker', false) ? LaraMonsterTracker::exception($e) : ['class' => get_class($e), 'message' => $e->getMessage(), 'file' => $e->getFile(), 'line' => $e->getLine()])
        + ['trace' => $frames];
}

chdir($base);
require $base.'/vendor/autoload.php';
$app = require $base.'/bootstrap/app.php';
$kernel = $app->make(Illuminate\Contracts\Console\Kernel::class);
$kernel->bootstrap();

// ── class discovery ──────────────────────────────────────────────────────────

/** @return list<string> classes declared in PHP files under $dir */
function lm_classes_in(string $dir): array
{
    if (! is_dir($dir)) {
        return [];
    }
    $out = [];
    $it = new RecursiveIteratorIterator(new RecursiveDirectoryIterator($dir, FilesystemIterator::SKIP_DOTS));
    foreach ($it as $file) {
        if ($file->getExtension() !== 'php') {
            continue;
        }
        $src = (string) file_get_contents($file->getPathname());
        if (preg_match('/^\s*namespace\s+([^;]+);/m', $src, $ns) && preg_match('/^\s*(?:final\s+|abstract\s+|readonly\s+)*(?:class|enum)\s+(\w+)/m', $src, $cl)) {
            $out[] = trim($ns[1]).'\\'.$cl[1];
        }
    }
    sort($out);

    return $out;
}

function lm_doc(?string $doc): ?string
{
    if (! $doc) {
        return null;
    }
    $lines = [];
    foreach (preg_split('/\R/', $doc) as $line) {
        $line = trim(preg_replace('/^\s*(\/\*\*|\*\/|\*)\s?/', '', $line));
        if ($line === '' && $lines) {
            break;
        }
        if ($line !== '' && ! str_starts_with($line, '@')) {
            $lines[] = $line;
        }
    }

    return $lines ? implode(' ', $lines) : null;
}

/** Constructor parameters as a form: name, type, default, and what the value means. */
function lm_params(ReflectionClass $class): array
{
    $ctor = $class->getConstructor();
    if (! $ctor) {
        return [];
    }
    $out = [];
    foreach ($ctor->getParameters() as $p) {
        $type = $p->getType();
        $names = $type instanceof ReflectionUnionType ? array_map(fn ($t) => $t->getName(), $type->getTypes()) : ($type ? [$type->getName()] : []);
        $kind = 'mixed';
        $model = null;
        $enum = null;
        foreach ($names as $n) {
            if (in_array($n, ['int', 'float', 'string', 'bool', 'array'], true)) {
                $kind = $n;
                break;
            }
            if (class_exists($n) && is_subclass_of($n, Illuminate\Database\Eloquent\Model::class)) {
                $kind = 'model';
                $model = $n;
                break;
            }
            if (enum_exists($n)) {
                $kind = 'enum';
                $enum = ['class' => $n, 'cases' => array_map(fn ($c) => $c instanceof BackedEnum ? $c->value : $c->name, $n::cases())];
                break;
            }
            if (is_a($n, DateTimeInterface::class, true)) {
                $kind = 'date';
                break;
            }
            if (class_exists($n) || interface_exists($n)) {
                $kind = 'object';
                $model = $n;
            }
        }
        $default = null;
        $hasDefault = $p->isDefaultValueAvailable();
        if ($hasDefault) {
            try {
                $default = LaraMonsterTracker::norm($p->getDefaultValue());
            } catch (Throwable) {
            }
        }
        $out[] = [
            'name' => $p->getName(),
            'type' => $type ? (string) $type : 'mixed',
            'kind' => $kind,
            'class' => $model,
            'enum' => $enum,
            'nullable' => $type?->allowsNull() ?? true,
            'optional' => $hasDefault,
            'default' => $default,
            'variadic' => $p->isVariadic(),
        ];
    }

    return $out;
}

/** Builds an object from form values, or from a raw PHP expression. */
function lm_build(string $class, array $input): object
{
    if (trim((string) ($input['raw'] ?? '')) !== '') {
        $code = trim($input['raw']);
        $code = str_starts_with($code, '<?php') ? substr($code, 5) : $code;
        $value = eval(str_contains($code, 'return ') ? $code : 'return '.rtrim($code, ';').';');
        if (! is_object($value)) {
            throw new InvalidArgumentException('The PHP code must return an object, like new App\\Events\\OrderPlaced($user, 10).');
        }

        return $value;
    }
    if (! class_exists($class)) {
        throw new InvalidArgumentException("Class {$class} doesn't exist.");
    }
    $reflection = new ReflectionClass($class);
    $args = [];
    $values = $input['args'] ?? [];
    foreach (lm_params($reflection) as $param) {
        $name = $param['name'];
        if (! array_key_exists($name, $values) || $values[$name] === '' || $values[$name] === null) {
            if ($param['optional']) {
                continue;
            }
            if ($param['nullable']) {
                $args[$name] = null;
                continue;
            }
            throw new InvalidArgumentException("“{$name}” is required.");
        }
        $v = $values[$name];
        $args[$name] = match ($param['kind']) {
            'int' => (int) $v,
            'float' => (float) $v,
            'bool' => filter_var($v, FILTER_VALIDATE_BOOL),
            'array' => is_array($v) ? $v : (json_decode((string) $v, true) ?? throw new InvalidArgumentException("“{$name}” must be JSON, like [1, 2] or {\"a\": 1}.")),
            'model' => $param['class']::findOrFail($v),
            'enum' => $param['enum'] && is_subclass_of($param['enum']['class'], BackedEnum::class) ? $param['enum']['class']::from(is_numeric($v) && is_int($param['enum']['cases'][0] ?? null) ? (int) $v : $v) : constant($param['enum']['class'].'::'.$v),
            'date' => Illuminate\Support\Carbon::parse($v),
            'object' => app($param['class']),
            default => is_string($v) && ($d = json_decode($v, true)) !== null && in_array($v[0] ?? '', ['[', '{'], true) ? $d : $v,
        };
    }

    return $reflection->newInstanceArgs($args);
}

function lm_trace(): array
{
    return LaraMonsterTracker::current()?->trace ?? [];
}

function lm_rel(string|false $file): ?string
{
    return $file ? LaraMonsterTracker::relative($file) : null;
}

function lm_is_listener_class(string $class): bool
{
    return class_exists($class) && ! str_starts_with($class, 'Illuminate\\');
}

// ── commands ─────────────────────────────────────────────────────────────────

// Laravel installs its own exception handler while booting, so failures
// are caught here and reported in the result instead.
try {
switch ($command) {
    case 'events':
        $dispatcher = app('events');
        $raw = method_exists($dispatcher, 'getRawListeners') ? $dispatcher->getRawListeners() : [];
        $names = array_unique(array_merge(
            lm_classes_in(app_path('Events')),
            array_filter(array_keys($raw), fn ($n) => class_exists($n) && ! str_starts_with($n, 'Illuminate\\') && ! str_starts_with($n, 'Laravel\\')),
        ));
        sort($names);
        $events = [];
        foreach ($names as $name) {
            if (! class_exists($name)) {
                continue;
            }
            $r = new ReflectionClass($name);
            if ($r->isAbstract() || $r->isInterface() || $r->isEnum()) {
                continue;
            }
            $events[] = [
                'class' => $name,
                'file' => lm_rel($r->getFileName()),
                'line' => $r->getStartLine(),
                'doc' => lm_doc($r->getDocComment() ?: null),
                'params' => lm_params($r),
                'interfaces' => array_values(array_map(fn ($i) => substr(strrchr($i, '\\') ?: $i, 1) ?: $i, array_filter($r->getInterfaceNames(), fn ($i) => str_starts_with($i, 'Illuminate\\Contracts\\')))),
                'listeners' => array_map(static function ($l) {
                    $d = LaraMonsterTracker::describeListener($l);
                    $class = explode('@', $d['name'])[0];
                    if (class_exists($class)) {
                        $lr = new ReflectionClass($class);
                        $d['file'] = lm_rel($lr->getFileName());
                        $d['doc'] = lm_doc($lr->getDocComment() ?: null);
                        $props = $lr->getDefaultProperties();
                        $d['queue'] = $d['queued'] ? ($props['queue'] ?? null) : null;
                        $d['connection'] = $d['queued'] ? ($props['connection'] ?? null) : null;
                    }

                    return $d;
                }, $raw[$name] ?? []),
            ];
        }
        lm_out(['ok' => true, 'events' => $events]);

    case 'dispatch-event':
        $event = lm_build((string) ($input['class'] ?? ''), $input);
        $started = microtime(true);
        $responses = null;
        $error = null;
        try {
            $responses = event($event);
        } catch (Throwable $e) {
            $error = lm_exception($e);
        }
        lm_out([
            'ok' => $error === null,
            'event' => get_class($event),
            'payload' => LaraMonsterTracker::norm($event),
            'responses' => LaraMonsterTracker::norm($responses),
            'duration' => round((microtime(true) - $started) * 1000, 2),
            'error' => $error,
            'trace' => lm_trace(),
        ]);

    case 'jobs':
        $jobs = [];
        $skip = ['Listeners', 'Mail', 'Notifications', 'Events', 'Models', 'Http', 'Providers', 'Console'];
        $candidates = lm_classes_in(app_path('Jobs'));
        foreach (lm_classes_in(app_path()) as $c) {
            $rel = str_replace('\\', '/', substr($c, strlen(app()->getNamespace())));
            if (! in_array(explode('/', $rel)[0], $skip, true) && class_exists($c) && is_subclass_of($c, Illuminate\Contracts\Queue\ShouldQueue::class)) {
                $candidates[] = $c;
            }
        }
        foreach (array_unique($candidates) as $class) {
            if (! class_exists($class)) {
                continue;
            }
            $r = new ReflectionClass($class);
            if ($r->isAbstract() || ! $r->hasMethod('handle')) {
                continue;
            }
            $props = $r->getDefaultProperties();
            $handle = $r->getMethod('handle');
            $file = $r->getFileName();
            $lines = $file ? file($file) : [];
            $slice = fn (ReflectionFunctionAbstract $m) => $lines ? implode('', array_slice($lines, $m->getStartLine() - 1, $m->getEndLine() - $m->getStartLine() + 1)) : '';
            $jobs[] = [
                'class' => $class,
                'file' => lm_rel($file),
                'line' => $r->getStartLine(),
                'doc' => lm_doc($r->getDocComment() ?: null),
                'params' => lm_params($r),
                'queued' => is_subclass_of($class, Illuminate\Contracts\Queue\ShouldQueue::class),
                'interfaces' => array_values(array_map(fn ($i) => substr(strrchr($i, '\\'), 1), array_filter($r->getInterfaceNames(), fn ($i) => str_starts_with($i, 'Illuminate\\Contracts\\')))),
                'traits' => array_values(array_map(fn ($t) => substr(strrchr($t, '\\') ?: '\\'.$t, 1), $r->getTraitNames())),
                // (object): an empty list must still be a JSON object ({}), not [].
                'settings' => (object) array_filter([
                    'connection' => $props['connection'] ?? null,
                    'queue' => $props['queue'] ?? null,
                    'tries' => $props['tries'] ?? null,
                    'timeout' => $props['timeout'] ?? null,
                    'backoff' => $props['backoff'] ?? null,
                    'maxExceptions' => $props['maxExceptions'] ?? null,
                    'uniqueFor' => $props['uniqueFor'] ?? null,
                    'delay' => $props['delay'] ?? null,
                    'deleteWhenMissingModels' => $props['deleteWhenMissingModels'] ?? null,
                    'failOnTimeout' => $props['failOnTimeout'] ?? null,
                ], fn ($v) => $v !== null),
                'methods' => array_values(array_filter(['middleware', 'retryUntil', 'uniqueId', 'failed', 'backoff', 'tags'], fn ($m) => $r->hasMethod($m) && $r->getMethod($m)->getDeclaringClass()->getName() === $class)),
                'handle_params' => array_map(fn ($p) => ($p->getType() ? $p->getType().' ' : '').'$'.$p->getName(), $handle->getParameters()),
                'handle_source' => $slice($handle),
                'constructor_source' => $r->getConstructor() && $r->getConstructor()->getDeclaringClass()->getName() === $class ? $slice($r->getConstructor()) : '',
                // Helper methods handle() calls, so the analysis can follow them.
                'helpers' => (static function () use ($r, $class, $slice) {
                    $out = [];
                    foreach ($r->getMethods() as $m) {
                        if ($m->getDeclaringClass()->getName() === $class && ! in_array($m->getName(), ['handle', '__construct', 'failed', 'middleware', 'backoff', 'retryUntil', 'uniqueId', 'tags'], true)) {
                            $out[$m->getName()] = $slice($m);
                        }
                    }

                    return (object) $out;
                })(),
            ];
        }
        // When: the schedule, read from the app itself.
        $schedule = [];
        try {
            foreach (app(Illuminate\Console\Scheduling\Schedule::class)->events() as $ev) {
                $schedule[] = [
                    'expression' => $ev->expression,
                    'description' => $ev->description ?: ($ev->command ?? ''),
                    'command' => $ev->command,
                    'timezone' => is_string($ev->timezone) ? $ev->timezone : ($ev->timezone?->getName() ?? config('app.timezone')),
                    'next' => $ev->nextRunDate()->format(DATE_ATOM),
                    'without_overlapping' => (bool) $ev->withoutOverlapping,
                    'on_one_server' => (bool) $ev->onOneServer,
                ];
            }
        } catch (Throwable) {
        }
        lm_out(['ok' => true, 'jobs' => $jobs, 'schedule' => $schedule, 'default_connection' => config('queue.default'), 'timezone' => config('app.timezone')]);

    case 'run-job':
        $job = lm_build((string) ($input['class'] ?? ''), $input);
        $mode = $input['mode'] ?? 'sync';
        $started = microtime(true);
        $error = null;
        $id = null;
        ob_start();
        try {
            if ($mode === 'sync') {
                app(Illuminate\Contracts\Bus\Dispatcher::class)->dispatchSync($job);
            } else {
                if (! empty($input['connection']) && method_exists($job, 'onConnection')) {
                    $job->onConnection($input['connection']);
                }
                if (! empty($input['queue']) && method_exists($job, 'onQueue')) {
                    $job->onQueue($input['queue']);
                }
                if (! empty($input['delay']) && method_exists($job, 'delay')) {
                    $job->delay((int) $input['delay']);
                }
                $id = app(Illuminate\Contracts\Bus\Dispatcher::class)->dispatch($job);
            }
        } catch (Throwable $e) {
            $error = lm_exception($e);
        }
        $output = ob_get_clean();
        lm_out([
            'ok' => $error === null,
            'job' => get_class($job),
            'mode' => $mode,
            'id' => is_scalar($id) ? $id : null,
            'payload' => LaraMonsterTracker::norm($job),
            'duration' => round((microtime(true) - $started) * 1000, 2),
            'output' => mb_substr((string) $output, 0, 20000),
            'error' => $error,
            'trace' => lm_trace(),
        ]);

    case 'queues':
        $default = config('queue.default');
        $connections = [];
        foreach ((array) config('queue.connections') as $name => $c) {
            $connections[] = ['name' => $name, 'driver' => $c['driver'] ?? '?', 'queue' => $c['queue'] ?? 'default', 'default' => $name === $default];
        }
        $conn = ($input['connection'] ?? '') ?: $default;
        $driver = config("queue.connections.$conn.driver");
        $names = array_merge([config("queue.connections.$conn.queue", 'default')], (array) ($input['queues'] ?? []));
        // Queues that exist right now (queued listeners, jobs from other code).
        try {
            if ($driver === 'database') {
                $names = array_merge($names, Illuminate\Support\Facades\DB::connection(config("queue.connections.$conn.connection"))
                    ->table(config("queue.connections.$conn.table", 'jobs'))->distinct()->pluck('queue')->all());
            } elseif ($driver === 'redis') {
                $prefix = config('database.redis.options.prefix', '');
                foreach ((array) Illuminate\Support\Facades\Redis::connection(config("queue.connections.$conn.connection", 'default'))->keys('queues:*') as $key) {
                    $key = substr($key, strlen($prefix));
                    if (! preg_match('/:(delayed|reserved|notify)$/', $key)) {
                        $names[] = substr($key, 7);
                    }
                }
            }
        } catch (Throwable) {
        }
        $names = array_values(array_unique(array_filter($names)));
        $queues = [];
        $error = null;
        foreach ($names as $q) {
            $row = ['name' => $q, 'size' => null, 'jobs' => []];
            try {
                $row['size'] = Illuminate\Support\Facades\Queue::connection($conn)->size($q);
                if ($driver === 'database') {
                    $table = config("queue.connections.$conn.table", 'jobs');
                    $db = Illuminate\Support\Facades\DB::connection(config("queue.connections.$conn.connection"));
                    foreach ($db->table($table)->where('queue', $q)->orderBy('id')->limit(25)->get() as $j) {
                        $p = json_decode($j->payload, true) ?: [];
                        $row['jobs'][] = ['id' => $j->id, 'name' => $p['displayName'] ?? '?', 'attempts' => $j->attempts, 'available_at' => date(DATE_ATOM, $j->available_at), 'created_at' => date(DATE_ATOM, $j->created_at), 'reserved' => (bool) $j->reserved_at];
                    }
                } elseif ($driver === 'redis') {
                    $redis = Illuminate\Support\Facades\Redis::connection(config("queue.connections.$conn.connection", 'default'));
                    foreach ((array) $redis->lrange('queues:'.$q, 0, 24) as $raw) {
                        $p = json_decode($raw, true) ?: [];
                        $row['jobs'][] = ['id' => $p['uuid'] ?? $p['id'] ?? null, 'name' => $p['displayName'] ?? '?', 'attempts' => $p['attempts'] ?? 0, 'available_at' => null, 'created_at' => isset($p['createdAt']) ? date(DATE_ATOM, $p['createdAt']) : null, 'reserved' => false];
                    }
                }
            } catch (Throwable $e) {
                $error ??= lm_exception($e);
            }
            $queues[] = $row;
        }
        $failed = [];
        try {
            foreach (array_slice((array) app('queue.failer')->all(), 0, 50) as $f) {
                $f = (array) $f;
                $p = json_decode($f['payload'] ?? '{}', true) ?: [];
                $failed[] = [
                    'id' => $f['uuid'] ?? $f['id'],
                    'connection' => $f['connection'] ?? null,
                    'queue' => $f['queue'] ?? null,
                    'name' => $p['displayName'] ?? '?',
                    'exception' => mb_substr(strtok((string) ($f['exception'] ?? ''), "\n"), 0, 400),
                    'failed_at' => $f['failed_at'] ?? null,
                ];
            }
        } catch (Throwable $e) {
            $error ??= lm_exception($e);
        }
        lm_out(['ok' => true, 'default' => $default, 'connection' => $conn, 'driver' => $driver, 'connections' => $connections, 'queues' => $queues, 'failed' => $failed, 'error' => $error]);

    case 'queue-action':
        $action = $input['action'] ?? '';
        $map = [
            'retry' => ['queue:retry', ['id' => [(string) ($input['id'] ?? '')]]],
            'retry-all' => ['queue:retry', ['id' => ['all']]],
            'forget' => ['queue:forget', ['id' => (string) ($input['id'] ?? '')]],
            'flush' => ['queue:flush', []],
            'clear' => ['queue:clear', array_filter(['connection' => $input['connection'] ?? null, '--queue' => $input['queue'] ?? null, '--force' => true])],
        ];
        if (! isset($map[$action])) {
            throw new InvalidArgumentException("Unknown action {$action}.");
        }
        $code = Illuminate\Support\Facades\Artisan::call($map[$action][0], $map[$action][1]);
        lm_out(['ok' => $code === 0, 'output' => trim(Illuminate\Support\Facades\Artisan::output())]);

    default:
        throw new InvalidArgumentException("Unknown runner command {$command}.");
}
} catch (Throwable $e) {
    lm_out(['ok' => false, 'error' => lm_exception($e), 'trace' => lm_trace()]);
}
