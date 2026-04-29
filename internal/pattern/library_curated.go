package pattern

// KnownPatternsCurated holds the curated overrides + source-specific
// reference entries. Source-specific entries should appear before generic
// entries and avoid %{GREEDYDATA} until the natural message tail.
var KnownPatternsCurated = []KnownPattern{
	// Web access logs
	{
		Name:        "Nginx Access Combined",
		Pattern:     `%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"`,
		Priority:    10,
		Specificity: 90,
	},
	{
		Name:        "Nginx Access Main With Timing",
		Pattern:     `%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}" "%{DATA:x_forwarded_for}" rt=%{NUMBER:request_time} uct="%{DATA:upstream_connect_time}" uht="%{DATA:upstream_header_time}" urt="%{DATA:upstream_response_time}"`,
		Priority:    11,
		Specificity: 98,
	},
	{
		Name:        "Nginx Error",
		Pattern:     `%{YEAR}/%{MONTHNUM}/%{MONTHDAY} %{TIME:time} \[%{LOGLEVEL:level}\] %{INT:pid}#%{INT:tid}: (?:\*%{INT:connection} )?%{GREEDYDATA:message}`,
		Priority:    12,
		Specificity: 95,
	},
	{
		Name:        "Apache Common",
		Pattern:     `%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url}(?: HTTP/%{NUMBER:http_version})?" %{INT:status} (?:%{INT:bytes}|-)`,
		Priority:    13,
		Specificity: 88,
	},
	{
		Name:        "Apache Combined",
		Pattern:     `%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url}(?: HTTP/%{NUMBER:http_version})?" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"`,
		Priority:    13,
		Specificity: 90,
	},
	{
		Name:        "Apache Vhost Combined",
		Pattern:     `%{IPORHOST:vhost}:%{INT:vhost_port} %{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url}(?: HTTP/%{NUMBER:http_version})?" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"`,
		Priority:    13,
		Specificity: 92,
	},
	{
		Name:        "Apache Error",
		Pattern:     `\[%{APACHE_ERROR_TIME:timestamp}\] \[%{DATA:module}:%{LOGLEVEL:level}\](?: \[pid %{INT:pid}(?::tid %{INT:tid})?\])?(?: \[client %{IPORHOST:client_ip}:%{INT:client_port}\])? %{GREEDYDATA:message}`,
		Priority:    14,
		Specificity: 95,
		CustomPatterns: map[string]string{
			"APACHE_ERROR_TIME": `[A-Za-z]{3} [A-Za-z]{3}\s+\d{1,2} %{TIME}(?:\.\d+)? \d{4}`,
		},
	},
	{
		Name:        "Caddy Access",
		Pattern:     `%{IPORHOST:client_ip} - %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} %{INT:bytes}`,
		Priority:    15,
		Specificity: 88,
	},
	{
		Name:        "Morgan Common",
		Pattern:     `%{IPORHOST:client_ip} - %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} %{INT:bytes}`,
		Priority:    16,
		Specificity: 86,
	},
	{
		Name:        "Envoy Access",
		Pattern:     `\[%{TIMESTAMP_ISO8601:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} %{NOTSPACE:response_flags} %{INT:bytes_received} %{INT:bytes_sent} %{INT:duration} %{NOTSPACE:upstream_service_time} "%{DATA:x_forwarded_for}" "%{DATA:user_agent}" "%{DATA:request_id}" "%{DATA:authority}" "%{DATA:upstream_host}"`,
		Priority:    17,
		Specificity: 96,
	},

	// System logs
	{
		Name:        "Syslog RFC3164",
		Pattern:     `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} %{PROG:program}(?:\[%{POSINT:pid}\])?: %{GREEDYDATA:message}`,
		Priority:    30,
		Specificity: 75,
	},
	{
		Name:        "Syslog RFC5424",
		Pattern:     `<%{POSINT:priority}>%{NONNEGINT:version} %{TIMESTAMP_ISO8601:timestamp} %{HOSTNAME:hostname} %{NOTSPACE:app_name} %{NOTSPACE:proc_id} %{NOTSPACE:msg_id} (?:-|%{SYSLOG5424SD:structured_data}) ?%{GREEDYDATA:message}`,
		Priority:    31,
		Specificity: 90,
	},
	{
		Name:        "SSH Authentication",
		Pattern:     `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} sshd\[%{POSINT:pid}\]: %{GREEDYDATA:message}`,
		Priority:    32,
		Specificity: 95,
	},
	{
		Name:        "Sudo",
		Pattern:     `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} sudo(?:\[%{POSINT:pid}\])?: %{GREEDYDATA:message}`,
		Priority:    33,
		Specificity: 92,
	},
	{
		Name:        "Cron",
		Pattern:     `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} (?:cron|CROND)(?:\[%{POSINT:pid}\])?: %{GREEDYDATA:message}`,
		Priority:    34,
		Specificity: 90,
	},
	{
		Name:        "Auditd Key Value",
		Pattern:     `type=%{WORD:audit_type} msg=audit\(%{NUMBER:audit_epoch}:%{INT:audit_id}\): %{GREEDYDATA:kvpairs}`,
		Priority:    35,
		Specificity: 95,
	},

	// Load balancers and cloud edge
	{
		Name:        "HAProxy HTTP",
		Pattern:     `%{IP:client_ip}:%{INT:client_port} \[%{HTTPDATE:timestamp}\] %{NOTSPACE:frontend_name} %{NOTSPACE:backend_name}/%{NOTSPACE:server_name} %{INT:t_request}/%{INT:t_queue}/%{INT:t_connect}/%{INT:t_response}/%{INT:t_total} %{INT:status} %{INT:bytes_read} %{NOTSPACE:req_cookie} %{NOTSPACE:resp_cookie} %{NOTSPACE:termination_state} %{INT:actconn}/%{INT:feconn}/%{INT:beconn}/%{INT:srvconn}/%{INT:retries} %{INT:srv_queue}/%{INT:backend_queue} "(?:%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}|%{DATA:raw_request})"`,
		Priority:    50,
		Specificity: 98,
	},
	{
		Name:        "HAProxy TCP",
		Pattern:     `%{IP:client_ip}:%{INT:client_port} \[%{HTTPDATE:timestamp}\] %{NOTSPACE:frontend_name} %{NOTSPACE:backend_name}/%{NOTSPACE:server_name} %{INT:t_queue}/%{INT:t_connect}/%{INT:t_total} %{INT:bytes_read} %{NOTSPACE:termination_state} %{INT:actconn}/%{INT:feconn}/%{INT:beconn}/%{INT:srvconn}/%{INT:retries} %{INT:srv_queue}/%{INT:backend_queue}`,
		Priority:    51,
		Specificity: 97,
	},
	{
		Name:        "AWS ALB",
		Pattern:     `%{WORD:type} %{TIMESTAMP_ISO8601:timestamp} %{NOTSPACE:elb_name} %{IP:client_ip}:%{INT:client_port} (?:%{IP:target_ip}:%{INT:target_port}|-) %{NUMBER:request_processing_time} %{NUMBER:target_processing_time} %{NUMBER:response_processing_time} %{INT:elb_status_code} (?:%{INT:target_status_code}|-) %{INT:received_bytes} %{INT:sent_bytes} "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" "%{DATA:user_agent}" %{NOTSPACE:ssl_cipher} %{NOTSPACE:ssl_protocol} %{NOTSPACE:target_group_arn} "%{DATA:trace_id}" "%{DATA:domain_name}" "%{DATA:chosen_cert_arn}" %{INT:matched_rule_priority} %{TIMESTAMP_ISO8601:request_creation_time} "%{DATA:actions_executed}" "%{DATA:redirect_url}" "%{DATA:error_reason}" "%{DATA:target_port_list}" "%{DATA:target_status_code_list}" "%{DATA:classification}" "%{DATA:classification_reason}"`,
		Priority:    52,
		Specificity: 99,
	},
	{
		Name:        "AWS CLB",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp} %{NOTSPACE:elb_name} %{IP:client_ip}:%{INT:client_port} (?:%{IP:backend_ip}:%{INT:backend_port}|-) %{NUMBER:request_processing_time} %{NUMBER:backend_processing_time} %{NUMBER:response_processing_time} %{INT:elb_status_code} (?:%{INT:backend_status_code}|-) %{INT:received_bytes} %{INT:sent_bytes} "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}"`,
		Priority:    53,
		Specificity: 97,
	},
	{
		Name:        "AWS NLB",
		Pattern:     `%{NOTSPACE:type} %{NOTSPACE:version} %{TIMESTAMP_ISO8601:timestamp} %{NOTSPACE:elb_name} %{NOTSPACE:listener} %{IP:client_ip}:%{INT:client_port} %{IP:target_ip}:%{INT:target_port} %{INT:tcp_connection_time_ms} %{INT:tls_handshake_time_ms} %{INT:received_bytes} %{INT:sent_bytes} %{NOTSPACE:incoming_tls_alert} %{NOTSPACE:chosen_cert_arn} %{NOTSPACE:chosen_cert_serial} %{NOTSPACE:tls_cipher} %{NOTSPACE:tls_protocol_version} %{NOTSPACE:tls_named_group} %{NOTSPACE:domain_name} %{NOTSPACE:alpn_fe_protocol} %{NOTSPACE:alpn_be_protocol} %{NOTSPACE:alpn_client_preference_list} %{NOTSPACE:tls_connection_creation_time}`,
		Priority:    54,
		Specificity: 98,
	},
	{
		Name:        "AWS VPC Flow Logs",
		Pattern:     `%{INT:version} %{NOTSPACE:account_id} %{NOTSPACE:interface_id} %{IP:src_addr} %{IP:dst_addr} %{INT:src_port} %{INT:dst_port} %{INT:protocol} %{INT:packets} %{INT:bytes} %{INT:start} %{INT:end} %{WORD:action} %{WORD:log_status}(?: %{GREEDYDATA:extra_fields})?`,
		Priority:    55,
		Specificity: 98,
	},
	{
		Name:        "CloudFront Standard",
		Pattern:     `%{DATE:date}\t%{TIME:time}\t%{NOTSPACE:edge_location}\t%{INT:bytes}\t%{IP:client_ip}\t%{WORD:method}\t%{NOTSPACE:host}\t%{NOTSPACE:url}\t%{INT:status}\t%{NOTSPACE:referrer}\t%{NOTSPACE:user_agent}\t%{NOTSPACE:query_string}\t%{NOTSPACE:cookie}\t%{NOTSPACE:edge_result_type}\t%{NOTSPACE:edge_request_id}\t%{NOTSPACE:host_header}\t%{NOTSPACE:protocol}\t%{INT:cs_bytes}\t%{NOTSPACE:time_taken}\t%{GREEDYDATA:rest}`,
		Priority:    56,
		Specificity: 96,
	},

	// Container / Kubernetes
	{
		Name:        "CRI Containerd",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp} %{WORD:stream} %{WORD:logtag} %{GREEDYDATA:message}`,
		Priority:    70,
		Specificity: 88,
	},
	{
		Name:        "Kubernetes Klog",
		Pattern:     `%{NOTSPACE:level_short}%{MONTHNUM2:month}%{MONTHDAY2:day} %{TIME:time}\s+%{INT:thread_id} %{NOTSPACE:source_file}:%{INT:source_line}\] %{GREEDYDATA:message}`,
		Priority:    71,
		Specificity: 92,
	},

	// Application logs
	{
		Name:        "Spring Boot Default",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+%{INT:pid}\s+---\s+\[%{DATA:thread}\]\s+%{JAVACLASS:logger}\s+:\s+%{GREEDYDATA:message}`,
		Priority:    80,
		Specificity: 96,
		CustomPatterns: map[string]string{
			"JAVACLASS": `(?:[A-Za-z0-9_$]+\.)*[A-Za-z0-9_$]+`,
		},
	},
	{
		Name:        "Log4j Logback",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+\[%{DATA:thread}\]\s+%{JAVACLASS:logger}\s+-\s+%{GREEDYDATA:message}`,
		Priority:    81,
		Specificity: 90,
		CustomPatterns: map[string]string{
			"JAVACLASS": `(?:[A-Za-z0-9_$]+\.)*[A-Za-z0-9_$]+`,
		},
	},
	{
		Name:        "Tomcat Catalina",
		Pattern:     `%{MONTHNUM:month}-%{MONTHDAY:day}-%{YEAR:year} %{TIME:time} %{LOGLEVEL:level} \[%{DATA:thread}\] %{JAVACLASS:logger} - %{GREEDYDATA:message}`,
		Priority:    82,
		Specificity: 90,
		CustomPatterns: map[string]string{
			"JAVACLASS": `(?:[A-Za-z0-9_$]+\.)*[A-Za-z0-9_$]+`,
		},
	},
	{
		Name:        "Python Logging",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+%{NOTSPACE:logger}\s+%{GREEDYDATA:message}`,
		Priority:    83,
		Specificity: 85,
	},
	{
		Name:        "Go Slog Text",
		Pattern:     `time=%{TIMESTAMP_ISO8601:timestamp} level=%{LOGLEVEL:level} msg=%{QUOTEDSTRING:message}(?: %{GREEDYDATA:kvpairs})?`,
		Priority:    84,
		Specificity: 92,
	},
	{
		Name:        "Winston Text",
		Pattern:     `(?:%{TIMESTAMP_ISO8601:timestamp}\s+)?%{LOGLEVEL:level}:\s+%{GREEDYDATA:message}`,
		Priority:    84,
		Specificity: 82,
	},
	{
		Name:        "Zap Console",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+%{NOTSPACE:logger}\s+%{GREEDYDATA:message}`,
		Priority:    84,
		Specificity: 86,
	},
	{
		Name:        "Gunicorn Access",
		Pattern:     `%{IPORHOST:client_ip} - %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} %{INT:bytes} "%{DATA:referrer}" "%{DATA:user_agent}"`,
		Priority:    85,
		Specificity: 88,
	},
	{
		Name:        "Rails",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp} %{LOGLEVEL:level}\s+%{GREEDYDATA:message}`,
		Priority:    86,
		Specificity: 70,
	},

	// Databases
	{
		Name:        "PostgreSQL",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp}(?: %{TZ:timezone})? \[%{POSINT:pid}\](?: %{DATA:user_db})? %{LOGLEVEL:level}:\s+%{GREEDYDATA:message}`,
		Priority:    110,
		Specificity: 92,
	},
	{
		Name:        "MySQL Error",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp} %{INT:thread_id} \[%{LOGLEVEL:level}\] %{GREEDYDATA:message}`,
		Priority:    111,
		Specificity: 90,
	},
	{
		Name:        "Redis",
		Pattern:     `%{INT:pid}:%{WORD:role} %{MONTHDAY:day} %{MONTH:month} %{YEAR:year} %{TIME:time} %{REDISLEVEL:level} %{GREEDYDATA:message}`,
		Priority:    112,
		Specificity: 90,
		CustomPatterns: map[string]string{
			"REDISLEVEL": `[.\-*#]`,
		},
	},
	{
		Name:        "MongoDB Text",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp} %{WORD:severity} %{WORD:component}\s+\[%{DATA:context}\] %{GREEDYDATA:message}`,
		Priority:    113,
		Specificity: 92,
	},

	// Messaging / infra
	{
		Name:        "Kafka",
		Pattern:     `\[%{TIMESTAMP_ISO8601:timestamp}\] %{LOGLEVEL:level} %{GREEDYDATA:message} \(%{JAVACLASS:logger}\)`,
		Priority:    130,
		Specificity: 90,
		CustomPatterns: map[string]string{
			"JAVACLASS": `(?:[A-Za-z0-9_$]+\.)*[A-Za-z0-9_$]+`,
		},
	},
	{
		Name:        "Elasticsearch",
		Pattern:     `\[%{TIMESTAMP_ISO8601:timestamp}\]\[%{LOGLEVEL:level}\s*\]\[%{DATA:component}\] %{GREEDYDATA:message}`,
		Priority:    131,
		Specificity: 92,
	},
	{
		Name:        "ZooKeeper",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp} \[myid:?(?:%{INT:myid})?\] - %{LOGLEVEL:level}\s+\[%{DATA:thread}\] - %{GREEDYDATA:message}`,
		Priority:    132,
		Specificity: 92,
	},

	// Security / network
	{
		Name:        "Squid Access",
		Pattern:     `%{NUMBER:timestamp}\s+%{INT:duration} %{IP:client_ip} %{WORD:cache_result}/%{INT:status} %{INT:bytes} %{WORD:method} %{NOTSPACE:url} %{NOTSPACE:user} %{NOTSPACE:hierarchy}/%{IPORHOST:server} %{NOTSPACE:content_type}`,
		Priority:    150,
		Specificity: 96,
	},
	{
		Name:        "Snort Fast Alert",
		Pattern:     `%{NOTSPACE:timestamp} \[\*\*\] \[%{DATA:sid}\] %{DATA:msg} \[\*\*\] \[Classification: %{DATA:classification}\] \[Priority: %{INT:priority}\] \{%{WORD:protocol}\} %{IP:src_ip}(?::%{INT:src_port})? -> %{IP:dst_ip}(?::%{INT:dst_port})?`,
		Priority:    151,
		Specificity: 95,
	},
	{
		Name:        "Cisco ASA",
		Pattern:     `%{SYSLOGTIMESTAMP:timestamp}(?: %{HOSTNAME:hostname})? %%{ASA_TAG:tag}: %{GREEDYDATA:message}`,
		Priority:    152,
		Specificity: 92,
		CustomPatterns: map[string]string{
			"ASA_TAG": `[A-Z]+-\d-\d+`,
		},
	},
	{
		Name:        "Fortinet Key Value",
		Pattern:     `date=%{DATE:date} time=%{TIME:time} devname=%{NOTSPACE:devname} %{GREEDYDATA:kvpairs}`,
		Priority:    153,
		Specificity: 92,
	},
}

