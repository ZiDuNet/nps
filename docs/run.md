# 运行

服务端：

```bash
./nps
```

客户端：

```bash
./npc -server=<服务器IP>:8024 -vkey=<VerifyKey>
```

TLS 客户端：

```bash
./npc -server=<服务器IP>:8025 -vkey=<VerifyKey> -tls_enable=true
```

更多说明见[快速开始](start.md)和[客户端配置](client_config.md)。
