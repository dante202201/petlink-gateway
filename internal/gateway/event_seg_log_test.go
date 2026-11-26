package gateway

import "testing"

func TestFormatSeqList(t *testing.T) {
	if got := formatSeqList(nil, 10); got != "" {
		t.Fatalf("empty=%q", got)
	}
	if got := formatSeqList([]int{3, 1, 2}, 10); got != "1,2,3" {
		t.Fatalf("got=%q", got)
	}
	got := formatSeqList([]int{0, 1, 2, 3, 4}, 3)
	if got != "0,1,2,...(+2)" {
		t.Fatalf("trunc=%q", got)
	}
}

func TestMissingSeqs(t *testing.T) {
	st := &audioKindConnState{segDeviceTotal: 5, segSeqSeen: map[int]struct{}{0: {}, 2: {}, 4: {}}}
	miss := st.missingSeqs()
	if formatSeqList(miss, 10) != "1,3" {
		t.Fatalf("miss=%v", miss)
	}
}
