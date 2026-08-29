// SortImages 递归扫描指定目录，按扩展名把其中的文件归档到当前工作目录下的分类桶中。
//
// 用法：
//
//	SortImages <待扫描目录>
//
// 程序先在当前工作目录下建好 Sorted 和 Archives 两个目录，再递归遍历待扫描目录，把遇到的
// 每个常规文件按扩展名复制进 Sorted 下对应的桶：
//
//	Sorted/{JPG,RAW,MP4,HEIC,Unknown}/<原文件名>
//
// 只复制，从不移动：源目录始终保持原样，最坏情况也只是多占一份磁盘，不会丢照片。
//
// Sorted 与 Archives 建在当前工作目录，而不是待扫描目录旁边，因此预期用法是先 cd 到存放
// 结果的目录，再把源目录作为参数传入。
//
// 分类完全依据文件扩展名，从不检查文件内容。凡是没有命中扩展名表的文件一律进入 Unknown，
// 所以这不是一个只处理图片的工具：扫描树里的每个常规文件都会被复制到某个桶里。
//
// 除 Sorted 下的分类桶外，JPG、RAW、HEIC 三类照片和 MP4 类视频还会额外在与 Sorted 平级的
// Archives 下留一份按拍摄日期组织的备份：
//
//	Archives/YYYY/YYYY-MM/YYYY-MM-DD/<原文件名>
//
// 详见 archiveFile。分类桶与 Archives 的重名处理规则完全一致，见 placeFile。
//
// 读取 EXIF 依赖外部命令 exiftool，程序启动时会先确认它在 PATH 中，找不到就打印安装方法
// 并以退出码 1 退出。选它而不是内嵌解析库，是因为它对 ORF、RW2 这类小众 RAW 的覆盖要完整
// 得多；代价是多一个系统依赖。
//
// 归档日期的取值顺序是 DateTimeOriginal、CreationDate、CreateDate、ModifyDate、文件修改
// 时间。注意 ModifyDate 记的是最后一次被软件写入的时间，也就是修图时间：只能拿到它时程序
// 会打印 date=modify 的警告，提醒这一天未必是拍摄日。
//
// 照片与视频的时间语义相反，这是最容易搞错的一点：EXIF 的拍摄时间是拍摄地的墙上时钟，而
// QuickTime 的时间按规范是 UTC。视频因此会先按 UTC 解释，再用 QuickTime 的 TimeZone 标签
// （没有则退到 GPS、本机）换算回拍摄地，这样一段当地 07:30 拍的视频不会被归到前一天。
// 换算刻意不用 exiftool 的 -api QuickTimeUTC：那个选项按运行机器的时区换算，同一段视频在
// 不同机器上会落到不同的日期。
//
// 确定年月日所用的时区依次取自 EXIF 偏移标签（视频是 QuickTime 的 TimeZone）、GPS 定位反查、
// 本机设置。EXIF 拍摄时间是墙上时钟，它的年月日不随运行机器的时区变化；退回文件修改时间时
// 则会变。
//
// 输出目录如果落在待扫描目录内部，遍历时会跳过 Sorted 和 Archives 这两棵子树，避免刚写好
// 的结果被当成新的源文件再处理一遍。macOS 在存储卡上生成的 ._ 开头 AppleDouble 文件同样直接忽略：它们不是
// 照片，却会跟着原文件的扩展名命中分类规则。
//
// 使用前需要知道的几处边界：
//
//   - 目标文件名只保留 basename，不保留源目录层级。不同子目录下的同名文件会落成
//     照片.jpg、照片-1.jpg……绝不互相覆盖，详见 placeFile。
//   - 除 filepath.Walk 自己报出的目录权限错误外，任何一次复制失败都会中止整轮归档，
//     留下归档到一半的结果。因为只复制不移动，直接重跑一次即可补齐。
//   - 任何失败都以非零退出码结束，脚本可以据此判断这次整理是否真的完成。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	// 内嵌一份 IANA 时区库。Windows 没有系统级的 tzdata，缺了这个 time.LoadLocation 在
	// Windows 上会直接失败，GPS 反查出来的时区名就没法用。Linux 与 macOS 有系统库，内嵌
	// 的这份只是兜底，代价是二进制多出几百 KB。
	_ "time/tzdata"

	"github.com/ringsaturn/tzf"
)

// exifToolName 是外部依赖 exiftool 的可执行文件名，程序启动时会先确认它在 PATH 中。
const exifToolName = "exiftool"

// exifToolMissingMessage 在找不到 exiftool 时打印，覆盖三种常见系统的安装方式。
const exifToolMissingMessage = `[Error] SortImages 需要 exiftool 来读取照片的 EXIF 信息，但在 PATH 中没有找到它。

请先安装 exiftool，然后重新运行本程序：

  macOS（Homebrew）
      brew install exiftool

  Ubuntu / Debian Linux
      sudo apt update
      sudo apt install libimage-exiftool-perl

  Omarchy Linux（基于 Arch，用 pacman）
      sudo pacman -S perl-image-exiftool

安装完成后可以用下面的命令确认：

      exiftool -ver
`

// sortedRoot 是五个按格式分类的桶所在的目录，archiveRoot 是按拍摄日期组织的备份树。
// 两者平级，都建在当前工作目录下：
//
//	Sorted/{JPG,RAW,MP4,HEIC,Unknown}/<原文件名>
//	Archives/YYYY/YYYY-MM/YYYY-MM-DD/<原文件名>
const (
	sortedRoot  = "./Sorted"
	archiveRoot = "./Archives"
)

// archiveMaxDedup 限制同名文件的去重尝试次数，避免目录异常时陷入死循环。
const archiveMaxDedup = 1000

// 时区的三个来源，按可信度从高到低排列，仅用于日志提示。
const (
	tzFromExif  = "exif"
	tzFromGPS   = "gps"
	tzFromLocal = "local"
)

// mediaKind 区分静态照片与视频。两者的时间标签语义正好相反，是这段代码里最容易搞错的地方：
//
//   - 照片的 EXIF DateTimeOriginal 按规范是拍摄地的**墙上时钟**，不含时区。
//   - 视频的 QuickTime CreateDate 按规范是 **UTC**，是一个绝对瞬间。
//
// 把视频那个当墙钟用，一段本地时间 07:30 拍的视频（UTC 前一天 23:30）会被归到前一天。
type mediaKind int

const (
	kindPhoto mediaKind = iota
	kindVideo
)

// 拍摄时间的三个来源，可信度依次下降，用于日志提示，也决定要不要打警告。
//
// 特意把 ModifyDate 单列一档：它记的是「文件最后一次被软件写入的时间」，也就是修图或扫描
// 的时间，不是拍摄时间。经 Photoshop、Lightroom 处理过的照片，这个值往往比真实拍摄日晚上
// 几天甚至几年，直接当拍摄日期用会把照片归错到编辑那天。
const (
	dateFromExif   = "exif"   // DateTimeOriginal 或 CreateDate，真正的拍摄时间
	dateFromModify = "modify" // 仅有 ModifyDate 可用，实为编辑时间
	dateFromMtime  = "mtime"  // 连 EXIF 都没有，退回文件系统的修改时间
)

