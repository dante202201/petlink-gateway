package model

// 音频上行 kind（设备 JSON 字段 "kind"）。
const (
	AudioKindEvent = 0 // 事件流：边采边传，叫声实时识别
	AudioKindFile  = 1 // 录音文件：本地录完再拆帧上传（record_now / 自动存档）
)

// ResolveAudioStreamKind 解析音频通路：仅看 kind。
// kind=1 → 录音文件；缺省 / 其它 → 事件流。
// 不再用 db==-600 分流：事件流静音帧 db 可到 -1000，会误分到文件桶。
func ResolveAudioStreamKind(kind *int, _ float64) int {
	if kind != nil && *kind == AudioKindFile {
		return AudioKindFile
	}
	return AudioKindEvent
}

// AudioStreamKindLabel 日志/文档用短名。
func AudioStreamKindLabel(kind int) string {
	if kind == AudioKindFile {
		return "file"
	}
	return "event"
}
