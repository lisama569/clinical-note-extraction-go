package terminology

import "testing"

func TestRegistrySnapshotSwitch(t *testing.T) {
	v1 := &Dictionary{Version: "v1"}
	v2 := &Dictionary{Version: "v2"}
	reg := NewRegistry(v1)
	reg.Register(v2)

	if got := reg.Snapshot(); got.Version != "v1" {
		t.Fatalf("初始快照应为 v1，得到 %s", got.Version)
	}
	// 切换前取得的快照在切换后仍指向旧版本（不可变、可复现历史结论）。
	old := reg.Snapshot()
	if !reg.Switch("v2") {
		t.Fatal("切换到已注册版本应成功")
	}
	if got := reg.Snapshot(); got.Version != "v2" {
		t.Fatalf("切换后快照应为 v2，得到 %s", got.Version)
	}
	if old.Version != "v1" {
		t.Errorf("已取得的快照被版本切换污染: %s", old.Version)
	}
	if reg.Switch("v9") {
		t.Error("切换到未注册版本应失败")
	}
	if got := reg.Snapshot(); got.Version != "v2" {
		t.Errorf("失败的切换不得改变当前版本，得到 %s", got.Version)
	}
	// 回退
	if !reg.Switch("v1") || reg.Snapshot().Version != "v1" {
		t.Error("回退到 v1 失败")
	}
}
