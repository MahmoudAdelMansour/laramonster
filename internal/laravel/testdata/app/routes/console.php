<?php

use Illuminate\Foundation\Inspiring;
use Illuminate\Support\Facades\Artisan;

Artisan::command('inspire', function () {
    $this->comment(Inspiring::quote());
})->purpose('Display an inspiring quote');

use App\Jobs\GenerateDailyReport;
use Illuminate\Support\Facades\Schedule;

Schedule::job(new GenerateDailyReport)->dailyAt('02:00');
