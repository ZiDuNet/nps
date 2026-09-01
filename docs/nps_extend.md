# 服务端增强功能（兼容路径）

> 此路径为早期外部链接保留。请优先阅读[服务端增强功能](server/nps_extend.md)和[域名代理与路由](extend/domain-proxy.md)，它们对应当前版本的配置项与实现。

## HTTPS

当前版本不再使用 `https_just_proxy`。域名规则上传证书时由 NPS 终结 TLS；未上传证书时 NPS 将 TLS 流量转发给内网服务处理。证书、自动 HTTPS、非标准端口和前置反向代理配置请使用[当前 HTTPS 说明](server/nps_extend.md#使用-https)。

## 历史补充

以下内容仅保留早期部署参考。涉及监听端口、用户登录、证书、容器网络或安全控制时，请以当前结构化页面为准。

## 与nginx配合

有时候还需要在云服务器上运行 Nginx 来处理静态文件缓存等场景。NPS 可与 Nginx 配合使用：在配置文件中将 `http_proxy_port` 设置为非 80 端口，并在 Nginx 中反向代理到该端口。例如 `http_proxy_port=8010`：
```
server {
    listen 80;
    server_name *.proxy.com;
    location / {
        proxy_set_header Host  $http_host;
        proxy_pass http://127.0.0.1:8010;
    }
}
```
如需使用 HTTPS，也可以让 Nginx 监听 443 并配置 SSL，再将 NPS 的 `https_proxy_port` 置空以关闭 NPS 自身 HTTPS 入口。例如 `http_proxy_port=8020`：

```
server {
    listen 443;
    server_name *.proxy.com;
    ssl on;
    ssl_certificate  certificate.crt;
    ssl_certificate_key private.key;
    ssl_session_timeout 5m;
    ssl_ciphers ECDHE-RSA-AES128-GCM-SHA256:ECDHE:ECDH:AES:HIGH:!NULL:!aNULL:!MD5:!ADH:!RC4;
    ssl_protocols TLSv1 TLSv1.1 TLSv1.2;
    ssl_prefer_server_ciphers on;
    location / {
        proxy_set_header Host  $http_host;
        proxy_pass http://127.0.0.1:8020;
    }
}
```
## web管理使用https
如果web管理需要使用https，可以在配置文件`nps.conf`中设置`web_open_ssl=true`，并配置`web_cert_file`和`web_key_file`
## web使用Caddy代理

如果将web配置到Caddy代理,实现子路径访问nps,可以这样配置.

假设我们想通过 `http://caddy_ip:caddy_port/nps` 来访问后台, Caddyfile 这样配置:

```Caddyfile
caddy_ip:caddy_port/nps {
  ##server_ip 为 nps 服务器IP
  ##web_port 为 nps 后台端口
  proxy / http://server_ip:web_port/nps {
	transparent
  }
}
```

nps.conf 修改 `web_base_url` 为 `/nps` 即可
```
web_base_url=/nps
```


## 关闭代理

如需关闭http代理可在配置文件中将http_proxy_port设置为空，如需关闭https代理可在配置文件中将https_proxy_port设置为空。

## 流量数据持久化
服务端默认每 1 分钟持久化一次流量数据。将 `nps.conf` 中的 `flow_store_interval` 设为空或 `0` 可关闭定时持久化；单位为分钟。

**注意：** nps不会持久化通过公钥连接的客户端
## 系统信息显示
nps 服务端支持在 web 上显示和统计服务器相关信息，`system_info_display` 默认开启；如需关闭可在 `nps.conf` 中设置 `system_info_display=false`。

## 自定义客户端连接密钥
web上可以自定义客户端连接的密钥，但是必须具有唯一性
## 关闭公钥访问
可以将`nps.conf`中的`public_vkey`设置为空或者删除

## 关闭web管理
可以将`nps.conf`中的`web_port`设置为空或者删除

## 服务端多用户登录

当前版本已经使用独立用户体系，普通用户由管理员在 Web 面板「用户管理」中创建，并通过客户端归属关系管理权限。默认 `allow_user_login=true`。

旧版本曾支持把客户端上的 `WebUserName` / `WebPassword` 当作登录账号。当前版本会兼容旧字段，但新部署不建议继续使用这种方式。详情见 [用户体系](user.md)。

## 用户注册功能
nps服务端支持用户注册功能，可将`nps.conf`中的`allow_user_register`设置为true，开启后登陆页将会有有注册功能，

## 监听指定ip

nps支持每个隧道监听不同的服务端端口,在`nps.conf`中设置`allow_multi_ip=true`后，可在web中控制，或者npc配置文件中(可忽略，默认为0.0.0.0)
```ini
server_ip=xxx
```
## 代理到服务端本地
在使用nps监听80或者443端口时，默认是将所有的请求都会转发到内网上，但有时候我们的nps服务器的上一些服务也需要使用这两个端口，nps提供类似于`nginx` `proxy_pass` 的功能，支持将代理到服务器本地，该功能支持域名解析，tcp、udp隧道，默认关闭。

**即：** 假设在nps的vps服务器上有一个服务使用5000端口，这时候nps占用了80端口和443，我们想能使用一个域名通过http(s)访问到5000的服务。

**使用方式：** 在`nps.conf`中设置`allow_local_proxy=true`，然后在web上设置想转发的隧道或者域名然后选择转发到本地选项即可成功。