// KnownPatternsGolden are corpus-derived shapes with high specificity. They
// take precedence over generic curated entries when they match the input.
var KnownPatternsGolden = []KnownPattern{
	{
		Name:        "Traefik Access",
		Pattern:     `%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} %{INT:bytes}(?: "%{DATA:referrer}" "%{DATA:user_agent}")? %{INT:req_id} "%{DATA:frontend}" "%{DATA:backend}" %{INT:duration}ms`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "UFW Kernel",
		Pattern:     `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} kernel: \[UFW %{WORD:action}\] %{GREEDYDATA:details}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Iptables Kernel",
		Pattern:     `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} kernel: \[%{NUMBER:uptime}\] %{GREEDYDATA:iptables}`,
		Priority:    2,
		Specificity: 98,
	},
	{
		Name:        "Cassandra Log",
		Pattern:     `%{LOGLEVEL:level}\s+\[%{NOTSPACE:thread}\]\s+%{TIMESTAMP_ISO8601:timestamp}\s+%{NOTSPACE:class}:%{INT:line}\s+-\s+%{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Celery Log",
		Pattern:     `\[%{TIMESTAMP_ISO8601:timestamp},000: %{LOGLEVEL:level}/%{NOTSPACE:process}\] %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Consul Log",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp} \[%{LOGLEVEL:level}\]\s+agent: %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Cron Log",
		Pattern:     `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} CRON\[%{POSINT:pid}\]: %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Django Error",
		Pattern:     `%{LOGLEVEL:level} %{TIMESTAMP_ISO8601:timestamp} %{NOTSPACE:module} %{GREEDYDATA:message}`,
		Priority:    3,
		Specificity: 95,
	},
	{
		Name:        "Django Request",
		Pattern:     `\[%{HTTPDATE_CONDENSED:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} %{INT:bytes}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Docker Compose",
		Pattern:     `%{NOTSPACE:service}\s+\|\s+\[%{TIMESTAMP_ISO8601:timestamp}\] %{LOGLEVEL:level} %{NOTSPACE:logger}: %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Elasticsearch Strict",
		Pattern:     `\[%{TIMESTAMP_ISO8601:timestamp}\]\[%{LOGLEVEL:level}\s*\]\[%{NOTSPACE:module}\s*\]\s*\[%{NOTSPACE:node}\]\s*%{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Envoy Access Lite",
		Pattern:     `\[%{TIMESTAMP_ISO8601:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} - %{INT:bytes_sent} %{INT:bytes_received} %{INT:duration_ms} "-" "%{DATA:user_agent}" "%{DATA:request_id}" "%{DATA:upstream_cluster}" "-"`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Envoy Text Error",
		Pattern:     `\[%{TIMESTAMP_ISO8601:timestamp}\]\[%{INT:thread}\]\[%{LOGLEVEL:level}\]\[%{NOTSPACE:component}\] %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Fastly Syslog",
		Pattern:     `<%{INT:priority}>%{TIMESTAMP_ISO8601:timestamp} %{NOTSPACE:cache} %{NOTSPACE:service} %{IP:client_ip} %{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version} %{INT:status} %{INT:bytes}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Flask Dev Log",
		Pattern:     `%{IPORHOST:client_ip} - %{USER:ident} \[%{HTTPDATE_CONDENSED:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} -`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Go Std Log",
		Pattern:     `%{YEAR}/%{MONTHNUM2}/%{MONTHDAY2} %{TIME:time} %{NOTSPACE:file}:%{INT:line}: %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Gunicorn Error",
		Pattern:     `\[%{TIMESTAMP_ISO8601:timestamp}\] \[%{INT:pid}\] \[%{LOGLEVEL:level}\] %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Kafka Component",
		Pattern:     `\[%{TIMESTAMP_ISO8601:timestamp}\]\s+%{LOGLEVEL:level}\s+\[%{DATA:component}\]\s+%{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "MySQL Error Strict",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp} %{INT:thread_id} \[%{LOGLEVEL:level}\] \[%{NOTSPACE:error_code}\] \[%{WORD:component}\] %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "NATS Log",
		Pattern:     `\[%{INT:nats_seq}\] %{YEAR}/%{MONTHNUM2}/%{MONTHDAY2} %{TIME:time} \[%{LOGLEVEL:level}\]\s+%{IPORHOST:peer}:%{INT:peer_port}\s+-\s+cid:%{INT:client_id}\s+-\s+%{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Nomad Log",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp} \[%{LOGLEVEL:level}\]\s+client\.alloc_runner\.task_runner: %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "PM2 Log",
		Pattern:     `%{INT:id}\|%{NOTSPACE:app}\s+\|\s+%{TIMESTAMP_ISO8601:timestamp}: %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Python Bracket Log",
		Pattern:     `\[%{TIMESTAMP_ISO8601:timestamp}\]\s+\[%{LOGLEVEL:level}\]\s+\[%{NOTSPACE:logger}:%{INT:pid}\]\s+%{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "RabbitMQ Log",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp} \[%{LOGLEVEL:level}\]\s+<%{NOTSPACE:pid}>\s+%{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Rails Default",
		Pattern:     `%{WORD:level}, \[%{TIMESTAMP_ISO8601:timestamp} #%{INT:pid}\]\s+%{LOGLEVEL:level2}\s+--\s+: %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Serilog Text",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp} \[%{LOGLEVEL:level}\] %{GREEDYDATA:message}`,
		Priority:    4,
		Specificity: 88,
	},
	{
		Name:        "Snort Bracket Alert",
		Pattern:     `\[\*\*\]\s+\[%{NOTSPACE:gid}:%{NOTSPACE:sid}:\d+\]\s+%{DATA:msg}\s+\[\*\*\]\s+\[Classification: %{DATA:classification}\]\s+\[Priority: %{INT:priority}\]\s+%{MONTHNUM2}/%{MONTHDAY2}-%{TIME:time}\s+%{IP:src_ip}:%{INT:src_port}\s+->\s+%{IP:dst_ip}:%{INT:dst_port}\s+%{GREEDYDATA:rest}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Sudo Command",
		Pattern:     `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} sudo:\s+%{NOTSPACE:user}\s+:.*?USER=root\s*;\s*COMMAND=%{GREEDYDATA:command}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "Tomcat Catalina Day",
		Pattern:     `%{MONTHDAY2}-%{MONTH}-%{YEAR} %{TIME:time} %{LOGLEVEL:level} \[%{PROCID:thread}\] %{GREEDYDATA:message}`,
		Priority:    1,
		Specificity: 99,
	},
	{
		Name:        "AWS S3 Access",
		Pattern:     `%{NOTSPACE:bucket_owner} %{NOTSPACE:bucket} \[%{HTTPDATE:timestamp}\] %{IP:client_ip} %{NOTSPACE:requester} %{NOTSPACE:request_id} %{PROCID:operation} %{NOTSPACE:key} "(?:%{WORD:method} %{NOTSPACE:url}(?: HTTP/%{NUMBER:http_version})?|-)" (?:%{INT:status}|-) (?:%{INT:error_code}|-) (?:%{INT:bytes_sent}|-) (?:%{INT:object_size}|-) (?:%{INT:request_time}|-) (?:%{INT:turnaround_time}|-) "%{DATA:referrer}" "%{DATA:user_agent}" %{GREEDYDATA:rest}`,
		Priority:    1,
		Specificity: 99,
	},
}

// KnownPatternsCatchall must remain last and low-specificity.
var KnownPatternsCatchall = []KnownPattern{
	{
		Name:        "Generic ISO Timestamp Level Logger Message",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+%{NOTSPACE:logger}\s+%{GREEDYDATA:message}`,
		Priority:    900,
		Specificity: 20,
	},
	{
		Name:        "Generic ISO Timestamp Level Message",
		Pattern:     `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+%{GREEDYDATA:message}`,
		Priority:    910,
		Specificity: 10,
	},
	{
		Name:        "Generic Bracketed Timestamp",
		Pattern:     `\[?%{TIMESTAMP_ISO8601:timestamp}\]?\s+%{GREEDYDATA:message}`,
		Priority:    920,
		Specificity: 5,
	},
	{
		Name:        "Generic Syslog Timestamp Message",
		Pattern:     `%{SYSLOGTIMESTAMP:timestamp}\s+%{GREEDYDATA:message}`,
		Priority:    930,
		Specificity: 4,
	},
}

// KnownPatternsBundled is populated in init() in bundle.go from
// BuiltinPatternPacks.
var KnownPatternsBundled []KnownPattern
