<?php
/*
 * LaraMonster's event dispatcher: Laravel's own, plus timing. Swapped in by
 * the agent before anything uses the dispatcher, so every event is recorded
 * when it starts (parents before the events they cause), with each
 * listener's duration, return value and exception.
 */

use Illuminate\Events\Dispatcher;

final class LaraMonsterDispatcher extends Dispatcher
{
    public static function wrap(mixed $original): mixed
    {
        // Only plain Laravel dispatchers: a subclass from a package keeps its
        // own behaviour, and the agent falls back to a wildcard listener.
        if (! $original instanceof Dispatcher || get_class($original) !== Dispatcher::class) {
            return null;
        }
        [$container, $queue, $transactions] = (fn () => [$this->container, $this->queueResolver, $this->transactionManagerResolver])->call($original);
        $wrapped = new self($container);
        $wrapped->queueResolver = $queue;
        $wrapped->transactionManagerResolver = $transactions;

        return $wrapped;
    }

    public function dispatch($event, $payload = [], $halt = false)
    {
        $tracker = LaraMonsterTracker::current();
        if (! $tracker) {
            return parent::dispatch($event, $payload, $halt);
        }
        $name = is_object($event) ? get_class($event) : (string) $event;
        $index = $tracker->start($name, is_object($event) ? [$event] : (array) $payload, $this);
        try {
            $result = parent::dispatch($event, $payload, $halt);
        } catch (Throwable $e) {
            $tracker->end($index, null, $e);
            throw $e;
        }
        $tracker->end($index, $result, null);

        return $result;
    }

    public function makeListener($listener, $wildcard = false)
    {
        $callable = parent::makeListener($listener, $wildcard);
        if ($wildcard) {
            return $callable;
        }
        $describe = LaraMonsterTracker::describeListener($listener);

        return static function (...$args) use ($callable, $describe) {
            $tracker = LaraMonsterTracker::current();
            if (! $tracker) {
                return $callable(...$args);
            }
            $started = microtime(true);
            try {
                $result = $callable(...$args);
            } catch (Throwable $e) {
                $tracker->listenerRan($describe, $started, null, $e);
                throw $e;
            }
            $tracker->listenerRan($describe, $started, $result, null);

            return $result;
        };
    }
}
