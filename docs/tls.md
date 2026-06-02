# TLS / mTLS 配置与证书签发

master ↔ slave 的控制连接支持三种模式:

| 模式 | master | slave | 何时用 |
|------|--------|-------|--------|
| 明文 | `tls.disabled: true` | `tls.disabled: true` | 完全可信内网 |
| 仅 TLS | `cert_file`+`key_file` | `ca_file`(+`server_name`) | 加密+验 master 身份 |
| mTLS | 上 + `require_client_cert: true`+`ca_file` | 上 + 自己的 `cert_file`+`key_file` | 双向验证(推荐生产) |

> **头号坑**:slave 的 `tls.server_name` 必须等于 master 证书里的某个 DNS SAN,否则报 `x509: certificate is valid for ...`。生产**不要**用 `insecure_skip_verify: true`。

---

## 用 openssl 签发一套自签 CA + 证书

下面生成:一个 CA、一张 master 服务端证书(SAN = master 主机名)、每个 slave 一张客户端证书(CN/SAN = 该 slave 的 `node_id`,以便启用 `bind_node_id_to_cert`)。

```sh
# 0) 变量
MASTER_DNS="nut-master.internal"     # slave 的 tls.server_name 要等于它
SLAVE_NODE_ID="slave-01"             # 该 slave 的 node_id

# 1) CA(私钥 + 自签根证书,有效期 10 年)
openssl genrsa -out ca.key 4096
openssl req -x509 -new -nodes -key ca.key -sha256 -days 3650 \
  -subj "/CN=nut-server-ca" -out ca.crt

# 2) master 服务端证书(SAN 必须含 MASTER_DNS)
openssl genrsa -out master.key 2048
openssl req -new -key master.key -subj "/CN=${MASTER_DNS}" -out master.csr
printf "subjectAltName=DNS:%s\nextendedKeyUsage=serverAuth\n" "$MASTER_DNS" > master.ext
openssl x509 -req -in master.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -days 825 -sha256 -extfile master.ext -out master.crt

# 3) slave 客户端证书(CN 与 SAN 都设为 node_id,启用身份绑定时必需)
openssl genrsa -out slave.key 2048
openssl req -new -key slave.key -subj "/CN=${SLAVE_NODE_ID}" -out slave.csr
printf "subjectAltName=DNS:%s\nextendedKeyUsage=clientAuth\n" "$SLAVE_NODE_ID" > slave.ext
openssl x509 -req -in slave.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -days 825 -sha256 -extfile slave.ext -out slave.crt
```

产物:`ca.crt`(两端都要)、`master.crt`+`master.key`(放 master)、`slave.crt`+`slave.key`(放对应 slave)。每台 slave 重复第 3 步并改 `SLAVE_NODE_ID`。

> 生产建议用 [step-ca](https://smallstep.com/docs/step-ca/) 这类内部 CA 自动签发/轮换,而不是手搓 openssl。

---

## 安装与配置

```sh
sudo install -d -m0750 -o nut-server -g nut-server /etc/nut-server/tls
# master 机:
sudo install -m0640 -o nut-server -g nut-server ca.crt master.crt master.key /etc/nut-server/tls/
# 各 slave 机:
sudo install -m0640 -o nut-server -g nut-server ca.crt slave.crt slave.key /etc/nut-server/tls/
```

master.yaml:

```yaml
tls:
  enabled: true
  cert_file: "/etc/nut-server/tls/master.crt"
  key_file: "/etc/nut-server/tls/master.key"
  ca_file: "/etc/nut-server/tls/ca.crt"   # mTLS:校验 slave 客户端证书
  require_client_cert: true                # 启用 mTLS
  bind_node_id_to_cert: true               # 可选:强制 node_id == 证书 CN/SAN
```

slave.yaml:

```yaml
master_addr: "nut-master.internal:9000"
tls:
  enabled: true
  cert_file: "/etc/nut-server/tls/slave.crt"
  key_file: "/etc/nut-server/tls/slave.key"
  ca_file: "/etc/nut-server/tls/ca.crt"
  server_name: "nut-master.internal"       # 必须等于 master 证书 SAN
  insecure_skip_verify: false
```

---

## 关于 `bind_node_id_to_cert`

默认 `false`(向后兼容:已有 mTLS 部署若证书 CN 不是 node_id 不受影响)。

设为 `true` 后,注册时 slave 上报的 `node_id` 必须等于其客户端证书的 CommonName 或某个 DNS SAN,否则**拒绝注册**。这把"持有合法 token + 任意合法证书即可冒充任意 node_id(并伪造其关机回执)"的横向接管堵死。启用前确保每张 slave 证书的 CN/SAN 都设成了对应的 `node_id`(见上面第 3 步)。

---

## 验证

```sh
# 看 master 证书链与 SAN
openssl s_client -connect nut-master.internal:9000 -servername nut-master.internal </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -ext subjectAltName

# slave 起来后,master 视角应看到它 online
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" http://127.0.0.1:9001/status | jq '.nodes'
```

握手相关报错的排查见 [troubleshooting.md](troubleshooting.md#tls--mtls-握手失败)。
