<?php
namespace App\Http\Controllers;
use App\Jobs\SendReceiptEmail;
class OrderController {
    public function store() {
        SendReceiptEmail::dispatch($user, 10);
        dispatch(new \App\Jobs\SendReceiptEmail($user, 5));
    }
}