// archiveMaxNameBytes 是常见文件系统（APFS、ext4、NTFS）单个文件名的字节上限。
const archiveMaxNameBytes = 255

// tzFinder 是 GPS 坐标反查时区所用的索引，由 timezoneFinder 惰性构造。
var (
	tzFinderOnce sync.Once
	tzFinderInst tzf.F
	tzFinderErr  error
)

// timezoneFinder 返回全局共享的时区反查索引，只在第一次调用时构造。
//
// tzf 要加载一份覆盖全球的时区边界多边形数据，构造代价不小，因此绝不能每张照片都建一次；
// 又因为多数照片带 EXIF 时区、根本用不上 GPS 反查，所以也不在程序启动时就构造，而是推迟
// 到第一次真的需要反查时。
func timezoneFinder() (tzf.F, error) {
	// sync.Once 保证多次调用只构造一次；即使将来改成并发遍历也不必再加锁。
	tzFinderOnce.Do(func() {
		// NewDefaultFinder 加载 tzf 内置的压缩版边界数据，精度足够且比 NewFullFinder 省内存。
		// 构造失败（数据损坏等）只记在 tzFinderErr 里，由调用方决定退回本机时区，不影响归档。
		tzFinderInst, tzFinderErr = tzf.NewDefaultFinder()
	})
	return tzFinderInst, tzFinderErr
}

// checkError 在 err 非 nil 时向标准输出（而非标准错误）打印一行错误信息，并把 err 原样
// 返回。它只负责记录，既不包装错误也不终止程序，是否处理由调用方决定。
//
// 错误文本经 %s 传入而不是拼进格式串，否则文件名里的 % 会被当作格式动词，把
// 100%sOFF.jpg 打印成 100%!s(MISSING)OFF.jpg。
func checkError(err error) error {
	// nil 是最常见的情况，直接原样返回，让调用方可以写成 err = checkError(err) 的链式风格。
	if err != nil {
		fmt.Printf("[Error] Hit an error! %s\n", err.Error())
	}
	return err
}

// placeFile 把 srcPath 复制进 dir 目录，文件名以 filename 为基准；名字被占用时依次尝试
// filename-1、filename-2 ……
//
// 返回值 placed 是这次真正写出来的路径；若因为目标已存在同样内容而跳过，placed 为空串。
// 调用方据此区分「新写入」和「早就有了」，Archives 树只打印本次新写入的部分。
//
// Archives 备份树和五个分类桶共用这一套判定，规则完全一致：
//
//   - 目标不存在：独占创建并写入。
//   - 目标是普通文件、大小相同且逐字节一致：认为已经放过了，直接跳过。
//   - 目标是普通文件但大小或内容不同：换下一个序号。
//   - 目标是目录或符号链接：同样视为被占用，换下一个序号。用 os.Lstat 探测，不跟随符号
//     链接，否则一个悬空链接会被误判成空位、内容被写到目录之外。
//   - 名字本身不可用（例如加了序号后超出文件名长度上限）：也换下一个序号，不让一次撞名
//     打断整轮整理。
//   - 连续 archiveMaxDedup 个名字都不可用：报错，由调用方中止本轮整理。
//
// 写入一律走 copyNewFile 的 O_EXCL 独占创建，因此「先探测再写入」之间的竞态不会造成两个
// 进程交错写坏同一个文件。本程序只复制、从不移动，源目录始终保持原样。
func placeFile(dir, filename, srcPath string) (placed string, err error) {
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}
	// 拆出主名和扩展名，序号要插在扩展名之前，得到 IMG-1.jpg 而不是 IMG.jpg-1。
	// 对无扩展名的文件 Ext 返回空串，退化成直接在末尾追加序号，同样成立。
	ext := filepath.Ext(filename)
	base := fitBaseName(strings.TrimSuffix(filename, ext), ext)
	for i := 0; i < archiveMaxDedup; i++ {
		// 三种情形互斥：带序号的候选、原名本身超长（否则第 0 轮必然触发 ENAMETOOLONG）、
		// 以及最常见的直接用原名。写成 switch 而不是层层 if，是为了让将来调整任何一支时
		// 不必再去推演它与其他分支的交叠。
		var name string
		switch {
		case i > 0:
			name = fmt.Sprintf("%s-%d%s", base, i, ext)
		case len(filename) > archiveMaxNameBytes:
			name = base + ext
		default:
			name = filename
		}
		candidate := filepath.Join(dir, name)
		dstInfo, statErr := os.Lstat(candidate)
		switch {
		case statErr == nil:
			// 候选路径指向的就是源文件自己——遍历重新进入了自己的输出目录时会出现这种情况。
			// 这时直接返回，什么都不做。os.SameFile 比较 dev+inode，能识别符号链接、硬链接
			// 以及大小写不敏感卷上的不同拼写；早先这里用路径字符串比较，在 macOS 上曾因
			// Work 与 work 被当成两个目录而失效，触发过数据丢失。
			if os.SameFile(dstInfo, srcInfo) {
				return "", nil
			}
			// 名字已被占用。只有当占位的是普通文件且大小一致时，才值得花时间比内容；
			// 目录和符号链接一律直接换名。
			if dstInfo.Mode().IsRegular() && dstInfo.Size() == srcInfo.Size() {
				same, cmpErr := sameContent(srcPath, candidate)
				if cmpErr != nil {
					return "", cmpErr
				}
				if same {
					return "", nil
				}
			}
			continue
		case os.IsNotExist(statErr):
			// 空位：独占写入。另一个进程抢先建好时会返回 EEXIST，退回去按「被占用」重判。
			err = copyNewFile(candidate, srcPath)
			if os.IsExist(err) {
				continue
			}
			if err != nil {
				return "", err
			}
			return candidate, nil
		default:
			// 既不是「不存在」也不是「已存在」，说明是权限、IO 故障这类真实问题。以前这里
			// 一律 continue 换下一个候选，结果是白试 1000 次之后报「重名太多」，把真正的
			// 原因彻底掩盖掉。候选名的长度已经由 fitBaseName 保证，不需要靠换名来绕开。
			return "", fmt.Errorf("stat %s: %w", candidate, statErr)
		}
	}
	return "", fmt.Errorf("too many name collisions when placing %s into %s", srcPath, dir)
}

// exifFloat 承接 exiftool 输出里的数值字段。
//
// 加了 -n 之后 exiftool 一般会把 GPS 坐标写成 JSON 数字，但个别标签损坏时也可能写成字符串
// 甚至 "undef"。直接声明成 float64 会让整条 JSON 记录解析失败、连日期一起丢掉，所以这里
// 自己处理：认得的形式取值并置 valid，认不得的一律当作「没有这个字段」。
type exifFloat struct {
	value float64
	valid bool
}

