# sessions

http sessions and stores

## usage

```go
store := sessions.NewFilesystemStore(604800, "/tmp/.sess")
app.Push(sessions.New(store, 604800, 300, "id"))
```

中间件会把当前会话挂到 request context 上，处理器里用
`sessions.GetSession(r)` 取：

```go
func handler(w http.ResponseWriter, r *http.Request) {
	s := sessions.GetSession(r)
	s.Store("user", "a@b.com")
}
```

## notes

* 会话没有任何数据时**不落盘**：以前每个请求都写一份 `md5(sid)` 文件，
  包括只会 302 的未登录 `GET /`，不带 Cookie 的脚本能把 `sess_path` 堆满。
  会话被清空（退出登录）时文件会被删掉。
* 过期按**当前配置**算：`MaxAge`/`Expires` 虽然还在文件里，但加载时会用
  store 的 `maxAge` 刷新，调大 `sess_ttl` 重启即生效。
* SID 用 `crypto/rand` 生成（原来是 `math/rand`，可预测）。
* cookie 带 `HttpOnly` / `SameSite=Lax`；`X-Forwarded-Proto: https` 或
  直接 TLS 时加 `Secure`（本地 http 调试不受影响）。
* 会话文件先写临时文件再 rename，读的人不会拿到写了一半的文件。

## stores

* `NewFilesystemStore(maxAge, dir)`：文件系统，目录按 `md5(sid)` 的前两个
  字节分层。
* `NewMemoryStore(maxAge)`：进程内存，重启即失效。
