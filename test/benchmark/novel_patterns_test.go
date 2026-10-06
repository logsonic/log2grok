package benchmark

import (
	"fmt"
	"strings"
	"testing"

	"github.com/logsonic/log2grok/internal/pattern"
)

func TestDiscoverNovelLogPatterns(t *testing.T) {
	cases := []struct {
		name           string
		lines          []string
		minFields      int
		mustContain    []string
		mustNotContain []string
	}{
		{
			name: "iot_sensor_readings",
			lines: []string{
				`device=thermo-01 zone=living_room temp=22.5 humidity=45.2 battery=87 status=online`,
				`device=thermo-02 zone=bedroom temp=19.8 humidity=52.1 battery=64 status=online`,
				`device=thermo-03 zone=kitchen temp=24.1 humidity=38.7 battery=12 status=low_battery`,
			},
			minFields:   5,
			mustContain: []string{"device=", "zone=", "temp=", "humidity=", "battery=", "status="},
		},
		{
			name: "payment_gateway_events",
			lines: []string{
				`txn_id=txn_9a3f2b merchant=stripe_acct_123 amount=4999 currency=USD card_country=US status=approved risk_score=0.12 decline_reason=none`,
				`txn_id=txn_7e1c4d merchant=stripe_acct_456 amount=12900 currency=EUR card_country=DE status=declined risk_score=0.78 decline_reason="insufficient_funds"`,
				`txn_id=txn_2b8a5e merchant=stripe_acct_789 amount=2500 currency=GBP card_country=GB status=approved risk_score=0.03 decline_reason=none`,
			},
			minFields:   6,
			mustContain: []string{"txn_id=", "merchant=", "amount=", "currency=", "status=", "risk_score=", "decline_reason="},
		},
		{
			name: "ci_cd_pipeline",
			lines: []string{
				`pipeline=web-deploy stage=build commit=abc1234 branch=main job_id=job_4821 duration=45.2 result=success`,
				`pipeline=web-deploy stage=test commit=abc1234 branch=main job_id=job_4822 duration=123.7 result=success`,
				`pipeline=api-deploy stage=build commit=def5678 branch=feature/oauth job_id=job_4823 duration=38.9 result=failed`,
				`pipeline=web-deploy stage=deploy commit=abc1234 branch=main job_id=job_4824 duration=12.1 result=success`,
			},
			minFields:   4,
			mustContain: []string{"pipeline=", "stage=", "commit=", "branch=", "duration=", "result="},
		},
		{
			name: "food_delivery_order_tracking",
			lines: []string{
				`order_id=ORD-2024-88421 restaurant="Tasty Thai" customer_id=cust_33821 rider_id=rider_9921 status=picked_up eta_min=14 lat=40.7128 lng=-74.0060`,
				`order_id=ORD-2024-88422 restaurant="Burger Barn" customer_id=cust_44192 rider_id=rider_1103 status=delivered eta_min=0 lat=40.7589 lng=-73.9851`,
				`order_id=ORD-2024-88423 restaurant="Pasta Palace" customer_id=cust_11283 rider_id=rider_8821 status=preparing eta_min=32 lat=40.6892 lng=-74.0445`,
			},
			minFields:   5,
			mustContain: []string{"order_id=", "restaurant=", "customer_id=", "rider_id=", "status=", "eta_min=", "lat=", "lng="},
		},
		{
			name: "cdn_cache_events",
			lines: []string{
				`edge=edge-lax-03 pop=LAX cache_status=HIT url="/api/v1/users/profile" bytes_sent=1241 response_time_ms=2.1 client_ip=203.0.113.45`,
				`edge=edge-iad-07 pop=IAD cache_status=MISS url="/api/v1/products/search?q=laptop" bytes_sent=8942 response_time_ms=145.3 client_ip=198.51.100.12`,
				`edge=edge-lhr-02 pop=LHR cache_status=HIT url="/static/images/logo.svg" bytes_sent=3421 response_time_ms=1.8 client_ip=203.0.113.88`,
				`edge=edge-lax-03 pop=LAX cache_status=HIT url="/api/v1/orders/history" bytes_sent=5621 response_time_ms=3.2 client_ip=203.0.113.45`,
			},
			minFields:   5,
			mustContain: []string{"edge=", "pop=", "cache_status=", "url=", "bytes_sent=", "response_time_ms=", "client_ip="},
		},
		{
			name: "game_server_events",
			lines: []string{
				`event=player_join server=pvp-eu-01 map=de_dust2 player_id=steam_7656119801 damage=0 weapon=none headshot=false`,
				`event=player_kill server=pvp-eu-01 map=de_dust2 player_id=steam_7656119801 damage=105 weapon="AK-47" headshot=true`,
				`event=player_respawn server=pvp-eu-01 map=de_dust2 player_id=steam_7656119801 damage=0 weapon=none headshot=false`,
			},
			minFields:   3,
			mustContain: []string{"event=", "server=", "map=", "player_id="},
		},
		{
			name: "security_auth_log",
			lines: []string{
				`src_ip=10.0.1.15 user=alice svc=ssh auth_method=pubkey result=success mfa=none session_id=sess_a1b2`,
				`src_ip=10.0.1.22 user=bob svc=ssh auth_method=password result=failed mfa="totp" session_id=sess_c3d4`,
				`src_ip=192.168.5.8 user=charlie svc=web auth_method=oauth result=success mfa=none session_id=sess_e5f6`,
			},
			minFields:   4,
			mustContain: []string{"src_ip=", "user=", "svc=", "auth_method=", "result=", "session_id="},
		},
		{
			name: "database_query_log",
			lines: []string{
				`db=orders shard=shard-03 query_id=qid_99821 table=users op=SELECT rows=1452 duration_ms=2.3 cache_hit=true`,
				`db=orders shard=shard-01 query_id=qid_99822 table=products op=UPDATE rows=1 duration_ms=45.7 cache_hit=false`,
				`db=analytics shard=shard-07 query_id=qid_99823 table=events op=INSERT rows=892 duration_ms=12.1 cache_hit=false`,
			},
			minFields:   5,
			mustContain: []string{"db=", "shard=", "query_id=", "table=", "op=", "rows=", "duration_ms=", "cache_hit="},
		},
		{
			name: "kubernetes_pod_events",
			lines: []string{
				`ts=2024-06-15T10:23:45Z cluster=k8s-prod ns=payments pod=payment-api-7d9f4b2c8-x1a2b node=ip-10-0-1-15 event=OOMKilled container=app exit_code=137 memory_limit_mb=512 memory_usage_mb=511.3 restart_count=3`,
				`ts=2024-06-15T10:24:12Z cluster=k8s-prod ns=payments pod=payment-api-7d9f4b2c8-x1a2b node=ip-10-0-1-15 event=Started container=app exit_code=0 memory_limit_mb=512 memory_usage_mb=128.4 restart_count=4`,
				`ts=2024-06-15T10:30:01Z cluster=k8s-staging ns=auth pod=auth-service-5a2e1c0d-y9z8w node=ip-10-0-2-88 event=Killing container=app exit_code=0 memory_limit_mb=256 memory_usage_mb=245.1 restart_count=0`,
			},
			minFields:   7,
			mustContain: []string{"ts=", "cluster=", "ns=", "pod=", "node=", "event=", "container=", "memory_limit_mb=", "restart_count="},
		},
		{
			name: "game_match_results",
			lines: []string{
				`match_id=match_88421 game=league mode=ranked winner=blue blue_score=28 red_score=14 duration_sec=1843 mvp="PlayerOne"`,
				`match_id=match_88422 game=league mode=ranked winner=red blue_score=15 red_score=22 duration_sec=2102 mvp="ProGamer99"`,
				`match_id=match_88423 game=league mode=ranked winner=blue blue_score=31 red_score=29 duration_sec=2456 mvp="PlayerOne"`,
			},
			minFields:   5,
			mustContain: []string{"match_id=", "game=", "mode=", "winner=", "duration_sec=", "mvp="},
		},
		{
			name: "feature_flags_audit",
			lines: []string{
				`ts=2024-07-01T09:00:00Z flag=new-checkout-rollout env=prod user=service_account action=enable scope=global previous_value=false new_value=true`,
				`ts=2024-07-01T09:15:33Z flag=dark-mode-default env=staging user=alice@example.com action=disable scope=user:11283 previous_value=true new_value=false`,
				`ts=2024-07-01T09:22:10Z flag=beta-api-v3 env=prod user=service_account action=enable scope=api-gateway previous_value=false new_value=true`,
			},
			minFields:   5,
			mustContain: []string{"flag=", "env=", "user=", "action=", "scope=", "previous_value=", "new_value="},
		},
		{
			name: "mixed_quoted_and_bare",
			lines: []string{
				`req_id=req_a1 method=GET path="/api/v1/users/123" status=200 latency_ms=12.4 user_agent="Mozilla/5.0"`,
				`req_id=req_b2 method=POST path="/api/v1/orders" status=201 latency_ms=45.2 user_agent="curl/7.68.0"`,
				`req_id=req_c3 method=DELETE path="/api/v1/items/99" status=204 latency_ms=8.1 user_agent="PostmanRuntime/7.32.3"`,
			},
			minFields:   5,
			mustContain: []string{"req_id=", "method=", "path=", "status=", "latency_ms=", "user_agent="},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := pattern.Options{LibraryThreshold: 0.75}
			dp, err := pattern.Discover(tc.lines, opts)
			if err != nil {
				t.Fatalf("discover failed: %v", err)
			}
			if dp == nil || dp.Grok == "" {
				t.Fatalf("empty discover result")
			}

			fmt.Printf("\n%s grok: %s\n", tc.name, dp.Grok)

			fieldCount := strings.Count(dp.Grok, "=")
			if fieldCount < tc.minFields {
				t.Errorf("expected at least %d key=value segments, got %d in: %s", tc.minFields, fieldCount, dp.Grok)
			}

			for _, sub := range tc.mustContain {
				if !strings.Contains(dp.Grok, sub) {
					t.Errorf("grok missing %q; got: %s", sub, dp.Grok)
				}
			}
			for _, sub := range tc.mustNotContain {
				if strings.Contains(dp.Grok, sub) {
					t.Errorf("grok unexpectedly contains %q; got: %s", sub, dp.Grok)
				}
			}

			re, err := pattern.CompileGrok(dp.Grok, dp.CustomPatterns)
			if err != nil {
				t.Fatalf("compile grok failed: %v\ngrok=%s", err, dp.Grok)
			}
			matched := pattern.EvaluateCoverage(re, tc.lines)
			if matched != len(tc.lines) {
				t.Fatalf("coverage %d/%d < 100%%", matched, len(tc.lines))
			}
		})
	}
}