// UnmarshalJSON 依次按数字、字符串两种形式尝试解析，都不成立时不报错，只是保持 valid 为假。
func (f *exifFloat) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &f.value); err == nil {
		f.valid = true
		return nil
	}
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return nil
	}
	f.value, f.valid = v, true
	return nil
}

// exifFields 是归档真正用得到的那几个 EXIF 标签，字段名与 exiftool -j 的输出一一对应。
//
// 只取这几个而不是全量读取，是为了让 exiftool 少做解析、输出也少一些。
type exifFields struct {
	// SourceFile 用来核对这条记录确实属于刚才请求的那个文件。不校验的话，一旦 exiftool
	// 因为任何原因多返回了一条记录，拿到的就是别的文件的拍摄时间和 GPS，而且因为来源仍标
	// 记为 exif，连警告都不会打——错档是彻底静默的。
	SourceFile         string `json:"SourceFile"`
	DateTimeOriginal   string `json:"DateTimeOriginal"`
	CreateDate         string `json:"CreateDate"`
	ModifyDate         string `json:"ModifyDate"`
	OffsetTimeOriginal string `json:"OffsetTimeOriginal"`
	OffsetTime         string `json:"OffsetTime"`
	// CreationDate 来自 QuickTime，iPhone 等设备会写成带偏移的本地时间，是视频里最可信的
	// 时间来源；写了它就不必再去猜时区。
	CreationDate string `json:"CreationDate"`
	// TimeZone 来自 QuickTime，加了 -n 之后是相对 UTC 的**分钟数**（+08:00 即 480）。
	// 它相当于视频版的 OffsetTimeOriginal，用来把 UTC 的 CreateDate 换算回拍摄地当天。
	TimeZone     exifFloat `json:"TimeZone"`
	GPSLatitude  exifFloat `json:"GPSLatitude"`
	GPSLongitude exifFloat `json:"GPSLongitude"`
}

// exifTool 封装一个常驻的 exiftool 进程。
//
// exiftool 是 Perl 脚本，每次启动都要几十毫秒（本机实测约 70ms）。整理一个上千张照片的目录
// 时，逐张启动会白白耗掉一分多钟，所以这里用官方的 -stay_open 模式：进程只起一次，之后每张
// 照片通过标准输入发一组参数、从标准输出读回结果，单次开销降到毫秒级。
type exifTool struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	seq    int
	// broken 在进程死亡或超时后置位。置位之后 read 一律直接报错，绝不退回「这个文件没有
	// EXIF」——否则 exiftool 一旦挂掉，后面每张照片都会被静默按文件修改时间归档，整批错档
	// 却没有任何提示。
	broken bool
}

// exifReadTimeout 是单个文件等待 exiftool 响应的上限。
//
// exiftool 解析畸形文件的耗时与文件段数成正比，实测一个几十 MB 的畸形 JPEG 就能让它卡上
// 半分钟以上，而在此期间程序没有任何输出，看起来像是死了。超时之后杀掉进程并如实报错，
// 好过无声无息地一直等下去。正常文件的响应在毫秒级，60 秒留了极宽的余量。
const exifReadTimeout = 60 * time.Second

// startExifTool 启动常驻的 exiftool 进程。调用方必须在用完后调用 Close，否则该进程会一直留着。
//
// -@ - 表示「从标准输入按行读取参数」，-stay_open True 表示处理完一批参数后不退出。
func startExifTool() (*exifTool, error) {
	cmd := exec.Command(exifToolName, "-stay_open", "True", "-@", "-")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// exiftool 会把「不认识的文件格式」之类的提示写到标准错误。这些提示对本程序没有意义：
	// 读不出 EXIF 时自然会退回文件修改时间，所以直接丢弃，免得刷屏。
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &exifTool{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}, nil
}

// read 读取单个文件的 EXIF 标签。
//
// 协议是 exiftool 的 -stay_open 约定：参数一行一个写进标准输入，最后一行是 -execute<N>；
// exiftool 处理完会输出结果，再输出一行 {ready<N>} 作为结束标记。序号 N 每次递增，这样即使
// 上一次残留了输出，也不会把它错当成本次的结果。
//
// 文件本身没有 EXIF、或 exiftool 解析失败时不算错误：返回空的 exifFields，由调用方退回文件
// 修改时间。只有管道读写失败（进程已经死掉）才返回错误。
func (e *exifTool) read(path string) (exifFields, error) {
	// 进程已经不可用就直接报错，不要让调用方以为「这个文件只是没有 EXIF」。
	if e.broken {
		return exifFields{}, fmt.Errorf("%s is no longer usable", exifToolName)
	}
	// -@ 按行切分参数，路径里含换行符会被拆成两个参数，读到的就不是这个文件了。这种文件名
	// 在实际拍摄中不会出现，直接当作没有 EXIF，让调用方退回文件修改时间。
	if strings.ContainsAny(path, "\n\r") {
		return exifFields{}, nil
	}
	// 转成绝对路径再交给 exiftool。相对路径若以 - 开头（例如 -sunset.jpg），exiftool 会把它
	// 当成选项而不是文件名——实测会报 Invalid TAG name 然后 No file specified，这张照片于是
	// 读不出任何 EXIF 却没有任何提示。绝对路径必定以路径分隔符开头，不会被误认成选项。
	if abs, absErr := filepath.Abs(path); absErr == nil {
		path = abs
	}
	e.seq++
	// 参数顺序无关紧要，但必须一行一个：-@ 模式按行切分，因此含空格的路径也不需要转义。
	// 路径里若含换行符会破坏这个协议，实际拍摄产生的文件名不会出现这种情况。
	args := []string{
		"-j", // 输出 JSON，便于直接反序列化
		"-SourceFile",
		"-n", // 数值标签输出原始数值，GPS 坐标因此是可直接使用的十进制度
		"-DateTimeOriginal",
		"-CreateDate",
		"-ModifyDate",
		"-OffsetTimeOriginal",
		"-OffsetTime",
		// 限定 QuickTime 组，避免在照片上匹配到同名的其他标签。照片没有这两个标签时
		// exiftool 只是不输出，不算错误。
		"-QuickTime:CreationDate",
		"-QuickTime:TimeZone",
		"-GPSLatitude",
		"-GPSLongitude",
		path,
		fmt.Sprintf("-execute%d", e.seq),
	}
	var request bytes.Buffer
	for _, arg := range args {
		request.WriteString(arg)
		request.WriteByte('\n')
	}
	if _, err := e.stdin.Write(request.Bytes()); err != nil {
		return exifFields{}, err
	}

	// 在单独的 goroutine 里读，好让主流程能在超时后放弃等待。
	marker := fmt.Sprintf("{ready%d}", e.seq)
	type readResult struct {
		payload []byte
		err     error
	}
	done := make(chan readResult, 1)
	go func() {
		var payload bytes.Buffer
		for {
			line, err := e.stdout.ReadString('\n')
			if strings.TrimRight(line, "\r\n") == marker {
				done <- readResult{payload: payload.Bytes()}
				return
			}
			payload.WriteString(line)
			if err != nil {
				// 还没读到结束标记管道就断了，说明 exiftool 进程异常退出。
				done <- readResult{err: fmt.Errorf("%s exited unexpectedly: %w", exifToolName, err)}
				return
			}
		}
	}()

	var payload []byte
	select {
	case r := <-done:
		if r.err != nil {
			e.broken = true
			return exifFields{}, r.err
		}
		payload = r.payload
	case <-time.After(exifReadTimeout):
		// 超时后杀掉进程：这会让上面的 goroutine 拿到 EOF 自行退出，不会泄漏。
		e.broken = true
		_ = e.cmd.Process.Kill()
		return exifFields{}, fmt.Errorf("%s timed out after %s on %s", exifToolName, exifReadTimeout, path)
	}

	// exiftool -j 输出的是一个数组。解析失败、数组为空、或者没有一条记录的 SourceFile 与
	// 请求的路径相符，都当作「这个文件没有可用的 EXIF」——调用方会退回文件修改时间并打警告，
	// 这比把别的文件的日期安到这张照片头上要好得多。
	var records []exifFields
	if err := json.Unmarshal(bytes.TrimSpace(payload), &records); err != nil {
		return exifFields{}, nil
	}
	for _, record := range records {
		if record.SourceFile == path {
			return record, nil
		}
	}
	return exifFields{}, nil
}

