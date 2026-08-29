# AGENTS.md

本文件为在本仓库中工作的编码代理提供协作指南。

## 项目概览

`SortImages` 是一个单文件 Go 命令行工具，递归遍历目录树，按文件扩展名把找到的每个常规文件归档到对应格式的桶目录中，并为照片额外生成一份按 EXIF 拍摄日期组织的备份。

**外部依赖**：EXIF 读取交给命令行工具 `exiftool`，不再使用内嵌解析库。`main` 的第一件事就是 `exec.LookPath("exiftool")`，找不到则打印各系统安装方法并 `os.Exit(1)`。选它是因为对 ORF、RW2 等小众 RAW 的覆盖远比 Go 侧的解析库完整（`imagemeta` 完全读不出 ORF）。

`read()` 传给 exiftool 的路径必须先转成绝对路径：`-@` 按行读参数，相对路径若以 `-` 开头（例如 `-sunset.jpg`）会被当成选项，该照片于是静默读不出 EXIF 而退回文件修改时间。路径含换行符同样会破坏按行协议，这类文件直接跳过 EXIF 读取。

exiftool 是 Perl 脚本，单次启动约 70 ms，逐张调用在上千张照片时会浪费一分多钟，因此用官方的 `-stay_open` 常驻进程模式：`exifTool` 类型持有该进程，每张照片通过 stdin 发参数、从 stdout 读到 `{ready<N>}` 为止。

