# HTTP契约

所有写入场景的JSON入口拒绝未知字段和尾随内容，最大8MiB。服务返回JSON错误；404为对象不存在，409为版本或生命周期冲突，429为成像容量不足，400为不符合输入契约，504为超时。未知路由由HTTP框架返回404。

| 方法与路径 | 输入及结果 |
|---|---|
| GET /healthz | ready |
| POST /v1/fonts | id,data(base64 TrueType) →201字体元数据；重复id 409 |
| GET /v1/fonts/{id} | 元数据：digest/family/license/glyphs/units_per_em/created；data为null |
| POST /v1/plates | id,title,document →201版面 |
| GET /v1/plates/{id} | 完整稿本历史 |
| PUT /v1/plates/{id} | expected_revision,document →追加稿本 |
| POST /v1/plates/{id}/branch | revision,id,title →201独立版面 |
| POST /v1/plates/{id}/archive | 禁止修订；历史仍可读取 |
| POST /v1/plates/{id}/render?revision=1&format=png | 201成品元数据；format也可为svg |
| POST /v1/plates/{id}/exposures?revision=1&format=png | 202瞬态任务 |
| GET /v1/exposures/{id} | queued/running/succeeded/failed/cancelled |
| DELETE /v1/exposures/{id} | 202取消请求 |
| GET /v1/artifacts/{id} | 图片；ETag为成品字节摘要；X-Scene-Digest为稿本摘要 |
| DELETE /v1/artifacts/{id} | 撤销成品 |
| POST /v1/proofs | id,artifact →201 open校样 |
| PUT /v1/proofs/{id} | state为accepted或rejected，note为说明 |
| GET /v1/plates/{id}/bundle | 封包JSON：稿本历史加引用的字体 |
| POST /v1/bundles | 原样封包；201导入，重名409 |

成品响应中的data为null，避免重复传输二进制；GET成品获得实际内容。If-None-Match精确匹配强ETag时返回304。成功校样可从持久化快照确认，当前版本没有校样列表/详情接口。GET版面返回完整历史，服务没有全局列表接口；字体同样没有列表接口，按标识读取。

图层 texts 数组保存可编辑文字对象：source、font、可选 font_digest、size、origin、letter_spacing、brush、transform。font_digest 提供时必须与字体字节摘要一致，否则400。缺失字形、未知字体、非法字体参数都在写入稿本时以400拒绝，错误文本定位到文字节点。

封包 schema 为 prism-plate-bundle-v2：digest 覆盖 {plate, fonts} 的规范JSON；fonts 为版面全部稿本引用的字体记录。导入逐字体校验 data 摘要，以字体字节重新派生 family/license/glyphs/units_per_em 元数据；引用字体缺失或摘要不符以400拒绝，同标识不同摘要以409拒绝，同标识同摘要自动去重。v1 封包不再接受。
