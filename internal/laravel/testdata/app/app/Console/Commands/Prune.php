<?php
namespace App\Console\Commands;
use App\Jobs\PruneTokens;
class Prune extends \Illuminate\Console\Command {
    protected $signature = 'tokens:prune {--days=30}';
    public function handle() { PruneTokens::dispatch(); }
}
