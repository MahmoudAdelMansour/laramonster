<?php
/*
 * LaraMonster Event Tracker agent.
 *
 * Loaded before every PHP request of a site that has event tracking on
 * (php-fpm auto_prepend_file), and by LaraMonster's runner. It waits for
 * Laravel to create its application, then listens to every event and writes
 * one "trace" per request, command or queued job as a JSON line.
 *
 * It never changes what the app does: listeners see the same events, and
 * every failure inside the agent is swallowed.
 */

if (! defined('LARAMONSTER_AGENT')) {
    define('LARAMONSTER_AGENT', '1');

    final class LaraMonsterTracker
    {
        public const MAX_EVENTS = 250;

        /** Framework events that only add noise to a trace. */
        private const SKIP = [
            'Illuminate\\Database\\Events\\QueryExecuted', 'Illuminate\\Database\\Events\\StatementPrepared',
            'Illuminate\\Database\\Events\\ConnectionEstablished', 'Illuminate\\Database\\Events\\TransactionBeginning',
            'Illuminate\\Database\\Events\\TransactionCommitted', 'Illuminate\\Database\\Events\\TransactionCommitting',
            'Illuminate\\Log\\Events\\MessageLogged', 'Illuminate\\Foundation\\Events\\LocaleUpdated',
            'Illuminate\\Routing\\Events\\PreparingResponse', 'Illuminate\\Routing\\Events\\ResponsePrepared',
            'Illuminate\\Routing\\Events\\Routing', 'Illuminate\\Cache\\Events\\CacheHit', 'Illuminate\\Cache\\Events\\CacheMissed',
            'Illuminate\\Cache\\Events\\RetrievingKey', 'Illuminate\\Cache\\Events\\RetrievingManyKeys', 'Illuminate\\Cache\\Events\\KeyWritten',
            'Illuminate\\Cache\\Events\\WritingKey', 'Illuminate\\Cache\\Events\\KeyForgotten', 'Illuminate\\Cache\\Events\\ForgettingKey',
            'Illuminate\\Foundation\\Http\\Events\\RequestHandled', 'Illuminate\\Console\\Events\\ScheduledTaskStarting',
            'Illuminate\\Queue\\Events\\Looping', 'Illuminate\\Queue\\Events\\JobPopping', 'Illuminate\\Queue\\Events\\JobPopped',
            'Illuminate\\Queue\\Events\\JobAttempted', 'Illuminate\\Mail\\Events\\MessageSending',
            'Illuminate\\Notifications\\Events\\NotificationSending', 'Illuminate\\Http\\Client\\Events\\RequestSending',
            'Illuminate\\Console\\Events\\ArtisanStarting', 'Illuminate\\Database\\Events\\MigrationsStarted',
            'Illuminate\\Queue\\Events\\JobQueueing', 'Illuminate\\Foundation\\Events\\Terminating',
        ];

        private static ?self $instance = null;

        /** @var array<string, mixed> */
        public array $trace = [];

        private float $start = 0;

        private bool $recording = false;

        private int $queries = 0;

        /** @var array<string, array<string, mixed>> queries by SQL and caller, for Performance */
        private array $sql = [];

        public const MAX_SQL = 200;

        private function __construct(public string $base, public ?string $out, public bool $inMemory) {}

        public static function boot(string $base, ?string $out, bool $inMemory): void
        {
            self::$instance = new self($base, $out, $inMemory);
            self::$instance->begin(PHP_SAPI === 'cli' ? 'command' : 'request', self::context());

            // Composer puts its autoloader in front of the queue, so load it
            // first (it's safe to load twice), then put this hook in front of
            // it: the hook sees the first class loaded after the app exists.
            $composer = $base.DIRECTORY_SEPARATOR.'vendor'.DIRECTORY_SEPARATOR.'autoload.php';
            if (is_file($composer)) {
                require_once $composer;
            }
            // It stays registered (removing an autoloader while PHP is
            // running the queue skips the rest of it); after attaching it
            // returns on the first line.
            $hook = static function (): void {
                static $attached = false;
                if ($attached || ! class_exists('Illuminate\\Foundation\\Application', false)) {
                    return;
                }
                try {
                    $app = (new ReflectionProperty('Illuminate\\Container\\Container', 'instance'))->getValue();
                    // As soon as the app exists: an extender registered before the
                    // dispatcher is first resolved is applied when it is.
                    if (! $app instanceof Illuminate\Foundation\Application) {
                        return;
                    }
                    $attached = true;
                    $wildcard = static fn ($dispatcher) => $dispatcher->listen('*', static function ($name, $payload) use ($dispatcher): void {
                        $tracker = self::$instance;
                        $tracker?->end($tracker->start($name, $payload, $dispatcher), null, null);
                    });
                    if ($app->resolved('events')) {
                        $wildcard($app['events']);
                    } else {
                        require_once __DIR__.DIRECTORY_SEPARATOR.'agent-dispatcher.php';
                        $app->extend('events', static function ($dispatcher) use ($wildcard) {
                            $wrapped = LaraMonsterDispatcher::wrap($dispatcher);
                            if ($wrapped === null) {
                                $wildcard($dispatcher);
                            }

                            return $wrapped ?? $dispatcher;
                        });
                    }
                } catch (Throwable) {
                }
            };
            spl_autoload_register($hook, true, true);

            register_shutdown_function(static fn () => self::$instance?->finish());
        }

        public static function current(): ?self
        {
            return self::$instance;
        }

        /** @return array<string, mixed> */
        private static function context(): array
        {
            if (PHP_SAPI === 'cli') {
                return ['title' => implode(' ', array_slice($_SERVER['argv'] ?? [], 1)) ?: 'php'];
            }

            return [
                'method' => $_SERVER['REQUEST_METHOD'] ?? 'GET',
                'uri' => $_SERVER['REQUEST_URI'] ?? '/',
                'title' => ($_SERVER['REQUEST_METHOD'] ?? 'GET').' '.strtok($_SERVER['REQUEST_URI'] ?? '/', '?'),
            ];
        }

        /** @param array<string, mixed> $context */
        public function begin(string $kind, array $context): void
        {
            $this->start = microtime(true);
            $this->queries = 0;
            $this->sql = [];
            $this->trace = [
                'id' => bin2hex(random_bytes(8)),
                'kind' => $kind,
                'started_at' => gmdate('Y-m-d\TH:i:s', (int) $this->start).sprintf('.%03dZ', ($this->start - floor($this->start)) * 1000),
                'php' => PHP_VERSION,
                'events' => [],
            ] + $context;
        }

        /** @var list<?int> indexes of the events being dispatched right now */
        private array $stack = [];

        /**
         * An event starts. Returns its index in the trace, or null when it
         * isn't recorded (noise, or the trace is full).
         */
        public function start(string $name, array $payload, $dispatcher): ?int
        {
            if ($this->recording) {
                return null; // an event fired while reading a payload
            }
            if ($name === 'Illuminate\\Database\\Events\\QueryExecuted') {
                $this->queries++;
                try {
                    $this->query($payload[0] ?? null);
                } catch (Throwable) {
                }
                $this->stack[] = null;

                return null;
            }
            $this->recording = true;
            $index = null;
            try {
                $this->observe($name, $payload);
                $noise = in_array($name, self::SKIP, true) || $this->isNoisyModelEvent($name)
                    || str_starts_with($name, 'bootstrapping: ') || str_starts_with($name, 'bootstrapped: ')
                    || str_starts_with($name, 'Illuminate\\Log\\Context\\');
                if (! $noise && count($this->trace['events']) < self::MAX_EVENTS) {
                    $event = $payload[0] ?? null;
                    $isObject = is_object($event) && count($payload) === 1;
                    $parent = null;
                    for ($i = count($this->stack) - 1; $i >= 0; $i--) {
                        if ($this->stack[$i] !== null) {
                            $parent = $this->stack[$i];
                            break;
                        }
                    }
                    $this->trace['events'][] = [
                        'name' => $name,
                        'at' => round((microtime(true) - $this->start) * 1000, 2),
                        'duration' => null,
                        'depth' => count(array_filter($this->stack, fn ($s) => $s !== null)),
                        'parent' => $parent,
                        'app' => ! str_starts_with($name, 'Illuminate\\') && ! str_starts_with($name, 'eloquent.') && ! str_starts_with($name, 'Laravel\\'),
                        'payload' => $isObject ? self::norm($event) : self::norm($payload),
                        'listeners' => $this->listenersFor($name, $dispatcher),
                        'ran' => [],
                        'source' => self::source(),
                    ];
                    $index = count($this->trace['events']) - 1;
                } elseif (! $noise) {
                    $this->trace['truncated'] = true;
                }
            } catch (Throwable) {
            } finally {
                $this->recording = false;
            }
            $this->stack[] = $index;

            return $index;
        }

        /** The event finished: how long it took, what listeners returned, or what it threw. */
        public function end(?int $index, mixed $responses, ?Throwable $error): void
        {
            array_pop($this->stack);
            if ($index === null || ! isset($this->trace['events'][$index])) {
                return;
            }
            $this->recording = true;
            try {
                $e = &$this->trace['events'][$index];
                $e['duration'] = round((microtime(true) - $this->start) * 1000 - $e['at'], 2);
                if (is_array($responses) && array_filter($responses, fn ($r) => $r !== null) !== []) {
                    $e['responses'] = self::norm($responses);
                } elseif ($responses !== null && ! is_array($responses)) {
                    $e['responses'] = self::norm([$responses]);
                }
                if ($error) {
                    $e['exception'] = self::exception($error);
                }
            } catch (Throwable) {
            } finally {
                $this->recording = false;
            }
        }

        /** @param array{name: string, queued: bool} $listener */
        public function listenerRan(array $listener, float $started, mixed $result, ?Throwable $error): void
        {
            // The event being dispatched right now: a listener of an
            // unrecorded event (a query log, say) belongs to nobody.
            $index = $this->stack === [] ? null : $this->stack[count($this->stack) - 1];
            if ($index === null || ! isset($this->trace['events'][$index]) || count($this->trace['events'][$index]['ran']) >= 50) {
                return;
            }
            $this->recording = true;
            try {
                $this->trace['events'][$index]['ran'][] = $listener + [
                    'duration' => round((microtime(true) - $started) * 1000, 2),
                    'result' => $result === null ? null : self::norm($result, 2),
                    'exception' => $error ? self::exception($error) : null,
                ];
            } catch (Throwable) {
            } finally {
                $this->recording = false;
            }
        }

        /** Context from framework events: the response, the command, queue job boundaries. */
        private function observe(string $name, array $payload): void
        {
            $event = $payload[0] ?? null;
            switch ($name) {
                case 'Illuminate\\Foundation\\Http\\Events\\RequestHandled':
                    $this->trace['status'] = $event->response->getStatusCode();
                    if ($route = $event->request->route()) {
                        $this->trace['route'] = $route->getName() ?: $route->uri();
                        $this->trace['action'] = $route->getActionName();
                    }
                    break;
                case 'Illuminate\\Console\\Events\\CommandStarting':
                    if ($event->command && $event->command !== 'queue:work' && $event->command !== 'queue:listen') {
                        $this->trace['command'] = $event->command;
                    }
                    break;
                case 'Illuminate\\Console\\Events\\CommandFinished':
                    $this->trace['exit_code'] = $event->exitCode;
                    break;
                case 'Illuminate\\Queue\\Events\\JobProcessing':
                    // A worker runs many jobs: each one becomes its own trace.
                    $this->flush();
                    $this->begin('job', [
                        'title' => $event->job->resolveName(),
                        'job' => $event->job->resolveName(),
                        'queue' => $event->job->getQueue(),
                        'connection' => $event->connectionName,
                        'attempt' => $event->job->attempts(),
                    ]);
                    break;
                case 'Illuminate\\Queue\\Events\\JobProcessed':
                case 'Illuminate\\Queue\\Events\\JobFailed':
                    if (($this->trace['kind'] ?? '') === 'job') {
                        $this->trace['status'] = $name === 'Illuminate\\Queue\\Events\\JobProcessed' ? 'done' : 'failed';
                        if (isset($event->exception)) {
                            $this->trace['exception'] = self::exception($event->exception);
                        }
                        $this->flush();
                        $this->begin('command', ['title' => 'queue:work']);
                    }
                    break;
                case 'Illuminate\\Log\\Events\\MessageLogged':
                    if (($event->context['exception'] ?? null) instanceof Throwable && empty($this->trace['exception'])) {
                        $this->trace['exception'] = self::exception($event->context['exception']);
                    }
                    break;
            }
        }

        private function isNoisyModelEvent(string $name): bool
        {
            return str_starts_with($name, 'eloquent.') && ! preg_match('/^eloquent\.(created|updated|deleted|restored|forceDeleted): /', $name);
        }

        /** @return list<array{name: string, queued: bool}> */
        private function listenersFor(string $name, $dispatcher): array
        {
            $out = [];
            try {
                $raw = method_exists($dispatcher, 'getRawListeners') ? ($dispatcher->getRawListeners()[$name] ?? []) : [];
                foreach ($raw as $listener) {
                    $l = self::describeListener($listener);
                    // The Debugger's own listeners aren't the app's.
                    if (! str_contains($l['name'], 'agent-debug.php')) {
                        $out[] = $l;
                    }
                }
            } catch (Throwable) {
            }

            return $out;
        }

        /** @return array{name: string, queued: bool} */
        public static function describeListener(mixed $listener): array
        {
            $class = null;
            if (is_string($listener)) {
                $class = explode('@', $listener)[0];
                $name = $listener;
            } elseif (is_array($listener)) {
                $class = is_object($listener[0]) ? get_class($listener[0]) : $listener[0];
                $name = $class.'@'.($listener[1] ?? 'handle');
            } elseif ($listener instanceof Closure) {
                $fn = new ReflectionFunction($listener);
                $name = 'Closure '.self::relative($fn->getFileName()).':'.$fn->getStartLine();
            } else {
                $class = get_class($listener);
                $name = $class;
            }
            $queued = $class !== null && class_exists($class) && is_subclass_of($class, 'Illuminate\\Contracts\\Queue\\ShouldQueue');

            return ['name' => $name, 'queued' => $queued];
        }

        public function finish(): void
        {
            if (PHP_SAPI !== 'cli' && empty($this->trace['status'])) {
                $this->trace['status'] = http_response_code() ?: 200;
            }
            $this->flush();
        }

        public function flush(): void
        {
            $this->trace['duration'] = round((microtime(true) - $this->start) * 1000, 2);
            $this->trace['queries'] = $this->queries;
            $this->trace['memory'] = memory_get_peak_usage(true);
            $this->trace['sql'] = array_values($this->sql);
            $hasApp = false;
            foreach ($this->trace['events'] as $e) {
                $hasApp = $hasApp || $e['app'];
            }
            // Every web request (Performance needs them all); commands only
            // when they show something: an app event, a query or a failure.
            $kind = $this->trace['kind'] ?? '';
            if ($this->inMemory || ! $this->out || ($kind !== 'request' && $kind !== 'job' && ! $hasApp && $this->queries === 0 && empty($this->trace['exception']))) {
                return;
            }
            $line = json_encode($this->trace, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PARTIAL_OUTPUT_ON_ERROR | JSON_INVALID_UTF8_SUBSTITUTE);
            if ($line !== false) {
                @file_put_contents($this->out, $line."\n", FILE_APPEND | LOCK_EX);
            }
            $this->trace['events'] = [];
        }

        /**
         * Counts a query by its SQL and the code that ran it: the first frame
         * in the app, and the first package (not Laravel itself) on the way,
         * which shows third-party code that runs queries. Repeats of one
         * query from one line in one request are how N+1 problems look.
         */
        private function query(mixed $q): void
        {
            if (! is_object($q) || ! isset($q->sql)) {
                return;
            }
            $sql = preg_replace('/\s+/', ' ', trim((string) $q->sql));
            $sql = preg_replace('/\(\s*\?(\s*,\s*\?)+\s*\)/', '(?, …)', $sql);
            $sql = mb_substr($sql, 0, 2000);
            $src = null;
            $pkg = null;
            $vendor = DIRECTORY_SEPARATOR.'vendor'.DIRECTORY_SEPARATOR;
            foreach (debug_backtrace(DEBUG_BACKTRACE_IGNORE_ARGS, 60) as $frame) {
                $file = $frame['file'] ?? '';
                if ($file === '' || dirname($file) === __DIR__) {
                    continue;
                }
                $at = strpos($file, $vendor);
                if ($at === false) {
                    $src = self::relative($file).':'.($frame['line'] ?? 0);
                    break;
                }
                if ($pkg === null) {
                    $parts = explode(DIRECTORY_SEPARATOR, substr($file, $at + strlen($vendor)), 3);
                    $name = ($parts[0] ?? '').'/'.($parts[1] ?? '');
                    if (! in_array($parts[0] ?? '', ['laravel', 'illuminate', 'composer', 'symfony', 'nesbot', 'psr'], true)) {
                        $pkg = $name;
                    }
                }
            }
            $key = $sql.'|'.$src;
            $ms = is_numeric($q->time ?? null) ? (float) $q->time : 0.0;
            if (isset($this->sql[$key])) {
                $this->sql[$key]['n']++;
                $this->sql[$key]['ms'] = round($this->sql[$key]['ms'] + $ms, 2);
                $this->sql[$key]['max'] = max($this->sql[$key]['max'], $ms);

                return;
            }
            if (count($this->sql) >= self::MAX_SQL) {
                return;
            }
            $bindings = [];
            foreach (array_slice((array) ($q->bindings ?? []), 0, 20) as $b) {
                $bindings[] = is_scalar($b) || $b === null ? (is_string($b) ? mb_substr($b, 0, 100) : $b) : (string) json_encode(self::norm($b));
            }
            $this->sql[$key] = [
                'sql' => $sql, 'n' => 1, 'ms' => round($ms, 2), 'max' => $ms, 'src' => $src, 'pkg' => $pkg,
                'conn' => (string) ($q->connectionName ?? ''), 'bindings' => $bindings,
            ];
        }

        /** The first frame outside vendor/ and this agent: who fired the event. */
        private static function source(): ?string
        {
            foreach (debug_backtrace(DEBUG_BACKTRACE_IGNORE_ARGS, 40) as $frame) {
                $file = $frame['file'] ?? '';
                if ($file && ! str_contains($file, DIRECTORY_SEPARATOR.'vendor'.DIRECTORY_SEPARATOR) && dirname($file) !== __DIR__) {
                    return self::relative($file).':'.($frame['line'] ?? 0);
                }
            }

            return null;
        }

        public static function relative(string $file): string
        {
            $base = self::$instance?->base;

            return $base && str_starts_with($file, $base.DIRECTORY_SEPARATOR) ? substr($file, strlen($base) + 1) : $file;
        }

        /** @return array{class: string, message: string, file: string, line: int} */
        public static function exception(Throwable $e): array
        {
            return ['class' => get_class($e), 'message' => mb_substr($e->getMessage(), 0, 1000), 'file' => self::relative($e->getFile()), 'line' => $e->getLine()];
        }

        /** Readable, bounded, secret-free copy of a value. */
        public static function norm(mixed $v, int $depth = 0, ?string $key = null): mixed
        {
            if ($key !== null && is_scalar($v) && $v !== '' && preg_match('/pass(word)?|secret|token|api[_-]?key|authorization|private[_-]?key/i', $key)) {
                return '••••••';
            }
            if ($v === null || is_bool($v) || is_int($v)) {
                return $v;
            }
            if (is_float($v)) {
                return is_finite($v) ? $v : (string) $v;
            }
            if (is_string($v)) {
                return mb_strlen($v) > 500 ? mb_substr($v, 0, 500).'…' : $v;
            }
            if (is_resource($v)) {
                return '(resource)';
            }
            if ($depth >= 4) {
                return is_array($v) ? '(array of '.count($v).')' : '('.get_class($v).')';
            }
            if (is_array($v)) {
                $out = [];
                $i = 0;
                foreach ($v as $k => $item) {
                    if (++$i > 25) {
                        $out['…'] = (count($v) - 25).' more';
                        break;
                    }
                    $out[$k] = self::norm($item, $depth + 1, is_string($k) ? $k : null);
                }

                return $out;
            }
            if ($v instanceof Closure) {
                return '(Closure)';
            }
            if ($v instanceof DateTimeInterface) {
                return $v->format('Y-m-d H:i:s T');
            }
            if ($v instanceof UnitEnum) {
                return get_class($v).'::'.$v->name;
            }
            if ($v instanceof Illuminate\Database\Eloquent\Model) {
                $attrs = array_diff_key($v->attributesToArray(), array_flip($v->getHidden()));

                return ['@model' => get_class($v), '@key' => $v->getKey(), 'attributes' => self::norm(array_slice($attrs, 0, 25, true), $depth + 1)]
                    + ($v->getRelations() ? ['@relations' => array_keys($v->getRelations())] : []);
            }
            if ($v instanceof Illuminate\Support\Collection) {
                return ['@collection' => get_class($v), 'count' => $v->count(), 'items' => self::norm($v->take(5)->all(), $depth + 1)];
            }
            if ($v instanceof Illuminate\Http\Request || $v instanceof Symfony\Component\HttpFoundation\Response || $v instanceof Illuminate\Contracts\Container\Container) {
                return ['@object' => get_class($v)];
            }
            $out = ['@object' => get_class($v)];
            foreach (get_object_vars($v) as $k => $item) {
                $out[$k] = self::norm($item, $depth + 1, $k);
            }

            return $out;
        }
    }

    (static function (): void {
        try {
            if (getenv('LARAMONSTER_AGENT_CAPTURE')) {
                // The runner: record in memory and report in its own output.
                LaraMonsterTracker::boot((string) getenv('LARAMONSTER_AGENT_CAPTURE'), null, true);

                return;
            }
            // A load test's requests are measured from outside: recording
            // them would flood the lists and slow the page being measured.
            if (($_SERVER['HTTP_USER_AGENT'] ?? '') === 'LaraMonster load test') {
                return;
            }
            $script = PHP_SAPI === 'cli' ? ($_SERVER['SCRIPT_FILENAME'] ?? '') : ($_SERVER['SCRIPT_FILENAME'] ?? '');
            $real = $script !== '' ? realpath($script) : false;
            if (! $real) {
                return;
            }
            // public/index.php for web requests, artisan for commands and workers.
            $base = basename($real) === 'artisan' ? dirname($real) : dirname($real, 2);
            // The Debugger (free): dumps, queries, logs and exceptions.
            $debug = __DIR__.DIRECTORY_SEPARATOR.'debug.json';
            if (is_file($debug)) {
                $site = (json_decode((string) file_get_contents($debug), true) ?: [])[$base] ?? null;
                if (is_array($site) && is_string($site['out'] ?? null)) {
                    require_once __DIR__.DIRECTORY_SEPARATOR.'agent-debug.php';
                    LaraMonsterDebugger::boot($base, $site['out'], (bool) ($site['echo'] ?? true));
                }
            }
            $config = __DIR__.DIRECTORY_SEPARATOR.'tracking.json';
            if (! is_file($config)) {
                return;
            }
            $sites = json_decode((string) file_get_contents($config), true);
            $out = $sites[$base] ?? null;
            if (is_string($out) && $out !== '') {
                LaraMonsterTracker::boot($base, $out, false);
            }
        } catch (Throwable) {
        }
    })();
}