// Close 让常驻的 exiftool 进程正常退出，并等待它回收。
func (e *exifTool) Close() error {
	// 先起一个 goroutine 把剩余输出读干净。子进程若还有没被读走的 stdout，管道写满之后它会
	// 阻塞在 write 上，永远读不到下面的退出指令，Wait 也就永远返回不了。
	go func() { _, _ = io.Copy(io.Discard, e.stdout) }()
	// -stay_open False 是约定的退出指令；写失败说明进程已经没了，忽略即可。
	_, _ = io.WriteString(e.stdin, "-stay_open\nFalse\n")
	_ = e.stdin.Close()
	return e.cmd.Wait()
}

// parseExifTime 解析 exiftool 输出的时间字符串，并告知它是否自带时区偏移。
//
// exiftool 的时间格式是 "2006:01:02 15:04:05"，部分标签会再带上偏移或亚秒。标签存在但无有效
// 值时 exiftool 会输出全零的 "0000:00:00 00:00:00"，这种要当作没有值。
func parseExifTime(v string) (t time.Time, hasOffset bool, ok bool) {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "0000") {
		return time.Time{}, false, false
	}
	// 带偏移的格式必须排在前面：Parse 要求整串匹配，先试更长的格式才不会漏掉偏移。
	layouts := []struct {
		layout    string
		hasOffset bool
	}{
		{"2006:01:02 15:04:05.999999999-07:00", true},
		{"2006:01:02 15:04:05-07:00", true},
		{"2006:01:02 15:04:05.999999999", false},
		{"2006:01:02 15:04:05", false},
	}
	for _, l := range layouts {
		if parsed, err := time.Parse(l.layout, v); err == nil {
			return parsed, l.hasOffset, true
		}
	}
	return time.Time{}, false, false
}

// parseOffset 把 "+09:00" 这样的偏移串变成一个固定偏移的时区。
//
// 固定偏移不带夏令时规则，但对「这张照片属于哪一天」来说已经足够：EXIF 记下的偏移就是拍摄
// 当时实际生效的偏移，本来就已经把夏令时算进去了。
func parseOffset(v string) (*time.Location, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	parsed, err := time.Parse("-07:00", v)
	if err != nil {
		return nil, false
	}
	_, offset := parsed.Zone()
	return time.FixedZone(v, offset), true
}

// abs 返回 v 的绝对值，仅用于拼时区名。
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// resolveLocation 决定该用哪个时区来解释这个文件的时间，并返回该时区的来源。
//
// 按可信度依次尝试三种来源：
//
//  1. EXIF 的偏移标签。优先 OffsetTimeOriginal（与 DateTimeOriginal 配对），其次 OffsetTime
//     （与 ModifyDate 配对）。这两个标签是 EXIF 2.31 才加进来的，较老的机身不会写。
//  2. 照片的 GPS 坐标。用 tzf 把经纬度反查成 IANA 时区名，再交给 time.LoadLocation。
//     经纬度同时为零视为没有 GPS；这会把真的落在几内亚湾零点的照片误判，实际拍摄中可忽略。
//  3. 本机时区，即 time.Local。Go 在 Linux 与 macOS 上读 /etc/localtime、在 Windows 上读
//     注册表，无需为不同平台分别处理。
func resolveLocation(f exifFields, kind mediaKind) (*time.Location, string) {
	// 视频优先看 QuickTime 的 TimeZone：它是相机记下的拍摄地偏移，等价于照片的
	// OffsetTimeOriginal，有了它换算结果就不再依赖运行机器的时区。
	if kind == kindVideo && f.TimeZone.valid {
		minutes := int(f.TimeZone.value)
		// 合法偏移在 ±14 小时以内；超出范围说明标签是脏的，宁可退到下一级也不要用。
		if minutes >= -14*60 && minutes <= 14*60 {
			offset := minutes * 60
			return time.FixedZone(fmt.Sprintf("%+03d:%02d", minutes/60, abs(minutes)%60), offset), tzFromExif
		}
	}
	// 两个偏移标签按优先级依次尝试，第一个能解析出来的就用。
	for _, raw := range []string{f.OffsetTimeOriginal, f.OffsetTime} {
		if loc, ok := parseOffset(raw); ok {
			return loc, tzFromExif
		}
	}
	// 没有偏移标签，退而用 GPS 定位反查。
	if f.GPSLatitude.valid && f.GPSLongitude.valid &&
		(f.GPSLatitude.value != 0 || f.GPSLongitude.value != 0) {
		// 反查索引构造失败时直接跳过这一级，落到本机时区，而不是让整张照片归档失败。
		if finder, err := timezoneFinder(); err == nil {
			// 注意 tzf 的参数顺序是先经度后纬度，与习惯的「纬度, 经度」相反。
			if name := finder.GetTimezoneName(f.GPSLongitude.value, f.GPSLatitude.value); name != "" {
				// 拿到的是 IANA 名（如 Pacific/Honolulu），交给 time.LoadLocation 变成带夏令时
				// 规则的 Location。查不到名字时（理论上不会）同样落到本机时区。
				if loc, err := time.LoadLocation(name); err == nil {
					return loc, tzFromGPS
				}
			}
		}
	}
	// 兜底：本机时区。Go 已经屏蔽了平台差异，这里不需要按操作系统分支。
	return time.Local, tzFromLocal
}

