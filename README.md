# Intra

把网站当作本机应用打开的 Windows 桌面程序（Windows 10 / 11，x64）。

- **收藏夹**：每个网站可自定义名称、网址和 DNS 记录（A / CNAME，支持 `*.example.local` 通配符）。打开网站时，该网站自己的记录优先，其次是其他收藏的记录，最后才是系统 DNS。
- **链接打开方式**：设置中的「点击收藏时在新窗口打开」默认开启；关闭后，点击链接菜单中的收藏会在当前窗口打开。回车打开同样遵循此设置，右键菜单可单独选择打开方式。
- **网站证书**：点击链接菜单中小地址栏左侧的盾牌，可查看该站点最近一次上游 TLS 连接的证书、签发者、指纹、校验结果和信任来源。
- **证书信任**：不使用 Windows 系统证书库。HTTPS 只信任内置的 Mozilla 公共根证书（`internal/netx/cacert.pem`，来自 curl.se），以及在「设置 → 证书」中手动添加的 CA（可删除）。
- **配置**：保存在 `%APPDATA%\IntraWebApps\config.json`，浏览器数据（Cookie、登录状态）在 `%LOCALAPPDATA%\IntraWebApps`。exe 本身不会被修改；同一用户同时只运行一个 Intra。可在「设置 → 配置」中以 JSON 导入或导出。

## 工作原理

```
WebView2 ──(--proxy-server)──► 本地代理 127.0.0.1:随机端口 ──► 目标服务器
                                 │  · 按收藏夹记录解析 A / CNAME
                                 │  · 用 Mozilla 根 + 自定义 CA 校验上游证书
                                 └  · 以本次运行生成的内存密钥重新签发给 WebView2
```

WebView2 通过 `--ignore-certificate-errors-spki-list` 只接受这把每次启动随机生成、从不落盘的密钥，
因此系统证书库对 Intra 内的网页不起作用；上游证书校验失败时显示 Intra 自己的错误页。

界面（`ui/index.html`）的标题栏尺寸、字体、颜色、圆角、窗口按钮取自 Codex 桌面版：36px 标题栏、14px 菜单文字、6px 圆角按钮、46px 宽的 Windows 风格窗口按钮。界面统一为白色简约风格。

## 构建

需要 Go 1.25+。

```powershell
./build.ps1 -Version 1.0.0     # 输出 release/Intra.exe
```

`rsrc_windows_amd64.syso`（图标、清单、版本信息）由 `tools/genicon` 从 `assets/icon.svg` 生成并已提交，直接 `go build` 也会带上图标。

运行需要 Microsoft Edge WebView2 运行时：Windows 11 自带；Windows 10 若缺失，程序会提示并打开下载页。

## 调试

- `INTRA_LOG=<文件路径>`：写入日志
- `INTRA_DEVTOOLS=1`：允许在界面层打开开发者工具
