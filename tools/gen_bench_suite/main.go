package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type familyDef struct {
	Name     string
	Expected string
	Source   string
	LineFn   func(caseIdx, lineIdx int) string
}

type caseMeta struct {
	Family   string `json:"family"`
	CaseName string `json:"case_name"`
	Expected string `json:"expected_grok"`
	Source   string `json:"expected_source"`
}

func main() {
	root := filepath.Join("test", "benchmark", "cases")
	must(os.RemoveAll(root))
	must(os.MkdirAll(root, 0o755))

	families := definitions()
	totalCases := 0
	totalFiles := 0

	for _, f := range families {
		for caseIdx := 1; caseIdx <= 10; caseIdx++ {
			caseName := fmt.Sprintf("%s_%03d", f.Name, caseIdx)
			caseDir := filepath.Join(root, caseName)
			must(os.MkdirAll(caseDir, 0o755))

			lines := make([]string, 0, 30)
			for lineIdx := 1; lineIdx <= 30; lineIdx++ {
				lines = append(lines, f.LineFn(caseIdx, lineIdx))
			}

			inputPath := filepath.Join(caseDir, "input.log")
			expectedPath := filepath.Join(caseDir, "expected.grok")
			metaPath := filepath.Join(caseDir, "meta.json")

			must(os.WriteFile(inputPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
			must(os.WriteFile(expectedPath, []byte(f.Expected+"\n"), 0o644))

			meta := caseMeta{
				Family:   f.Name,
				CaseName: caseName,
				Expected: f.Expected,
				Source:   f.Source,
			}
			raw, err := json.MarshalIndent(meta, "", "  ")
			must(err)
			must(os.WriteFile(metaPath, append(raw, '\n'), 0o644))

			totalCases++
			totalFiles += 3
		}
	}

	fmt.Printf("Generated %d benchmark cases (%d files) in %s\n", totalCases, totalFiles, root)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func definitions() []familyDef {
	return []familyDef{
		{
			Name:     "nginx_access_combined",
			Expected: `%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"`,
			Source:   "library:Nginx Access Combined",
			LineFn: func(c, l int) string {
				return fmt.Sprintf("10.%d.%d.%d - user%d [10/Oct/2024:13:%02d:%02d +0000] \"GET /svc/%d?q=%d HTTP/1.1\" %d %d \"https://example.com/r/%d\" \"Mozilla/5.0 case/%d line/%d\"",
					c, l%250, (l*3)%250, c, (l*2)%60, (l*3)%60, c, l, 200+(l%5), 1000+l, c, c, l)
			},
		},
		{
			Name:     "apache_combined",
			Expected: `%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url}(?: HTTP/%{NUMBER:http_version})?" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"`,
			Source:   "library:Apache Combined",
			LineFn: func(c, l int) string {
				return fmt.Sprintf("172.16.%d.%d - - [11/Oct/2024:09:%02d:%02d +0000] \"POST /api/v%d/orders/%d HTTP/1.1\" %d %d \"-\" \"curl/8.%d.%d\"",
					c, l%255, l%60, (l*5)%60, c, l, 201+(l%3), 512+l, c%10, l%10)
			},
		},
		{
			Name:     "syslog_rfc3164",
			Expected: `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} %{PROG:program}(?:\[%{POSINT:pid}\])?: %{GREEDYDATA:message}`,
			Source:   "library:Syslog RFC3164",
			LineFn: func(c, l int) string {
				return fmt.Sprintf("Oct %2d 10:%02d:%02d host-%d app%d[%d]: event id=%d severity=info message=\"service ok\"",
					(l%28)+1, l%60, (l*7)%60, c, c, 1000+l, 50000+l)
			},
		},
		{
			Name:     "syslog_rfc5424",
			Expected: `<%{POSINT:priority}>%{NONNEGINT:version} %{TIMESTAMP_ISO8601:timestamp} %{HOSTNAME:hostname} %{NOTSPACE:app_name} %{NOTSPACE:proc_id} %{NOTSPACE:msg_id} (?:-|%{SYSLOG5424SD:structured_data}) ?%{GREEDYDATA:message}`,
			Source:   "library:Syslog RFC5424",
			LineFn: func(c, l int) string {
				return fmt.Sprintf("<134>1 2024-10-%02dT12:%02d:%02dZ host-%d svc%d %d ID%d [meta k=\"v%d\"] app started req=%d",
					(l%28)+1, l%60, (l*2)%60, c, c, 2000+l, 900+l, l, 100000+l)
			},
		},
		{
			Name:     "ssh_auth",
			Expected: `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} sshd\[%{POSINT:pid}\]: %{GREEDYDATA:message}`,
			Source:   "library:SSH Authentication",
			LineFn: func(c, l int) string {
				return fmt.Sprintf("Nov %2d 23:%02d:%02d node-%d sshd[%d]: Accepted password for user%d from 192.168.%d.%d port %d ssh2",
					(l%28)+1, l%60, (l*3)%60, c, 3000+l, c, c, l%200, 50000+l)
			},
		},
		{
			Name:     "haproxy_http",
			Expected: `%{IP:client_ip}:%{INT:client_port} \[%{HTTPDATE:timestamp}\] %{NOTSPACE:frontend_name} %{NOTSPACE:backend_name}/%{NOTSPACE:server_name} %{INT:t_request}/%{INT:t_queue}/%{INT:t_connect}/%{INT:t_response}/%{INT:t_total} %{INT:status} %{INT:bytes_read} %{NOTSPACE:req_cookie} %{NOTSPACE:resp_cookie} %{NOTSPACE:termination_state} %{INT:actconn}/%{INT:feconn}/%{INT:beconn}/%{INT:srvconn}/%{INT:retries} %{INT:srv_queue}/%{INT:backend_queue} "(?:%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}|%{DATA:raw_request})"`,
			Source:   "library:HAProxy HTTP",
			LineFn: func(c, l int) string {
				return fmt.Sprintf("192.0.%d.%d:%d [12/Oct/2024:14:%02d:%02d +0000] fe%d be%d/srv%d 0/0/1/2/3 %d %d - - --NI 1/1/1/1/0 0/0 \"GET /health/%d HTTP/1.1\"",
					c, l%200, 40000+l, l%60, (l*4)%60, c, c, l%3, 200+(l%10), 2048+l, l)
			},
		},
		{
			Name:     "aws_alb",
			Expected: `%{WORD:type} %{TIMESTAMP_ISO8601:timestamp} %{NOTSPACE:elb_name} %{IP:client_ip}:%{INT:client_port} (?:%{IP:target_ip}:%{INT:target_port}|-) %{NUMBER:request_processing_time} %{NUMBER:target_processing_time} %{NUMBER:response_processing_time} %{INT:elb_status_code} (?:%{INT:target_status_code}|-) %{INT:received_bytes} %{INT:sent_bytes} "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" "%{DATA:user_agent}" %{NOTSPACE:ssl_cipher} %{NOTSPACE:ssl_protocol} %{NOTSPACE:target_group_arn} "%{DATA:trace_id}" "%{DATA:domain_name}" "%{DATA:chosen_cert_arn}" %{INT:matched_rule_priority} %{TIMESTAMP_ISO8601:request_creation_time} "%{DATA:actions_executed}" "%{DATA:redirect_url}" "%{DATA:error_reason}" "%{DATA:target_port_list}" "%{DATA:target_status_code_list}" "%{DATA:classification}" "%{DATA:classification_reason}"`,
			Source:   "library:AWS ALB",
			LineFn: func(c, l int) string {
				return fmt.Sprintf("http 2024-10-%02dT15:%02d:%02d.000000Z app/lb-%d/abc 203.0.113.%d:%d 10.0.%d.%d:80 0.000 0.002 0.001 200 200 %d %d \"GET https://example.org/path/%d HTTP/1.1\" \"agent/%d\" TLS_AES_128_GCM_SHA256 TLSv1.3 arn:aws:elasticloadbalancing:xx \"Root=1-abcdef-%d\" \"example.org\" \"arn:cert\" 1 2024-10-%02dT15:%02d:%02d.000000Z \"forward\" \"-\" \"-\" \"10.0.0.1:80\" \"200\" \"-\" \"-\"",
					(l%28)+1, l%60, (l*5)%60, c, l%200, 50000+l, c, l%200, 300+l, 900+l, l, c, l, (l%28)+1, l%60, (l*5)%60)
			},
		},
		{
			Name:     "postgresql",
			Expected: `%{TIMESTAMP_ISO8601:timestamp}(?: %{TZ:timezone})? \[%{POSINT:pid}\](?: %{DATA:user_db})? %{LOGLEVEL:level}:\s+%{GREEDYDATA:message}`,
			Source:   "library:PostgreSQL",
			LineFn: func(c, l int) string {
				return fmt.Sprintf("2024-10-%02d 16:%02d:%02d UTC [%d] user=db%d LOG: duration: %.3f ms  statement: SELECT * FROM t%d WHERE id=%d;",
					(l%28)+1, l%60, (l*2)%60, 7000+l, c, float64(l)/3.0, c, l)
			},
		},
		{
			Name:     "redis",
			Expected: `%{INT:pid}:%{WORD:role} %{MONTHDAY:day} %{MONTH:month} %{YEAR:year} %{TIME:time} %{REDISLEVEL:level} %{GREEDYDATA:message}`,
			Source:   "library:Redis",
			LineFn: func(c, l int) string {
				levels := []string{".", "-", "*", "#"}
				level := levels[l%len(levels)]
				return fmt.Sprintf("%d:M %02d Oct 2024 17:%02d:%02d %s Ready to accept connections case=%d line=%d",
					8000+l, (l%28)+1, l%60, (l*6)%60, level, c, l)
			},
		},
		{
			Name:     "log4j_logback",
			Expected: `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+\[%{DATA:thread}\]\s+%{JAVACLASS:logger}\s+-\s+%{GREEDYDATA:message}`,
			Source:   "library:Log4j Logback",
			LineFn: func(c, l int) string {
				return fmt.Sprintf("2024-10-%02d 18:%02d:%02d,123 INFO [worker-%d] com.example.Service%d - requestId=%d completed in %dms",
					(l%28)+1, l%60, (l*7)%60, c, c, 900000+l, l+5)
			},
		},
		{
			Name:     "generic_logfmt",
			Expected: `%{GREEDYDATA:kvpairs}`,
			Source:   "structured:logfmt",
			LineFn: func(c, l int) string {
				return fmt.Sprintf("ts=2024-10-%02dT19:%02d:%02dZ level=info service=svc%d trace_id=%032x span_id=%016x msg=\"hello %d\" duration=%dms status=%d",
					(l%28)+1, l%60, (l*2)%60, c, l+1000, l+77, l, l+1, 200+(l%5))
			},
		},
		{
			Name:     "generic_json",
			Expected: `\{%{GREEDYDATA:json}\}`,
			Source:   "structured:JSON Object",
			LineFn: func(c, l int) string {
				return fmt.Sprintf("{\"timestamp\":\"2024-10-%02dT20:%02d:%02dZ\",\"level\":\"info\",\"service\":\"svc%d\",\"message\":\"event-%d\",\"request_id\":\"req-%d\"}",
					(l%28)+1, l%60, (l*3)%60, c, l, 100000+l)
			},
		},
	}
}