// inLocation 把 t 的墙钟数字原样搬到 loc 时区下重新解释，得到的瞬间与原来不同，但年月日
// 时分秒的读数完全一致。
//
// EXIF 的 DateTimeOriginal 按规范记的就是拍摄地的墙上时钟，本身不含时区；不带偏移时
// time.Parse 会把它当成 UTC。用 In 转换会平移读数、把日期算错，所以这里必须重新构造而不是
// 转换。
func inLocation(t time.Time, loc *time.Location) time.Time {
	// 逐个字段取出再重新组装，等价于「把这串读数当作 loc 时区的本地时间」。
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(),
		t.Second(), t.Nanosecond(), loc)
}

// copyNewFile 把 srcName 复制到一个此前并不存在的路径 dstName。
//
// 先写同目录下的临时文件，落盘后再用 os.Link 定名。这样做有两层保障：
//
//   - os.Link 在目标已存在时返回 EEXIST，具备与 O_EXCL 相同的独占语义。两个进程同时整理
//     到同一个目录时不会交错写进同一个文件，也不会跟随符号链接把内容写到目录之外。
//   - 进程被信号打断（Ctrl-C、SIGTERM、崩溃、断电）时，留在磁盘上的是一个点号开头的临时
//     文件，而不是一个截断的半截文件占着正确的名字。直接往目标名写的话，下次运行会因为
//     大小不符而把完整副本写到 -1 后缀上，那个坏文件则永远占着正确的名字不会被修复。
//
// 临时文件在所有返回路径上都会被清理；只有进程被硬杀时才会留下，届时可按 .sortimages- 前缀
// 识别并删除。
func copyNewFile(dstName, srcName string) error {
	src, err := os.Open(srcName)
	if err != nil {
		return err
	}
	defer src.Close()
	// 临时文件必须和目标同目录，否则 os.Link 会因为跨文件系统而失败。
	tmp, err := os.CreateTemp(filepath.Dir(dstName), ".sortimages-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return err
	}
	// Sync 之后再定名：确保被硬链接过去的是已经落盘的完整内容。
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	// 显式 Close 并检查错误，而不是 defer：只在关闭阶段才暴露的写入失败（磁盘写满、网络卷
	// 断开）必须被发现，否则会把一个不完整的文件当成有效备份定名下去。
	if err = tmp.Close(); err != nil {
		return err
	}
	// CreateTemp 建出来的是 0600，改成与其他产物一致的 0644。
	if err = os.Chmod(tmpName, 0644); err != nil {
		return err
	}
	// 定名。目标已存在时返回 EEXIST，由调用方换下一个候选名。
	return os.Link(tmpName, dstName)
}

// fitBaseName 在必要时截短主名，保证追加去重序号和扩展名之后仍不超过文件名长度上限。
//
// 不做这一步的话，一个名字已经接近上限的文件一旦撞名，加上 -1 就会触发 ENAMETOOLONG，
// 而这个错误会从 walk 回调冒出去中止整轮归档。
func fitBaseName(base, ext string) string {
	// 去重序号最长形如 -999，预留 4 个字节。
	budget := archiveMaxNameBytes - len(ext) - 4
	if budget < 1 || len(base) <= budget {
		return base
	}
	// 按字节截断可能把多字节字符切成两半，退到最近的完整 UTF-8 边界。
	cut := base[:budget]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// shotDate 返回 path 的拍摄时间，以及这个时间和它所用时区各自的来源。
//
// 时间按 DateTimeOriginal、CreateDate、ModifyDate 的顺序取第一个能解析出来的。三个都没有、
// 文件没有 EXIF 或 exiftool 读不出来时，退回文件自身的修改时间，dateSource 相应为
// dateFromMtime。连 os.Stat 都失败时返回零值时间，调用方据此判定无法归档。
//
// 视频与照片的时间语义相反，见 mediaKind：照片的 EXIF 时间是墙上时钟，视频的 QuickTime
// 时间是 UTC 绝对瞬间。前者用 inLocation 原样挂时区，后者用 In 换算。
//
// 时区由 resolveLocation 按 EXIF、GPS、本机的顺序决定，两条时间来源的用法并不一样：
//
//   - EXIF 时间是墙上时钟，重新挂到解析出的时区上（见 inLocation），年月日读数不变。
//     换句话说，时区在这条路径上决定的是这个瞬间的确切含义，而不会把照片挪到另一天。
//     唯一的例外是时间串本身就带偏移，那时它已经是一个明确的瞬间，直接采用。
//   - 文件修改时间是一个绝对瞬间，用 In 换算到解析出的时区，年月日会随时区变化。GPS 能
//     反查出时区时，这一步才真正把照片归到拍摄地当天，而不是本机时区的那一天。
func shotDate(tool *exifTool, path string, kind mediaKind) (t time.Time, dateSource string, tzSource string, err error) {
	// 先给出最保守的默认值：读不到任何元数据时就按本机时区解释。
	loc := time.Local
	tzSource = tzFromLocal
	// exiftool 读失败与「这个文件没有 EXIF」必须区分开：前者说明进程已经不可用，若在这里
	// 悄悄退回文件修改时间，后面每张照片都会被按运行当天归档，整批错档却毫无提示。
	fields, readErr := tool.read(path)
	if readErr != nil {
		return time.Time{}, "", tzSource, readErr
	}
	{
		// 日期标签按可信度依次尝试。前面几个是真正的拍摄时间，ModifyDate 只是兜底。
		// 视频把 CreationDate 排在最前：iPhone 等设备把它写成带偏移的本地时间，最不容易出错。
		candidates := []struct {
			raw    string
			source string
		}{
			{fields.DateTimeOriginal, dateFromExif},
			{fields.CreationDate, dateFromExif},
			{fields.CreateDate, dateFromExif},
			{fields.ModifyDate, dateFromModify},
		}
		for _, c := range candidates {
			parsed, hasOffset, ok := parseExifTime(c.raw)
			if !ok {
				continue
			}
			if hasOffset {
				// 时间串自带偏移，本身就是一个明确的瞬间，不需要再去猜时区。
				return parsed, c.source, tzFromExif, nil
			}
			loc, tzSource = resolveLocation(fields, kind)
			if kind == kindVideo {
				// QuickTime 的时间按规范记的是 UTC。parseExifTime 在没有偏移时正是按 UTC
				// 解析的，所以 parsed 已经是正确的绝对瞬间，这里用 In 换算到拍摄地时区，
				// 年月日随之落到当地的那一天。绝不能用 inLocation——那会把 UTC 读数原样
				// 当成本地时间，白白差出一个时区。
				return parsed.In(loc), c.source, tzSource, nil
			}
			// 照片：EXIF 是墙上时钟，把读数原样挂到解析出的时区上。
			return inLocation(parsed, loc), c.source, tzSource, nil
		}
		// 没有可用的拍摄时间，但偏移标签或 GPS 对下面的 mtime 回退依然有价值。
		loc, tzSource = resolveLocation(fields, kind)
	}
	// 回退到文件修改时间。它是绝对瞬间，这里必须用 In 换算，年月日会随 loc 变化——
	// 这正是 GPS 反查在这条路径上真正起作用的地方。
	if info, statErr := os.Stat(path); statErr == nil {
		return info.ModTime().In(loc), dateFromMtime, tzSource, nil
	}
	// 文件既读不出 EXIF 又 Stat 不到，无从判断日期，交给调用方按零值处理。
	return time.Time{}, "", tzSource, nil
}

// sameContent 逐字节比较两个文件的内容是否完全一致。
//
// 只在备份目标已存在且大小相同时才会调用，因此这份额外的读取开销只落在同名冲突这条少见
// 的路径上。代价是：对已经备份过的照片重跑一次，两边文件都会被完整读一遍。之所以不满足
// 于比较大小，是因为同名同大小但内容不同的照片并不罕见，只比大小会把它们当成同一份而
// 静默丢弃，对一个备份功能来说不可接受。
func sameContent(a, b string) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	// 两个同样大小的缓冲区并排读，64 KB 是个折中：足够摊薄系统调用开销，又不至于占太多内存。
	bufA := make([]byte, 64*1024)
	bufB := make([]byte, 64*1024)
	for {
		// 用 ReadFull 而不是 Read：Read 允许只填一部分缓冲区，两边填充长度不同就会误判成
		// 内容不一致。ReadFull 只在真正读到结尾时才少填。
		na, errA := io.ReadFull(fa, bufA)
		nb, errB := io.ReadFull(fb, bufB)
		// 长度不同或内容不同，立刻可以断定两个文件不一样，不必读完剩下的部分。
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false, nil
		}
		// io.ReadFull 在读到结尾时返回 io.EOF 或 io.ErrUnexpectedEOF，两者都表示读完了。
		endA := errA == io.EOF || errA == io.ErrUnexpectedEOF
		endB := errB == io.EOF || errB == io.ErrUnexpectedEOF
		// 只要有一边到头就该收尾：两边同时到头才算内容一致，一边到头一边没到说明长度不同。
		if endA || endB {
			return endA && endB, nil
		}
		// 走到这里说明是真正的读取错误（磁盘故障、文件被删等），如实上报而不是当成「不一致」。
		if errA != nil {
			return false, errA
		}
		if errB != nil {
			return false, errB
		}
	}
}

