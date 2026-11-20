//go:build windows

// Windows 데스크톱 창(Wails + WebView2).
package mode

import (
	"context"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// RunDesktop 은 WebView2 창으로 UI 를 띄운다.
func RunDesktop(assets fs.FS, api http.Handler, brokers string, probe func(context.Context) error) error {
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := probe(ctx); err != nil {
		log.Printf("경고: 브로커 연결 실패 (%s): %v — UI 설정에서 변경 가능", brokers, err)
	}
	cancel()

	return wails.Run(&options.App{
		Title:            "Kafka V-Console",
		Width:            1280,
		Height:           800,
		MinWidth:         900,
		MinHeight:        600,
		BackgroundColour: &options.RGBA{R: 246, G: 247, B: 249, A: 255},
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: api,
		},
	})
}
