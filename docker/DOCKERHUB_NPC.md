# NPC 客户端（ZiDuNet 优化版）

> 对原版 NPS 进行了大量 bug 修复、性能优化和安全加固，长期维护中。

## 优化内容

- **交互式菜单优化** — 递归改循环防止栈溢出
- **内存泄漏修复** — UDP 数据转发 buf 泄漏、P2P 连接资源释放
- **并发安全** — 连接状态原子化、关闭时资源正确回收
- **弃用 API 更新** — rand.Seed、ioutil.WriteFile 等已替换
- **版本显示** — 菜单中显示当前版本号
- **内置更新** — 菜单选项一键更新客户端

## 快速启动

```bash
docker run -d --name npc \
  ZiDuNet/npc -server=your-ip:8024 -vkey=your-key
```

TLS 模式：
```bash
docker run -d --name npc \
  ZiDuNet/npc -server=your-ip:8025 -vkey=your-key -tls_enable=true
```

## 相关链接

- GitHub: https://github.com/ZiDuNet/nps