// archiveFile 把 path 按拍摄日期额外备份到 Archive/YYYY/YYYY-MM/YYYY-MM-DD 下，
// 文件名保持 filename 原样，不做 HEIC 那样的扩展名改写，以免多点文件名被截短。
//
// 调用点排在归档到分类桶之前：备份失败会把错误返回给调用方并中止本轮遍历，这样不会留下
// 「分类桶里已经有了、Archives 却没有备份」的半成品。因为只复制不移动，源目录始终完好，
// 重跑一次即可补齐。
//
// 具体的落地与重名处理交给 placeFile，规则与五个分类桶完全一致：内容一致则跳过（重复运行
// 因此是幂等的），不一致则依次追加 -1、-2 ……全程 O_EXCL 独占创建，绝不覆盖已有文件。
func archiveFile(tool *exifTool, path, filename string, kind mediaKind) (placed string, err error) {
	// 时间和它的两个来源一次拿全，来源只用于下面那句提示。
	t, dateSource, tzSource, err := shotDate(tool, path, kind)
	if err != nil {
		return "", err
	}
	// 零值意味着文件既读不出 EXIF 也 Stat 不到，没有任何依据可以决定放进哪个日期目录。
	if t.IsZero() {
		return "", fmt.Errorf("cannot determine shot date for %s", path)
	}
	if dateSource != dateFromExif || tzSource == tzFromLocal {
		// 只要日期或时区里有一项不是真正的拍摄信息，就提示一句，让使用者知道这张照片落在
		// 哪一天并不完全可信。date=modify 尤其值得注意：那是修图时间，不是拍摄时间。
		fmt.Printf("[Warning] date=%s tz=%s: %s\n", dateSource, tzSource, path)
	}
	// 三层目录都用 t 自己的时区渲染，因此 YYYY-MM-DD 与上层的 YYYY、YYYY-MM 必然自洽。
	dir := filepath.Join(archiveRoot, t.Format("2006"), t.Format("2006-01"), t.Format("2006-01-02"))
	// MkdirAll 对已存在的目录返回 nil，所以每张照片都调用一次也没有额外代价。
	// 这里的错误不能忽略：目录建不出来后面的复制必然失败，早点返回错误信息更清楚。
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	// 先取源文件大小，供下面的去重比对使用，避免在循环里重复 Stat 同一个文件。
	// 落地规则与分类桶完全一致，交给 placeFile。
	return placeFile(dir, filename, path)
}

// printArchiveTree 把本次运行新增的备份打印成树状结构，整理结束后调用。
//
// 只打印本次真正新写进去的文件，而不是整个 Archive 目录。Archive 是跨运行累积的：同一个
// 输出目录整理过多张存储卡之后，全量打印会把历次运行的照片混在一起，让人误以为这次又处理
// 了它们。重复运行时因为全部命中去重，这里会如实显示「本次无新增备份」。
//
// added 里是 placeFile 返回的实际写入路径，形如 ./Archive/2026/2026-08/2026-08-22/DSC01488.ARW。
func printArchiveTree(added []string) {
	fmt.Printf("\n%s\n", archiveRoot)
	if len(added) == 0 {
		fmt.Println("本次无新增备份")
		return
	}
	// 转成相对 archiveRoot 的路径并排序，渲染出来的树才是确定的。
	rel := make([]string, 0, len(added))
	for _, p := range added {
		r, err := filepath.Rel(archiveRoot, p)
		if err != nil {
			// 理论上不会发生；真发生了就退回完整路径，至少信息不丢。
			r = p
		}
		rel = append(rel, filepath.ToSlash(r))
	}
	sort.Strings(rel)
	renderTree(rel, "")
	fmt.Printf("\n本次新增 %d 个备份文件\n", len(added))
}

// renderTree 把一组已排序的相对路径按层级打印成树。
//
// paths 必须已排序，这样同一个目录下的条目一定是连续的，只要顺序扫描、把前缀相同的一段
// 归为一组递归下去即可，不需要先建一棵中间的树结构。
//
// prefix 是这一层每行前面的缩进：父层是最后一项时用空格，否则用竖线，竖线因此能一直连到
// 该分支的最后一个条目。
func renderTree(paths []string, prefix string) {
	for i := 0; i < len(paths); {
		head, _, _ := strings.Cut(paths[i], "/")
		// 把开头同为 head 的路径收成一组，顺便剥掉这一层的前缀留给下一层。
		var children []string
		j := i
		for ; j < len(paths); j++ {
			h, rest, found := strings.Cut(paths[j], "/")
			if h != head {
				break
			}
			if found {
				children = append(children, rest)
			}
		}
		branch, indent := "├── ", "│   "
		if j == len(paths) {
			branch, indent = "└── ", "    "
		}
		fmt.Printf("%s%s%s\n", prefix, branch, head)
		if len(children) > 0 {
			renderTree(children, prefix+indent)
		}
		i = j
	}
}