func TestPolymorphicLogFallback(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
	}{
		{
			name: "varying_keys_per_line",
			lines: []string{
				`event=login user=alice`,
				`event=purchase user=bob item="sku_4821" amount=4999`,
				`event=logout user=alice session_length_min=12`,
			},
		},
		{
			name: "different_key_order",
			lines: []string{
				`a=1 b=2 c=3`,
				`a=1 c=3 b=2`,
				`b=2 a=1 c=3`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := pattern.Options{LibraryThreshold: 0.75}
			dp, err := pattern.Discover(tc.lines, opts)
			if err != nil {
				t.Fatalf("discover failed: %v", err)
			}
			if dp == nil || dp.Grok == "" {
				t.Fatalf("empty discover result")
			}
			if !strings.Contains(dp.Grok, "%{GREEDYDATA:kvpairs}") {
				t.Fatalf("expected fallback to GREEDYDATA for polymorphic logs, got: %s", dp.Grok)
			}
			re, err := pattern.CompileGrok(dp.Grok, dp.CustomPatterns)
			if err != nil {
				t.Fatalf("compile failed: %v", err)
			}
			matched := pattern.EvaluateCoverage(re, tc.lines)
			if matched != len(tc.lines) {
				t.Fatalf("coverage %d/%d", matched, len(tc.lines))
			}
		})
	}
}
