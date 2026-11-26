package model

import "strings"

// IsPlaceholderBreed breed/pet 占位（未选品种），不算有效绑定档案。
func IsPlaceholderBreed(breed string) bool {
	switch strings.ToLower(strings.TrimSpace(breed)) {
	case "", "0", "1", "4294967295", "ffffffff", "-1":
		return true
	}
	return false
}

// BoundFromProfile 绑定态：pid 与 breed 均有效非空时返回 1，否则 0。
func BoundFromProfile(pid, breed string) int {
	if IsUnboundPetID(pid) || IsPlaceholderBreed(breed) {
		return 0
	}
	return 1
}
