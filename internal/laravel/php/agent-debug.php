<?php
/*
 * LaraMonster Debugger (free): loaded by agent.php for sites that have the
 * debugger on. It records, per request or command, the dump() calls, the
 * queries, log messages and exceptions, and writes them as one JSON line.
 *
 * Like the event agent it never changes what the app does: dumps still show
 * in the page unless the site asked to keep them out, and every failure in
 * here is swallowed.
 */

if (! class_exists('LaraMonsterDebugger', false)) {
    final class LaraMonsterDebugger
    {
        public const MAX_DUMPS = 50;

        public const MAX_QUERIES = 150;

        public const MAX_LOGS = 50;

        public const MAX_DUMP_BYTES = 20000;

        private static ?self $instance = null;

        /** @var array<string, mixed> */
        private array $entry = [];

        private float $start = 0;

        private bool $busy = false;

        private function __construct(private string $base, private string $out, private bool $echo) {}

        public static function boot(string $base, string $out, bool $echo): void
        {
            self::$instance = new self($base, $out, $echo);
            self::$instance->begin();

            $composer = $base.DIRECTORY_SEPARATOR.'vendor'.DIRECTORY_SEPARATOR.'autoload.php';
            if (is_file($composer)) {
                require_once $composer;
            }
            // The same trick as the event agent: wait for the first class
            // loaded after Laravel created its application.
            $hook = static function (): void {
                static $attached = false;
                if ($attached || ! class_exists('Illuminate\\Foundation\\Application', false)) {
                    return;
                }
                try {
                    $app = (new ReflectionProperty('Illuminate\\Container\\Container', 'instance'))->getValue();
                    if (! $app instanceof Illuminate\Foundation\Application) {
                        return;
                    }
                    $attached = true;
                    // After the providers: Laravel installs its own dump
                    // handler while registering, which ours wraps.
                    $app->booted(static fn ($app) => self::$instance?->attach($app));
                } catch (Throwable) {
                }
            };
            spl_autoload_register($hook, true, true);
            register_shutdown_function(static fn () => self::$instance?->finish());
        }

        private function begin(): void
        {
            $this->start = microtime(true);
            $cli = PHP_SAPI === 'cli';
            $this->entry = [
                'id' => bin2hex(random_bytes(8)),
                'kind' => $cli ? 'command' : 'request',
                'started_at' => gmdate('Y-m-d\TH:i:s', (int) $this->start).sprintf('.%03dZ', ($this->start - floor($this->start)) * 1000),
                'php' => PHP_VERSION,
                'dumps' => [],
                'queries' => [],
                'query_count' => 0,
                'query_ms' => 0.0,
                'logs' => [],
            ];
            if ($cli) {
                $this->entry['title'] = implode(' ', array_slice($_SERVER['argv'] ?? [], 1)) ?: 'php';
            } else {
                $this->entry['method'] = $_SERVER['REQUEST_METHOD'] ?? 'GET';
                $this->entry['uri'] = $_SERVER['REQUEST_URI'] ?? '/';
                $this->entry['title'] = $this->entry['method'].' '.strtok($this->entry['uri'], '?');
                $this->entry['ajax'] = strtolower($_SERVER['HTTP_X_REQUESTED_WITH'] ?? '') === 'xmlhttprequest' || str_contains($_SERVER['HTTP_ACCEPT'] ?? '', 'application/json');
            }
        }

        private function attach($app): void
        {
            try {
                if (class_exists('Symfony\\Component\\VarDumper\\VarDumper')) {
                    $previous = null;
                    $previous = Symfony\Component\VarDumper\VarDumper::setHandler(function (...$args) use (&$previous) {
                        $this->dump($args[0] ?? null, isset($args[1]) && is_string($args[1]) ? $args[1] : null);
                        if ($this->echo && $previous) {
                            return $previous(...$args);
                        }
                        if ($this->echo) {
                            // No handler yet: what Symfony does by default.
                            $cloner = new Symfony\Component\VarDumper\Cloner\VarCloner;
                            $dumper = PHP_SAPI === 'cli' ? new Symfony\Component\VarDumper\Dumper\CliDumper : new Symfony\Component\VarDumper\Dumper\HtmlDumper;
                            $dumper->dump($cloner->cloneVar($args[0] ?? null));
                        }

                        return null;
                    });
                }
                $events = $app['events'];
                $events->listen('Illuminate\\Database\\Events\\QueryExecuted', fn ($q) => $this->query($q));
                $events->listen('Illuminate\\Log\\Events\\MessageLogged', fn ($e) => $this->log($e));
                // A worker runs many jobs: each one is its own entry.
                $events->listen('Illuminate\\Queue\\Events\\JobProcessing', function ($e): void {
                    $this->flush();
                    $this->begin();
                    $this->entry['kind'] = 'job';
                    $this->entry['title'] = $e->job->resolveName();
                });
                $events->listen(['Illuminate\\Queue\\Events\\JobProcessed', 'Illuminate\\Queue\\Events\\JobFailed'], function ($e): void {
                    if (($this->entry['kind'] ?? '') !== 'job') {
                        return;
                    }
                    $this->entry['status'] = isset($e->exception) ? 'failed' : 'done';
                    if (isset($e->exception) && empty($this->entry['exception'])) {
                        $this->entry['exception'] = self::exception($e->exception, $this->base);
                    }
                    $this->flush();
                    $this->begin();
                    $this->entry['title'] = 'queue:work';
                });
                $events->listen('Illuminate\\Foundation\\Http\\Events\\RequestHandled', function ($e): void {
                    try {
                        $this->entry['status'] = $e->response->getStatusCode();
                        if ($route = $e->request->route()) {
                            $this->entry['route'] = $route->getName() ?: $route->uri();
                            $this->entry['action'] = $route->getActionName();
                        }
                        if ($user = $e->request->user()) {
                            $this->entry['user'] = method_exists($user, 'getAuthIdentifier') ? (string) $user->getAuthIdentifier() : null;
                        }
                    } catch (Throwable) {
                    }
                });
            } catch (Throwable) {
            }
        }

        private function dump(mixed $value, ?string $label): void
        {
            if ($this->busy || count($this->entry['dumps']) >= self::MAX_DUMPS) {
                return;
            }
            $this->busy = true;
            try {
                $cloner = new Symfony\Component\VarDumper\Cloner\VarCloner;
                $cloner->setMaxItems(500);
                $dumper = new Symfony\Component\VarDumper\Dumper\CliDumper;
                $dumper->setColors(false);
                $text = (string) $dumper->dump($cloner->cloneVar($value), true);
                if (strlen($text) > self::MAX_DUMP_BYTES) {
                    $text = substr($text, 0, self::MAX_DUMP_BYTES)."\n… (cut: the dump is longer)";
                }
                $this->entry['dumps'][] = [
                    'at' => round((microtime(true) - $this->start) * 1000, 2),
                    'label' => $label,
                    'type' => get_debug_type($value),
                    'text' => rtrim($text),
                    'src' => $this->source(),
                ];
            } catch (Throwable) {
            } finally {
                $this->busy = false;
            }
        }

        private function query(mixed $q): void
        {
            $this->entry['query_count']++;
            $ms = is_numeric($q->time ?? null) ? (float) $q->time : 0.0;
            $this->entry['query_ms'] = round($this->entry['query_ms'] + $ms, 2);
            if (count($this->entry['queries']) >= self::MAX_QUERIES) {
                return;
            }
            try {
                $bindings = [];
                foreach (array_slice((array) ($q->bindings ?? []), 0, 30) as $b) {
                    $bindings[] = is_scalar($b) || $b === null ? (is_string($b) ? mb_substr($b, 0, 200) : $b) : (string) json_encode($b);
                }
                $this->entry['queries'][] = [
                    'sql' => mb_substr(preg_replace('/\s+/', ' ', trim((string) $q->sql)), 0, 4000),
                    'bindings' => $bindings,
                    'ms' => round($ms, 2),
                    'conn' => (string) ($q->connectionName ?? ''),
                    'src' => $this->source(),
                ];
            } catch (Throwable) {
            }
        }

        private function log(mixed $e): void
        {
            try {
                $error = $e->context['exception'] ?? null;
                if ($error instanceof Throwable && empty($this->entry['exception'])) {
                    $this->entry['exception'] = self::exception($error, $this->base);
                }
                if (count($this->entry['logs']) >= self::MAX_LOGS) {
                    return;
                }
                $context = $e->context;
                unset($context['exception']);
                $this->entry['logs'][] = [
                    'level' => (string) $e->level,
                    'message' => mb_substr((string) $e->message, 0, 2000),
                    'context' => $context ? mb_substr((string) json_encode(class_exists('LaraMonsterTracker', false) ? LaraMonsterTracker::norm($context) : $context, JSON_UNESCAPED_SLASHES | JSON_PARTIAL_OUTPUT_ON_ERROR), 0, 2000) : null,
                    'at' => round((microtime(true) - $this->start) * 1000, 2),
                ];
            } catch (Throwable) {
            }
        }

        /** The first frame in the app itself: who called dump() or ran the query. */
        private function source(): ?string
        {
            $vendor = DIRECTORY_SEPARATOR.'vendor'.DIRECTORY_SEPARATOR;
            foreach (debug_backtrace(DEBUG_BACKTRACE_IGNORE_ARGS, 60) as $frame) {
                $file = $frame['file'] ?? '';
                if ($file === '' || dirname($file) === __DIR__ || str_contains($file, $vendor)) {
                    continue;
                }

                return self::relative($file, $this->base).':'.($frame['line'] ?? 0);
            }

            return null;
        }

        /** @return array<string, mixed> */
        public static function exception(Throwable $e, string $base): array
        {
            $frames = [];
            foreach (array_slice($e->getTrace(), 0, 30) as $f) {
                if (! isset($f['file'])) {
                    continue;
                }
                $frames[] = [
                    'file' => self::relative($f['file'], $base),
                    'line' => $f['line'] ?? 0,
                    'call' => isset($f['class']) ? $f['class'].($f['type'] ?? '::').$f['function'] : ($f['function'] ?? ''),
                    'vendor' => str_contains($f['file'], DIRECTORY_SEPARATOR.'vendor'.DIRECTORY_SEPARATOR),
                ];
            }
            $out = [
                'class' => get_class($e),
                'message' => mb_substr($e->getMessage(), 0, 2000),
                'file' => self::relative($e->getFile(), $base),
                'line' => $e->getLine(),
                'frames' => $frames,
            ];
            if ($prev = $e->getPrevious()) {
                $out['previous'] = ['class' => get_class($prev), 'message' => mb_substr($prev->getMessage(), 0, 500)];
            }

            return $out;
        }

        private static function relative(string $file, string $base): string
        {
            return str_starts_with($file, $base.DIRECTORY_SEPARATOR) ? substr($file, strlen($base) + 1) : $file;
        }

        public function finish(): void
        {
            $this->flush();
        }

        private function flush(): void
        {
            try {
                $fatal = error_get_last();
                if ($fatal && in_array($fatal['type'], [E_ERROR, E_PARSE, E_CORE_ERROR, E_COMPILE_ERROR], true) && empty($this->entry['exception'])) {
                    $this->entry['exception'] = ['class' => 'Fatal error', 'message' => mb_substr($fatal['message'], 0, 2000),
                        'file' => self::relative($fatal['file'], $this->base), 'line' => $fatal['line'], 'frames' => []];
                    $this->entry['status'] = $this->entry['status'] ?? 500;
                }
                if ($this->entry['kind'] === 'request' && empty($this->entry['status'])) {
                    $this->entry['status'] = http_response_code() ?: 200;
                }
                $this->entry['duration'] = round((microtime(true) - $this->start) * 1000, 2);
                $this->entry['memory'] = memory_get_peak_usage(true);
                // Every web request and job; a command only when it shows something.
                if ($this->entry['kind'] === 'command' && ! $this->entry['dumps'] && ! $this->entry['logs'] && empty($this->entry['exception'])) {
                    return;
                }
                $line = json_encode($this->entry, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PARTIAL_OUTPUT_ON_ERROR | JSON_INVALID_UTF8_SUBSTITUTE);
                if ($line !== false) {
                    @file_put_contents($this->out, $line."\n", FILE_APPEND | LOCK_EX);
                }
            } catch (Throwable) {
            }
        }
    }
}