**Go 依赖**：仅 [`tzf`](https://github.com/ringsaturn/tzf)（GPS 经纬度反查 IANA 时区名）。它要求 `go 1.25`，但本仓库 `go.mod` 指定 `go 1.27.0`；内嵌的全球时区边界数据让构建产物约 16 MB。

另外空导入了 `time/tzdata`：Windows 没有系统级 tzdata，缺了它 `time.LoadLocation` 在 Windows 上会失败。

## 常用命令

```bash
go mod download   # 拉取 imagemeta 依赖
go build          # 生成 ./SortImages（已被 gitignore 忽略）
go vet ./...
gofmt -l .        # 列出未格式化的文件
go run . <dir>    # 将 <dir> 下的文件复制到当前工作目录下的分类桶中（只复制，无移动选项）
```

## 本地安装

在仓库根目录执行以下命令，编译并安装当前源码：

```bash
go install .
```

未单独配置 `GOBIN` 时，可执行文件通常会安装到 `$(go env GOPATH)/bin/SortImages`。如需明确安装到该目录，可以执行：

```bash
GOBIN="$(go env GOPATH)/bin" go install .
```

当前仓库没有测试、lint 配置或 CI。

## Git 提交规范

- 提交信息应使用中文，表达简明扼要。
- 每行不超过 30 个中文字。
- 遵循 [Conventional Commits 1.0.0](https://www.conventionalcommits.org/zh-hans/v1.0.0/#%e7%ba%a6%e5%ae%9a%e5%bc%8f%e6%8f%90%e4%ba%a4%e8%a7%84%e8%8c%83) 规范。
- 示例：`feat: 新增哈苏 RAW 格式`。

## 注释规范

代码注释一律使用中文，并遵循 Go 官方文档注释规范 [go.dev/doc/comment](https://go.dev/doc/comment)：

- 只用 `//` 行注释，不用 `/* */` 块注释。
- 文档注释紧贴声明上方，中间不留空行；**首句以被声明者的名字开头**，例如 `// copyFile 将 srcName 复制到 dstName。`
- 注释写成完整句子，使用中文全角标点；代码标识符、扩展名、命令行 flag 保持英文原样。
- `package main` 上方要有包注释。本仓库是一个命令（command），包注释应说明程序做什么以及如何使用；用法示例用缩进写成预格式化块。
- 注释解释**为什么**和**实际行为/边界**，不复述代码字面意思。已知缺陷（例如首个点截断、缺少 `O_TRUNC`）要在相应位置如实写明，不要粉饰。
- 提交前跑 `gofmt -l .`，确保没有被 gofmt 重排的注释格式。

## 架构说明

所有代码都在 [SortImages.go](SortImages.go) 中；`main` 完成初始化后执行一次 `filepath.Walk`。

- **输出建在当前工作目录中，而不是扫描路径旁边。** 两个根目录平级：`sortedRoot`（`./Sorted`，下辖 JPG/RAW/MP4/HEIC/Unknown 五个桶）与 `archiveRoot`（`./Archives`，按拍摄日期分层）。这个工具的预期用法是在目标目录中运行，并把源目录作为参数传入。
- **路由完全依赖扩展名**，通过四个 `map[string]bool` 查找小写后的扩展名（jpeg / raw / mp4 / heic）。程序不会检查文件内容。
- **`Sorted/Unknown` 是所有未匹配文件的兜底目录。** 这不是一个只处理图片的工具；扫描树中的任何文件都会被复制或移动到某个目录。
- **HEIC 系列文件会被重命名**：`.heif` / `.hif` / `.heic` 都会进入 `Sorted/HEIC`，并把扩展名规范化为 `.heic`（见 `strings.Split(filename, ".")[0]` 的改写逻辑；它会在第一个点处分割，因此包含多个点的文件名会被截短）。
- **没有移动模式**。曾经的 `-m` 已移除：它引入过一处删源文件的数据丢失回归，而对一个备份工具来说移动的风险远大于收益。源目录永远不被改动。
- **目标文件名是扁平的 basename**，来自不同源子目录的同名文件会冲突，但由 `placeFile` 统一编号处理，不会互相覆盖。
- **错误处理**：`checkError` 会打印错误并原样返回；复制失败会从 walk 回调返回错误，导致后续遍历全部中止。只有 `filepath.Walk` 自身报出的目录权限错误会被警告并跳过；复制阶段遇到的权限错误不走这条分支，同样会中止遍历。

### Archives 备份树

除五个分类桶外，`JPG`、`RAW`、`HEIC` 三类照片还会额外备份到
`Archives/YYYY/YYYY-MM/YYYY-MM-DD/<原文件名>`：

- 时间由 `shotDate` 决定，顺序为 DateTimeOriginal → CreationDate → CreateDate → **ModifyDate** → 文件修改时间。
- **ModifyDate 单列一档（`dateFromModify`）**：它是「最后一次被软件写入的时间」，即修图时间。经 Photoshop/Lightroom 处理过的照片常常只剩这个标签，直接当拍摄日会归错年份，因此拿到它时必须打 `date=modify` 警告。改这段逻辑时不要把它并回 `dateFromExif`。
- 时区由 `resolveLocation` 按三级链路决定：EXIF 的 `OffsetTimeOriginal` / `OffsetTime` → GPS 经纬度经 `tzf` 反查 → 本机时区 `time.Local`。
- **照片与视频的时间语义相反**，由 `mediaKind` 区分，这是全文件最容易搞错的地方：
  - 照片的 EXIF 时间是**墙上时钟**，用 `inLocation` 把读数原样挂到解析出的时区上。
  - 视频的 QuickTime 时间按规范是 **UTC 绝对瞬间**，用 `.In(loc)` 换算。用错方向会整整差一个时区，跨天的视频会归错日期。
- 视频时区优先取 `QuickTime:TimeZone`（`-n` 下是**分钟数**，+08:00 即 480），再退到 GPS、本机。
- **刻意不用 exiftool 的 `-api QuickTimeUTC`**：它按运行机器的时区做 UTC→本地换算，同一段视频在纽约的机器上会落到前一天。自己拿 TimeZone 标签换算才能跨机器稳定。
- 两条时间来源用法不同，这是最容易看错的地方：
  - **EXIF 时间是墙上时钟**，按规范不含时区。`inLocation` 把读数原样挂到解析出的时区上（不能用 `In`，那会平移读数、把日期算错），所以年月日不随时区变化，跨机器跑结果稳定。
  - **文件修改时间是绝对瞬间**，用 `In` 换算到解析出的时区，年月日会随时区改变。GPS 能反查出时区时，这一步才真正把照片归到拍摄地当天。
- 日期或时区任一项不是从文件本身读出来的，就打印 `[Warning] date=... tz=...`。
- 备份与分类桶都只做复制，源目录不被改动。
- 备份用的是原始文件名，不做 HEIC 那样的扩展名改写，避免多点文件名被截短。
- 目标已存在时绝不覆盖：先比大小、大小相同再逐字节比内容，完全一致才跳过（重复运行幂等）；内容不同则追加 `-1`、`-2` …… 代价是对已备份的照片重跑会完整读两遍文件。
- 落地逻辑统一在 `placeFile`，**Archives 与五个分类桶共用**，改动时注意两边行为必须保持一致。写入走 `copyNewFile`：先写同目录的 `.sortimages-*` 临时文件，`Sync` 之后用 `os.Link` 定名。`os.Link` 遇目标已存在返回 `EEXIST`，因此保留了独占语义（并发写不会交错、不跟随符号链接）；临时文件则保证进程被信号打断时留下的是可识别的临时文件，而不是占着正确名字的半截文件。存在性探测用 `os.Lstat`（不跟随链接）。
- 候选名先经 `fitBaseName` 截短，保证加上 `-N` 后不超过 255 字节；名字不可用时换下一个候选，而不是让一次撞名中止整轮遍历。
- 遍历时会跳过 `Sorted` 和 `Archives` 两棵子树（剪掉 Sorted 就等于剪掉了下面五个桶），用 `os.SameFile` 比较 dev+inode 而**不是**路径字符串——macOS 默认卷大小写不敏感、`/tmp` 之类系统符号链接都会让两侧拼法不同，字符串比较曾因此失效并触发过数据丢失。
- 遍历时直接忽略 `._` 开头的 AppleDouble 文件：它们不是照片，却会跟着原扩展名命中分类规则。
- 备份失败会把错误返回给 walk 回调，中止本轮遍历，并让进程以非零码退出。因为只复制不移动，重跑一次即可补齐。
- 只有 Unknown 一类不备份；MP4 类视频与照片一样会备份。

### 输出

整理结束后 `printArchiveTree` 会把 `Archives` 打印成树。用 `os.ReadDir`（返回已排序）逐层递归，输出确定；不做截断，上万张照片就输出上万行，这是刻意的。

### 新增格式

将小写扩展名添加到 `main` 中对应的 map。如果需要新的分类桶，还要新增匹配的 `os.MkdirAll` 调用，并在 walk 回调的 if/else 链中新增分支。近期提交（`add mov file support`、`feat: 新增哈苏RAW格式`）都是单行 map 增量，可作为参考。

### 失败处理与退出码

- `main` 只把 `run() error` 翻译成退出码，失败一律非零（`0` 成功 / `1` 运行失败 / `2` 未定义选项）。`errReported` 表示信息已打印过，不要重复打印。
- `exifTool.broken`：进程死亡或超时后置位，之后 `read` 一律报错。**不要**把这类错误降级成「这个文件没有 EXIF」——那会让 exiftool 挂掉之后每张照片都被静默按 mtime 归档。
- `read` 有 `exifReadTimeout`（60s）超时，超时杀进程并报错；`Close` 先排空 stdout 再发退出指令，否则子进程可能阻塞在写管道上导致 `Wait` 永不返回。
- `read` 会核对返回记录的 `SourceFile` 与请求路径一致，避免把别的文件的日期安到这张照片上。
- `placeFile` 中 `os.Lstat` 的非 ENOENT 错误要如实上报，**不要** `continue` 换名——那会把权限、IO 故障掩盖成「重名太多」。
- 打印的树只含**本次新增**（`placeFile` 返回实际写入路径，去重时返回空串）。Archives 跨运行累积，全量打印会把历次运行的照片混进来。
