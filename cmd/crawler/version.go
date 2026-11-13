// 版本信息：构建时注入 + Go VCS 元数据读取（参考 tanspec-cli-version 方案）。
//   - 发布构建：go build -ldflags "-X main.version=vX.Y.Z -s -w" -buildvcs=true
//   - 本地构建：version 保持 "dev"，VCS 元数据由 -buildvcs=auto 自动嵌入
//     （git 仓库内构建即可获得 vcs.time；无仓库/源码包时显示 unknown）
package main

import (
	"fmt"
	"runtime/debug"
)

// version 由构建时 -ldflags -X 注入；未注入时为 dev。
var version = "dev"

// vcsTime 从 Go build info 读取当前修订时间（vcs.time）。
// 注意：这是源码修订时间，不是实际编译时刻；没有仓库/元数据时为 unknown。
func vcsTime() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.time" {
			if setting.Value == "" {
				return "unknown"
			}
			return setting.Value
		}
	}
	return "unknown"
}

// VersionString 输出格式：version (commit vcs.time)。
func VersionString() string {
	return fmt.Sprintf("%s (commit %s)", version, vcsTime())
}
