# Project File Structure

```text
.
├── about_project.md
├── assets
│   ├── architecture.png
│   └── data_flow.png
├── buf.gen.yaml
├── cmd
│   ├── grpcclient
│   │   └── main.go
│   ├── keygen
│   │   └── main.go
│   ├── loadtester
│   │   └── main.go
│   ├── raceprobe
│   │   └── main.go
│   ├── router
│   │   └── main.go
│   └── testdb
│       └── main.go
├── DAY10.md
├── DAY12.md
├── DAY13.md
├── DAY14.md
├── DAY15.md
├── DAY1.md
├── DAY2.md
├── DAY3.md
├── DAY4.md
├── DAY5.md
├── DAY6.md
├── DAY7.md
├── DAY8.md
├── DAY9.md
├── go.mod
├── go.sum
├── Implementing Composable Middleware Pattern.md
├── internal
│   ├── identity
│   │   ├── identity.go
│   │   └── middleware.go
│   ├── middleware
│   │   └── middleware.go
│   ├── mockllm
│   │   └── server.go
│   ├── proxy
│   │   └── proxy.go
│   ├── ratelimit
│   │   ├── keys.go
│   │   ├── limiter.go
│   │   ├── middleware.go
│   │   ├── race_test.go
│   │   ├── redis_limiter.go
│   │   └── tokenbucket.go
│   ├── tenant
│   │   └── grpc_server.go
│   ├── usage
│   │   ├── event.go
│   │   ├── middleware.go
│   │   └── store.go
│   └── workerpool
│       └── pool.go
├── migrations
│   └── 0001_usage_events.sql
├── pkg
│   └── api
│       └── proto
│           └── router
│               └── v1
│                   ├── tenant_grpc.pb.go
│                   └── tenant.pb.go
├── progress with scenoir.md
├── proto
│   └── router
│       └── v1
│           └── tenant.proto
├── roadmap_text.txt
├── router
├── visual.md
└── WEEK1-DEEPDIVE.md
```
