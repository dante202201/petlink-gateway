package model

import "testing"

func TestResolveAudioStreamKind(t *testing.T) {
	k0, k1 := 0, 1
	if ResolveAudioStreamKind(&k0, -600) != AudioKindEvent {
		t.Fatal("kind=0 → event")
	}
	if ResolveAudioStreamKind(&k1, -45) != AudioKindFile {
		t.Fatal("kind=1 → file")
	}
	// 无 kind：默认事件流（不再用 db==-600）
	if ResolveAudioStreamKind(nil, -600) != AudioKindEvent {
		t.Fatal("nil kind + db=-600 → event（不再当文件）")
	}
	if ResolveAudioStreamKind(nil, -1000) != AudioKindEvent {
		t.Fatal("nil kind + 静音 db → event")
	}
	if ResolveAudioStreamKind(nil, -45) != AudioKindEvent {
		t.Fatal("nil kind → event")
	}
}
