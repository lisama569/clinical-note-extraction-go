package terminology

import "testing"

func TestBuiltinVersions(t *testing.T) {
	r := NewRegistry()
	if got := r.Versions(); len(got) != 2 || got[0] != "v1" || got[1] != "v2" {
		t.Fatalf("期望版本 [v1 v2]，实际 %v", got)
	}
	if r.Latest() != V2 {
		t.Fatalf("期望最新版本 v2，实际 %s", r.Latest())
	}
	if _, err := r.Snapshot("v9"); err == nil {
		t.Fatal("未知版本必须返回错误")
	} else if e, ok := err.(*ErrUnknownVersion); !ok || e.Name != "v9" {
		t.Fatalf("错误类型不符: %#v", err)
	}
}

func TestSnapshotImmutability(t *testing.T) {
	r := NewRegistry()
	v1, err := r.Snapshot(V1)
	if err != nil {
		t.Fatal(err)
	}
	// 快照是不可变的：对注册中心的任何后续操作都不得改变已取快照内容。
	first := v1.Concepts[0]
	_ = r.Latest()
	_, _ = r.Snapshot(V2)
	if v1.Concepts[0] != first || v1.Name != V1 {
		t.Fatal("已取得的版本快照在注册中心再次读取后发生变化")
	}
}

// abbrevMap 返回 缩写->概念ID集合，便于断言。
func abbrevMap(v *Version) map[string]map[string]bool {
	m := map[string]map[string]bool{}
	for _, a := range v.Abbrev {
		if m[a.Abbreviation] == nil {
			m[a.Abbreviation] = map[string]bool{}
		}
		m[a.Abbreviation][a.ConceptID] = true
	}
	return m
}

// TestVersionDifferences 校验升级(v1->v2)带来的术语变更，
// 这些差异正是“版本切换不得产生新旧混合、历史结论可复现”的基础。
func TestVersionDifferences(t *testing.T) {
	r := NewRegistry()
	v1, _ := r.Snapshot(V1)
	v2, _ := r.Snapshot(V2)
	a1, a2 := abbrevMap(v1), abbrevMap(v2)

	// v1 存在、v2 下线的缩写
	for _, ab := range []string{"DM", "CHD", "TB"} {
		if !a1[ab][""] && len(a1[ab]) == 0 {
			t.Fatalf("v1 应含缩写 %s", ab)
		}
		if len(a2[ab]) != 0 {
			t.Fatalf("v2 应已下线缩写 %s，实际 %v", ab, a2[ab])
		}
	}
	// v2 新增 COVID 缩写与 MS 的代谢综合征义项
	if len(a2["COVID"]) != 1 || !a2["COVID"]["COVID19"] {
		t.Fatalf("v2 应新增 COVID->COVID19，实际 %v", a2["COVID"])
	}
	if len(a2["MS"]) != 3 || !a2["MS"]["METABOLIC_SYNDROME"] {
		t.Fatalf("v2 MS 应有三个义项且含代谢综合征，实际 %v", a2["MS"])
	}
	// 稳定概念 ID 跨版本不变
	id1 := map[string]bool{}
	for _, c := range v1.Concepts {
		id1[c.ID] = true
	}
	for _, id := range []string{"DIABETES", "MULTIPLE_SCLEROSIS", "MITRAL_STENOSIS"} {
		if !id1[id] {
			t.Fatalf("v1 缺少稳定概念 %s", id)
		}
		found := false
		for _, c := range v2.Concepts {
			if c.ID == id {
				found = true
				if c.Version != V2 {
					t.Fatalf("概念应标记其来源版本 v2，实际 %s", c.Version)
				}
			}
		}
		if !found {
			t.Fatalf("稳定概念 %s 在 v2 中不应消失", id)
		}
	}
}

// TestConceptTriggersNormalized 触发词必须已归一（大写、无多余空白）。
func TestConceptTriggersNormalized(t *testing.T) {
	r := NewRegistry()
	for _, vn := range r.Versions() {
		v, _ := r.Snapshot(vn)
		for _, c := range v.Concepts {
			for _, tr := range c.Triggers {
				if tr != normalizeTerm(tr) {
					t.Fatalf("[%s] %s 触发词未归一: %q", vn, c.ID, tr)
				}
			}
		}
	}
}
