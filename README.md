# PT-Backend: BGP Route Collector & Analysis API

基于 [GoBGP](https://github.com/osrg/gobgp) 开发的高性能 BGP 路由收集器与分析后端。本项目参考了 [bgp.tools](https://bgp.tools/kb/what-is-a-upstream) 的逻辑，以每个 BGP peer 为单位收集全量路由，并自动识别 Tier 1 提供商、上游 (Upstream) 链路、起源 AS 及下游 AS。

## 核心功能

- **路由收集器模式**: 以 ADJIN 方式监听所有 BGP peer 的全量路由更新，每条路由按 `prefix → peer` 两级索引存储。
- **路径分析**:
    - **Tier 1 识别**: 自动匹配全球核心 Tier 1 运营商 (NTT, Cogent, GTT, Lumen 等 13 个)。
    - **上游判定**: 参照 `bgp.tools` 标准，识别 Tier 1 与 Origin 之间的中转 AS 为上游。
    - **起源提取**: 准确识别路由的原始发布者 (Origin AS)。
- **前缀查询 API**: 按 CIDR 查询路由、peer、上游及更具体前缀。
- **ASN 查询 API**: 按 ASN 查询其发布的前缀、上游、下游及 peer。
- **配置热重载**: 通过 `config.yaml` 管理 BGP 邻居，文件变更自动生效，无需重启。

## 项目结构

```text
/
├── main.go               # 入口：初始化、启动、配置热重载
├── config.yaml           # BGP 全局配置与邻居列表
├── bgp/
│   ├── server.go         # GoBGP 封装：启动、邻居管理、事件监听
│   ├── analyzer.go       # 路径分析：Tier 1 识别、上游链路计算
│   └── analyzer_test.go  # 分析逻辑单元测试
├── handler/
│   ├── handler.go        # BGPHandler 核心：事件循环、路由存储
│   ├── prefix.go         # 前缀查询方法及 PrefixSummary 类型
│   └── asn.go            # ASN 查询方法及 ASNSummary 类型
├── web/
│   ├── server.go         # HTTP 服务器启动与路由注册
│   ├── prefix.go         # /api/v1/prefix/ 处理器
│   └── asn.go            # /api/v1/asn/ 处理器
├── config/
│   └── config.go         # YAML 配置解析
└── db/
    └── db.go             # 数据持久化（待扩展）
```

## 技术栈

- **语言**: Go 1.21+
- **BGP 库**: [osrg/gobgp/v3](https://github.com/osrg/gobgp)
- **协议**: BGP (port 179), HTTP (port 8080), gRPC (内部)

## 快速开始

### 1. 安装依赖

```bash
go mod tidy
```

### 2. 配置 BGP 邻居

编辑 `config.yaml`：

```yaml
global:
  asn: 65001
  router_id: 1.1.1.1
  port: 179

neighbors:
  - address: "192.168.1.100"
    asn: 65002
  - address: "172.16.0.1"
    asn: 65003
```

### 3. 启动

```bash
# BGP 监听 179 端口需要 root 权限
sudo go run main.go
```

- **BGP 监听**: `:179`
- **Web API**: `http://localhost:8080`

`config.yaml` 修改后自动重载邻居配置，无需重启。

## REST API

### 前缀查询

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/prefix/{cidr}` | 前缀摘要（origin AS、peer 数、上游） |
| GET | `/api/v1/prefix/{cidr}/routes` | 该前缀来自所有 peer 的完整路由 |
| GET | `/api/v1/prefix/{cidr}/peers` | 宣告该前缀的 peer IP 列表 |
| GET | `/api/v1/prefix/{cidr}/upstreams` | 上游及 Tier 1 AS 列表 |
| GET | `/api/v1/prefix/{cidr}/downstreams` | 包含于该前缀的更具体路由 |

示例：
```bash
curl http://localhost:8080/api/v1/prefix/1.1.1.0/24
curl http://localhost:8080/api/v1/prefix/1.1.1.0/24/routes
curl http://localhost:8080/api/v1/prefix/2001:db8::/32/peers
```

### ASN 查询

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/asn/{asn}` | ASN 摘要（前缀数、上游、下游、peer） |
| GET | `/api/v1/asn/{asn}/prefixes` | 该 ASN 发布的所有前缀 |
| GET | `/api/v1/asn/{asn}/upstreams` | 该 ASN 的上游 AS |
| GET | `/api/v1/asn/{asn}/downstreams` | 将该 ASN 作为中转的下游 AS |
| GET | `/api/v1/asn/{asn}/peers` | 宣告该 ASN 前缀的 collector peer IP |

示例：
```bash
curl http://localhost:8080/api/v1/asn/13335
curl http://localhost:8080/api/v1/asn/13335/prefixes
curl http://localhost:8080/api/v1/asn/13335/upstreams
```

## 分析逻辑说明

本项目遵循 **bgp.tools** 的上游判定准则：

- **Tier 1 定义**: AS174, AS1299, AS2914, AS3257, AS3356, AS3491, AS5511, AS6453, AS6461, AS6762, AS6830, AS7018, AS12956
- **Upstream 判断**: 在 `AS_PATH` 中，任何位于 Tier 1 AS 和 Origin AS 之间的 ASN 均被视为上游。
- **示例**: 路径 `[Peer → AS2914(NTT) → AS4809(CN2) → AS4134(Chinatelecom) → Origin]`
    - Tier 1: AS2914
    - Upstream Chain: `[AS2914, AS4809, AS4134]`
    - Direct Upstream: AS4134
    - Origin AS: `Origin`

## 运行测试

```bash
go test -v ./bgp/...
```

## 开源协议

[MIT License](LICENSE)
