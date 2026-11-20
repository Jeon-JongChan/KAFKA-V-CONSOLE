// Package web 은 빌드 단계 없는 정적 프론트(HTML + ES 모듈 + vendor min.js)를 바이너리에 내장한다.
package web

import "embed"

// Files 는 web 폴더의 정적 파일 트리다.
//
//go:embed index.html style.css *.js locales vendor
var Files embed.FS
