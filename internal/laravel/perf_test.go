package laravel

import "testing"

func TestAnalyzePerf(t *testing.T) {
	traces := []Trace{
		{ID: "a", Kind: "request", Method: "GET", Route: "posts", Duration: 120, Queries: 22, Memory: 8 << 20,
			SQL: []TraceQuery{
				{SQL: `select * from "posts"`, N: 1, MS: 2, Max: 2, Src: "app/Http/Controllers/PostController.php:14"},
				{SQL: `select * from "users" where "users"."id" = ? limit 1`, N: 20, MS: 40, Max: 3, Src: "resources/views/posts/index.blade.php:9"},
				{SQL: `select * from "roles"`, N: 1, MS: 150, Max: 150, Pkg: "spatie/laravel-permission"},
				{SQL: `insert into "logs" ("line") values (?)`, N: 30, MS: 9, Max: 1, Src: "app/Jobs/Import.php:20"},
			},
			Events: []TraceEvent{{Name: "App\\Events\\Viewed", Ran: []RanListener{{Name: "Vendor\\Pkg\\Listen@handle", Duration: 12}}}}},
		{ID: "b", Kind: "request", Method: "GET", URI: "/posts/42?x=1", Duration: 30, Queries: 2},
		{ID: "c", Kind: "command", Duration: 999},
	}
	r := AnalyzePerf(traces, func(c string) string {
		if c == "Vendor\\Pkg\\Listen" {
			return "vendor/pkg"
		}
		return ""
	})
	if r.Requests != 2 || r.AvgMS != 75 || r.AvgQueries != 12 {
		t.Fatalf("totals: %+v", r)
	}
	if len(r.Routes) != 2 || r.Routes[0].Route != "GET /posts" || r.Routes[1].Route != "GET /posts/{id}" {
		t.Errorf("routes: %+v", r.Routes)
	}
	if len(r.NPlusOne) != 1 || r.NPlusOne[0].PerRequest != 20 || r.NPlusOne[0].Relation != "user" || r.NPlusOne[0].Trace != "a" {
		t.Errorf("n+1: %+v", r.NPlusOne)
	}
	if len(r.Slow) != 1 || r.Slow[0].MaxMS != 150 || r.Slow[0].Pkg != "spatie/laravel-permission" {
		t.Errorf("slow: %+v", r.Slow)
	}
	if len(r.Packages) != 2 || r.Packages[0].Package != "spatie/laravel-permission" || r.Packages[1].ListenerMS != 12 {
		t.Errorf("packages: %+v", r.Packages)
	}
}

func TestGuessRelation(t *testing.T) {
	for sql, want := range map[string]string{
		`select * from "comments" where "comments"."post_id" = ? and "comments"."post_id" is not null`: "comments",
		"select * from `users` where `users`.`id` = ? limit 1":                                         "user",
		`select * from "categories" where "categories"."id" in (?, …)`:                                 "category",
		`select * from "order_items" where "order_items"."order_id" = ?`:                               "orderItems",
		`update "posts" set "views" = ?`:                                                               "",
	} {
		if got := guessRelation(sql); got != want {
			t.Errorf("guessRelation(%q) = %q, want %q", sql, got, want)
		}
	}
}

func TestPercentileNearestRank(t *testing.T) {
	if got := percentile([]float64{30, 499, 30}, 95); got != 499 {
		t.Errorf("p95 of 3 = %v, want the slowest", got)
	}
	v := make([]float64, 100)
	for i := range v {
		v[i] = float64(i + 1)
	}
	if percentile(v, 50) != 50 || percentile(v, 95) != 95 || percentile(v, 99) != 99 {
		t.Errorf("p50/p95/p99 of 1..100 = %v %v %v", percentile(v, 50), percentile(v, 95), percentile(v, 99))
	}
}
