//go:build !windows

// 비-Windows 포터블 빌드: Wails(CGO/WebKit) 없음.
package mode

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
)

// RunDesktop 은 Linux/mac 포터블 빌드에서 창을 띄우지 않는다.
func RunDesktop(assets fs.FS, api http.Handler, brokers string, probe func(context.Context) error) error {
	return fmt.Errorf("이 플랫폼 빌드는 데스크톱 창을 지원하지 않습니다. -console 또는 -listen :8080 을 사용하세요")
}