// errReported 表示错误信息已经打印过了，main 只需据此以非零码退出，不要再重复打印一遍。
var errReported = errors.New("already reported")

// main 调用 run 并把失败翻译成退出码。
//
// 退出码必须可信：整理脚本里常见 `SortImages src && rm -rf src` 这样的写法，任何失败却返回
// 0 都会让后半句照常执行。以前无论遍历中途被中止、还是六个目录一个都建不出来，程序一律
// 返回 0，这是实打实的丢照片路径。
func main() {
	if err := run(); err != nil {
		if !errors.Is(err, errReported) {
			fmt.Printf("[Error] %s\n", err.Error())
		}
		os.Exit(1)
	}
}

// run 执行一次完整的整理，返回非 nil 表示这次运行没有成功完成。
func run() error {
	// 启动的第一件事就是确认 exiftool 在 PATH 中。整个 Archive 备份都依赖它读取拍摄日期，
	// 缺了它每张照片都只能退回文件修改时间，归档结果会大面积失真，所以宁可直接退出并告诉
	// 用户怎么装，也不要装作能正常工作。
	if _, err := exec.LookPath(exifToolName); err != nil {
		fmt.Print(exifToolMissingMessage)
		return errReported
	}
	// 没有任何 flag，但仍然调用 Parse：这样 -h / --help 会打印用法，也为将来加选项留好位置。
	flag.Parse()
	// Args 返回的是解析停止之后剩下的位置参数，正常用法下只有待扫描目录这一个。
	args := flag.Args()
	// 只接受一个位置参数。多给或少给都打印用法并以非零码退出，脚本才能发现参数写错了。
	if len(args) != 1 {
		fmt.Printf("Usage: SortImages [path to scan]\n")
		return errReported
	}
	// 五个分类桶都建在当前工作目录下。建目录失败只打印不处理，程序照常往下走；但只要有文件
	// 需要落进这个缺失的桶，第一次复制就会失败并从 walk 回调返回错误，中止整个遍历，连本可
	// 正常归档的其他类型文件也不再处理。
	// 参数确认无误之后才启动常驻的 exiftool 进程，免得只是打印用法也白起一个进程。
	tool, err := startExifTool()
	if err != nil {
		return fmt.Errorf("failed to start %s: %w", exifToolName, err)
	}
	// 归档结束后让它正常退出。run 只用 return 收尾，因此这个 defer 一定会执行。
	defer func() { _ = tool.Close() }()
	// failed 记录「已经打印过、但不足以中止整轮」的错误，用来决定最终退出码。
	failed := false
	// 0755 让目录对本人可写、对其他人可读，与一般照片库的期望一致。
	// Archives 只是备份树的根，按日期分出来的子目录在 archiveFile 里按需创建。
	// 建目录失败只记录不中止：也许这次扫描根本用不到那个桶。但会影响最终退出码。
	for _, d := range []string{
		filepath.Join(sortedRoot, "JPG"),
		filepath.Join(sortedRoot, "RAW"),
		filepath.Join(sortedRoot, "MP4"),
		filepath.Join(sortedRoot, "HEIC"),
		filepath.Join(sortedRoot, "Unknown"),
		archiveRoot,
	} {
		if checkError(os.MkdirAll(d, 0755)) != nil {
			failed = true
		}
	}
	// 以下四张表是扩展名集合，值恒为 true，并不携带桶信息；命中哪张表决定进哪个桶，由后面的
	// if/else 链决定。键一律为小写，查表前会先对扩展名做 strings.ToLower，因此匹配不区分大
	// 小写。四张表都没命中的文件归入 Unknown。
	var jpegExtensions = make(map[string]bool)
	// map 查不到的键返回零值 false，因此不必显式登记「不属于这一类」的扩展名。
	jpegExtensions[".jpg"] = true
	jpegExtensions[".jpeg"] = true
	// RAW 表按厂商覆盖：.raf 富士，.dng Adobe 通用格式（徕卡、理光宾得等在用），
	// .orf 奥林巴斯，.arw 索尼，.3fr 哈苏，.cr3/.cr2/.crw 佳能，.nef/.nrw 尼康，
	// .rw2/.raw 松下。其中 .raw 并非松下专用，其他设备的原始数据文件也可能用它。
	var rawExtensions = make(map[string]bool)
	rawExtensions[".raf"] = true
	rawExtensions[".dng"] = true
	rawExtensions[".orf"] = true
	rawExtensions[".arw"] = true
	rawExtensions[".3fr"] = true
	rawExtensions[".cr3"] = true
	rawExtensions[".cr2"] = true
	rawExtensions[".crw"] = true
	rawExtensions[".nef"] = true
	rawExtensions[".nrw"] = true
	rawExtensions[".rw2"] = true
	rawExtensions[".raw"] = true
	// 视频表覆盖常见的相机与手机格式：.mp4/.mov 最常见，.m4v 是苹果的变体，.mts/.m2ts 是
	// AVCHD（索尼、松下摄像机），.avi/.mpg/.mpeg/.wmv 是较老的格式，.mkv 常见于第三方录制，
	// .3gp 来自早期手机，.insv 是 Insta360，.lrv/.lrf 是大疆等设备的低码率代理片段。
	var mp4Extensions = make(map[string]bool)
	mp4Extensions[".mp4"] = true
	mp4Extensions[".mov"] = true
	mp4Extensions[".m4v"] = true
	mp4Extensions[".mts"] = true
	mp4Extensions[".m2ts"] = true
	mp4Extensions[".avi"] = true
	mp4Extensions[".mpg"] = true
	mp4Extensions[".mpeg"] = true
	mp4Extensions[".mkv"] = true
	mp4Extensions[".wmv"] = true
	mp4Extensions[".3gp"] = true
	mp4Extensions[".insv"] = true
	mp4Extensions[".lrv"] = true
	mp4Extensions[".lrf"] = true
	// HEIC 表收的是 HEIF 容器的三种常见后缀，.hif 多见于富士机身。归档时统一改名为 .heic，
	// 但程序不检查容器内实际的编码，若不是 HEVC，改名后的扩展名也会名不副实。
	var heicExtensions = make(map[string]bool)
	heicExtensions[".heic"] = true
	heicExtensions[".heif"] = true
	heicExtensions[".hif"] = true
	// 输出目录如果落在待扫描目录内部（例如 cd ~/Pictures/sorted && SortImages ~/Pictures），
	// Walk 会把刚写好的备份和归档件当成新的源文件再处理一遍：Archives 里的备份会被重新
	// 分类进 Sorted，凭空多出一堆副本。这里把两个输出根的 FileInfo 记下来，遍历时整枝剪掉。
	//
	// 用 os.Stat 记下每个目录的 FileInfo，之后靠 os.SameFile 比较 dev+inode，而不是比较路径
	// 字符串。字符串比较在这里靠不住：macOS 默认卷大小写不敏感，Work 与 work 是同一个目录却
	// 是两个字符串；/tmp、/var 这类系统自带的符号链接同样会让两侧拼法不同。一旦剪枝失效，
	// 遍历就会重新进入输出目录，把已经归好的文件当成新源文件再处理一遍。
	// 本次运行真正新写进 Archive 的文件，结束时打印成树。
	var added []string
	var outputDirs []os.FileInfo
	// 只需记住两个根：剪掉 Sorted 就等于剪掉了它下面的五个桶。
	for _, d := range []string{sortedRoot, archiveRoot} {
		if fi, statErr := os.Stat(d); statErr == nil {
			outputDirs = append(outputDirs, fi)
		}
	}
	// filepath.Walk 递归遍历待扫描目录。回调返回非 nil 错误会让整个遍历立即中止，因此任何一
	// 次复制失败都会导致剩余文件不再被处理，只有下面显式识别出的目录权限错误例外。Walk 自身
	// 的返回值被丢弃。
	walkErr := filepath.Walk(args[0], func(path string, info os.FileInfo, err error) error {
		// 这里的 os.IsPermission 只看 filepath.Walk 传入的错误，也就是 Walk 自己 lstat 或读目录
		// 时的失败，典型情况是无权限进入的子目录：警告后跳过，遍历继续。复制阶段遇到的权限错误
		// 不走这条分支，会和其他错误一样中止整个遍历。
		if os.IsPermission(err) {
			fmt.Printf("[Warning] No permission: %s\n", path)
			return nil
		}
		// 其余由 Walk 报出的错误（路径消失、IO 故障等）打印后原样返回，遍历就此中止。
		if err != nil {
			return checkError(err)
		}
		// 碰到自己的输出目录就整棵子树跳过，避免把备份和归档件当成新的源文件。
		if info.IsDir() {
			for _, out := range outputDirs {
				if os.SameFile(info, out) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		// 只处理常规文件；符号链接、设备文件等一律跳过。
		if info.Mode().IsRegular() {
			// 转小写后再查表，DSC001.JPG 与 dsc001.jpg 会走同一个分支。
			// Ext 返回的是最后一个点及其后的部分，无扩展名时为空串，最终落到 Unknown。
			ext := strings.ToLower(filepath.Ext(path))
			// 目标文件名只取 basename，不保留源目录层级；同名冲突由 placeFile 编号处理。
			filename := filepath.Base(path)
			// macOS 在 exFAT/FAT 存储卡上会为文件另外生成一个 ._ 开头的 AppleDouble 资源叉。
			// 它不是照片，却会跟着原文件的扩展名命中分类规则：读不出 EXIF 于是按 mtime 归到
			// 运行当天，HEIC 分支的改名还会把 ._DSC0001.HIF 截成一个名叫 .heic 的隐藏文件。
			// 直接忽略，既不整理也不备份。
			if strings.HasPrefix(filename, "._") {
				return nil
			}
			if jpegExtensions[ext] {
				// 先备份再归档：备份失败要在动分类桶之前就中止，避免留下「桶里有、Archives 没有」的半成品。
				// 备份失败直接返回错误中止遍历，宁可停下，也不要在没有备份的情况下动源文件。
				placed, archiveErr := archiveFile(tool, path, filename, kindPhoto)
				if err = checkError(archiveErr); err != nil {
					return err
				}
				if placed != "" {
					added = append(added, placed)
				}
				_, err = placeFile(filepath.Join(sortedRoot, "JPG"), filename, path)
				// 打印之后再判断，保证错误既被记录也能中止遍历。
				err = checkError(err)
				if err != nil {
					return err
				}
				// 命中即返回，不再往下试其他扩展名表。
				return nil
			} else if rawExtensions[ext] {
				// 备份与归档的顺序、错误处理与上面的 JPG 分支完全一致。
				placed, archiveErr := archiveFile(tool, path, filename, kindPhoto)
				if err = checkError(archiveErr); err != nil {
					return err
				}
				if placed != "" {
					added = append(added, placed)
				}
				_, err = placeFile(filepath.Join(sortedRoot, "RAW"), filename, path)
				err = checkError(err)
				if err != nil {
					return err
				}
				return nil
			} else if mp4Extensions[ext] {
				// 视频与照片一样要备份，只是时间标签按 UTC 解释，见 mediaKind。
				placed, archiveErr := archiveFile(tool, path, filename, kindVideo)
				if err = checkError(archiveErr); err != nil {
					return err
				}
				if placed != "" {
					added = append(added, placed)
				}
				_, err = placeFile(filepath.Join(sortedRoot, "MP4"), filename, path)
				err = checkError(err)
				if err != nil {
					return err
				}
				return nil
			} else if heicExtensions[ext] {
				// 必须在下面改写扩展名之前备份，这样 Archive 里留的是未经截断的原始文件名。
				placed, archiveErr := archiveFile(tool, path, filename, kindPhoto)
				if err = checkError(archiveErr); err != nil {
					return err
				}
				if placed != "" {
					added = append(added, placed)
				}
				// 三种扩展名在此统一改写为 .heic，对本来就是 .heic 的文件同样生效。改写用
				// strings.Split 在第一个点处截断，只保留首段：IMG.001.hif 变成 IMG.heic，
				// "2024.06.01 shot.heif" 会被压成 2024.heic，因此同一目录下的多个文件也可能
				// 撞成同名并互相覆盖。
				filename = strings.Split(filename, ".")[0] + ".heic"
				_, err = placeFile(filepath.Join(sortedRoot, "HEIC"), filename, path)
				err = checkError(err)
				if err != nil {
					return err
				}
				return nil
			} else {
				// 兜底分支：未命中任何扩展名表的文件全部进入 Unknown，其中也包括非图片文件。
				_, err = placeFile(filepath.Join(sortedRoot, "Unknown"), filename, path)
				err = checkError(err)
				if err != nil {
					return err
				}
				return nil
			}
		}
		// 非常规文件（符号链接、设备文件等）走到这里，返回 nil 表示继续遍历。
		return nil
	})
	// 整理结束后把备份树打印出来，方便一眼确认每张照片落到了哪一天。
	// 即使遍历中途被错误中止也照样打印：这时更需要看清已经备份到哪里了。
	printArchiveTree(added)
	// 遍历被中止是硬失败，原样上报；只是个别目录没建出来则已经打印过，用 errReported 收尾。
	if walkErr != nil {
		return walkErr
	}
	if failed {
		return errReported
	}
	return nil
}
