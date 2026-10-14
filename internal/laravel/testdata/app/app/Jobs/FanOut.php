<?php
namespace App\Jobs;
class FanOut {
    public function handle() { Bus::batch(collect($ids)->map(fn ($c) => new FanOutChunk($c))->all())->dispatch(); }
}
